//go:build darwin || linux || freebsd

package proc

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// waitOrphaned polls statusPath the way a caller of StartOrphaned does.
func waitOrphaned(t *testing.T, statusPath string) int {
	t.Helper()
	var code int
	require.Eventually(t, func() bool {
		c, done, err := OrphanedExitStatus(statusPath)
		require.NoError(t, err)
		code = c
		return done
	}, 10*time.Second, 10*time.Millisecond, "the orphaned command must record its exit status")
	return code
}

// TestStartOrphaned_LeavesTheCallersProcessTree is the sageox/ox#1936
// mechanism: Claude Code kills a SessionEnd hook's whole tree by parent PID,
// so the upload the hook starts must not be in that tree once the hook has
// started it. A setsid'd child (Detach) still was, and died with the hook.
func TestStartOrphaned_LeavesTheCallersProcessTree(t *testing.T) {
	dir := t.TempDir()
	pidFile, release := filepath.Join(dir, "pid"), filepath.Join(dir, "release")
	cmd := exec.Command("/bin/sh", "-c",
		`echo $$ > "$1.tmp" && mv "$1.tmp" "$1"; while [ ! -e "$2" ]; do sleep 0.05; done`, "sh", pidFile, release)
	status := filepath.Join(dir, "status")
	require.NoError(t, StartOrphaned(cmd, status))
	t.Cleanup(func() { _ = os.WriteFile(release, nil, 0o600) })

	var pid int
	require.Eventually(t, func() bool {
		data, err := os.ReadFile(pidFile)
		if err != nil {
			return false
		}
		pid, err = strconv.Atoi(strings.TrimSpace(string(data)))
		return err == nil
	}, 10*time.Second, 10*time.Millisecond)

	// walk the command's ancestry: a tree kill that starts at this process
	// reaches exactly the processes that have it as an ancestor
	for p, hops := pid, 0; p > 1 && hops < 64; hops++ {
		parent, err := parentPID(p)
		require.NoError(t, err)
		require.NotEqual(t, os.Getpid(), parent, "the orphaned command must not descend from the process that started it")
		p = parent
	}

	require.NoError(t, os.WriteFile(release, nil, 0o600))
	assert.Equal(t, 0, waitOrphaned(t, status))
}

// TestStartOrphaned_RecordsHowTheCommandEnded: the caller cannot wait for an
// orphan, so the status file is how it learns the stop failed or was killed.
func TestStartOrphaned_RecordsHowTheCommandEnded(t *testing.T) {
	for _, tc := range []struct {
		name   string
		script string
		want   int
	}{
		{"success", "exit 0", 0},
		{"failure", "exit 3", 3},
		{"killed", "kill -9 $$", 128 + 9},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status := filepath.Join(t.TempDir(), "status")
			_, done, err := OrphanedExitStatus(status)
			require.NoError(t, err)
			require.False(t, done, "no status file: still running")
			require.NoError(t, StartOrphaned(exec.Command("/bin/sh", "-c", tc.script), status))
			assert.Equal(t, tc.want, waitOrphaned(t, status))
		})
	}
}

// TestStartOrphaned_KeepsDirectoryEnvironmentAndFiles: the orphan runs where
// and as the caller set it up, writing straight into the caller's files.
func TestStartOrphaned_KeepsDirectoryEnvironmentAndFiles(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	out, err := os.Create(filepath.Join(dir, "out"))
	require.NoError(t, err)
	defer out.Close()
	errFile, err := os.Create(filepath.Join(dir, "err"))
	require.NoError(t, err)
	defer errFile.Close()

	cmd := exec.Command("/bin/sh", "-c", `pwd -P; echo "$ORPHAN_PROBE"; echo said >&2`)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "ORPHAN_PROBE=inherited") // safe: the shell only echoes; it never runs ox
	cmd.Stdout, cmd.Stderr = out, errFile
	status := filepath.Join(dir, "status")
	require.NoError(t, StartOrphaned(cmd, status))
	require.Equal(t, 0, waitOrphaned(t, status))

	printed, err := os.ReadFile(out.Name())
	require.NoError(t, err)
	assert.Equal(t, dir+"\ninherited\n", string(printed))
	said, err := os.ReadFile(errFile.Name())
	require.NoError(t, err)
	assert.Equal(t, "said\n", string(said))
}

// TestStartOrphaned_RefusesWhatCannotBeOrphaned: a pipe would bind the command
// to this process again, and a missing binary must fail here, as Start does,
// not later as an exit status nobody may read.
func TestStartOrphaned_RefusesWhatCannotBeOrphaned(t *testing.T) {
	dir := t.TempDir()
	piped := exec.Command("/bin/sh", "-c", "true")
	piped.Stdout = &bytes.Buffer{}
	require.ErrorContains(t, StartOrphaned(piped, filepath.Join(dir, "a")), "never a pipe")

	require.Error(t, StartOrphaned(exec.Command(filepath.Join(dir, "missing-ox")), filepath.Join(dir, "b")))

	_, err := os.Stat(filepath.Join(dir, "a"))
	assert.True(t, os.IsNotExist(err), "nothing ran")
}
