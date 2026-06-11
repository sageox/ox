//go:build windows

package agentwork

import (
	"os/exec"
)

// setProcAttr is a no-op on Windows; process group isolation
// is not supported via SysProcAttr.Setpgid.
func setProcAttr(cmd *exec.Cmd) {}
