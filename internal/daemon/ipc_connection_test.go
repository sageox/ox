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

func TestNewServer(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	s := NewServer(logger)

	assert.NotNil(t, s)
	assert.Equal(t, logger, s.logger)
	assert.False(t, s.startTime.IsZero())
}

func TestServer_SetHandlers(t *testing.T) {
	s := NewServer(nil)

	s.SetHandlers(
		func() error { return nil },
		func() {},
		func() *StatusData { return nil },
	)

	// verify handlers are set on the callback service
	s.svc.mu.Lock()
	assert.NotNil(t, s.svc.onSync)
	assert.NotNil(t, s.svc.onStop)
	assert.NotNil(t, s.svc.onStatus)
	s.svc.mu.Unlock()
}

func TestNewDirectClient(t *testing.T) {
	c := newDirectClient()

	assert.NotNil(t, c)
	assert.Equal(t, SocketPath(), c.socketPath)
	assert.Equal(t, 50*time.Millisecond, c.timeout) // fast timeout for localhost
}

func TestNewDirectClientWithTimeout(t *testing.T) {
	c := newDirectClientWithTimeout(10 * time.Second)

	assert.NotNil(t, c)
	assert.Equal(t, SocketPath(), c.socketPath)
	assert.Equal(t, 10*time.Second, c.timeout)
}

func TestClient_Connect_DaemonNotRunning(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("XDG_RUNTIME_DIR", tmpDir)

	c := newDirectClient()
	conn, err := c.Connect()

	assert.Error(t, err)
	assert.Nil(t, conn)
	assert.Contains(t, err.Error(), "connect to daemon")
}

func TestTryConnect_DaemonNotRunning(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("XDG_RUNTIME_DIR", tmpDir)

	client := TryConnect()
	assert.Nil(t, client)
}

func TestIsHealthy_DaemonNotRunning(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("XDG_RUNTIME_DIR", tmpDir)

	err := IsHealthy()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "daemon not running")
}

func TestIsHealthy_DaemonHung(t *testing.T) {
	// Use /tmp directly to avoid long socket paths (Unix socket path limit ~104 chars)
	tmpDir := "/tmp"
	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("XDG_RUNTIME_DIR", tmpDir)

	// Create socket that accepts but never responds (simulates hung daemon)
	socketPath := SocketPath()
	listener, err := listen(socketPath)
	require.NoError(t, err)
	defer listener.Close()
	defer cleanupSocket(socketPath)

	// Accept connections but never respond
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			// Hold connection open but never respond - simulates hung daemon
			time.Sleep(10 * time.Second)
			conn.Close()
		}
	}()

	// IsHealthy should fail with "not responsive" (times out waiting for ping response)
	err = IsHealthy()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "daemon not responsive")
}

