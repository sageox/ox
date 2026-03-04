package daemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"log/slog"

	"github.com/sageox/ox/internal/gitutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDoPull_StaleLockFileDetection verifies that doPull skips the pull and
// reports an issue when stale .git lock files exist. The daemon intentionally
// does NOT remove lock files (a running git process may own them); instead it
// skips the pull and sets an IssueTypeGitLock issue so ox doctor can surface it.
func TestDoPull_StaleLockFileDetection(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	tmpDir := t.TempDir()
	ledgerDir := filepath.Join(tmpDir, "ledger")
	require.NoError(t, os.MkdirAll(ledgerDir, 0755))
	setupGitRepo(t, ledgerDir)

	// plant a stale index.lock as if a previous git process crashed
	lockPath := filepath.Join(ledgerDir, ".git", "index.lock")
	require.NoError(t, os.WriteFile(lockPath, []byte("stale"), 0644))

	cfg := DefaultConfig()
	cfg.LedgerPath = ledgerDir
	cfg.SyncIntervalRead = 1 * time.Second
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	scheduler := NewSyncScheduler(cfg, logger)
	issues := NewIssueTracker()
	scheduler.SetIssueTracker(issues)

	ctx := context.Background()
	err := scheduler.doPull(ctx, nil, false)

	// doPull returns nil (skips gracefully) rather than erroring
	assert.NoError(t, err, "doPull should skip gracefully when lock files exist")

	// verify the lock file was NOT removed (daemon doesn't auto-delete)
	_, statErr := os.Stat(lockPath)
	assert.NoError(t, statErr, "lock file should still exist; daemon does not auto-remove")

	// verify an issue was reported
	allIssues := issues.GetIssues()
	require.Len(t, allIssues, 1, "expected exactly one issue for the stale lock")
	assert.Equal(t, IssueTypeGitLock, allIssues[0].Type)
	assert.Equal(t, "ledger", allIssues[0].Repo)
	assert.Contains(t, allIssues[0].Summary, "index.lock")
}

// TestDoPull_LockFileClearedAfterResolution verifies that the git lock issue
// is cleared on the next successful doPull after the lock file is removed.
func TestDoPull_LockFileClearedAfterResolution(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	tmpDir := t.TempDir()
	ledgerDir := filepath.Join(tmpDir, "ledger")
	require.NoError(t, os.MkdirAll(ledgerDir, 0755))
	setupGitRepo(t, ledgerDir)

	cfg := DefaultConfig()
	cfg.LedgerPath = ledgerDir
	cfg.SyncIntervalRead = 1 * time.Second
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	scheduler := NewSyncScheduler(cfg, logger)
	issues := NewIssueTracker()
	scheduler.SetIssueTracker(issues)

	ctx := context.Background()

	// first: create lock file, trigger doPull to set the issue
	lockPath := filepath.Join(ledgerDir, ".git", "index.lock")
	require.NoError(t, os.WriteFile(lockPath, []byte("stale"), 0644))
	_ = scheduler.doPull(ctx, nil, false)
	require.Len(t, issues.GetIssues(), 1, "issue should be set")

	// now remove the lock file and run doPull again
	require.NoError(t, os.Remove(lockPath))
	_ = scheduler.doPull(ctx, nil, false)

	// the git lock issue should be cleared
	for _, issue := range issues.GetIssues() {
		assert.NotEqual(t, IssueTypeGitLock, issue.Type,
			"git lock issue should be cleared after lock file is removed")
	}
}

// TestDoPull_MultipleLockFiles verifies that all known lock file types are detected.
func TestDoPull_MultipleLockFiles(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	tmpDir := t.TempDir()
	ledgerDir := filepath.Join(tmpDir, "ledger")
	require.NoError(t, os.MkdirAll(ledgerDir, 0755))
	setupGitRepo(t, ledgerDir)

	// plant multiple lock files
	gitDir := filepath.Join(ledgerDir, ".git")
	for _, lock := range []string{"index.lock", "HEAD.lock", "config.lock"} {
		require.NoError(t, os.WriteFile(filepath.Join(gitDir, lock), []byte("stale"), 0644))
	}

	cfg := DefaultConfig()
	cfg.LedgerPath = ledgerDir
	cfg.SyncIntervalRead = 1 * time.Second
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	scheduler := NewSyncScheduler(cfg, logger)
	issues := NewIssueTracker()
	scheduler.SetIssueTracker(issues)

	_ = scheduler.doPull(context.Background(), nil, false)

	allIssues := issues.GetIssues()
	require.Len(t, allIssues, 1)
	// all three lock names should appear in the summary
	assert.Contains(t, allIssues[0].Summary, "index.lock")
	assert.Contains(t, allIssues[0].Summary, "HEAD.lock")
	assert.Contains(t, allIssues[0].Summary, "config.lock")
}

