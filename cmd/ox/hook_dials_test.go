package main

import (
	"bufio"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	"github.com/sageox/ox/internal/daemon"
)

// fakeDaemonDials listens on the daemon socket for the current workspace and
// counts accepted connections. Each connection gets one canned success reply.
func fakeDaemonDials(t *testing.T) *atomic.Int64 {
	t.Helper()
	sock := daemon.SocketPath()
	require.NoError(t, os.MkdirAll(filepath.Dir(sock), 0o700))
	ln, err := net.Listen("unix", sock)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })

	var dials atomic.Int64
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
				_, _ = bufio.NewReader(conn).ReadBytes('\n')
				_, _ = conn.Write([]byte(`{"success":true}` + "\n"))
			}()
		}
	}()
	return &dials
}

func TestAgentHookInvocationDialBudget(t *testing.T) {
	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("XDG_RUNTIME_DIR", "/tmp") // short path: unix socket limit ~104 chars
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	repo := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".sageox"), 0o755))
	require.NoError(t, exec.Command("git", "-C", repo, "init", "-q").Run())
	t.Chdir(repo)

	dials := fakeDaemonDials(t)

	agent := &cobra.Command{Use: "agent"}
	require.NoError(t, agent.ParseFlags([]string{"hook", "PostToolUse"}))

	// the three daemon touches an `ox agent hook PostToolUse` makes:
	// heartbeat (root pre-run), settings (initFeatureFlags), whispers (handleAfterTool).
	// the IPC server handles one request per connection, so 3 dials is the floor.
	maybeHeartbeat(agent)
	DrainHeartbeats()
	initFeatureFlags(agent)
	emitWhispers(os.Stderr, "Oxtest")

	require.LessOrEqual(t, dials.Load(), int64(3), "hook must not spend dials on liveness pings")
}
