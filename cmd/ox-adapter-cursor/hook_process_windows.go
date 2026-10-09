//go:build windows

package main

import "os/exec"

// Windows lifecycle support is unqualified; CommandContext bounds the direct
// child and WaitDelay bounds inherited output pipes.
func configureCursorHookProcess(cmd *exec.Cmd) {}
