package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sessionScopedID derives a distinct, valid ses_ id per session name.
//
// Stamping one shared id on every fixture session put two sessions with the
// same ses_ id in one ledger — a state production cannot produce, and one that
// would mask an id-collision bug rather than expose it.
func sessionScopedID(sessionName string) string {
	h := sha256.Sum256([]byte(sessionName))
	return fmt.Sprintf("ses_01950000-0000-7000-8000-%012x", h[:6])
}

// finalizedLedgerSession writes a NON-draft session into the ledger.
func finalizedLedgerSession(t *testing.T, ledgerPath, sessionName string) string {
	t.Helper()
	dir := filepath.Join(ledgerPath, "sessions", sessionName)
	require.NoError(t, os.MkdirAll(dir, 0755))
	require.NoError(t, lfs.WriteSessionMetaOnly(dir, &lfs.SessionMeta{
		SessionName: sessionName, SessionID: sessionScopedID(sessionName),
		CreatedAt: time.Now(), Title: "real work", EntryCount: 40,
		Files: map[string]lfs.FileRef{"raw.jsonl": {OID: "sha256:abc", Size: 10}},
	}))
	return dir
}

// draftLedgerSession writes a DRAFT session into the ledger (no git).
//
// UpdatedAt is set because production ALWAYS sets it (WriteDraftSessionMeta).
// Omitting it produced a shape the code can never emit — and specifically the
// one shape the orphan reaper classifies as "cannot age", so several consumers
// were being asserted against an impossible input.
func draftLedgerSession(t *testing.T, ledgerPath, sessionName string) string {
	t.Helper()
	dir := filepath.Join(ledgerPath, "sessions", sessionName)
	require.NoError(t, os.MkdirAll(dir, 0755))
	updated := time.Now().UTC()
	require.NoError(t, lfs.WriteSessionMetaOnly(dir, &lfs.SessionMeta{
		SessionName: sessionName, SessionID: sessionScopedID(sessionName),
		CreatedAt: updated, Draft: true, TurnCount: 2, UpdatedAt: &updated,
		Files: map[string]lfs.FileRef{},
	}))
	return dir
}

// TestIsSessionInLedger_DraftIsNotUploaded is the regression for the bug this
// feature would otherwise introduce.
//
// isSessionInLedger was a bare os.Stat on the directory. A draft makes that
// succeed, so ClassifySession returns StatusUploaded for a LIVE recording —
// which makes `ox session abort <name>` refuse with "already uploaded, use
// session delete". That breaks the privacy escape hatch for exactly the
// sessions a user is most likely to want discarded.
//
// Red-first check: revert isSessionInLedger to the bare os.Stat and the draft
// sub-case flips to true.
func TestIsSessionInLedger_DraftIsNotUploaded(t *testing.T) {
	setTestCfg(t)
	projectRoot, ledgerPath := setupLedgerProject(t)
	t.Chdir(projectRoot)

	draftLedgerSession(t, ledgerPath, "2026-01-01T00-00-testuser-OxDr01")
	assert.False(t, isSessionInLedger("2026-01-01T00-00-testuser-OxDr01"),
		"a draft placeholder must not count as uploaded")

	finalizedLedgerSession(t, ledgerPath, "2026-01-01T00-00-testuser-OxFi01")
	assert.True(t, isSessionInLedger("2026-01-01T00-00-testuser-OxFi01"),
		"a finalized session must still count as uploaded")

	// Fail-safe direction: an unreadable meta.json is treated as FINALIZED, so
	// abort refuses and points at `session delete` rather than deleting a
	// directory it cannot classify.
	corruptDir := filepath.Join(ledgerPath, "sessions", "2026-01-01T00-00-testuser-OxCo01")
	require.NoError(t, os.MkdirAll(corruptDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(corruptDir, "meta.json"), []byte(`{"draft":tr`), 0644))
	assert.True(t, isSessionInLedger("2026-01-01T00-00-testuser-OxCo01"),
		"an unclassifiable session must fail safe toward 'do not delete'")

	assert.False(t, isSessionInLedger("2026-01-01T00-00-testuser-OxNone"),
		"a session with no ledger presence is not uploaded")
}

