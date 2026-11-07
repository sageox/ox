package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/gitserver"
	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// End-to-end proofs for publishing `ox session regenerate` output (GH #1433).
// Real: the Ledger clone and its bare remote, the LFS client, credentials,
// pointer writing, the guarded snapshot commit and pushLedger. Fake: the LFS
// store, which keeps every uploaded object so a test can read what the
// remote's pointers name.

// regenerateLedgerFixture is a project whose Ledger fetches LFS objects from a
// fake store and pushes to a real bare remote.
type regenerateLedgerFixture struct {
	*draftLedgerFixture
	store *fakeLFSStore
}

// newRegenerateLedgerFixture builds the fixture with isolated XDG dirs and
// stored credentials for the fake store.
func newRegenerateLedgerFixture(t *testing.T) *regenerateLedgerFixture {
	t.Helper()
	f := &regenerateLedgerFixture{draftLedgerFixture: newDraftLedgerFixture(t), store: newFakeLFSStore(t)}
	t.Chdir(f.projectRoot)
	oldCfg := cfg
	cfg = &config.Config{}
	t.Cleanup(func() { cfg = oldCfg })
	for _, key := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME"} {
		t.Setenv(key, t.TempDir())
	}
	priorDir := gitserver.TestSetConfigDirOverride(t.TempDir())
	t.Cleanup(func() { gitserver.TestSetConfigDirOverride(priorDir) })
	priorStorage := gitserver.TestSetForceFileStorage(true)
	t.Cleanup(func() { gitserver.TestSetForceFileStorage(priorStorage) })

	t.Setenv("SAGEOX_ENDPOINT", f.store.url)
	runGit(t, f.ledgerPath, "remote", "set-url", "origin", f.store.url+"/ledger.git")
	runGit(t, f.ledgerPath, "remote", "set-url", "--push", "origin", f.barePath)
	require.NoError(t, gitserver.SaveCredentialsForEndpoint(f.store.url, gitserver.GitCredentials{
		Username: "testuser", Token: "test-token", ServerURL: f.store.url, ExpiresAt: time.Now().Add(time.Hour),
	}))
	return f
}

// publishSession commits and pushes a session the way `ox session stop`
// leaves it: pointers and meta.json on the Ledger, objects in the LFS store,
// and the raw content in the Ledger cache, as after `ox session download`.
func (f *regenerateLedgerFixture) publishSession(t *testing.T, name, prompt string) {
	t.Helper()
	rawPath := filepath.Join(t.TempDir(), ledgerFileRaw)
	require.NoError(t, rewriteRawJSONL(rawPath, &session.StoredSession{
		Meta: &session.StoreMeta{Version: "1", CreatedAt: time.Now().UTC(), AgentType: "claude-code", Username: "Devon"},
		Entries: []map[string]any{
			{"type": "user", "content": prompt},
			{"type": "assistant", "content": "Done."},
		},
	}))
	raw, err := os.ReadFile(rawPath)
	require.NoError(t, err)
	staleMD := []byte("# stale session.md\n")

	refs := map[string]lfs.FileRef{ledgerFileRaw: lfs.NewFileRef(raw), ledgerFileSessionMD: lfs.NewFileRef(staleMD)}
	f.store.mu.Lock()
	f.store.objects[refs[ledgerFileRaw].BareOID()] = raw
	f.store.objects[refs[ledgerFileSessionMD].BareOID()] = staleMD
	f.store.mu.Unlock()

	sessionDir := filepath.Join(f.ledgerPath, "sessions", name)
	require.NoError(t, os.MkdirAll(sessionDir, 0o755))
	require.NoError(t, lfs.WriteSessionMetaOnly(sessionDir, &lfs.SessionMeta{
		Version: "1.0", SessionName: name, Username: "Devon", AgentID: "OxRgn1", Files: refs,
	}))
	_, err = lfs.WritePointerFiles(sessionDir, lfs.AssertUploadedManifest(refs))
	require.NoError(t, err)
	require.NoError(t, ensureSessionsGitignore(filepath.Join(f.ledgerPath, "sessions")))
	gitserver.EnsureGitignoreBeforeCommit(f.ledgerPath)
	runGit(t, f.ledgerPath, "add", "sessions")
	runGit(t, f.ledgerPath, "commit", "--no-verify", "-q", "-m", "session: "+name)
	runGit(t, f.ledgerPath, "push", "-q", "origin", "HEAD")

	cacheDir := ledgerSessionCacheDir(f.ledgerPath, name)
	require.NoError(t, os.MkdirAll(cacheDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(cacheDir, ledgerFileRaw), raw, 0o600))
}

