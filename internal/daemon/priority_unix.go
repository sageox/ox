//go:build unix

package daemon

import (
	"errors"
	"os"
	"runtime"
	"strconv"
	"syscall"
)

// daemonNiceness is the nice value the daemon runs at. 10 is the conventional
// "background job" value: the scheduler still gives the daemon every idle cycle
// but yields to a coworker's editor, build, or agent whenever they contend.
const daemonNiceness = 10

// lowerDaemonPriority makes the daemon a polite background process.
//
// Why: indexing, ledger reconciliation, and git all run in this process and in
// the git children it spawns. When one of them wedges it burns a full core, and
// at normal priority that competes with whatever the coworker is doing in the
// foreground. At nice 10 a runaway still wastes battery but no longer makes the
// machine feel slow. Child processes inherit the niceness of the thread that
// forks them, so git subprocesses are covered too.
//
// Linux nice values are per thread, not per process: setpriority(PRIO_PROCESS, 0)
// only touches the calling thread, and the Go runtime has already started
// several others by the time main runs. Each existing thread is lowered
// explicitly; threads created later inherit from their creator. A thread that
// is spawned during the sweep may keep the old value, which only means a little
// of that thread's work is not deprioritized.
//
// Best effort: lowering priority needs no privileges, but a sandbox may still
// refuse it, and the daemon is fully functional either way. The error is
// returned so the caller can log it at debug level.
func lowerDaemonPriority() error {
	var niceErr error
	if runtime.GOOS == "linux" {
		niceErr = lowerPriorityAllThreads(daemonNiceness)
	} else {
		niceErr = syscall.Setpriority(syscall.PRIO_PROCESS, 0, daemonNiceness)
	}
	return errors.Join(niceErr, enterBackgroundBand())
}

// lowerPriorityAllThreads applies nice to every thread listed in
// /proc/self/task, falling back to the calling thread when procfs is not
// available.
func lowerPriorityAllThreads(nice int) error {
	entries, err := os.ReadDir("/proc/self/task")
	if err != nil {
		return syscall.Setpriority(syscall.PRIO_PROCESS, 0, nice)
	}

	var firstErr error
	for _, entry := range entries {
		tid, convErr := strconv.Atoi(entry.Name())
		if convErr != nil {
			continue
		}
		err := syscall.Setpriority(syscall.PRIO_PROCESS, tid, nice)
		// ESRCH: the thread exited between the listing and the call.
		if err != nil && !errors.Is(err, syscall.ESRCH) && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