// TestClassifySession_DraftIsNotUploaded pins the classifier itself, so the fix
// cannot regress by someone "simplifying" isSessionInLedger's caller.
func TestClassifySession_DraftIsNotUploaded(t *testing.T) {
	info := session.SessionInfo{SessionName: "s", Draft: true}
	assert.Equal(t, session.StatusDraft, session.ClassifySession(info, true),
		"a draft must classify as draft even when the caller says 'in the ledger'")

	assert.Equal(t, session.StatusUploaded,
		session.ClassifySession(session.SessionInfo{SessionName: "s"}, true),
		"negative control: a non-draft in the ledger is still uploaded")

	// A live recording outranks draft-ness — the Recording branch comes first.
	live := session.SessionInfo{SessionName: "s", Draft: true, Recording: true, ParentPID: os.Getpid()}
	assert.Equal(t, session.StatusRecording, session.ClassifySession(live, true))
}

// TestResolveSessionForAbort_PrefersCacheOverLedgerDraft.
//
// The store search order used to be [XDG cache, ledger sessions/, ledger
// cache]. Recordings live in the LEDGER CACHE, searched last. Once a draft
// exists, the git-tracked ledger directory matches first — and the caller then
// runs os.RemoveAll on a TRACKED directory, deleting it from the worktree with
// no `git rm` (so the next pull restores it) while leaving the real recording
// completely untouched.
//
// Red-first check: restore the old store order and this returns the ledger
// path.
func TestResolveSessionForAbort_PrefersCacheOverLedgerDraft(t *testing.T) {
	setTestCfg(t)
	projectRoot, ledgerPath := setupLedgerProject(t)
	t.Chdir(projectRoot)

	sessionName, cachePath := makeLedgerCacheOrphanSession(t, ledgerPath, "OxDual")
	draftLedgerSession(t, ledgerPath, sessionName)

	resolved, resolvedPath, err := resolveSessionForAbort(projectRoot, sessionName)
	require.NoError(t, err)
	assert.Equal(t, sessionName, resolved)
	assert.Equal(t, cachePath, resolvedPath,
		"abort must resolve to the recording cache, never to the git-tracked draft directory")
}

// TestResolveSessionForAbort_SkipsDraftOnlyLedgerMatch — with no cache copy at
// all, a draft-only ledger match must not be handed back as something to
// os.RemoveAll. The ledger draft is removed via git, by a different code path.
func TestResolveSessionForAbort_SkipsDraftOnlyLedgerMatch(t *testing.T) {
	setTestCfg(t)
	projectRoot, ledgerPath := setupLedgerProject(t)
	t.Chdir(projectRoot)

	const sessionName = "2026-01-01T00-00-testuser-OxOnly"
	draftLedgerSession(t, ledgerPath, sessionName)

	// Not "not found": a draft-only session must still be ABORTABLE. The
	// sentinel routes the caller to the git-removal path instead of
	// os.RemoveAll on a tracked directory. Without it, a placeholder whose
	// recording is gone (pruned cache, dead agent, an earlier abort whose git
	// removal never landed) advertises the session forever with no way to
	// remove it — and the daemon deliberately skips drafts, so nothing else
	// ever would.
	resolvedName, resolvedPath, err := resolveSessionForAbort(projectRoot, sessionName)
	require.ErrorIs(t, err, errDraftOnlySession)
	assert.Equal(t, sessionName, resolvedName, "the caller needs the name to git-remove it")
	assert.Empty(t, resolvedPath, "there is no local directory to delete")

	// Negative control: a finalized ledger session IS resolvable (existing
	// behavior — abort then refuses it by status, not by resolution).
	finalized := "2026-01-01T00-00-testuser-OxFin2"
	finalizedLedgerSession(t, ledgerPath, finalized)
	resolved, _, err := resolveSessionForAbort(projectRoot, finalized)
	require.NoError(t, err)
	assert.Equal(t, finalized, resolved)
}

// --- git-level abort ------------------------------------------------------

// TestDeleteDraftFromLedger_RemovesFromRemote asserts through a FRESH clone.
// The local worktree is not the ledger; a local deletion proves nothing about
// what teammates still see.
func TestDeleteDraftFromLedger_RemovesFromRemote(t *testing.T) {
	f := newDraftLedgerFixture(t)
	const sessionName = "2026-01-01T00-00-testuser-OxDel"

	f.publish(t, sessionName, 2)
	f.push(t)
	require.Contains(t, remoteTree(t, f.barePath), "sessions/"+sessionName+"/meta.json")

	res, err := deleteDraftFromLedger(f.ledgerPath, sessionName)
	require.NoError(t, err)
	assert.True(t, res.Deleted)
	assert.Empty(t, res.PushWarning)

	assert.NotContains(t, remoteTree(t, f.barePath), "sessions/"+sessionName+"/meta.json",
		"the draft must be gone from the remote, not just locally")
	assert.NoDirExists(t, draftLedgerSessionDir(f.ledgerPath, sessionName))
	gitFsckClean(t, f.barePath)
}

