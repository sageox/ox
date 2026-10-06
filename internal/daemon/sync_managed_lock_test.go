package daemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// behindClone returns a clone of a bare remote that has since advanced f.md
// from v1 to v2, with the clone's FETCH_HEAD aged so the pull is not skipped as
// "recently fetched".
func behindClone(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("short: git pull operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	isolateCredentials(t)
	bare := makeBareRepo(t, "lockpull", "f.md", "v1\n")
	clone := filepath.Join(t.TempDir(), "clone")
	out, err := runGitOut(t, t.TempDir(), "clone", bare, clone)
	require.NoError(t, err, out)
	pushExtraCommit(t, bare, "f.md", "v2\n")
	agePastFetchHead(t, clone)
	return clone
}

func readF(t *testing.T, clone string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(clone, "f.md"))
	require.NoError(t, err)
	return string(body)
}

func pullOnce(ctx context.Context, clone string) ManagedRepoPullResult {
	return newTestScheduler(clone).pullManagedRepo(ctx, ManagedRepoPullOpts{
		RepoPath: clone,
		RepoName: "ledger",
		Logger:   discardLogger(),
	})
}

// A fresh index.lock that a live git releases moments later must delay the pull,
// not skip it. Skipping on sight turned every hook commit into a missed sync
// cycle ("git lock files detected, skipping pull" 7-16 times per 30 minutes).
func TestPullManagedRepo_WaitsForForeignIndexLock(t *testing.T) {
	clone := behindClone(t)
	lock := filepath.Join(clone, ".git", "index.lock")
	require.NoError(t, os.WriteFile(lock, []byte("live"), 0o644))
	go func() {
		time.Sleep(300 * time.Millisecond)
		_ = os.Remove(lock)
	}()

	res := pullOnce(context.Background(), clone)

	assert.False(t, res.Skipped, "a lock released within the wait budget must not skip the pull (skip=%q)", res.SkipReason)
	assert.NoError(t, res.Err)
	assert.True(t, res.PullRan)
	assert.Equal(t, "v2\n", readF(t, clone))
}

// A lock that outlives the wait yields a busy skip and leaves the repo exactly
// as found: no pull, no rebase state, and the foreign lock file untouched.
func TestPullManagedRepo_PersistentForeignLockSkipsWithoutTouchingRepo(t *testing.T) {
	clone := behindClone(t)
	lock := filepath.Join(clone, ".git", "index.lock")
	require.NoError(t, os.WriteFile(lock, []byte("live"), 0o644))
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	res := pullOnce(ctx, clone)

	assert.True(t, res.Skipped)
	assert.Equal(t, skipReasonLockFilesPresent, res.SkipReason)
	assert.NoError(t, res.Err)
	assert.FileExists(t, lock, "a lock held by a foreign git is never removed")
	assert.NoDirExists(t, filepath.Join(clone, ".git", "rebase-merge"))
	assert.Equal(t, "v1\n", readF(t, clone))
}

// git reports "Unable to create '...index.lock': File exists" when a foreign git
// grabs the lock after our pre-flight. The pull must retry once, not treat it
// as a conflict and climb to `rebase --abort`.
//
// The pre-rebase hook stands in for the foreign git: its first run fails with
// git's own lock message, its second passes.
func TestPullManagedRepo_IndexLockMidPullRetriesOnce(t *testing.T) {
	clone := behindClone(t)
	// a local commit forces a real rebase, which is what runs pre-rebase
	require.NoError(t, os.WriteFile(filepath.Join(clone, "local.md"), []byte("l\n"), 0o644))
	out, err := runGitOut(t, clone, "add", "local.md")
	require.NoError(t, err, out)
	out, err = runGitOut(t, clone, "commit", "-m", "local")
	require.NoError(t, err, out)
	agePastFetchHead(t, clone)

	counter := filepath.Join(t.TempDir(), "runs")
	hook := "#!/bin/sh\necho x >> '" + counter + "'\n" +
		"if [ \"$(wc -l < '" + counter + "')\" -le 1 ]; then\n" +
		"  echo \"fatal: Unable to create '$PWD/.git/index.lock': File exists.\" >&2\n  exit 1\nfi\nexit 0\n"
	require.NoError(t, os.WriteFile(filepath.Join(clone, ".git", "hooks", "pre-rebase"), []byte(hook), 0o755))

	res := pullOnce(context.Background(), clone)

	assert.NoError(t, res.Err, "one retry after the lock clears must succeed")
	assert.True(t, res.PullRan)
	assert.Equal(t, "v2\n", readF(t, clone))
	assert.FileExists(t, filepath.Join(clone, "local.md"), "local commit replayed")
	runs, err := os.ReadFile(counter)
	require.NoError(t, err)
	assert.Equal(t, 2, len(runs)/2, "hook ran exactly twice: first pull and the single retry")
}
