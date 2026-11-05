//go:build !windows

package daemon

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sageox/ox/internal/paths"
	"github.com/stretchr/testify/require"
)

// startPreMigrationServer runs a daemon IPC server on the shared-temp socket an
// older ox used, then switches the environment so that path is "pre-migration".
func startPreMigrationServer(t *testing.T, workspacePath string) *atomic.Int32 {
	t.Helper()
	tmp, err := os.MkdirTemp("", "oxpm")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(tmp) })
	t.Setenv("OX_XDG_DISABLE", "")
	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("TMPDIR", tmp)
	t.Setenv("XDG_RUNTIME_DIR", tmp)
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_STATE_HOME", filepath.Join(tmp, "state"))

	var stops atomic.Int32
	server := NewServer(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))
	server.SetHandlers(func() error { return nil }, func() { stops.Add(1) }, func() *StatusData {
		return &StatusData{Running: true, WorkspacePath: workspacePath}
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = server.Start(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	socket := SocketPath()
	require.Eventually(t, func() bool { _, err := os.Lstat(socket); return err == nil }, 5*time.Second, 10*time.Millisecond)
	t.Setenv("XDG_RUNTIME_DIR", "")
	require.Equal(t, socket, paths.PreMigrationDaemonSocketFile(CurrentWorkspaceID()), "test setup must place the server on the pre-migration socket")
	require.NotEqual(t, socket, SocketPath())
	return &stops
}

func TestStopPreMigrationDaemons_StopsConfirmedWorkspace(t *testing.T) {
	cwd, err := os.Getwd()
	require.NoError(t, err)
	stops := startPreMigrationServer(t, cwd)
	stopPreMigrationDaemons()
	require.Eventually(t, func() bool { return stops.Load() == 1 }, 2*time.Second, 20*time.Millisecond)
}

func TestStopPreMigrationDaemons_LeavesUnconfirmedWorkspace(t *testing.T) {
	stops := startPreMigrationServer(t, "/somewhere/else")
	stopPreMigrationDaemons()
	time.Sleep(100 * time.Millisecond)
	require.Zero(t, stops.Load(), "a daemon reporting another workspace must not be stopped")
}

func TestStopPreMigrationDaemons_IgnoresSymlinkedSocket(t *testing.T) {
	stops := startPreMigrationServer(t, "/somewhere/else")
	socket := paths.PreMigrationDaemonSocketFile(CurrentWorkspaceID())
	real := socket + ".real"
	require.NoError(t, os.Rename(socket, real))
	require.NoError(t, os.Symlink(real, socket))
	stopPreMigrationDaemons()
	require.Zero(t, stops.Load())
}
