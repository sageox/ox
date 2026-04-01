package session

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSaveRecordingState(t *testing.T) {
	t.Run("saves state successfully", func(t *testing.T) {
		tempHome := t.TempDir()
		t.Setenv("HOME", tempHome)
		t.Setenv("XDG_CACHE_HOME", "")

		tmpDir := t.TempDir()
		sessionPath := filepath.Join(tmpDir, "sessions", "2026-01-06T14-30-user-OxA1b2")

		state := &RecordingState{
			OutputFile:  "/path/to/session.md",
			AgentID:     "OxA1b2",
			StartedAt:   time.Now(),
			AdapterName: "claude-code",
			SessionFile: "/path/to/session.jsonl",
			SessionPath: sessionPath,
		}

		err := SaveRecordingState(tmpDir, state)
		require.NoError(t, err)

		// verify file exists in session folder
		statePath := filepath.Join(sessionPath, recordingFile)
		_, err = os.Stat(statePath)
		assert.False(t, os.IsNotExist(err), "expected .recording.json to exist in session folder")
	})

	t.Run("creates session directory if missing", func(t *testing.T) {
		tempHome := t.TempDir()
		t.Setenv("HOME", tempHome)
		t.Setenv("XDG_CACHE_HOME", "")

		tmpDir := t.TempDir()
		sessionPath := filepath.Join(tmpDir, "sessions", "2026-01-06T14-30-user-OxA1b2")

		state := &RecordingState{
			OutputFile:  "/path/to/session.md",
			AgentID:     "OxA1b2",
			StartedAt:   time.Now(),
			SessionPath: sessionPath,
		}

		err := SaveRecordingState(tmpDir, state)
		require.NoError(t, err)

		info, err := os.Stat(sessionPath)
		require.NoError(t, err, "expected session directory to exist")
		assert.True(t, info.IsDir(), "expected session path to be a directory")
	})

	t.Run("returns error for empty project root", func(t *testing.T) {
		state := &RecordingState{AgentID: "OxA1b2", SessionPath: "/tmp/session"}
		err := SaveRecordingState("", state)
		assert.Error(t, err)
	})

	t.Run("returns error for nil state", func(t *testing.T) {
		err := SaveRecordingState("/tmp", nil)
		assert.Error(t, err)
	})

	t.Run("returns error for empty session path", func(t *testing.T) {
		state := &RecordingState{AgentID: "OxA1b2"}
		err := SaveRecordingState("/tmp", state)
		assert.Error(t, err)
	})
}

func TestLoadRecordingState(t *testing.T) {
	t.Run("loads saved state from session folder", func(t *testing.T) {
		cacheDir := t.TempDir()
		projectRoot, sessionsBase := setupRecordingTestWithSessionsBase(t, cacheDir)
		sessionPath := filepath.Join(sessionsBase, "2026-01-06T14-30-user-OxA1b2")

		originalState := &RecordingState{
			OutputFile:  "/path/to/session.md",
			AgentID:     "OxA1b2",
			StartedAt:   time.Now().Truncate(time.Second),
			AdapterName: "claude-code",
			SessionFile: "/path/to/session.jsonl",
			SessionPath: sessionPath,
		}

		err := SaveRecordingState(projectRoot, originalState)
		require.NoError(t, err, "failed to save state")

		loadedState, err := LoadRecordingState(projectRoot)
		require.NoError(t, err)
		require.NotNil(t, loadedState, "expected state to be loaded")

		assert.Equal(t, originalState.AgentID, loadedState.AgentID)
		assert.Equal(t, originalState.OutputFile, loadedState.OutputFile)
		assert.Equal(t, originalState.AdapterName, loadedState.AdapterName)
		assert.Equal(t, originalState.SessionFile, loadedState.SessionFile)
		assert.Equal(t, originalState.SessionPath, loadedState.SessionPath)
	})

	t.Run("loads state with title", func(t *testing.T) {
		cacheDir := t.TempDir()
		projectRoot, sessionsBase := setupRecordingTestWithSessionsBase(t, cacheDir)
		sessionPath := filepath.Join(sessionsBase, "2026-01-06T14-30-user-OxA1b2")

		originalState := &RecordingState{
			AgentID:     "OxA1b2",
			StartedAt:   time.Now().Truncate(time.Second),
			Title:       "Setting up AWS infrastructure",
			SessionPath: sessionPath,
		}

		err := SaveRecordingState(projectRoot, originalState)
		require.NoError(t, err, "failed to save state")

		loadedState, err := LoadRecordingState(projectRoot)
		require.NoError(t, err)

		assert.Equal(t, originalState.Title, loadedState.Title)
	})

	t.Run("returns nil for non-existent state", func(t *testing.T) {
		cacheDir := t.TempDir()
		projectRoot := setupRecordingTest(t, cacheDir)

		state, err := LoadRecordingState(projectRoot)
		require.NoError(t, err)
		assert.Nil(t, state, "expected nil state for non-existent file")
	})

	t.Run("returns error for empty project root", func(t *testing.T) {
		_, err := LoadRecordingState("")
		assert.Error(t, err)
	})

	t.Run("skips invalid JSON in session folder and continues", func(t *testing.T) {
		cacheDir := t.TempDir()
		projectRoot, sessionsBase := setupRecordingTestWithSessionsBase(t, cacheDir)

		// create a session folder with invalid .recording.json
		sessionPath := filepath.Join(sessionsBase, "2026-01-06T14-30-user-OxBad")
		err := os.MkdirAll(sessionPath, 0755)
		require.NoError(t, err, "failed to create session dir")

		invalidPath := filepath.Join(sessionPath, recordingFile)
		err = os.WriteFile(invalidPath, []byte("invalid json"), 0600)
		require.NoError(t, err, "failed to write invalid state")

		// should return nil without error (skips invalid entries)
		state, err := LoadRecordingState(projectRoot)
		require.NoError(t, err)
		assert.Nil(t, state, "expected nil state when only invalid JSON exists")
	})
}

