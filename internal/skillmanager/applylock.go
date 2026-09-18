package skillmanager

// Cross-process serialization for Apply.
//
// Apply is a multi-step mutation — journal, then per-file writes and removes,
// then the lockfile — and it is now reachable from four callers: `ox init`,
// `ox doctor --fix`, the daemon's autofix tick, and (since the inventory became
// gitignored, locally materialized state) `ox agent prime` on the session hot
// path. Two of those can fire at the same instant in the same repository: two
// agent sessions starting together, or a session starting while the daemon's
// 30-minute tick runs.
//
// Interleaved applies are not merely wasteful. Both processes plan against the
// same on-disk digests, so the loser can write a file the winner just retired,
// or stamp a lockfile that disagrees with what is actually on disk — precisely
// the ownership drift the digest model exists to prevent.
//
// The lock is advisory POSIX flock(2) on a file beside the journal, and it is
// deliberately NON-BLOCKING: prime must never wait on a lock, and a doctor run
// that silently blocked would look like a hang. A caller that loses the race
// gets ErrApplyInProgress and decides for itself — prime skips in silence,
// doctor says so out loud.
//
// flock is per-open-file-description, so this correctly serializes two
// goroutines in one process as well as two processes. Windows gets the same
// non-blocking exclusive semantics from LockFileEx.

import (
	"errors"
	"fmt"
	"os"
)

// ErrApplyInProgress reports that another ox process (or another goroutine in
// this one) currently holds the apply lock for this repository. It is an
// expected outcome, not a fault: the other holder is doing the same work.
var ErrApplyInProgress = errors.New("skills: another ox process is reconciling this repository")

// applyLockRelativePath sits next to the apply journal, under the already
// gitignored .sageox/cache/, so the lock never becomes a tracked file.
const applyLockRelativePath = ".sageox/cache/skills-apply.lock"

// acquireApplyLock takes the per-repository apply lock without blocking.
//
// Returns (unlock, true, nil) on success; the caller must invoke unlock.
// Returns (nil, false, nil) when another holder has it.
// Returns (nil, false, err) only for genuine filesystem failures.
//
// The lock file is created if absent and is never removed: unlinking a file
// while another process holds a lock on it opens a TOCTOU window where two
// processes hold locks on two different inodes with the same name.
func acquireApplyLock(repoRoot string) (unlock func(), acquired bool, err error) {
	root, err := os.OpenRoot(repoRoot)
	if err != nil {
		return nil, false, fmt.Errorf("open repository root: %w", err)
	}
	defer func() { _ = root.Close() }()
	return acquireApplyLockInRoot(root)
}

func acquireApplyLockInRoot(root *os.Root) (unlock func(), acquired bool, err error) {
	parent, base, err := openRepoParent(root, applyLockRelativePath, true)
	if err != nil {
		return nil, false, fmt.Errorf("create skills lock dir: %w", err)
	}
	defer func() { _ = parent.Close() }()

	var file *os.File
	for range 2 {
		info, statErr := parent.Lstat(base)
		if os.IsNotExist(statErr) {
			file, err = parent.OpenFile(base, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
			if os.IsExist(err) {
				continue
			}
			if err != nil {
				return nil, false, fmt.Errorf("create skills apply lock: %w", err)
			}
			return platformAcquireApplyLock(file)
		}
		if statErr != nil {
			return nil, false, fmt.Errorf("inspect skills apply lock: %w", statErr)
		}
		if !info.Mode().IsRegular() {
			return nil, false, fmt.Errorf("refusing non-regular or symlink skills apply lock %s", base)
		}
		file, err = parent.OpenFile(base, os.O_RDWR, 0o600)
		if err != nil {
			return nil, false, fmt.Errorf("open skills apply lock: %w", err)
		}
		actual, statErr := file.Stat()
		if statErr != nil || !actual.Mode().IsRegular() || !os.SameFile(info, actual) {
			_ = file.Close()
			if statErr != nil {
				return nil, false, fmt.Errorf("inspect opened skills apply lock: %w", statErr)
			}
			return nil, false, fmt.Errorf("skills apply lock changed while opening")
		}
		return platformAcquireApplyLock(file)
	}
	return nil, false, fmt.Errorf("skills apply lock changed while creating")
}
