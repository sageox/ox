package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClearRecordingState(t *testing.T) {
	t.Run("clears existing state from session folder", func(t *testing.T) {
		cacheDir := t.TempDir()
		projectRoot, sessionsBase := setupRecordingTestWithSessionsBase(t, cacheDir)
		sessionPath := filepath.Join(sessionsBase, "2026-01-06T14-30-user-OxA1b2")

		state := &RecordingState{
			AgentID:     "OxA1b2",
			StartedAt:   time.Now(),
			SessionPath: sessionPath,
		}

		err := SaveRecordingState(projectRoot, state)
		require.NoError(t, err, "failed to save state")

		err = ClearRecordingState(projectRoot)
		require.NoError(t, err)

		// verify file is gone from session folder
		statePath := filepath.Join(sessionPath, recordingFile)
		_, err = os.Stat(statePath)
		assert.True(t, os.IsNotExist(err), "expected .recording.json to be removed")
	})

	t.Run("cleans up stale lock files", func(t *testing.T) {
		cacheDir := t.TempDir()
		projectRoot, sessionsBase := setupRecordingTestWithSessionsBase(t, cacheDir)
		sessionPath := filepath.Join(sessionsBase, "2026-01-06T14-30-user-OxLock")

		state := &RecordingState{
			AgentID:     "OxLock",
			StartedAt:   time.Now(),
			SessionPath: sessionPath,
		}

		err := SaveRecordingState(projectRoot, state)
		require.NoError(t, err)

		// create stale lock files
		lockFile := filepath.Join(sessionPath, "input.jsonl.lock")
		require.NoError(t, os.WriteFile(lockFile, []byte(""), 0600))

		err = ClearRecordingState(projectRoot)
		require.NoError(t, err)

		// lock file should be cleaned up
		_, err = os.Stat(lockFile)
		assert.True(t, os.IsNotExist(err), "lock file should be removed")
	})

	t.Run("succeeds when no state exists", func(t *testing.T) {
		cacheDir := t.TempDir()
		projectRoot := setupRecordingTest(t, cacheDir)

		err := ClearRecordingState(projectRoot)
		require.NoError(t, err, "expected no error when clearing non-existent state")
	})

	t.Run("returns error for empty project root", func(t *testing.T) {
		err := ClearRecordingState("")
		assert.Error(t, err)
	})
}

// --- Gap 3: cleanupStaleEmptyRecordings tests ---

func TestCleanupStaleEmptyRecordings_RemovesOldStubs(t *testing.T) {
	cacheDir := t.TempDir()
	projectRoot, sessionsBase := setupRecordingTestWithSessionsBase(t, cacheDir)
	sessionPath := filepath.Join(sessionsBase, "2026-01-01T00-00-user-OxStale")
	require.NoError(t, os.MkdirAll(sessionPath, 0755))

	// create a .recording.json with StartedAt > 48h ago, no raw.jsonl
	state := &RecordingState{
		AgentID:     "OxStale",
		StartedAt:   time.Now().Add(-72 * time.Hour),
		SessionPath: sessionPath,
	}
	data, err := json.Marshal(state)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(sessionPath, recordingFile), data, 0600))

	cleanupStaleEmptyRecordings(projectRoot)

	// .recording.json should be removed
	_, err = os.Stat(filepath.Join(sessionPath, recordingFile))
	assert.True(t, os.IsNotExist(err), ".recording.json should be removed for stale empty stub")
}

func TestCleanupStaleEmptyRecordings_KeepsRecentStubs(t *testing.T) {
	cacheDir := t.TempDir()
	projectRoot, sessionsBase := setupRecordingTestWithSessionsBase(t, cacheDir)
	sessionPath := filepath.Join(sessionsBase, "2026-01-01T00-00-user-OxNew1")
	require.NoError(t, os.MkdirAll(sessionPath, 0755))

	// create a .recording.json with StartedAt < 48h ago, no raw.jsonl
	state := &RecordingState{
		AgentID:     "OxNew1",
		StartedAt:   time.Now().Add(-1 * time.Hour),
		SessionPath: sessionPath,
	}
	data, err := json.Marshal(state)
	require.NoError(t, err)
	recPath := filepath.Join(sessionPath, recordingFile)
	require.NoError(t, os.WriteFile(recPath, data, 0600))

	cleanupStaleEmptyRecordings(projectRoot)

	// .recording.json should still exist
	_, err = os.Stat(recPath)
	assert.False(t, os.IsNotExist(err), ".recording.json should be preserved for recent stub")
}