func TestStopRecording(t *testing.T) {
	t.Run("stops recording successfully", func(t *testing.T) {
		cacheDir := t.TempDir()
		projectRoot := setupRecordingTest(t, cacheDir)

		// start a recording first
		opts := StartRecordingOptions{
			AgentID:     "OxA1b2",
			AdapterName: "claude-code",
			Title:       "Test recording",
			Username:    "testuser",
		}

		startState, err := StartRecording(projectRoot, opts)
		require.NoError(t, err, "failed to start recording")

		// stop the recording
		state, err := StopRecording(projectRoot, "OxA1b2")
		require.NoError(t, err)
		require.NotNil(t, state, "expected state to be returned")

		assert.Equal(t, opts.AgentID, state.AgentID)
		assert.Equal(t, opts.Title, state.Title)

		// verify .recording.json is removed from session folder
		recordingPath := filepath.Join(startState.SessionPath, recordingFile)
		_, err = os.Stat(recordingPath)
		assert.True(t, os.IsNotExist(err), "expected .recording.json to be removed after stop")

		// verify IsRecording returns false after stop
		assert.False(t, IsRecording(projectRoot), "expected IsRecording to return false after StopRecording")
	})

	t.Run("returns error when not recording", func(t *testing.T) {
		tempHome := t.TempDir()
		t.Setenv("OX_XDG_ENABLE", "1")
		t.Setenv("HOME", tempHome)
		t.Setenv("XDG_CACHE_HOME", tempHome)

		tmpDir := t.TempDir()

		_, err := StopRecording(tmpDir, "test-agent")
		assert.Error(t, err, "expected error when not recording")
		assert.True(t, errors.Is(err, ErrNotRecording))
	})

	t.Run("returns error for empty project root", func(t *testing.T) {
		_, err := StopRecording("", "test-agent")
		assert.Error(t, err)
	})
}

func TestLoadRecordingState_MultipleRecordingsReturnsFirst(t *testing.T) {
	cacheDir := t.TempDir()
	projectRoot, sessionsBase := setupRecordingTestWithSessionsBase(t, cacheDir)

	// create two sessions with .recording.json — ReadDir returns alphabetically
	for _, agentID := range []string{"OxFirst", "OxSecnd"} {
		name := "2026-01-06T14-30-user-" + agentID
		sessionPath := filepath.Join(sessionsBase, name)
		state := &RecordingState{
			AgentID:     agentID,
			StartedAt:   time.Now(),
			SessionPath: sessionPath,
		}
		err := SaveRecordingState(projectRoot, state)
		require.NoError(t, err)
	}

	state, err := LoadRecordingState(projectRoot)
	require.NoError(t, err)
	require.NotNil(t, state)

	// ReadDir sorts alphabetically, so "OxFirst" session folder comes first
	assert.Equal(t, "OxFirst", state.AgentID,
		"LoadRecordingState should return the alphabetically first recording")
}