// remoteSessionMD resolves the session.md the remote's pointer names.
func (f *regenerateLedgerFixture) remoteSessionMD(t *testing.T, name string) string {
	t.Helper()
	pointer := runGit(t, f.barePath, "show", "HEAD:sessions/"+name+"/"+ledgerFileSessionMD)
	oid, _, err := lfs.ParsePointer(pointer + "\n")
	require.NoError(t, err, "the remote session.md must be an LFS pointer, got:\n%s", pointer)
	content := f.store.object(oid)
	require.NotNil(t, content, "the LFS object %s the remote points at was never uploaded", oid)
	var meta lfs.SessionMeta
	metaJSON := runGit(t, f.barePath, "show", "HEAD:sessions/"+name+"/meta.json")
	require.NoError(t, json.Unmarshal([]byte(metaJSON), &meta))
	assert.Equal(t, oid, meta.Files[ledgerFileSessionMD].OID, "meta.json must record the session.md the pointer names")
	return string(content)
}

// staged lists the Ledger paths staged in the index.
func (f *regenerateLedgerFixture) staged(t *testing.T) []string {
	t.Helper()
	out := runGit(t, f.ledgerPath, "diff", "--cached", "--name-only")
	if out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

// newStore opens the project's managed session store.
func (f *regenerateLedgerFixture) newStore(t *testing.T) *session.Store {
	t.Helper()
	store, _, err := newSessionStore()
	require.NoError(t, err)
	return store
}

// Regenerating a published session publishes the new session.md as a pointer,
// and a second session regenerates cleanly after the first.
// Failure prevented: regenerate printed success while staging plain session.md
// over its pointer, so nothing reached the remote and the next regenerate, for
// another session, was refused naming the first.
func TestRegenerateSession_PublishesArtifactsAsPointers(t *testing.T) {
	f := newRegenerateLedgerFixture(t)
	first, second := recentSessionName("OxRgnA"), recentSessionName("OxRgnB")
	f.publishSession(t, first, "Fix the login redirect loop")
	f.publishSession(t, second, "Explain the failing Ledger push")
	store := f.newStore(t)

	silenceStdout(t, func() {
		require.NoError(t, regenerateSingleSessionArtifacts(store, f.projectRoot, first))
		require.NoError(t, regenerateSingleSessionArtifacts(store, f.projectRoot, second))
	})

	assert.Contains(t, f.remoteSessionMD(t, first), "Fix the login redirect loop")
	assert.Contains(t, f.remoteSessionMD(t, second), "Explain the failing Ledger push")
	assert.Empty(t, runGit(t, f.ledgerPath, "status", "--porcelain", "--", "sessions"), "the Ledger worktree must match what was pushed")
	cached, err := os.ReadFile(filepath.Join(ledgerSessionCacheDir(f.ledgerPath, first), ledgerFileSessionMD))
	require.NoError(t, err)
	assert.Contains(t, string(cached), "Fix the login redirect loop", "the real bytes stay readable in the Ledger cache")
}

// A failed upload fails the command and leaves the Ledger as it was.
// Failure prevented: a WARN followed by a success line and exit 0.
func TestRegenerateSession_FailedUploadFailsAndLeavesLedgerClean(t *testing.T) {
	f := newRegenerateLedgerFixture(t)
	name := recentSessionName("OxRgnC")
	f.publishSession(t, name, "Rotate the deploy credentials")
	store := f.newStore(t)
	f.store.mu.Lock()
	f.store.failBatch = true
	f.store.mu.Unlock()

	var err error
	silenceStdout(t, func() { err = regenerateSingleSessionArtifacts(store, f.projectRoot, name) })

	require.Error(t, err)
	assert.Contains(t, err.Error(), "LFS upload")
	assert.Empty(t, runGit(t, f.ledgerPath, "status", "--porcelain", "--", "sessions"), "no tracked session file may change")
}

// A refused commit fails the command and unstages what it staged.
// Failure prevented: a refused commit left this session's files staged, so
// the refusal followed every later Ledger commit.
func TestRegenerateSession_RefusedCommitUnstagesItsFiles(t *testing.T) {
	f := newRegenerateLedgerFixture(t)
	name, other := recentSessionName("OxRgnD"), recentSessionName("OxRgnE")
	f.publishSession(t, name, "Trace the cache miss")
	f.publishSession(t, other, "Tune the indexer")
	// another command left plain content staged over a pointer
	poisoned := filepath.Join("sessions", other, ledgerFileSessionMD)
	require.NoError(t, os.WriteFile(filepath.Join(f.ledgerPath, poisoned), []byte("# plain content\n"), 0o644))
	runGit(t, f.ledgerPath, "add", poisoned)
	// a pending change at a path this command stages too
	pending := filepath.Join("sessions", ".gitignore")
	body, err := os.ReadFile(filepath.Join(f.ledgerPath, pending))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(f.ledgerPath, pending), append(body, []byte("# pending\n")...), 0o644))
	runGit(t, f.ledgerPath, "add", pending)
	store := f.newStore(t)

	silenceStdout(t, func() { err = regenerateSingleSessionArtifacts(store, f.projectRoot, name) })

	require.Error(t, err)
	assert.Contains(t, err.Error(), "must contain an LFS pointer")
	assert.ElementsMatch(t, []string{filepath.ToSlash(pending), filepath.ToSlash(poisoned)}, f.staged(t),
		"only what was staged before the command may remain staged")
	assert.Contains(t, runGit(t, f.ledgerPath, "show", ":"+filepath.ToSlash(pending)), "# pending",
		"a change staged before the command must keep its staged content")
}

