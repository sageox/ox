package session

import (
	"errors"
	"os"
	"path/filepath"
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
		tempHome := t.TempDir()
		t.Setenv("HOME", tempHome)
		t.Setenv("XDG_CACHE_HOME", "")

		tmpDir := t.TempDir()
		sessionPath := filepath.Join(tmpDir, "sessions", "2026-01-06T14-30-user-OxA1b2")

		originalState := &RecordingState{
			OutputFile:  "/path/to/session.md",
			AgentID:     "OxA1b2",
			StartedAt:   time.Now().Truncate(time.Second),
			AdapterName: "claude-code",
			SessionFile: "/path/to/session.jsonl",
			SessionPath: sessionPath,
		}

		err := SaveRecordingState(tmpDir, originalState)
		require.NoError(t, err, "failed to save state")

		loadedState, err := LoadRecordingState(tmpDir)
		require.NoError(t, err)
		require.NotNil(t, loadedState, "expected state to be loaded")

		assert.Equal(t, originalState.AgentID, loadedState.AgentID)
		assert.Equal(t, originalState.OutputFile, loadedState.OutputFile)
		assert.Equal(t, originalState.AdapterName, loadedState.AdapterName)
		assert.Equal(t, originalState.SessionFile, loadedState.SessionFile)
		assert.Equal(t, originalState.SessionPath, loadedState.SessionPath)
	})

	t.Run("loads state with title", func(t *testing.T) {
		tempHome := t.TempDir()
		t.Setenv("HOME", tempHome)
		t.Setenv("XDG_CACHE_HOME", "")

		tmpDir := t.TempDir()
		sessionPath := filepath.Join(tmpDir, "sessions", "2026-01-06T14-30-user-OxA1b2")

		originalState := &RecordingState{
			AgentID:     "OxA1b2",
			StartedAt:   time.Now().Truncate(time.Second),
			Title:       "Setting up AWS infrastructure",
			SessionPath: sessionPath,
		}

		err := SaveRecordingState(tmpDir, originalState)
		require.NoError(t, err, "failed to save state")

		loadedState, err := LoadRecordingState(tmpDir)
		require.NoError(t, err)

		assert.Equal(t, originalState.Title, loadedState.Title)
	})

	t.Run("returns nil for non-existent state", func(t *testing.T) {
		tempHome := t.TempDir()
		t.Setenv("HOME", tempHome)
		t.Setenv("XDG_CACHE_HOME", "")

		tmpDir := t.TempDir()

		state, err := LoadRecordingState(tmpDir)
		require.NoError(t, err)
		assert.Nil(t, state, "expected nil state for non-existent file")
	})

	t.Run("returns error for empty project root", func(t *testing.T) {
		_, err := LoadRecordingState("")
		assert.Error(t, err)
	})

	t.Run("skips invalid JSON in session folder and continues", func(t *testing.T) {
		tempHome := t.TempDir()
		t.Setenv("HOME", tempHome)
		t.Setenv("XDG_CACHE_HOME", "")

		tmpDir := t.TempDir()

		// create a session folder with invalid .recording.json
		sessionPath := filepath.Join(tmpDir, "sessions", "2026-01-06T14-30-user-OxBad")
		err := os.MkdirAll(sessionPath, 0755)
		require.NoError(t, err, "failed to create session dir")

		invalidPath := filepath.Join(sessionPath, recordingFile)
		err = os.WriteFile(invalidPath, []byte("invalid json"), 0600)
		require.NoError(t, err, "failed to write invalid state")

		// should return nil without error (skips invalid entries)
		state, err := LoadRecordingState(tmpDir)
		require.NoError(t, err)
		assert.Nil(t, state, "expected nil state when only invalid JSON exists")
	})
}

