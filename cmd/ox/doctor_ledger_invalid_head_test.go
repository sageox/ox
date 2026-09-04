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
	"github.com/sageox/ox/internal/gitutil"
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
	cmd := exec.Command("git", "add", "-A")
	cmd.Dir = cloneDir
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

func TestInvalidHeadCheck_NotAGitRepoSkips(t *testing.T) {
	result := invalidHeadCheck(t.TempDir(), false)
	assert.True(t, result.skipped)
}

func TestDetectInvalidHead_UnreadableHEADIsNotThisShape(t *testing.T) {
	// no .git/HEAD at all (as opposed to one containing a bad ref) — a
	// different failure mode this check must not misclassify.
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".git"), 0o755))
	ref, corrupted := detectInvalidHead(context.Background(), root)
	assert.False(t, corrupted)
	assert.Empty(t, ref)
}

// checkLedgerInvalidHead is the getLedgerPath()-based wrapper around
// invalidHeadCheck; every other test in this file calls invalidHeadCheck
// directly against a fixture path, so this is the only thing that exercises
// the wrapper itself. It can't control what ledger (if any) getLedgerPath()
// resolves to outside a configured project, so this only asserts the call
// completes without panicking — invalidHeadCheck's behavior for every input
// shape is already proven directly above.
func TestCheckLedgerInvalidHead_CallsThroughToInvalidHeadCheck(t *testing.T) {
	assert.NotPanics(t, func() {
		checkLedgerInvalidHead(false)
	})
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
	runGitLedgerT(t, cloneDir, "rev-parse", "--verify", "-q", "HEAD")

	// the staged content must have survived the reclone, committed.
	sessionContent, err := os.ReadFile(filepath.Join(cloneDir, "sessions", "s1.txt"))
	require.NoError(t, err, "sessions/s1.txt must survive the repair")
	assert.Equal(t, "session content\n", string(sessionContent))
	dataContent, err := os.ReadFile(filepath.Join(cloneDir, "data", "d1.txt"))
	require.NoError(t, err, "data/d1.txt must survive the repair")
	assert.Equal(t, "data content\n", string(dataContent))

	statusOut := runGitLedgerT(t, cloneDir, "status", "--porcelain")
	assert.Empty(t, statusOut, "restored content must be committed, not left staged")

	// the corrupted original must be preserved, never deleted, per
	// .claude/rules/daemon-git.md "never discard uncommitted changes".
	backups, err := filepath.Glob(cloneDir + ".corrupt-backup-*")
	require.NoError(t, err)
	require.Len(t, backups, 1, "the corrupted clone must be preserved as a backup, not deleted")
	backupSession, err := os.ReadFile(filepath.Join(backups[0], "sessions", "s1.txt"))
	require.NoError(t, err)
	assert.Equal(t, "session content\n", string(backupSession), "backup must retain the original staged content untouched")
}

