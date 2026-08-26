//go:build windows

package testguard

// countOpenFDs is not implemented on Windows: there is no fcntl(F_GETFD)
// equivalent reachable from the stdlib syscall package, and this helper is a
// best-effort test diagnostic, not something worth a CGo/handle-enumeration
// dependency to support. Callers already treat a negative return as
// "unsupported platform" and skip (see RequireNoFDLeakWithDelta and the
// existing fdleak_test.go skip-on-negative pattern), so this preserves the
// original behavior of the file when it had no build constraint at all.
func countOpenFDs() int {
	return -1
}
