package daemon

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
