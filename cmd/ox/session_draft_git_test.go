package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/gitutil"
	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- fixtures -------------------------------------------------------------
//
// Tests built on newDraftLedgerFixture drive REAL git against a REAL bare
// remote. Mocking the push would hide the entire failure class this feature can
// produce (rebase, autostash, index scope, staged-but-uncommitted deletions),
// so for anything that commits or pushes the rule is: real bare remote or it
// didn't happen.
//
// Guards that return BEFORE any git command use newDraftGuardFixture instead,
// so they run in the every-commit `-short` pass rather than only in test-all.

const draftTestSessionID = "ses_01950000-0000-7000-8000-0000000000aa"

// draftLedgerFixture is a project whose ledger is a real clone of a real bare
// remote, registered so resolveLedgerPath() finds it.
type draftLedgerFixture struct {
	projectRoot string
	ledgerPath  string
	barePath    string
}

func newDraftLedgerFixture(t *testing.T) *draftLedgerFixture {
	t.Helper()
	if testing.Short() {
		t.Skip("short: real git operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	barePath, ledgerPath := createBareAndClone(t)

	projectRoot := t.TempDir()
	runGit(t, projectRoot, "init")
	sageoxDir := filepath.Join(projectRoot, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(sageoxDir, "config.json"),
		[]byte(`{"config_version":"2","repo_id":"repo_draft_test"}`), 0644))
	require.NoError(t, config.SaveLocalConfig(projectRoot, &config.LocalConfig{
		Ledger: &config.LedgerConfig{Path: ledgerPath},
	}))

	cacheDir := t.TempDir()
	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("HOME", cacheDir)
	t.Setenv("XDG_CACHE_HOME", cacheDir)

	// isolatePushEnv chdirs into the ledger clone and points SAGEOX_ENDPOINT at
	// an unreachable host so credential refresh cannot rewrite the file://
	// remote out from under us.
	isolatePushEnv(t, ledgerPath)

	return &draftLedgerFixture{projectRoot: projectRoot, ledgerPath: ledgerPath, barePath: barePath}
}

// newDraftGuardFixture is a project + ledger with NO git and NO short-mode
// skip, for the guards that return before any git command runs.
//
// prepareDraftLedgerWrite validates the session NAME first and the ledger
// PATH second, both before touching git, so the two highest-blast-radius
// validations in this feature — the one standing between a malformed name and
// `git rm -r -- sessions` (the whole ledger), and the one standing between a
// mis-derived path and a commit into the user's own product repo — need no
// remote at all. Gating them behind the git fixture kept them out of the
// every-commit run for no benefit.
func newDraftGuardFixture(t *testing.T) *draftLedgerFixture {
	t.Helper()
	projectRoot := t.TempDir()
	ledgerPath := t.TempDir()

	sageoxDir := filepath.Join(projectRoot, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(sageoxDir, "config.json"),
		[]byte(`{"config_version":"2","repo_id":"repo_draft_test"}`), 0644))
	require.NoError(t, config.SaveLocalConfig(projectRoot, &config.LocalConfig{
		Ledger: &config.LedgerConfig{Path: ledgerPath},
	}))
	// A .git marker so validateDraftLedgerPath's "is this a repo" check passes
	// for the positive case; no history is needed because nothing commits.
	require.NoError(t, os.MkdirAll(filepath.Join(ledgerPath, ".git"), 0755))

	return &draftLedgerFixture{projectRoot: projectRoot, ledgerPath: ledgerPath}
}

func (f *draftLedgerFixture) writeDraft(t *testing.T, sessionName string, turnCount int) {
	t.Helper()
	require.NoError(t, lfs.WriteDraftSessionMeta(context.Background(),
		draftLedgerSessionDir(f.ledgerPath, sessionName), lfs.DraftInput{
			SessionName: sessionName,
			SessionID:   draftTestSessionID,
			Username:    "Test Coworker",
			RepoID:      "repo_draft_test",
			AgentID:     "OxDraft",
			AgentType:   "claude-code",
			CreatedAt:   time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			TurnCount:   turnCount,
		}))
}

// publish writes + commits a draft the way the hook does.
func (f *draftLedgerFixture) publish(t *testing.T, sessionName string, turnCount int) {
	t.Helper()
	f.writeDraft(t, sessionName, turnCount)
	require.NoError(t, commitDraftLocally(f.ledgerPath, sessionName))
}

func (f *draftLedgerFixture) push(t *testing.T) {
	t.Helper()
	require.NoError(t, pushLedger(context.Background(), f.ledgerPath))
}

