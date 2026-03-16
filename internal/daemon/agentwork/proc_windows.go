//go:build windows

package agentwork

import (
	"os/exec"
)

// setProcAttr is a no-op on Windows; process group isolation
// is not supported via SysProcAttr.Setpgid.
func setProcAttr(cmd *exec.Cmd) {}

// killProcessGroup kills the process directly on Windows.
func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
