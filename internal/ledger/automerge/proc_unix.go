//go:build !windows

package automerge

import (
	"os/exec"
	"syscall"
)

// setProcAttr puts the spawned LLM binary in its own process group so a
// hung child (and any descendants it forked) can be killed on context
// cancel. Mirrors internal/daemon/agentwork/proc_unix.go — kept local
// to avoid a daemon-package import from this leaf utility.
func setProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}
