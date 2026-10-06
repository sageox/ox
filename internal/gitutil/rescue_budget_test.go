package gitutil

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// slowGitOnPath puts a `git` wrapper first on PATH that sleeps before the
// named subcommands, simulating a host so loaded that local git work crawls.
func slowGitOnPath(t *testing.T, revListDelay, abortDelay time.Duration) {
	t.Helper()
	realGit, err := exec.LookPath("git")
	require.NoError(t, err)

	binDir := t.TempDir()
	script := "#!/bin/sh\n" +
		"case \"$*\" in\n" +
		"  *'rev-list --count HEAD --not'*) sleep " + secs(revListDelay) + " ;;\n" +
		"  *'rebase --abort'*) sleep " + secs(abortDelay) + " ;;\n" +
		"esac\n" +
		"exec " + realGit + " \"$@\"\n"
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "git"), []byte(script), 0o755))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func secs(d time.Duration) string {
	return strings.TrimSuffix(d.Round(time.Second).String(), "s")
}

// A slow stranded-commit count and a slow `git rebase --abort` must not leave
// the repo mid-rebase: a killed abort is what turned one timed-out pull into a
// wedge storm (#1195).
func TestRescueIfNeededThenAbort_SlowGitStillClearsWedge(t *testing.T) {
	if testing.Short() {
		t.Skip("short: sleeps to simulate a loaded host")
	}
	dir, _ := makeAbortableStrandedWedge(t)
	slowGitOnPath(t, 6*time.Second, 11*time.Second)

	_, err := RescueIfNeededThenAbort(context.Background(), dir, "test", quietLogger())

	require.NoError(t, err)
	assert.False(t, IsRebaseInProgress(dir), "abort must run to completion, not be killed mid-flight")
}

// Retrying recovery on a wedge that persists must not pile up rescue
// branches: once the first rescue branch anchors HEAD, nothing is stranded, so
// the retry creates no second one (#1195).
func TestRescueIfNeededThenAbort_RetryCreatesNoSecondRescueBranch(t *testing.T) {
	if testing.Short() {
		t.Skip("short: spawns git subprocesses")
	}
	dir := makeStrandedWedge(t, 2)

	first, err := RescueIfNeededThenAbort(context.Background(), dir, "test", quietLogger())
	require.Error(t, err, "detached zombie stays loud so the wedge persists for the retry")
	_, err = RescueIfNeededThenAbort(context.Background(), dir, "test", quietLogger())
	require.Error(t, err)

	assert.Equal(t, first, gitIn(t, dir, "branch", "--list", rescueBranchPrefix+"*", "--format=%(refname:short)"))
}
