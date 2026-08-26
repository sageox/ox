package testguard

import (
	"runtime"
	"testing"
	"time"
)

// RequireNoFDLeak snapshots the open file descriptor count at call time and
// registers a t.Cleanup that fails the test if FDs grew beyond a small delta.
// Use it in any test that exercises resource-acquiring code (fsnotify watchers,
// go-git repos, database connections) to catch leaked handles.
//
// The allowed delta accounts for runtime noise (GC finalizers, runtime file
// table resizing). Set it higher only if the test legitimately opens
// long-lived descriptors that outlive the cleanup.
//
// Platforms: works on macOS and Linux via dup2 probing.
// Silently skips on unsupported platforms (Windows).
func RequireNoFDLeak(t *testing.T) {
	t.Helper()
	RequireNoFDLeakWithDelta(t, 5)
}

// RequireNoFDLeakWithDelta is like RequireNoFDLeak but accepts a custom delta.
func RequireNoFDLeakWithDelta(t *testing.T, allowedDelta int) {
	t.Helper()

	before := countOpenFDs()
	if before < 0 {
		return // unsupported platform
	}

	t.Cleanup(func() {
		// let finalizers and deferred closes run
		runtime.GC()
		time.Sleep(50 * time.Millisecond)
		runtime.GC()

		after := countOpenFDs()
		if after < 0 {
			return
		}
		leaked := after - before
		if leaked > allowedDelta {
			t.Errorf("FD leak detected: %d open before test, %d after (+%d, allowed +%d)",
				before, after, leaked, allowedDelta)
		}
	})
}
