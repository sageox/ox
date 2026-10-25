package daemon

import (
	"context"
	"log/slog"
	"os"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func startRecoveryTestServer(t *testing.T, server *Server) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Start(ctx) }()

	client := &Client{socketPath: SocketPath(), timeout: time.Second}
	require.Eventually(t, func() bool { return client.Ping() == nil },
		2*time.Second, 10*time.Millisecond, "server socket was not ready")

	return func() {
		cancel()
		select {
		case err := <-done:
			require.ErrorIs(t, err, context.Canceled)
		case <-time.After(2 * time.Second):
			t.Fatal("server did not stop after cancellation")
		}
	}
}

func recoveryRuntimeDir(t *testing.T, pattern string) string {
	t.Helper()
	root := os.TempDir()
	if runtime.GOOS != "windows" {
		root = "/tmp" // keep Unix socket paths below macOS's small limit
	}
	dir, err := os.MkdirTemp(root, pattern)
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// TestSyncFinalResponseReplacesProgressDeadline protects the distinction
// between best-effort progress and the authoritative final response. A progress
// update installs a 100ms write deadline. Real sync work can continue longer
// than that after its last update, so the server must replace the expired
// deadline before writing success or failure.
func TestSyncFinalResponseReplacesProgressDeadline(t *testing.T) {
	runtimeDir := recoveryRuntimeDir(t, "ox-ipc-final-")
	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)

	server := NewServer(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))
	server.SetHandlers(func() error { return nil }, func() {}, func() *StatusData {
		return &StatusData{Running: true}
	})
	server.SetSyncHandler(func(progress *ProgressWriter) error {
		if err := progress.WriteStage("fetching", "remote contacted"); err != nil {
			return err
		}
		// Expire ProgressWriter's short deadline before returning the final
		// result. Without sendResponse resetting it, the client never sees OK.
		time.Sleep(150 * time.Millisecond)
		return nil
	})
	stop := startRecoveryTestServer(t, server)
	defer stop()

	client := &Client{socketPath: SocketPath(), timeout: 2 * time.Second}
	var stages []string
	err := client.SyncWithProgress(func(stage string, _ *int, _ string) {
		stages = append(stages, stage)
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"fetching"}, stages)
}

// TestSyncDroppedConnectionRestartAndRetry is the real socket boundary for a
// CLI disappearing mid-sync. The abandoned handler must finish without
// poisoning server shutdown, and a replacement daemon must accept a retry and
// report progress normally.
func TestSyncDroppedConnectionRestartAndRetry(t *testing.T) {
	runtimeDir := recoveryRuntimeDir(t, "ox-ipc-retry-")
	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	first := NewServer(logger)
	first.SetHandlers(func() error { return nil }, func() {}, func() *StatusData {
		return &StatusData{Running: true}
	})
	release := make(chan struct{})
	finished := make(chan struct{})
	first.SetSyncHandler(func(progress *ProgressWriter) error {
		_ = progress.WriteStage("fetching", "first attempt")
		<-release
		close(finished)
		return nil
	})
	stopFirst := startRecoveryTestServer(t, first)

	client := &Client{socketPath: SocketPath(), timeout: 75 * time.Millisecond}
	err := client.SyncWithProgress(nil)
	require.Error(t, err, "client should disconnect when progress stops")
	require.ErrorIs(t, err, os.ErrDeadlineExceeded)

	close(release)
	require.Eventually(t, func() bool {
		select {
		case <-finished:
			return true
		default:
			return false
		}
	}, time.Second, 10*time.Millisecond, "abandoned handler did not finish")
	stopFirst()

	second := NewServer(logger)
	second.SetHandlers(func() error { return nil }, func() {}, func() *StatusData {
		return &StatusData{Running: true}
	})
	var calls atomic.Int32
	second.SetSyncHandler(func(progress *ProgressWriter) error {
		calls.Add(1)
		_ = progress.WriteStage("complete", "retry succeeded")
		return nil
	})
	stopSecond := startRecoveryTestServer(t, second)
	defer stopSecond()

	client = &Client{socketPath: SocketPath(), timeout: time.Second}
	var stages []string
	require.NoError(t, client.SyncWithProgress(func(stage string, _ *int, _ string) {
		stages = append(stages, stage)
	}))
	assert.Equal(t, int32(1), calls.Load())
	assert.Equal(t, []string{"complete"}, stages)
}
