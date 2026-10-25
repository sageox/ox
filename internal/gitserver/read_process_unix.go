//go:build !windows

package gitserver

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// Matches the one-shot adapter process policy without importing its package:
// cancel the owned Git tree, including remote-https and lazy-fetch children.
func configureReadProcess(cmd *exec.Cmd) {
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
