//go:build darwin || linux || freebsd

package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/testguard"
	"github.com/stretchr/testify/require"
)

// Codex cleans up a tool command's process group after it returns. Background
// daemons must survive that cleanup, including when prime or sync starts them.
func TestBackgroundDaemonSurvivesCommandCleanup(t *testing.T) {
	if testing.Short() {
		t.Skip("short: builds ox and starts real daemons")
	}
	oxBin := testguard.BuildOxBinary(t, filepath.Dir(filepath.Dir(packageDir)))
	for _, args := range [][]string{
		{"daemon", "start"},
		{"agent", "prime", "--agent", "codex", "--format", "json"},
		{"sync", "--json"},
	} {
		t.Run(args[0], func(t *testing.T) {
			// Keep Unix socket names below their platform length limit.
			runtimeDir, err := os.MkdirTemp("/tmp", "ox-bg-")
			require.NoError(t, err)
			t.Cleanup(func() { _ = os.RemoveAll(runtimeDir) })
			repo, home := t.TempDir(), t.TempDir()
			require.NoError(t, os.MkdirAll(filepath.Join(home, "config"), 0o700))
			initGitRepo(t, repo)
			require.NoError(t, config.SaveProjectConfig(repo, &config.ProjectConfig{
				RepoID:   filepath.Base(runtimeDir),
				Endpoint: "http://127.0.0.1:1",
			}))
			env := []string{
				"HOME=" + home,
				"USER=" + filepath.Base(runtimeDir),
				"XDG_CONFIG_HOME=" + filepath.Join(home, "config"),
				"XDG_CACHE_HOME=" + filepath.Join(home, "cache"),
				"XDG_DATA_HOME=" + filepath.Join(home, "data"),
				"XDG_STATE_HOME=" + filepath.Join(home, "state"),
				"XDG_RUNTIME_DIR=" + runtimeDir,
				"SAGEOX_ENDPOINT=http://127.0.0.1:1",
				"SAGEOX_DAEMON=true", "OX_NO_DAEMON=0",
				"HTTP_PROXY=http://127.0.0.1:1", "HTTPS_PROXY=http://127.0.0.1:1",
				"NO_PROXY=127.0.0.1,localhost",
			}
			testguard.StopDaemonCleanup(t, oxBin, repo, env)
			command := testguard.OxCmd(t, oxBin, repo, env, args...)
			command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			output, commandErr := command.CombinedOutput()
			// Sync can fail because this fixture has no remote. Its daemon must
			// still outlive the command, just as it does after a successful sync.
			if args[0] != "sync" {
				require.NoError(t, commandErr, "%s", output)
			}
			require.NotNil(t, command.Process)
			callerPGID := command.Process.Pid

			var status struct {
				Running bool `json:"running"`
				PID     int  `json:"pid"`
			}
			readStatus := func() bool {
				out, err := testguard.OxCmd(t, oxBin, repo, env, "daemon", "status", "--json").Output()
				status.Running = false
				return err == nil && json.Unmarshal(out, &status) == nil && status.Running
			}
			require.Eventually(t, readStatus, 5*time.Second, 20*time.Millisecond,
				"the command must actually start a daemon: %s", output)
			daemonPID := status.PID

			// Simulate the tool runner's cleanup, without signaling the test's
			// own process group. ESRCH means the caller already left no children.
			err = syscall.Kill(-callerPGID, syscall.SIGKILL)
			require.True(t, err == nil || errors.Is(err, syscall.ESRCH), "%v", err)
			require.Eventually(t, func() bool {
				return errors.Is(syscall.Kill(-callerPGID, 0), syscall.ESRCH)
			}, 5*time.Second, 20*time.Millisecond, "caller process group must be gone")
			require.True(t, readStatus(), "tool cleanup killed the background daemon")
			require.Equal(t, daemonPID, status.PID, "the original daemon must survive")
		})
	}
}