func TestClearRecordingState_WithMultipleRecordings_OnlyClearsFirst(t *testing.T) {
	cacheDir := t.TempDir()
	projectRoot, sessionsBase := setupRecordingTestWithSessionsBase(t, cacheDir)

	// create two recording states
	statePaths := map[string]string{}
	for _, agentID := range []string{"OxAAAA", "OxBBBB"} {
		name := "2026-01-06T14-30-user-" + agentID
		sessionPath := filepath.Join(sessionsBase, name)
		state := &RecordingState{
			AgentID:     agentID,
			StartedAt:   time.Now(),
			SessionPath: sessionPath,
		}
		err := SaveRecordingState(projectRoot, state)
		require.NoError(t, err)
		statePaths[agentID] = filepath.Join(sessionPath, recordingFile)
	}

	// clear — should only remove the first one found (alphabetically)
	err := ClearRecordingState(projectRoot)
	require.NoError(t, err)

	// first recording should be gone
	_, err = os.Stat(statePaths["OxAAAA"])
	assert.True(t, os.IsNotExist(err), "first recording should be cleared")

	// second recording should survive
	_, err = os.Stat(statePaths["OxBBBB"])
	assert.False(t, os.IsNotExist(err), "second recording should survive ClearRecordingState")

	// LoadRecordingState should now find the second one
	state, err := LoadRecordingState(projectRoot)
	require.NoError(t, err)
	require.NotNil(t, state)
	assert.Equal(t, "OxBBBB", state.AgentID)
}

func TestStartRecording_ConcurrentAgents_PreservesSessionData(t *testing.T) {
	cacheDir := t.TempDir()
	projectRoot := setupRecordingTest(t, cacheDir)

	// Agent A starts recording
	stateA, err := StartRecording(projectRoot, StartRecordingOptions{
		AgentID: "OxAgntA", AdapterName: "claude-code", Username: "testuser",
	})
	require.NoError(t, err)

	// simulate Agent A writing session data
	rawPath := filepath.Join(stateA.SessionPath, "raw.jsonl")
	require.NoError(t, os.WriteFile(rawPath, []byte("{\"type\":\"header\"}\n"), 0644))

	// Agent B starts — both recordings should coexist
	stateB, err := StartRecording(projectRoot, StartRecordingOptions{
		AgentID: "OxAgntB", AdapterName: "claude-code", Username: "testuser",
	})
	require.NoError(t, err)

	// A's .recording.json should still exist (no ghost clearing)
	_, err = os.Stat(filepath.Join(stateA.SessionPath, recordingFile))
	assert.False(t, os.IsNotExist(err), "A's .recording.json should still exist")

	// B's .recording.json should also exist
	_, err = os.Stat(filepath.Join(stateB.SessionPath, recordingFile))
	assert.False(t, os.IsNotExist(err), "B's .recording.json should exist")

	// A's session DATA must survive (raw.jsonl)
	_, err = os.Stat(rawPath)
	assert.False(t, os.IsNotExist(err), "A's raw.jsonl must survive")

	// A's session folder itself must still exist
	_, err = os.Stat(stateA.SessionPath)
	assert.False(t, os.IsNotExist(err), "A's session folder must survive")

	// both agents should be recording
	assert.True(t, IsRecordingForAgent(projectRoot, "OxAgntA"), "Agent A should still be recording")
	assert.True(t, IsRecordingForAgent(projectRoot, "OxAgntB"), "Agent B should be recording")
}

func TestStartRecording_RepoContextPath_DirectPath(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)
	t.Setenv("XDG_CACHE_HOME", "")

	tmpDir := t.TempDir()
	repoContextPath := filepath.Join(tmpDir, "my-ledger")
	require.NoError(t, os.MkdirAll(repoContextPath, 0755))

	opts := StartRecordingOptions{
		AgentID:         "OxDrct",
		AdapterName:     "claude-code",
		Username:        "testuser",
		RepoContextPath: repoContextPath,
	}

	state, err := StartRecording(tmpDir, opts)
	require.NoError(t, err)
	require.NotNil(t, state)

	// session should be under repoContextPath/sessions/, not XDG cache
	assert.True(t, strings.HasPrefix(state.SessionPath, filepath.Join(repoContextPath, "sessions")),
		"session should be under RepoContextPath/sessions/, got %s", state.SessionPath)
}