// remoteTree lists every path tracked on the bare remote's default branch.
// Assertions go through here rather than os.Stat on the local worktree: the
// worktree is not the ledger, and a local file proves nothing about what
// teammates will see.
func remoteTree(t *testing.T, barePath string) []string {
	t.Helper()
	out := runGit(t, barePath, "ls-tree", "-r", "--name-only", "HEAD")
	if strings.TrimSpace(out) == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

func gitFsckClean(t *testing.T, barePath string) {
	t.Helper()
	cmd := exec.Command("git", "fsck", "--no-dangling")
	cmd.Dir = barePath
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git fsck reported corruption: %s", string(out))
}

// commitsTouching returns the commit subjects that changed a given path.
func commitsTouching(t *testing.T, dir, path string) []string {
	t.Helper()
	out := runGit(t, dir, "log", "--format=%s", "--", path)
	if strings.TrimSpace(out) == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

// --- privacy invariant ----------------------------------------------------

// TestDraftPublish_NoTurnContentInAnyGitObject is the privacy invariant.
//
// A leak here is unrecoverable: the ledger is shared, and git history is
// forever. The oracle deliberately scans EVERY object in the object database
// rather than the HEAD tree — a staged-then-amended leak is unreachable from
// HEAD but still present, still pushed, and still readable by any teammate.
func TestDraftPublish_NoTurnContentInAnyGitObject(t *testing.T) {
	f := newDraftLedgerFixture(t)
	const sessionName = "2026-01-01T00-00-testuser-OxDraft"

	// Plant transcript-shaped sentinels where a careless implementation would
	// pick them up: the cache raw.jsonl, and the ledger session dir itself.
	cacheDir := filepath.Join(f.ledgerPath, ".sageox", "cache", "sessions", sessionName)
	require.NoError(t, os.MkdirAll(cacheDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(cacheDir, "raw.jsonl"),
		[]byte(`{"type":"header"}`+"\n"+`{"type":"user","content":"OX_SENTINEL_PROMPT_7f3a"}`+"\n"), 0644))

	f.publish(t, sessionName, 2)
	f.publish(t, sessionName, 12) // refresh
	f.push(t)

	// Oracle 1: no object in the entire database contains the sentinel.
	cmd := exec.Command("sh", "-c", "git cat-file --batch-all-objects --batch")
	cmd.Dir = f.barePath
	allObjects, err := cmd.CombinedOutput()
	require.NoError(t, err)
	assert.NotContains(t, string(allObjects), "OX_SENTINEL_PROMPT_7f3a",
		"transcript content reached a git object in the shared ledger")

	// Oracle 2: every commit's tree for this session holds exactly meta.json.
	revs := strings.Split(runGit(t, f.barePath, "rev-list", "--all"), "\n")
	for _, rev := range revs {
		listed := runGit(t, f.barePath, "ls-tree", "-r", "--name-only", rev, "--",
			"sessions/"+sessionName)
		if strings.TrimSpace(listed) == "" {
			continue
		}
		assert.Equal(t, []string{"sessions/" + sessionName + "/meta.json"},
			strings.Split(listed, "\n"), "commit %s staged more than meta.json", rev[:8])
	}

	// Oracle 3: field-level, on the committed blob — not the struct we wrote.
	blob := runGit(t, f.barePath, "show", "HEAD:sessions/"+sessionName+"/meta.json")
	for _, forbidden := range []string{`"title"`, `"summary"`, `"produced_commits"`, `"linked_prs"`} {
		assert.NotContains(t, blob, forbidden, "draft blob carries %s", forbidden)
	}
	assert.Contains(t, blob, `"draft": true`)
	assert.Contains(t, blob, draftTestSessionID)

	gitFsckClean(t, f.barePath)
}

// TestDraftPublish_StagesOnlyMetaEvenWithStrayContent.
//
// Catches routing the draft commit through sessionArtifactsToStage, whose glob
// fallback (*.jsonl / *.html / *.md) fires exactly when the manifest is empty —
// a draft's normal state. Staging a real raw.jsonl as a git blob breaks LFS
// linkage and makes the ledger reject every future push for the whole team.
func TestDraftPublish_StagesOnlyMetaEvenWithStrayContent(t *testing.T) {
	f := newDraftLedgerFixture(t)
	const sessionName = "2026-01-01T00-00-testuser-OxStray"

	f.publish(t, sessionName, 2)

	// Server-authored artifacts land in the draft dir, as a `git pull --rebase`
	// during finalize would produce. These match the *.md / *.jsonl glob that
	// sessionArtifactsToStage falls back to on an empty manifest.
	sessionDir := draftLedgerSessionDir(f.ledgerPath, sessionName)
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, "summary.md"), []byte("SERVER_AUTHORED"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, "notes.jsonl"), []byte(`{"leak":1}`), 0644))

	// The manifest-driven stager must refuse a draft dir outright rather than
	// falling through to that glob.
	assert.Nil(t, sessionArtifactsToStage(sessionDir),
		"sessionArtifactsToStage must return nothing for a draft, not fall through to the glob")

	// A refresh must still stage only meta.json.
	f.writeDraft(t, sessionName, 12)
	require.NoError(t, commitDraftLocally(f.ledgerPath, sessionName))
	f.push(t)

	for _, p := range remoteTree(t, f.barePath) {
		if strings.HasPrefix(p, "sessions/"+sessionName+"/") {
			assert.Equal(t, "sessions/"+sessionName+"/meta.json", p,
				"draft commit staged a non-meta file: %s", p)
		}
	}
}

// TestDraftWrite_RefusesOnceRealTranscriptBytesArePresent.
//
// If raw.jsonl ever appears in the git-tracked session dir, that directory is
// no longer a draft — either a finalize is half-done or something recovered
// bytes into the wrong place. Continuing to stamp draft:true would label a
// directory holding real transcript content as a zero-turn placeholder, and
// every draft-aware consumer (doctor's orphan sweep, the daemon skip, abort)
// would then treat real work as disposable.
func TestDraftWrite_RefusesOnceRealTranscriptBytesArePresent(t *testing.T) {
	f := newDraftLedgerFixture(t)
	const sessionName = "2026-01-01T00-00-testuser-OxBytes"

	f.publish(t, sessionName, 2)
	sessionDir := draftLedgerSessionDir(f.ledgerPath, sessionName)
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, "raw.jsonl"), []byte(`{"type":"user"}`), 0644))

	err := lfs.WriteDraftSessionMeta(context.Background(), sessionDir, lfs.DraftInput{
		SessionName: sessionName, SessionID: draftTestSessionID,
		AgentID: "OxDraft", AgentType: "claude-code", CreatedAt: time.Now(), TurnCount: 12,
	})
	require.ErrorIs(t, err, lfs.ErrDraftDirNotEmpty)

	meta, readErr := lfs.ReadSessionMeta(sessionDir)
	require.NoError(t, readErr)
	assert.Equal(t, 2, meta.TurnCount, "the refused refresh must not have mutated the meta")
}

// TestDraftPublish_EmptyManifestAndNoPointerFiles is the LFS invariant at
// publish time: nothing to upload, nothing to orphan, nothing to reconcile.
func TestDraftPublish_EmptyManifestAndNoPointerFiles(t *testing.T) {
	f := newDraftLedgerFixture(t)
	const sessionName = "2026-01-01T00-00-testuser-OxLfs"
	f.publish(t, sessionName, 2)

	sessionDir := draftLedgerSessionDir(f.ledgerPath, sessionName)
	meta, err := lfs.ReadSessionMeta(sessionDir)
	require.NoError(t, err)
	assert.True(t, meta.IsDraft())
	assert.Empty(t, meta.Files, "a draft must claim no LFS objects")

	entries, err := os.ReadDir(sessionDir)
	require.NoError(t, err)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	assert.Equal(t, []string{"meta.json"}, names,
		"a draft directory must contain exactly meta.json — no raw.jsonl, no .recording.json")
}

