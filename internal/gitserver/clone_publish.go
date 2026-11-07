package gitserver

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// A full clone is written into a temp sibling of its target and only moved
// into place once it is complete. A clone that dies mid-transfer (a dropped
// link, kill -9, a laptop that sleeps) otherwise leaves a half-written .git at
// the target, HEAD pointing at refs/heads/.invalid, which the daemon and every
// CLI command then treat as a real repository. The daemon's Checkout and
// doctor's direct Ledger clone both publish through these helpers, so the two
// cannot drift.

// ErrTargetIsSymlink reports a clone target that is a symlink. A symlinked
// target is a coworker's relocation of the checkout; moving it aside or
// renaming a clone over it would undo that move (and could replace a link to
// data the clone knows nothing about), so nothing touches it.
var ErrTargetIsSymlink = errors.New("clone target is a symlink")

// RejectSymlinkTarget returns ErrTargetIsSymlink when target is itself a
// symlink (including a dangling one). It uses Lstat on purpose: os.Stat would
// follow the link and describe whatever it points at.
func RejectSymlinkTarget(target string) error {
	if fi, err := os.Lstat(target); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: %s (remove or repoint the link, then re-run `ox doctor --fix`)", ErrTargetIsSymlink, target)
	}
	return nil
}

// VerifyClone checks that the clone at dir is complete: it has a .git and its
// HEAD resolves. An early-EOF clone usually exits non-zero, but can also exit
// zero and leave a HEAD that resolves to nothing, as a clone of an empty
// remote does.
func VerifyClone(ctx context.Context, dir string) error {
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		return fmt.Errorf("clone verification failed: temp clone has no .git directory: %w", err)
	}
	if err := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--verify", "-q", "HEAD").Run(); err != nil {
		return fmt.Errorf("clone verification failed: temp clone's HEAD does not resolve: %w", err)
	}
	return nil
}

// MoveCloneIntoPlace renames a verified clone at tempPath to target: the only
// moment target goes from absent to a complete repository. Renaming onto a
// directory with content fails rather than merging into it, so a target that
// someone else filled is reported, not corrupted.
//
// One exception: a target holding only a .sageox/cache skeleton is not a
// clone. CodeDB creates its shared index at <ledger>/.sageox/cache/codedb as
// soon as it opens, which can be before the first clone lands. That cache is
// carried into the clone (AdoptCacheOnlyTarget) so the rename can land instead
// of failing "file exists" forever, and handed back if the rename fails.
func MoveCloneIntoPlace(tempPath, target string) error {
	if err := RejectSymlinkTarget(target); err != nil {
		return err
	}
	restoreCache, adoptErr := AdoptCacheOnlyTarget(tempPath, target)
	if err := os.Rename(tempPath, target); err != nil {
		// the caller deletes the temp clone; give the adopted cache back
		restoreCache()
		if adoptErr != nil {
			return fmt.Errorf("failed to move completed clone into place: %w (could not adopt the cache-only target: %w)", err, adoptErr)
		}
		return fmt.Errorf("failed to move completed clone into place: %w", err)
	}
	return nil
}

// IsCacheOnlyDir reports whether dir is a local-only skeleton: nothing except
// .sageox/cache (optionally empty) plus, at most, empty directories (an empty
// sessions/ folder is what a recording leaves before the first clone). All
// entries must be real directories. That is what CodeDB, the whisper store and
// session recording leave when they run before the first clone lands; it is not
// a clone and must not be treated as one, nor moved aside as corrupt.
func IsCacheOnlyDir(dir string) bool {
	// a symlinked target is a coworker's relocation, not a skeleton: adopting
	// it would remove the link and the clone would silently undo the move
	if fi, err := os.Lstat(dir); err != nil || fi.Mode()&os.ModeSymlink != 0 {
		return false
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) == 0 {
		return false
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			return false
		}
		if entry.Name() != ".sageox" {
			if !onlyEmptyDirs(filepath.Join(dir, entry.Name())) {
				return false
			}
			continue
		}
		inner, err := os.ReadDir(filepath.Join(dir, ".sageox"))
		if err != nil || len(inner) > 1 {
			return false
		}
		if len(inner) == 1 && (inner[0].Name() != "cache" || !inner[0].IsDir()) {
			return false
		}
	}
	return true
}

// onlyEmptyDirs reports whether dir is a tree of real directories holding no
// files, symlinks, or anything else.
func onlyEmptyDirs(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if !entry.IsDir() || !onlyEmptyDirs(filepath.Join(dir, entry.Name())) {
			return false
		}
	}
	return true
}

// removeEmptyDirs removes an empty-directory tree bottom-up with os.Remove only,
// so unexpected content makes it fail instead of being deleted.
func removeEmptyDirs(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := removeEmptyDirs(filepath.Join(dir, entry.Name())); err != nil {
			return err
		}
	}
	return os.Remove(dir)
}

// AdoptCacheOnlyTarget handles a clone target that exists but holds nothing
// except <target>/.sageox/cache. It moves that cache into the fresh clone at
// tempPath and removes the empty skeleton so the caller's rename can land.
// A target with any other content, or a cache that is not a real directory,
// is left alone and the rename fails loudly as before.
//
// The returned restore func (never nil) moves an adopted cache back to target;
// call it if the rename fails, because the temp clone is then deleted with the
// cache inside it.
func AdoptCacheOnlyTarget(tempPath, target string) (restore func(), err error) {
	restore = func() {}
	if !IsCacheOnlyDir(target) {
		return restore, nil
	}
	sageoxDir := filepath.Join(target, ".sageox")
	inner, err := os.ReadDir(sageoxDir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return restore, err
	}
	if len(inner) == 1 {
		// never move local data through a symlinked .sageox in the clone
		tempSageox := filepath.Join(tempPath, ".sageox")
		if fi, lerr := os.Lstat(tempSageox); lerr == nil && !fi.IsDir() {
			return restore, fmt.Errorf("clone's .sageox is not a directory")
		}
		src := filepath.Join(sageoxDir, "cache")
		dst := filepath.Join(tempSageox, "cache")
		if _, lerr := os.Lstat(dst); lerr == nil {
			// the clone already ships a cache dir; the old one is derived data
			if err := os.RemoveAll(src); err != nil {
				return restore, err
			}
		} else {
			if err := os.MkdirAll(tempSageox, 0755); err != nil {
				return restore, err
			}
			if err := os.Rename(src, dst); err != nil {
				return restore, err
			}
			restore = func() {
				if _, lerr := os.Lstat(src); lerr == nil {
					return
				}
				if os.MkdirAll(sageoxDir, 0755) == nil {
					_ = os.Rename(dst, src)
				}
			}
		}
	}
	// remove only empty directories; never recurse into unexpected content
	top, err := os.ReadDir(target)
	if err != nil {
		restore()
		return func() {}, err
	}
	for _, entry := range top {
		if err := removeEmptyDirs(filepath.Join(target, entry.Name())); err != nil {
			restore()
			return func() {}, err
		}
	}
	if err := os.Remove(target); err != nil {
		restore()
		return func() {}, err
	}
	return restore, nil
}