// TestCheckout_CorruptRepoSelfHealing verifies that when a directory exists but
// is not a valid git repo (e.g., .git/HEAD is corrupted or .git is missing),
// the Checkout path moves it to a .bak directory and attempts a fresh clone.
func TestCheckout_CorruptRepoSelfHealing(t *testing.T) {
	parentDir := t.TempDir()
	repoDir := filepath.Join(parentDir, "corrupt-repo")
	require.NoError(t, os.MkdirAll(repoDir, 0755))

	// create files so we can verify backup contents
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "data.txt"), []byte("important"), 0644))

	// intentionally no .git directory -- simulates a corrupt/incomplete clone

	cfg := DefaultConfig()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	scheduler := NewSyncScheduler(cfg, logger)

	// Checkout will detect "exists but not git repo" -> self-heal -> attempt clone
	// the clone will fail because the URL is untrusted, but the backup should exist
	result, err := scheduler.Checkout(CheckoutPayload{
		RepoPath: repoDir,
		CloneURL: "https://example.com/repo.git",
		RepoType: "ledger",
	}, nil)

	assert.Error(t, err, "clone should fail against untrusted host")
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "untrusted git host")

	// verify original directory was backed up
	backups, _ := filepath.Glob(filepath.Join(parentDir, "corrupt-repo.bak.*"))
	require.Len(t, backups, 1, "exactly one backup directory should be created")

	// verify backup preserved original content
	backupData := filepath.Join(backups[0], "data.txt")
	content, err := os.ReadFile(backupData)
	require.NoError(t, err, "backup should contain original files")
	assert.Equal(t, "important", string(content))
}

// TestDoPull_CorruptGitHeadDetection verifies that doPull detects when .git/HEAD
// is corrupted (making the repo fail isValidGitRepo) and self-heals by moving
// the corrupt directory aside so a re-clone can happen on the next cycle.
func TestDoPull_CorruptGitHeadDetection(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	tmpDir := t.TempDir()
	ledgerDir := filepath.Join(tmpDir, "ledger")
	require.NoError(t, os.MkdirAll(ledgerDir, 0755))

	// create a .git directory but no valid git structure
	// pathIsGitRepo checks for .git directory existence, so this should pass
	// but isValidGitRepo (git rev-parse --git-dir) will fail
	gitDir := filepath.Join(ledgerDir, ".git")
	require.NoError(t, os.MkdirAll(gitDir, 0755))

	// verify pathIsGitRepo reports true (shallow check passes)
	assert.True(t, pathIsGitRepo(ledgerDir), "pathIsGitRepo should return true when .git exists")

	// verify isValidGitRepo reports false (deep check fails)
	assert.False(t, isValidGitRepo(ledgerDir), "isValidGitRepo should return false for empty .git dir")

	// without a .git directory, pathIsGitRepo should return false
	emptyDir := filepath.Join(tmpDir, "empty")
	require.NoError(t, os.MkdirAll(emptyDir, 0755))
	assert.False(t, pathIsGitRepo(emptyDir), "pathIsGitRepo should return false without .git")

	cfg := DefaultConfig()
	cfg.LedgerPath = ledgerDir
	cfg.SyncIntervalRead = 1 * time.Second
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	scheduler := NewSyncScheduler(cfg, logger)
	ctx := context.Background()

	// doPull should detect the corrupt repo via isValidGitRepo and move it aside
	err := scheduler.doPull(ctx, nil, true)
	assert.NoError(t, err, "doPull should return nil after moving corrupt repo aside")

	// verify the original directory was moved to .bak
	_, statErr := os.Stat(ledgerDir)
	assert.True(t, os.IsNotExist(statErr), "original ledger dir should be moved aside")

	// verify backup exists
	backups, _ := filepath.Glob(filepath.Join(tmpDir, "ledger.bak.*"))
	require.Len(t, backups, 1, "exactly one backup should be created")
}

