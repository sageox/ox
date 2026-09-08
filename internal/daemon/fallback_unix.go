//go:build !windows

package daemon

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// sigTERM is the signal to send for graceful termination.
const sigTERM = syscall.SIGTERM

// sigKILL is the signal to send when SIGTERM was ignored.
const sigKILL = syscall.SIGKILL

// signalProcess sends a signal to the given PID.
// Used by KillStaleDaemon to check liveness (signal 0) and terminate (SIGTERM).
func signalProcess(pid int, sig syscall.Signal) error {
	return syscall.Kill(pid, sig)
}

// isOxDaemonProcess checks if the given PID is an ox daemon process by reading
// its command line. Returns false if the process cannot be identified as an
// ox daemon (e.g., PID was reused by an unrelated process).
func isOxDaemonProcess(pid int) bool {
	cmdline, ok := processCmdline(pid)
	if !ok {
		return false
	}
	return strings.Contains(cmdline, "ox") && strings.Contains(cmdline, "daemon")
}

// processCmdline returns the full command line of pid.
//
// Linux exposes it as NUL-separated bytes in /proc/<pid>/cmdline. macOS and the
// BSDs have no /proc, so fall back to ps(1). Without that fallback
// isOxDaemonProcess always returned false off Linux, and its caller
// (KillStaleDaemon) responded by unregistering the entry and deleting the pid
// file and socket while leaving the process ALIVE and now untracked — so every
// stale-daemon cleanup on macOS leaked an orphan, and the SIGTERM escalation
// below it was unreachable dead code.
func processCmdline(pid int) (string, bool) {
	if cmdline, ok := procCmdline(pid); ok {
		return cmdline, true
	}
	return psCmdline(pid)
}

// procCmdline reads /proc/<pid>/cmdline. Present on Linux, absent everywhere
// else — kept separate from psCmdline so each strategy is directly testable on
// a platform where the other one is the live path.
func procCmdline(pid int) (string, bool) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return "", false
	}
	// cmdline is NUL-separated; join for simple substring match
	return strings.ReplaceAll(string(data), "\x00", " "), true
}

// psCmdline asks ps(1) for the process arguments. Bounded: a wedged process
// table must not stall the daemon-start path.
func psCmdline(pid int) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ps", "-p", strconv.Itoa(pid), "-o", "args=").Output()
	if err != nil {
		return "", false
	}
	cmdline := strings.TrimSpace(string(out))
	if cmdline == "" {
		return "", false
	}
	return cmdline, true
}
