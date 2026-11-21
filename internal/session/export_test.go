package session

import "testing"

// SetHoldLockAttempt makes f run just before WriteHoldMarkerFor asks for
// live.lock, for the length of t.
func SetHoldLockAttempt(t testing.TB, f func()) {
	t.Helper()
	prev := holdLockAttempt
	holdLockAttempt = f
	t.Cleanup(func() { holdLockAttempt = prev })
}
