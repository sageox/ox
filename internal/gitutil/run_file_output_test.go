package gitutil

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunGitFileOutput(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	output, err := RunGitFileOutput(context.Background(), "", "version")
	require.NoError(t, err)
	assert.Contains(t, output, "git version")

	// the error carries git's own message, as RunGit's does: push retries
	// classify a failure by it (non-fast-forward, missing LFS objects, 403)
	repo := t.TempDir()
	_, err = RunGit(context.Background(), "", "init", "-q", repo)
	require.NoError(t, err)
	output, err = RunGitFileOutput(context.Background(), repo, "rev-parse", "--verify", "no-such-ref")
	require.Error(t, err)
	assert.Contains(t, output, "fatal")
	assert.Contains(t, err.Error(), "git rev-parse")
	assert.Contains(t, err.Error(), output)

	_, err = RunGitFileOutput(context.Background(), "")
	require.Error(t, err)
}

func TestRunWithFileOutput_CapturesBothStreamsInOrder(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a shell script stands in for git")
	}
	out, err := runWithFileOutput(exec.Command("/bin/sh", "-c", "echo one; echo two >&2; echo three; exit 2"))
	var exitErr *exec.ExitError
	require.True(t, errors.As(err, &exitErr), "the command's own exit is the error: %v", err)
	assert.Equal(t, 2, exitErr.ExitCode())
	assert.Equal(t, "one\ntwo\nthree\n", string(out))
}

// TestRunWithFileOutput_OutlivesItsParent is the push half of sageox/ox#1936:
// the stop running a session push was killed at Claude Code's SessionEnd, and
// the orphaned push died on its next write to the dead stop's pipe (SIGPIPE),
// after authenticating and before sending anything. Here the process running
// the command is killed mid-command; the command writes again afterwards and
// must still finish.
func TestRunWithFileOutput_OutlivesItsParent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a shell script stands in for git")
	}
	dir := t.TempDir()
	started, release, done := filepath.Join(dir, "started"), filepath.Join(dir, "release"), filepath.Join(dir, "done")
	parent := exec.Command(os.Args[0], "-test.run=^TestHelperRunWithFileOutputParent$")
	parent.Env = append(os.Environ(), "OX_TEST_FILE_OUTPUT_DIR="+dir) // safe: the helper runs only a shell script, never ox or git
	require.NoError(t, parent.Start())
	t.Cleanup(func() { _ = os.WriteFile(release, nil, 0o600) })

	require.Eventually(t, func() bool { _, err := os.Stat(started); return err == nil }, 10*time.Second, 10*time.Millisecond)
	require.NoError(t, parent.Process.Kill())
	_ = parent.Wait()
	// the command writes again only once its parent is gone
	require.NoError(t, os.WriteFile(release, nil, 0o600))

	require.Eventually(t, func() bool { _, err := os.Stat(done); return err == nil }, 10*time.Second, 20*time.Millisecond,
		"the command must finish after the process reading its output died")
}

// TestHelperRunWithFileOutputParent is the pusher that gets killed.
func TestHelperRunWithFileOutputParent(t *testing.T) {
	dir := os.Getenv("OX_TEST_FILE_OUTPUT_DIR")
	if dir == "" {
		t.Skip("helper process for TestRunWithFileOutput_OutlivesItsParent")
	}
	_, _ = runWithFileOutput(exec.Command("/bin/sh", "-c",
		`echo authenticated; touch "$1"; i=0; while [ ! -e "$2" ] && [ $i -lt 600 ]; do sleep 0.05; i=$((i+1)); done; echo sending pack; touch "$3"`,
		"sh", filepath.Join(dir, "started"), filepath.Join(dir, "release"), filepath.Join(dir, "done")))
}
