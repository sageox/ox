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