// --all publishes the sessions it regenerated in one commit, fails when the
// push fails, and an identical retry publishes the pending commit.
// Failure prevented: --all committed under the placeholder name
// "batch-regenerate", downgraded the refusal to a WARN, and a retry with no
// new changes returned before pushing.
func TestRegenerateAll_PublishesAndRetriesFailedPush(t *testing.T) {
	f := newRegenerateLedgerFixture(t)
	name := recentSessionName("OxRgnF")
	f.publishSession(t, name, "Fix the worktree upload bug")
	store := f.newStore(t)
	// authored on this machine: the managed store holds the session too
	writeSessionFiles(t, filepath.Dir(store.BasePath()), name)
	remoteHead := runGit(t, f.barePath, "rev-parse", "HEAD")

	runGit(t, f.ledgerPath, "remote", "set-url", "--push", "origin", filepath.Join(t.TempDir(), "missing.git"))
	var err error
	silenceStdout(t, func() { err = regenerateAllSessionsArtifacts(store, f.projectRoot, true) })
	require.Error(t, err, "a failed push must fail --all")
	assert.Equal(t, remoteHead, runGit(t, f.barePath, "rev-parse", "HEAD"))
	assert.Empty(t, f.staged(t))

	runGit(t, f.ledgerPath, "remote", "set-url", "--push", "origin", f.barePath)
	silenceStdout(t, func() { err = regenerateAllSessionsArtifacts(store, f.projectRoot, true) })
	require.NoError(t, err)
	assert.Contains(t, f.remoteSessionMD(t, name), "Fix the worktree upload bug")
	assert.Equal(t, "session: regenerate 1 sessions", runGit(t, f.barePath, "log", "-1", "--format=%s"))
}

