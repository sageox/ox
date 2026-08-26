//go:build windows

package hooks

import (
	"os/exec"
	"strconv"
	"syscall"

	"golang.org/x/sys/windows"
)

// newHookCmd builds the exec.Cmd used to run a hook's command. Windows has
// no POSIX shell; route through cmd.exe the way "sh -c" is used on Unix.
func newHookCmd(command string) *exec.Cmd {
	return exec.Command("cmd.exe", "/c", command)
}

// setProcessGroup starts the hook in a new process group ID (equal to its
// own PID) so terminateProcessGroup can target the group with a console
// control event, and so a Ctrl+Break delivered to ox's own console doesn't
// also land on the hook.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP}
}

// terminateProcessGroup asks the hook's process group to exit by delivering
// CTRL_BREAK_EVENT — the Windows analog of SIGTERM for a process group.
//
// Caveat for reviewers: CTRL_BREAK_EVENT only reaches console-aware
// processes that installed a handler for it; cmd.exe and most simple child
// processes ignore it and keep running. It is NOT a reliable full-tree
// terminator on its own (unlike SIGTERM to a Unix process group, which the
// kernel delivers unconditionally). killProcessGroup below is the backstop
// that actually reaps a stuck hook tree on Windows.
func terminateProcessGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	_ = windows.GenerateConsoleCtrlEvent(windows.CTRL_BREAK_EVENT, uint32(cmd.Process.Pid))
}

// killProcessGroup force-kills the entire process tree rooted at the hook's
// PID via taskkill /T /F. Windows has no SIGKILL-to-process-group equivalent
// in the stdlib syscall/exec surface (a full descendant-tree kill without a
// job object needs either CGo or golang.org/x/sys/windows job-object APIs,
// neither of which this package otherwise needs), so shelling out to the
// built-in taskkill utility is the pragmatic, dependency-free choice here.
func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	_ = exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run()
}
