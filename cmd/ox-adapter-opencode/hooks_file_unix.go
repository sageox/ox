//go:build unix

package main

import (
	"errors"
	"fmt"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func openPluginFile(path string, flag int) (*os.File, error) {
	// The entry can change between Lstat and open. Reject symlinks and
	// avoid blocking on a replaced entry before checking its type.
	return os.OpenFile(path, flag|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
}

func openRootPluginFile(root *os.Root, name string, flag int) (*os.File, error) {
	return root.OpenFile(name, flag|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
}

func preservePluginOwner(file *os.File, existing os.FileInfo) error {
	current, err := file.Stat()
	if err != nil {
		return err
	}
	want := existing.Sys().(*syscall.Stat_t)
	have := current.Sys().(*syscall.Stat_t)
	if want.Uid == have.Uid && want.Gid == have.Gid {
		return nil
	}
	if err := file.Chown(int(want.Uid), int(want.Gid)); err != nil {
		if errors.Is(err, os.ErrPermission) {
			return fmt.Errorf("OpenCode plugin is owned by uid %d gid %d; rerun as that user to repair it: %w", want.Uid, want.Gid, err)
		}
		return err
	}
	return nil
}