// TestSessionsGitignore_ExcludesRecordingMarker.
//
// One line of .gitignore kills an entire kill chain: a .recording.json
// committed into a git-tracked session dir makes the daemon's anti-entropy
// treat it as a recovery opportunity and write REAL transcript bytes onto the
// tracked raw.jsonl, breaking LFS linkage team-wide. Drafts make that directory
// a live working area for the first time, so the marker only has to land once.
func TestSessionsGitignore_ExcludesRecordingMarker(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, lfs.EnsureSessionsGitignore(dir))
	body, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	require.NoError(t, err)
	assert.Contains(t, string(body), ".recording.json")
	assert.Contains(t, string(body), ".needs-summary", "the pre-existing exclusion must survive")
}

// --- supersession ---------------------------------------------------------

// TestSupersedeDraft_PurgesServerAuthoredArtifacts is the scenario the whole
// provisionality invariant exists for.
//
// The SageOx server may summarize a zero-turn draft, write summary.json /
// summary.md, and push them. A finalize-time `git pull --rebase` folds them
// into our working tree. Without a wholesale purge, that fabricated summary of
// an empty session is committed as the finished session's summary and becomes
// permanent, citable team history.
func TestSupersedeDraft_PurgesServerAuthoredArtifacts(t *testing.T) {
	f := newDraftLedgerFixture(t)
	const sessionName = "2026-01-01T00-00-testuser-OxSup"

	f.publish(t, sessionName, 2)
	f.push(t)

	// Another writer (the server) authors artifacts against the draft.
	other := cloneBare(t, f.barePath)
	otherSessionDir := filepath.Join(other, "sessions", sessionName)
	require.NoError(t, os.MkdirAll(otherSessionDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(otherSessionDir, "summary.md"),
		[]byte("SERVER_AUTHORED_SENTINEL"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(otherSessionDir, "summary.json"),
		[]byte(`{"title":"Zero-turn session","files_changed":["SERVER.md"]}`), 0644))
	runGit(t, other, "add", "-A")
	runGit(t, other, "commit", "--no-verify", "-m", "server: summarize draft")
	runGit(t, other, "push")

	// Our finalize pulls those in, then supersedes.
	runGit(t, f.ledgerPath, "pull", "--rebase", "--autostash")
	require.FileExists(t, filepath.Join(f.ledgerPath, "sessions", sessionName, "summary.json"),
		"fixture precondition: the server artifacts must actually be in our tree")

	preservedID, wasDraft, err := supersedeDraftForFinalize(f.ledgerPath, sessionName)
	require.NoError(t, err)
	assert.True(t, wasDraft)
	assert.Equal(t, draftTestSessionID, preservedID,
		"the ses_ id must be read before the purge deletes the file carrying it")

	// Everything provisional is gone from the worktree, INCLUDING the directory
	// itself: an empty leftover directory has no meta.json, so listSessionSessions
	// renders it as a non-draft row that lands in uploadedSessions and displays
	// as "✓ uploaded" for a session that was never uploaded. Callers
	// (CopySessionToLedger, the doctor retry, the daemon stager) all mkdir.
	sessionDir := filepath.Join(f.ledgerPath, "sessions", sessionName)
	assert.NoDirExists(t, sessionDir)

	// The removal is COMMITTED, not left staged. A staged-but-uncommitted
	// deletion is the hazard: finalize can fail at several points it is built
	// to tolerate (LFS upload, meta write, read-only endpoint), and the next
	// unrelated bare `git commit` would sweep that deletion in under the wrong
	// message.
	assert.Empty(t, runGit(t, f.ledgerPath, "status", "--porcelain", "--", "sessions/"),
		"the purge must leave no staged deletion behind")
	subjects := commitsTouching(t, f.ledgerPath, "sessions/"+sessionName)
	require.NotEmpty(t, subjects)
	assert.Contains(t, subjects[0], "session-draft: supersede",
		"the purge must land as its own committed removal")
}

// TestSupersedeDraft_LeavesFinalizedSessionAlone is the negative control.
// Without it, "purge everything always" passes every test above while
// destroying finalized sessions.
func TestSupersedeDraft_LeavesFinalizedSessionAlone(t *testing.T) {
	f := newDraftLedgerFixture(t)
	const sessionName = "2026-01-01T00-00-testuser-OxFinal"

	sessionDir := draftLedgerSessionDir(f.ledgerPath, sessionName)
	require.NoError(t, os.MkdirAll(sessionDir, 0755))
	require.NoError(t, lfs.WriteSessionMetaOnly(sessionDir, &lfs.SessionMeta{
		SessionName: sessionName, SessionID: draftTestSessionID,
		CreatedAt: time.Now(), Title: "real work", EntryCount: 40,
		Files: map[string]lfs.FileRef{"raw.jsonl": {OID: "sha256:abc", Size: 10}},
	}))

	preservedID, wasDraft, err := supersedeDraftForFinalize(f.ledgerPath, sessionName)
	require.NoError(t, err)
	assert.False(t, wasDraft)
	assert.Equal(t, draftTestSessionID, preservedID)
	assert.FileExists(t, filepath.Join(sessionDir, "meta.json"), "a finalized session must never be purged")
}

// TestSupersedeDraft_UnreadableMetaIsFatal.
//
// We cannot tell whether an unreadable meta.json held a ses_ id we would
// rotate. Proceeding would mint a fresh one and 404 every /c/ link already
// circulating, so refusing is the only conservative choice.
func TestSupersedeDraft_UnreadableMetaIsFatal(t *testing.T) {
	f := newDraftGuardFixture(t)
	const sessionName = "2026-01-01T00-00-testuser-OxCorrupt"

	sessionDir := draftLedgerSessionDir(f.ledgerPath, sessionName)
	require.NoError(t, os.MkdirAll(sessionDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, "meta.json"), []byte(`{"draft":tr`), 0644))

	_, _, err := supersedeDraftForFinalize(f.ledgerPath, sessionName)
	require.Error(t, err)
	assert.FileExists(t, filepath.Join(sessionDir, "meta.json"), "a fatal classification must not delete anything")
}