func TestUpdateRecordingState_SequentialUpdates(t *testing.T) {
	cacheDir := t.TempDir()
	projectRoot := setupRecordingTest(t, cacheDir)

	_, err := StartRecording(projectRoot, StartRecordingOptions{
		AgentID: "OxUpdt", AdapterName: "claude-code", Username: "testuser",
	})
	require.NoError(t, err)

	// first update: set entry count
	err = UpdateRecordingState(projectRoot, func(s *RecordingState) {
		s.EntryCount = 10
	})
	require.NoError(t, err)

	// second update: set reminder seq (should see entry count from first update)
	err = UpdateRecordingState(projectRoot, func(s *RecordingState) {
		s.LastReminderSeq = 5
	})
	require.NoError(t, err)

	// verify both mutations applied
	state, err := LoadRecordingState(projectRoot)
	require.NoError(t, err)
	assert.Equal(t, 10, state.EntryCount, "first update should persist")
	assert.Equal(t, 5, state.LastReminderSeq, "second update should persist")
}

func TestStopRecording_FailsIfStateFileUnremovable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission semantics differ on Windows")
	}

	cacheDir := t.TempDir()
	projectRoot := setupRecordingTest(t, cacheDir)

	state, err := StartRecording(projectRoot, StartRecordingOptions{
		AgentID: "OxPerm", AdapterName: "claude-code", Username: "testuser",
	})
	require.NoError(t, err)

	// make the session directory read-only so .recording.json can't be removed
	require.NoError(t, os.Chmod(state.SessionPath, 0555))
	t.Cleanup(func() { os.Chmod(state.SessionPath, 0755) })

	_, err = StopRecording(projectRoot, "OxPerm")
	assert.Error(t, err, "StopRecording should fail if state file can't be removed")
}

func TestLoadAllRecordingStates_Deduplicates(t *testing.T) {
	cacheDir := t.TempDir()
	projectRoot := setupRecordingTest(t, cacheDir)

	// start a recording
	_, err := StartRecording(projectRoot, StartRecordingOptions{
		AgentID: "OxDedup", AdapterName: "claude-code", Username: "testuser",
	})
	require.NoError(t, err)

	// LoadAllRecordingStates checks both project-local and XDG cache paths;
	// they may overlap. Verify no duplicates.
	states, err := LoadAllRecordingStates(projectRoot)
	require.NoError(t, err)

	ids := map[string]int{}
	for _, s := range states {
		ids[s.AgentID]++
	}
	for id, count := range ids {
		assert.Equal(t, 1, count, "agent %s should appear exactly once", id)
	}

	// cleanup
	_, _ = StopRecording(projectRoot, "OxDedup")
}

func TestUpdateRecordingStateForAgent_Isolation(t *testing.T) {
	projectRoot, _ := createTestSessionProject(t)

	agentA := "OxAgentA"
	agentB := "OxAgentB"

	_, err := StartRecording(projectRoot, StartRecordingOptions{
		AgentID:     agentA,
		AdapterName: "claude-code",
	})
	require.NoError(t, err)

	_, err = StartRecording(projectRoot, StartRecordingOptions{
		AgentID:     agentB,
		AdapterName: "claude-code",
	})
	require.NoError(t, err)

	// update agent A's entry count
	err = UpdateRecordingStateForAgent(projectRoot, agentA, func(s *RecordingState) {
		s.EntryCount = 42
	})
	require.NoError(t, err)

	// verify agent A updated
	stateA, _ := LoadRecordingStateForAgent(projectRoot, agentA)
	require.NotNil(t, stateA)
	assert.Equal(t, 42, stateA.EntryCount)

	// verify agent B untouched
	stateB, _ := LoadRecordingStateForAgent(projectRoot, agentB)
	require.NotNil(t, stateB)
	assert.Equal(t, 0, stateB.EntryCount)
}

// --- Gap 4: Corrupted .recording.json recovery tests ---

func TestLoadRecordingState_CorruptJSON(t *testing.T) {
	cacheDir := t.TempDir()
	projectRoot, sessionsBase := setupRecordingTestWithSessionsBase(t, cacheDir)
	sessionPath := filepath.Join(sessionsBase, "2026-01-01T00-00-user-OxBadJ")
	require.NoError(t, os.MkdirAll(sessionPath, 0755))

	// write truncated JSON
	require.NoError(t, os.WriteFile(filepath.Join(sessionPath, recordingFile), []byte(`{"agent_id": "Ox`), 0600))

	state, err := LoadRecordingState(projectRoot)
	require.NoError(t, err, "corrupt JSON should not return an error")
	assert.Nil(t, state, "corrupt JSON should return nil state")
}

