package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDoPull_NetworkFailurePreservesStateAndRetryProgresses exercises the
// complete offline-to-online transition against real git repositories. The
// failed fetch must preserve both user work and the last-known-good sync
// checkpoint. Restoring connectivity must then fetch remote content and reset
// failure state without requiring a scheduler restart.
func TestDoPull_NetworkFailurePreservesStateAndRetryProgresses(t *testing.T) {
	if testing.Short() {
		t.Skip("short: exercises real git failure and recovery")
	}

	root := t.TempDir()
	ledgerDir := filepath.Join(root, "ledger")
	require.NoError(t, os.MkdirAll(ledgerDir, 0o755))
	setupGitRepo(t, ledgerDir)
	remote := bareRepoPath(ledgerDir)
	scheduler := newPullTestScheduler(t, ledgerDir)

	// Establish a durable last-known-good checkpoint before connectivity fails.
	require.NoError(t, scheduler.doPull(context.Background(), nil, true, true))
	before := LoadSyncState(ledgerDir)
	require.False(t, before.LastSync.IsZero())
	require.NotEmpty(t, before.LastSyncCommit)

	// User work exists only in the worktree while the remote becomes
	// unreachable. A failed pull must not reset/overwrite either kind of state.
	const localEdit = "# Test\n\nlocal uncommitted work\n"
	require.NoError(t, os.WriteFile(filepath.Join(ledgerDir, "README.md"), []byte(localEdit), 0o644))
	gitCmd(t, ledgerDir, "remote", "set-url", "origin", "http://127.0.0.1:1/unreachable.git")

	err := scheduler.doPull(context.Background(), nil, true, true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fetch failed")
	afterFailure := LoadSyncState(ledgerDir)
	assert.Equal(t, before.LastSync, afterFailure.LastSync,
		"failure must preserve the last successful sync time")
	assert.Equal(t, before.LastSyncCommit, afterFailure.LastSyncCommit,
		"failure must preserve the last known-good commit")
	assert.Equal(t, 1, afterFailure.ConsecutiveFailures)
	body, readErr := os.ReadFile(filepath.Join(ledgerDir, "README.md"))
	require.NoError(t, readErr)
	assert.Equal(t, localEdit, string(body), "network failure must preserve uncommitted user work")

	// Connectivity returns and the remote has moved. Backdate FETCH_HEAD to
	// model the next scheduled retry (a failed fetch may still touch the file,
	// and the cross-daemon dedup window intentionally suppresses immediate
	// refetches). Force bypasses failure backoff, proving retry makes progress.
	pushFromSeparateClone(t, remote, "remote-recovery.txt", "remote is back\n")
	gitCmd(t, ledgerDir, "remote", "set-url", "origin", remote)
	oldFetch := time.Now().Add(-time.Hour)
	require.NoError(t, os.Chtimes(filepath.Join(ledgerDir, ".git", "FETCH_HEAD"), oldFetch, oldFetch))
	require.NoError(t, scheduler.doPull(context.Background(), nil, true, true))

	afterRecovery := LoadSyncState(ledgerDir)
	assert.Zero(t, afterRecovery.ConsecutiveFailures)
	assert.True(t, afterRecovery.LastSync.After(before.LastSync))
	assert.NotEqual(t, before.LastSyncCommit, afterRecovery.LastSyncCommit,
		"successful retry must advance the durable checkpoint")
	require.FileExists(t, filepath.Join(ledgerDir, "remote-recovery.txt"),
		"successful retry must materialize remote progress")
	body, readErr = os.ReadFile(filepath.Join(ledgerDir, "README.md"))
	require.NoError(t, readErr)
	assert.Equal(t, localEdit, string(body), "successful autostash retry must restore user work")

	metrics := scheduler.Metrics().Snapshot()
	assert.Equal(t, int64(1), metrics.PullFailureCount)
	assert.Equal(t, int64(1), metrics.PullSuccessCount,
		"the recovery pull, unlike the initial remote-unchanged checkpoint, must record success")
}

// TestDoPull_LedgerPullWaitsForSymlinkProtection: while core.symlinks=false
// cannot be saved, the Ledger pull does not run, so a symlink the remote
// gained cannot land as a real link; once it can be saved, the next pull
// brings the link in as a plain file. Failure prevented: a failed save lets a
// teammate's committed symlink through, and a later ox write follows it out
// of the Ledger.
func TestDoPull_LedgerPullWaitsForSymlinkProtection(t *testing.T) {
	if testing.Short() {
		t.Skip("short: exercises real git pulls")
	}
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks needs elevated rights on Windows, where git checks them out as files anyway")
	}
	ledgerDir := filepath.Join(t.TempDir(), "ledger")
	require.NoError(t, os.MkdirAll(ledgerDir, 0o755))
	setupGitRepo(t, ledgerDir)
	remote := bareRepoPath(ledgerDir)
	scheduler := newPullTestScheduler(t, ledgerDir)

	teammate := filepath.Join(t.TempDir(), "teammate")
	gitCmd(t, t.TempDir(), "clone", remote, teammate)
	gitCmd(t, teammate, "config", "user.name", "test")
	gitCmd(t, teammate, "config", "user.email", "test@test.com")
	require.NoError(t, os.Symlink(filepath.Join(t.TempDir(), "outside"), filepath.Join(teammate, "AGENTS.md")))
	gitCmd(t, teammate, "add", "AGENTS.md")
	gitCmd(t, teammate, "commit", "-m", "plant a link")
	gitCmd(t, teammate, "push", "origin", "HEAD")

	scheduler.disableSymlinksTestHook = func(string) error { return errors.New("config not writable") }
	err := scheduler.doPull(context.Background(), nil, true, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "symlink protection")
	_, statErr := os.Lstat(filepath.Join(ledgerDir, "AGENTS.md"))
	require.True(t, os.IsNotExist(statErr), "no pull may run while protection is unsaved: %v", statErr)

	scheduler.disableSymlinksTestHook = nil
	require.NoError(t, scheduler.doPull(context.Background(), nil, true, false))
	info, err := os.Lstat(filepath.Join(ledgerDir, "AGENTS.md"))
	require.NoError(t, err)
	assert.True(t, info.Mode().IsRegular(), "the pulled link must arrive as a plain file, got %s", info.Mode())
}
