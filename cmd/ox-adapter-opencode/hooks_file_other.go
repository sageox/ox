//go:build !unix

package main

import "os"

func openPluginForCheck(path string) (*os.File, error) {
	return os.Open(path)
}
