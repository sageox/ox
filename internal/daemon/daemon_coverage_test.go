package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsDaemonDisabled(t *testing.T) {
	tests := []struct {
		name    string
		envVal  string
		wantDis bool
	}{
		{"not_set", "", false},
		{"true_val", "true", false},
		{"false_val", "false", true},
		{"FALSE_val", "FALSE", true},
		{"False_val", "False", true},
		{"one_val", "1", false},
		{"random", "something", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("SAGEOX_DAEMON", tt.envVal)
			assert.Equal(t, tt.wantDis, IsDaemonDisabled())
		})
	}
}

func TestDefaultConfig_AllValues(t *testing.T) {
	t.Parallel()
	cfg := DefaultConfig()

	assert.Equal(t, 60*time.Second, cfg.SyncIntervalRead)
	assert.Equal(t, 15*time.Second, cfg.TeamContextSyncInterval)
	assert.Equal(t, 500*time.Millisecond, cfg.DebounceWindow)
	assert.Equal(t, 30*time.Minute, cfg.VersionCheckInterval)
	assert.Equal(t, 1*time.Hour, cfg.GCCheckInterval)
	assert.Equal(t, 6*time.Hour, cfg.DistillInterval)
	assert.Equal(t, 15*time.Minute, cfg.BaselineCheckInterval)
	assert.Equal(t, 15*time.Minute, cfg.GitHubSyncInterval)
	assert.Equal(t, 1*time.Hour, cfg.InactivityTimeout)
	assert.True(t, cfg.AutoStart)
	assert.Empty(t, cfg.LedgerPath)
	assert.Empty(t, cfg.ProjectRoot)
}

func TestWorkspaceID_SymlinkResolution(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()

	realDir := filepath.Join(tmpDir, "real")
	require.NoError(t, os.MkdirAll(realDir, 0755))

	symlinkDir := filepath.Join(tmpDir, "link")
	require.NoError(t, os.Symlink(realDir, symlinkDir))

	idReal := WorkspaceID(realDir)
	idSymlink := WorkspaceID(symlinkDir)

	assert.Equal(t, idReal, idSymlink, "symlink and real path should produce the same workspace ID")
}

func TestHeartbeatPaths(t *testing.T) {
	t.Parallel()
	ep := "https://sageox.ai"
	repoID := "repo_abc123"
	workspaceID := "a1b2c3d4"
	teamID := "team_xyz789"

	wsPath := UserHeartbeatPath(ep, repoID, workspaceID)
	assert.Contains(t, wsPath, "repo_abc123_a1b2c3d4.jsonl")

	ledgerPath := UserLedgerHeartbeatPath(ep, repoID, workspaceID)
	assert.Contains(t, ledgerPath, "repo_abc123_a1b2c3d4_ledger.jsonl")

	teamPath := UserTeamHeartbeatPath(ep, teamID)
	assert.Contains(t, teamPath, "team_xyz789.jsonl")

	assert.Contains(t, wsPath, "heartbeats")
	assert.Contains(t, ledgerPath, "heartbeats")
	assert.Contains(t, teamPath, "heartbeats")
}

func TestWriteAndReadHeartbeat(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	hbPath := filepath.Join(tmpDir, "heartbeat.jsonl")

	entry := HeartbeatEntry{
		Timestamp:     time.Now(),
		DaemonPID:     12345,
		DaemonVersion: "0.10.0",
		Workspace:     "test-workspace",
		Status:        "healthy",
	}

	err := WriteHeartbeatToPath(hbPath, entry)
	require.NoError(t, err)

	last, err := ReadLastHeartbeatFromPath(hbPath)
	require.NoError(t, err)
	require.NotNil(t, last)

	assert.Equal(t, 12345, last.DaemonPID)
	assert.Equal(t, "0.10.0", last.DaemonVersion)
	assert.Equal(t, "test-workspace", last.Workspace)
	assert.Equal(t, "healthy", last.Status)
}

