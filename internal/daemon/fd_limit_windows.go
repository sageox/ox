//go:build windows

package daemon

// CurrentProcessFDLimit returns 0 on Windows. Windows does not expose a
// RLIMIT_NOFILE-equivalent through Go's syscall package, so we report
// "unknown" and the doctor FD-pressure check skips. The daemon's primary
// supported platforms are macOS and Linux; Windows is a build-only target.
func CurrentProcessFDLimit() uint64 {
	return 0
}
