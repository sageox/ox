//go:build !unix

package main

import "os"

func openPluginFile(path string, flag int) (*os.File, error) {
	return os.OpenFile(path, flag, 0)
}

func openRootPluginFile(root *os.Root, name string, flag int) (*os.File, error) {
	return root.OpenFile(name, flag, 0)
}

func preservePluginOwner(*os.File, os.FileInfo) error {
	return nil
}
