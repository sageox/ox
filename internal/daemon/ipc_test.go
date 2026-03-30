package daemon

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMessage_MarshalJSON(t *testing.T) {
	msg := Message{
		Type:    MsgTypePing,
		Payload: json.RawMessage(`{"key":"value"}`),
	}

	data, err := json.Marshal(msg)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"type":"ping"`)
	assert.Contains(t, string(data), `"payload"`)
}

func TestMessage_WithWorkspaceID(t *testing.T) {
	msg := Message{
		Type:        MsgTypeStatus,
		WorkspaceID: "a1b2c3d4",
	}

	data, err := json.Marshal(msg)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"type":"status"`)
	assert.Contains(t, string(data), `"workspace_id":"a1b2c3d4"`)

	// verify round-trip
	var decoded Message
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)
	assert.Equal(t, "a1b2c3d4", decoded.WorkspaceID)
}

func TestMessage_WorkspaceID_OmittedWhenEmpty(t *testing.T) {
	msg := Message{
		Type: MsgTypePing,
	}

	data, err := json.Marshal(msg)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "workspace_id")
}

func TestMessage_CallerID(t *testing.T) {
	t.Run("present", func(t *testing.T) {
		msg := Message{
			Type:        MsgTypeStatus,
			WorkspaceID: "a1b2c3d4",
			CallerID:    "e5f6g7h8",
		}

		data, err := json.Marshal(msg)
		require.NoError(t, err)
		assert.Contains(t, string(data), `"caller_id":"e5f6g7h8"`)

		var decoded Message
		err = json.Unmarshal(data, &decoded)
		require.NoError(t, err)
		assert.Equal(t, "e5f6g7h8", decoded.CallerID)
	})

	t.Run("omitted_when_empty", func(t *testing.T) {
		msg := Message{
			Type: MsgTypePing,
		}

		data, err := json.Marshal(msg)
		require.NoError(t, err)
		assert.NotContains(t, string(data), "caller_id")
	})

	t.Run("backwards_compat_missing_field", func(t *testing.T) {
		// JSON from an older client that doesn't send caller_id
		oldJSON := `{"type":"status","workspace_id":"a1b2c3d4"}`

		var decoded Message
		err := json.Unmarshal([]byte(oldJSON), &decoded)
		require.NoError(t, err)
		assert.Equal(t, "status", decoded.Type)
		assert.Equal(t, "a1b2c3d4", decoded.WorkspaceID)
		assert.Empty(t, decoded.CallerID, "missing caller_id should default to empty string")
	})

	t.Run("backwards_compat_old_worktree_id", func(t *testing.T) {
		// JSON from an older client that sends worktree_id (old name)
		// should be silently ignored — CallerID stays empty
		oldJSON := `{"type":"status","workspace_id":"a1b2c3d4","worktree_id":"e5f6g7h8"}`

		var decoded Message
		err := json.Unmarshal([]byte(oldJSON), &decoded)
		require.NoError(t, err)
		assert.Empty(t, decoded.CallerID, "old worktree_id field should not populate CallerID")
	})
}

func TestResponse_MarshalJSON(t *testing.T) {
	resp := Response{
		Success: true,
		Data:    json.RawMessage(`"pong"`),
	}

	data, err := json.Marshal(resp)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"success":true`)
}

func TestResponse_WithError(t *testing.T) {
	resp := Response{
		Success: false,
		Error:   "something went wrong",
	}

	data, err := json.Marshal(resp)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"success":false`)
	assert.Contains(t, string(data), `"error":"something went wrong"`)
}

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

func TestStatusData_JSON(t *testing.T) {
	status := &StatusData{
		Running:          true,
		Pid:              12345,
		Uptime:           time.Hour,
		LedgerPath:       "/path/to/ledger",
		LastSync:         time.Now(),
		SyncIntervalRead: 15 * time.Minute,
	}

	data, err := json.Marshal(status)
	require.NoError(t, err)

	var decoded StatusData
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, status.Running, decoded.Running)
	assert.Equal(t, status.Pid, decoded.Pid)
	assert.Equal(t, status.LedgerPath, decoded.LedgerPath)
}

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

