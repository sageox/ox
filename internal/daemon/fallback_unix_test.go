//go:build !windows

package daemon

import (
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- KillStaleDaemon tests ---

// setupIsolatedRegistry creates an isolated XDG environment and writes a registry
// with the given entries. Returns the tmp dir.
func setupIsolatedRegistry(t *testing.T, entries map[string]DaemonInfo) string {
	t.Helper()
	tmpDir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", tmpDir)

	reg := &Registry{Daemons: entries}
	data, err := json.MarshalIndent(reg, "", "  ")
	require.NoError(t, err)

	regDir := filepath.Join(tmpDir, "sageox", "daemon")
	require.NoError(t, os.MkdirAll(regDir, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(regDir, "registry.json"), data, 0600))

	return tmpDir
}

func TestKillStaleDaemon_NoRegistryEntry(t *testing.T) {
	setupIsolatedRegistry(t, map[string]DaemonInfo{})

	// should be a no-op, no panics
	KillStaleDaemon("nonexistent")
}

func TestKillStaleDaemon_DeadProcess(t *testing.T) {
	wsID := "deadbeef"
	tmpDir := setupIsolatedRegistry(t, map[string]DaemonInfo{
		wsID: {
			WorkspaceID: wsID,
			PID:         999999999, // very unlikely to be alive
			SocketPath:  "/tmp/nonexistent.sock",
		},
	})

	// create stale PID file so cleanup behavior is actually exercised
	pidPath := filepath.Join(tmpDir, "sageox", "daemon", "daemon-"+wsID+".pid")
	require.NoError(t, os.WriteFile(pidPath, []byte("999999999\n"), 0600))

	KillStaleDaemon(wsID)

	// registry entry should be cleaned up
	reg, err := LoadRegistry()
	require.NoError(t, err)
	assert.Nil(t, reg.FindByWorkspaceID(wsID), "dead entry should be removed from registry")

	// stale PID file should be cleaned up
	_, err = os.Stat(pidPath)
	assert.True(t, os.IsNotExist(err), "stale PID file should be removed")
}

func TestKillStaleDaemon_AliveButReachable(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping daemon IPC test in short mode")
	}

	// Use /tmp directly to keep the socket path short — macOS limits Unix socket
	// paths to 104 chars, and t.TempDir() generates paths under /var/folders/...
	// which are too long when combined with the daemon socket suffix.
	tmpDir, err := os.MkdirTemp("/tmp", "oxd-")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(tmpDir) })
	t.Setenv("XDG_RUNTIME_DIR", tmpDir)

	// start a child process to act as the "stale daemon"
	child := exec.Command("sleep", "300")
	require.NoError(t, child.Start())
	childPID := child.Process.Pid
	childDone := make(chan struct{})
	go func() {
		_ = child.Wait()
		close(childDone)
	}()

	wsID := "reachable"
	socketPath := filepath.Join(tmpDir, "sageox", "daemon", "daemon-"+wsID+".sock")
	require.NoError(t, os.MkdirAll(filepath.Dir(socketPath), 0700))

	// create a mock socket that responds to ping and stop, then kills the child
	listener, err := net.Listen("unix", socketPath)
	require.NoError(t, err)
	stopCalled := make(chan struct{}, 1)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			buf := make([]byte, 4096)
			n, _ := conn.Read(buf)
			var msg Message
			if json.Unmarshal(buf[:n], &msg) == nil {
				switch msg.Type {
				case MsgTypePing:
					resp := Response{Success: true, Data: json.RawMessage(`"pong"`)}
					data, _ := json.Marshal(resp)
					conn.Write(append(data, '\n'))
				case MsgTypeStop:
					resp := Response{Success: true}
					data, _ := json.Marshal(resp)
					conn.Write(append(data, '\n'))
					// simulate graceful shutdown: kill the child process
					_ = child.Process.Kill()
					stopCalled <- struct{}{}
				}
			}
			conn.Close()
		}
	}()
	defer listener.Close()
	t.Cleanup(func() {
		_ = child.Process.Kill()
		<-childDone
	})

	// write registry entry with the child's PID
	reg := &Registry{Daemons: map[string]DaemonInfo{
		wsID: {
			WorkspaceID: wsID,
			PID:         childPID,
			SocketPath:  socketPath,
		},
	}}
	data, _ := json.MarshalIndent(reg, "", "  ")
	regPath := filepath.Join(tmpDir, "sageox", "daemon", "registry.json")
	require.NoError(t, os.WriteFile(regPath, data, 0600))

	KillStaleDaemon(wsID)

	// IPC stop should have been called (graceful path)
	select {
	case <-stopCalled:
		// good — graceful stop was used
	case <-time.After(2 * time.Second):
		t.Fatal("expected IPC stop to be called")
	}
}

