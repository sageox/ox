//go:build !unix

package main

import "os"

func openPluginFile(path string, flag int) (*os.File, error) {
	return os.OpenFile(path, flag, 0)
}
