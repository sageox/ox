//go:build !windows

package hooks

import (
	"os/exec"
	"syscall"
)

// newHookCmd builds the exec.Cmd used to run a hook's shell command.
func newHookCmd(command string) *exec.Cmd {
	return exec.Command("sh", "-c", command)
}

// setProcessGroup configures cmd to become the leader of a new process
// group (its pgid equals its pid once started). This lets
// terminateProcessGroup / killProcessGroup signal the whole tree — including
// any children the hook itself forks — instead of only the immediate child.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// terminateProcessGroup sends SIGTERM to the hook's process group so forked
// children are also asked to exit gracefully.
func terminateProcessGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
}

// killProcessGroup sends SIGKILL to the hook's process group.
func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
