package fileutil

import (
	"errors"
	"fmt"
	"os"
)

// EnsurePrivateDir creates path as a 0700 directory, or makes an existing one
// 0700. It refuses path itself being a symlink or a non-directory, so a link
// planted at a predictable path, such as one under /tmp, cannot redirect
// writes. It does not check path's parents, which MkdirAll follows.
func EnsurePrivateDir(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("not a real directory: %s", path)
	}
	return os.Chmod(path, 0o700)
}

// OpenPrivateLog opens path for appending, creating it with mode 0600 and
// tightening an existing file to 0600. Like EnsurePrivateDir, it refuses a
// symlink or any other non-regular file at path. The check precedes the open,
// and a hard link is not refused.
func OpenPrivateLog(path string) (*os.File, error) {
	info, err := os.Lstat(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err == nil && !info.Mode().IsRegular() {
		return nil, fmt.Errorf("not a regular file: %s", path)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}
