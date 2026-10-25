//go:build !windows

package gitutil

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// gitWaitDelay bounds how long Wait lingers after cancellation for the process
// group to exit and its output pipes to drain before Go force-kills the leader.
const gitWaitDelay = 2 * time.Second

// gitKillGrace is how long a SIGTERMed group gets to clean up (git removes its
// own lock files on SIGTERM) before SIGKILL.
const gitKillGrace = time.Second

// setProcessGroupKill runs cmd in its own process group and, on context
// cancellation, terminates the WHOLE group instead of only the leader.
//
// Why: `git pull --rebase` spawns `git rebase` as a child. exec.CommandContext
// kills only the pull; the rebase survives as an orphan (ppid 1) that keeps
// running, holds .git/index.lock, and makes the next cycle's `rebase --abort`
// fail with exit 128.
func setProcessGroupKill(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = gitWaitDelay
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		pgid := cmd.Process.Pid
		if err := syscall.Kill(-pgid, syscall.SIGTERM); err != nil {
			if errors.Is(err, syscall.ESRCH) {
				return os.ErrProcessDone
			}
			// group signal failed; kill the leader directly as a last resort
			return cmd.Process.Kill()
		}
		// SIGKILL fallback for anything that ignores or outlives SIGTERM.
		// ESRCH (group already gone) is the normal outcome and harmless.
		time.AfterFunc(gitKillGrace, func() { _ = syscall.Kill(-pgid, syscall.SIGKILL) })
		return nil
	}
}
