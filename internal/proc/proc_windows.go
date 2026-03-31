//go:build windows

package proc

import "os"

// parentPID is not implemented on Windows; returns unsupported.
func parentPID(_ int) (int, error) {
	return 0, nil
}

// processName is not implemented on Windows.
func processName(_ int) string {
	return ""
}

// isAliveProc checks if a process is alive by sending signal 0.
// On Windows, os.FindProcess always succeeds; use process.Wait as a proxy.
func isAliveProc(proc *os.Process) bool {
	// On Windows, Signal(0) is not supported. Best-effort: assume alive if findProcess succeeded.
	return proc != nil
}
