package daemon

import "syscall"

// From <sys/resource.h>; not exported by syscall or x/sys/unix.
const (
	prioDarwinProcess = 4      // PRIO_DARWIN_PROCESS: "who" is a pid (0 = self)
	prioDarwinBG      = 0x1000 // PRIO_DARWIN_BG: enter the background band
)

// enterBackgroundBand puts the daemon in macOS's background band, the QoS that
// Spotlight and Time Machine run in: on Apple silicon its threads are kept on
// the efficiency cores, and its disk and network I/O are throttled behind the
// coworker's. Nice alone only reorders the run queue; a runaway at nice 10 still
// heats the performance cores, which caused thermal shutdowns. git children
// inherit the band.
func enterBackgroundBand() error {
	return syscall.Setpriority(prioDarwinProcess, 0, prioDarwinBG)
}
