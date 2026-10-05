//go:build unix

package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const priorityHelperEnv = "OX_TEST_LOWER_PRIORITY_HELPER"

// threadNiceness returns the nice value of one thread (tid) of this process.
//
// Linux is read from /proc because the raw getpriority syscall returns 20-nice
// there while BSD/macOS return nice itself, and because Linux nice is
// per-thread — the very property lowerDaemonPriority has to deal with.
func threadNiceness(tid int) (int, error) {
	if runtime.GOOS != "linux" {
		return syscall.Getpriority(syscall.PRIO_PROCESS, 0)
	}
	data, err := os.ReadFile(fmt.Sprintf("/proc/self/task/%d/stat", tid))
	if err != nil {
		return 0, err
	}
	// "pid (comm) state ppid ...": comm may contain spaces, so split after the
	// last ')'. nice is field 19 overall, index 16 after "state".
	stat := string(data)
	fields := strings.Fields(stat[strings.LastIndex(stat, ")")+1:])
	if len(fields) < 17 {
		return 0, fmt.Errorf("short stat line: %q", stat)
	}
	return strconv.Atoi(fields[16])
}

// allThreadNiceness returns the nice value of every thread of this process on
// Linux, and just the calling thread's value elsewhere (where nice is
// process-wide).
func allThreadNiceness() ([]int, error) {
	if runtime.GOOS != "linux" {
		n, err := threadNiceness(0)
		return []int{n}, err
	}
	entries, err := os.ReadDir("/proc/self/task")
	if err != nil {
		return nil, err
	}
	var out []int
	for _, e := range entries {
		tid, convErr := strconv.Atoi(e.Name())
		if convErr != nil {
			continue
		}
		n, err := threadNiceness(tid)
		if err != nil {
			continue // thread exited since the listing
		}
		out = append(out, n)
	}
	return out, nil
}

// TestLowerDaemonPriorityHelper is the child half of
// TestLowerDaemonPriority_RaisesNiceness. It does nothing in a normal run: it
// lowers THIS process's priority, which cannot be undone without privileges, so
// it must only ever run in a throwaway subprocess.
func TestLowerDaemonPriorityHelper(t *testing.T) {
	if os.Getenv(priorityHelperEnv) != "1" {
		t.Skip("subprocess helper; run via TestLowerDaemonPriority_RaisesNiceness")
	}
	before, err := allThreadNiceness()
	require.NoError(t, err)
	callErr := lowerDaemonPriority()
	after, err := allThreadNiceness()
	require.NoError(t, err)
	fmt.Printf("RESULT before=%v after=%v err=%v\n", before, after, callErr)
}

// TestLowerDaemonPriority_RaisesNiceness proves the daemon actually runs at a
// lower priority after lowerDaemonPriority — in a subprocess, because the
// change is irreversible for an unprivileged process.
//
// Failure prevented: a wedged indexer or push loop pegging a core at normal
// priority and making the coworker's foreground work (editor, build, agent)
// crawl. On Linux the call must reach every thread, not just the caller:
// nice is per-thread there, and the Go runtime has already started many.
func TestLowerDaemonPriority_RaisesNiceness(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestLowerDaemonPriorityHelper$", "-test.v")
	cmd.Env = append(os.Environ(), priorityHelperEnv+"=1") // safe: re-execs this test binary
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "helper failed: %s", out)

	m := regexp.MustCompile(`RESULT before=\[([^\]]*)\] after=\[([^\]]*)\] err=(.*)`).FindStringSubmatch(string(out))
	require.NotNil(t, m, "helper printed no result: %s", out)

	parse := func(list string) []int {
		var nums []int
		for _, f := range strings.Fields(list) {
			n, convErr := strconv.Atoi(f)
			require.NoError(t, convErr)
			nums = append(nums, n)
		}
		return nums
	}
	before, after := parse(m[1]), parse(m[2])
	require.NotEmpty(t, before)
	require.NotEmpty(t, after)

	for _, n := range before {
		if n >= daemonNiceness {
			t.Skipf("test runner already runs at nice %d; cannot observe a change", n)
		}
	}

	assert.Equal(t, "<nil>", strings.TrimSpace(m[3]), "lowerDaemonPriority must succeed unprivileged")
	for _, n := range after {
		assert.GreaterOrEqual(t, n, daemonNiceness, "every thread must be at nice >= %d after lowerDaemonPriority (before=%v after=%v)", daemonNiceness, before, after)
	}
}