func TestIsHealthy_DaemonHealthy(t *testing.T) {
	// Use /tmp directly to avoid long socket paths
	tmpDir := "/tmp"
	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("XDG_RUNTIME_DIR", tmpDir)

	// Start a real server that responds to pings
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	server := NewServer(logger)
	server.SetHandlers(
		func() error { return nil },
		func() {},
		func() *StatusData { return &StatusData{Running: true} },
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errChan := make(chan error, 1)
	go func() {
		errChan <- server.Start(ctx)
	}()

	// Wait for server to be ready
	time.Sleep(100 * time.Millisecond)

	// IsHealthy should succeed
	err := IsHealthy()
	assert.NoError(t, err)

	cancel()
	<-errChan
}

// Test that client handles unresponsive server
func TestClient_Timeout(t *testing.T) {
	// use /tmp directly to avoid long socket paths (Unix socket path limit ~104 chars)
	tmpDir := "/tmp"
	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("XDG_RUNTIME_DIR", tmpDir)

	// create socket file but don't serve
	socketPath := SocketPath()
	listener, err := listen(socketPath)
	require.NoError(t, err)
	defer listener.Close()
	defer cleanupSocket(socketPath)

	// accept but don't respond
	go func() {
		conn, _ := listener.Accept()
		if conn != nil {
			time.Sleep(10 * time.Second) // simulate unresponsive
			conn.Close()
		}
	}()

	client := &Client{
		socketPath: socketPath,
		timeout:    100 * time.Millisecond, // short timeout
	}

	_, err = client.sendMessage(Message{Type: MsgTypePing})
	assert.Error(t, err)
}

// TestServer_GracefulShutdown_WaitsForInflightConnections tests that the server
// waits for in-flight connection handlers to complete before returning.
// This catches regressions in the WaitGroup-based connection tracking.
func TestServer_GracefulShutdown_WaitsForInflightConnections(t *testing.T) {
	tmpDir, err := os.MkdirTemp("/tmp", "ox-ipc-graceful-")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(tmpDir) })
	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("XDG_RUNTIME_DIR", tmpDir)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	server := NewServer(logger)

	// track when sync handler starts and completes
	handlerStarted := make(chan struct{})
	handlerComplete := make(chan struct{})

	server.SetSyncHandler(func(progress *ProgressWriter) error {
		close(handlerStarted)
		// simulate slow operation
		time.Sleep(200 * time.Millisecond)
		close(handlerComplete)
		return nil
	})

	server.SetHandlers(
		func() error { return nil },
		func() {},
		func() *StatusData { return &StatusData{Running: true} },
	)

	ctx, cancel := context.WithCancel(context.Background())

	serverDone := make(chan struct{})
	go func() {
		server.Start(ctx)
		close(serverDone)
	}()
	time.Sleep(100 * time.Millisecond)

	// start a slow sync request
	go func() {
		client := newDirectClientWithTimeout(5 * time.Second)
		_ = client.SyncWithProgress(nil)
	}()

	// wait for handler to start
	<-handlerStarted

	// cancel context (trigger shutdown) while handler is in progress
	cancel()

	// server should wait for handler to complete
	select {
	case <-serverDone:
		// verify handler actually completed
		select {
		case <-handlerComplete:
			// good - handler completed before server returned
		default:
			t.Fatal("server returned before handler completed - WaitGroup not working")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server shutdown timed out")
	}
}

// TestServerClient_ConcurrentStatusRequests verifies that multiple concurrent status
// calls are served in parallel, not serialized.
func TestServerClient_ConcurrentStatusRequests(t *testing.T) {
	tmpDir, err := os.MkdirTemp("/tmp", "ox-ipc-concstatus-")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(tmpDir) })
	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("XDG_RUNTIME_DIR", tmpDir)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	server := NewServer(logger)

	// status handler takes 50ms (simulating light work)
	server.SetHandlers(
		func() error { return nil },
		func() {},
		func() *StatusData {
			time.Sleep(50 * time.Millisecond)
			return &StatusData{Running: true}
		},
	)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	go server.Start(ctx)
	time.Sleep(100 * time.Millisecond)

	// send 10 concurrent status requests
	const numRequests = 10
	results := make(chan time.Duration, numRequests)

	overallStart := time.Now()
	for i := 0; i < numRequests; i++ {
		go func() {
			client := &Client{socketPath: SocketPath(), timeout: 5 * time.Second}
			start := time.Now()
			_, err := client.Status()
			if err != nil {
				results <- -1
				return
			}
			results <- time.Since(start)
		}()
	}

	// collect all results
	for i := 0; i < numRequests; i++ {
		d := <-results
		assert.Greater(t, d, time.Duration(0), "request %d should succeed", i)
	}
	overallElapsed := time.Since(overallStart)

	// if serialized: 10 * 50ms = 500ms minimum
	// if parallel: ~50ms + overhead
	// allow generous margin but catch serialization
	assert.Less(t, overallElapsed, 500*time.Millisecond,
		"10 concurrent status requests should complete in <500ms if parallel (took %v); serialized would take 500ms+", overallElapsed)
}
