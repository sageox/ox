//go:build windows

package daemon

// Windows fallback for AcquireKBLock. The daemon's KB sync path is
// POSIX-first (XDG paths, flock(2), Unix-style sockets). Until cross-
// process locking is wired up via LockFileEx, return acquired=true so
// the caller proceeds. The race the unix backend prevents is still
// possible on Windows; documented as a known gap.
//
// TODO: implement via LockFileEx (windows.LockFileEx with
// LOCKFILE_EXCLUSIVE_LOCK | LOCKFILE_FAIL_IMMEDIATELY) when Windows
// daemon support lands. See ADR-017 §5 / bead ox-kdt2.

import (
	"fmt"
	"os"
)

func platformAcquireKBLock(path string) (unlock func(), acquired bool, err error) {
	// Touch the file so the path exists on disk (parity with unix path)
	// but do not actually serialize — single-daemon-per-host is the
	// current Windows posture.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, fmt.Errorf("open kb lock file %s: %w", path, err)
	}
	unlock = func() { _ = f.Close() }
	return unlock, true, nil
}
