//go:build !windows

package adapters

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// configureOneShotCommand isolates an adapter in its own process group so a
// timeout or output-limit cancellation terminates descendants as well as the
// adapter process itself. Adapter wrappers are commonly shell scripts, and a
// child inheriting their output pipes would otherwise outlive the canceled
// command.
func configureOneShotCommand(cmd *exec.Cmd) {
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

func runOneShotCommand(cmd *exec.Cmd) error {
	configureOneShotCommand(cmd)
	return cmd.Run()
}
