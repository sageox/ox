//go:build windows

package automerge

import "os/exec"

// setProcAttr is a no-op on Windows; SysProcAttr.Setpgid is unsupported.
// Cancellation still works at the immediate child via exec.CommandContext.
func setProcAttr(cmd *exec.Cmd) {}