// Sessions the Ledger has no place for are skipped, not failed.
func TestPrepareRegeneratedArtifacts_SkipsSessionsWithNothingToPublish(t *testing.T) {
	f := newRegenerateLedgerFixture(t)
	published := recentSessionName("OxRgnG")
	f.publishSession(t, published, "Skip me")
	draft := recentSessionName("OxRgnH")
	draftDir := filepath.Join(f.ledgerPath, "sessions", draft)
	require.NoError(t, os.MkdirAll(draftDir, 0o755))
	require.NoError(t, lfs.WriteSessionMetaOnly(draftDir, &lfs.SessionMeta{Version: "1.0", SessionName: draft, Draft: true}))

	withContent := func(t *testing.T, held bool) string {
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, ledgerFileSessionMD), []byte("# regenerated\n"), 0o600))
		if held {
			require.NoError(t, os.WriteFile(filepath.Join(dir, session.HeldMarkerFile), nil, 0o600))
		}
		return dir
	}
	pointerOnly := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(pointerOnly, ledgerFileSessionMD),
		[]byte(lfs.FormatPointer(lfs.NewFileRef([]byte("x")).OID, 1)), 0o600))

	tests := []struct {
		name, session, dir string
	}{
		{"not on the Ledger", recentSessionName("OxRgnI"), withContent(t, false)},
		{"draft placeholder", draft, withContent(t, false)},
		{"held on this machine", published, withContent(t, true)},
		{"nothing regenerated", published, pointerOnly},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ok, err := prepareRegeneratedArtifacts(f.projectRoot, f.ledgerPath, tt.session, tt.dir)
			require.NoError(t, err)
			assert.False(t, ok)
		})
	}
	assert.Empty(t, f.staged(t))
	assert.Empty(t, runGit(t, f.ledgerPath, "status", "--porcelain", "--", filepath.Join("sessions", published)))
}

// A pointer that cannot be written fails and leaves meta.json naming the old refs.
func TestPrepareRegeneratedArtifacts_FailedPointerWriteRevertsRefs(t *testing.T) {
	f := newRegenerateLedgerFixture(t)
	name := recentSessionName("OxRgnJ")
	f.publishSession(t, name, "Break the pointer")
	sessionDir := filepath.Join(f.ledgerPath, "sessions", name)
	before, err := lfs.ReadSessionMeta(sessionDir)
	require.NoError(t, err)
	// summary.md cannot be replaced: a non-empty directory sits in its place
	require.NoError(t, os.MkdirAll(filepath.Join(sessionDir, ledgerFileSummaryMD, "x"), 0o755))
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, ledgerFileSessionMD), []byte("# new session\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ledgerFileSummaryMD), []byte("# new summary\n"), 0o600))

	ok, err := prepareRegeneratedArtifacts(f.projectRoot, f.ledgerPath, name, dir)

	require.Error(t, err)
	assert.False(t, ok)
	after, err := lfs.ReadSessionMeta(sessionDir)
	require.NoError(t, err)
	assert.Equal(t, before.Files, after.Files, "meta.json must name the refs it had before")
	pointer, err := os.ReadFile(filepath.Join(sessionDir, ledgerFileSessionMD))
	require.NoError(t, err)
	assert.Equal(t, runGit(t, f.ledgerPath, "show", "HEAD:sessions/"+name+"/"+ledgerFileSessionMD),
		strings.TrimSpace(string(pointer)), "the session.md pointer must be restored")
}

// The revert restores only the refs this run set.
func TestRevertRegeneratedRefs(t *testing.T) {
	dir := t.TempDir()
	old, mine, theirs := lfs.NewFileRef([]byte("old")), lfs.NewFileRef([]byte("mine")), lfs.NewFileRef([]byte("theirs"))
	require.NoError(t, lfs.WriteSessionMetaOnly(dir, &lfs.SessionMeta{Version: "1.0", SessionName: "s", Files: map[string]lfs.FileRef{
		ledgerFileSessionMD: mine,   // set by this run over old
		ledgerFileSummaryMD: mine,   // set by this run, absent before
		ledgerFileRaw:       theirs, // changed by another writer since
	}}))
	uploaded := map[string]lfs.FileRef{ledgerFileSessionMD: mine, ledgerFileSummaryMD: mine, ledgerFileRaw: mine}
	prior := map[string]*lfs.FileRef{ledgerFileSessionMD: &old, ledgerFileSummaryMD: nil, ledgerFileRaw: &old}

	require.NoError(t, revertRegeneratedRefs(dir, uploaded, prior))

	meta, err := lfs.ReadSessionMeta(dir)
	require.NoError(t, err)
	assert.Equal(t, map[string]lfs.FileRef{ledgerFileSessionMD: old, ledgerFileRaw: theirs}, meta.Files)

	empty := t.TempDir()
	require.NoError(t, lfs.WriteSessionMetaOnly(empty, &lfs.SessionMeta{Version: "1.0", SessionName: "e"}))
	require.NoError(t, revertRegeneratedRefs(empty, uploaded, prior), "no refs is nothing to revert")
}

