package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/gitserver"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ox-baz5.6 — a ledger clone that dropped mid-transfer left .git/HEAD
// pointing at refs/heads/.invalid: a syntactically illegal branch name git
// refuses to resolve. Every subsequent commit failed ("cannot lock ref
// HEAD"), and every other ledger doctor check silently skipped or errored
// cryptically because each assumes HEAD resolves. These tests reproduce the
// exact corruption (verified manually against real git before writing this
// file: `git status --porcelain` reports every tracked file as staged-new,
// `git commit` fails with "cannot lock ref 'HEAD': unable to resolve
// reference 'refs/heads/.invalid': reference broken") and drive the
// detect + repair path against it.

// runGitLedgerT runs a git command in dir, failing the test on error.
func runGitLedgerT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
	return string(out)
}

// setupHealthyLedgerClone creates a bare remote with one commit and a local
// clone of it — a normal, uncorrupted ledger.
func setupHealthyLedgerClone(t *testing.T) (bareDir, cloneDir string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()
	bareDir = filepath.Join(root, "bare.git")
	work := filepath.Join(root, "work")
	runGitLedgerT(t, root, "init", "--bare", "--initial-branch=main", bareDir)
	runGitLedgerT(t, root, "clone", "--quiet", bareDir, work)
	runGitLedgerT(t, work, "config", "user.email", "test@example.com")
	runGitLedgerT(t, work, "config", "user.name", "Test User")
	require.NoError(t, os.WriteFile(filepath.Join(work, "seed.txt"), []byte("seed\n"), 0o644))
	runGitLedgerT(t, work, "add", "-A")
	runGitLedgerT(t, work, "commit", "-q", "-m", "seed")
	runGitLedgerT(t, work, "push", "-q", "origin", "main")

	cloneDir = filepath.Join(root, "ledger")
	runGitLedgerT(t, root, "clone", "--quiet", bareDir, cloneDir)
	runGitLedgerT(t, cloneDir, "config", "user.email", "test@example.com")
	runGitLedgerT(t, cloneDir, "config", "user.name", "Test User")
	return bareDir, cloneDir
}

