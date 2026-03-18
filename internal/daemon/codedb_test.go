package daemon

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func codedbTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

// --- UpdateProjectRoot ---

func TestUpdateProjectRoot_UpdatesPath(t *testing.T) {
	t.Parallel()
	mgr := NewCodeDBManager("/old/workspace", codedbTestLogger(), nil)

	mgr.UpdateProjectRoot("/new/workspace")

	mgr.mu.Lock()
	got := mgr.projectRoot
	mgr.mu.Unlock()
	assert.Equal(t, "/new/workspace", got)
}

func TestUpdateProjectRoot_IgnoresEmptyPath(t *testing.T) {
	t.Parallel()
	mgr := NewCodeDBManager("/original", codedbTestLogger(), nil)

	mgr.UpdateProjectRoot("")

	mgr.mu.Lock()
	got := mgr.projectRoot
	mgr.mu.Unlock()
	assert.Equal(t, "/original", got)
}

func TestUpdateProjectRoot_IgnoresSamePath(t *testing.T) {
	t.Parallel()
	mgr := NewCodeDBManager("/same/path", codedbTestLogger(), nil)

	mgr.UpdateProjectRoot("/same/path")

	mgr.mu.Lock()
	got := mgr.projectRoot
	mgr.mu.Unlock()
	assert.Equal(t, "/same/path", got)
}

func TestUpdateProjectRoot_ConcurrentUpdates(t *testing.T) {
	t.Parallel()
	mgr := NewCodeDBManager("/initial", codedbTestLogger(), nil)

	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			mgr.UpdateProjectRoot("/workspace-" + string(rune('a'+n%26)))
		}(i)
	}

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("concurrent UpdateProjectRoot deadlocked")
	}

	// should have one of the paths, not be corrupted
	mgr.mu.Lock()
	got := mgr.projectRoot
	mgr.mu.Unlock()
	assert.NotEmpty(t, got)
}

func TestUpdateProjectRoot_RaceWithStats(t *testing.T) {
	t.Parallel()
	mgr := NewCodeDBManager("/initial", codedbTestLogger(), nil)

	var wg sync.WaitGroup
	// writers
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			mgr.UpdateProjectRoot("/new/path")
		}()
	}
	// readers
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = mgr.Stats()
		}()
	}

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("concurrent UpdateProjectRoot + Stats deadlocked")
	}
}

// --- CallerPath callback wiring ---

func TestCallerPathCallback_FiresOnNewPath(t *testing.T) {
	t.Parallel()
	handler := NewHeartbeatHandler(codedbTestLogger())

	var received []string
	var mu sync.Mutex
	handler.SetCallerPathCallback(func(path string) {
		mu.Lock()
		received = append(received, path)
		mu.Unlock()
	})

	// heartbeat with caller path
	payload := HeartbeatPayload{
		CallerPath: "/workspace/alpha",
		Timestamp:  time.Now(),
	}
	data, _ := json.Marshal(payload)
	handler.Handle("caller-1", data)

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, received, 1)
	assert.Equal(t, "/workspace/alpha", received[0])
}

func TestCallerPathCallback_FiresOnEveryHeartbeat(t *testing.T) {
	t.Parallel()
	handler := NewHeartbeatHandler(codedbTestLogger())

	var count int
	var mu sync.Mutex
	handler.SetCallerPathCallback(func(path string) {
		mu.Lock()
		count++
		mu.Unlock()
	})

	// same path sent twice — callback fires both times because the daemon
	// can't know if the path became invalid and was re-created
	for range 3 {
		payload := HeartbeatPayload{
			CallerPath: "/workspace/same",
			Timestamp:  time.Now(),
		}
		data, _ := json.Marshal(payload)
		handler.Handle("caller-1", data)
	}

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 3, count)
}

func TestCallerPathCallback_NotFiredWithoutCallerPath(t *testing.T) {
	t.Parallel()
	handler := NewHeartbeatHandler(codedbTestLogger())

	called := false
	handler.SetCallerPathCallback(func(path string) {
		called = true
	})

	// heartbeat without CallerPath
	payload := HeartbeatPayload{
		RepoPath:  "/some/repo",
		Timestamp: time.Now(),
	}
	data, _ := json.Marshal(payload)
	handler.Handle("caller-1", data)

	assert.False(t, called)
}

func TestCallerPathCallback_NotFiredWithoutCallerID(t *testing.T) {
	t.Parallel()
	handler := NewHeartbeatHandler(codedbTestLogger())

	called := false
	handler.SetCallerPathCallback(func(path string) {
		called = true
	})

	// heartbeat with path but no caller ID — caller tracking block is skipped
	payload := HeartbeatPayload{
		CallerPath: "/workspace/orphan",
		Timestamp:  time.Now(),
	}
	data, _ := json.Marshal(payload)
	handler.Handle("", data)

	assert.False(t, called)
}

