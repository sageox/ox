//go:build windows

package daemon

// Windows fallback for AcquireGlobalSyncLease. Daemon support on Windows
// is POSIX-first and single-daemon-per-host, so the cross-process race
// the unix backend prevents doesn't materialize in the current shipping
// posture. Return acquired=true so the caller proceeds with global-sync
// work.
//
// TODO: implement via LockFileEx when Windows multi-daemon support
// lands. See ADR-017 §5 and bead ox-6zme.

import (
	"fmt"
	"os"
)

func platformAcquireGlobalLease(path string) (*os.File, bool, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, fmt.Errorf("open global lease file %s: %w", path, err)
	}
	return f, true, nil
}

func platformReleaseGlobalLease(f *os.File) error {
	if f == nil {
		return nil
	}
	return f.Close()
}
