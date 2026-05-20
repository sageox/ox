//go:build unix

package daemon

// POSIX flock(2) backend for AcquireKBLock. Uses stdlib syscall — we
// deliberately avoid golang.org/x/sys/unix here since `syscall.Flock` is
// available on Linux, macOS, and the BSDs and the project rule is to
// keep the dependency surface minimal when stdlib suffices.

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// platformAcquireKBLock opens (or creates) the lock file and attempts a
// non-blocking exclusive flock. On EWOULDBLOCK / EAGAIN the file is
// closed and (nil, false, nil) is returned. On success the unlock
// closure releases the lock and closes the fd; both are safe to call
// from a defer.
func platformAcquireKBLock(path string) (unlock func(), acquired bool, err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, fmt.Errorf("open kb lock file: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		// Both EWOULDBLOCK and EAGAIN signal "another holder" on POSIX
		// systems; they are the same value on Linux but distinct on some
		// BSDs. Treat both as the expected contention case.
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("flock kb lock file: %w", err)
	}
	unlock = func() {
		// LOCK_UN may return EBADF if the fd was already closed; not a
		// real failure for the caller's lifecycle, swallow it.
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}
	return unlock, true, nil
}