func TestWriteHeartbeat_RollingHistory(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	hbPath := filepath.Join(tmpDir, "heartbeat.jsonl")

	for i := 0; i < maxHeartbeatHistory+5; i++ {
		entry := HeartbeatEntry{
			Timestamp:     time.Now(),
			DaemonPID:     i,
			DaemonVersion: "0.10.0",
			Status:        "healthy",
		}
		require.NoError(t, WriteHeartbeatToPath(hbPath, entry))
	}

	entries, err := readHeartbeatsFromPath(hbPath)
	require.NoError(t, err)
	assert.Len(t, entries, maxHeartbeatHistory, "should trim to max history")
	assert.Equal(t, maxHeartbeatHistory+4, entries[len(entries)-1].DaemonPID)
}

func TestReadLastHeartbeat_EmptyFile(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	hbPath := filepath.Join(tmpDir, "empty.jsonl")

	require.NoError(t, os.WriteFile(hbPath, []byte{}, 0644))

	last, err := ReadLastHeartbeatFromPath(hbPath)
	require.NoError(t, err)
	assert.Nil(t, last)
}

func TestReadLastHeartbeat_NonexistentFile(t *testing.T) {
	t.Parallel()
	last, err := ReadLastHeartbeatFromPath("/nonexistent/path/heartbeat.jsonl")
	assert.Error(t, err)
	assert.Nil(t, last)
}

func TestReadHeartbeats_MalformedLines(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	hbPath := filepath.Join(tmpDir, "malformed.jsonl")

	content := `{"ts":"2024-01-01T00:00:00Z","pid":1,"version":"0.1.0","status":"healthy"}
not json
{"ts":"2024-01-02T00:00:00Z","pid":2,"version":"0.1.0","status":"error"}
`
	require.NoError(t, os.WriteFile(hbPath, []byte(content), 0644))

	entries, err := readHeartbeatsFromPath(hbPath)
	require.NoError(t, err)
	assert.Len(t, entries, 2, "should skip malformed lines")
}

func TestRestartHistory_SaveAndLoad(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)
	os.MkdirAll(filepath.Join(tmpDir, "sageox"), 0755)

	h := &restartHistory{
		Restarts: []time.Time{
			time.Now().Add(-1 * time.Minute),
			time.Now(),
		},
	}

	err := saveRestartHistory(h)
	require.NoError(t, err)

	loaded, err := loadRestartHistory()
	require.NoError(t, err)
	require.NotNil(t, loaded)
	assert.Len(t, loaded.Restarts, 2)
}

func TestRestartHistory_PrunesOld(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)
	os.MkdirAll(filepath.Join(tmpDir, "sageox"), 0755)

	h := &restartHistory{
		Restarts: []time.Time{
			time.Now().Add(-10 * time.Minute),
			time.Now().Add(-1 * time.Minute),
			time.Now(),
		},
	}

	err := saveRestartHistory(h)
	require.NoError(t, err)

	loaded, err := loadRestartHistory()
	require.NoError(t, err)
	assert.Len(t, loaded.Restarts, 2, "should prune entries outside restart window")
}

func TestLoadRestartHistory_Nonexistent(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	loaded, err := loadRestartHistory()
	require.NoError(t, err)
	require.NotNil(t, loaded)
	assert.Empty(t, loaded.Restarts)
}

func TestLoadRestartHistory_CorruptFile(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	configDir := filepath.Join(tmpDir, "sageox")
	require.NoError(t, os.MkdirAll(configDir, 0755))
	require.NoError(t, os.WriteFile(
		filepath.Join(configDir, restartHistoryFile),
		[]byte("not json"),
		0600,
	))

	loaded, err := loadRestartHistory()
	require.NoError(t, err, "corrupt file should not return error")
	require.NotNil(t, loaded)
	assert.Empty(t, loaded.Restarts, "corrupt file should start fresh")
}

func TestErrorStore_ConcurrentAccess(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	storePath := filepath.Join(tmpDir, "errors.json")

	store := NewErrorStore(storePath)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 50; i++ {
			store.Add(StoredError{
				Code:     "concurrent_err",
				Message:  "concurrent test",
				Severity: "warning",
			})
		}
	}()

	for i := 0; i < 50; i++ {
		_ = store.GetAll()
		_ = store.GetUnviewed()
		_ = store.Count()
		_ = store.UnviewedCount()
	}

	<-done
}