// TestCommitDraftRetraction_RemovesDraftForZeroEntrySession.
//
// A stop that produced no entries never writes a real session. Without an
// explicit retraction the /c/ page claims "in progress" forever AND the ledger
// is left holding a staged deletion that the next unrelated commit sweeps up
// under the wrong message.
func TestCommitDraftRetraction_RemovesDraftForZeroEntrySession(t *testing.T) {
	f := newDraftLedgerFixture(t)
	const sessionName = "2026-01-01T00-00-testuser-OxZero"

	f.publish(t, sessionName, 2)
	f.push(t)
	require.Contains(t, remoteTree(t, f.barePath), "sessions/"+sessionName+"/meta.json")

	_, wasDraft, err := supersedeDraftForFinalize(f.ledgerPath, sessionName)
	require.NoError(t, err)
	require.True(t, wasDraft)
	require.NoError(t, commitDraftRetraction(f.ledgerPath, sessionName))

	assert.NotContains(t, remoteTree(t, f.barePath), "sessions/"+sessionName+"/meta.json",
		"the retraction must reach the remote, not just the local index")
	assert.Empty(t, runGit(t, f.ledgerPath, "status", "--porcelain", "--", "sessions/"),
		"no staged deletion may be left behind to poison the next commit")
	gitFsckClean(t, f.barePath)
}

// --- commit scoping -------------------------------------------------------

// TestCommitDraftLocally_DoesNotSweepCoStagedFiles.
//
// A bare `git commit` writes the WHOLE index. Drafts fire every N turns from
// every agent sharing a ledger clone, so a file another session left staged
// after a failed finalize would routinely ride along under a draft's message —
// and, worse, be pushed by the daemon's draft-push cycle before its LFS blobs
// exist.
func TestCommitDraftLocally_DoesNotSweepCoStagedFiles(t *testing.T) {
	f := newDraftLedgerFixture(t)
	const sessionA = "2026-01-01T00-00-testuser-OxAaaa"
	const sessionB = "2026-01-01T00-00-testuser-OxBbbb"

	// Agent A crashed mid-publish: its meta.json is staged but not committed.
	f.writeDraft(t, sessionA, 2)
	runGit(t, f.ledgerPath, "add", "--sparse", "sessions/"+sessionA+"/meta.json")

	// Agent B publishes.
	f.publish(t, sessionB, 2)

	committed := runGit(t, f.ledgerPath, "log", "-1", "--format=", "--name-only")
	assert.Contains(t, committed, sessionB)
	assert.NotContains(t, committed, sessionA,
		"B's draft commit swept up A's staged file")

	stillStaged := runGit(t, f.ledgerPath, "diff", "--cached", "--name-only")
	assert.Contains(t, stillStaged, sessionA, "A's staged file must remain staged, not be silently committed")
}

// TestCommitDraftLocally_DoesNotPush.
//
// The push is deferred to the daemon's sync cycle on purpose: pushLedger is a
// secret scan plus a credential refresh plus an LFS reconcile plus a 3-attempt
// rebase loop, and paying that on a turn boundary is what this design avoids.
// If someone "helpfully" adds a push here, the agent's turn gets seconds slower
// and this test says so.
func TestCommitDraftLocally_DoesNotPush(t *testing.T) {
	f := newDraftLedgerFixture(t)
	const sessionName = "2026-01-01T00-00-testuser-OxNoPush"

	f.publish(t, sessionName, 2)

	assert.NotContains(t, remoteTree(t, f.barePath), "sessions/"+sessionName+"/meta.json",
		"commitDraftLocally must not push")
	ahead := runGit(t, f.ledgerPath, "rev-list", "--count", "@{upstream}..HEAD")
	assert.Equal(t, "1", ahead, "exactly one unpushed draft commit should be waiting for the daemon")
}

// TestCommitDraftLocally_Idempotent — a refresh with identical counters must
// not produce an empty commit per turn.
func TestCommitDraftLocally_Idempotent(t *testing.T) {
	f := newDraftLedgerFixture(t)
	const sessionName = "2026-01-01T00-00-testuser-OxIdem"

	f.publish(t, sessionName, 2)
	before := commitCount(t, f.ledgerPath)

	require.NoError(t, commitDraftLocally(f.ledgerPath, sessionName))
	assert.Equal(t, before, commitCount(t, f.ledgerPath), "an unchanged draft must not create a commit")
}

// --- crash recovery -------------------------------------------------------

// TestDraftPublish_CrashRecovery walks the publish sequence and kills the
// process after each step, asserting the next invocation converges.
func TestDraftPublish_CrashRecovery(t *testing.T) {
	// Hoisted to the parent so `-short` reports SKIP rather than a PASS whose
	// every subtest silently skipped. A green parent over an empty table reads
	// as coverage in the run developers actually do.
	if testing.Short() {
		t.Skip("short: real git operations")
	}
	const sessionName = "2026-01-01T00-00-testuser-OxCrash"

	t.Run("crash after mkdir, before writing meta", func(t *testing.T) {
		f := newDraftLedgerFixture(t)
		require.NoError(t, os.MkdirAll(draftLedgerSessionDir(f.ledgerPath, sessionName), 0755))

		f.publish(t, sessionName, 2)
		f.push(t)
		assert.Contains(t, remoteTree(t, f.barePath), "sessions/"+sessionName+"/meta.json")
	})

	t.Run("crash after writing meta, before git add", func(t *testing.T) {
		f := newDraftLedgerFixture(t)
		f.writeDraft(t, sessionName, 2)

		// The orphaned meta.json is still the ses_ id carrier.
		id, isDraft, err := lfs.PreservedSessionIDAndDraft(draftLedgerSessionDir(f.ledgerPath, sessionName))
		require.NoError(t, err)
		assert.True(t, isDraft)
		assert.Equal(t, draftTestSessionID, id)

		f.publish(t, sessionName, 3)
		assert.Len(t, commitsTouching(t, f.ledgerPath, "sessions/"+sessionName), 1,
			"the retry must produce exactly one commit, not a duplicate")
	})

	t.Run("crash after git add, before commit", func(t *testing.T) {
		f := newDraftLedgerFixture(t)
		f.writeDraft(t, sessionName, 2)
		runGit(t, f.ledgerPath, "add", "--sparse", "sessions/"+sessionName+"/meta.json")

		f.publish(t, sessionName, 3)
		assert.Len(t, commitsTouching(t, f.ledgerPath, "sessions/"+sessionName), 1)
		assert.Empty(t, runGit(t, f.ledgerPath, "status", "--porcelain", "--", "sessions/"),
			"no staged-but-uncommitted leftovers under sessions/")
	})

	t.Run("crash after commit, before push: the next push lands it", func(t *testing.T) {
		f := newDraftLedgerFixture(t)
		f.publish(t, sessionName, 2)
		require.NotContains(t, remoteTree(t, f.barePath), "sessions/"+sessionName+"/meta.json")

		// This is the normal path — the daemon's cycle pushes it later.
		f.push(t)
		assert.Contains(t, remoteTree(t, f.barePath), "sessions/"+sessionName+"/meta.json")
		assert.Equal(t, "0", runGit(t, f.ledgerPath, "rev-list", "--count", "@{upstream}..HEAD"))
		gitFsckClean(t, f.barePath)
	})

	t.Run("push failed once, retry lands it with no duplicate commit", func(t *testing.T) {
		f := newDraftLedgerFixture(t)
		f.publish(t, sessionName, 2)

		runGit(t, f.ledgerPath, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "gone.git"))
		require.Error(t, pushLedger(context.Background(), f.ledgerPath),
			"fixture precondition: the push must actually fail")
		assert.Len(t, commitsTouching(t, f.ledgerPath, "sessions/"+sessionName), 1,
			"the local commit must survive a push failure")

		runGit(t, f.ledgerPath, "remote", "set-url", "origin", f.barePath)
		f.push(t)
		assert.Contains(t, remoteTree(t, f.barePath), "sessions/"+sessionName+"/meta.json")
		assert.Len(t, commitsTouching(t, f.barePath, "sessions/"+sessionName), 1)
		gitFsckClean(t, f.barePath)
	})
}

