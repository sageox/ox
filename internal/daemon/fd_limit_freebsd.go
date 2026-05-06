//go:build freebsd

package daemon

import "syscall"

// CurrentProcessFDLimit returns the soft RLIMIT_NOFILE for the current
// process, or 0 if it cannot be determined.
//
// On freebsd syscall.Rlimit.Cur is int64 (vs uint64 on linux/darwin), so
// the conversion is required here. Kept in its own file so the unconvert
// linter doesn't flag it on the platforms where the cast is a no-op.
// Negative values can't occur for RLIMIT_NOFILE in practice.
func CurrentProcessFDLimit() uint64 {
	var rlim syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &rlim); err != nil {
		return 0
	}
	return uint64(rlim.Cur)
}