// TestDoPull_FetchFailureRecordsBackoff verifies that when git fetch fails
// (e.g., remote unreachable), the scheduler records a sync failure and applies
// exponential backoff so the next pull is skipped.
func TestDoPull_FetchFailureRecordsBackoff(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	tmpDir := t.TempDir()
	ledgerDir := filepath.Join(tmpDir, "ledger")

	// init repo with unreachable remote
	require.NoError(t, exec.Command("git", "init", ledgerDir).Run())
	require.NoError(t, exec.Command("git", "-C", ledgerDir, "config", "user.email", "test@test.com").Run())
	require.NoError(t, exec.Command("git", "-C", ledgerDir, "config", "user.name", "Test").Run())
	require.NoError(t, os.WriteFile(filepath.Join(ledgerDir, "README.md"), []byte("test"), 0644))
	require.NoError(t, exec.Command("git", "-C", ledgerDir, "add", ".").Run())
	require.NoError(t, exec.Command("git", "-C", ledgerDir, "commit", "-m", "initial").Run())
	require.NoError(t, exec.Command("git", "-C", ledgerDir, "remote", "add", "origin", "https://127.0.0.1:1/nonexistent.git").Run())

	cfg := DefaultConfig()
	cfg.LedgerPath = ledgerDir
	cfg.SyncIntervalRead = 1 * time.Second
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	scheduler := NewSyncScheduler(cfg, logger)
	ctx := context.Background()

	// first pull should fail
	err := scheduler.doPull(ctx, nil, false)
	assert.Error(t, err, "first pull should fail on unreachable remote")
	assert.Contains(t, err.Error(), "ledger fetch failed")

	// verify failure was recorded and backoff is active
	failures, nextRetry := scheduler.workspaceRegistry.GetSyncRetryInfo("ledger")
	assert.Equal(t, 1, failures, "should record exactly one failure")
	assert.True(t, nextRetry.After(time.Now()), "next retry should be in the future")

	// verify metrics recorded the failure
	snapshot := scheduler.metrics.Snapshot()
	assert.Equal(t, int64(1), snapshot.PullFailureCount)

	// second pull should be skipped by backoff (returns nil, not error)
	err = scheduler.doPull(ctx, nil, false)
	assert.NoError(t, err, "second pull should be skipped by backoff")

	// failure count should NOT increase (pull was skipped, not attempted)
	failures2, _ := scheduler.workspaceRegistry.GetSyncRetryInfo("ledger")
	assert.Equal(t, 1, failures2, "failure count should not increase on skip")
}

// TestDoPull_AlreadyCanceledContext verifies that doPull with an already-canceled
// context returns promptly without leaving lock files or corrupted state.
// This covers the scenario where the daemon is shutting down and context is
// canceled before or during a pull attempt.
func TestDoPull_AlreadyCanceledContext(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	tmpDir := t.TempDir()
	ledgerDir := filepath.Join(tmpDir, "ledger")
	require.NoError(t, os.MkdirAll(ledgerDir, 0755))
	setupGitRepo(t, ledgerDir)

	cfg := DefaultConfig()
	cfg.LedgerPath = ledgerDir
	cfg.SyncIntervalRead = 1 * time.Second
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	scheduler := NewSyncScheduler(cfg, logger)

	// cancel context before calling doPull
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// doPull with already-canceled context should return quickly
	done := make(chan error, 1)
	go func() {
		done <- scheduler.doPull(ctx, nil, true)
	}()

	select {
	case err := <-done:
		// may error or return nil depending on where cancellation is checked
		_ = err
	case <-time.After(5 * time.Second):
		t.Fatal("doPull did not return promptly with canceled context")
	}

	// verify no lock files were left behind
	gitDir := filepath.Join(ledgerDir, ".git")
	locks := gitutil.HasLockFiles(gitDir)
	assert.Empty(t, locks, "no lock files should remain after canceled context")

	// verify pullInProgress was reset
	scheduler.mu.Lock()
	assert.False(t, scheduler.pullInProgress, "pullInProgress should be reset after doPull returns")
	scheduler.mu.Unlock()
}

