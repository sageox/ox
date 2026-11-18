//go:build windows

package main

import (
	"errors"
	"os"
	"os/exec"
)

// runStatuslineWrap on Windows runs the command without ox's row: holding a
// row needs a pseudo-terminal, which this build does not drive.
func runStatuslineWrap(argv []string) (int, error) {
	if len(argv) == 0 {
		return 2, errors.New("usage: ox statusline --wrap -- <command> [args]")
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	err := cmd.Run()
	if err == nil {
		return 0, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode(), nil
	}
	return 1, err
}
