package fileutil

import (
	"context"
	"sync"
	"time"
)

// inProcessLocks is a process-wide map of lock-path -> a 1-buffered
// channel acting as a mutex. It exists because POSIX flock semantics
// differ across kernels for two goroutines in the SAME process opening
// the SAME lock file: on some platforms the second open inherits the
// lock and tryFlock succeeds spuriously. The in-process gate closes
// that loophole so two goroutines in the same binary can't both believe
// they hold the lock.
//
// A channel, not a sync.Mutex: WithFileLockTimeout's callers pass a
// context and a timeout that must bound the ENTIRE wait, including the
// in-process leg. A plain mu.Lock() cannot be interrupted, so two
// goroutines in the same process contending on a long-held lock (a git
// fetch/pull via gitutil.WithRepoLock, which can legitimately run tens
// of seconds — see ADR-030) would ignore both ctx cancellation and the
// caller's timeout and block forever behind a same-process peer. That
// gap is real: two of ADR-030's four lock sites (the daemon's sync
// scheduler pull cycle and its GC/wedge probe) run as goroutines in the
// SAME daemon process, not separate processes, so in-process contention
// on a repo lock is an expected, not a corner, case.
//
// Keyed by the absolute lock path. Cleared lazily — entries don't get
// removed when no one is contending, but the memory cost is one channel
// per locked path, which is fine for the bounded set of shared targets
// in ox (one per session, a handful of per-project files, one per
// managed git clone).
var inProcessLocks struct {
	sync.Mutex
	m map[string]chan struct{}
}

func inProcessChan(lockPath string) chan struct{} {
	inProcessLocks.Lock()
	defer inProcessLocks.Unlock()
	if inProcessLocks.m == nil {
		inProcessLocks.m = map[string]chan struct{}{}
	}
	ch, ok := inProcessLocks.m[lockPath]
	if !ok {
		ch = make(chan struct{}, 1)
		inProcessLocks.m[lockPath] = ch
	}
	return ch
}

// acquireInProcess blocks until the in-process gate for lockPath is free,
// honoring ctx.Done() and deadline. Returns a release function on success.
func acquireInProcess(ctx context.Context, lockPath string, deadline time.Time) (func(), error) {
	ch := inProcessChan(lockPath)

	var timerC <-chan time.Time
	if !deadline.IsZero() {
		d := time.Until(deadline)
		if d < 0 {
			d = 0
		}
		timer := time.NewTimer(d)
		defer timer.Stop()
		timerC = timer.C
	}

	select {
	case ch <- struct{}{}:
		return func() { <-ch }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timerC:
		return nil, &ErrLockTimeout{Path: lockPath, Timeout: time.Until(deadline)}
	}
}
