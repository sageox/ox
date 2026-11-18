//go:build unix

package main

import (
	"os"

	"golang.org/x/sys/unix"
)

func openPluginFile(path string, flag int) (*os.File, error) {
	// The entry can change between Lstat and open. Reject symlinks and
	// avoid blocking on a replaced entry before checking its type.
	return os.OpenFile(path, flag|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
}
