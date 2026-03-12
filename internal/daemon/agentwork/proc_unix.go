//go:build !windows

package agentwork

import (
	"os/exec"
	"syscall"
)

// setProcAttr configures the command to run in its own process group
// so we can kill the entire group on cancellation.
func setProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcessGroup sends SIGTERM to the process group.
func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	}
}