// corruptHEADToInvalidRef reproduces the ox-baz5.6 shape in-place on an
// existing clone: HEAD -> refs/heads/.invalid (a name git's own
// check-ref-format rejects), the matching ref file present, and
// sessions/data content staged but never committed.
func corruptHEADToInvalidRef(t *testing.T, cloneDir string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(cloneDir, ".git", "HEAD"), []byte("ref: refs/heads/.invalid\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(cloneDir, ".git", "refs", "heads"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cloneDir, ".git", "refs", "heads", ".invalid"), nil, 0o644))

	require.NoError(t, os.MkdirAll(filepath.Join(cloneDir, "sessions"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(cloneDir, "data"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cloneDir, "sessions", "s1.txt"), []byte("session content\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(cloneDir, "data", "d1.txt"), []byte("data content\n"), 0o644))

	// staging works even with HEAD broken — this is what makes the corruption
	// so confusing in the field: `git add` succeeds, `git commit` doesn't.
	cmd := exec.Command("git", "-C", cloneDir, "add", "-A")
	require.NoError(t, cmd.Run())
}

// --- detectInvalidHead ---

func TestDetectInvalidHead_HealthyClone(t *testing.T) {
	_, cloneDir := setupHealthyLedgerClone(t)
	ref, corrupted := detectInvalidHead(context.Background(), cloneDir)
	assert.False(t, corrupted)
	assert.Empty(t, ref)
}

func TestDetectInvalidHead_UnbornBranchIsNotThisShape(t *testing.T) {
	// a valid branch name with zero commits — unbornLedgerFailure's territory,
	// not this check's. Must not be claimed here.
	root := t.TempDir()
	runGitLedgerT(t, root, "init", "-q", "--initial-branch=main", root)
	ref, corrupted := detectInvalidHead(context.Background(), root)
	assert.False(t, corrupted, "an unborn branch with a VALID name is not the ox-baz5.6 shape")
	assert.Empty(t, ref)
}

func TestDetectInvalidHead_DetachedHeadIsNotThisShape(t *testing.T) {
	_, cloneDir := setupHealthyLedgerClone(t)
	sha := runGitLedgerT(t, cloneDir, "rev-parse", "HEAD")
	runGitLedgerT(t, cloneDir, "checkout", "-q", trimNL(sha))
	ref, corrupted := detectInvalidHead(context.Background(), cloneDir)
	assert.False(t, corrupted)
	assert.Empty(t, ref)
}

func TestDetectInvalidHead_InvalidRefIsDetected(t *testing.T) {
	_, cloneDir := setupHealthyLedgerClone(t)
	corruptHEADToInvalidRef(t, cloneDir)

	ref, corrupted := detectInvalidHead(context.Background(), cloneDir)
	assert.True(t, corrupted)
	assert.Equal(t, "refs/heads/.invalid", ref)
}

func trimNL(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}

// --- invalidHeadCheck (report path) ---

func TestInvalidHeadCheck_CleanLedgerPasses(t *testing.T) {
	_, cloneDir := setupHealthyLedgerClone(t)
	result := invalidHeadCheck(cloneDir, false)
	assert.True(t, result.passed)
	assert.False(t, result.warning)
}

func TestInvalidHeadCheck_ReportsWithoutFix(t *testing.T) {
	_, cloneDir := setupHealthyLedgerClone(t)
	corruptHEADToInvalidRef(t, cloneDir)

	result := invalidHeadCheck(cloneDir, false)
	assert.False(t, result.passed)
	assert.Equal(t, "critical", result.priority)
	assert.Contains(t, result.message, ".invalid")

	// report-only must not touch the corrupted clone at all.
	head, err := os.ReadFile(filepath.Join(cloneDir, ".git", "HEAD"))
	require.NoError(t, err)
	assert.Equal(t, "ref: refs/heads/.invalid\n", string(head), "reporting must not repair")
}

func TestInvalidHeadCheck_NoLedgerSkips(t *testing.T) {
	result := invalidHeadCheck("", false)
	assert.True(t, result.skipped)
}

// --- fix path: the real repair, against a real local git server ---

// isolateLedgerRepairCredentials points gitserver's credential store at a
// throwaway temp dir (never the real machine's ~/.sageox) and forces file
// storage instead of the OS keychain, matching internal/daemon's
// isolateCredentialsWithDir pattern.
func isolateLedgerRepairCredentials(t *testing.T) {
	t.Helper()
	prevConfigDir := gitserver.TestSetConfigDirOverride(t.TempDir())
	prevForceFile := gitserver.TestSetForceFileStorage(true)
	t.Cleanup(func() {
		gitserver.TestSetConfigDirOverride(prevConfigDir)
		gitserver.TestSetForceFileStorage(prevForceFile)
	})
}

// serveDumbHTTP serves a bare repo over dumb HTTP (a plain static file
// server) and returns the URL. gitserver's URL validation (like sync.go's
// isValidCloneURL) allows http:// only for localhost/127.0.0.1, which
// httptest.NewServer binds to; file:// is not an option for either path.
func serveDumbHTTP(t *testing.T, bareDir string) string {
	t.Helper()
	runGitLedgerT(t, bareDir, "update-server-info")
	srv := httptest.NewServer(http.FileServer(http.Dir(bareDir)))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestInvalidHeadCheck_FixReClonesAndRestoresStagedContent(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	isolateLedgerRepairCredentials(t)

	fakeEndpoint := "https://fake-ledger-repair-test.invalid"
	t.Setenv(endpoint.EnvVar, fakeEndpoint)
	require.NoError(t, gitserver.SaveCredentialsForEndpoint(fakeEndpoint, gitserver.GitCredentials{
		Token:     "test-token",
		ServerURL: fakeEndpoint,
		Username:  "oauth2",
		ExpiresAt: time.Now().Add(24 * time.Hour),
	}))

	bareDir, cloneDir := setupHealthyLedgerClone(t)
	httpURL := serveDumbHTTP(t, bareDir)
	// point the corrupted clone's origin at the HTTP mirror the fix will
	// re-clone from — production ledgers always have a real http(s) origin;
	// the on-disk bare path is only reachable in this test harness.
	runGitLedgerT(t, cloneDir, "remote", "set-url", "origin", httpURL)

	corruptHEADToInvalidRef(t, cloneDir)

	result := invalidHeadCheck(cloneDir, true)

	require.True(t, result.passed, "fix should succeed: %s / %s", result.message, result.detail)
	assert.True(t, result.warning, "a repair is reported as a warning, not a silent pass")
	assert.Contains(t, result.message, "repaired")

	// the repaired clone must be healthy: HEAD resolves.
	out, err := exec.Command("git", "-C", cloneDir, "rev-parse", "--verify", "-q", "HEAD").CombinedOutput()
	require.NoError(t, err, string(out))

	// the staged content must have survived the reclone, committed.
	sessionContent, err := os.ReadFile(filepath.Join(cloneDir, "sessions", "s1.txt"))
	require.NoError(t, err, "sessions/s1.txt must survive the repair")
	assert.Equal(t, "session content\n", string(sessionContent))
	dataContent, err := os.ReadFile(filepath.Join(cloneDir, "data", "d1.txt"))
	require.NoError(t, err, "data/d1.txt must survive the repair")
	assert.Equal(t, "data content\n", string(dataContent))

	statusOut, err := exec.Command("git", "-C", cloneDir, "status", "--porcelain").CombinedOutput()
	require.NoError(t, err)
	assert.Empty(t, string(statusOut), "restored content must be committed, not left staged")

	// the corrupted original must be preserved, never deleted, per
	// .claude/rules/daemon-git.md "never discard uncommitted changes".
	backups, err := filepath.Glob(cloneDir + ".corrupt-backup-*")
	require.NoError(t, err)
	require.Len(t, backups, 1, "the corrupted clone must be preserved as a backup, not deleted")
	backupSession, err := os.ReadFile(filepath.Join(backups[0], "sessions", "s1.txt"))
	require.NoError(t, err)
	assert.Equal(t, "session content\n", string(backupSession), "backup must retain the original staged content untouched")
}

func TestInvalidHeadCheck_FixIsNonDestructiveWhenRecloneFails(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git operations")
	}
	isolateLedgerRepairCredentials(t)

	fakeEndpoint := "https://fake-ledger-repair-fail-test.invalid"
	t.Setenv(endpoint.EnvVar, fakeEndpoint)
	require.NoError(t, gitserver.SaveCredentialsForEndpoint(fakeEndpoint, gitserver.GitCredentials{
		Token:     "test-token",
		ServerURL: fakeEndpoint,
		Username:  "oauth2",
		ExpiresAt: time.Now().Add(24 * time.Hour),
	}))

	_, cloneDir := setupHealthyLedgerClone(t)

	// a real HTTP server that 404s everything — the reclone must fail cleanly.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	runGitLedgerT(t, cloneDir, "remote", "set-url", "origin", srv.URL)

	corruptHEADToInvalidRef(t, cloneDir)

	result := invalidHeadCheck(cloneDir, true)

	assert.False(t, result.passed, "fix must fail when the reclone itself fails")

	// the original corrupted content must be restored to its EXACT original
	// path — non-destructive failure, not "moved somewhere and abandoned".
	sessionContent, err := os.ReadFile(filepath.Join(cloneDir, "sessions", "s1.txt"))
	require.NoError(t, err, "original clone must be restored to cloneDir on reclone failure")
	assert.Equal(t, "session content\n", string(sessionContent))

	head, err := os.ReadFile(filepath.Join(cloneDir, ".git", "HEAD"))
	require.NoError(t, err)
	assert.Equal(t, "ref: refs/heads/.invalid\n", string(head))

	// no orphaned backup directory when the restore succeeded.
	backups, err := filepath.Glob(cloneDir + ".corrupt-backup-*")
	require.NoError(t, err)
	assert.Empty(t, backups, "a successfully-restored failure should not leave an orphaned backup dir")
}
