package proc

import "golang.org/x/sys/unix"

// startTime reads the process's start time from the kernel, in microseconds
// since the Unix epoch.
func startTime(pid int) (int64, bool) {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || kp.Proc.P_pid != int32(pid) {
		return 0, false
	}
	t := kp.Proc.P_starttime
	return t.Sec*1_000_000 + int64(t.Usec), true
}
