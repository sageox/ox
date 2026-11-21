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

// MoveAsideKeepingCache moves a broken clone target to backup so a fresh clone
// can land, but leaves <target>/.sageox/cache where it is. That cache is
// local state no clone brings back, including session recordings that hooks
// and the daemon keep writing to by path: renaming the whole target orphaned
// them in a .bak (live streaming went silent, and doctor deletes old .bak
// folders). Afterwards target is a cache-only skeleton the next clone adopts
// (AdoptCacheOnlyTarget). It returns the cache path it kept, or "" when there
// was no real cache directory and target was renamed whole.
//
// Every step is a rename and nothing is deleted, so a failure part-way leaves
// each entry either in target or in backup; the caller retries next cycle.
func MoveAsideKeepingCache(target, backup string) (kept string, err error) {
	sageoxDir := filepath.Join(target, ".sageox")
	cache := filepath.Join(sageoxDir, "cache")
	// a symlinked target (a relocation) is renamed as the link it is, never
	// emptied through it
	if !isRealDir(target) || !isRealDir(sageoxDir) || !isRealDir(cache) {
		return "", os.Rename(target, backup)
	}
	info, err := os.Stat(target)
	if err != nil {
		return "", err
	}
	// Mkdir, not MkdirAll: never merge into a backup that already exists
	if err := os.Mkdir(backup, info.Mode().Perm()); err != nil {
		return "", err
	}
	if err := moveEntriesExcept(target, backup, ".sageox"); err != nil {
		return "", err
	}
	if err := moveEntriesExcept(sageoxDir, filepath.Join(backup, ".sageox"), "cache"); err != nil {
		return "", err
	}
	_ = os.Remove(backup) // only succeeds when nothing but the cache was there
	return cache, nil
}

// moveEntriesExcept renames every entry of src except keep into dst, creating
// dst only when there is something to move.
func moveEntriesExcept(src, dst, keep string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Name() == keep {
			continue
		}
		if err := os.MkdirAll(dst, 0755); err != nil {
			return err
		}
		if err := os.Rename(filepath.Join(src, entry.Name()), filepath.Join(dst, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

func isRealDir(path string) bool {
	fi, err := os.Lstat(path)
	return err == nil && fi.IsDir()
}

// CarryCache moves <from>/.sageox/cache into <to>/.sageox/cache by rename,
// merging with whatever <to> already holds. A re-clone keeps local state
// (session recordings among it) this way instead of by copy: a copy taken
// before a minutes-long clone misses every line written after it. A file both
// trees hold stays in from and is reported, and the carry goes on past it;
// nothing is deleted. carried is true when from had a cache and all of it
// moved.
//
// Going on matters because <to> is a fresh clone of a repo anyone on the team
// can push to: one tracked file under .sageox/cache that sorts before
// sessions/ used to stop the carry there, and every live session stayed in
// the clone the caller sets aside, unstreamed, with hooks failing to append.
func CarryCache(from, to string) (carried bool, err error) {
	src := filepath.Join(from, ".sageox", "cache")
	if !isRealDir(src) {
		return false, nil
	}
	dstSageox := filepath.Join(to, ".sageox")
	if fi, lerr := os.Lstat(dstSageox); lerr == nil && !fi.IsDir() {
		return false, fmt.Errorf("carry cache: %s is not a directory", dstSageox)
	}
	if err := os.MkdirAll(dstSageox, 0755); err != nil {
		return false, err
	}
	if err := carryByRename(src, filepath.Join(dstSageox, "cache")); err != nil {
		return false, fmt.Errorf("carry cache from %s: %w", from, err)
	}
	return true, nil
}

// carryByRename is mergeByRename without the all-or-nothing contract: an
// entry that cannot move stays in src, every other entry still moves, and the
// errors are joined. Nothing is deleted except src directories it emptied.
func carryByRename(src, dst string) error {
	if _, err := os.Lstat(dst); errors.Is(err, os.ErrNotExist) {
		return os.Rename(src, dst)
	}
	if !isRealDir(dst) || !isRealDir(src) {
		return fmt.Errorf("%w: both %s and %s exist", ErrPathExists, src, dst)
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	var errs []error
	for _, entry := range entries {
		if err := carryByRename(filepath.Join(src, entry.Name()), filepath.Join(dst, entry.Name())); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return os.Remove(src)
}

// rename is one move mergeByRename made, kept so it can be undone.
type rename struct{ from, to string }

// mergeByRename moves src's contents into dst. An entry dst lacks is renamed
// across whole; a directory both hold is merged recursively; a file both hold
// stops the merge with an error and stays in src. Nothing is ever deleted
// except src directories the merge emptied. It returns the moves made, in
// order, for undoRenames.
func mergeByRename(src, dst string) ([]rename, error) {
	var moved []rename
	if _, err := os.Lstat(dst); errors.Is(err, os.ErrNotExist) {
		if err := os.Rename(src, dst); err != nil {
			return nil, err
		}
		return []rename{{from: src, to: dst}}, nil
	}
	if !isRealDir(dst) || !isRealDir(src) {
		return nil, fmt.Errorf("%w: both %s and %s exist", ErrPathExists, src, dst)
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		sub, err := mergeByRename(filepath.Join(src, entry.Name()), filepath.Join(dst, entry.Name()))
		moved = append(moved, sub...)
		if err != nil {
			return moved, err
		}
	}
	if err := os.Remove(src); err != nil {
		return moved, err
	}
	return append(moved, rename{from: src, to: ""}), nil
}

// undoRenames reverses mergeByRename's moves, newest first, recreating the
// source directories the merge removed.
func undoRenames(moved []rename) {
	for i := len(moved) - 1; i >= 0; i-- {
		m := moved[i]
		if m.to == "" {
			_ = os.MkdirAll(m.from, 0755)
			continue
		}
		_ = os.MkdirAll(filepath.Dir(m.from), 0755)
		_ = os.Rename(m.to, m.from)
	}
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
// The cache is never deleted, even when the clone ships a .sageox/cache of its
// own: it holds session recordings (raw.jsonl, the live spool and stream
// state), not only derived indexes. Its entries are merged in by rename; a
// file both sides hold refuses the adoption and everything goes back.
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
		if err := os.MkdirAll(tempSageox, 0755); err != nil {
			return restore, err
		}
		src := filepath.Join(sageoxDir, "cache")
		moved, err := mergeByRename(src, filepath.Join(tempSageox, "cache"))
		restore = func() { undoRenames(moved) }
		if err != nil {
			restore()
			return func() {}, fmt.Errorf("carry the local cache into the clone: %w", err)
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
