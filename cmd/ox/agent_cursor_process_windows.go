package main

import "os/exec"

// Windows is compile-compatible; native Cursor qualification is macOS only.
func configureCursorPrimeProcess(cmd *exec.Cmd) {}