func TestCheckoutPayload_JSON(t *testing.T) {
	payload := CheckoutPayload{
		RepoPath: "/path/to/repo",
		CloneURL: "https://github.com/example/repo.git",
		RepoType: "ledger",
	}

	data, err := json.Marshal(payload)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"repo_path":"/path/to/repo"`)
	assert.Contains(t, string(data), `"clone_url":"https://github.com/example/repo.git"`)
	assert.Contains(t, string(data), `"repo_type":"ledger"`)

	var decoded CheckoutPayload
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)
	assert.Equal(t, payload, decoded)
}

func TestCheckoutResult_JSON(t *testing.T) {
	result := CheckoutResult{
		Path:          "/path/to/repo",
		AlreadyExists: false,
		Cloned:        true,
	}

	data, err := json.Marshal(result)
	require.NoError(t, err)

	var decoded CheckoutResult
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)
	assert.Equal(t, result, decoded)
}

func TestProgressResponse_WithProgress(t *testing.T) {
	percent := 50
	resp := ProgressResponse{
		Progress: &CheckoutProgress{
			Stage:   "cloning",
			Percent: &percent,
			Message: "Cloning repository...",
		},
	}

	data, err := json.Marshal(resp)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"progress"`)
	assert.Contains(t, string(data), `"stage":"cloning"`)
	assert.Contains(t, string(data), `"percent":50`)
}

func TestProgressResponse_WithoutPercent(t *testing.T) {
	resp := ProgressResponse{
		Progress: &CheckoutProgress{
			Stage:   "connecting",
			Message: "Establishing connection...",
		},
	}

	data, err := json.Marshal(resp)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"stage":"connecting"`)
	assert.NotContains(t, string(data), `"percent"`) // omitted when nil
}

func TestProgressResponse_Final(t *testing.T) {
	resultData, _ := json.Marshal(CheckoutResult{Path: "/repo", Cloned: true})
	resp := ProgressResponse{
		Success: true,
		Data:    resultData,
	}

	data, err := json.Marshal(resp)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"success":true`)
	assert.NotContains(t, string(data), `"progress"`) // omitempty
}

func TestServer_SetCheckoutHandler(t *testing.T) {
	s := NewServer(nil)

	s.SetCheckoutHandler(func(payload CheckoutPayload, progress *ProgressWriter) (*CheckoutResult, error) {
		return &CheckoutResult{Path: payload.RepoPath, Cloned: true}, nil
	})

	s.svc.mu.Lock()
	assert.NotNil(t, s.svc.onCheckout)
	s.svc.mu.Unlock()
}

