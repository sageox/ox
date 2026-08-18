//go:build windows

package proc

import (
	"os"

	"golang.org/x/sys/windows"
)

// stillActive is the exit code GetExitCodeProcess reports for a process that has
// not exited (STILL_ACTIVE in the Win32 headers, 259). x/sys/windows exposes the
// same value as STATUS_PENDING but not under the STILL_ACTIVE name, so spell it
// out rather than borrowing a constant that means something else.
const stillActive = 259

// parentPID is not implemented on Windows; returns unsupported.
func parentPID(_ int) (int, error) {
	return 0, nil
}

// processName is not implemented on Windows.
func processName(_ int) string {
	return ""
}

// isAliveProc reports whether a process is still running.
//
// This used to return `proc != nil`, which is ALWAYS true: os.FindProcess never
// fails on Windows, so IsAlive reported every PID — including long-dead ones —
// as alive. Callers that use liveness to decide whether to restart something
// (carts' dolt server) would therefore trust a dead record forever.
//
// Signal(0) is not an option either: Go's Windows os.Process.Signal supports only
// os.Kill and returns syscall.EWINDOWS for anything else. Open the process and
// ask for its exit code instead.
func isAliveProc(proc *os.Process) bool {
	if proc == nil {
		return false
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(proc.Pid))
	if err != nil {
		// Most often ERROR_INVALID_PARAMETER: the PID names nothing. A permission
		// failure also lands here; reporting "not alive" is the safe answer, since
		// a process we cannot query is one we cannot manage either.
		return false
	}
	defer windows.CloseHandle(h)

	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	return code == stillActive
}

// terminateProc kills the process. os.Interrupt is not deliverable on Windows —
// os.Process.Signal returns syscall.EWINDOWS for it — so attempting a graceful
// signal here would fail while reporting nothing useful to the caller.
func terminateProc(proc *os.Process) error {
	return proc.Kill()
}