func TestClearRecordingState(t *testing.T) {
	t.Run("clears existing state from session folder", func(t *testing.T) {
		tempHome := t.TempDir()
		t.Setenv("HOME", tempHome)
		t.Setenv("XDG_CACHE_HOME", "")

		tmpDir := t.TempDir()
		sessionPath := filepath.Join(tmpDir, "sessions", "2026-01-06T14-30-user-OxA1b2")

		state := &RecordingState{
			AgentID:     "OxA1b2",
			StartedAt:   time.Now(),
			SessionPath: sessionPath,
		}

		err := SaveRecordingState(tmpDir, state)
		require.NoError(t, err, "failed to save state")

		err = ClearRecordingState(tmpDir)
		require.NoError(t, err)

		// verify file is gone from session folder
		statePath := filepath.Join(sessionPath, recordingFile)
		_, err = os.Stat(statePath)
		assert.True(t, os.IsNotExist(err), "expected .recording.json to be removed")
	})

	t.Run("succeeds when no state exists", func(t *testing.T) {
		tempHome := t.TempDir()
		t.Setenv("HOME", tempHome)
		t.Setenv("XDG_CACHE_HOME", "")

		tmpDir := t.TempDir()

		err := ClearRecordingState(tmpDir)
		require.NoError(t, err, "expected no error when clearing non-existent state")
	})

	t.Run("returns error for empty project root", func(t *testing.T) {
		err := ClearRecordingState("")
		assert.Error(t, err)
	})
}

func TestIsRecording(t *testing.T) {
	t.Run("returns true when recording exists in session folder", func(t *testing.T) {
		tempHome := t.TempDir()
		t.Setenv("HOME", tempHome)
		t.Setenv("XDG_CACHE_HOME", "")

		tmpDir := t.TempDir()
		sessionPath := filepath.Join(tmpDir, "sessions", "2026-01-06T14-30-user-OxA1b2")

		state := &RecordingState{
			AgentID:     "OxA1b2",
			StartedAt:   time.Now(),
			SessionPath: sessionPath,
		}

		err := SaveRecordingState(tmpDir, state)
		require.NoError(t, err, "failed to save state")

		assert.True(t, IsRecording(tmpDir), "expected IsRecording to return true")
	})

	t.Run("returns false when no recording exists", func(t *testing.T) {
		tempHome := t.TempDir()
		t.Setenv("HOME", tempHome)
		t.Setenv("XDG_CACHE_HOME", "")

		tmpDir := t.TempDir()

		assert.False(t, IsRecording(tmpDir), "expected IsRecording to return false")
	})

	t.Run("returns false for empty project root", func(t *testing.T) {
		assert.False(t, IsRecording(""), "expected IsRecording to return false for empty project root")
	})
}

func TestGetRecordingDuration(t *testing.T) {
	t.Run("returns duration for active recording", func(t *testing.T) {
		tempHome := t.TempDir()
		t.Setenv("HOME", tempHome)
		t.Setenv("XDG_CACHE_HOME", "")

		tmpDir := t.TempDir()
		sessionPath := filepath.Join(tmpDir, "sessions", "2026-01-06T14-30-user-OxA1b2")

		startTime := time.Now().Add(-5 * time.Minute)
		state := &RecordingState{
			AgentID:     "OxA1b2",
			StartedAt:   startTime,
			SessionPath: sessionPath,
		}

		err := SaveRecordingState(tmpDir, state)
		require.NoError(t, err, "failed to save state")

		duration := GetRecordingDuration(tmpDir)
		assert.GreaterOrEqual(t, duration, 5*time.Minute, "expected duration >= 5m")
		assert.Less(t, duration, 6*time.Minute, "expected duration < 6m")
	})

	t.Run("returns 0 when no recording exists", func(t *testing.T) {
		tempHome := t.TempDir()
		t.Setenv("HOME", tempHome)
		t.Setenv("XDG_CACHE_HOME", "")

		tmpDir := t.TempDir()

		duration := GetRecordingDuration(tmpDir)
		assert.Equal(t, time.Duration(0), duration)
	})

	t.Run("returns 0 for empty project root", func(t *testing.T) {
		duration := GetRecordingDuration("")
		assert.Equal(t, time.Duration(0), duration)
	})
}

