//go:build windows

package skillmanager

// Windows backend for acquireApplyLock.
//
// This is a KNOWN GAP, stated plainly rather than hidden: it creates the lock
// file for path parity but does not serialize, so two concurrent applies in one
// repository can still interleave on Windows. It mirrors the existing posture in
// internal/daemon/kb_lock_windows.go.
//
// The gap matters more here than it does for the daemon, because Windows is a
// first-class target for the skill inventory (copy materialization is exactly
// what Windows gets). Implementing this via LockFileEx with
// LOCKFILE_EXCLUSIVE_LOCK | LOCKFILE_FAIL_IMMEDIATELY is the correct fix.

import (
	"fmt"
	"os"
)

func platformAcquireApplyLock(path string) (unlock func(), acquired bool, err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, fmt.Errorf("open skills apply lock %s: %w", path, err)
	}
	unlock = func() { _ = f.Close() }
	return unlock, true, nil
}
