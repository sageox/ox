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

func (f *regenerateLedgerFixture) staged(t *testing.T) []string {
	t.Helper()
	out := runGit(t, f.ledgerPath, "diff", "--cached", "--name-only")
	if out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

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
	store := f.newStore(t)

	var err error
	silenceStdout(t, func() { err = regenerateSingleSessionArtifacts(store, f.projectRoot, name) })

	require.Error(t, err)
	assert.Contains(t, err.Error(), "must contain an LFS pointer")
	assert.Equal(t, []string{filepath.ToSlash(poisoned)}, f.staged(t), "only the other command's entry may remain staged")
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