// TestDoPull_ContextCancellationDuringFetch verifies that a short-lived context
// causes doPull to fail fast when the remote is unreachable (connection refused).
// Uses 127.0.0.1:1 which immediately refuses connections rather than hanging.
func TestDoPull_ContextCancellationDuringFetch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	tmpDir := t.TempDir()
	ledgerDir := filepath.Join(tmpDir, "ledger")

	// use 127.0.0.1:1 which refuses connections immediately (no hang)
	require.NoError(t, exec.Command("git", "init", ledgerDir).Run())
	require.NoError(t, exec.Command("git", "-C", ledgerDir, "config", "user.email", "test@test.com").Run())
	require.NoError(t, exec.Command("git", "-C", ledgerDir, "config", "user.name", "Test").Run())
	require.NoError(t, os.WriteFile(filepath.Join(ledgerDir, "README.md"), []byte("test"), 0644))
	require.NoError(t, exec.Command("git", "-C", ledgerDir, "add", ".").Run())
	require.NoError(t, exec.Command("git", "-C", ledgerDir, "commit", "-m", "initial").Run())
	require.NoError(t, exec.Command("git", "-C", ledgerDir, "remote", "add", "origin", "https://127.0.0.1:1/nonexistent.git").Run())

	cfg := DefaultConfig()
	cfg.LedgerPath = ledgerDir
	cfg.SyncIntervalRead = 1 * time.Second
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	scheduler := NewSyncScheduler(cfg, logger)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// doPull should complete (fail on fetch) within timeout
	done := make(chan error, 1)
	go func() {
		done <- scheduler.doPull(ctx, nil, true)
	}()

	select {
	case err := <-done:
		// should error from fetch failure (connection refused)
		assert.Error(t, err, "should error when fetch fails")
	case <-time.After(15 * time.Second):
		t.Fatal("doPull did not complete within 15s")
	}

	// verify no lock files were left behind
	gitDir := filepath.Join(ledgerDir, ".git")
	locks := gitutil.HasLockFiles(gitDir)
	assert.Empty(t, locks, "no lock files should remain after fetch failure")
}

// TestIsClonePermanentError_Classification exercises the transient vs permanent
// error classification that determines whether the daemon retries (transient)
// or applies shorter backoff (permanent). This directly affects user experience:
// wrong classification means either unnecessary waits or infinite retries.
func TestIsClonePermanentError_Classification(t *testing.T) {
	tests := []struct {
		name      string
		msg       string
		permanent bool
	}{
		// permanent: won't resolve without human intervention
		{
			name:      "authentication failed",
			msg:       "fatal: Authentication failed for 'https://git.example.com/repo.git'",
			permanent: true,
		},
		{
			name:      "permission denied publickey",
			msg:       "Permission denied (publickey)",
			permanent: true,
		},
		{
			name:      "terminal prompts disabled",
			msg:       "fatal: could not read Username for 'https://git.example.com': terminal prompts disabled",
			permanent: true,
		},
		{
			name:      "invalid credentials from server",
			msg:       "remote: invalid credentials",
			permanent: true,
		},
		{
			name:      "repository not found",
			msg:       "fatal: repository not found",
			permanent: true,
		},
		{
			name:      "not a git repository",
			msg:       "fatal: 'https://example.com/bad' does not appear to be a git repository",
			permanent: true,
		},
		{
			name:      "http 401 unauthorized",
			msg:       "git clone failed: HTTP 401",
			permanent: true,
		},
		{
			name:      "http 403 forbidden",
			msg:       "git clone failed: HTTP 403",
			permanent: true,
		},
		{
			name:      "http 404 not found",
			msg:       "git clone failed: HTTP 404",
			permanent: true,
		},
		{
			name:      "invalid clone url",
			msg:       "invalid clone URL: scheme must be https",
			permanent: true,
		},

		// transient: may resolve on retry (network issues, server overload)
		{
			name:      "connection timed out",
			msg:       "fatal: unable to access 'https://git.example.com/repo.git': Connection timed out",
			permanent: false,
		},
		{
			name:      "dns resolution failure",
			msg:       "fatal: unable to access: Could not resolve host: git.example.com",
			permanent: false,
		},
		{
			name:      "remote hung up",
			msg:       "fatal: the remote end hung up unexpectedly",
			permanent: false,
		},
		{
			name:      "server 500 error",
			msg:       "error: RPC failed; HTTP 500 curl 22",
			permanent: false,
		},
		{
			name:      "generic exit code",
			msg:       "git clone failed: exit status 128",
			permanent: false,
		},
		{
			name:      "connection refused",
			msg:       "fatal: unable to access: Failed to connect to github.com port 443: Connection refused",
			permanent: false,
		},
		{
			name:      "ssl handshake failure",
			msg:       "fatal: unable to access: SSL connection timeout",
			permanent: false,
		},
		{
			name:      "http 502 bad gateway",
			msg:       "error: RPC failed; HTTP 502",
			permanent: false,
		},
		{
			name:      "http 503 service unavailable",
			msg:       "error: RPC failed; HTTP 503",
			permanent: false,
		},
		{
			name:      "empty error message",
			msg:       "",
			permanent: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isClonePermanentError(tt.msg)
			assert.Equal(t, tt.permanent, got, "msg=%q", tt.msg)
		})
	}
}