func TestLoadRecordingState_EmptyFile(t *testing.T) {
	cacheDir := t.TempDir()
	projectRoot, sessionsBase := setupRecordingTestWithSessionsBase(t, cacheDir)
	sessionPath := filepath.Join(sessionsBase, "2026-01-01T00-00-user-OxMtyF")
	require.NoError(t, os.MkdirAll(sessionPath, 0755))

	// write empty file
	require.NoError(t, os.WriteFile(filepath.Join(sessionPath, recordingFile), []byte{}, 0600))

	state, err := LoadRecordingState(projectRoot)
	require.NoError(t, err, "empty file should not return an error")
	assert.Nil(t, state, "empty file should return nil state")
}

func TestLoadAllRecordingStates_MixedValidAndCorrupt(t *testing.T) {
	cacheDir := t.TempDir()
	projectRoot, sessionsBase := setupRecordingTestWithSessionsBase(t, cacheDir)

	// create a valid session
	validSessionPath := filepath.Join(sessionsBase, "2026-01-01T00-00-user-OxGood")
	require.NoError(t, os.MkdirAll(validSessionPath, 0755))
	validState := &RecordingState{
		AgentID:     "OxGood",
		StartedAt:   time.Now(),
		SessionPath: validSessionPath,
	}
	validData, err := json.Marshal(validState)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(validSessionPath, recordingFile), validData, 0600))

	// create a corrupt session
	corruptSessionPath := filepath.Join(sessionsBase, "2026-01-01T00-00-user-OxBad1")
	require.NoError(t, os.MkdirAll(corruptSessionPath, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(corruptSessionPath, recordingFile), []byte(`{corrupted`), 0600))

	states, err := LoadAllRecordingStates(projectRoot)
	require.NoError(t, err, "mixed valid/corrupt should not return an error")
	require.Len(t, states, 1, "should return only the valid recording state")
	assert.Equal(t, "OxGood", states[0].AgentID)
}

// --- P0: Recording state corruption resilience ---

func TestLoadRecordingState_TruncatedJSON(t *testing.T) {
	tmpDir := t.TempDir()
	sessionsDir := filepath.Join(tmpDir, "sessions")
	sessionPath := filepath.Join(sessionsDir, "2026-01-01T00-00-user-OxCrash")
	require.NoError(t, os.MkdirAll(sessionPath, 0755))

	// write truncated JSON simulating crash during write
	require.NoError(t, os.WriteFile(
		filepath.Join(sessionPath, recordingFile),
		[]byte(`{"agent_id":"Ox`), 0600))

	state, err := LoadRecordingState(tmpDir)
	// truncated JSON must never produce a partial state
	if err != nil {
		assert.Contains(t, err.Error(), "parse", "error should mention parsing")
		assert.Nil(t, state, "state must be nil when parse fails")
		return
	}
	assert.Nil(t, state, "truncated JSON must not produce a partial state")
}

func TestSaveRecordingState_PersistsAndLeavesNoArtifacts(t *testing.T) {
	tmpDir := t.TempDir()
	sessionPath := filepath.Join(tmpDir, "sessions", "2026-01-01T00-00-user-OxAtomic")

	state := &RecordingState{
		AgentID:     "OxAtomic",
		StartedAt:   time.Now(),
		SessionPath: sessionPath,
		ParentPID:   os.Getpid(),
	}

	require.NoError(t, SaveRecordingState(tmpDir, state))

	// verify only the recording file exists — no temp artifacts
	entries, err := os.ReadDir(sessionPath)
	require.NoError(t, err)
	for _, e := range entries {
		assert.False(t, strings.HasSuffix(e.Name(), ".tmp"),
			"temp file should not remain after successful save: %s", e.Name())
	}

	// verify the state round-trips correctly
	loaded, err := LoadRecordingStateForAgent(tmpDir, "OxAtomic")
	require.NoError(t, err)
	require.NotNil(t, loaded)
	assert.Equal(t, "OxAtomic", loaded.AgentID)
	assert.Equal(t, state.SessionPath, loaded.SessionPath)
}
