package gitutil

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

// Failure prevented: a validation refusal is treated as a remote rejection and
// triggers a rebase, repair, or another attempt that publishes refused history.
func TestPushValidation_RefusalDoesNotPublish(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git push validation")
	}
	repo, bare := initBareRemoteRepo(t)
	remoteHead := gitInRepo(t, bare, "rev-parse", "HEAD")
	addCommit(t, repo, "pending.txt", "pending", "pending commit")
	localHead := gitInRepo(t, repo, "rev-parse", "HEAD")
	refusal := errors.New("rejected: non-fast-forward; LFS objects are missing")
	validations := 0
	repairs := 0
	err := PushWithRetry(context.Background(), repo, PushOpts{
		ValidatePush: func(ctx context.Context, path string) (bool, error) {
			validations++
			assert.Equal(t, repo, path)
			return false, refusal
		},
		ReconcileLFS: func(path string) (bool, error) {
			repairs++
			return true, nil
		},
	})
	require.ErrorIs(t, err, refusal)
	assert.Equal(t, 1, validations)
	assert.Zero(t, repairs)
	assert.Equal(t, remoteHead, gitInRepo(t, bare, "rev-parse", "HEAD"))
	assert.Equal(t, localHead, gitInRepo(t, repo, "rev-parse", "HEAD"))
}

// Failure prevented: an idempotent publication check pushes unrelated commits
// or retains the old circuit-breaker penalty after reporting success.
func TestPushValidation_AlreadyPublishedLeavesPendingCommitsLocal(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git push validation")
	}
	repo, bare := initBareRemoteRepo(t)
	remoteHead := gitInRepo(t, bare, "rev-parse", "HEAD")
	addCommit(t, repo, "unrelated.txt", "pending", "unrelated commit")
	clock := newFakePushClock()
	breaker := newPushBreaker(clock.Now)
	breaker.trip(repo)
	clock.Advance(pushWedgeBackoffBase)
	validations := 0
	err := PushWithRetry(context.Background(), repo, PushOpts{
		breaker: breaker,
		ValidatePush: func(ctx context.Context, path string) (bool, error) {
			validations++
			return true, nil
		},
	})
	require.NoError(t, err)
	assert.Equal(t, 1, validations)
	assert.Equal(t, remoteHead, gitInRepo(t, bare, "rev-parse", "HEAD"))
	assert.NotEqual(t, remoteHead, gitInRepo(t, repo, "rev-parse", "HEAD"))
	assert.Equal(t, clock.Now().Add(pushWedgeBackoffBase), breaker.trip(repo), "success clears the previous trip count")
}

// Failure prevented: retrying a rejected push publishes history changed by a
// rebase without giving the caller another chance to validate it.
func TestPushValidation_RechecksHistoryAfterRebase(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git push and retry rebase")
	}
	for _, refuseRetry := range []bool{true, false} {
		name := "publish validated retry"
		if refuseRetry {
			name = "refuse changed history"
		}
		t.Run(name, func(t *testing.T) {
			repo := divergedRepo(t)
			bare := gitInRepo(t, repo, "remote", "get-url", "origin")
			remoteHead := gitInRepo(t, bare, "rev-parse", "HEAD")
			localHead := gitInRepo(t, repo, "rev-parse", "HEAD")
			refusal := errors.New("refuse rebased history")
			validations := 0
			err := PushWithRetry(context.Background(), repo, PushOpts{
				MaxRetries: 2,
				OpTimeout:  10 * time.Second,
				ValidatePush: func(ctx context.Context, path string) (bool, error) {
					validations++
					head := gitInRepo(t, path, "rev-parse", "HEAD")
					if validations == 1 {
						assert.Equal(t, localHead, head)
					} else {
						assert.NotEqual(t, localHead, head)
						assert.FileExists(t, filepath.Join(path, "b.txt"))
						assert.FileExists(t, filepath.Join(path, "c.txt"))
						if refuseRetry {
							return false, refusal
						}
					}
					return false, nil
				},
			})
			assert.Equal(t, 2, validations)
			if refuseRetry {
				require.ErrorIs(t, err, refusal)
				assert.Equal(t, remoteHead, gitInRepo(t, bare, "rev-parse", "HEAD"))
			} else {
				require.NoError(t, err)
				assert.Equal(t, gitInRepo(t, repo, "rev-parse", "HEAD"), gitInRepo(t, bare, "rev-parse", "HEAD"))
			}
		})
	}
}