func TestCleanupStaleEmptyRecordings_KeepsWithRawJSONL(t *testing.T) {
	cacheDir := t.TempDir()
	projectRoot, sessionsBase := setupRecordingTestWithSessionsBase(t, cacheDir)
	sessionPath := filepath.Join(sessionsBase, "2026-01-01T00-00-user-OxHasR")
	require.NoError(t, os.MkdirAll(sessionPath, 0755))

	// create a .recording.json with StartedAt > 48h ago AND raw.jsonl present
	state := &RecordingState{
		AgentID:     "OxHasR",
		StartedAt:   time.Now().Add(-72 * time.Hour),
		SessionPath: sessionPath,
	}
	data, err := json.Marshal(state)
	require.NoError(t, err)
	recPath := filepath.Join(sessionPath, recordingFile)
	require.NoError(t, os.WriteFile(recPath, data, 0600))
	require.NoError(t, os.WriteFile(filepath.Join(sessionPath, "raw.jsonl"), []byte("{}\n"), 0600))

	cleanupStaleEmptyRecordings(projectRoot)

	// .recording.json should still exist because raw.jsonl is present
	_, err = os.Stat(recPath)
	assert.False(t, os.IsNotExist(err), ".recording.json should be preserved when raw.jsonl exists")
}

func TestCleanupStaleEmptyRecordings_RemovesEmptyDir(t *testing.T) {
	cacheDir := t.TempDir()
	projectRoot, sessionsBase := setupRecordingTestWithSessionsBase(t, cacheDir)
	sessionPath := filepath.Join(sessionsBase, "2026-01-01T00-00-user-OxEmDr")
	require.NoError(t, os.MkdirAll(sessionPath, 0755))

	// create a stale .recording.json (only file in the dir)
	state := &RecordingState{
		AgentID:     "OxEmDr",
		StartedAt:   time.Now().Add(-72 * time.Hour),
		SessionPath: sessionPath,
	}
	data, err := json.Marshal(state)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(sessionPath, recordingFile), data, 0600))

	cleanupStaleEmptyRecordings(projectRoot)

	// session directory should be removed since it became empty after cleanup
	_, err = os.Stat(sessionPath)
	assert.True(t, os.IsNotExist(err), "empty session directory should be removed after cleanup")
}

// --- Ghost session cleanup tests ---

func TestCleanupGhostSessionsInDir_RemovesDeadPIDNoData(t *testing.T) {
	sessionsDir := filepath.Join(t.TempDir(), "sessions")
	sessionPath := filepath.Join(sessionsDir, "2026-01-01T00-00-user-OxDead")
	require.NoError(t, os.MkdirAll(sessionPath, 0755))

	// create recording with a PID that doesn't exist (99999999)
	state := &RecordingState{
		AgentID:     "OxDead",
		StartedAt:   time.Now().Add(-10 * time.Minute),
		SessionPath: sessionPath,
		ParentPID:   99999999, // guaranteed dead
	}
	data, _ := json.Marshal(state)
	require.NoError(t, os.WriteFile(filepath.Join(sessionPath, recordingFile), data, 0600))

	result := CleanupGhostSessionsInDir(sessionsDir)
	assert.Equal(t, 1, result.Removed)
	assert.Contains(t, result.Names, "2026-01-01T00-00-user-OxDead")

	// .recording.json should be gone
	_, err := os.Stat(filepath.Join(sessionPath, recordingFile))
	assert.True(t, os.IsNotExist(err), "ghost recording marker should be removed")
}

func TestCleanupGhostSessionsInDir_PreservesOrphanWithData(t *testing.T) {
	sessionsDir := filepath.Join(t.TempDir(), "sessions")
	sessionPath := filepath.Join(sessionsDir, "2026-01-01T00-00-user-OxOrph")
	require.NoError(t, os.MkdirAll(sessionPath, 0755))

	// dead PID but has raw.jsonl with content = orphan, not ghost
	state := &RecordingState{
		AgentID:     "OxOrph",
		StartedAt:   time.Now().Add(-2 * time.Hour),
		SessionPath: sessionPath,
		ParentPID:   99999999,
	}
	data, _ := json.Marshal(state)
	require.NoError(t, os.WriteFile(filepath.Join(sessionPath, recordingFile), data, 0600))
	require.NoError(t, os.WriteFile(filepath.Join(sessionPath, "raw.jsonl"), []byte(`{"metadata":{}}`+"\n"+`{"type":"user"}`+"\n"), 0600))

	result := CleanupGhostSessionsInDir(sessionsDir)
	assert.Equal(t, 0, result.Removed, "orphan with data should NOT be cleaned up")

	// .recording.json should still exist
	_, err := os.Stat(filepath.Join(sessionPath, recordingFile))
	assert.NoError(t, err, "orphan recording marker should be preserved")
}