func TestKillStaleDaemon_StopAckedPidAlive(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("requires Linux /proc cmdline check")
	}
	if testing.Short() {
		t.Skip("skipping daemon IPC escalation test in short mode")
	}

	tmpDir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", tmpDir)

	// start a child process whose cmdline contains "ox" and "daemon"
	child := exec.Command("bash", "-c", "exec -a 'ox daemon start --foreground' sleep 300")
	require.NoError(t, child.Start())
	childPID := child.Process.Pid
	childDone := make(chan struct{})
	go func() {
		_ = child.Wait()
		close(childDone)
	}()
	t.Cleanup(func() {
		_ = child.Process.Kill()
		<-childDone
	})

	wsID := "stop-acked"
	socketPath := filepath.Join(tmpDir, "sageox", "daemon", "daemon-"+wsID+".sock")
	require.NoError(t, os.MkdirAll(filepath.Dir(socketPath), 0700))

	// mock daemon ACKs Stop but does NOT kill the child — simulates a hung daemon
	listener, err := net.Listen("unix", socketPath)
	require.NoError(t, err)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			buf := make([]byte, 4096)
			n, _ := conn.Read(buf)
			var msg Message
			if json.Unmarshal(buf[:n], &msg) == nil {
				switch msg.Type {
				case MsgTypePing:
					resp := Response{Success: true, Data: json.RawMessage(`"pong"`)}
					data, _ := json.Marshal(resp)
					conn.Write(append(data, '\n'))
				case MsgTypeStop:
					// ACK stop but DON'T kill the process
					resp := Response{Success: true}
					data, _ := json.Marshal(resp)
					conn.Write(append(data, '\n'))
				}
			}
			conn.Close()
		}
	}()
	defer listener.Close()

	reg := &Registry{Daemons: map[string]DaemonInfo{
		wsID: {WorkspaceID: wsID, PID: childPID, SocketPath: socketPath},
	}}
	data, _ := json.MarshalIndent(reg, "", "  ")
	require.NoError(t, os.WriteFile(
		filepath.Join(tmpDir, "sageox", "daemon", "registry.json"), data, 0600))

	err = KillStaleDaemon(wsID)
	require.NoError(t, err, "should succeed via SIGTERM escalation")

	// child should be dead — SIGTERM escalation should have killed it
	select {
	case <-childDone:
		// child was reaped — escalation to SIGTERM worked
	case <-time.After(3 * time.Second):
		t.Fatal("child process did not exit after IPC→SIGTERM escalation")
	}
}

func TestKillStaleDaemon_AliveButUnreachable(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("requires Linux /proc cmdline check")
	}
	if testing.Short() {
		t.Skip("skipping process kill test in short mode")
	}

	tmpDir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", tmpDir)

	// start a child process whose cmdline contains "ox" and "daemon"
	// so isOxDaemonProcess returns true
	child := exec.Command("bash", "-c", "exec -a 'ox daemon start --foreground' sleep 300")
	require.NoError(t, child.Start())
	childPID := child.Process.Pid
	// reap child in background so it doesn't become zombie after SIGTERM
	childDone := make(chan struct{})
	go func() {
		_ = child.Wait()
		close(childDone)
	}()
	t.Cleanup(func() {
		_ = child.Process.Kill()
		<-childDone
	})

	// verify child is alive
	require.NoError(t, signalProcess(childPID, 0), "child should be alive")

	wsID := "unreachable"
	// no socket file — daemon is unreachable via IPC
	regDir := filepath.Join(tmpDir, "sageox", "daemon")
	require.NoError(t, os.MkdirAll(regDir, 0700))
	reg := &Registry{Daemons: map[string]DaemonInfo{
		wsID: {
			WorkspaceID: wsID,
			PID:         childPID,
			SocketPath:  filepath.Join(regDir, "daemon-"+wsID+".sock"), // doesn't exist
		},
	}}
	data, _ := json.MarshalIndent(reg, "", "  ")
	require.NoError(t, os.WriteFile(filepath.Join(regDir, "registry.json"), data, 0600))

	KillStaleDaemon(wsID)

	// child should exit after SIGTERM — wait for reaper goroutine
	select {
	case <-childDone:
		// child was reaped — SIGTERM worked
	case <-time.After(3 * time.Second):
		t.Fatal("child process did not exit after SIGTERM")
	}
}

// --- buildDaemonArgs tests ---

func TestBuildDaemonArgs_WithRepo(t *testing.T) {
	args := buildDaemonArgs("sageox/ox")
	assert.Equal(t, []string{"daemon", "start", "--foreground", "--repo=sageox/ox"}, args)
}

func TestBuildDaemonArgs_WithoutRepo(t *testing.T) {
	args := buildDaemonArgs("")
	assert.Equal(t, []string{"daemon", "start", "--foreground"}, args)
	for _, arg := range args {
		assert.NotContains(t, arg, "--repo")
	}
}
