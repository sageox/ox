package fileutil

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// CopyFile copies src to dst, preserving file mode.
// Uses Lstat to avoid following symlinks — a symlink is recreated as a symlink,
// never dereferenced. This prevents a rogue symlink (e.g., pointing to /etc/shadow)
// from copying host files into a checkout.
func CopyFile(src, dst string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}

	// recreate symlinks as symlinks, never dereference
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(src)
		if err != nil {
			return fmt.Errorf("readlink %s: %w", src, err)
		}
		return os.Symlink(target, dst)
	}

	// skip non-regular files (devices, sockets, etc.)
	if !info.Mode().IsRegular() {
		return nil
	}

	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	// Callers delete the source once a copy succeeds, so a write the
	// filesystem only reports on close must fail the copy.
	return out.Close()
}

// CopyDir recursively copies a directory tree from src to dst.
func CopyDir(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		relPath, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		dstPath := filepath.Join(dst, relPath)

		if d.IsDir() {
			return os.MkdirAll(dstPath, 0755)
		}

		return CopyFile(path, dstPath)
	})
}
