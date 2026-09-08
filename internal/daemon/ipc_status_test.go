package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sageox/ox/internal/codedb"
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

// TestServerClient_StatusWithLockedCodeDB verifies both status IPC handlers stay
// responsive when a cold or failed index has no cached stats and another writer
// holds the database open.
func TestServerClient_StatusWithLockedCodeDB(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real code databases and IPC servers")
	}

	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("XDG_RUNTIME_DIR", recoveryRuntimeDir(t, "ox-ipc-stats-"))
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	for _, tt := range []struct {
		name     string
		indexing bool
		cached   bool
		lastErr  error
	}{
		{name: "cold start"},
		{name: "failed initial index", lastErr: errors.New("code index is corrupt")},
		{name: "active initial index", indexing: true},
		{name: "failed refresh preserves stats", cached: true, lastErr: errors.New("refresh failed")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dataDir := t.TempDir()
			seedCodeDB(t, dataDir)
			// Keep Bleve's real writer lock held throughout the status requests.
			db, err := codedb.Open(dataDir)
			require.NoError(t, err)
			defer db.Close()

			cfg := DefaultConfig()
			cfg.ProjectRoot = t.TempDir()
			d := New(cfg, nil)
			d.scheduler = NewSyncScheduler(cfg, d.logger)
			d.codedb = NewCodeDBManager(cfg.ProjectRoot, d.logger, nil)
			d.codedb.dataDir = dataDir
			d.codedb.indexing = tt.indexing
			d.codedb.lastErr = tt.lastErr
			d.codedb.ledgerStats = CodeDBStats{IndexExists: true, Commits: 7}
			if tt.cached {
				d.codedb.stats = queryStatsFromDB(db, dataDir)
				d.codedb.lastIndex = time.Now().Add(-time.Minute).UTC()
			}

			server := NewServerWithService(d.logger, &daemonServiceImpl{d: d})
			stop := startRecoveryTestServer(t, server)
			defer func() {
				// Release a blocked old implementation before draining handlers.
				require.NoError(t, db.Close())
				stop()
			}()

			client := &Client{socketPath: SocketPath(), timeout: 250 * time.Millisecond}
			for range 2 {
				status, err := client.Status()
				require.NoError(t, err, "daemon status must not wait for the code database")
				require.NotNil(t, status.CodeDB)
				assert.True(t, status.Running)
				codeStatus, err := client.CodeStatus()
				require.NoError(t, err, "code status must not wait for the code database")
				assert.Equal(t, status.CodeDB, codeStatus)
				assert.True(t, codeStatus.IndexExists)
				assert.Equal(t, tt.indexing, codeStatus.IndexingNow)
				assert.Equal(t, dataDir, codeStatus.DataDir)
				assert.True(t, codeStatus.LedgerExists)
				assert.Equal(t, 7, codeStatus.LedgerCommits)
				if tt.lastErr != nil {
					assert.Equal(t, tt.lastErr.Error(), codeStatus.LastError)
				} else {
					assert.Empty(t, codeStatus.LastError)
				}
				if tt.cached {
					assert.Equal(t, 3, codeStatus.Commits)
					assert.Len(t, codeStatus.Repos, 2)
					assert.Equal(t, d.codedb.lastIndex, codeStatus.LastIndexed)
				}
			}
		})
	}
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

	stop := startRecoveryTestServer(t, server)
	defer stop()

	// send 100 concurrent status requests
	const numRequests = 100
	done := make(chan error, numRequests)

	overallStart := time.Now()
	for i := 0; i < numRequests; i++ {
		go func() {
			client := &Client{socketPath: SocketPath(), timeout: 5 * time.Second}
			_, err := client.Status()
			done <- err
		}()
	}

	// wait for all to complete
	for i := 0; i < numRequests; i++ {
		select {
		case err := <-done:
			require.NoError(t, err, "status request failed")
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
