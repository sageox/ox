//go:build !windows

package daemon

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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
	return matchesOxDaemon(cmdline)
}

// oxDaemonExecutables are the argv[0] basenames a real ox daemon can have.
// Note the absence of "ox.test": a test binary must never be mistaken for a
// daemon, which is the whole premise of internal/selfexec.
var oxDaemonExecutables = map[string]bool{"ox": true, "ox.exe": true}

// matchesOxDaemon reports whether a command line belongs to an `ox daemon ...`
// process: argv[0] is literally the ox binary, and one of its arguments is the
// daemon subcommand.
//
// Exactness here is a safety property, not tidiness. This is the PID-reuse
// guard KillStaleDaemon consults immediately before it sends SIGTERM and then
// SIGKILL. The previous check —
// strings.Contains(cmdline, "ox") && strings.Contains(cmdline, "daemon") —
// also accepts `firefox --daemon`, `podman daemon`, `toxiproxy daemon`, and
// literally any command line under a path like /Users/rox/. It went unnoticed
// because it was dead code off Linux; restoring the ps(1) fallback and adding
// SIGKILL escalation makes it live and lethal at the same time.
//
// The tradeoff is deliberate and asymmetric: an ox binary the user renamed no
// longer matches, so KillStaleDaemon unregisters the entry and leaves the
// process running. Leaking one orphan a coworker can kill by hand beats
// SIGKILLing a process that was never ours. Every uncertain input — an
// unsplittable path containing spaces, a truncated argument list — fails in
// that same safe direction.
func matchesOxDaemon(cmdline string) bool {
	fields := strings.Fields(cmdline)
	if len(fields) < 2 {
		return false
	}
	if !oxDaemonExecutables[filepath.Base(fields[0])] {
		return false
	}
	return slices.Contains(fields[1:], "daemon")
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

// psCommand runs ps(1) for one pid. A seam, because ps exiting 0 with empty
// output is a real case (the process vanished mid-query) that cannot be staged
// with a live process.
//
// -ww: never truncate to terminal width. A clipped argument list can drop the
// "daemon" argument matchesOxDaemon requires, turning a real daemon into an
// unrecognized process, and an orphan leak.
var psCommand = func(ctx context.Context, pid int) ([]byte, error) {
	return exec.CommandContext(ctx, "ps", "-ww", "-p", strconv.Itoa(pid), "-o", "args=").Output()
}

// psCmdline asks ps(1) for the process arguments. Bounded: a wedged process
// table must not stall the daemon-start path.
func psCmdline(pid int) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := psCommand(ctx, pid)
	if err != nil {
		return "", false
	}
	cmdline := strings.TrimSpace(string(out))
	if cmdline == "" {
		return "", false
	}
	return cmdline, true
}
