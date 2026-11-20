package telemetry

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

var errUnsafeQueue = errors.New("telemetry queue is not a private regular file in a real directory")

// openQueueFile never truncates at open time. Directory and file identities are
// checked against pinned handles before reading or mutating anything, so a
// symlink substitution between Lstat and Open cannot redirect queue I/O.
func openQueueFile(path string, flag int) (*os.File, error) {
	if !filepath.IsAbs(path) || flag&os.O_TRUNC != 0 || runtime.GOOS == "js" || runtime.GOOS == "plan9" {
		return nil, errUnsafeQueue
	}
	parent, err := openQueueDir(filepath.Dir(path), flag&os.O_CREATE != 0)
	if err != nil {
		return nil, err
	}
	defer parent.Close()

	name := filepath.Base(path)
	before, err := parent.Lstat(name)
	if errors.Is(err, os.ErrNotExist) && flag&os.O_CREATE != 0 {
		// Exclusive creation refuses even a dangling symlink installed after Lstat.
		flag |= os.O_EXCL
	} else if err != nil {
		return nil, err
	} else {
		if !before.Mode().IsRegular() {
			return nil, errUnsafeQueue
		}
		flag &^= os.O_CREATE
	}
	f, err := parent.OpenFile(name, flag, 0600)
	if err != nil {
		return nil, err
	}
	actual, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if !actual.Mode().IsRegular() || !queuePrivateMode(actual) ||
		(before != nil && !os.SameFile(before, actual)) {
		_ = f.Close()
		return nil, errUnsafeQueue
	}
	return f, nil
}

// Walk from the filesystem root rather than OpenRoot(path), which would follow
// symlinks in path. Each child is pinned and identity-checked before descending.
func openQueueDir(path string, create bool) (*os.Root, error) {
	volume := filepath.VolumeName(path) + string(filepath.Separator)
	root, err := os.OpenRoot(volume)
	if err != nil {
		return nil, err
	}
	for _, part := range strings.Split(strings.TrimPrefix(path, volume), string(filepath.Separator)) {
		if part == "" {
			continue
		}
		before, err := root.Lstat(part)
		if errors.Is(err, os.ErrNotExist) && create {
			if err := root.Mkdir(part, 0700); err != nil && !errors.Is(err, os.ErrExist) {
				_ = root.Close()
				return nil, err
			}
			before, err = root.Lstat(part)
		}
		if err != nil {
			_ = root.Close()
			return nil, err
		}
		if !before.IsDir() {
			_ = root.Close()
			return nil, errUnsafeQueue
		}
		child, err := root.OpenRoot(part)
		_ = root.Close()
		if err != nil {
			return nil, err
		}
		actual, err := child.Stat(".")
		if err != nil || !os.SameFile(before, actual) {
			_ = child.Close()
			return nil, errUnsafeQueue
		}
		root = child
	}
	info, err := root.Stat(".")
	if err != nil || !queuePrivateMode(info) {
		_ = root.Close()
		return nil, errUnsafeQueue
	}
	return root, nil
}

func queuePrivateMode(info os.FileInfo) bool {
	// Windows uses ACLs, not POSIX permission bits.
	return runtime.GOOS == "windows" || info.Mode().Perm()&0077 == 0
}
