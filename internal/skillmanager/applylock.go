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
	"path/filepath"
)

// ErrApplyInProgress reports that another ox process (or another goroutine in
// this one) currently holds the apply lock for this repository. It is an
// expected outcome, not a fault: the other holder is doing the same work.
var ErrApplyInProgress = errors.New("skills: another ox process is reconciling this repository")

// applyLockRelativePath sits next to the apply journal, under the already
// gitignored .sageox/cache/, so the lock never becomes a tracked file.
const applyLockRelativePath = ".sageox/cache/skills-apply.lock"

func applyLockPath(repoRoot string) string {
	return filepath.Join(repoRoot, filepath.FromSlash(applyLockRelativePath))
}

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
	path := applyLockPath(repoRoot)
	// ensureDir, not os.MkdirAll: MkdirAll happily walks THROUGH a symlinked
	// .sageox/cache, so a repository whose cache directory points elsewhere would
	// have ox create and flock a file outside itself. ensureDir refuses a symlink
	// at any component, matching every other write path in this package.
	if err := ensureDir(repoRoot, filepath.Dir(path)); err != nil {
		return nil, false, fmt.Errorf("create skills lock dir: %w", err)
	}
	// The lock file itself gets the same treatment: os.OpenFile follows a symlink,
	// so without this a symlinked skills-apply.lock redirects the open.
	if info, statErr := os.Lstat(path); statErr == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return nil, false, fmt.Errorf("refusing non-regular or symlink skills apply lock %s", path)
		}
	} else if !os.IsNotExist(statErr) {
		return nil, false, fmt.Errorf("inspect skills apply lock: %w", statErr)
	}
	return platformAcquireApplyLock(path)
}