// Failure prevented: autostash restores the working copy over a different
// staged draft while an automatic retry tries to reconcile remote history.
func TestPushValidation_DivergedRemotePreservesStagedDraft(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git push with diverged remote")
	}
	repo := divergedRepo(t)
	bare := gitInRepo(t, repo, "remote", "get-url", "origin")
	localHead := gitInRepo(t, repo, "rev-parse", "HEAD")
	remoteHead := gitInRepo(t, bare, "rev-parse", "HEAD")
	trackingHead := gitInRepo(t, repo, "rev-parse", "@{upstream}")
	privatePath := filepath.Join(repo, "private.txt")
	require.NoError(t, os.WriteFile(privatePath, []byte("staged private draft"), 0o644))
	run(t, repo, "git", "add", "private.txt")
	require.NoError(t, os.WriteFile(privatePath, []byte("different working draft"), 0o644))
	indexPath := filepath.Join(repo, ".git", "index")
	indexBefore, err := os.ReadFile(indexPath)
	require.NoError(t, err)
	fetchHeadPath := filepath.Join(repo, ".git", "FETCH_HEAD")
	require.NoFileExists(t, fetchHeadPath)
	validations := 0
	err = PushWithRetry(context.Background(), repo, PushOpts{
		MaxRetries: 2,
		OpTimeout:  10 * time.Second,
		ValidatePush: func(ctx context.Context, path string) (bool, error) {
			validations++
			return false, nil
		},
	})
	require.ErrorContains(t, err, "staged changes remain untouched")
	assert.Contains(t, err.Error(), "save the staged draft before manual synchronization, then rerun import")
	assert.Equal(t, 1, validations)
	assert.Equal(t, localHead, gitInRepo(t, repo, "rev-parse", "HEAD"))
	assert.Equal(t, remoteHead, gitInRepo(t, bare, "rev-parse", "HEAD"))
	assert.Equal(t, trackingHead, gitInRepo(t, repo, "rev-parse", "@{upstream}"))
	assert.NoFileExists(t, fetchHeadPath, "refusal must happen before fetching")
	indexAfter, err := os.ReadFile(indexPath)
	require.NoError(t, err)
	assert.Equal(t, indexBefore, indexAfter, "the index must remain byte-for-byte intact")
	assert.Equal(t, "staged private draft", gitInRepo(t, repo, "show", ":private.txt"))
	workingDraft, err := os.ReadFile(privatePath)
	require.NoError(t, err)
	assert.Equal(t, "different working draft", string(workingDraft))
	assert.False(t, IsRebaseInProgress(repo))
}

// Failure prevented: git becomes unsafe after preflight and validation runs
// against a checkout another operation has started mutating.
func TestPushValidation_RechecksGitSafetyBeforeCallback(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git push validation")
	}
	repo, bare := initBareRemoteRepo(t)
	remoteHead := gitInRepo(t, bare, "rev-parse", "HEAD")
	addCommit(t, repo, "pending.txt", "pending", "pending commit")
	lockPath := filepath.Join(repo, ".git", "index.lock")
	validations := 0
	err := PushWithRetry(context.Background(), repo, PushOpts{
		PrePush: func(path string) error {
			return os.WriteFile(lockPath, nil, 0o644)
		},
		ValidatePush: func(ctx context.Context, path string) (bool, error) {
			validations++
			return false, nil
		},
	})
	require.ErrorContains(t, err, "repo blocked")
	assert.Zero(t, validations)
	assert.FileExists(t, lockPath)
	assert.Equal(t, remoteHead, gitInRepo(t, bare, "rev-parse", "HEAD"))
}

// Failure prevented: a concurrent writer changes HEAD after validation while
// the remote is still receiving the push.
func TestPushValidation_HoldsRepoLockThroughPush(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git push with blocking receive hook")
	}
	if runtime.GOOS == "windows" {
		t.Skip("blocking pre-receive fixture needs a POSIX shell")
	}
	repo, bare := initBareRemoteRepo(t)
	addCommit(t, repo, "pending.txt", "pending", "pending commit")
	signals := t.TempDir()
	enteredPath := filepath.Join(signals, "entered")
	releasePath := filepath.Join(signals, "release")
	hook := "#!/bin/sh\ntouch '" + enteredPath + "'\nwhile [ ! -f '" + releasePath + "' ]; do sleep 0.01; done\n"
	require.NoError(t, os.WriteFile(filepath.Join(bare, "hooks", "pre-receive"), []byte(hook), 0o755))
	t.Cleanup(func() { _ = os.WriteFile(releasePath, nil, 0o644) })
	callbackLockErr := make(chan error, 1)
	pushDone := make(chan error, 1)
	go func() {
		pushDone <- PushWithRetry(context.Background(), repo, PushOpts{
			OpTimeout: 10 * time.Second,
			ValidatePush: func(ctx context.Context, path string) (bool, error) {
				lockCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
				defer cancel()
				callbackLockErr <- WithRepoLock(lockCtx, path, func() error { return nil })
				return false, nil
			},
		})
	}()
	select {
	case err := <-callbackLockErr:
		require.True(t, IsRepoLockBusy(err), "validation must hold the repo lock")
	case <-time.After(10 * time.Second):
		t.Fatal("validation did not run")
	}
	require.Eventually(t, func() bool {
		_, err := os.Stat(enteredPath)
		return err == nil
	}, 10*time.Second, 10*time.Millisecond, "push must reach the remote hook")
	lockCtx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err := WithRepoLock(lockCtx, repo, func() error { return nil })
	require.True(t, IsRepoLockBusy(err), "the actual push must hold the same repo lock")
	require.NoError(t, os.WriteFile(releasePath, nil, 0o644))
	select {
	case err := <-pushDone:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("push did not finish after releasing the remote hook")
	}
	assert.Equal(t, gitInRepo(t, repo, "rev-parse", "HEAD"), gitInRepo(t, bare, "rev-parse", "HEAD"))
}