// TestInvalidHeadCheck_FixIdenticalContentIsNoOp covers restoreLedgerDir's
// "already present, identical content" path: if the backup's file and the
// fresh clone's file at the same relative path happen to match byte-for-
// byte, that's not a conflict — it's copied over as a no-op and the repair
// proceeds normally.
func TestInvalidHeadCheck_FixIdenticalContentIsNoOp(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	isolateLedgerRepairCredentials(t)

	fakeEndpoint := "https://fake-ledger-repair-identical-test.invalid"
	t.Setenv(endpoint.EnvVar, fakeEndpoint)
	require.NoError(t, gitserver.SaveCredentialsForEndpoint(fakeEndpoint, gitserver.GitCredentials{
		Token:     "test-token",
		ServerURL: fakeEndpoint,
		Username:  "oauth2",
		ExpiresAt: time.Now().Add(24 * time.Hour),
	}))

	bareDir, cloneDir := setupHealthyLedgerClone(t)
	httpURL := serveDumbHTTP(t, bareDir)
	runGitLedgerT(t, cloneDir, "remote", "set-url", "origin", httpURL)

	// origin already has sessions/s1.txt with content X...
	writerDir := filepath.Join(t.TempDir(), "writer")
	runGitLedgerT(t, filepath.Dir(bareDir), "clone", "--quiet", bareDir, writerDir)
	runGitLedgerT(t, writerDir, "config", "user.email", "test@example.com")
	runGitLedgerT(t, writerDir, "config", "user.name", "Test User")
	require.NoError(t, os.MkdirAll(filepath.Join(writerDir, "sessions"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(writerDir, "sessions", "s1.txt"), []byte("identical content\n"), 0o644))
	runGitLedgerT(t, writerDir, "add", "-A")
	runGitLedgerT(t, writerDir, "commit", "-q", "-m", "session already on origin")
	runGitLedgerT(t, writerDir, "push", "-q", "origin", "main")
	runGitLedgerT(t, bareDir, "update-server-info")

	// ...and the corrupted local backup independently staged the SAME
	// content X at the same path — not a conflict, just redundant.
	corruptHEADToInvalidRef(t, cloneDir)
	require.NoError(t, os.WriteFile(filepath.Join(cloneDir, "sessions", "s1.txt"), []byte("identical content\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(cloneDir, "data", "d1.txt"), []byte("still new\n"), 0o644))
	cmd := exec.Command("git", "add", "-A")
	cmd.Dir = cloneDir
	require.NoError(t, cmd.Run())

	result := invalidHeadCheck(cloneDir, true)

	require.True(t, result.passed, "identical content must not be treated as a conflict: %s / %s", result.message, result.detail)
	sessionContent, err := os.ReadFile(filepath.Join(cloneDir, "sessions", "s1.txt"))
	require.NoError(t, err)
	assert.Equal(t, "identical content\n", string(sessionContent))
	dataContent, err := os.ReadFile(filepath.Join(cloneDir, "data", "d1.txt"))
	require.NoError(t, err)
	assert.Equal(t, "still new\n", string(dataContent), "the genuinely-new file must still be restored alongside the identical one")
}

// TestRepairInvalidHead_NoOriginRemoteFailsCleanly covers repairInvalidHead's
// earliest failure branch: a clone with no "origin" remote configured (or
// one that's been removed) can't be re-cloned from anywhere. Must fail
// before touching the corrupted clone at all.
func TestRepairInvalidHead_NoOriginRemoteFailsCleanly(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git operations")
	}
	_, cloneDir := setupHealthyLedgerClone(t)
	corruptHEADToInvalidRef(t, cloneDir)
	runGitLedgerT(t, cloneDir, "remote", "remove", "origin")

	result := invalidHeadCheck(cloneDir, true)

	assert.False(t, result.passed)
	assert.Contains(t, result.message, "remote URL")

	// nothing was touched — the failure happened before any rename.
	head, err := os.ReadFile(filepath.Join(cloneDir, ".git", "HEAD"))
	require.NoError(t, err)
	assert.Equal(t, "ref: refs/heads/.invalid\n", string(head))
	backups, err := filepath.Glob(cloneDir + ".corrupt-backup-*")
	require.NoError(t, err)
	assert.Empty(t, backups)
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

// TestRepairInvalidHead_RefusesConflictingContent covers the case flagged on
// review: the backed-up (corrupted) clone could in principle be BEHIND
// origin, not just a never-completed first clone. If origin already has a
// DIFFERENT version of a path the backup also wants to restore, blindly
// overwriting it would publish stale content over what origin already has.
// The repair must refuse instead of guessing, and leave the original
// exactly as it was.
func TestRepairInvalidHead_RefusesConflictingContent(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	isolateLedgerRepairCredentials(t)

	fakeEndpoint := "https://fake-ledger-repair-conflict-test.invalid"
	t.Setenv(endpoint.EnvVar, fakeEndpoint)
	require.NoError(t, gitserver.SaveCredentialsForEndpoint(fakeEndpoint, gitserver.GitCredentials{
		Token:     "test-token",
		ServerURL: fakeEndpoint,
		Username:  "oauth2",
		ExpiresAt: time.Now().Add(24 * time.Hour),
	}))

	bareDir, cloneDir := setupHealthyLedgerClone(t)
	httpURL := serveDumbHTTP(t, bareDir)
	runGitLedgerT(t, cloneDir, "remote", "set-url", "origin", httpURL)

	// corrupt the local clone with STALE versions of TWO paths staged, BOTH
	// under sessions/ — proves the "(+N more)" summary for multiple conflicts
	// in the same directory, not just the single-conflict case...
	corruptHEADToInvalidRef(t, cloneDir)
	require.NoError(t, os.WriteFile(filepath.Join(cloneDir, "sessions", "s1.txt"), []byte("STALE local content 1\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(cloneDir, "sessions", "s2.txt"), []byte("STALE local content 2\n"), 0o644))
	cmd := exec.Command("git", "add", "-A")
	cmd.Dir = cloneDir
	require.NoError(t, cmd.Run())

	// ...while origin independently gains DIFFERENT versions of both exact
	// paths via a separate writer clone, pushed AFTER the corruption above —
	// simulating a backup that is behind origin, not just a fresh clone that
	// never finished.
	writerDir := filepath.Join(t.TempDir(), "writer")
	runGitLedgerT(t, filepath.Dir(bareDir), "clone", "--quiet", bareDir, writerDir)
	runGitLedgerT(t, writerDir, "config", "user.email", "test@example.com")
	runGitLedgerT(t, writerDir, "config", "user.name", "Test User")
	require.NoError(t, os.MkdirAll(filepath.Join(writerDir, "sessions"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(writerDir, "sessions", "s1.txt"), []byte("NEWER origin content 1\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(writerDir, "sessions", "s2.txt"), []byte("NEWER origin content 2\n"), 0o644))
	runGitLedgerT(t, writerDir, "add", "-A")
	runGitLedgerT(t, writerDir, "commit", "-q", "-m", "newer content from origin")
	runGitLedgerT(t, writerDir, "push", "-q", "origin", "main")
	// re-serve so the dumb-HTTP mirror picks up the new commit before reclone.
	runGitLedgerT(t, bareDir, "update-server-info")

	result := invalidHeadCheck(cloneDir, true)

	assert.False(t, result.passed, "repair must refuse when backup content conflicts with origin's current content")
	assert.Contains(t, result.message, "differ")
	assert.Contains(t, result.message, "more", "a second conflict in the same dir must be summarized, not silently dropped")

	// the original corrupted clone must be restored EXACTLY — including the
	// stale content that was staged, untouched — so nothing is silently
	// discarded or published.
	head, err := os.ReadFile(filepath.Join(cloneDir, ".git", "HEAD"))
	require.NoError(t, err)
	assert.Equal(t, "ref: refs/heads/.invalid\n", string(head))
	staleContent, err := os.ReadFile(filepath.Join(cloneDir, "sessions", "s1.txt"))
	require.NoError(t, err)
	assert.Equal(t, "STALE local content 1\n", string(staleContent), "the stale local content must survive the refused repair untouched")

	backups, err := filepath.Glob(cloneDir + ".corrupt-backup-*")
	require.NoError(t, err)
	assert.Empty(t, backups, "a rolled-back conflict must not leave an orphaned backup dir")
}

// TestRepairInvalidHead_NothingToRestoreIsAWarningNotAFailure covers the
// case where the corrupted backup never got as far as staging any
// sessions/ or data/ content at all (e.g. the clone dropped before the
// daemon wrote anything beyond the initial checkout) — a repair with
// nothing to restore is still a genuine repair (HEAD now resolves), just
// with nothing to commit on top of it.
func TestRepairInvalidHead_NothingToRestoreIsAWarningNotAFailure(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	isolateLedgerRepairCredentials(t)

	fakeEndpoint := "https://fake-ledger-repair-empty-test.invalid"
	t.Setenv(endpoint.EnvVar, fakeEndpoint)
	require.NoError(t, gitserver.SaveCredentialsForEndpoint(fakeEndpoint, gitserver.GitCredentials{
		Token:     "test-token",
		ServerURL: fakeEndpoint,
		Username:  "oauth2",
		ExpiresAt: time.Now().Add(24 * time.Hour),
	}))

	bareDir, cloneDir := setupHealthyLedgerClone(t)
	httpURL := serveDumbHTTP(t, bareDir)
	runGitLedgerT(t, cloneDir, "remote", "set-url", "origin", httpURL)

	// corrupt HEAD only — no sessions/ or data/ ever got created, so there's
	// nothing beyond the corruption itself to restore.
	require.NoError(t, os.WriteFile(filepath.Join(cloneDir, ".git", "HEAD"), []byte("ref: refs/heads/.invalid\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(cloneDir, ".git", "refs", "heads"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cloneDir, ".git", "refs", "heads", ".invalid"), nil, 0o644))

	result := invalidHeadCheck(cloneDir, true)

	require.True(t, result.passed, "a repair with nothing to restore is still a successful repair: %s / %s", result.message, result.detail)
	assert.True(t, result.warning)
	assert.Contains(t, result.message, "nothing to restore")

	runGitLedgerT(t, cloneDir, "rev-parse", "--verify", "-q", "HEAD")

	backups, err := filepath.Glob(cloneDir + ".corrupt-backup-*")
	require.NoError(t, err)
	assert.Len(t, backups, 1, "the corrupted original is still preserved even when there was nothing to restore from it")
}

// TestRepairInvalidHead_CommitFailureSurfacesButKeepsRepair covers the
// branch where reclone + restore both succeed (no conflicts) but the
// subsequent commit fails — fixLedgerDirtyWorkdir already refuses to
// auto-commit content carrying literal git conflict markers. This is NOT
// corruption (HEAD resolves fine, the content is sitting right there on
// disk, staged) so — unlike a reclone or restore failure — this must NOT
// roll back; it's a normal "dirty workdir, needs a human" state the
// existing clean-workdir check already knows how to report on the next run.
func TestRepairInvalidHead_CommitFailureSurfacesButKeepsRepair(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	isolateLedgerRepairCredentials(t)

	fakeEndpoint := "https://fake-ledger-repair-commitfail-test.invalid"
	t.Setenv(endpoint.EnvVar, fakeEndpoint)
	require.NoError(t, gitserver.SaveCredentialsForEndpoint(fakeEndpoint, gitserver.GitCredentials{
		Token:     "test-token",
		ServerURL: fakeEndpoint,
		Username:  "oauth2",
		ExpiresAt: time.Now().Add(24 * time.Hour),
	}))

	bareDir, cloneDir := setupHealthyLedgerClone(t)
	httpURL := serveDumbHTTP(t, bareDir)
	runGitLedgerT(t, cloneDir, "remote", "set-url", "origin", httpURL)

	corruptHEADToInvalidRef(t, cloneDir)
	// overwrite the staged session content with literal conflict markers —
	// firstUnstageableFileInIndex (session_upload.go) refuses to auto-commit
	// this, the same guard the routine session-upload commit path uses.
	require.NoError(t, os.WriteFile(filepath.Join(cloneDir, "sessions", "s1.txt"),
		[]byte("<<<<<<< HEAD\nours\n=======\ntheirs\n>>>>>>> branch\n"), 0o644))
	cmd := exec.Command("git", "add", "-A")
	cmd.Dir = cloneDir
	require.NoError(t, cmd.Run())

	result := invalidHeadCheck(cloneDir, true)

	assert.False(t, result.passed, "a commit-time refusal must surface as a failure")
	assert.Contains(t, result.detail, "backup at")

	// unlike a reclone/restore failure, this must NOT roll back: HEAD now
	// resolves and the restored content is genuinely on disk, just uncommitted.
	runGitLedgerT(t, cloneDir, "rev-parse", "--verify", "-q", "HEAD")
	content, err := os.ReadFile(filepath.Join(cloneDir, "sessions", "s1.txt"))
	require.NoError(t, err)
	assert.Contains(t, string(content), "<<<<<<<", "the conflicted content must still be present, uncommitted, for a human to resolve")

	backups, err := filepath.Glob(cloneDir + ".corrupt-backup-*")
	require.NoError(t, err)
	assert.Len(t, backups, 1, "the backup must still be kept as a safety net")
}

// TestRepairInvalidHead_RenameAsideFails covers the earliest failure point
// inside repairInvalidHead itself: moving the corrupted clone aside can fail
// (e.g. a read-only parent directory) before any network call is made. Must
// fail cleanly with no clone attempted and nothing renamed.
func TestRepairInvalidHead_RenameAsideFails(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git operations")
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission-denied fixtures don't apply")
	}
	_, cloneDir := setupHealthyLedgerClone(t)
	corruptHEADToInvalidRef(t, cloneDir)

	// remove write permission on the parent directory — renaming cloneDir
	// aside requires modifying ITS parent's directory entries, not cloneDir
	// itself, so read/traverse (needed for the git commands that run first)
	// still works.
	parent := filepath.Dir(cloneDir)
	require.NoError(t, os.Chmod(parent, 0o555))
	t.Cleanup(func() { _ = os.Chmod(parent, 0o755) })

	result := invalidHeadCheck(cloneDir, true)

	assert.False(t, result.passed)
	assert.Contains(t, result.message, "could not move the corrupted clone aside")
}

// TestRepairInvalidHead_RestoreErrorTriggersRollback covers restoreLedgerDir
// returning a genuine error (as opposed to a content conflict): origin now
// has a plain FILE at "sessions" where the backup has a whole directory of
// staged content. Creating the destination directory fails outright, and
// that failure must roll back exactly like a conflict or a reclone failure —
// nothing published, original preserved.
func TestRepairInvalidHead_RestoreErrorTriggersRollback(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	isolateLedgerRepairCredentials(t)

	fakeEndpoint := "https://fake-ledger-repair-restoreerr-test.invalid"
	t.Setenv(endpoint.EnvVar, fakeEndpoint)
	require.NoError(t, gitserver.SaveCredentialsForEndpoint(fakeEndpoint, gitserver.GitCredentials{
		Token:     "test-token",
		ServerURL: fakeEndpoint,
		Username:  "oauth2",
		ExpiresAt: time.Now().Add(24 * time.Hour),
	}))

	bareDir, cloneDir := setupHealthyLedgerClone(t)
	httpURL := serveDumbHTTP(t, bareDir)
	runGitLedgerT(t, cloneDir, "remote", "set-url", "origin", httpURL)

	writerDir := filepath.Join(t.TempDir(), "writer")
	runGitLedgerT(t, filepath.Dir(bareDir), "clone", "--quiet", bareDir, writerDir)
	runGitLedgerT(t, writerDir, "config", "user.email", "test@example.com")
	runGitLedgerT(t, writerDir, "config", "user.name", "Test User")
	require.NoError(t, os.WriteFile(filepath.Join(writerDir, "sessions"), []byte("not a directory\n"), 0o644))
	runGitLedgerT(t, writerDir, "add", "-A")
	runGitLedgerT(t, writerDir, "commit", "-q", "-m", "sessions becomes a file upstream")
	runGitLedgerT(t, writerDir, "push", "-q", "origin", "main")
	runGitLedgerT(t, bareDir, "update-server-info")

	corruptHEADToInvalidRef(t, cloneDir)

	result := invalidHeadCheck(cloneDir, true)

	assert.False(t, result.passed, "restoring into a path origin now uses for something else must fail, not silently drop content")
	assert.Contains(t, result.message, "restoring")

	head, err := os.ReadFile(filepath.Join(cloneDir, ".git", "HEAD"))
	require.NoError(t, err)
	assert.Equal(t, "ref: refs/heads/.invalid\n", string(head))
	backups, err := filepath.Glob(cloneDir + ".corrupt-backup-*")
	require.NoError(t, err)
	assert.Empty(t, backups, "a rolled-back restore failure must not leave an orphaned backup dir")
}

// --- restoreLedgerDir / filesIdentical: direct unit tests for the
// walk-error and read-error branches. These are pure filesystem functions,
// so the permission fixtures can be set up before the walk even starts —
// unlike the git-backed repairInvalidHead tests above, no clone timing
// window to work around.

func TestRestoreLedgerDir_WalkErrorOnUnreadableSubdir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission-denied fixtures don't apply")
	}
	src := t.TempDir()
	dst := t.TempDir()

	blocked := filepath.Join(src, "blocked")
	require.NoError(t, os.MkdirAll(blocked, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(blocked, "f.txt"), []byte("x"), 0o644))
	require.NoError(t, os.Chmod(blocked, 0o000))
	t.Cleanup(func() { _ = os.Chmod(blocked, 0o755) })

	_, err := restoreLedgerDir(src, dst)
	assert.Error(t, err, "an unreadable subdirectory must surface as an error, not be silently skipped")
}

func TestRestoreLedgerDir_ConflictCheckReadFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission-denied fixtures don't apply")
	}

	t.Run("unreadable source file", func(t *testing.T) {
		src := t.TempDir()
		dst := t.TempDir()
		srcFile := filepath.Join(src, "s1.txt")
		dstFile := filepath.Join(dst, "s1.txt")
		require.NoError(t, os.WriteFile(srcFile, []byte("src content\n"), 0o644))
		require.NoError(t, os.WriteFile(dstFile, []byte("dst content\n"), 0o644))
		require.NoError(t, os.Chmod(srcFile, 0o000))
		t.Cleanup(func() { _ = os.Chmod(srcFile, 0o644) })

		_, err := restoreLedgerDir(src, dst)
		assert.Error(t, err, "a source file the process can't read must surface as an error, not be treated as a conflict or skipped")
	})

	t.Run("unreadable destination file", func(t *testing.T) {
		src := t.TempDir()
		dst := t.TempDir()
		srcFile := filepath.Join(src, "s1.txt")
		dstFile := filepath.Join(dst, "s1.txt")
		require.NoError(t, os.WriteFile(srcFile, []byte("src content\n"), 0o644))
		require.NoError(t, os.WriteFile(dstFile, []byte("dst content\n"), 0o644))
		require.NoError(t, os.Chmod(dstFile, 0o000))
		t.Cleanup(func() { _ = os.Chmod(dstFile, 0o644) })

		_, err := restoreLedgerDir(src, dst)
		assert.Error(t, err, "a destination file the process can't read must surface as an error, not be silently overwritten")
	})
}

func TestFilesIdentical_ReadErrors(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission-denied fixtures don't apply")
	}
	dir := t.TempDir()
	a := filepath.Join(dir, "a.txt")
	b := filepath.Join(dir, "b.txt")
	require.NoError(t, os.WriteFile(a, []byte("a"), 0o644))
	require.NoError(t, os.WriteFile(b, []byte("b"), 0o644))

	_, err := filesIdentical(filepath.Join(dir, "missing.txt"), b)
	assert.Error(t, err, "an unreadable first file must surface as an error")

	_, err = filesIdentical(a, filepath.Join(dir, "missing2.txt"))
	assert.Error(t, err, "an unreadable second file must surface as an error")
}

// TestFixLedgerInvalidHead_SerializesWithPreCloneLock covers the review
// finding that fixLedgerInvalidHead ran completely unlocked: a concurrent
// daemon Checkout() (or another doctor repair) racing the same path could
// observe a missing or partial ledger. The repair must take the same
// gitutil.WithPreCloneLock Checkout() takes before cloning, so a peer
// holding the lock blocks the repair from ever starting — proven here by
// asserting the corrupted clone is completely untouched when the lock
// cannot be acquired in time.
func TestFixLedgerInvalidHead_SerializesWithPreCloneLock(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git operations")
	}
	_, cloneDir := setupHealthyLedgerClone(t)
	corruptHEADToInvalidRef(t, cloneDir)

	headBefore, err := os.ReadFile(filepath.Join(cloneDir, ".git", "HEAD"))
	require.NoError(t, err)

	// hold the pre-clone lock on this exact path from outside, as if a
	// concurrent daemon Checkout() (or another repair) were already running.
	holding := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = gitutil.WithPreCloneLock(context.Background(), cloneDir, func() error {
			close(holding)
			<-release
			return nil
		})
	}()
	<-holding
	defer func() {
		close(release)
		<-done // wait for the holder's unlock to finish before t.TempDir() cleanup runs
	}()

	shortCtx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	result := fixLedgerInvalidHead(shortCtx, cloneDir, "refs/heads/.invalid")

	assert.False(t, result.passed)
	assert.Contains(t, result.message, "could not acquire the ledger clone lock")

	// the corrupted clone must be completely untouched — the lock gated
	// entry into the repair before it ever renamed anything aside.
	headAfter, err := os.ReadFile(filepath.Join(cloneDir, ".git", "HEAD"))
	require.NoError(t, err)
	assert.Equal(t, string(headBefore), string(headAfter))
	backups, err := filepath.Glob(cloneDir + ".corrupt-backup-*")
	require.NoError(t, err)
	assert.Empty(t, backups, "a repair blocked by the lock must never have started renaming anything")
}
