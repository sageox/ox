package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWriteHeartbeatToPath_CreatesFile(t *testing.T) {
	t.Parallel()

	hbPath := filepath.Join(t.TempDir(), "heartbeats", "test.jsonl")

	entry := HeartbeatEntry{
		Timestamp:     time.Now(),
		DaemonPID:     12345,
		DaemonVersion: "v0.9.0",
		Workspace:     "/tmp/project",
		Status:        "healthy",
	}

	err := WriteHeartbeatToPath(hbPath, entry)
	require.NoError(t, err)

	// verify file exists
	_, err = os.Stat(hbPath)
	require.NoError(t, err)

	// read back
	got, err := ReadLastHeartbeatFromPath(hbPath)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, 12345, got.DaemonPID)
	assert.Equal(t, "healthy", got.Status)
	assert.Equal(t, "v0.9.0", got.DaemonVersion)
}

func TestWriteHeartbeatToPath_RollingWindow(t *testing.T) {
	t.Parallel()

	hbPath := filepath.Join(t.TempDir(), "test.jsonl")

	// write more than maxHeartbeatHistory entries
	for i := range maxHeartbeatHistory + 3 {
		entry := HeartbeatEntry{
			Timestamp: time.Now().Add(time.Duration(i) * time.Second),
			DaemonPID: 1000 + i,
			Status:    "healthy",
		}
		err := WriteHeartbeatToPath(hbPath, entry)
		require.NoError(t, err)
	}

	// read all entries
	entries, err := readHeartbeatsFromPath(hbPath)
	require.NoError(t, err)
	assert.Len(t, entries, maxHeartbeatHistory, "should truncate to maxHeartbeatHistory")

	// oldest remaining should be entry #3 (0-indexed: entries 3,4,5,6,7)
	assert.Equal(t, 1003, entries[0].DaemonPID, "oldest entries should be trimmed")
}

func TestReadLastHeartbeatFromPath_EmptyFile(t *testing.T) {
	t.Parallel()

	hbPath := filepath.Join(t.TempDir(), "empty.jsonl")
	require.NoError(t, os.WriteFile(hbPath, []byte{}, 0600))

	got, err := ReadLastHeartbeatFromPath(hbPath)
	require.NoError(t, err)
	assert.Nil(t, got, "empty file should return nil")
}

func TestReadLastHeartbeatFromPath_MissingFile(t *testing.T) {
	t.Parallel()

	got, err := ReadLastHeartbeatFromPath(filepath.Join(t.TempDir(), "nonexistent.jsonl"))
	assert.Error(t, err)
	assert.Nil(t, got)
}

func TestReadHeartbeatsFromPath_SkipsMalformedLines(t *testing.T) {
	t.Parallel()

	hbPath := filepath.Join(t.TempDir(), "partial.jsonl")

	// write a mix of valid and invalid lines
	validEntry := HeartbeatEntry{
		Timestamp: time.Now(),
		DaemonPID: 42,
		Status:    "healthy",
	}
	validJSON, _ := json.Marshal(validEntry)

	content := string(validJSON) + "\n" +
		"not valid json\n" +
		"{\"ts\":\"2025-01-01T00:00:00Z\",\"pid\":99,\"status\":\"error\"}\n"

	require.NoError(t, os.WriteFile(hbPath, []byte(content), 0600))

	entries, err := readHeartbeatsFromPath(hbPath)
	require.NoError(t, err)
	assert.Len(t, entries, 2, "should skip malformed lines and keep valid ones")
	assert.Equal(t, 42, entries[0].DaemonPID)
	assert.Equal(t, 99, entries[1].DaemonPID)
}

func TestWriteHeartbeatToPath_AtomicWrite(t *testing.T) {
	t.Parallel()

	hbPath := filepath.Join(t.TempDir(), "atomic.jsonl")

	// write initial entry
	err := WriteHeartbeatToPath(hbPath, HeartbeatEntry{
		Timestamp: time.Now(),
		DaemonPID: 1,
		Status:    "healthy",
	})
	require.NoError(t, err)

	// verify no .tmp file remains
	tmpPath := hbPath + ".tmp"
	_, err = os.Stat(tmpPath)
	assert.True(t, os.IsNotExist(err), "temp file should be cleaned up after atomic write")
}

func TestHeartbeatEntry_JSONFields(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC().Truncate(time.Second)
	lastSync := now.Add(-5 * time.Minute)

	entry := HeartbeatEntry{
		Timestamp:     now,
		DaemonPID:     12345,
		DaemonVersion: "v0.9.0",
		Workspace:     "/home/user/project",
		LastSync:      lastSync,
		Status:        "healthy",
		ErrorCount:    3,
	}

	data, err := json.Marshal(entry)
	require.NoError(t, err)

	// verify expected JSON field names
	raw := string(data)
	assert.True(t, strings.Contains(raw, `"ts"`), "timestamp should serialize as 'ts'")
	assert.True(t, strings.Contains(raw, `"pid"`), "daemon PID should serialize as 'pid'")
	assert.True(t, strings.Contains(raw, `"version"`), "version should serialize as 'version'")
	assert.True(t, strings.Contains(raw, `"status"`))
	assert.True(t, strings.Contains(raw, `"error_count"`))

	// roundtrip
	var restored HeartbeatEntry
	require.NoError(t, json.Unmarshal(data, &restored))
	assert.Equal(t, entry.DaemonPID, restored.DaemonPID)
	assert.Equal(t, entry.Status, restored.Status)
	assert.Equal(t, entry.ErrorCount, restored.ErrorCount)
}

func TestUserHeartbeatPath(t *testing.T) {
	t.Parallel()

	path := UserHeartbeatPath("sageox.ai", "repo_abc", "ws_123")
	assert.Contains(t, path, "repo_abc_ws_123.jsonl")
	assert.True(t, filepath.IsAbs(path))
}

func TestUserLedgerHeartbeatPath(t *testing.T) {
	t.Parallel()

	path := UserLedgerHeartbeatPath("sageox.ai", "repo_abc", "ws_123")
	assert.Contains(t, path, "repo_abc_ws_123_ledger.jsonl")
	assert.True(t, filepath.IsAbs(path))
}

func TestUserTeamHeartbeatPath(t *testing.T) {
	t.Parallel()

	path := UserTeamHeartbeatPath("sageox.ai", "team_xyz")
	assert.Contains(t, path, "team_xyz.jsonl")
	assert.True(t, filepath.IsAbs(path))
}
