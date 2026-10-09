//go:build darwin

package daemon

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const priorityBandHelperEnv = "OX_TEST_PRIORITY_BAND_HELPER"

// darwinBackgroundPriority is the scheduler priority ps reports for a process
// in macOS's background band (PRIO_DARWIN_BG). Normal user processes sit at 31
// and a nice'd process a little below that; 4 means the band.
const darwinBackgroundPriority = 4

// TestPriorityBandHelper is the child half of
// TestLowerDaemonPriority_StaysOutOfBackgroundBand: it lowers THIS process,
// which an unprivileged process cannot undo, so it only runs in a throwaway
// subprocess. It reports its own scheduler priority and that of a child it
// spawns, the way the daemon spawns git.
func TestPriorityBandHelper(t *testing.T) {
	if os.Getenv(priorityBandHelperEnv) != "1" {
		t.Skip("subprocess helper; run via TestLowerDaemonPriority_StaysOutOfBackgroundBand")
	}
	require.NoError(t, lowerDaemonPriority())
	self, err := exec.Command("ps", "-o", "pri=", "-p", strconv.Itoa(os.Getpid())).Output()
	require.NoError(t, err)
	child, err := exec.Command("sh", "-c", "ps -o pri= -p $$").Output()
	require.NoError(t, err)
	t.Logf("RESULT self=%s child=%s", strings.TrimSpace(string(self)), strings.TrimSpace(string(child)))
}

// TestLowerDaemonPriority_StaysOutOfBackgroundBand proves the daemon and the
// git it spawns are merely nice'd, never placed in the background band.
//
// Failure prevented: in the band (the QoS Spotlight runs in) the daemon and its
// git children were scheduled at priority 4 and got zero CPU whenever the
// machine was busy. A Ledger push then sat behind a `git status` that never
// ran, clients timed out and auto-started replacements that starved too
// (#1235). Nice alone keeps the daemon polite without ever starving it.
func TestLowerDaemonPriority_StaysOutOfBackgroundBand(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestPriorityBandHelper$", "-test.v")
	cmd.Env = append(os.Environ(), priorityBandHelperEnv+"=1") // safe: re-execs this test binary
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "helper failed: %s", out)

	_, result, found := strings.Cut(string(out), "RESULT ")
	require.True(t, found, "helper printed no result: %s", out)
	fields := strings.Fields(result)
	require.GreaterOrEqual(t, len(fields), 2, "malformed result: %s", result)
	band := strconv.Itoa(darwinBackgroundPriority)
	require.NotEqual(t, "self="+band, fields[0], "daemon must not enter the background band")
	require.NotEqual(t, "child="+band, fields[1], "spawned git must not inherit the background band")
}
