//go:build !windows

package main

import (
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/daemon"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// startStandInDaemon runs a process the test owns that presents as `ox daemon
// start --foreground` (argv[0] basename "ox", "daemon" argument) so the PID-reuse
// guard in KillStaleDaemon accepts it, and exits cleanly on SIGTERM. Nothing
// here ever signals a real daemon: the PID is one this test spawned.
func startStandInDaemon(t *testing.T) (pid int, exited <-chan struct{}) {
	t.Helper()

	sh, err := exec.LookPath("sh")
	require.NoError(t, err)
	fakeExe := filepath.Join(t.TempDir(), "ox")
	require.NoError(t, os.Symlink(sh, fakeExe))

	readyRead, readyWrite, err := os.Pipe()
	require.NoError(t, err)
	defer func() { _ = readyRead.Close() }()
	defer func() { _ = readyWrite.Close() }()

	body := "trap 'exit 0' TERM; printf R >&3; exec 3>&-; while true; do sleep 1; done"
	child := exec.Command(fakeExe, "-c", body, "daemon", "start", "--foreground")
	child.ExtraFiles = []*os.File{readyWrite}
	require.NoError(t, child.Start())
	_ = readyWrite.Close()

	done := make(chan struct{})
	go func() {
		_ = child.Wait() // reap, so a dead child is not mistaken for a live one
		close(done)
	}()
	t.Cleanup(func() {
		_ = child.Process.Kill()
		<-done
	})

	ready := make(chan error, 1)
	go func() {
		var b [1]byte
		_, err := io.ReadFull(readyRead, b[:])
		ready <- err
	}()
	select {
	case err := <-ready:
		require.NoError(t, err, "stand-in daemon exited before it was ready")
	case <-time.After(5 * time.Second):
		t.Fatal("stand-in daemon did not become ready")
	}
	return child.Process.Pid, done
}

// TestStopRunningDaemonAndWait_RefusingSocketFallsBackToKill reproduces the
// second `ox daemon restart` against the real stop path: the previous daemon is
// alive and draining, its listener is closed, its socket file is still on disk,
// and its registry entry still names a live PID — so IsRunning() is true but
// every IPC call is refused.
//
// Failure prevented: "Error: failed to stop daemon: connect to daemon: dial
// unix ...: connect: connection refused", with the daemon left running and no
// restart performed.
func TestStopRunningDaemonAndWait_RefusingSocketFallsBackToKill(t *testing.T) {
	if testing.Short() {
		t.Skip("short: spawns a stand-in daemon process")
	}

	// short path: macOS limits unix socket paths to 104 bytes
	runtimeDir, err := os.MkdirTemp("/tmp", "ox-stop-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(runtimeDir) })
	t.Setenv("OX_XDG_DISABLE", "")
	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)

	// Isolation guard: every path the stop code touches must live under the
	// scratch dir, or this test could signal a developer's real daemon.
	require.True(t, strings.HasPrefix(daemon.RegistryPath(), runtimeDir), "registry path %q escaped the scratch dir", daemon.RegistryPath())
	require.True(t, strings.HasPrefix(daemon.SocketPath(), runtimeDir), "socket path %q escaped the scratch dir", daemon.SocketPath())

	pid, exited := startStandInDaemon(t)

	// a socket file with no listener behind it
	wsID := daemon.CurrentWorkspaceID()
	socketPath := daemon.SocketPathForWorkspace(wsID)
	require.NoError(t, os.MkdirAll(filepath.Dir(socketPath), 0o700))
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socketPath, Net: "unix"})
	require.NoError(t, err)
	listener.SetUnlinkOnClose(false)
	require.NoError(t, listener.Close())

	reg := &daemon.Registry{Daemons: map[string]daemon.DaemonInfo{
		wsID: {WorkspaceID: wsID, PID: pid, SocketPath: socketPath},
	}}
	require.NoError(t, reg.Save())

	// precondition: this is exactly the state restart saw
	require.True(t, daemon.IsRunning(), "precondition: socket file + live registry PID reads as running")
	require.Error(t, daemon.NewClientForCurrentRepoWithTimeout(daemonStopRequestTimeout).Stop(), "precondition: stop over IPC is refused")

	var warnings []string
	ops := liveDaemonStopOps()
	ops.progress = func(string) {}
	ops.warn = func(msg string) { warnings = append(warnings, msg) }

	outcome, err := stopDaemonAndWait(ops, daemonStopWaitBudget, daemonStopWaitInterval, daemonStopProgressAfter)

	require.NoError(t, err, "restart must recover from a refused stop request, not surface ECONNREFUSED")
	assert.Equal(t, daemonStopForced, outcome)
	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0], "force-stopping", "the CLI must say it had to force-stop")

	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("the stand-in daemon is still alive after force-stop")
	}
	assert.False(t, daemon.IsRunning(), "the workspace must be free for the restart to proceed")
	_, statErr := os.Stat(socketPath)
	assert.True(t, os.IsNotExist(statErr), "stale socket file must be cleaned up")
}