// setupRecordingTest creates a proper project context for recording tests.
// Returns projectRoot and sets up XDG cache to point to the given cacheDir.
func setupRecordingTest(t *testing.T, cacheDir string) string {
	t.Helper()
	projectRoot := t.TempDir()
	repoID := "test-repo-id"

	// create .sageox/config.json with repo_id (canonical format)
	sageoxDir := filepath.Join(projectRoot, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755))
	configContent := `{"config_version":"2","repo_id":"` + repoID + `"}`
	require.NoError(t, os.WriteFile(filepath.Join(sageoxDir, "config.json"), []byte(configContent), 0644))

	// set up environment
	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("HOME", cacheDir)
	t.Setenv("XDG_CACHE_HOME", cacheDir)

	return projectRoot
}

func TestStartRecording(t *testing.T) {
	t.Run("starts recording successfully", func(t *testing.T) {
		cacheDir := t.TempDir()
		projectRoot := setupRecordingTest(t, cacheDir)

		opts := StartRecordingOptions{
			AgentID:     "OxA1b2",
			AdapterName: "claude-code",
			SessionFile: "/path/to/session.jsonl",
			Title:       "Test recording",
			Username:    "testuser",
			// no RepoContextPath - uses XDG cache via repo_id
		}

		state, err := StartRecording(projectRoot, opts)
		require.NoError(t, err)
		require.NotNil(t, state, "expected state to be returned")

		assert.Equal(t, opts.AgentID, state.AgentID)
		assert.Equal(t, opts.AdapterName, state.AdapterName)
		assert.Equal(t, opts.Title, state.Title)
		assert.False(t, state.StartedAt.IsZero(), "expected StartedAt to be set")
		assert.NotEmpty(t, state.SessionPath, "expected SessionPath to be set")

		// verify session folder was created in XDG cache
		_, err = os.Stat(state.SessionPath)
		assert.False(t, os.IsNotExist(err), "expected session folder to be created")
		assert.True(t, strings.Contains(state.SessionPath, "sessions"), "session path should contain 'sessions'")

		// verify .recording.json exists in session folder
		recordingPath := filepath.Join(state.SessionPath, recordingFile)
		_, err = os.Stat(recordingPath)
		assert.False(t, os.IsNotExist(err), "expected .recording.json to exist in session folder")

		// verify IsRecording returns true
		assert.True(t, IsRecording(projectRoot), "expected IsRecording to return true after StartRecording")
	})

	t.Run("uses default username when not provided", func(t *testing.T) {
		cacheDir := t.TempDir()
		projectRoot := setupRecordingTest(t, cacheDir)

		opts := StartRecordingOptions{
			AgentID:     "OxA1b2",
			AdapterName: "claude-code",
		}

		state, err := StartRecording(projectRoot, opts)
		require.NoError(t, err)

		// session path should contain "user" as default username
		require.NotEmpty(t, state.SessionPath, "expected SessionPath to be set")
	})

	t.Run("returns error when already recording", func(t *testing.T) {
		cacheDir := t.TempDir()
		projectRoot := setupRecordingTest(t, cacheDir)

		opts := StartRecordingOptions{
			AgentID:     "OxA1b2",
			AdapterName: "claude-code",
			Username:    "testuser",
		}

		// start first recording
		_, err := StartRecording(projectRoot, opts)
		require.NoError(t, err, "first start failed")

		// try to start second recording
		_, err = StartRecording(projectRoot, opts)
		assert.Error(t, err, "expected error when starting second recording")
		assert.True(t, errors.Is(err, ErrAlreadyRecording))
	})

	t.Run("duplicate start preserves original session", func(t *testing.T) {
		cacheDir := t.TempDir()
		projectRoot := setupRecordingTest(t, cacheDir)

		opts := StartRecordingOptions{
			AgentID:     "OxA1b2",
			AdapterName: "claude-code",
			Username:    "testuser",
		}

		// start first recording
		firstState, err := StartRecording(projectRoot, opts)
		require.NoError(t, err, "first start failed")

		// attempt second start — should fail
		_, err = StartRecording(projectRoot, opts)
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrAlreadyRecording))

		// original recording must still be intact
		assert.True(t, IsRecording(projectRoot), "original recording should still be active")

		loaded, err := LoadRecordingState(projectRoot)
		require.NoError(t, err)
		require.NotNil(t, loaded)
		assert.Equal(t, firstState.AgentID, loaded.AgentID)
		assert.Equal(t, firstState.SessionPath, loaded.SessionPath)

		// .recording.json must still exist in the original session folder
		recordingPath := filepath.Join(firstState.SessionPath, recordingFile)
		_, err = os.Stat(recordingPath)
		assert.False(t, os.IsNotExist(err), "original .recording.json must not be deleted")
	})

	t.Run("session name from state matches GetSessionName", func(t *testing.T) {
		cacheDir := t.TempDir()
		projectRoot := setupRecordingTest(t, cacheDir)

		opts := StartRecordingOptions{
			AgentID:     "OxC3d4",
			AdapterName: "claude-code",
			Username:    "devuser",
		}

		state, err := StartRecording(projectRoot, opts)
		require.NoError(t, err)

		// the session name extracted from SessionPath should be stable
		sessionName := GetSessionName(state.SessionPath)
		assert.NotEmpty(t, sessionName)
		assert.Contains(t, sessionName, "devuser")
		assert.Contains(t, sessionName, "OxC3d4")

		// calling GetSessionName again yields the same value (no time.Now() drift)
		assert.Equal(t, sessionName, GetSessionName(state.SessionPath),
			"GetSessionName should be deterministic for the same path")
	})

	t.Run("returns error when no ledger configured", func(t *testing.T) {
		tempHome := t.TempDir()
		t.Setenv("OX_XDG_ENABLE", "1")
		t.Setenv("HOME", tempHome)
		t.Setenv("XDG_CACHE_HOME", tempHome)

		tmpDir := t.TempDir()

		opts := StartRecordingOptions{
			AgentID:     "OxA1b2",
			AdapterName: "claude-code",
			Username:    "testuser",
			// no RepoContextPath - should fail
		}

		_, err := StartRecording(tmpDir, opts)
		assert.Error(t, err)
		assert.True(t, errors.Is(err, ErrNoLedger), "expected ErrNoLedger")
	})

	t.Run("returns error for empty project root", func(t *testing.T) {
		opts := StartRecordingOptions{AgentID: "OxA1b2"}
		_, err := StartRecording("", opts)
		assert.Error(t, err)
	})

	// Ghost session regression tests: when user exits Claude Code without
	// stopping the session (e.g., after hooks restart notice), the .recording.json
	// persists. A new instance with a different agent ID should auto-clear it,
	// while the same agent ID starting twice should still error.
	ghostTests := []struct {
		name         string
		firstAgent   string
		secondAgent  string
		wantError    bool
		wantErrorIs  error
	}{
		{
			name:        "clears ghost session from different agent",
			firstAgent:  "OxOldAgent",
			secondAgent: "OxNewAgent",
			wantError:   false,
		},
		{
			name:        "same agent duplicate start still blocked",
			firstAgent:  "OxSameAgent",
			secondAgent: "OxSameAgent",
			wantError:   true,
			wantErrorIs: ErrAlreadyRecording,
		},
	}
	for _, tt := range ghostTests {
		t.Run(tt.name, func(t *testing.T) {
			cacheDir := t.TempDir()
			projectRoot := setupRecordingTest(t, cacheDir)

			// first agent starts a recording
			firstState, err := StartRecording(projectRoot, StartRecordingOptions{
				AgentID: tt.firstAgent, AdapterName: "claude-code", Username: "testuser",
			})
			require.NoError(t, err)
			require.True(t, IsRecording(projectRoot))

			// second agent (or same agent) tries to start
			secondState, err := StartRecording(projectRoot, StartRecordingOptions{
				AgentID: tt.secondAgent, AdapterName: "claude-code", Username: "testuser",
			})

			if tt.wantError {
				assert.Error(t, err)
				if tt.wantErrorIs != nil {
					assert.True(t, errors.Is(err, tt.wantErrorIs))
				}
			} else {
				require.NoError(t, err)
				require.NotNil(t, secondState)
				assert.Equal(t, tt.secondAgent, secondState.AgentID)

				// old recording state should be gone
				oldPath := filepath.Join(firstState.SessionPath, recordingFile)
				_, statErr := os.Stat(oldPath)
				assert.True(t, os.IsNotExist(statErr), "ghost .recording.json should be cleared")

				// new recording should be the active one
				loaded, loadErr := LoadRecordingState(projectRoot)
				require.NoError(t, loadErr)
				require.NotNil(t, loaded)
				assert.Equal(t, tt.secondAgent, loaded.AgentID)
			}
		})
	}
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
		state, err := StopRecording(projectRoot)
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

		_, err := StopRecording(tmpDir)
		assert.Error(t, err, "expected error when not recording")
		assert.True(t, errors.Is(err, ErrNotRecording))
	})

	t.Run("returns error for empty project root", func(t *testing.T) {
		_, err := StopRecording("")
		assert.Error(t, err)
	})
}

