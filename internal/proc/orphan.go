package proc

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// StartOrphaned starts cmd so that, by the time it returns, cmd is no longer a
// descendant of this process, and records cmd's exit status in statusPath when
// cmd ends. The caller reads that with OrphanedExitStatus: it cannot wait for a
// process that is not its child.
//
// Detach alone is not enough for a hook. Claude Code ends a SessionEnd hook by
// signaling every process in the hook's tree, found by parent PID, not by
// process group or session (sageox/ox#1936). A setsid'd child is still in that
// tree, so the upload it was running died with the hook. A grandchild whose
// parent has exited is re-parented away from the tree before the kill.
//
// cmd's Stdout and Stderr must be nil or *os.File. A pipe would tie cmd to this
// process again: its writes fail with SIGPIPE once this process is gone. Path
// should be absolute, and SysProcAttr and ExtraFiles are not carried over.
func StartOrphaned(cmd *exec.Cmd, statusPath string) error {
	if cmd.Process != nil {
		return fmt.Errorf("start orphaned %s: already started", cmd.Path)
	}
	if cmd.Err != nil {
		return cmd.Err
	}
	for _, w := range []any{cmd.Stdout, cmd.Stderr} {
		if _, ok := w.(*os.File); w != nil && !ok {
			return fmt.Errorf("start orphaned %s: output must be a file, never a pipe", cmd.Path)
		}
	}
	// exec.Command does not check a path it was given; Start would have.
	if _, err := exec.LookPath(cmd.Path); err != nil {
		return err
	}
	return startOrphaned(cmd, statusPath)
}

// OrphanedExitStatus reads the exit status StartOrphaned recorded at
// statusPath. done is false while the command still runs. A command ended by a
// signal reports 128 plus the signal number, as a shell does.
func OrphanedExitStatus(statusPath string) (code int, done bool, err error) {
	data, err := os.ReadFile(statusPath)
	if os.IsNotExist(err) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	code, err = strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, false, fmt.Errorf("read exit status %s: %w", statusPath, err)
	}
	return code, true, nil
}
