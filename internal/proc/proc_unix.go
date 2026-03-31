//go:build darwin || linux || freebsd

package proc

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// parentPID returns the parent PID of the given PID using ps.
func parentPID(pid int) (int, error) {
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "ppid=").Output()
	if err != nil {
		return 0, fmt.Errorf("ps ppid for %d: %w", pid, err)
	}
	ppid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0, fmt.Errorf("parse ppid for %d: %w", pid, err)
	}
	return ppid, nil
}

// processName returns the process name for the given PID using ps.
func processName(pid int) string {
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "comm=").Output()
	if err != nil {
		return ""
	}
	// ps returns the full path on some systems; use the base name
	name := strings.TrimSpace(string(out))
	if idx := strings.LastIndex(name, "/"); idx >= 0 {
		name = name[idx+1:]
	}
	return name
}

// isAliveProc checks if a process is alive using kill(pid, 0).
func isAliveProc(proc *os.Process) bool {
	err := proc.Signal(syscall.Signal(0))
	return err == nil
}