// TestHasLockFiles_Unit verifies the lock file detection function works
// correctly for all known lock file types.
func TestHasLockFiles_Unit(t *testing.T) {
	gitDir := filepath.Join(t.TempDir(), ".git")
	require.NoError(t, os.MkdirAll(gitDir, 0755))

	// no lock files initially
	assert.Empty(t, gitutil.HasLockFiles(gitDir))

	// create each type of lock file and verify detection
	knownLocks := []string{"index.lock", "shallow.lock", "config.lock", "HEAD.lock"}
	for _, lock := range knownLocks {
		require.NoError(t, os.WriteFile(filepath.Join(gitDir, lock), []byte{}, 0644))
	}

	found := gitutil.HasLockFiles(gitDir)
	assert.Len(t, found, len(knownLocks))
	for _, lock := range knownLocks {
		assert.Contains(t, found, lock)
	}

	// remove one and verify partial detection
	require.NoError(t, os.Remove(filepath.Join(gitDir, "index.lock")))
	found = gitutil.HasLockFiles(gitDir)
	assert.Len(t, found, len(knownLocks)-1)
	assert.NotContains(t, found, "index.lock")
}

// TestHasLockFiles_NonexistentDir verifies no panic on missing .git directory.
func TestHasLockFiles_NonexistentDir(t *testing.T) {
	found := gitutil.HasLockFiles("/nonexistent/path/.git")
	assert.Empty(t, found)
}

// TestPullTeamContext_StaleLockFileDetection verifies that pullTeamContext also
// detects lock files and reports issues, matching the doPull behavior.
func TestPullTeamContext_StaleLockFileDetection(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	teamDir := t.TempDir()
	setupGitRepo(t, teamDir)

	// plant a stale lock file
	lockPath := filepath.Join(teamDir, ".git", "index.lock")
	require.NoError(t, os.WriteFile(lockPath, []byte("stale"), 0644))

	cfg := DefaultConfig()
	cfg.TeamContextSyncInterval = 10 * time.Minute
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	scheduler := NewSyncScheduler(cfg, logger)
	issues := NewIssueTracker()
	scheduler.SetIssueTracker(issues)

	err := scheduler.pullTeamContext(context.Background(), teamDir)
	assert.NoError(t, err, "pullTeamContext should return nil when skipping due to lock files")

	// verify issue was reported with the team context repo name
	allIssues := issues.GetIssues()
	require.Len(t, allIssues, 1)
	assert.Equal(t, IssueTypeGitLock, allIssues[0].Type)
	assert.Contains(t, allIssues[0].Summary, "index.lock")
}

// TestClonePermanentBackoffIsShorter verifies that permanent errors use a
// shorter max backoff than transient errors, so users get faster feedback
// when credentials are wrong rather than waiting 30+ minutes.
func TestClonePermanentBackoffIsShorter(t *testing.T) {
	assert.Less(t, clonePermanentBackoffMax, cloneBackoffMax,
		"permanent errors should have shorter max backoff than transient")
	assert.Equal(t, 5*time.Minute, clonePermanentBackoffMax)
}

// TestDoPull_NonGitDirEntersClonePath verifies that when the ledger directory
// exists but is not a git repo (no .git directory), doPull returns nil and
// does NOT attempt git fetch/pull on it.
func TestDoPull_NonGitDirEntersClonePath(t *testing.T) {
	tmpDir := t.TempDir()
	ledgerDir := filepath.Join(tmpDir, "ledger")
	require.NoError(t, os.MkdirAll(ledgerDir, 0755))

	// create a file to prove the directory exists but has no .git
	require.NoError(t, os.WriteFile(filepath.Join(ledgerDir, "stale-file.txt"), []byte("leftover"), 0644))

	cfg := DefaultConfig()
	cfg.LedgerPath = ledgerDir
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	scheduler := NewSyncScheduler(cfg, logger)

	// should return nil (enters clone branch, finds no clone URL, returns)
	err := scheduler.doPull(context.Background(), nil, false)
	assert.NoError(t, err, "doPull should return nil for non-git directory")
}

