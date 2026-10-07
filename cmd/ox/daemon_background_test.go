//go:build darwin || linux || freebsd

package main

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/testguard"
	"github.com/stretchr/testify/require"
)

// killTestDaemons registers a cleanup that SIGKILLs every `<oxBin> daemon start`
// process and fails the test if one survives. The daemons these tests start are
// detached on purpose, so nothing else reaps them, and `ox daemon stop` is a
// no-op for a daemon that has not finished starting (it reports "not running"
// and leaves the process behind). oxBin is a per-test build, so matching its
// path cannot touch the developer's real daemons; a shared prebuilt binary is
// skipped for that reason.
func killTestDaemons(t *testing.T, oxBin string) {
	t.Helper()
	if os.Getenv(testguard.TestOxBinaryEnv) != "" {
		return
	}
	t.Cleanup(func() {
		// kill inside the poll: a dying `daemon start` parent can still spawn its
		// foreground child after the first sweep.
		var discoveryErr error
		require.Eventually(t, func() bool {
			pids, err := listTestDaemons(oxBin)
			if err != nil {
				discoveryErr = err
				return true
			}
			for _, pid := range pids {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
			return len(pids) == 0
		}, 5*time.Second, 50*time.Millisecond, "an `ox daemon start` process outlived the test")
		require.NoError(t, discoveryErr, "cannot verify test daemon cleanup")
	})
}

func listTestDaemons(oxBin string) ([]int, error) {
	out, err := exec.Command("ps", "-Ao", "pid=,args=").Output()
	if err != nil {
		return nil, err
	}
	var pids []int
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 || fields[1] != oxBin || fields[2] != "daemon" || fields[3] != "start" {
			continue
		}
		if pid, err := strconv.Atoi(fields[0]); err == nil {
			pids = append(pids, pid)
		}
	}
	return pids, nil
}

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
			// registered before StopDaemonCleanup so it runs after it (cleanups are
			// LIFO): `daemon stop` is a no-op while a daemon is still starting, so
			// this is the backstop that guarantees nothing outlives the test.
			killTestDaemons(t, oxBin)
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
			require.Eventually(t, readStatus, 30*time.Second, 20*time.Millisecond,
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

// Process discovery must distinguish an unavailable ps from a successful empty list.
func TestTestDaemonDiscoveryReportsFailure(t *testing.T) {
	for _, tt := range []struct {
		name, script string
		wantError    bool
		want         []int
	}{
		{name: "discovery failure", script: "#!/bin/sh\nexit 1\n", wantError: true},
		{name: "empty success", script: "#!/bin/sh\nexit 0\n"},
		{name: "only owned daemon", script: "#!/bin/sh\nprintf '%s\\n' '123 /tmp/test-ox daemon start --foreground' '456 /tmp/other-ox daemon start' '789 /tmp/test-ox version'\n", want: []int{123}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			bin := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(bin, "ps"), []byte(tt.script), 0o755))
			t.Setenv("PATH", bin)
			pids, err := listTestDaemons("/tmp/test-ox")
			if tt.wantError {
				require.Error(t, err)
				require.Nil(t, pids)
			} else {
				require.NoError(t, err)
				require.Equal(t, tt.want, pids)
			}
		})
	}
}

// A failed process listing must fail cleanup itself, even when no PIDs were returned.
func TestDaemonCleanupFailsWhenProcessDiscoveryFails(t *testing.T) {
	const childEnv = "GO_TEST_DAEMON_DISCOVERY_FAILURE"
	if os.Getenv(childEnv) == "1" {
		killTestDaemons(t, "/tmp/test-ox")
		return
	}
	bin := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bin, "ps"), []byte("#!/bin/sh\nexit 1\n"), 0o755))
	cmd := exec.Command(os.Args[0], "-test.run=^TestDaemonCleanupFailsWhenProcessDiscoveryFails$")
	cmd.Env = []string{childEnv + "=1", "PATH=" + bin, "HOME=" + t.TempDir()}
	out, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	require.ErrorAs(t, err, &exitErr)
	require.Equal(t, 1, exitErr.ExitCode())
	require.Contains(t, string(out), "cannot verify test daemon cleanup")
}