func TestErrorStore_MarkViewedNonexistent(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	store := NewErrorStore(filepath.Join(tmpDir, "errors.json"))

	store.MarkViewed("nonexistent-id")
	assert.Equal(t, 0, store.Count())
}

func TestErrorStore_MarkAllViewed_Idempotent(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	store := NewErrorStore(filepath.Join(tmpDir, "errors.json"))

	store.Add(StoredError{Code: "err1", Message: "Error 1", Severity: "error"})
	store.MarkAllViewed()
	store.MarkAllViewed() // second call is a no-op
	assert.Equal(t, 0, store.UnviewedCount())
}

func TestErrorStore_CleanupNoOldErrors(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	store := NewErrorStore(filepath.Join(tmpDir, "errors.json"))

	store.Add(StoredError{Code: "recent", Message: "Recent", Severity: "error"})
	store.Cleanup(365 * 24 * time.Hour)
	assert.Equal(t, 1, store.Count())
}

func TestErrorStorePathForWorkspace(t *testing.T) {
	t.Parallel()
	path := ErrorStorePathForWorkspace("abc12345")
	assert.Contains(t, path, "errors-abc12345.json")
	assert.True(t, filepath.IsAbs(path))
}

func TestVersionCacheData_Serialization(t *testing.T) {
	t.Parallel()
	data := VersionCacheData{
		LatestVersion: "v0.10.0",
		CheckedAt:     time.Now().Truncate(time.Second),
		ETag:          "W/\"abc123\"",
	}

	raw, err := json.Marshal(data)
	require.NoError(t, err)

	var decoded VersionCacheData
	require.NoError(t, json.Unmarshal(raw, &decoded))

	assert.Equal(t, data.LatestVersion, decoded.LatestVersion)
	assert.Equal(t, data.ETag, decoded.ETag)
}

func TestActivityTracker_EdgeCapacities(t *testing.T) {
	t.Parallel()
	tracker := NewActivityTrackerWithMaxKeys(0, -1)
	assert.NotNil(t, tracker)
	assert.Equal(t, 50, tracker.capacity)
	assert.Equal(t, 0, tracker.maxKeys)
}

func TestActivityTracker_DefaultMaxKeys(t *testing.T) {
	t.Parallel()
	tracker := NewActivityTracker(100)
	assert.NotNil(t, tracker)
	assert.Equal(t, 100, tracker.capacity)
	assert.Equal(t, defaultMaxKeys, tracker.maxKeys)
}

func TestHeartbeatEntry_WithErrorCount(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	hbPath := filepath.Join(tmpDir, "error-heartbeat.jsonl")

	entry := HeartbeatEntry{
		Timestamp:     time.Now(),
		DaemonPID:     999,
		DaemonVersion: "0.10.0",
		Workspace:     "test",
		LastSync:      time.Now().Add(-5 * time.Minute),
		Status:        "error",
		ErrorCount:    3,
	}

	require.NoError(t, WriteHeartbeatToPath(hbPath, entry))

	last, err := ReadLastHeartbeatFromPath(hbPath)
	require.NoError(t, err)
	require.NotNil(t, last)

	assert.Equal(t, "error", last.Status)
	assert.Equal(t, 3, last.ErrorCount)
	assert.False(t, last.LastSync.IsZero())
}

func TestWriteHeartbeat_CreatesDirectory(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	hbPath := filepath.Join(tmpDir, "deep", "nested", "heartbeat.jsonl")

	entry := HeartbeatEntry{
		Timestamp:     time.Now(),
		DaemonPID:     1,
		DaemonVersion: "0.1.0",
		Status:        "healthy",
	}

	err := WriteHeartbeatToPath(hbPath, entry)
	require.NoError(t, err)

	_, err = os.Stat(filepath.Dir(hbPath))
	assert.NoError(t, err, "directory should be auto-created")
}