// TestDoPull_RebaseStateSkips verifies that doPull skips when the repo is
// in a broken rebase state (rebase-merge or rebase-apply directory exists).
func TestDoPull_RebaseStateSkips(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	tmpDir := t.TempDir()
	ledgerDir := filepath.Join(tmpDir, "ledger")
	require.NoError(t, os.MkdirAll(ledgerDir, 0755))
	setupGitRepo(t, ledgerDir)

	// simulate rebase-merge state
	rebaseMergeDir := filepath.Join(ledgerDir, ".git", "rebase-merge")
	require.NoError(t, os.MkdirAll(rebaseMergeDir, 0755))

	cfg := DefaultConfig()
	cfg.LedgerPath = ledgerDir
	cfg.SyncIntervalRead = 1 * time.Second
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	scheduler := NewSyncScheduler(cfg, logger)

	err := scheduler.doPull(context.Background(), nil, false)
	assert.NoError(t, err, "doPull should skip gracefully when in rebase state")
}

// TestDoPull_PartialGitDirTriggersReclone verifies that doPull detects a partial
// .git directory (from an interrupted clone) and self-heals by moving it aside.
// The directory passes pathIsGitRepo (.git exists) but fails isValidGitRepo
// (git rev-parse --git-dir fails), triggering the self-healing path.
func TestDoPull_PartialGitDirTriggersReclone(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	tmpDir := t.TempDir()
	ledgerDir := filepath.Join(tmpDir, "ledger")
	require.NoError(t, os.MkdirAll(ledgerDir, 0755))

	// simulate an interrupted clone: .git exists but is empty/incomplete
	gitDir := filepath.Join(ledgerDir, ".git")
	require.NoError(t, os.MkdirAll(gitDir, 0755))

	// create a marker file to verify backup preserves content
	require.NoError(t, os.WriteFile(filepath.Join(ledgerDir, "partial.txt"), []byte("interrupted"), 0644))

	// precondition: pathIsGitRepo passes, isValidGitRepo fails
	require.True(t, pathIsGitRepo(ledgerDir), "precondition: .git dir must exist")
	require.False(t, isValidGitRepo(ledgerDir), "precondition: repo must be invalid")

	cfg := DefaultConfig()
	cfg.LedgerPath = ledgerDir
	cfg.SyncIntervalRead = 1 * time.Second
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	scheduler := NewSyncScheduler(cfg, logger)
	ctx := context.Background()

	err := scheduler.doPull(ctx, nil, false)
	assert.NoError(t, err, "doPull should return nil after self-healing corrupt repo")

	// original directory should be gone (renamed to .bak)
	_, statErr := os.Stat(ledgerDir)
	assert.True(t, os.IsNotExist(statErr), "original ledger dir should be moved aside")

	// backup should exist with .bak.* suffix
	backups, _ := filepath.Glob(filepath.Join(tmpDir, "ledger.bak.*"))
	require.Len(t, backups, 1, "exactly one backup should be created")

	// verify backup preserved original content
	backupContent, err := os.ReadFile(filepath.Join(backups[0], "partial.txt"))
	require.NoError(t, err, "backup should contain original files")
	assert.Equal(t, "interrupted", string(backupContent))
}

// TestCheckout_SemaphoreTimeout verifies that Checkout returns an error when
// all clone semaphore slots are occupied and the timeout expires, rather than
// blocking indefinitely.
func TestCheckout_SemaphoreTimeout(t *testing.T) {
	cfg := DefaultConfig()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	scheduler := NewSyncScheduler(cfg, logger)

	// use a very short timeout for the test
	scheduler.cloneSemTimeoutOverride = 100 * time.Millisecond

	// fill all clone semaphore slots
	for i := 0; i < maxConcurrentClones; i++ {
		scheduler.cloneSem <- struct{}{}
	}

	// Checkout should timeout trying to acquire a slot
	parentDir := t.TempDir()
	repoDir := filepath.Join(parentDir, "test-repo")

	result, err := scheduler.Checkout(CheckoutPayload{
		RepoPath: repoDir,
		CloneURL: "https://example.com/repo.git",
		RepoType: "ledger",
	}, nil)

	require.Error(t, err, "Checkout should fail when semaphore is full")
	assert.Nil(t, result)
	assert.True(t, strings.Contains(err.Error(), "clone semaphore timeout"),
		"error should mention semaphore timeout, got: %s", err.Error())

	// drain semaphore slots to clean up
	for i := 0; i < maxConcurrentClones; i++ {
		<-scheduler.cloneSem
	}
}
