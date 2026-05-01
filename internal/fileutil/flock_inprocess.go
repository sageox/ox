package fileutil

import "sync"

// inProcessLocks is a process-wide map of lock-path → mutex. It exists
// because POSIX flock semantics differ across kernels for two goroutines
// in the SAME process opening the SAME lock file: on some platforms the
// second open inherits the lock and tryFlock succeeds spuriously. The
// in-process mutex closes that loophole so two goroutines in the same
// binary can't both believe they hold the lock.
//
// Keyed by the absolute lock path. Cleared lazily — entries don't get
// removed when no one is contending, but the memory cost is one
// sync.Mutex per locked path, which is fine for the bounded set of
// shared files in ox (meta.json per session, config.local.toml,
// AGENTS.md / CLAUDE.md / SOUL.md).
var inProcessLocks struct {
	sync.Mutex
	m map[string]*sync.Mutex
}

// acquireInProcess returns a release function. Locks the process-wide
// mutex for `lockPath` and returns a closure that unlocks it. Pair with
// `defer release()`.
func acquireInProcess(lockPath string) func() {
	inProcessLocks.Lock()
	if inProcessLocks.m == nil {
		inProcessLocks.m = map[string]*sync.Mutex{}
	}
	mu, ok := inProcessLocks.m[lockPath]
	if !ok {
		mu = &sync.Mutex{}
		inProcessLocks.m[lockPath] = mu
	}
	inProcessLocks.Unlock()
	mu.Lock()
	return mu.Unlock
}
