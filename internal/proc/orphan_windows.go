//go:build windows

package proc

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
)

// startOrphaned starts cmd as a plain child on Windows, as before
// StartOrphaned existed: whether an agent there kills a hook's whole tree is
// unverified. Its exit status is recorded from a goroutine.
func startOrphaned(cmd *exec.Cmd, statusPath string) error {
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() {
		code := 0
		if err := cmd.Wait(); err != nil {
			code = 1
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				code = exitErr.ExitCode()
			}
		}
		tmp := statusPath + ".tmp"
		if os.WriteFile(tmp, []byte(strconv.Itoa(code)+"\n"), 0o600) == nil {
			_ = os.Rename(tmp, statusPath)
		}
	}()
	return nil
}
