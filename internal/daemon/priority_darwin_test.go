package daemon

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const backgroundBandHelperEnv = "OX_TEST_BACKGROUND_BAND_HELPER"

// darwinBackgroundPriority is the scheduler priority ps reports for a process
// in the background band (normal user processes sit at 31).
const darwinBackgroundPriority = 4

// TestBackgroundBandHelper is the child half of
// TestLowerDaemonPriority_EntersBackgroundBand: it lowers THIS process, which an
// unprivileged process cannot undo, so it only runs in a throwaway subprocess.
// It reports its own priority and that of a child it spawns, the way the
// daemon spawns git.
func TestBackgroundBandHelper(t *testing.T) {
	if os.Getenv(backgroundBandHelperEnv) != "1" {
		t.Skip("subprocess helper; run via TestLowerDaemonPriority_EntersBackgroundBand")
	}
	require.NoError(t, lowerDaemonPriority())
	self, err := exec.Command("ps", "-o", "pri=", "-p", strconv.Itoa(os.Getpid())).Output()
	require.NoError(t, err)
	child, err := exec.Command("sh", "-c", "ps -o pri= -p $$").Output()
	require.NoError(t, err)
	t.Logf("RESULT self=%s child=%s", strings.TrimSpace(string(self)), strings.TrimSpace(string(child)))
}

// TestLowerDaemonPriority_EntersBackgroundBand proves the daemon and the git it
// spawns leave the performance cores on macOS. Failure prevented: a runaway
// index or push loop at nice 10 still ran on the performance cores and drove
// the machine into thermal shutdown.
func TestLowerDaemonPriority_EntersBackgroundBand(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestBackgroundBandHelper$", "-test.v")
	cmd.Env = append(os.Environ(), backgroundBandHelperEnv+"=1") // safe: re-execs this test binary
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "helper failed: %s", out)

	_, result, found := strings.Cut(string(out), "RESULT ")
	require.True(t, found, "helper printed no result: %s", out)
	fields := strings.Fields(result)
	require.GreaterOrEqual(t, len(fields), 2, "malformed result: %s", result)
	require.Equal(t, "self="+strconv.Itoa(darwinBackgroundPriority), fields[0], "daemon must be in the background band")
	require.Equal(t, "child="+strconv.Itoa(darwinBackgroundPriority), fields[1], "spawned git must inherit the background band")
}
