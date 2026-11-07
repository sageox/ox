//go:build !windows

package gitutil

import (
	"bufio"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// pidExists reports whether pid still exists.
func pidExists(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || !errors.Is(err, syscall.ESRCH)
}

func waitForPidFile(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(path); err == nil {
			if pid, convErr := strconv.Atoi(strings.TrimSpace(string(data))); convErr == nil {
				return pid
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("pid file %s never appeared", path)
	return 0
}

// Cancel must be safe when there is nothing left to kill: the process never
// started, or its whole group already exited.
func TestSetProcessGroupKillCancelWithNothingToKill(t *testing.T) {
	notStarted := commandContext(context.Background(), "true")
	require.ErrorIs(t, notStarted.Cancel(), os.ErrProcessDone)

	finished := commandContext(context.Background(), "true")
	require.NoError(t, finished.Run())
	require.ErrorIs(t, finished.Cancel(), os.ErrProcessDone)
}

// A git command that spawns a child (git pull -> git rebase) must not leave
// that child running once the context expires: the orphan holds
// .git/index.lock and wedges every later sync cycle.
func TestGitCommandsKillWholeProcessGroupOnTimeout(t *testing.T) {
	tests := []struct {
		name string
		run  func(ctx context.Context) error
	}{
		{"RunGit", func(ctx context.Context) error {
			_, err := RunGit(ctx, "", "fake-subcommand")
			return err
		}},
		{"NewNetworkCmd", func(ctx context.Context) error {
			return NewNetworkCmd(ctx, "fake-subcommand").Run()
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			binDir := t.TempDir()
			pidFile := filepath.Join(binDir, "child.pid")
			// the fake git forks a long-lived child (stdio detached so it cannot
			// pin the output pipe) and waits, like pull waiting on rebase
			script := "#!/bin/sh\nsleep 30 >/dev/null 2>&1 &\necho $! > '" + pidFile + "'\nwait\n"
			require.NoError(t, os.WriteFile(filepath.Join(binDir, "git"), []byte(script), 0o755))
			t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

			// cancel only once the child exists, so a loaded machine cannot
			// expire the context before the fake git has forked anything
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			errCh := make(chan error, 1)
			go func() { errCh <- tt.run(ctx) }()

			childPid := waitForPidFile(t, pidFile)
			t.Cleanup(func() { _ = syscall.Kill(childPid, syscall.SIGKILL) })
			cancel()
			require.Error(t, <-errCh)

			// SIGTERM is sent at cancel; allow the SIGKILL fallback window
			deadline := time.Now().Add(3 * time.Second)
			for pidExists(childPid) && time.Now().Before(deadline) {
				time.Sleep(20 * time.Millisecond)
			}
			require.False(t, pidExists(childPid), "grandchild %d survived the context timeout", childPid)
		})
	}
}

// A canceled git must not leave its lock files behind: a SIGKILLed git cannot
// remove them, and the next fetch then fails with "cannot lock ref ... File
// exists" (#1569). update-ref --stdin holds refs/heads/main.lock between
// "prepare" and "commit", so canceling there is deterministic.
func TestCanceledGitRemovesItsRefLock(t *testing.T) {
	repo, head := initRepoWithCommits(t, 1)
	lock := filepath.Join(repo, ".git", "refs", "heads", "main.lock")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := NewNetworkCmd(ctx, "-C", repo, "update-ref", "--stdin")
	stdin, err := cmd.StdinPipe()
	require.NoError(t, err)
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	_, err = stdin.Write([]byte("start\nupdate refs/heads/main " + head + "\nprepare\n"))
	require.NoError(t, err)

	reply := bufio.NewReader(stdout)
	for _, want := range []string{"start: ok", "prepare: ok"} {
		line, err := reply.ReadString('\n')
		require.NoError(t, err)
		require.Equal(t, want, strings.TrimSpace(line))
	}
	require.FileExists(t, lock, "fixture must hold the ref lock when canceled")

	cancel()
	require.Error(t, cmd.Wait())
	require.NoFileExists(t, lock, "canceled git left a stale ref lock")
}