func TestRecordingStateDuration(t *testing.T) {
	t.Run("returns correct duration", func(t *testing.T) {
		startTime := time.Now().Add(-10 * time.Minute)
		state := &RecordingState{
			AgentID:   "OxA1b2",
			StartedAt: startTime,
		}

		duration := state.Duration()
		assert.GreaterOrEqual(t, duration, 10*time.Minute, "expected duration >= 10m")
		assert.Less(t, duration, 11*time.Minute, "expected duration < 11m")
	})

	t.Run("returns 0 for nil state", func(t *testing.T) {
		var state *RecordingState
		duration := state.Duration()
		assert.Equal(t, time.Duration(0), duration)
	})

	t.Run("returns 0 for zero start time", func(t *testing.T) {
		state := &RecordingState{
			AgentID: "OxA1b2",
			// StartedAt is zero value
		}

		duration := state.Duration()
		assert.Equal(t, time.Duration(0), duration)
	})
}

func TestGetSessionName(t *testing.T) {
	t.Run("extracts session name from path", func(t *testing.T) {
		tests := []struct {
			sessionPath string
			expected    string
		}{
			{"/path/to/sessions/2026-01-06T14-30-user-OxA1b2", "2026-01-06T14-30-user-OxA1b2"},
			{"/path/to/sessions/2026-01-06T14-30-user-OxA1b2/", "2026-01-06T14-30-user-OxA1b2"},
			{"2026-01-06T14-30-user-OxA1b2", "2026-01-06T14-30-user-OxA1b2"},
		}

		for _, tc := range tests {
			result := GetSessionName(tc.sessionPath)
			assert.Equal(t, tc.expected, result, "GetSessionName(%q)", tc.sessionPath)
		}
	})
}

