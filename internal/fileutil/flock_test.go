package fileutil

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWithFileLock_SerializesGoroutines proves the in-process side of
// the lock: two goroutines hammering the same target with WithFileLock
// can never observe each other inside the critical section. Without
// this guarantee, our meta.json / config.local.toml read-modify-write
// fixes would still leak under high concurrency on platforms where
// flock-by-FD has loose semantics for same-process FDs.
//
// Failure prevented: a future refactor that drops acquireInProcess and
// trusts kernel-level flock to handle in-process callers — which works
// on Linux but not on every BSD/macOS combo.
func TestWithFileLock_SerializesGoroutines(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "shared.dat")

	var inside int32
	var maxConcurrent int32

	const N = 16
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := WithFileLock(context.Background(), target, func() error {
				cur := atomic.AddInt32(&inside, 1)
				for {
					m := atomic.LoadInt32(&maxConcurrent)
					if cur <= m || atomic.CompareAndSwapInt32(&maxConcurrent, m, cur) {
						break
					}
				}
				// hold long enough to expose any concurrency
				time.Sleep(2 * time.Millisecond)
				atomic.AddInt32(&inside, -1)
				return nil
			})
			if err != nil {
				t.Errorf("lock failed: %v", err)
			}
		}()
	}
	wg.Wait()

	if got := atomic.LoadInt32(&maxConcurrent); got != 1 {
		t.Errorf("max concurrent goroutines inside critical section = %d, want 1", got)
	}
}

// TestWithFileLock_ReleasesOnPanic guarantees a panic inside the
// critical section still releases the lock. Without this, a single
// panic in a CLI write would wedge every future writer until the lock
// file is manually removed — which is the worst kind of "ox is
// mysteriously broken" report.
func TestWithFileLock_ReleasesOnPanic(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "panicky.dat")

	func() {
		defer func() { _ = recover() }()
		_ = WithFileLock(context.Background(), target, func() error {
			panic("boom")
		})
	}()

	// must be acquirable again immediately
	done := make(chan error, 1)
	go func() {
		done <- WithFileLock(context.Background(), target, func() error { return nil })
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("post-panic acquire failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("post-panic acquire timed out — lock leaked")
	}
}

// TestWithFileLock_RespectsContext verifies that a contended lock
// returns promptly when the caller's context is canceled, instead of
// spinning until LockTimeout. This matters because the daemon runs many
// of these RMW operations under a parent context tied to the daemon's
// shutdown signal — without context awareness, a contended lock would
// delay shutdown by up to 10s.
func TestWithFileLock_RespectsContext(t *testing.T) {
	if !crossProcessFlockAvailable() {
		t.Skip("cross-process flock not available on this platform")
	}

	dir := t.TempDir()
	target := filepath.Join(dir, "contend.dat")

	// hold the lock from a child process so our in-process map is
	// not the gate (the in-process map already returns immediately
	// to context cancel via the mutex; cross-process is the test).
	holder, release := startLockHolder(t, target)
	defer release()
	<-holder.acquired

	ctx, cancel := context.WithCancel(context.Background())
	doneCh := make(chan error, 1)
	go func() {
		doneCh <- WithFileLock(ctx, target, func() error { return nil })
	}()

	// Give the goroutine a moment to enter the poll loop, then cancel.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-doneCh:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("expected context.Canceled, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("WithFileLock didn't return after context cancel")
	}
}

// crossProcessFlockAvailable reports whether the test's flock primitive
// actually cross-process-serializes. Skips the cross-process test on
// platforms where tryFlock is a no-op (Windows).
func crossProcessFlockAvailable() bool {
	dir, _ := os.MkdirTemp("", "ox-flock-probe-")
	defer os.RemoveAll(dir)
	target := filepath.Join(dir, "probe")
	f, err := os.Create(LockPath(target))
	if err != nil {
		return false
	}
	defer f.Close()
	if err := tryFlock(f); err != nil {
		return false
	}
	_ = unlockFlock(f)
	return true
}

