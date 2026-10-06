//go:build !windows

package gitutil

import (
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