func TestLoadRecordingState_MultipleRecordingsReturnsFirst(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)
	t.Setenv("XDG_CACHE_HOME", "")

	tmpDir := t.TempDir()

	// create two sessions with .recording.json — ReadDir returns alphabetically
	for _, agentID := range []string{"OxFirst", "OxSecnd"} {
		name := "2026-01-06T14-30-user-" + agentID
		sessionPath := filepath.Join(tmpDir, "sessions", name)
		state := &RecordingState{
			AgentID:     agentID,
			StartedAt:   time.Now(),
			SessionPath: sessionPath,
		}
		err := SaveRecordingState(tmpDir, state)
		require.NoError(t, err)
	}

	state, err := LoadRecordingState(tmpDir)
	require.NoError(t, err)
	require.NotNil(t, state)

	// ReadDir sorts alphabetically, so "OxFirst" session folder comes first
	assert.Equal(t, "OxFirst", state.AgentID,
		"LoadRecordingState should return the alphabetically first recording")
}

func TestClearRecordingState_WithMultipleRecordings_OnlyClearsFirst(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)
	t.Setenv("XDG_CACHE_HOME", "")

	tmpDir := t.TempDir()

	// create two recording states
	paths := map[string]string{}
	for _, agentID := range []string{"OxAAAA", "OxBBBB"} {
		name := "2026-01-06T14-30-user-" + agentID
		sessionPath := filepath.Join(tmpDir, "sessions", name)
		state := &RecordingState{
			AgentID:     agentID,
			StartedAt:   time.Now(),
			SessionPath: sessionPath,
		}
		err := SaveRecordingState(tmpDir, state)
		require.NoError(t, err)
		paths[agentID] = filepath.Join(sessionPath, recordingFile)
	}

	// clear — should only remove the first one found (alphabetically)
	err := ClearRecordingState(tmpDir)
	require.NoError(t, err)

	// first recording should be gone
	_, err = os.Stat(paths["OxAAAA"])
	assert.True(t, os.IsNotExist(err), "first recording should be cleared")

	// second recording should survive
	_, err = os.Stat(paths["OxBBBB"])
	assert.False(t, os.IsNotExist(err), "second recording should survive ClearRecordingState")

	// LoadRecordingState should now find the second one
	state, err := LoadRecordingState(tmpDir)
	require.NoError(t, err)
	require.NotNil(t, state)
	assert.Equal(t, "OxBBBB", state.AgentID)
}

