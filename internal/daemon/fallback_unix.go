//go:build !windows

package daemon

import (
	"fmt"
	"os"
	"strings"
	"syscall"
)

// sigTERM is the signal to send for graceful termination.
const sigTERM = syscall.SIGTERM

// signalProcess sends a signal to the given PID.
// Used by KillStaleDaemon to check liveness (signal 0) and terminate (SIGTERM).
func signalProcess(pid int, sig syscall.Signal) error {
	return syscall.Kill(pid, sig)
}

// isOxDaemonProcess checks if the given PID is an ox daemon process by reading
// /proc/<pid>/cmdline. Returns false if the process cannot be identified as an
// ox daemon (e.g., PID was reused by an unrelated process).
func isOxDaemonProcess(pid int) bool {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return false // can't read cmdline (process gone, or not Linux)
	}
	// cmdline is NUL-separated; join for simple substring match
	cmdline := strings.ReplaceAll(string(data), "\x00", " ")
	return strings.Contains(cmdline, "ox") && strings.Contains(cmdline, "daemon")
}