// --- concurrency ----------------------------------------------------------

// TestConcurrentDraftPublish_SameSession.
//
// RecordingState is an unlocked load-modify-save, so two Stop hooks can both
// cross the publish threshold. The durable guard has to be the committed
// meta.json, not the in-memory counter. Losses to index.lock contention are
// tolerated (the same convention as TestConcurrentSessionUploads_Parallel);
// what must hold is that the repo is not corrupted and no commit adds the same
// path twice.
func TestConcurrentDraftPublish_SameSession(t *testing.T) {
	f := newDraftLedgerFixture(t)
	const sessionName = "2026-01-01T00-00-testuser-OxRace"

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(turn int) {
			defer wg.Done()
			_ = lfs.WriteDraftSessionMeta(context.Background(),
				draftLedgerSessionDir(f.ledgerPath, sessionName), lfs.DraftInput{
					SessionName: sessionName, SessionID: draftTestSessionID,
					AgentID: "OxDraft", AgentType: "claude-code",
					CreatedAt: time.Now(), TurnCount: turn,
				})
			_ = commitDraftLocally(f.ledgerPath, sessionName)
		}(2 + i)
	}
	wg.Wait()

	adds := 0
	for _, subject := range commitsTouching(t, f.ledgerPath, "sessions/"+sessionName) {
		assert.True(t, strings.HasPrefix(subject, "session-draft: "),
			"unexpected commit subject touching a draft: %q", subject)
		adds++
	}
	assert.GreaterOrEqual(t, adds, 1, "at least one publish must land")

	meta, err := lfs.ReadSessionMeta(draftLedgerSessionDir(f.ledgerPath, sessionName))
	require.NoError(t, err)
	assert.True(t, meta.IsDraft())
	assert.Equal(t, draftTestSessionID, meta.SessionID, "no racer may rotate the id")

	f.push(t)
	gitFsckClean(t, f.barePath)
}

// TestConcurrentDraftPublish_DistinctSessions is the multi-agent monorepo case:
// four agents sharing one ledger clone. Each commit must reference exactly one
// session directory.
func TestConcurrentDraftPublish_DistinctSessions(t *testing.T) {
	f := newDraftLedgerFixture(t)

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			name := fmt.Sprintf("2026-01-01T00-00-testuser-OxM%03d", n)
			_ = lfs.WriteDraftSessionMeta(context.Background(),
				draftLedgerSessionDir(f.ledgerPath, name), lfs.DraftInput{
					SessionName: name,
					SessionID:   fmt.Sprintf("ses_01950000-0000-7000-8000-0000000000%02d", n),
					AgentID:     fmt.Sprintf("OxM%03d", n), AgentType: "claude-code",
					CreatedAt: time.Now(), TurnCount: 2,
				})
			_ = commitDraftLocally(f.ledgerPath, name)
		}(i)
	}
	wg.Wait()

	out := runGit(t, f.ledgerPath, "log", "--format=COMMIT %s", "--name-only", "@{upstream}..HEAD")
	var current string
	seen := map[string]map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "COMMIT ") {
			current = strings.TrimPrefix(line, "COMMIT ")
			seen[current] = map[string]bool{}
			continue
		}
		// sessions/.gitignore is shared infrastructure every draft commit may
		// legitimately touch — it is not a session directory.
		if line == "" || current == "" || !strings.HasPrefix(line, "sessions/") ||
			line == "sessions/.gitignore" {
			continue
		}
		parts := strings.Split(line, "/")
		if len(parts) > 1 {
			seen[current][parts[1]] = true
		}
	}
	for subject, dirs := range seen {
		assert.LessOrEqual(t, len(dirs), 1,
			"commit %q touched %d session dirs; draft commits must be pathspec-scoped to one", subject, len(dirs))
	}

	f.push(t)
	gitFsckClean(t, f.barePath)
}

// --- guarded index mutation (P0 regressions) ------------------------------

