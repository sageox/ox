package livesession

import "time"

// SetPrepareReopenStateHook makes PrepareReopen's state write fail (or run fn
// in its place) until the returned restore is called.
func SetPrepareReopenStateHook(fn func() error) (restore func()) {
	prepareReopenStateHook = fn
	return func() { prepareReopenStateHook = nil }
}

// SetReopenAwaitWindow makes a wait AwaitReopen starts last d (a negative d
// starts it already expired) until the returned restore is called.
func SetReopenAwaitWindow(d time.Duration) (restore func()) {
	prev := reopenAwaitWindow
	reopenAwaitWindow = d
	return func() { reopenAwaitWindow = prev }
}

// SetReopenAwaitedStateHook makes the state write that finishes a deferred
// reopen fail (or run fn in its place) until the returned restore is called.
func SetReopenAwaitedStateHook(fn func() error) (restore func()) {
	reopenAwaitedStateHook = fn
	return func() { reopenAwaitedStateHook = nil }
}
