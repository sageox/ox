package daemon

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestServerStart_DoesNotRemoveSocketOnShutdown verifies that Server.Start
// does NOT unlink the socket file when its context is canceled. Socket-file
// lifetime is owned by Daemon.cleanup, which knows whether the daemon was
// superseded by a replacement (in which case the file at the shared path
// belongs to the new daemon and must be preserved).
//
// Failure prevented: superseded daemon shutdown destroys the replacement
// daemon's socket file, leaving it running but unreachable from CLI
// (manifested as "Murmur not delivered (daemon unavailable)" even though
// the daemon process holds the socket open in the kernel).
func TestServerStart_DoesNotRemoveSocketOnShutdown(t *testing.T) {
	tmpDir, err := os.MkdirTemp("/tmp", "ox-server-shutdown-")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(tmpDir) })
	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("XDG_RUNTIME_DIR", tmpDir)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	server := NewServer(logger)
	server.SetHandlers(
		func() error { return nil },
		func() {},
		func() *StatusData { return &StatusData{Running: true} },
	)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	done := make(chan error, 1)
	go func() { done <- server.Start(ctx) }()

	// wait for the socket to be bound
	socketPath := SocketPath()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(socketPath); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	_, err = os.Stat(socketPath)
	require.NoError(t, err, "server should have created socket file")

	// shut the server down by canceling its context
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("server did not exit after context cancel")
	}

	// invariant: Server.Start must NOT unlink the socket file. Socket
	// removal is Daemon.cleanup's job (which respects wasSuperseded).
	_, err = os.Stat(socketPath)
	assert.NoError(t, err, "socket file must survive Server.Start shutdown (Daemon.cleanup owns removal)")
}

// TestDaemonCleanup_SupersededPreservesSocket verifies the original bug's
// invariant at the Daemon layer: a superseded daemon's cleanup must NOT
// unlink the socket file, because the file at that path belongs to the
// successor daemon.
//
// Failure prevented: regression of the cleanup branch that decides whether
// to remove the socket based on wasSuperseded.
func TestDaemonCleanup_SupersededPreservesSocket(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("XDG_RUNTIME_DIR", tmpDir)

	d := New(nil, nil)
	d.wasSuperseded = true

	require.NoError(t, d.writePidFile())

	socketPath := SocketPath()
	f, err := os.Create(socketPath)
	require.NoError(t, err)
	f.Close()

	d.cleanup()

	_, err = os.Stat(socketPath)
	assert.NoError(t, err, "superseded daemon must NOT unlink the socket file — it now belongs to the successor")
	_, err = os.Stat(PidPath())
	assert.True(t, os.IsNotExist(err) || err == nil, "PID file removal in superseded case is also skipped")
}
