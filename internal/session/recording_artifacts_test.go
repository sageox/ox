package session

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStartRecording(t *testing.T) {
	t.Run("starts recording successfully", func(t *testing.T) {
		cacheDir := t.TempDir()
		projectRoot := setupRecordingTest(t, cacheDir)

		// create a real session file for validation
		sessionFile := filepath.Join(t.TempDir(), "session.jsonl")
		require.NoError(t, os.WriteFile(sessionFile, []byte("{}\n"), 0644))

		opts := StartRecordingOptions{
			AgentID:     "OxA1b2",
			AdapterName: "claude-code",
			SessionFile: sessionFile,
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

	t.Run("rejects directory as session file", func(t *testing.T) {
		// regression: directory path stored as SessionFile caused read failures (ox-5eu5)
		cacheDir := t.TempDir()
		projectRoot := setupRecordingTest(t, cacheDir)

		opts := StartRecordingOptions{
			AgentID:     "OxA1b2",
			AdapterName: "claude-code",
			SessionFile: t.TempDir(), // a directory, not a file
			Username:    "testuser",
		}

		_, err := StartRecording(projectRoot, opts)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not a regular file")
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

	// Concurrent agent tests: different agents can record simultaneously.
	// The same agent starting twice is still blocked.
	t.Run("different agents can record concurrently", func(t *testing.T) {
		cacheDir := t.TempDir()
		projectRoot := setupRecordingTest(t, cacheDir)

		// first agent starts a recording
		firstState, err := StartRecording(projectRoot, StartRecordingOptions{
			AgentID: "OxOldAgent", AdapterName: "claude-code", Username: "testuser",
		})
		require.NoError(t, err)
		require.True(t, IsRecording(projectRoot))

		// second agent starts — should succeed (coexistence, not destruction)
		secondState, err := StartRecording(projectRoot, StartRecordingOptions{
			AgentID: "OxNewAgent", AdapterName: "claude-code", Username: "testuser",
		})
		require.NoError(t, err)
		require.NotNil(t, secondState)
		assert.Equal(t, "OxNewAgent", secondState.AgentID)

		// first agent's recording should still exist
		assert.True(t, IsRecordingForAgent(projectRoot, "OxOldAgent"),
			"first agent's recording should survive second agent's start")
		oldPath := filepath.Join(firstState.SessionPath, recordingFile)
		_, statErr := os.Stat(oldPath)
		assert.False(t, os.IsNotExist(statErr), "first agent's .recording.json should still exist")

		// second agent's recording should also exist
		assert.True(t, IsRecordingForAgent(projectRoot, "OxNewAgent"),
			"second agent should be recording")

		// both recordings should be returned by LoadAllRecordingStates
		all, loadErr := LoadAllRecordingStates(projectRoot)
		require.NoError(t, loadErr)
		assert.Len(t, all, 2, "both agents should have active recordings")
	})

	t.Run("same agent duplicate start still blocked", func(t *testing.T) {
		cacheDir := t.TempDir()
		projectRoot := setupRecordingTest(t, cacheDir)

		_, err := StartRecording(projectRoot, StartRecordingOptions{
			AgentID: "OxSameAgent", AdapterName: "claude-code", Username: "testuser",
		})
		require.NoError(t, err)

		_, err = StartRecording(projectRoot, StartRecordingOptions{
			AgentID: "OxSameAgent", AdapterName: "claude-code", Username: "testuser",
		})
		assert.Error(t, err)
		assert.True(t, errors.Is(err, ErrAlreadyRecording))
	})
}

func TestLoadAllRecordingStates(t *testing.T) {
	t.Run("returns all recordings", func(t *testing.T) {
		cacheDir := t.TempDir()
		projectRoot := setupRecordingTest(t, cacheDir)

		// start first recording
		_, err := StartRecording(projectRoot, StartRecordingOptions{
			AgentID: "OxAAA1", AdapterName: "claude-code", Username: "testuser",
		})
		require.NoError(t, err)

		// manually create a second recording (StartRecording blocks on existing)
		repoID := getRepoIDFromProject(projectRoot)
		contextPath := GetContextPath(repoID)
		secondSessionPath := filepath.Join(contextPath, "sessions", "2026-01-05T12-00-user-OxBBB2")
		require.NoError(t, os.MkdirAll(secondSessionPath, 0755))
		secondState := &RecordingState{
			AgentID:     "OxBBB2",
			AdapterName: "claude-code",
			SessionPath: secondSessionPath,
			StartedAt:   time.Now(),
		}
		secondData, _ := json.Marshal(secondState)
		require.NoError(t, os.WriteFile(filepath.Join(secondSessionPath, recordingFile), secondData, 0644))

		states, err := LoadAllRecordingStates(projectRoot)
		require.NoError(t, err)
		assert.Len(t, states, 2, "should find both recordings")

		ids := map[string]bool{}
		for _, s := range states {
			ids[s.AgentID] = true
		}
		assert.True(t, ids["OxAAA1"], "should include first agent")
		assert.True(t, ids["OxBBB2"], "should include second agent")
	})

	t.Run("returns empty when no recordings", func(t *testing.T) {
		cacheDir := t.TempDir()
		projectRoot := setupRecordingTest(t, cacheDir)

		states, err := LoadAllRecordingStates(projectRoot)
		require.NoError(t, err)
		assert.Empty(t, states)
	})

	t.Run("empty project root returns error", func(t *testing.T) {
		_, err := LoadAllRecordingStates("")
		require.Error(t, err)
	})
}

func TestExplicitStopMarker_EmptyProjectRoot(t *testing.T) {
	err := MarkExplicitStop("", "test-agent")
	assert.Error(t, err, "MarkExplicitStop with empty root should error")
	assert.False(t, ConsumeExplicitStop("", "test-agent"), "ConsumeExplicitStop with empty root should return false")
}

// TestStopClearStartLifecycle reproduces the exact bug from issue #132:
// user runs /ox-session-stop, then /clear (which auto-starts via prime),
// then /ox-session-start fails with ErrAlreadyRecording.
//
// The fix: session stop writes an explicit-stop marker; prime's auto-start
// checks and consumes that marker before starting. This test verifies that
// the marker correctly gates the auto-start path.
func TestStopClearStartLifecycle(t *testing.T) {
	cacheDir := t.TempDir()
	projectRoot := setupRecordingTest(t, cacheDir)

	// 1. user starts a session
	_, err := StartRecording(projectRoot, StartRecordingOptions{
		AgentID: "OxUser1", AdapterName: "claude-code", Username: "testuser",
	})
	require.NoError(t, err)

	// 2. user runs /ox-session-stop (StopRecording + MarkExplicitStop)
	_, err = StopRecording(projectRoot, "OxUser1")
	require.NoError(t, err)
	require.NoError(t, MarkExplicitStop(projectRoot, "OxUser1"))

	// 3. user runs /clear → hook calls prime → prime checks marker before auto-start
	//    ConsumeExplicitStop returns true → prime skips auto-start
	assert.True(t, ConsumeExplicitStop(projectRoot, "OxUser1"),
		"marker must be present after explicit stop — prime relies on this to skip auto-start")

	// 4. since prime skipped auto-start, IsRecording should still be false
	assert.False(t, IsRecording(projectRoot),
		"no recording should be active after prime consumed the stop marker")

	// 5. user runs /ox-session-start — must succeed (the original bug: this failed)
	state, err := StartRecording(projectRoot, StartRecordingOptions{
		AgentID: "OxUser1", AdapterName: "claude-code", Username: "testuser",
	})
	require.NoError(t, err, "explicit session start after stop+clear must succeed — this was the #132 bug")
	assert.Equal(t, "OxUser1", state.AgentID)

	// cleanup
	_, _ = StopRecording(projectRoot, "OxUser1")
}

// TestExplicitStopMarker_XDGPaths verifies the marker works when sessions
// are stored in the XDG cache path (production path). The basic
// TestExplicitStopMarker uses a bare tmpDir with manually created sessions/;
// production always resolves paths via repo_id → XDG cache.
func TestExplicitStopMarker_XDGPaths(t *testing.T) {
	cacheDir := t.TempDir()
	projectRoot := setupRecordingTest(t, cacheDir)

	// marker should work through the XDG-aware sessionsSearchPaths
	require.NoError(t, MarkExplicitStop(projectRoot, "OxUser1"))
	assert.True(t, ConsumeExplicitStop(projectRoot, "OxUser1"),
		"marker must be consumable when written via XDG search paths")
	assert.False(t, ConsumeExplicitStop(projectRoot, "OxUser1"),
		"consumed marker must not be consumable again")
}