// TestDraftWrite_RefusesDuringRebase is the highest-value test in this file.
//
// It pins the team-wide data-destruction bug: during a conflicted rebase,
// `git add` MARKS THE CONFLICT RESOLVED and `git commit -- <pathspec>` SUCCEEDS
// on the detached rebase HEAD, consuming the replay step. The next
// `rebase --continue` reports "Successfully rebased" and the commit being
// replayed — a teammate's finalize, a murmur, a plan — is silently gone.
//
// Draft writes are the first ledger index writer that deliberately does not
// push, so they miss the IsSafeForGitOps check that rides along with
// PushWithRetry for every other writer.
//
// Red-first check: delete the assertLedgerSafeForDraftWrite call from
// prepareDraftLedgerWrite and this test fails by LOSING the local commit.
func TestDraftWrite_RefusesDuringRebase(t *testing.T) {
	f := newDraftLedgerFixture(t)
	const sessionName = "2026-01-01T00-00-testuser-OxRbse"

	// Build a real divergence on a path both sides touch, so the rebase
	// genuinely conflicts rather than fast-forwarding.
	conflictPath := filepath.Join(f.ledgerPath, "sessions", sessionName, "meta.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(conflictPath), 0755))

	other := cloneBare(t, f.barePath)
	otherPath := filepath.Join(other, "sessions", sessionName, "meta.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(otherPath), 0755))
	require.NoError(t, os.WriteFile(otherPath, []byte(`{"session_name":"remote-side"}`), 0644))
	runGit(t, other, "add", "-A")
	runGit(t, other, "commit", "--no-verify", "-m", "remote side")
	runGit(t, other, "push")

	require.NoError(t, os.WriteFile(conflictPath, []byte(`{"session_name":"local-side"}`), 0644))
	runGit(t, f.ledgerPath, "add", "-A")
	runGit(t, f.ledgerPath, "commit", "--no-verify", "-m", "LOCAL_SIDE_MUST_SURVIVE")
	localSHA := runGit(t, f.ledgerPath, "rev-parse", "HEAD")

	// Start a rebase and let it stop on the conflict.
	runGit(t, f.ledgerPath, "fetch", "origin")
	rebase := exec.Command("git", "-C", f.ledgerPath, "rebase", "origin/"+currentBranch(t, f.ledgerPath))
	_ = rebase.Run() // expected to fail with a conflict
	require.True(t, gitutil.IsRebaseInProgress(f.ledgerPath),
		"fixture precondition: the rebase must actually be stopped mid-replay")

	// A Stop hook fires now. It must refuse.
	err := commitDraftLocally(f.ledgerPath, sessionName)
	require.ErrorIs(t, err, errDraftUnsafeLedger,
		"a draft write during a rebase must refuse, not silently consume the replay step")

	// The replay is still pending, and the commit being replayed still exists.
	assert.True(t, gitutil.IsRebaseInProgress(f.ledgerPath), "the rebase must be untouched")
	assert.Contains(t, runGit(t, f.ledgerPath, "cat-file", "-t", localSHA), "commit",
		"the commit being replayed must still exist")

	// Every other draft git path must refuse too — they are all index writers.
	_, purgeErr := deleteDraftFromLedger(f.ledgerPath, sessionName)
	assert.Error(t, purgeErr, "deleteDraftFromLedger must not mutate the index mid-rebase")
	assert.ErrorIs(t, purgeDraftSessionDir(f.ledgerPath, sessionName), errDraftUnsafeLedger)
}

func currentBranch(t *testing.T, dir string) string {
	t.Helper()
	return runGit(t, dir, "rev-parse", "--abbrev-ref", "HEAD")
}

// TestDraftCommit_RefusesWhenWorktreeChangedAfterStaging.
//
// `git commit -- <pathspec>` commits the WORKING TREE at those paths, not the
// index that was just built. A concurrent finalize rewrites this exact
// meta.json, so without a re-check the finalize's bytes — LFS OIDs and all —
// land under a "session-draft:" subject. The daemon's push filter keys on that
// subject, so it would then push a finalize-shaped commit with neither the LFS
// reconcile nor the pre-push secret gate the CLI applies.
//
// Red-first check: delete assertDraftStillStaged and the committed blob becomes
// the finalize's content.
func TestDraftCommit_RefusesWhenWorktreeChangedAfterStaging(t *testing.T) {
	f := newDraftLedgerFixture(t)
	const sessionName = "2026-01-01T00-00-testuser-OxSwap"

	f.writeDraft(t, sessionName, 2)
	metaPath := filepath.Join(draftLedgerSessionDir(f.ledgerPath, sessionName), "meta.json")
	runGit(t, f.ledgerPath, "add", "--sparse", "--", "sessions/"+sessionName+"/meta.json")

	// A finalize lands between `git add` and `git commit`.
	require.NoError(t, os.WriteFile(metaPath,
		[]byte(`{"session_name":"`+sessionName+`","session_id":"`+draftTestSessionID+
			`","files":{"raw.jsonl":{"oid":"sha256:deadbeef","size":10}}}`), 0644))

	err := assertDraftStillStaged(f.ledgerPath, sessionName,
		draftStagePaths(sessionName))
	require.Error(t, err, "the pre-commit re-check must catch a worktree swap")

	// And the full publish path must refuse rather than commit foreign bytes.
	require.Error(t, commitDraftLocally(f.ledgerPath, sessionName))
	assert.Empty(t, commitsTouching(t, f.ledgerPath, "sessions/"+sessionName),
		"no commit may be created from bytes another writer owns")
}

// TestDraftSessionName_RejectsPathTraversal.
//
// draftSessionRelDir("") is "sessions" and filepath.Base("") is ".", so an
// empty or traversal-shaped name turns the purge and the abort-delete into
// `git rm -r --force -- sessions` plus os.RemoveAll of the ENTIRE sessions
// tree. That was reachable only by accident before (the paths first require a
// draft meta.json to exist), which is not a guarantee.
func TestDraftSessionName_RejectsPathTraversal(t *testing.T) {
	for _, name := range []string{"", ".", "..", "../evil", "a/b", `a\b`, "foo/../.."} {
		t.Run("name="+name, func(t *testing.T) {
			require.Error(t, validateDraftSessionName(name))
		})
	}
	require.NoError(t, validateDraftSessionName("2026-01-01T00-00-testuser-OxOk01"))
}

// TestDraftSessionName_GuardIsWiredIntoEveryWriter proves the validation is not
// merely defined but actually reached — a guard nobody calls is decoration.
func TestDraftSessionName_GuardIsWiredIntoEveryWriter(t *testing.T) {
	f := newDraftGuardFixture(t)

	// Sanity: the whole sessions tree exists and must survive every call below.
	sessionsDir := filepath.Join(f.ledgerPath, "sessions")
	require.NoError(t, os.MkdirAll(sessionsDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(sessionsDir, "canary.txt"), []byte("keep me"), 0644))

	for _, bad := range []string{"", ".", "../evil"} {
		assert.Error(t, commitDraftLocally(f.ledgerPath, bad), "commitDraftLocally(%q)", bad)
		assert.Error(t, purgeDraftSessionDir(f.ledgerPath, bad), "purgeDraftSessionDir(%q)", bad)
		_, err := deleteDraftFromLedger(f.ledgerPath, bad)
		assert.Error(t, err, "deleteDraftFromLedger(%q)", bad)
	}

	assert.FileExists(t, filepath.Join(sessionsDir, "canary.txt"),
		"a malformed session name must never widen a pathspec to the whole sessions tree")
}

// TestResolveDraftLedgerPath_RefusesNonLedgerTargets.
//
// deriveLedgerPath returns filepath.Dir for ANY path whose parent is named
// "sessions" — which includes the XDG cache and the legacy in-repo fallback,
// where the derived "ledger" is the USER'S OWN PROJECT ROOT. Its prior callers
// only built IPC payloads, so a wrong answer was inert. This is the first
// caller that runs `git commit`, and committing a placeholder into someone's
// product repo would be an unrecoverable trust failure.
func TestResolveDraftLedgerPath_RefusesNonLedgerTargets(t *testing.T) {
	f := newDraftGuardFixture(t)

	// The real ledger resolves.
	realSession := filepath.Join(f.ledgerPath, "sessions", "2026-01-01T00-00-testuser-OxReal")
	assert.Equal(t, f.ledgerPath, resolveDraftLedgerPath(f.projectRoot, realSession))

	// A session path inside the user's own project does NOT.
	inRepo := filepath.Join(f.projectRoot, "sessions", "2026-01-01T00-00-testuser-OxRepo")
	assert.Empty(t, resolveDraftLedgerPath(f.projectRoot, inRepo),
		"a path deriving to the user's project root must never be treated as a ledger")

	// Nor does an unrelated XDG-shaped cache path.
	xdg := filepath.Join(t.TempDir(), "sessions", "2026-01-01T00-00-testuser-OxXdg")
	assert.Empty(t, resolveDraftLedgerPath(f.projectRoot, xdg))
}

// --- end-to-end lifecycle -------------------------------------------------

// TestDraftLifecycle_EndToEnd_ManifestMatchesTree.
//
// Every other test in this file exercises one stage. This one walks the whole
// arc a real session takes — publish, refresh, supersede, finalize, push — and
// asserts the terminal state through a FRESH clone, which is what teammates
// actually see.
//
// The load-bearing assertion is the conservation law:
//
//	set(meta.Files) ∪ {meta.json} == set(git tree for that session)
//
// It catches drift in BOTH directions, which no single-sided assertion does:
//   - a manifest entry with no blob in the tree means the ledger's pre-receive
//     hook starts rejecting pushes with "LFS objects are missing", for everyone;
//   - a blob in the tree with no manifest entry is a stowaway — exactly the
//     server-authored draft-era artifact this feature has to purge.
//
// LFS transport is deliberately not exercised (that is internal/lfs's job and
// it has its own batch-server harness). What IS exercised is the part drafts
// put at risk: staging scope, pointer-vs-content on the tracked path, id
// stability across the purge, and the shape of the final commit.
func TestDraftLifecycle_EndToEnd_ManifestMatchesTree(t *testing.T) {
	f := newDraftLedgerFixture(t)
	const sessionName = "2026-01-01T00-00-testuser-OxE2E1"

	// --- turns 2 and 12: publish, then refresh ---
	f.publish(t, sessionName, 2)
	f.publish(t, sessionName, 12)
	f.push(t)
	require.Contains(t, remoteTree(t, f.barePath), "sessions/"+sessionName+"/meta.json")

	// The server summarizes the zero-turn placeholder and pushes.
	other := cloneBare(t, f.barePath)
	otherDir := filepath.Join(other, "sessions", sessionName)
	require.NoError(t, os.MkdirAll(otherDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(otherDir, "summary.md"),
		[]byte("SERVER_AUTHORED_SENTINEL"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(otherDir, "summary.json"),
		[]byte(`{"title":"Zero-turn session","files_changed":[{"path":"SERVER.md"}]}`), 0644))
	// An artifact our finalize does NOT produce. This is the case the ADR's
	// "wholesale purge, never a whitelist" rule exists for: a file the server
	// invents later, which no overwrite can displace. Without the purge it
	// survives into the finalized session as a tree blob with no manifest
	// entry — and it is the ONLY server file here that the conservation law
	// can catch, because finalize overwrites the other two by name.
	require.NoError(t, os.WriteFile(filepath.Join(otherDir, "summary.html"),
		[]byte("<h1>SERVER_INVENTED_ARTIFACT</h1>"), 0644))
	runGit(t, other, "add", "-A")
	runGit(t, other, "commit", "--no-verify", "-m", "server: summarize draft")
	runGit(t, other, "push")
	runGit(t, f.ledgerPath, "pull", "--rebase", "--autostash")
	require.FileExists(t, filepath.Join(f.ledgerPath, "sessions", sessionName, "summary.html"),
		"fixture precondition: the server-invented artifact must really be in our tree")

	// --- session stop: supersede, then write the real recording ---
	preservedID, wasDraft, err := supersedeDraftForFinalize(f.ledgerPath, sessionName)
	require.NoError(t, err)
	require.True(t, wasDraft)
	require.Equal(t, draftTestSessionID, preservedID,
		"the id must be read before the purge deletes the file carrying it")

	sessionDir := draftLedgerSessionDir(f.ledgerPath, sessionName)
	require.NoError(t, os.MkdirAll(sessionDir, 0755))
	rawBody := `{"type":"header","metadata":{"session_id":"` + draftTestSessionID + `"}}` + "\n" +
		`{"ts":"2026-01-01T00:05:00Z","type":"user","content":"OX_E2E_TRANSCRIPT_SENTINEL"}` + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, "raw.jsonl"), []byte(rawBody), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, "summary.md"), []byte("OUR REAL SUMMARY"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, "summary.json"),
		[]byte(`{"title":"Real work","files_changed":[{"path":"real.go"}]}`), 0644))

	// The manifest the finalize would produce (OIDs stand in for a real upload).
	refs := map[string]lfs.FileRef{
		"raw.jsonl":  lfs.NewFileRef([]byte(rawBody)),
		"summary.md": lfs.NewFileRef([]byte("OUR REAL SUMMARY")),
	}
	finalMeta := &lfs.SessionMeta{
		Version: "1.0", SessionName: sessionName,
		SessionID: session.ResolveOrMintSessionID(preservedID, ""),
		AgentID:   "OxDraft", AgentType: "claude-code",
		CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Title:     "Real work", EntryCount: 1, Files: refs,
	}
	require.NoError(t, lfs.WriteSessionMetaOnly(sessionDir, finalMeta))
	registerGitArtifactInMeta(sessionDir, "summary.json", 64)

	require.NoError(t, commitAndPushLedger(f.ledgerPath, sessionName))
	// Content becomes pointers only after the push succeeds.
	_, err = lfs.WritePointerFiles(sessionDir, lfs.AssertUploadedManifest(refs))
	require.NoError(t, err)
	require.NoError(t, commitPointerRewriteAndPush(f.ledgerPath, sessionName,
		[]string{filepath.Join("sessions", sessionName, "raw.jsonl"),
			filepath.Join("sessions", sessionName, "summary.md")}))

	// --- assert the terminal state through a FRESH clone ---
	fresh := cloneBare(t, f.barePath)
	freshDir := filepath.Join(fresh, "sessions", sessionName)

	blob := runGit(t, f.barePath, "show", "HEAD:sessions/"+sessionName+"/meta.json")
	assert.NotContains(t, blob, `"draft"`, "the finalized meta must carry no draft key at all")
	assert.Contains(t, blob, draftTestSessionID, "the ses_ id must be stable across the whole arc")

	freshMeta, err := lfs.ReadSessionMeta(freshDir)
	require.NoError(t, err)
	assert.False(t, freshMeta.IsDraft())
	assert.Zero(t, freshMeta.TurnCount, "draft counters must not survive")
	assert.Nil(t, freshMeta.UpdatedAt)

	// The server-authored draft-era summary must be gone, ours in its place.
	summaryMD, err := os.ReadFile(filepath.Join(freshDir, "summary.md"))
	require.NoError(t, err)
	assert.NotContains(t, string(summaryMD), "SERVER_AUTHORED_SENTINEL",
		"a summary of the zero-turn placeholder must never survive into the finalized session")

	// raw.jsonl on the tracked path is a POINTER, never the transcript.
	assert.True(t, lfs.IsPointerFile(filepath.Join(freshDir, "raw.jsonl")),
		"the git-tracked raw.jsonl must be an LFS pointer")
	pointerBody, err := os.ReadFile(filepath.Join(freshDir, "raw.jsonl"))
	require.NoError(t, err)
	assert.NotContains(t, string(pointerBody), "OX_E2E_TRANSCRIPT_SENTINEL")

	// THE CONSERVATION LAW.
	inTree := map[string]bool{}
	for _, p := range remoteTree(t, f.barePath) {
		if rel, ok := strings.CutPrefix(p, "sessions/"+sessionName+"/"); ok {
			inTree[rel] = true
		}
	}
	expected := map[string]bool{"meta.json": true}
	for name := range freshMeta.Files {
		expected[name] = true
	}
	assert.Equal(t, expected, inTree,
		"every manifest entry must exist in the tree and every tree blob must be in the manifest")

	gitFsckClean(t, f.barePath)
}

