package gitutil

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sageox/ox/internal/fileutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWithRepoLock_SerializesCallers proves two callers on the same clone
// never hold the lock at once — the property every fetch/pull site relies on.
func TestWithRepoLock_SerializesCallers(t *testing.T) {
	repo := t.TempDir()
	var inside, overlaps atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := WithRepoLock(context.Background(), repo, func() error {
				if inside.Add(1) > 1 {
					overlaps.Add(1)
				}
				time.Sleep(5 * time.Millisecond)
				inside.Add(-1)
				return nil
			})
			assert.NoError(t, err)
		}()
	}
	wg.Wait()
	assert.Zero(t, overlaps.Load(), "two callers held the repo lock simultaneously")
}

// TestWithRepoLock_DifferentClonesDoNotContend guards the keying: locking
// clone A must not block clone B, or one slow ledger would stall every team
// context on the machine.
func TestWithRepoLock_DifferentClonesDoNotContend(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	require.NotEqual(t, RepoLockTarget(a), RepoLockTarget(b))
	release := make(chan struct{})
	go func() {
		_ = WithRepoLock(context.Background(), a, func() error { <-release; return nil })
	}()
	time.Sleep(20 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := WithRepoLock(ctx, b, func() error { return nil })
	close(release)
	assert.NoError(t, err, "clone B must not wait on clone A's lock")
}

// TestWithRepoLock_ContextBoundsTheWait: a CLI with a 10s budget must not
// sit for RepoLockTimeout behind a daemon fetch — it gives up on its own
// deadline and the caller treats that as busy, not broken.
func TestWithRepoLock_ContextBoundsTheWait(t *testing.T) {
	repo := t.TempDir()
	release := make(chan struct{})
	held := make(chan struct{})
	go func() {
		_ = WithRepoLock(context.Background(), repo, func() error { close(held); <-release; return nil })
	}()
	<-held
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := WithRepoLock(ctx, repo, func() error { return errors.New("must not run") })
	close(release)
	require.Error(t, err)
	assert.True(t, IsRepoLockBusy(err), "context expiry while waiting is 'busy': %v", err)
	assert.Less(t, time.Since(start), 2*time.Second)
}

func TestIsRepoLockBusy(t *testing.T) {
	assert.False(t, IsRepoLockBusy(nil))
	assert.False(t, IsRepoLockBusy(errors.New("fatal: not a git repository")))
	assert.True(t, IsRepoLockBusy(&fileutil.ErrLockTimeout{Path: "x", Timeout: time.Second}))
	assert.True(t, IsRepoLockBusy(context.DeadlineExceeded))
}