func TestCleanupGhostSessionsInDir_RemovesHeaderOnlyRawJSONL(t *testing.T) {
	// Regression test: writeRawHeader always writes a 1-line metadata header to raw.jsonl
	// at session start. A header-only file (size > 0, but 1 line) has no real session
	// content and should be treated as a ghost, not an orphan.
	sessionsDir := filepath.Join(t.TempDir(), "sessions")
	sessionPath := filepath.Join(sessionsDir, "2026-01-01T00-00-user-OxHead")
	require.NoError(t, os.MkdirAll(sessionPath, 0755))

	state := &RecordingState{
		AgentID:     "OxHead",
		StartedAt:   time.Now().Add(-10 * time.Minute),
		SessionPath: sessionPath,
		ParentPID:   99999999,
	}
	data, _ := json.Marshal(state)
	require.NoError(t, os.WriteFile(filepath.Join(sessionPath, recordingFile), data, 0600))

	// write ONLY the header line (what writeRawHeader produces)
	headerOnly := `{"schema_version":"1","agent_id":"OxHead","started_at":"2026-01-01T00:00:00Z"}` + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(sessionPath, "raw.jsonl"), []byte(headerOnly), 0600))

	result := CleanupGhostSessionsInDir(sessionsDir)
	assert.Equal(t, 1, result.Removed, "header-only raw.jsonl is a ghost, not an orphan — should be cleaned")

	_, err := os.Stat(filepath.Join(sessionPath, recordingFile))
	assert.True(t, os.IsNotExist(err), "ghost recording marker should be removed")
}

func TestCleanupGhostSessionsInDir_SkipsLiveProcess(t *testing.T) {
	sessionsDir := filepath.Join(t.TempDir(), "sessions")
	sessionPath := filepath.Join(sessionsDir, "2026-01-01T00-00-user-OxLive")
	require.NoError(t, os.MkdirAll(sessionPath, 0755))

	// use current PID (definitely alive)
	state := &RecordingState{
		AgentID:     "OxLive",
		StartedAt:   time.Now().Add(-10 * time.Minute),
		SessionPath: sessionPath,
		ParentPID:   os.Getpid(),
		EntryCount:  0,
	}
	data, _ := json.Marshal(state)
	require.NoError(t, os.WriteFile(filepath.Join(sessionPath, recordingFile), data, 0600))

	result := CleanupGhostSessionsInDir(sessionsDir)
	assert.Equal(t, 0, result.Removed, "live process session should NOT be cleaned up")

	_, err := os.Stat(filepath.Join(sessionPath, recordingFile))
	assert.NoError(t, err, "live session recording marker should be preserved")
}

func TestCleanupGhostSessionsInDir_SkipsNoPID(t *testing.T) {
	sessionsDir := filepath.Join(t.TempDir(), "sessions")
	sessionPath := filepath.Join(sessionsDir, "2026-01-01T00-00-user-OxNoPd")
	require.NoError(t, os.MkdirAll(sessionPath, 0755))

	// no ParentPID recorded — can't determine liveness, skip
	state := &RecordingState{
		AgentID:     "OxNoPd",
		StartedAt:   time.Now().Add(-72 * time.Hour),
		SessionPath: sessionPath,
		ParentPID:   0,
	}
	data, _ := json.Marshal(state)
	require.NoError(t, os.WriteFile(filepath.Join(sessionPath, recordingFile), data, 0600))

	result := CleanupGhostSessionsInDir(sessionsDir)
	assert.Equal(t, 0, result.Removed, "session without PID should NOT be cleaned by ghost cleanup")
}

func TestCleanupGhostSessionsInDir_DoubleCleanupIsIdempotent(t *testing.T) {
	sessionsDir := filepath.Join(t.TempDir(), "sessions")
	sessionPath := filepath.Join(sessionsDir, "2026-01-01T00-00-user-OxDbl1")
	require.NoError(t, os.MkdirAll(sessionPath, 0755))

	state := &RecordingState{
		AgentID:     "OxDbl1",
		StartedAt:   time.Now().Add(-1 * time.Hour),
		SessionPath: sessionPath,
		ParentPID:   99999999,
	}
	data, _ := json.Marshal(state)
	require.NoError(t, os.WriteFile(filepath.Join(sessionPath, recordingFile), data, 0600))

	// first cleanup
	r1 := CleanupGhostSessionsInDir(sessionsDir)
	assert.Equal(t, 1, r1.Removed)

	// second cleanup — should be a no-op (no .recording.json left to find)
	r2 := CleanupGhostSessionsInDir(sessionsDir)
	assert.Equal(t, 0, r2.Removed, "second cleanup should find nothing — idempotent")
}
