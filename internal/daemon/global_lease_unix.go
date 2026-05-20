//go:build unix

package daemon

// POSIX flock(2) backend for AcquireGlobalSyncLease. Mirrors the
// kb_lock_unix.go strategy: stdlib syscall.Flock, LOCK_EX|LOCK_NB, both
// EWOULDBLOCK and EAGAIN treated as "another holder."
//
// Unlike kb_lock.go, the lease file is held open for the daemon's
// lifetime and only released on shutdown; there is no per-tick acquire
// path here. The file is created with mode 0600 on first acquisition
// and never deleted (deleting it would create a TOCTOU race with another
// daemon's acquire).

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// platformAcquireGlobalLease opens (or creates) the lease file and
// attempts a non-blocking exclusive flock. Returns the open *os.File on
// success so Release can flock(LOCK_UN) + close the same fd; otherwise
// returns (nil, false, nil) for "another holder" or (nil, false, err)
// for unexpected filesystem failure.
func platformAcquireGlobalLease(path string) (*os.File, bool, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, fmt.Errorf("open global lease file: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("flock global lease file: %w", err)
	}
	return f, true, nil
}

// platformReleaseGlobalLease releases the flock and closes the fd. Errors
// from LOCK_UN (EBADF on already-closed fd) are swallowed; the caller's
// lifecycle is unaffected.
func platformReleaseGlobalLease(f *os.File) error {
	if f == nil {
		return nil
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return f.Close()
}