// startLockHolder spawns a child process that holds the lock until
// signaled (by closing its stdin). The child uses a separate process
// so the parent's in-process map doesn't gate it.
type lockHolder struct {
	cmd      *exec.Cmd
	acquired chan struct{}
}

func startLockHolder(t *testing.T, target string) (*lockHolder, func()) {
	t.Helper()
	// Re-exec the test binary in a special mode that just acquires the
	// lock and waits. Implemented via TestHelperFlockHolder below.
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^TestHelperFlockHolder$")
	cmd.Env = append(os.Environ(), // safe: re-exec of test binary as a lock-holder helper; needs full env for $TMPDIR + Go runtime
		"OX_TEST_LOCK_TARGET="+target,
		"OX_TEST_LOCK_HOLDER=1",
	)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	h := &lockHolder{cmd: cmd, acquired: make(chan struct{})}

	// child writes "ack\n" once it has the lock.
	go func() {
		buf := make([]byte, 4)
		_, _ = stdout.Read(buf)
		close(h.acquired)
	}()

	release := func() {
		_ = stdin.Close()
		_ = cmd.Wait()
	}
	return h, release
}

// TestHelperFlockHolder is the entry point used by startLockHolder —
// only runs when OX_TEST_LOCK_HOLDER=1, otherwise no-ops so it
// doesn't pollute regular `go test` runs.
func TestHelperFlockHolder(t *testing.T) {
	if os.Getenv("OX_TEST_LOCK_HOLDER") != "1" {
		t.Skip("helper for cross-process tests")
	}
	target := os.Getenv("OX_TEST_LOCK_TARGET")
	if target == "" {
		t.Fatal("OX_TEST_LOCK_TARGET unset")
	}
	err := WithFileLock(context.Background(), target, func() error {
		// Signal the parent we're holding it.
		_, _ = os.Stdout.WriteString("ack\n")
		// Block until parent closes our stdin.
		buf := make([]byte, 1)
		_, _ = os.Stdin.Read(buf)
		return nil
	})
	if err != nil {
		t.Fatalf("helper acquire failed: %v", err)
	}
}

// TestErrLockTimeout_Error covers the Timeout field this PR added: the
// zero-value fallback to the package default, and reporting the actual
// caller-supplied timeout (gitutil.WithRepoLock passes its own 2-minute
// budget here, not the 10s package default — a bare zero check would have
// let that regress silently).
func TestErrLockTimeout_Error(t *testing.T) {
	zero := &ErrLockTimeout{Path: "/tmp/x.lock"}
	assert.Contains(t, zero.Error(), LockTimeout.String(), "Timeout: 0 must fall back to the package default, not report a bogus 0s")

	custom := &ErrLockTimeout{Path: "/tmp/x.lock", Timeout: 2 * time.Minute}
	assert.Contains(t, custom.Error(), "2m0s", "a caller-supplied timeout must be reported as given, not the package default")
	assert.NotContains(t, custom.Error(), LockTimeout.String())
}

// TestWithFileLockTimeout_ReportsRequestedTimeout is the regression for the
// exact bug this PR fixed: acquireInProcess used to compute
// time.Until(deadline) AFTER its timer fired, which is ~0 (sometimes
// negative) by construction, not the timeout the caller actually asked for.
func TestWithFileLockTimeout_ReportsRequestedTimeout(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "contended.dat")

	release := make(chan struct{})
	held := make(chan struct{})
	go func() {
		_ = WithFileLock(context.Background(), target, func() error { close(held); <-release; return nil })
	}()
	<-held
	defer close(release)

	const requested = 150 * time.Millisecond
	err := WithFileLockTimeout(context.Background(), target, requested, func() error {
		return errors.New("must not run")
	})

	var timeoutErr *ErrLockTimeout
	require.ErrorAs(t, err, &timeoutErr)
	// Within a few ms of what was asked for (clock-read jitter between the
	// two time.Now() calls), not ~0s — which is what
	// time.Until(deadline) computed AFTER the timer fires would report.
	assert.InDelta(t, requested, timeoutErr.Timeout, float64(5*time.Millisecond),
		"must report the timeout actually requested, not ~0s from time.Until(deadline) computed after the timer already fired")
	assert.Greater(t, timeoutErr.Timeout, 100*time.Millisecond,
		"the bug this regresses to reports a near-zero duration; anything under 100ms means it's back")
}

