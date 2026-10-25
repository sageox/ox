//go:build windows

package gitutil

import "os/exec"

// setProcessGroupKill is a no-op on Windows; Setpgid is not available.
func setProcessGroupKill(cmd *exec.Cmd) {}
