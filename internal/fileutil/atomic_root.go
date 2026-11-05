package fileutil

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// AtomicWriteBytesInRoot is the project-scoped variant of AtomicWriteBytes.
// All lookups, temporary files and replacements stay relative to root, even
// if a repository entry is concurrently replaced by a symlink. Relative
// symlinks within root are preserved; absolute or escaping links fail closed.
// Existing permissions are preserved, and read-only/non-regular files are
// not replaced. perm applies only to new files. The parent must already exist.
func AtomicWriteBytesInRoot(root *os.Root, name string, data []byte, perm os.FileMode) error {
	// Resolve leaf links through root, not through the process filesystem.
	// Do not Clean the concatenation: "link/../file" must retain its filesystem
	// semantics. Every subsequent operation is also rooted, not check-then-use
	// of an absolute pathname.
	for links := 0; ; links++ {
		if links >= 255 {
			return fmt.Errorf("too many symlinks in %q", name)
		}
		info, err := root.Lstat(name)
		if errors.Is(err, fs.ErrNotExist) {
			break
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink == 0 {
			break
		}
		target, err := root.Readlink(name)
		if err != nil {
			return err
		}
		if filepath.IsAbs(target) || filepath.VolumeName(target) != "" {
			return fmt.Errorf("refusing absolute symlink %q", name)
		}
		dir, _ := filepath.Split(name)
		name = dir + target
	}

	dir, base := filepath.Split(name)
	if dir == "" {
		dir = "."
	}
	parent, err := root.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer parent.Close()

	info, err := parent.Lstat(base)
	if err == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm()&0200 == 0 {
			return fmt.Errorf("refusing to replace non-regular or read-only file %q", name)
		}
		perm = info.Mode().Perm()
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}

	tmpName := ".tmp-" + rand.Text()
	tmp, err := parent.OpenFile(tmpName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	defer func() { _ = parent.Remove(tmpName) }()
	defer tmp.Close()
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	// Rename does not follow the destination leaf if it became a symlink.
	if err := parent.Rename(tmpName, base); err != nil {
		return fmt.Errorf("replace %q: %w", name, err)
	}
	if d, err := parent.Open("."); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}
