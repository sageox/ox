//go:build unix

package fileutil

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// tryFlock attempts a non-blocking exclusive lock. Returns nil on
// success, or an error (typically EWOULDBLOCK) if another process
// holds the lock. Callers retry; the polling cadence lives in
// WithFileLock.
func tryFlock(f *os.File) error {
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		// Wrap so callers (and the timeout path) can still match a
		// real I/O error vs the expected "would block" via errors.Is
		// against the underlying syscall error.
		return err
	}
	return nil
}

// unlockFlock releases an advisory lock previously acquired via
// tryFlock. Errors are logged by the caller (via defer) since by the
// time we're unlocking the success path has already happened.
func unlockFlock(f *os.File) error {
	err := unix.Flock(int(f.Fd()), unix.LOCK_UN)
	if err != nil && !errors.Is(err, unix.EBADF) {
		// EBADF can happen if the file was closed before unlock — not
		// a real failure for our caller, just defensive.
		return err
	}
	return nil
}