// TestDraftLifecycle_RefreshAfterFinalizeCannotResurrectDraft.
//
// Finalize is the ABSORBING state. A refresh that lands afterward — a Stop hook
// that was already in flight when the session stopped — must not stamp
// draft:true back onto a finished session. If it did, the /c/ page would revert
// to "in progress" permanently, doctor would refuse to repair the session, and
// the orphan reaper would eventually offer to delete a real recording's
// directory.
func TestDraftLifecycle_RefreshAfterFinalizeCannotResurrectDraft(t *testing.T) {
	f := newDraftLedgerFixture(t)
	const sessionName = "2026-01-01T00-00-testuser-OxAbsrb"

	f.publish(t, sessionName, 2)
	_, wasDraft, err := supersedeDraftForFinalize(f.ledgerPath, sessionName)
	require.NoError(t, err)
	require.True(t, wasDraft)

	// Finalize writes the real session.
	sessionDir := draftLedgerSessionDir(f.ledgerPath, sessionName)
	require.NoError(t, os.MkdirAll(sessionDir, 0755))
	require.NoError(t, lfs.WriteSessionMetaOnly(sessionDir, &lfs.SessionMeta{
		Version: "1.0", SessionName: sessionName, SessionID: draftTestSessionID,
		CreatedAt: time.Now(), Title: "Real work", EntryCount: 40,
		Files: map[string]lfs.FileRef{"raw.jsonl": {OID: "sha256:abc", Size: 10}},
	}))

	// A late refresh arrives.
	err = lfs.WriteDraftSessionMeta(context.Background(), sessionDir, lfs.DraftInput{
		SessionName: sessionName, SessionID: draftTestSessionID,
		AgentID: "OxDraft", AgentType: "claude-code", CreatedAt: time.Now(), TurnCount: 12,
	})
	require.ErrorIs(t, err, lfs.ErrNotDraft,
		"a finished session must refuse to be downgraded back to a placeholder")

	meta, readErr := lfs.ReadSessionMeta(sessionDir)
	require.NoError(t, readErr)
	assert.False(t, meta.IsDraft())
	assert.Equal(t, "Real work", meta.Title, "the finalized meta must be untouched")
	assert.Equal(t, 40, meta.EntryCount)

	// And the full publish path is a no-op too, not a commit.
	before := commitCount(t, f.ledgerPath)
	assert.False(t, publishDraftPlaceholder(f.projectRoot, f.ledgerPath, sessionName,
		&session.RecordingState{
			AgentID: "OxDraft", SessionID: draftTestSessionID, TurnCount: 12,
			SessionPath: sessionDir,
		}),
		"publishDraftPlaceholder must report failure rather than resurrect the draft")
	assert.Equal(t, before, commitCount(t, f.ledgerPath), "no commit may be produced")
}
