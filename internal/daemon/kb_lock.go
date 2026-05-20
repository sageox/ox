package daemon

// Cross-process serialization for per-kb_id work on the shared XDG kb
// working tree at paths.KBDir(kb_id). N per-repo daemons (one per
// workspace_id) all read from the same canonical bubble directory; an
// in-process sync.Mutex / sync.Map is therefore insufficient. Without a
// kernel-level file lock, two daemons can call cloneBubble on the same
// path simultaneously (corrupting the working tree) or GC can rename the
// active clone into .trash/ mid-write (yanking the inode out from under
// the other daemon's git process).
//
// We use advisory POSIX flock(2) on a lock file per kb_id. flock locks
// are per-open-file-description: even within a single process, opening
// the same lock file twice and attempting LOCK_EX|LOCK_NB on the second
// fd correctly returns EWOULDBLOCK. Locks are released on process death
// by the kernel — no stale-lock recovery code needed.
//
// Per .claude/rules/lfs-no-git-lfs-binary.md, this code does not depend
// on the git-lfs binary. Per project conventions, we use stdlib
// `syscall.Flock` rather than `golang.org/x/sys/unix` to keep the
// dependency surface minimal.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/sageox/ox/internal/paths"
)

// kbLockDirOnce guards lazy creation of the lock parent dir; we only need
// to attempt MkdirAll once per process to keep the hot path allocation-free.
var kbLockDirOnce sync.Once
var kbLockDirCached string
var kbLockDirErr error

// kbLockDirName is the subdirectory under DaemonStateDir() that holds the
// per-kb_id advisory lock files.
const kbLockDirName = "kb-locks"

// kbLockDir returns the directory containing per-kb_id flock files,
// creating it on first call. The directory lives under
// paths.DaemonStateDir() so it inherits the project's XDG vs legacy
// resolution and the existing daemon state cleanup posture.
func kbLockDir() (string, error) {
	kbLockDirOnce.Do(func() {
		base := paths.DaemonStateDir()
		if base == "" {
			kbLockDirErr = errors.New("daemon state dir unresolved")
			return
		}
		dir := filepath.Join(base, kbLockDirName)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			kbLockDirErr = fmt.Errorf("create kb lock dir: %w", err)
			return
		}
		kbLockDirCached = dir
	})
	if kbLockDirErr != nil {
		return "", kbLockDirErr
	}
	return kbLockDirCached, nil
}

// kbLockPath returns the absolute path to the lock file for a kb_id.
// Filename is `<kb_id>.lock`; kb_ids are URL-safe slugs (see kb API) so
// no further sanitization is needed.
func kbLockPath(kbID string) (string, error) {
	dir, err := kbLockDir()
	if err != nil {
		return "", err
	}
	// defense in depth: a kb_id containing a path separator would escape
	// the lock dir. The kb API does not emit such ids, but a stray bad
	// input must not be able to flock arbitrary files.
	if filepath.Base(kbID) != kbID || kbID == "" {
		return "", fmt.Errorf("invalid kb_id for lock: %q", kbID)
	}
	return filepath.Join(dir, kbID+".lock"), nil
}

// AcquireKBLock attempts a non-blocking exclusive flock on the kb_id's
// lock file. Returns:
//
//   - (unlock, true, nil) on success; the caller MUST invoke unlock
//     when finished (or rely on process exit; the kernel releases flock
//     on fd close / process termination).
//   - (nil, false, nil) when another holder (any process, including this
//     one via a different fd) holds the lock. The caller should skip
//     this kb_id this pass.
//   - (nil, false, err) on unexpected error (filesystem failure,
//     unresolvable state dir, invalid kb_id). The caller should log and
//     skip; we never escalate lock-infrastructure errors to a panic.
//
// The lock file is created with mode 0600 on first acquisition; it is
// never removed (the file is a name on which we hold a lock, removing
// it would create a TOCTOU race with another acquirer).
func AcquireKBLock(kbID string) (unlock func(), acquired bool, err error) {
	path, err := kbLockPath(kbID)
	if err != nil {
		return nil, false, err
	}
	return acquireKBLockAt(path)
}

// acquireKBLockAt is the platform-specific lock acquisition. Split out so
// tests can target a tmp lock dir without going through kbLockDir.
// Implementation lives in kb_lock_unix.go / kb_lock_windows.go.
func acquireKBLockAt(path string) (unlock func(), acquired bool, err error) {
	return platformAcquireKBLock(path)
}
