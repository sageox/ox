//go:build windows

package daemon

import (
	"fmt"
	"os"
	"syscall"
)

// sigTERM is the signal to send for graceful termination.
// Windows doesn't have SIGTERM; we use 0xF (15) as a placeholder since
// signalProcess on Windows ignores the signal value and calls proc.Kill().
const sigTERM = syscall.Signal(0xF)

// sigKILL mirrors sigTERM on Windows: signalProcess ignores the signal value
// for anything non-zero and calls proc.Kill(), which is already ungraceful.
const sigKILL = syscall.Signal(0x9)

// isOxDaemonProcess checks if the given PID is an ox daemon process.
// On Windows, /proc is not available; we optimistically assume it is.
// TODO: use Windows API (CreateToolhelp32Snapshot) for process identity checks.
func isOxDaemonProcess(pid int) bool {
	return true
}

// signalProcess sends a signal to the given PID.
// On Windows, only signal 0 (liveness check) and termination are supported.
//
// TODO: os.FindProcess always succeeds on Windows even for non-existent PIDs,
// and proc.Signal(0) is unreliable for liveness checks. Consider using the
// Windows OpenProcess API for accurate liveness detection.
func signalProcess(pid int, sig syscall.Signal) error {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	if sig == 0 {
		// os.FindProcess succeeds if process exists; Signal(0) is unsupported
		// on Windows (returns EWINDOWS). FindProcess success = alive.
		return nil
	}
	// Windows doesn't support SIGTERM; kill the process directly.
	if err := proc.Kill(); err != nil {
		return fmt.Errorf("kill process %d: %w", pid, err)
	}
	return nil
}