// --all fails, and stages nothing, when a session cannot be uploaded or the
// batch commit is refused.
func TestRegenerateAll_FailedPublicationFails(t *testing.T) {
	t.Run("upload fails", func(t *testing.T) {
		f := newRegenerateLedgerFixture(t)
		name := recentSessionName("OxRgnK")
		f.publishSession(t, name, "Upload will fail")
		store := f.newStore(t)
		writeSessionFiles(t, filepath.Dir(store.BasePath()), name)
		f.store.mu.Lock()
		f.store.failBatch = true
		f.store.mu.Unlock()

		var err error
		silenceStdout(t, func() { err = regenerateAllSessionsArtifacts(store, f.projectRoot, true) })

		require.Error(t, err)
		assert.Contains(t, err.Error(), "LFS upload")
		assert.Empty(t, f.staged(t))
	})
	t.Run("commit refused", func(t *testing.T) {
		f := newRegenerateLedgerFixture(t)
		name, other := recentSessionName("OxRgnL"), recentSessionName("OxRgnM")
		f.publishSession(t, name, "Commit will be refused")
		f.publishSession(t, other, "Holds the poisoned entry")
		poisoned := filepath.Join("sessions", other, ledgerFileSessionMD)
		require.NoError(t, os.WriteFile(filepath.Join(f.ledgerPath, poisoned), []byte("# plain content\n"), 0o644))
		runGit(t, f.ledgerPath, "add", poisoned)
		store := f.newStore(t)
		writeSessionFiles(t, filepath.Dir(store.BasePath()), name)

		var err error
		silenceStdout(t, func() { err = regenerateAllSessionsArtifacts(store, f.projectRoot, true) })

		require.Error(t, err)
		assert.Contains(t, err.Error(), "must contain an LFS pointer")
		assert.Equal(t, []string{filepath.ToSlash(poisoned)}, f.staged(t))
	})
}

// Without a Ledger, regenerated artifacts stay local and nothing fails.
func TestRegenerate_NoLedgerKeepsArtifactsLocal(t *testing.T) {
	t.Chdir(t.TempDir())
	oldCfg := cfg
	cfg = &config.Config{}
	t.Cleanup(func() { cfg = oldCfg })
	_, err := resolveLedgerPath()
	require.Error(t, err)

	src, out := artifactDirs(nil, "s")
	assert.Equal(t, "s", src)
	assert.Equal(t, "s", out)
	store, err := session.NewStore(t.TempDir())
	require.NoError(t, err)
	src, out = artifactDirs(store, "s")
	assert.Equal(t, store.GetSessionPath("s"), src)
	assert.Equal(t, src, out)
	ok, err := publishRegeneratedSession(t.TempDir(), "s", t.TempDir())
	require.NoError(t, err)
	assert.False(t, ok)
	n, err := publishRegeneratedSessions(t.TempDir(), []regeneratedSession{{name: "s", dir: t.TempDir()}})
	require.NoError(t, err)
	assert.Zero(t, n)
}

// A session held on this machine regenerates locally and publishes nothing.
func TestRegenerateSession_HeldSessionStaysLocal(t *testing.T) {
	f := newRegenerateLedgerFixture(t)
	name := recentSessionName("OxRgnN")
	f.publishSession(t, name, "Keep this local")
	store := f.newStore(t)
	writeSessionFiles(t, filepath.Dir(store.BasePath()), name)
	require.NoError(t, os.WriteFile(filepath.Join(store.GetSessionPath(name), session.HeldMarkerFile), nil, 0o600))
	head := runGit(t, f.ledgerPath, "rev-parse", "HEAD")

	silenceStdout(t, func() { require.NoError(t, regenerateSingleSessionArtifacts(store, f.projectRoot, name)) })

	assert.FileExists(t, filepath.Join(store.GetSessionPath(name), ledgerFileSessionMD))
	assert.Equal(t, head, runGit(t, f.ledgerPath, "rev-parse", "HEAD"))
	assert.Empty(t, f.staged(t))
}

