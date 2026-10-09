//go:build !windows

package main

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

func configureCursorPrimeProcess(cmd *exec.Cmd) {
	// The adapter starts its hook in a dedicated process group. Keep prime
	// in that group so the outer deadline also kills a late-starting prime.
	// The bridge cleans up any remaining descendants when the hook exits.
	group := 0
	if syscall.Getpgrp() == os.Getpid() {
		group = os.Getpid()
	} else {
		// Direct host invocation has no owned group; isolate the child so
		// its timeout cannot signal the caller's shell or other tasks.
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		target := group
		if target == 0 {
			target = cmd.Process.Pid
		}
		// When the hook owns the group, this also terminates the hook.
		// The adapter remains outside it and emits the native fallback.
		err := syscall.Kill(-target, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
}