// TestDeleteDraftFromLedger_RefusesFinalizedSession.
//
// Abort resolves sessions by partial name. A collision with a teammate's
// finalized session pulled from the remote must never become a deletion of
// their work — that is what `ox agent session delete` is for, with its own
// confirmation.
func TestDeleteDraftFromLedger_RefusesFinalizedSession(t *testing.T) {
	f := newDraftLedgerFixture(t)
	const sessionName = "2026-01-01T00-00-testuser-OxKeep"

	finalizedLedgerSession(t, f.ledgerPath, sessionName)

	res, err := deleteDraftFromLedger(f.ledgerPath, sessionName)
	require.Error(t, err)
	assert.False(t, res.Deleted)
	assert.Contains(t, err.Error(), "session delete")
	assert.FileExists(t, filepath.Join(f.ledgerPath, "sessions", sessionName, "meta.json"),
		"a finalized session must survive an abort that names it")
}

// TestDeleteDraftFromLedger_UnreadableMetaRefuses — same fail-safe direction as
// isSessionInLedger. We cannot classify it, so we do not delete it.
func TestDeleteDraftFromLedger_UnreadableMetaRefuses(t *testing.T) {
	f := newDraftLedgerFixture(t)
	const sessionName = "2026-01-01T00-00-testuser-OxBad"

	dir := draftLedgerSessionDir(f.ledgerPath, sessionName)
	require.NoError(t, os.MkdirAll(dir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "meta.json"), []byte(`{{{`), 0644))

	res, err := deleteDraftFromLedger(f.ledgerPath, sessionName)
	require.Error(t, err)
	assert.False(t, res.Deleted)
	assert.FileExists(t, filepath.Join(dir, "meta.json"))
}

// TestDeleteDraftFromLedger_NoLedgerPresenceIsNoop — aborting a session that
// never published a draft must be silent, not an error. Most aborts are this.
func TestDeleteDraftFromLedger_NoLedgerPresenceIsNoop(t *testing.T) {
	f := newDraftLedgerFixture(t)
	res, err := deleteDraftFromLedger(f.ledgerPath, "2026-01-01T00-00-testuser-OxGhost")
	require.NoError(t, err)
	assert.False(t, res.Deleted)
	assert.Empty(t, res.PushWarning)
}

// TestDeleteDraftFromLedger_SurvivesPushFailureAndMakesProgress.
//
// This is the wedge-harness part-3 clause, and it is the part that matters.
// Asserting only "the local commit exists" would pass while the ledger is
// permanently stuck: a naive retry re-runs `git rm`, finds nothing to remove,
// and reports zero staged forever. The test therefore restores the remote and
// proves the deletion actually lands.
func TestDeleteDraftFromLedger_SurvivesPushFailureAndMakesProgress(t *testing.T) {
	f := newDraftLedgerFixture(t)
	const sessionName = "2026-01-01T00-00-testuser-OxPushFail"

	f.publish(t, sessionName, 2)
	f.push(t)

	runGit(t, f.ledgerPath, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "gone.git"))

	// 1. The abort still SUCCEEDS — the local data is what abort promised to
	//    discard, and failing here would leave the user with no clean retry.
	res, err := deleteDraftFromLedger(f.ledgerPath, sessionName)
	require.NoError(t, err)
	assert.True(t, res.Deleted)
	assert.NotEmpty(t, res.PushWarning, "an unpushed deletion must be surfaced, not swallowed")

	// 2. The deletion is durable locally.
	assert.NoDirExists(t, draftLedgerSessionDir(f.ledgerPath, sessionName))
	assert.Empty(t, runGit(t, f.ledgerPath, "status", "--porcelain", "--", "sessions/"),
		"no staged deletion may be left dangling for the next commit to sweep up")

	// 3. THE PART THAT MATTERS: the ledger makes progress afterward.
	runGit(t, f.ledgerPath, "remote", "set-url", "origin", f.barePath)
	f.push(t)
	assert.NotContains(t, remoteTree(t, f.barePath), "sessions/"+sessionName+"/meta.json")
	assert.Equal(t, "0", runGit(t, f.ledgerPath, "rev-list", "--count", "@{upstream}..HEAD"))
	gitFsckClean(t, f.barePath)
}

