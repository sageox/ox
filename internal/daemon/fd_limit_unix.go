//go:build !windows

package daemon

import "syscall"

// CurrentProcessFDLimit returns the soft RLIMIT_NOFILE for the current
// process, or 0 if it cannot be determined. Used to compute FD-pressure
// percentage rather than hard-coding thresholds: a daemon at 4096 open FDs
// is fine on a host with a 1M limit, alarming on a host with a 4500 limit.
//
// Unix-only because syscall.Getrlimit / syscall.Rlimit / syscall.RLIMIT_NOFILE
// are guarded by `//go:build unix` upstream and don't exist for GOOS=windows.
// See fd_limit_windows.go for the Windows stub.
//
// rlim.Cur is uint64 on linux and darwin but int64 on freebsd; cast to keep
// the cross-GOOS return type stable. Negative values can't occur for
// RLIMIT_NOFILE in practice.
func CurrentProcessFDLimit() uint64 {
	var rlim syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &rlim); err != nil {
		return 0
	}
	return uint64(rlim.Cur)
}