// --- Workspace lifecycle edge cases ---
// These test the patterns that lead to the original bug: daemon holds a path
// that stops existing mid-flight.

func TestCodeDBManager_DeletedProjectRoot_StatsDoesNotPanic(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	mgr := NewCodeDBManager(dir, codedbTestLogger(), nil)

	// simulate Conductor deleting the workspace
	require.NoError(t, os.RemoveAll(dir))

	// Stats should return gracefully, not panic
	stats := mgr.Stats()
	assert.False(t, stats.IndexExists)
	assert.Empty(t, stats.LastError)
}

func TestCodeDBManager_DeletedThenRecreatedProjectRoot(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	mgr := NewCodeDBManager(dir, codedbTestLogger(), nil)

	// delete workspace
	require.NoError(t, os.RemoveAll(dir))

	// new workspace at different path
	newDir := t.TempDir()
	mgr.UpdateProjectRoot(newDir)

	mgr.mu.Lock()
	got := mgr.projectRoot
	mgr.mu.Unlock()
	assert.Equal(t, newDir, got)
}

func TestCodeDBManager_RapidWorkspaceSwitches(t *testing.T) {
	t.Parallel()

	mgr := NewCodeDBManager("/initial", codedbTestLogger(), nil)

	// simulate Conductor rapidly creating/deleting workspaces
	paths := []string{
		"/workspace/alpha",
		"/workspace/bravo",
		"/workspace/charlie",
		"/workspace/delta",
	}
	for _, p := range paths {
		mgr.UpdateProjectRoot(p)
	}

	mgr.mu.Lock()
	got := mgr.projectRoot
	mgr.mu.Unlock()
	assert.Equal(t, "/workspace/delta", got, "should have the last path")
}

// --- End-to-end: heartbeat → codedb project root update ---

func TestHeartbeatUpdatesCodeDBProjectRoot(t *testing.T) {
	t.Parallel()

	logger := codedbTestLogger()
	handler := NewHeartbeatHandler(logger)
	mgr := NewCodeDBManager("/old/workspace", logger, nil)

	// wire them the same way daemon.go does
	handler.SetCallerPathCallback(func(path string) {
		mgr.UpdateProjectRoot(path)
	})

	// simulate heartbeat from new workspace
	payload := HeartbeatPayload{
		CallerPath: "/new/workspace",
		Timestamp:  time.Now(),
	}
	data, _ := json.Marshal(payload)
	handler.Handle("caller-abc", data)

	mgr.mu.Lock()
	got := mgr.projectRoot
	mgr.mu.Unlock()
	assert.Equal(t, "/new/workspace", got)
}

func TestHeartbeatUpdatesCodeDBProjectRoot_MultipleWorkspaces(t *testing.T) {
	t.Parallel()

	logger := codedbTestLogger()
	handler := NewHeartbeatHandler(logger)
	mgr := NewCodeDBManager("/workspace/edinburgh-v1", logger, nil)

	handler.SetCallerPathCallback(func(path string) {
		mgr.UpdateProjectRoot(path)
	})

	// simulate the exact Conductor pattern:
	// edinburgh-v1 → khartoum-v1 → da-nang-v1
	workspaces := []struct {
		callerID string
		path     string
	}{
		{"abc123", "/workspace/edinburgh-v1"},
		{"def456", "/workspace/khartoum-v1"},
		{"ghi789", "/workspace/da-nang-v1"},
	}

	for _, ws := range workspaces {
		payload := HeartbeatPayload{
			CallerPath: ws.path,
			Timestamp:  time.Now(),
		}
		data, _ := json.Marshal(payload)
		handler.Handle(ws.callerID, data)
	}

	mgr.mu.Lock()
	got := mgr.projectRoot
	mgr.mu.Unlock()
	assert.Equal(t, "/workspace/da-nang-v1", got)
}

// --- resolveSharedDataDir with deleted project root ---

func TestCodeDBManager_ResolveSharedDataDir_MissingProjectRoot(t *testing.T) {
	t.Parallel()

	// project root that doesn't exist — resolveSharedDataDir should fall back
	// to legacy path without panicking
	mgr := NewCodeDBManager("/does/not/exist", codedbTestLogger(), nil)
	dir := mgr.resolveSharedDataDir()
	assert.NotEmpty(t, dir, "should return a fallback path even with missing project root")
}

// --- Symlink edge case ---

func TestUpdateProjectRoot_Symlink(t *testing.T) {
	t.Parallel()

	realDir := t.TempDir()
	linkDir := filepath.Join(t.TempDir(), "link")
	require.NoError(t, os.Symlink(realDir, linkDir))

	mgr := NewCodeDBManager(realDir, codedbTestLogger(), nil)

	// update to symlink path — should accept it (let git resolve the real path)
	mgr.UpdateProjectRoot(linkDir)

	mgr.mu.Lock()
	got := mgr.projectRoot
	mgr.mu.Unlock()
	assert.Equal(t, linkDir, got)
}