// Inputs that cannot be read or cached fail before anything is staged.
func TestPrepareRegeneratedArtifacts_FailsOnUnusableInputs(t *testing.T) {
	f := newRegenerateLedgerFixture(t)
	regenerated := func(t *testing.T) string {
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, ledgerFileSessionMD), []byte("# regenerated\n"), 0o600))
		return dir
	}
	tests := []struct {
		name, wantErr string
		setup         func(t *testing.T, session string) string
	}{
		{"corrupt meta.json", "read meta.json", func(t *testing.T, session string) string {
			require.NoError(t, os.WriteFile(filepath.Join(f.ledgerPath, "sessions", session, "meta.json"), []byte("{"), 0o644))
			return regenerated(t)
		}},
		{"unreadable artifact", "read regenerated", func(t *testing.T, _ string) string {
			dir := t.TempDir()
			require.NoError(t, os.MkdirAll(filepath.Join(dir, ledgerFileSessionMD), 0o700))
			return dir
		}},
		{"Ledger cache blocked", "create Ledger cache", func(t *testing.T, session string) string {
			cacheDir := ledgerSessionCacheDir(f.ledgerPath, session)
			require.NoError(t, os.RemoveAll(cacheDir))
			require.NoError(t, os.WriteFile(cacheDir, nil, 0o600))
			return regenerated(t)
		}},
		{"cached file blocked", "cache regenerated", func(t *testing.T, session string) string {
			require.NoError(t, os.MkdirAll(filepath.Join(ledgerSessionCacheDir(f.ledgerPath, session), ledgerFileSessionMD, "x"), 0o700))
			return regenerated(t)
		}},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			session := recentSessionName("OxRgP" + string(rune('a'+i)))
			f.publishSession(t, session, "Unusable input")
			dir := tt.setup(t, session)

			ok, err := prepareRegeneratedArtifacts(f.projectRoot, f.ledgerPath, session, dir)

			require.ErrorContains(t, err, tt.wantErr)
			assert.False(t, ok)
			assert.Empty(t, f.staged(t))
		})
	}
}

// Unusable directories and indexes fail with an error instead of being skipped.
func TestRegenerateHelpers_ReportUnusablePaths(t *testing.T) {
	blocked := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(blocked, nil, 0o600))
	require.ErrorContains(t, regenerateArtifactsInto(t.TempDir(), filepath.Join(blocked, "out"), &session.StoredSession{}), "create")

	corrupt := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(corrupt, "meta.json"), []byte("{"), 0o600))
	require.ErrorContains(t, revertRegeneratedRefs(corrupt, map[string]lfs.FileRef{}, nil), "revert meta.json LFS refs")

	notRepo := t.TempDir()
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(notRepo))
	_, err := ledgerIndexEntries(notRepo, []string{"meta.json"})
	require.ErrorContains(t, err, "read Ledger index")
	restoreLedgerIndex(notRepo, []string{"meta.json"}, nil) // logs and returns

	repo := t.TempDir()
	runGit(t, repo, "init", "-q")
	restoreLedgerIndex(repo, []string{"meta.json"}, []byte("not an index entry\x00"))
	assert.Empty(t, runGit(t, repo, "ls-files", "--stage"), "a bad prior entry must not be written")
}

// A batch naming a session that is not on disk fails at git add and stages nothing.
func TestCommitAndPushLedgerBatch_FailedAddStagesNothing(t *testing.T) {
	f := newRegenerateLedgerFixture(t)
	f.publishSession(t, recentSessionName("OxRgnQ"), "Already published")

	err := commitAndPushLedgerBatch(f.ledgerPath, "session: regenerate 1 sessions", []string{recentSessionName("OxRgnR")})

	require.ErrorContains(t, err, "git add")
	assert.Empty(t, f.staged(t))
}
