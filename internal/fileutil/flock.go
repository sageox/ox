// Package fileutil — advisory file lock for cross-process serialization
// of read-modify-write sequences on shared config / manifest files.
//
// We need this because several files in ox are written by both the daemon
// and the CLI (and sometimes by a second daemon in another worktree):
// `<ledger>/sessions/<name>/meta.json`, `.sageox/config.local.toml`, and
// the marker-managed AGENTS.md / CLAUDE.md / SOUL.md. AtomicWriteBytes
// stops a single writer from leaving a torn file on the floor; it does
// nothing about two writers each doing
//
//	cfg := Read()                    -+
//	cfg.X = ...                       |  read-modify-write window
//	AtomicWriteBytes(p, Marshal(cfg)) -+
//
// concurrently. Whichever process writes second clobbers the other's
// in-memory copy. WithFileLock wraps the whole RMW window in an advisory
// flock so the two processes serialize at the file-system level.
//
// Lock file shape: a sibling `.<basename>.lock` next to the target. The
// lock file is intentionally separate from the data file — locking the
// data file directly would race with our own atomic temp+rename
// (rename(2) replaces the inode the lock is held against).
package fileutil

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// LockTimeout is the maximum time WithFileLock will wait to acquire an
// advisory lock before returning ErrLockTimeout. Generous because the
// expected hold time is a millisecond-scale read-modify-write; if we
// can't get the lock in this window, something is genuinely stuck.
const LockTimeout = 10 * time.Second

// LockPollInterval is how often WithFileLock retries while the lock is
// held by another process. Short enough that contention completes
// quickly; long enough not to spin the CPU.
const LockPollInterval = 25 * time.Millisecond

// ErrLockTimeout is returned by WithFileLock when LockTimeout elapses
// without acquiring the lock.
type ErrLockTimeout struct{ Path string }

func (e *ErrLockTimeout) Error() string {
	return fmt.Sprintf("acquire advisory lock %q: timed out after %s", e.Path, LockTimeout)
}

// LockPath returns the canonical advisory-lock sidecar path for a target
// data file. Exposed so tests and callers that need to clean up stale
// locks know where to look.
//
// Convention: alongside the target with a leading dot and `.lock`
// suffix, e.g.
//
//	/x/sessions/abc/meta.json    -> /x/sessions/abc/.meta.json.lock
//	/y/.sageox/config.local.toml -> /y/.sageox/.config.local.toml.lock
//
// The dot prefix is intentional: keeps the lock out of `ls`, ignored by
// most globs, and obviously not user-facing.
func LockPath(targetPath string) string {
	dir, base := filepath.Split(targetPath)
	if dir == "" {
		dir = "."
	}
	return filepath.Join(dir, "."+base+".lock")
}

// WithFileLock acquires an advisory exclusive lock on the sidecar lock
// file for `targetPath`, runs `fn` with the lock held, and releases it
// (always, even if `fn` panics).
//
// Use this around any read-modify-write sequence on a file that may be
// touched concurrently by another process. Both readers and writers
// must use it (or readers may observe a torn intermediate state); for
// reads-only fast paths it's optional, since AtomicWriteBytes guarantees
// the readable bytes are a coherent snapshot of *some* committed write.
//
// Implementation note: this is advisory only. Cooperating processes
// must all go through WithFileLock; a rogue process can ignore the
// lock and corrupt state. We accept that — every writer in ox is in
// the same codebase, and a rogue writer is a bug we'd notice anyway.
//
// On platforms without flock (currently none we ship to, but keep
// portability): the function still serializes within the process via
// inProcessLocks, so two goroutines in the same binary are safe even
// if cross-process locking is a no-op.
func WithFileLock(ctx context.Context, targetPath string, fn func() error) error {
	lockPath := LockPath(targetPath)

	// Ensure the lock file's directory exists. Don't create the lock
	// file's parents recursively — the caller's data file should
	// already live in an extant directory; if not, that's a real
	// caller error we'd rather surface here than mask.
	if _, err := os.Stat(filepath.Dir(lockPath)); err != nil {
		return fmt.Errorf("lock directory missing for %q: %w", targetPath, err)
	}

	// In-process serialization first. Two goroutines in the same
	// process trying to flock the same FD do NOT block each other on
	// some POSIX implementations (flock-by-FD vs flock-by-inode
	// semantics differ across kernels). We avoid the corner case by
	// gating on a process-wide named mutex keyed by the lock path.
	relock := acquireInProcess(lockPath)
	defer relock()

	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open lock file %q: %w", lockPath, err)
	}
	defer f.Close()

	deadline := time.Now().Add(LockTimeout)
	for {
		if err := tryFlock(f); err == nil {
			break
		}
		if time.Now().After(deadline) {
			return &ErrLockTimeout{Path: lockPath}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(LockPollInterval):
		}
	}
	defer func() { _ = unlockFlock(f) }()

	return fn()
}