// TestWithFileLockTimeout_AlreadyPastDeadline covers acquireInProcess's
// negative-duration clamp: a caller whose deadline has already passed by
// the time it calls in (a slow caller building the deadline earlier, then
// getting delayed before the actual lock call) must fail fast, not panic
// or block on a negative-duration timer.
func TestWithFileLockTimeout_AlreadyPastDeadline(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "past-deadline.dat")

	release := make(chan struct{})
	held := make(chan struct{})
	go func() {
		_ = WithFileLock(context.Background(), target, func() error { close(held); <-release; return nil })
	}()
	<-held
	defer close(release)

	start := time.Now()
	err := WithFileLockTimeout(context.Background(), target, -1*time.Second, func() error {
		return errors.New("must not run")
	})
	elapsed := time.Since(start)

	var timeoutErr *ErrLockTimeout
	require.ErrorAs(t, err, &timeoutErr)
	assert.Equal(t, time.Duration(0), timeoutErr.Timeout, "an already-past deadline clamps to zero, not a negative duration")
	assert.Less(t, elapsed, time.Second, "must fail immediately, not block on a negative-duration timer")
}

// TestWithFileLockTimeout_InProcessContextCancel covers acquireInProcess's
// OWN ctx.Done() branch specifically — contention from another goroutine in
// the SAME process, not the cross-process flock's separate ctx check
// (TestWithFileLock_RespectsContext exercises that one, via a child process
// whose lock never contends the in-process gate at all). This is exactly
// the path two of ADR-030's four WithRepoLock sites depend on: the daemon's
// sync scheduler and its GC/wedge probe are goroutines in the same process,
// not separate ones.
func TestWithFileLockTimeout_InProcessContextCancel(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "inprocess-cancel.dat")

	release := make(chan struct{})
	held := make(chan struct{})
	go func() {
		_ = WithFileLock(context.Background(), target, func() error { close(held); <-release; return nil })
	}()
	<-held
	defer close(release)

	ctx, cancel := context.WithCancel(context.Background())
	doneCh := make(chan error, 1)
	go func() {
		doneCh <- WithFileLockTimeout(ctx, target, LockTimeout, func() error { return nil })
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case err := <-doneCh:
		assert.ErrorIs(t, err, context.Canceled, "in-process contention must respect ctx cancellation, not just the timeout")
	case <-time.After(2 * time.Second):
		t.Fatal("did not return after context cancel — in-process gate ignored ctx.Done()")
	}
}

// TestWithFileLockTimeout_CrossProcessGenuineTimeout exercises the
// cross-process flock polling loop's OWN deadline check — distinct from
// every other timeout test here, which all contend at the in-process gate
// and never reach this loop at all. A child-process holder bypasses the
// in-process map entirely, so this is the only way to prove the flock-level
// ErrLockTimeout actually fires when the lock is genuinely still held.
func TestWithFileLockTimeout_CrossProcessGenuineTimeout(t *testing.T) {
	if !crossProcessFlockAvailable() {
		t.Skip("cross-process flock not available on this platform")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "genuine-timeout.dat")

	holder, release := startLockHolder(t, target)
	defer release()
	<-holder.acquired

	start := time.Now()
	err := WithFileLockTimeout(context.Background(), target, 100*time.Millisecond, func() error {
		return errors.New("must not run — the child process still holds the lock")
	})
	elapsed := time.Since(start)

	var timeoutErr *ErrLockTimeout
	require.ErrorAs(t, err, &timeoutErr)
	assert.GreaterOrEqual(t, elapsed, 100*time.Millisecond, "must actually wait out the requested timeout, not return early")
	assert.Less(t, elapsed, 2*time.Second, "must not wait past the requested timeout")
}
