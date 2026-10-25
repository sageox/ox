//go:build !windows

package testguard

import "syscall"

// maxFDProbe is the upper bound for FD probing. 8192 covers typical ulimit -n
// defaults and is fast enough (single syscall per FD, ~1ms total).
const maxFDProbe = 8192

// countOpenFDs counts open file descriptors by probing with fcntl(F_GETFD).
// Returns -1 on unsupported platforms.
func countOpenFDs() int {
	count := 0
	for fd := 0; fd < maxFDProbe; fd++ {
		_, _, err := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), syscall.F_GETFD, 0)
		if err == 0 {
			count++
		}
	}
	return count
}
