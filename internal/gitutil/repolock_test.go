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
	held := make(chan struct{})
	go func() {
		_ = WithRepoLock(context.Background(), a, func() error { close(held); <-release; return nil })
	}()
	<-held // wait for confirmation clone A actually holds the lock, not a fixed guess
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

// TestWithPreCloneLock_SerializesCallers is WithRepoLock's proof, applied to
// the pre-clone lock: two callers targeting the same not-yet-cloned path
// never hold the lock at once.
func TestWithPreCloneLock_SerializesCallers(t *testing.T) {
	target := t.TempDir() + "/not-yet-cloned"
	var inside, overlaps atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := WithPreCloneLock(context.Background(), target, func() error {
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
	assert.Zero(t, overlaps.Load(), "two callers held the pre-clone lock simultaneously")
}

// TestWithPreCloneLock_DifferentTargetsDoNotContend guards the keying, and
// that it's distinct from WithRepoLock's key for the same path — the two
// are never expected to be held simultaneously, but a hash collision would
// be a silent, hard-to-diagnose deadlock risk.
func TestWithPreCloneLock_DifferentTargetsDoNotContend(t *testing.T) {
	a, b := t.TempDir()+"/a", t.TempDir()+"/b"
	require.NotEqual(t, PreCloneLockTarget(a), PreCloneLockTarget(b))
	require.NotEqual(t, PreCloneLockTarget(a), RepoLockTarget(a), "pre-clone and post-clone locks for the same path must not collide")

	release := make(chan struct{})
	held := make(chan struct{})
	go func() {
		_ = WithPreCloneLock(context.Background(), a, func() error { close(held); <-release; return nil })
	}()
	<-held
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := WithPreCloneLock(ctx, b, func() error { return nil })
	close(release)
	assert.NoError(t, err, "target B must not wait on target A's lock")
}

func TestIsPreCloneLockBusy(t *testing.T) {
	assert.False(t, IsPreCloneLockBusy(nil))
	assert.False(t, IsPreCloneLockBusy(errors.New("fatal: not a git repository")))
	assert.True(t, IsPreCloneLockBusy(&fileutil.ErrLockTimeout{Path: "x", Timeout: time.Second}))
	assert.True(t, IsPreCloneLockBusy(context.DeadlineExceeded))
}

// TestTryWithPreCloneLock_SharesTheBlockingLock verifies the non-blocking
// pre-clone lock contends with the blocking one on the same path, and only
// that path.
// Failure prevented: a caller trying the lock (the daemon's Checkout, doctor's
// direct clone) and one waiting on it (the Ledger HEAD repair, a read sync)
// key on different files, and two clones write into the same Ledger path.
func TestTryWithPreCloneLock_SharesTheBlockingLock(t *testing.T) {
	target, other := t.TempDir()+"/ledger", t.TempDir()+"/other"
	release, held := make(chan struct{}), make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = WithPreCloneLock(context.Background(), target, func() error { close(held); <-release; return nil })
	}()
	<-held
	defer func() { close(release); <-done }()

	acquired, err := TryWithPreCloneLock(target, func() error {
		t.Error("ran while another clone of the same path held the lock")
		return nil
	})
	require.NoError(t, err)
	assert.False(t, acquired, "a clone already holding the path must be reported")

	ran := false
	acquired, err = TryWithPreCloneLock(other, func() error { ran = true; return nil })
	require.NoError(t, err)
	assert.True(t, acquired && ran, "another path's lock is free")
}
