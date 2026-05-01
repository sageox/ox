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
	"crypto/sha256"
	"encoding/hex"
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
// Lock files live in the OS tmpdir under `sageox-locks/`, keyed by an
// absolute-path hash of the target. Why NOT alongside the target:
//
//   - `<ledger>/sessions/<name>/meta.json`'s sibling `.meta.json.lock`
//     would live inside a git-tracked tree. A crashed writer leaves an
//     empty lock file there; the next `git add .` (user) or any glob
//     that didn't anticipate `.lock` files would commit it. We can't
//     rely on every adjacent `.gitignore` covering `.*.lock`.
//   - User-visible: even with the dot prefix, `ls -a` shows the lock
//     and prompts "what is this?" support questions.
//   - GC / blue-green reclone: any extra file inside a sparse-checkout
//     tree is a state-divergence risk.
//
// Storing in OS tmpdir sidesteps all three. The hash is sha256 of the
// abs target path truncated to 16 hex chars — collisions among the
// bounded set of lock targets in ox (one per session, plus a few
// per-project files) are statistically zero, and even if one occurred
// it'd just serialize two unrelated writers harmlessly.
//
// Tradeoff accepted: locks don't survive a reboot. That's correct —
// after reboot every prior holder is dead and the lock should be
// gone. It also means the OS tmpdir cleaner reaps stale lock files
// for free, no per-session cleanup logic needed.
func LockPath(targetPath string) string {
	abs, err := filepath.Abs(targetPath)
	if err != nil {
		abs = targetPath // best-effort; relative paths still hash deterministically
	}
	sum := sha256.Sum256([]byte(abs))
	name := hex.EncodeToString(sum[:8]) + ".lock"
	return filepath.Join(lockDir(), name)
}

// lockDir returns (and lazily creates) the per-user tmpdir we store
// lock files in. Best-effort: if MkdirAll fails the subsequent
// OpenFile will surface the error to the caller.
func lockDir() string {
	dir := filepath.Join(os.TempDir(), "sageox-locks")
	_ = os.MkdirAll(dir, 0o700)
	return dir
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

	// lockDir() creates the parent on first call; nothing else to
	// pre-flight here — OpenFile below will surface any real error.

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
