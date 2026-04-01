package daemon

import (
	"context"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Integration test: Server and Client communication
func TestServerClient_Integration(t *testing.T) {
	// use /tmp directly to avoid long socket paths (Unix socket path limit ~104 chars)
	tmpDir := "/tmp"
	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("XDG_RUNTIME_DIR", tmpDir)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	server := NewServer(logger)

	// set up handlers
	syncCount := 0
	server.SetHandlers(
		func() error { syncCount++; return nil },
		func() {},
		func() *StatusData {
			return &StatusData{
				Running:    true,
				Pid:        os.Getpid(),
				LedgerPath: "/test/ledger",
			}
		},
	)

	// start server in background
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	serverErr := make(chan error, 1)
	go func() {
		serverErr <- server.Start(ctx)
	}()

	// wait for server to start
	time.Sleep(100 * time.Millisecond)

	// test ping
	t.Run("ping", func(t *testing.T) {
		client := newDirectClient()
		err := client.Ping()
		assert.NoError(t, err)
	})

	// test status
	t.Run("status", func(t *testing.T) {
		client := newDirectClient()
		status, err := client.Status()
		require.NoError(t, err)
		assert.True(t, status.Running)
		assert.Equal(t, "/test/ledger", status.LedgerPath)
	})

	// test sync
	t.Run("sync", func(t *testing.T) {
		client := newDirectClient()
		err := client.RequestSync()
		assert.NoError(t, err)
		assert.Equal(t, 1, syncCount)
	})

	// test stop
	t.Run("stop", func(t *testing.T) {
		client := newDirectClient()
		err := client.Stop()
		assert.NoError(t, err)
	})

	cancel()

	// wait for server to stop
	select {
	case err := <-serverErr:
		assert.ErrorIs(t, err, context.Canceled)
	case <-time.After(2 * time.Second):
		t.Fatal("server didn't stop in time")
	}
}

// Test multiple concurrent client requests
func TestServerClient_ConcurrentRequests(t *testing.T) {
	// use /tmp directly to avoid long socket paths
	tmpDir := "/tmp"
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
	defer cancel()

	go server.Start(ctx)
	time.Sleep(100 * time.Millisecond)

	// send concurrent requests
	const numRequests = 20
	results := make(chan error, numRequests)

	for i := 0; i < numRequests; i++ {
		go func() {
			client := newDirectClient()
			results <- client.Ping()
		}()
	}

	// all should succeed
	for i := 0; i < numRequests; i++ {
		err := <-results
		assert.NoError(t, err)
	}

	cancel()
}

// TestServer_ConcurrentConnections_RaceDetector tests for race conditions
// when multiple clients connect concurrently. Run with -race flag.
func TestServer_ConcurrentConnections_RaceDetector(t *testing.T) {
	tmpDir, err := os.MkdirTemp("/tmp", "ox-ipc-race-")
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
	defer cancel()

	go server.Start(ctx)
	time.Sleep(100 * time.Millisecond)

	// spawn many concurrent clients accessing shared server state
	const numClients = 50
	done := make(chan struct{}, numClients)

	for i := 0; i < numClients; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			client := newDirectClient()
			// mix of operations that access different server handlers
			_ = client.Ping()
			_, _ = client.Status()
		}()
	}

	// wait for all to complete (or timeout)
	for i := 0; i < numClients; i++ {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatalf("client %d timed out", i)
		}
	}
}

// TestServerClient_PingDuringSlowStatus verifies that ping (health check) is not blocked
// by a slow status handler running on a different connection.
func TestServerClient_PingDuringSlowStatus(t *testing.T) {
	tmpDir, err := os.MkdirTemp("/tmp", "ox-ipc-pingblock-")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(tmpDir) })
	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("XDG_RUNTIME_DIR", tmpDir)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	server := NewServer(logger)

	// status handler with artificial 2s delay (simulates blocked SQLite query)
	statusStarted := make(chan struct{})
	var statusOnce sync.Once
	server.SetHandlers(
		func() error { return nil },
		func() {},
		func() *StatusData {
			statusOnce.Do(func() { close(statusStarted) })
			time.Sleep(2 * time.Second)
			return &StatusData{Running: true}
		},
	)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	go server.Start(ctx)
	time.Sleep(100 * time.Millisecond)

	// start slow status request in background
	go func() {
		client := &Client{socketPath: SocketPath(), timeout: 5 * time.Second}
		_, _ = client.Status()
	}()

	// wait for status handler to be actively running
	<-statusStarted

	// ping must complete quickly despite slow status on another connection
	client := &Client{socketPath: SocketPath(), timeout: 5 * time.Second}
	start := time.Now()
	err = client.Ping()
	elapsed := time.Since(start)

	require.NoError(t, err)
	assert.Less(t, elapsed, 100*time.Millisecond,
		"ping should complete in <100ms during slow status (took %v)", elapsed)
}
