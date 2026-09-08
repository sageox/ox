//go:build windows

package skillmanager

// Windows backend for acquireApplyLock, using LockFileEx.
//
// This used to return acquired=true without taking a lock, mirroring the daemon's
// older posture. That is a worse gap here than it is there: Windows is a
// first-class target for the skill inventory — copy materialization is exactly
// what Windows gets — so an unlocked apply lets `ox init`, `ox doctor --fix`, the
// daemon tick, and `ox agent prime` interleave after planning and overwrite or
// remove one another's managed files and lockfile state.
//
// LOCKFILE_EXCLUSIVE_LOCK | LOCKFILE_FAIL_IMMEDIATELY gives the same
// non-blocking exclusive semantics the POSIX backend gets from
// flock(LOCK_EX|LOCK_NB): contention returns acquired=false rather than waiting,
// because the caller — often a session start — must never block on a peer.

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

func platformAcquireApplyLock(path string) (unlock func(), acquired bool, err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, fmt.Errorf("open skills apply lock %s: %w", path, err)
	}
	h := windows.Handle(f.Fd())
	var overlapped windows.Overlapped
	lockErr := windows.LockFileEx(
		h,
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0,
		1, 0, // lock one byte; the region is a token, not a data range
		&overlapped,
	)
	if lockErr != nil {
		_ = f.Close()
		// ERROR_LOCK_VIOLATION is the expected contention signal for
		// LOCKFILE_FAIL_IMMEDIATELY; anything else is a real failure.
		if errors.Is(lockErr, windows.ERROR_LOCK_VIOLATION) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("lock skills apply lock: %w", lockErr)
	}
	unlock = func() {
		var o windows.Overlapped
		_ = windows.UnlockFileEx(h, 0, 1, 0, &o)
		_ = f.Close()
	}
	return unlock, true, nil
}
