package main

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	"github.com/sageox/ox/internal/daemon"
)

// fakeDaemonDials listens on the daemon socket for the current workspace and
// counts accepted connections and records each request's message type. Each
// connection gets one canned success reply.
func fakeDaemonDials(t *testing.T) (*atomic.Int64, func() []string) {
	t.Helper()
	sock := daemon.SocketPath()
	require.NoError(t, os.MkdirAll(filepath.Dir(sock), 0o700))
	ln, err := net.Listen("unix", sock)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })

	var dials atomic.Int64
	var mu sync.Mutex
	var types []string
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			dials.Add(1)
			go func() {
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(time.Second))
				line, _ := bufio.NewReader(conn).ReadBytes('\n')
				var msg daemon.Message
				if json.Unmarshal(line, &msg) == nil {
					mu.Lock()
					types = append(types, msg.Type)
					mu.Unlock()
				}
				_, _ = conn.Write([]byte(`{"success":true}` + "\n"))
			}()
		}
	}()
	return &dials, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(types)
	}
}

// Failure prevented: a non-hook command (or a nil command) being treated as a
// hook would skip the liveness ping and dial a daemon that may not exist.
func TestIsAgentHookInvocation(t *testing.T) {
	tests := []struct {
		name string
		use  string
		args []string
		want bool
	}{
		{name: "agent hook", use: "agent", args: []string{"hook", "PostToolUse"}, want: true},
		{name: "agent other subcommand", use: "agent", args: []string{"prime"}, want: false},
		{name: "agent no args", use: "agent", args: nil, want: false},
		{name: "non-agent command with hook arg", use: "status", args: []string{"hook"}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := &cobra.Command{Use: tt.use}
			require.NoError(t, cmd.ParseFlags(tt.args))
			require.Equal(t, tt.want, isAgentHookInvocation(cmd))
		})
	}
	require.False(t, isAgentHookInvocation(nil))
}

// Failure prevented: a heartbeat sent from a directory that is not an
// initialized project, spending a daemon connection for nothing.
func TestMaybeHeartbeatSkipsUninitializedProject(t *testing.T) {
	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("XDG_RUNTIME_DIR", "/tmp") // short path: unix socket limit ~104 chars
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Chdir(t.TempDir())

	dials, _ := fakeDaemonDials(t)
	agent := &cobra.Command{Use: "agent"}
	require.NoError(t, agent.ParseFlags([]string{"hook", "PostToolUse"}))

	maybeHeartbeat(agent)
	DrainHeartbeats()

	require.Zero(t, dials.Load())
}

func TestAgentHookInvocationDialBudget(t *testing.T) {
	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("XDG_RUNTIME_DIR", "/tmp") // short path: unix socket limit ~104 chars
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	repo := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".sageox"), 0o755))
	require.NoError(t, exec.Command("git", "-C", repo, "init", "-q").Run())
	t.Chdir(repo)

	dials, received := fakeDaemonDials(t)

	agent := &cobra.Command{Use: "agent"}
	require.NoError(t, agent.ParseFlags([]string{"hook", "PostToolUse"}))

	// the three daemon touches an `ox agent hook PostToolUse` makes:
	// heartbeat (root pre-run), settings (initFeatureFlags), whispers (handleAfterTool).
	// the IPC server handles one request per connection, so 3 dials is the floor.
	maybeHeartbeat(agent)
	DrainHeartbeats()
	initFeatureFlags(agent)
	emitWhispers(os.Stderr, "Oxtest")

	require.Equal(t, int64(3), dials.Load(), "hook must spend exactly three dials and none on liveness pings")
	require.ElementsMatch(t, []string{daemon.MsgTypeHeartbeat, daemon.MsgTypeSettingsGet, daemon.MsgTypeWhispers}, received())
}

// Failure prevented: a non-hook command skipping the liveness ping and dialing
// a daemon that is not there; only hook invocations get the shortcut.
func TestDaemonReachable_NonHookChecksDaemon(t *testing.T) {
	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	status := &cobra.Command{Use: "status"}
	require.False(t, daemonReachable(status), "no daemon socket: non-hook commands must report unreachable")

	hook := &cobra.Command{Use: "agent"}
	require.NoError(t, hook.ParseFlags([]string{"hook", "PostToolUse"}))
	require.True(t, daemonReachable(hook), "hook invocations skip the liveness ping")
}
