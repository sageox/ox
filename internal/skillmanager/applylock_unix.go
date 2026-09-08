//go:build unix

package skillmanager

// POSIX flock(2) backend for acquireApplyLock. Uses stdlib syscall rather than
// golang.org/x/sys/unix: syscall.Flock exists on Linux, macOS, and the BSDs, and
// the project rule is to keep the dependency surface minimal when stdlib suffices.

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

func platformAcquireApplyLock(path string) (unlock func(), acquired bool, err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, fmt.Errorf("open skills apply lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		// EWOULDBLOCK and EAGAIN both mean "another holder"; they are the same
		// value on Linux but distinct on some BSDs, so test for both.
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("flock skills apply lock: %w", err)
	}
	unlock = func() {
		// LOCK_UN can report EBADF if the fd is already closed; harmless for the
		// caller's lifecycle, and the kernel releases the lock on close regardless.
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}
	return unlock, true, nil
}
