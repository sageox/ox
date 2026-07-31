//go:build unix

package agentwork

// POSIX flock(2) backend for acquireLedgerLease. Mirrors
// daemon/global_lease_unix.go: stdlib syscall.Flock, LOCK_EX|LOCK_NB, with both
// EWOULDBLOCK and EAGAIN meaning "another process holds it."
//
// The lease file is never deleted — unlinking it would let a second process
// create and lock a different inode while the first still holds the old one.

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

func platformAcquireLedgerLease(path string) (*os.File, bool, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, fmt.Errorf("open ledger lease file: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("flock ledger lease file: %w", err)
	}
	return f, true, nil
}

func platformReleaseLedgerLease(f *os.File) error {
	if f == nil {
		return nil
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_UN); err != nil {
		_ = f.Close()
		return fmt.Errorf("unlock ledger lease file: %w", err)
	}
	return f.Close()
}
