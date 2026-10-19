package gitserver

import (
	"context"
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

// IsCacheOnlyDir reports whether dir holds nothing except
// .sageox/cache (optionally empty), all real directories. That is what CodeDB
// leaves when it opens its shared index before the first clone lands; it is
// not a clone and must not be treated as one, nor moved aside as corrupt.
func IsCacheOnlyDir(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != ".sageox" || !entries[0].IsDir() {
		return false
	}
	inner, err := os.ReadDir(filepath.Join(dir, ".sageox"))
	if err != nil || len(inner) > 1 {
		return false
	}
	return len(inner) == 0 || (inner[0].Name() == "cache" && inner[0].IsDir())
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
	if err != nil {
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
	if err := os.Remove(sageoxDir); err != nil {
		restore()
		return func() {}, err
	}
	if err := os.Remove(target); err != nil {
		restore()
		return func() {}, err
	}
	return restore, nil
}