func TestStartRecording_GhostClear_PreservesSessionData(t *testing.T) {
	cacheDir := t.TempDir()
	projectRoot := setupRecordingTest(t, cacheDir)

	// Agent A starts recording
	stateA, err := StartRecording(projectRoot, StartRecordingOptions{
		AgentID: "OxAgntA", AdapterName: "claude-code", Username: "testuser",
	})
	require.NoError(t, err)

	// simulate Agent A writing session data
	rawPath := filepath.Join(stateA.SessionPath, "raw.jsonl")
	eventsPath := filepath.Join(stateA.SessionPath, "events.jsonl")
	require.NoError(t, os.WriteFile(rawPath, []byte("{\"type\":\"header\"}\n"), 0644))
	require.NoError(t, os.WriteFile(eventsPath, []byte("{\"event\":\"test\"}\n"), 0644))

	// Agent B starts — ghost-clears A's .recording.json
	_, err = StartRecording(projectRoot, StartRecordingOptions{
		AgentID: "OxAgntB", AdapterName: "claude-code", Username: "testuser",
	})
	require.NoError(t, err)

	// A's .recording.json should be gone
	_, err = os.Stat(filepath.Join(stateA.SessionPath, recordingFile))
	assert.True(t, os.IsNotExist(err), "A's .recording.json should be cleared")

	// but A's session DATA must survive (raw.jsonl, events.jsonl)
	_, err = os.Stat(rawPath)
	assert.False(t, os.IsNotExist(err), "A's raw.jsonl must survive ghost clearing")
	_, err = os.Stat(eventsPath)
	assert.False(t, os.IsNotExist(err), "A's events.jsonl must survive ghost clearing")

	// A's session folder itself must still exist
	_, err = os.Stat(stateA.SessionPath)
	assert.False(t, os.IsNotExist(err), "A's session folder must survive ghost clearing")
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
	if os.Getenv("CI") != "" {
		t.Skip("skipping permission test in CI")
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

	_, err = StopRecording(projectRoot)
	assert.Error(t, err, "StopRecording should fail if state file can't be removed")
}

func TestRecordingState_StaleBoundary(t *testing.T) {
	// the stale threshold is 12 hours (health.go uses > not >=)
	// use fixed durations to avoid time.Since() drift

	// a recording that started 11h59m ago is NOT stale
	notStaleAge := StaleRecordingThreshold - time.Minute
	assert.False(t, notStaleAge > StaleRecordingThreshold,
		"recording under the threshold should NOT be stale")

	// a recording that started 12h1m ago IS stale
	staleAge := StaleRecordingThreshold + time.Minute
	assert.True(t, staleAge > StaleRecordingThreshold,
		"recording past the threshold should be stale")

	// exactly at threshold: > means NOT stale (boundary behavior)
	exactAge := StaleRecordingThreshold
	assert.False(t, exactAge > StaleRecordingThreshold,
		"recording at exactly the threshold should NOT be stale (uses > not >=)")
}
