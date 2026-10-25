//go:build !windows

package daemon

import (
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestKillStaleDaemon_DrainingDaemonWithClosedListener is the state a second
// `ox daemon restart` meets: the daemon is still alive and draining after an
// earlier stop, its listener is closed, but the socket FILE is still on disk
// (SetUnlinkOnClose(false) leaves it for Daemon.cleanup). Dialing it fails with
// ECONNREFUSED.
//
// Failure prevented: `ox daemon restart` failed with "connect: connection
// refused" and did nothing, because only IPC was tried. The CLI now falls back
// to KillStaleDaemon for exactly this state, so this pins that KillStaleDaemon
// handles it: no IPC, straight to the PID-verified signal, then cleans up the
// registry entry, PID file and socket file.
func TestKillStaleDaemon_DrainingDaemonWithClosedListener(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping daemon signal test in short mode")
	}

	// short path: macOS limits unix socket paths to 104 bytes
	tmpDir, err := os.MkdirTemp("/tmp", "oxd-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(tmpDir) })
	t.Setenv("OX_XDG_DISABLE", "")
	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("XDG_RUNTIME_DIR", tmpDir)

	childPID, childDone := startFakeOxDaemon(t, fakeDaemonExitsOnTerm)

	wsID := "draining"
	socketPath := SocketPathForWorkspace(wsID)
	require.NoError(t, os.MkdirAll(filepath.Dir(socketPath), 0o700))

	// bind then close without unlinking: a socket file with no listener
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socketPath, Net: "unix"})
	require.NoError(t, err)
	listener.SetUnlinkOnClose(false)
	require.NoError(t, listener.Close())
	_, statErr := os.Stat(socketPath)
	require.NoError(t, statErr, "precondition: stale socket file must remain")
	require.Error(t, NewClientWithSocket(socketPath).Ping(), "precondition: dialing the stale socket must fail")

	pidPath := PidPathForWorkspace(wsID)
	require.NoError(t, os.WriteFile(pidPath, []byte("1"), 0o600))
	writeTestRegistry(t, tmpDir, map[string]DaemonInfo{
		wsID: {WorkspaceID: wsID, PID: childPID, SocketPath: socketPath},
	})

	require.NoError(t, KillStaleDaemon(wsID))

	select {
	case <-childDone:
	case <-time.After(5 * time.Second):
		t.Fatal("the draining daemon process was not stopped")
	}

	_, err = os.Stat(socketPath)
	assert.True(t, os.IsNotExist(err), "stale socket file must be removed")
	_, err = os.Stat(pidPath)
	assert.True(t, os.IsNotExist(err), "PID file must be removed")
	reg, err := LoadRegistry()
	require.NoError(t, err)
	assert.Nil(t, reg.FindByWorkspaceID(wsID), "registry entry must be removed")
}