// Integration test: Checkout with progress streaming
func TestServerClient_Checkout_Integration(t *testing.T) {
	// use unique temp dir to avoid socket conflicts with parallel tests
	tmpDir, err := os.MkdirTemp("/tmp", "ox-ipc-checkout-int-")
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

	// mock checkout handler that sends progress
	server.SetCheckoutHandler(func(payload CheckoutPayload, progress *ProgressWriter) (*CheckoutResult, error) {
		// send progress updates
		if progress != nil {
			_ = progress.WriteProgress("connecting", 0, "Connecting...")
			_ = progress.WriteProgress("cloning", 50, "Cloning...")
			_ = progress.WriteProgress("verifying", 90, "Verifying...")
		}
		return &CheckoutResult{
			Path:   payload.RepoPath,
			Cloned: true,
		}, nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go server.Start(ctx)
	time.Sleep(100 * time.Millisecond)

	// test checkout with progress callback (use socket path matching server)
	client := &Client{
		socketPath: SocketPath(),
		timeout:    5 * time.Second,
	}

	progressUpdates := []string{}
	result, err := client.Checkout(
		CheckoutPayload{
			RepoPath: "/test/repo",
			CloneURL: "https://example.com/repo.git",
			RepoType: "ledger",
		},
		func(stage string, percent *int, message string) {
			progressUpdates = append(progressUpdates, stage)
		},
	)

	require.NoError(t, err)
	assert.NotNil(t, result)
	assert.Equal(t, "/test/repo", result.Path)
	assert.True(t, result.Cloned)

	// verify progress was received
	assert.Len(t, progressUpdates, 3)
	assert.Equal(t, "connecting", progressUpdates[0])
	assert.Equal(t, "cloning", progressUpdates[1])
	assert.Equal(t, "verifying", progressUpdates[2])

	cancel()
}

// Test checkout when handler returns error
func TestServerClient_Checkout_Error(t *testing.T) {
	// use unique temp dir to avoid socket conflicts with parallel tests
	tmpDir, err := os.MkdirTemp("/tmp", "ox-ipc-checkout-err-")
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

	server.SetCheckoutHandler(func(payload CheckoutPayload, progress *ProgressWriter) (*CheckoutResult, error) {
		return nil, assert.AnError
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go server.Start(ctx)
	time.Sleep(100 * time.Millisecond)

	client := &Client{
		socketPath: SocketPath(),
		timeout:    5 * time.Second,
	}
	result, err := client.Checkout(
		CheckoutPayload{RepoPath: "/test/repo", CloneURL: "https://example.com/repo.git"},
		nil,
	)

	assert.Error(t, err)
	assert.Nil(t, result)

	cancel()
}

// Test checkout without handler set
func TestServerClient_Checkout_NoHandler(t *testing.T) {
	// use unique temp dir to avoid socket conflicts with parallel tests
	tmpDir, err := os.MkdirTemp("/tmp", "ox-ipc-checkout-nohandler-")
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
	// deliberately not setting checkout handler

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go server.Start(ctx)
	time.Sleep(100 * time.Millisecond)

	// create client with socket path matching server (using XDG_RUNTIME_DIR)
	client := &Client{
		socketPath: SocketPath(),
		timeout:    5 * time.Second,
	}
	result, err := client.Checkout(
		CheckoutPayload{RepoPath: "/test/repo", CloneURL: "https://example.com/repo.git"},
		nil,
	)

	assert.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "checkout handler not set")

	cancel()
}

func TestServer_SetSyncHandler(t *testing.T) {
	s := NewServer(nil)

	s.SetSyncHandler(func(progress *ProgressWriter) error {
		if progress != nil {
			_ = progress.WriteStage("fetching", "Fetching...")
		}
		return nil
	})

	s.svc.mu.Lock()
	assert.NotNil(t, s.svc.onSyncWithProgress)
	s.svc.mu.Unlock()
}

// Integration test: Sync with progress streaming
func TestServerClient_SyncWithProgress_Integration(t *testing.T) {
	// use short path to avoid Unix socket 104-char limit on macOS
	tmpDir, err := os.MkdirTemp("/tmp", "ox-ipc-")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(tmpDir) })
	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("XDG_RUNTIME_DIR", tmpDir)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	server := NewServer(logger)

	server.SetHandlers(
		func() error { return nil }, // legacy handler (ignored when progress handler set)
		func() {},
		func() *StatusData { return &StatusData{Running: true} },
	)

	// set sync handler with progress
	server.SetSyncHandler(func(progress *ProgressWriter) error {
		if progress != nil {
			_ = progress.WriteStage("fetching", "Fetching from remote...")
			_ = progress.WriteStage("pulling", "Pulling changes...")
			_ = progress.WriteStage("checking", "Checking for local changes...")
			_ = progress.WriteStage("skipped", "No changes to push")
		}
		return nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go server.Start(ctx)
	time.Sleep(100 * time.Millisecond)

	client := &Client{
		socketPath: SocketPath(),
		timeout:    5 * time.Second,
	}

	progressUpdates := []string{}
	err = client.SyncWithProgress(func(stage string, percent *int, message string) {
		progressUpdates = append(progressUpdates, stage)
	})

	require.NoError(t, err)

	// verify progress was received
	assert.Len(t, progressUpdates, 4)
	assert.Equal(t, "fetching", progressUpdates[0])
	assert.Equal(t, "pulling", progressUpdates[1])
	assert.Equal(t, "checking", progressUpdates[2])
	assert.Equal(t, "skipped", progressUpdates[3])

	cancel()
}

// Test sync with progress when handler returns error
func TestServerClient_SyncWithProgress_Error(t *testing.T) {
	// use short path to avoid Unix socket 104-char limit on macOS
	tmpDir, err := os.MkdirTemp("/tmp", "ox-ipc-")
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

	server.SetSyncHandler(func(progress *ProgressWriter) error {
		if progress != nil {
			_ = progress.WriteStage("fetching", "Fetching...")
		}
		return assert.AnError
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go server.Start(ctx)
	time.Sleep(100 * time.Millisecond)

	client := &Client{
		socketPath: SocketPath(),
		timeout:    5 * time.Second,
	}

	progressUpdates := []string{}
	err = client.SyncWithProgress(func(stage string, percent *int, message string) {
		progressUpdates = append(progressUpdates, stage)
	})

	assert.Error(t, err)
	// should still have received progress before error
	assert.Equal(t, []string{"fetching"}, progressUpdates)

	cancel()
}

// Test sync falls back to legacy handler when progress handler not set
func TestServerClient_SyncWithProgress_LegacyFallback(t *testing.T) {
	// use short path to avoid Unix socket 104-char limit on macOS
	tmpDir, err := os.MkdirTemp("/tmp", "ox-ipc-")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(tmpDir) })
	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("XDG_RUNTIME_DIR", tmpDir)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	server := NewServer(logger)

	legacySyncCalled := false
	server.SetHandlers(
		func() error { legacySyncCalled = true; return nil },
		func() {},
		func() *StatusData { return &StatusData{Running: true} },
	)
	// deliberately not setting progress handler

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go server.Start(ctx)
	time.Sleep(100 * time.Millisecond)

	client := &Client{
		socketPath: SocketPath(),
		timeout:    5 * time.Second,
	}

	progressUpdates := []string{}
	err = client.SyncWithProgress(func(stage string, percent *int, message string) {
		progressUpdates = append(progressUpdates, stage)
	})

	require.NoError(t, err)
	assert.True(t, legacySyncCalled, "legacy handler should be called")
	assert.Empty(t, progressUpdates, "no progress expected from legacy handler")

	cancel()
}

// TestSyncWithProgress_IdleTimeoutExtendsOnProgress verifies that the idle
// timeout resets on each progress message. A short idle timeout (500ms) would
// expire if not reset, but progress messages every 300ms keep it alive.
func TestSyncWithProgress_IdleTimeoutExtendsOnProgress(t *testing.T) {
	tmpDir, err := os.MkdirTemp("/tmp", "ox-ipc-idle-")
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

	// handler emits 4 progress messages with 300ms gaps = 1.2s total,
	// which exceeds the 500ms idle timeout — only works if the deadline resets.
	server.SetSyncHandler(func(progress *ProgressWriter) error {
		stages := []string{"stage1", "stage2", "stage3", "stage4"}
		for _, s := range stages {
			time.Sleep(300 * time.Millisecond)
			if progress != nil {
				_ = progress.WriteStage(s, "working...")
			}
		}
		return nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go server.Start(ctx)
	time.Sleep(100 * time.Millisecond)

	client := &Client{
		socketPath: SocketPath(),
		timeout:    500 * time.Millisecond, // short idle timeout
	}

	var progressUpdates []string
	err = client.SyncWithProgress(func(stage string, percent *int, message string) {
		progressUpdates = append(progressUpdates, stage)
	})

	require.NoError(t, err, "should succeed — progress resets the idle timeout")
	assert.Equal(t, []string{"stage1", "stage2", "stage3", "stage4"}, progressUpdates)

	cancel()
}

// TestSyncWithProgress_IdleTimeoutExpiresWithoutProgress verifies that the
// connection times out when no progress is received within the idle period.
func TestSyncWithProgress_IdleTimeoutExpiresWithoutProgress(t *testing.T) {
	tmpDir, err := os.MkdirTemp("/tmp", "ox-ipc-idle-")
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

	// handler stalls for 2s without sending any progress — should trigger timeout
	server.SetSyncHandler(func(progress *ProgressWriter) error {
		time.Sleep(2 * time.Second)
		return nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go server.Start(ctx)
	time.Sleep(100 * time.Millisecond)

	client := &Client{
		socketPath: SocketPath(),
		timeout:    500 * time.Millisecond, // short idle timeout
	}

	err = client.SyncWithProgress(nil)
	assert.Error(t, err, "should timeout — no progress to reset deadline")

	cancel()
}

func TestServer_SetTeamSyncHandler(t *testing.T) {
	s := NewServer(nil)

	s.SetTeamSyncHandler(func(progress *ProgressWriter) error {
		if progress != nil {
			_ = progress.WriteStage("syncing", "Syncing teams...")
		}
		return nil
	})

	s.svc.mu.Lock()
	assert.NotNil(t, s.svc.onTeamSync)
	s.svc.mu.Unlock()
}

// Integration test: Team sync with progress streaming
func TestServerClient_TeamSyncWithProgress_Integration(t *testing.T) {
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

	// set team sync handler with progress
	server.SetTeamSyncHandler(func(progress *ProgressWriter) error {
		if progress != nil {
			_ = progress.WriteStage("starting", "Syncing 2 team context(s)...")
			_ = progress.WriteStage("syncing", "Syncing team: Backend")
			_ = progress.WriteStage("synced", "Team Backend synced")
			_ = progress.WriteStage("syncing", "Syncing team: Frontend")
			_ = progress.WriteStage("synced", "Team Frontend synced")
			_ = progress.WriteStage("complete", "Synced 2, skipped 0 team context(s)")
		}
		return nil
	})

	ctx, cancel := context.WithCancel(context.Background())

	serverDone := make(chan struct{})
	go func() {
		server.Start(ctx)
		close(serverDone)
	}()
	time.Sleep(100 * time.Millisecond)

	client := &Client{
		socketPath: SocketPath(),
		timeout:    5 * time.Second,
	}

	progressUpdates := []string{}
	err := client.TeamSyncWithProgress(func(stage string, percent *int, message string) {
		progressUpdates = append(progressUpdates, stage)
	})

	require.NoError(t, err)

	// verify progress was received
	assert.Len(t, progressUpdates, 6)
	assert.Equal(t, "starting", progressUpdates[0])
	assert.Equal(t, "syncing", progressUpdates[1])
	assert.Equal(t, "synced", progressUpdates[2])
	assert.Equal(t, "syncing", progressUpdates[3])
	assert.Equal(t, "synced", progressUpdates[4])
	assert.Equal(t, "complete", progressUpdates[5])

	cancel()
	<-serverDone // wait for server to fully shut down and clean up socket
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

// Test team sync when handler not set
func TestServerClient_TeamSyncWithProgress_NoHandler(t *testing.T) {
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
	// deliberately not setting team sync handler

	ctx, cancel := context.WithCancel(context.Background())

	serverDone := make(chan struct{})
	go func() {
		server.Start(ctx)
		close(serverDone)
	}()
	time.Sleep(100 * time.Millisecond)

	client := &Client{
		socketPath: SocketPath(),
		timeout:    5 * time.Second,
	}

	err := client.TeamSyncWithProgress(nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "team sync handler not set")

	cancel()
	<-serverDone // wait for server to fully shut down and clean up socket
}

// --- Regression tests: IPC status must remain responsive during long-running operations ---
// These tests verify the architectural guarantee that each IPC connection runs in its own
// goroutine, so a slow handler on one connection cannot block status/ping on another.
// Regression for: ox daemon status timed out because status handler blocked on SQLite during indexing.

// TestServerClient_StatusNonBlocking verifies that status requests complete quickly
// even when another handler (sync) is slow and occupying a different connection.
func TestServerClient_StatusNonBlocking(t *testing.T) {
	tmpDir, err := os.MkdirTemp("/tmp", "ox-ipc-nonblock-")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(tmpDir) })
	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("XDG_RUNTIME_DIR", tmpDir)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	server := NewServer(logger)

	// status handler returns cached data instantly
	server.SetHandlers(
		func() error { return nil },
		func() {},
		func() *StatusData {
			return &StatusData{Running: true, Pid: os.Getpid(), LedgerPath: "/cached"}
		},
	)

	// sync handler simulates a slow operation (2s)
	syncStarted := make(chan struct{})
	server.SetSyncHandler(func(progress *ProgressWriter) error {
		close(syncStarted)
		time.Sleep(2 * time.Second)
		return nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	go server.Start(ctx)
	time.Sleep(100 * time.Millisecond)

	// start slow sync in background
	go func() {
		client := &Client{socketPath: SocketPath(), timeout: 5 * time.Second}
		_ = client.SyncWithProgress(nil)
	}()

	// wait for sync handler to be actively running
	<-syncStarted

	// now send a status request — it must complete quickly despite the slow sync
	client := &Client{socketPath: SocketPath(), timeout: 5 * time.Second}
	start := time.Now()
	status, err := client.Status()
	elapsed := time.Since(start)

	require.NoError(t, err)
	assert.True(t, status.Running)
	assert.Equal(t, "/cached", status.LedgerPath)
	assert.Less(t, elapsed, 100*time.Millisecond,
		"status should complete in <100ms, not be blocked by slow sync (took %v)", elapsed)
}

// TestServerClient_StatusDuringSlowHandler verifies that status does not block when a
// code_index handler is performing heavy work on a different connection.
func TestServerClient_StatusDuringSlowHandler(t *testing.T) {
	tmpDir, err := os.MkdirTemp("/tmp", "ox-ipc-slowidx-")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(tmpDir) })
	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("XDG_RUNTIME_DIR", tmpDir)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	server := NewServer(logger)

	server.SetHandlers(
		func() error { return nil },
		func() {},
		func() *StatusData {
			return &StatusData{Running: true, Pid: os.Getpid()}
		},
	)

	// simulate a slow code_index handler (3s, like SQLite during indexing)
	indexStarted := make(chan struct{})
	server.SetCodeIndexHandler(func(payload CodeIndexPayload, progress *ProgressWriter) (*CodeIndexResult, error) {
		close(indexStarted)
		time.Sleep(3 * time.Second)
		return &CodeIndexResult{BlobsParsed: 100}, nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	go server.Start(ctx)
	time.Sleep(100 * time.Millisecond)

	// start code_index request in background
	go func() {
		client := &Client{socketPath: SocketPath(), timeout: 10 * time.Second}
		payload, _ := json.Marshal(CodeIndexPayload{URL: "/test"})
		_, _ = client.sendMessage(Message{Type: MsgTypeCodeIndex, Payload: payload})
	}()

	// wait for index handler to be actively running
	<-indexStarted

	// status request must complete quickly
	client := &Client{socketPath: SocketPath(), timeout: 5 * time.Second}
	start := time.Now()
	status, err := client.Status()
	elapsed := time.Since(start)

	require.NoError(t, err)
	assert.True(t, status.Running)
	assert.Less(t, elapsed, 200*time.Millisecond,
		"status should complete in <200ms during code indexing (took %v)", elapsed)
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

// TestServer_StatusHandler_NeverBlocks verifies the status handler isolation by calling
// it concurrently many times and ensuring all complete within a reasonable window.
func TestServer_StatusHandler_NeverBlocks(t *testing.T) {
	tmpDir, err := os.MkdirTemp("/tmp", "ox-ipc-statusiso-")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(tmpDir) })
	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("XDG_RUNTIME_DIR", tmpDir)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	server := NewServer(logger)

	var callCount atomic.Int64
	server.SetHandlers(
		func() error { return nil },
		func() {},
		func() *StatusData {
			callCount.Add(1)
			return &StatusData{Running: true}
		},
	)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	go server.Start(ctx)
	time.Sleep(100 * time.Millisecond)

	// send 100 concurrent status requests
	const numRequests = 100
	done := make(chan struct{}, numRequests)

	overallStart := time.Now()
	for i := 0; i < numRequests; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			client := &Client{socketPath: SocketPath(), timeout: 5 * time.Second}
			_, _ = client.Status()
		}()
	}

	// wait for all to complete
	for i := 0; i < numRequests; i++ {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatalf("status request %d timed out after 5s", i)
		}
	}
	overallElapsed := time.Since(overallStart)

	assert.Less(t, overallElapsed, 1*time.Second,
		"100 concurrent status calls should complete in <1s (took %v)", overallElapsed)
	assert.Equal(t, int64(numRequests), callCount.Load(),
		"all %d status handler invocations should have been called", numRequests)
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
