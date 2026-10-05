package daemon

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// isolateDaemonState points every daemon state path (socket, PID file,
// registry) at a throwaway directory so a test can never touch a real daemon.
func isolateDaemonState(t *testing.T) {
	t.Helper()
	t.Setenv("OX_XDG_DISABLE", "")
	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	require.True(t, filepath.IsAbs(RegistryPath()))
}

// seedDaemonFiles creates the socket file, PID file and registry entry a
// running daemon owns for this workspace, with the registry entry naming
// registeredPID.
func seedDaemonFiles(t *testing.T, registeredPID int) {
	t.Helper()
	wsID := CurrentWorkspaceID()

	require.NoError(t, os.MkdirAll(filepath.Dir(SocketPath()), 0o700))
	require.NoError(t, os.WriteFile(SocketPath(), nil, 0o600))
	require.NoError(t, os.WriteFile(PidPath(), []byte("1"), 0o600))
	writeTestRegistry(t, os.Getenv("XDG_RUNTIME_DIR"), map[string]DaemonInfo{
		wsID: {WorkspaceID: wsID, PID: registeredPID, SocketPath: SocketPath()},
	})
}

// TestAwaitGoroutines_TimeoutCleansUpOwnFiles pins what happens when the
// goroutine drain gives up.
//
// Failure prevented: the timeout path returned ErrShutdownTimeout without
// removing the socket file, PID file or registry entry. GetState reads "socket
// file exists + registry PID alive" as Running, so `ox daemon restart` saw a
// daemon that was already exiting, errored, and the next attempt dialed a
// socket with no listener ("connection refused").
func TestAwaitGoroutines_TimeoutCleansUpOwnFiles(t *testing.T) {
	tests := []struct {
		name          string
		registeredPID func() int
		wantRemoved   bool
	}{
		{
			name:          "registry names this process: files are ours and are removed",
			registeredPID: os.Getpid,
			wantRemoved:   true,
		},
		{
			// A replacement daemon registered under the same workspace ID and
			// bound the same socket path while this one was draining
			// (ox-6bo3). Removing the path would strand the live replacement.
			name:          "registry names a newer daemon: its files are left alone",
			registeredPID: func() int { return os.Getpid() + 99999 },
			wantRemoved:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isolateDaemonState(t)
			seedDaemonFiles(t, tt.registeredPID())

			d := New(nil, nil)
			d.running = true

			neverDone := make(chan struct{}) // a goroutine that never finishes
			err := d.awaitGoroutines(neverDone, 20*time.Millisecond)

			require.ErrorIs(t, err, ErrShutdownTimeout)
			assert.False(t, d.running, "daemon must not report running after shutdown timeout")

			_, sockErr := os.Stat(SocketPath())
			_, pidErr := os.Stat(PidPath())
			reg, regErr := LoadRegistry()
			require.NoError(t, regErr)
			entry := reg.FindByWorkspaceID(CurrentWorkspaceID())

			if tt.wantRemoved {
				assert.True(t, os.IsNotExist(sockErr), "socket file must be removed on shutdown timeout")
				assert.True(t, os.IsNotExist(pidErr), "PID file must be removed on shutdown timeout")
				assert.Nil(t, entry, "registry entry must be removed on shutdown timeout")
				return
			}
			assert.NoError(t, sockErr, "a newer daemon's socket file must survive")
			assert.NoError(t, pidErr, "a newer daemon's PID file must survive")
			if assert.NotNil(t, entry, "a newer daemon's registry entry must survive") {
				assert.Equal(t, os.Getpid()+99999, entry.PID)
			}
		})
	}
}

// TestAwaitGoroutines_GracefulStillCleansUp keeps the original contract: a
// drain that finishes in time cleans up and reports success.
func TestAwaitGoroutines_GracefulStillCleansUp(t *testing.T) {
	isolateDaemonState(t)
	seedDaemonFiles(t, os.Getpid())

	d := New(nil, nil)
	d.running = true

	done := make(chan struct{})
	close(done)
	require.NoError(t, d.awaitGoroutines(done, time.Second))

	assert.False(t, d.running)
	_, err := os.Stat(SocketPath())
	assert.True(t, os.IsNotExist(err), "graceful shutdown must remove the socket file")
}

// TestMaxGracefulShutdown_CoversBothDrainWaits ties the exported budget to the
// waits shutdown() actually performs, so a CLI that derives its patience from
// it cannot silently fall behind the daemon.
func TestMaxGracefulShutdown_CoversBothDrainWaits(t *testing.T) {
	assert.Equal(t, shutdownCodeDBDrainTimeout+shutdownGoroutineWaitTimeout, MaxGracefulShutdown)
	assert.GreaterOrEqual(t, MaxGracefulShutdown, 35*time.Second,
		"the documented worst case is a 30s CodeDB drain plus a 5s goroutine wait")
}
