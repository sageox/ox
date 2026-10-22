//go:build darwin || linux || freebsd

package main

import (
	"context"
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
	"github.com/sageox/ox/internal/daemon"
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
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ps", "-Ao", "pid=,args=")
	cmd.WaitDelay = 100 * time.Millisecond
	out, err := cmd.Output()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, err
	}
	var pids []int
	for _, line := range strings.Split(string(out), "\n") {
		pidText, command, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok {
			continue
		}
		args, owned := strings.CutPrefix(strings.TrimSpace(command), oxBin+" ")
		fields := strings.Fields(args)
		if !owned || len(fields) < 2 || fields[0] != "daemon" || fields[1] != "start" {
			continue
		}
		if pid, err := strconv.Atoi(pidText); err == nil {
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

// Failure prevented: a version-upgrade re-exec starts from HOME and replaces
// the project's daemon with one that has neither its workspace nor its Ledger.
func TestDaemonUpgradeRetainsProject(t *testing.T) {
	if testing.Short() {
		t.Skip("short: builds ox and exercises a real daemon re-exec")
	}
	oxBin := testguard.BuildOxBinary(t, filepath.Dir(filepath.Dir(packageDir)))
	runtimeDir, err := os.MkdirTemp("/tmp", "ox-upgrade-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(runtimeDir)) })
	repo, home := t.TempDir(), t.TempDir()
	env := []string{
		"HOME=" + home, "USER=" + filepath.Base(runtimeDir),
		"XDG_CONFIG_HOME=" + filepath.Join(home, "config"),
		"XDG_CACHE_HOME=" + filepath.Join(home, "cache"),
		"XDG_DATA_HOME=" + filepath.Join(home, "data"),
		"XDG_STATE_HOME=" + filepath.Join(home, "state"),
		"XDG_RUNTIME_DIR=" + runtimeDir,
		"OX_XDG_ENABLE=1", "OX_XDG_DISABLE=",
		"OX_USER_CONFIG=" + filepath.Join(home, "config.yaml"),
		"OX_GIT_CREDENTIALS_FILE=" + filepath.Join(home, "git-creds.json"),
		"SAGEOX_ENDPOINT=http://127.0.0.1:1",
		"SAGEOX_DAEMON=true", "OX_NO_DAEMON=0",
		"HTTP_PROXY=http://127.0.0.1:1", "HTTPS_PROXY=http://127.0.0.1:1",
		"NO_PROXY=127.0.0.1,localhost",
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + filepath.Join(home, ".gitconfig"),
	}
	for _, kv := range env {
		key, value, _ := strings.Cut(kv, "=")
		t.Setenv(key, value)
	}
	// disposable_machine is deliberately unset, as on an ordinary laptop.
	require.NoError(t, os.WriteFile(filepath.Join(home, "config.yaml"), []byte("agent_worker:\n  agent: none\n"), 0o600))
	initGitRepo(t, repo)
	repoID := filepath.Base(runtimeDir)
	require.NoError(t, config.SaveProjectConfig(repo, &config.ProjectConfig{
		RepoID: repoID, Endpoint: "http://127.0.0.1:1",
	}))
	ledgerPath := config.DefaultLedgerPath(repoID, "http://127.0.0.1:1")
	require.NoError(t, os.MkdirAll(ledgerPath, 0o700))
	initGitRepo(t, ledgerPath)

	log, err := os.Create(filepath.Join(home, "daemon.log"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = log.Close() })
	t.Cleanup(func() {
		if t.Failed() {
			data, _ := os.ReadFile(log.Name())
			t.Logf("test daemon log:\n%s", data)
		}
	})
	command := testguard.OxCmd(t, oxBin, repo, env, "daemon", "start", "--foreground")
	command.Stdout, command.Stderr = log, log
	require.NoError(t, command.Start())
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	// Re-exec keeps this PID. Kill only our child, including on a failed check
	// where it incorrectly restarted under HOME and repo-scoped stop misses it.
	t.Cleanup(func() {
		_ = command.Process.Kill()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("test daemon did not exit")
		}
	})
	var status daemon.StatusJSON
	readStatus := func() bool {
		out, err := testguard.OxCmd(t, oxBin, repo, env, "daemon", "status", "--json").Output()
		status = daemon.StatusJSON{}
		return err == nil && json.Unmarshal(out, &status) == nil && status.Running
	}
	require.Eventually(t, readStatus, 30*time.Second, 50*time.Millisecond, "daemon must start before the upgrade")
	require.Equal(t, command.Process.Pid, status.Pid)
	require.Equal(t, ledgerPath, status.LedgerPath)

	workspaceID := daemon.RepoBasedWorkspaceID(repo)
	payload, err := json.Marshal(daemon.HeartbeatPayload{
		RepoPath: repo, WorkspaceID: workspaceID, Timestamp: time.Now(), CLIVersion: "999.0.0",
	})
	require.NoError(t, err)
	client := daemon.NewClientWithSocket(daemon.SocketPathForWorkspace(workspaceID))
	require.NoError(t, client.SendOneWay(daemon.Message{
		Type: daemon.MsgTypeHeartbeat, WorkspaceID: workspaceID, CallerID: "upgrade-test", Payload: payload,
	}))
	// Wait until a second Start actually runs; seeing the original daemon's
	// status immediately after the heartbeat would make this pass without exec.
	require.Eventually(t, func() bool {
		data, err := os.ReadFile(log.Name())
		return err == nil && strings.Count(string(data), "daemon starting") >= 2
	}, 30*time.Second, 50*time.Millisecond, "version mismatch must really re-exec")
	require.Eventually(t, readStatus, 10*time.Second, 50*time.Millisecond, "restarted daemon must retain the project's socket")
	require.Equal(t, command.Process.Pid, status.Pid, "re-exec must preserve the daemon PID")
	require.Equal(t, ledgerPath, status.LedgerPath, "the restarted daemon must still serve the project's Ledger")

	out, code, _ := testguard.RunOx(t, oxBin, repo, env, "daemon", "start")
	require.Zero(t, code, "%s", out)
	require.True(t, readStatus())
	require.Equal(t, command.Process.Pid, status.Pid, "the next CLI must not spawn a replacement daemon")
	out, code, _ = testguard.RunOx(t, oxBin, repo, env, "daemon", "stop")
	require.Zero(t, code, "%s", out)
}

// Process discovery must distinguish an unavailable ps from a successful empty list.
func TestTestDaemonDiscoveryReportsFailure(t *testing.T) {
	for _, tt := range []struct {
		name, script string
		oxBin        string
		wantError    bool
		want         []int
	}{
		{name: "discovery failure", script: "#!/bin/sh\nexit 1\n", wantError: true},
		{name: "empty success", script: "#!/bin/sh\nexit 0\n"},
		{name: "path with spaces", oxBin: "/tmp/Test Dir/ox", script: "#!/bin/sh\nprintf '%s\\n' '123 /tmp/Test Dir/ox daemon start --foreground' '456 /tmp/Test Dir/ox-other daemon start' '789 /tmp/Test Dir/ox version'\n", want: []int{123}},
		{name: "only owned daemon", script: "#!/bin/sh\nprintf '%s\\n' '123 /tmp/test-ox daemon start --foreground' '456 /tmp/other-ox daemon start' '789 /tmp/test-ox version'\n", want: []int{123}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			bin := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(bin, "ps"), []byte(tt.script), 0o755))
			t.Setenv("PATH", bin)
			oxBin := tt.oxBin
			if oxBin == "" {
				oxBin = "/tmp/test-ox"
			}
			pids, err := listTestDaemons(oxBin)
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

// A stuck process listing must not leave a cleanup callback blocked indefinitely.
func TestTestDaemonDiscoveryBoundsStalledCommand(t *testing.T) {
	if testing.Short() {
		t.Skip("short: exercises a one-second subprocess deadline")
	}
	bin := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bin, "ps"), []byte("#!/bin/sh\nexec /bin/sleep 60\n"), 0o755))
	t.Setenv("PATH", bin)
	start := time.Now()
	pids, err := listTestDaemons("/tmp/test-ox")
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Nil(t, pids)
	require.Less(t, time.Since(start), 3*time.Second)
}
