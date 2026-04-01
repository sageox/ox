package daemon

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