// TestDeleteDraftFromLedger_Idempotent — a repeated abort must not error or
// produce a second commit.
func TestDeleteDraftFromLedger_Idempotent(t *testing.T) {
	f := newDraftLedgerFixture(t)
	const sessionName = "2026-01-01T00-00-testuser-OxTwice"

	f.publish(t, sessionName, 2)
	f.push(t)

	_, err := deleteDraftFromLedger(f.ledgerPath, sessionName)
	require.NoError(t, err)
	before := commitCount(t, f.ledgerPath)

	res, err := deleteDraftFromLedger(f.ledgerPath, sessionName)
	require.NoError(t, err)
	assert.False(t, res.Deleted, "nothing left to delete")
	assert.Equal(t, before, commitCount(t, f.ledgerPath), "no second commit")
	gitFsckClean(t, f.barePath)
}

// TestDeleteDraftFromLedger_DoesNotTouchSiblingDrafts.
//
// `git rm -r` plus an unscoped commit would sweep a co-staged sibling. Two
// agents drafting into one ledger clone is the normal case, not an edge case.
func TestDeleteDraftFromLedger_DoesNotTouchSiblingDrafts(t *testing.T) {
	f := newDraftLedgerFixture(t)
	const doomed = "2026-01-01T00-00-testuser-OxGone"
	const keeper = "2026-01-01T00-00-testuser-OxStay"

	f.publish(t, doomed, 2)
	f.publish(t, keeper, 2)
	f.push(t)

	_, err := deleteDraftFromLedger(f.ledgerPath, doomed)
	require.NoError(t, err)

	tree := remoteTree(t, f.barePath)
	assert.NotContains(t, tree, "sessions/"+doomed+"/meta.json")
	assert.Contains(t, tree, "sessions/"+keeper+"/meta.json",
		"the sibling draft must survive, and must be committed rather than left staged")
	assert.Empty(t, runGit(t, f.ledgerPath, "status", "--porcelain", "--", "sessions/"))
	gitFsckClean(t, f.barePath)
}

// TestSessionRemove_SkipsDraftPlaceholders.
//
// Before drafts, a name pattern could not match a LIVE session in the ledger —
// a session was either in the cache (live) or in the ledger (finished). A draft
// makes a live session match, and `ox session remove` would then delete the
// running recording's local copy alongside the placeholder, while the session
// kept recording and republished at stop. Abort is the command for discarding a
// live session; it removes the placeholder itself.
func TestSessionRemove_SkipsDraftPlaceholders(t *testing.T) {
	setTestCfg(t)
	f := newDraftLedgerFixture(t)
	// removeSessionByPattern resolves the ledger from the PROJECT root, so the
	// fixture's chdir-into-the-clone (which exists for push credential
	// isolation) has to be undone for this one.
	t.Chdir(f.projectRoot)

	const draftName = "2026-01-01T00-00-testuser-OxRmDr"
	const doneName = "2026-01-01T00-00-testuser-OxRmDn"
	f.publish(t, draftName, 2)
	finalizedLedgerSession(t, f.ledgerPath, doneName)
	runGit(t, f.ledgerPath, "add", "--sparse", "--", "sessions/"+doneName+"/meta.json")
	runGit(t, f.ledgerPath, "commit", "--no-verify", "-m", "session: "+doneName)
	f.push(t)

	// The "local" store in production is the XDG/session cache, NOT the ledger.
	// Passing the ledger here would route both sessions down the local-delete
	// branch and never exercise the ledger match at all.
	localStore, err := session.NewStore(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, removeSessionByPattern(localStore, "2026-01-01T00-00-testuser-OxRm", true))

	tree := remoteTree(t, f.barePath)
	assert.Contains(t, tree, "sessions/"+draftName+"/meta.json",
		"a live session's draft placeholder must survive `session remove`")
	assert.NotContains(t, tree, "sessions/"+doneName+"/meta.json",
		"negative control: a finalized session is still removed")
}
