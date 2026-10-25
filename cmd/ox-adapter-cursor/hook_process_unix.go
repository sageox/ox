//go:build !windows

package main

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// Killing the group also bounds the prime subprocess spawned by ox.
func configureCursorHookProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
			if errors.Is(err, syscall.ESRCH) {
				return os.ErrProcessDone
			}
			return err
		}
		return nil
	}
}

// A prime timeout may let the host exit before its descendants. The adapter
// owns this process group until the entire hook invocation has returned.
func cleanupCursorHookProcess(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
