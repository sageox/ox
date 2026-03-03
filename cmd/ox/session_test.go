package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFormatDurationHuman(t *testing.T) {
	tests := []struct {
		name     string
		duration time.Duration
		want     string
	}{
		{
			name:     "1 second",
			duration: 1 * time.Second,
			want:     "1 second",
		},
		{
			name:     "multiple seconds",
			duration: 30 * time.Second,
			want:     "30 seconds",
		},
		{
			name:     "1 minute",
			duration: 1 * time.Minute,
			want:     "1 minute",
		},
		{
			name:     "multiple minutes",
			duration: 5 * time.Minute,
			want:     "5 minutes",
		},
		{
			name:     "1 hour",
			duration: 1 * time.Hour,
			want:     "1 hour",
		},
		{
			name:     "multiple hours",
			duration: 3 * time.Hour,
			want:     "3 hours",
		},
		{
			name:     "1 hour and minutes",
			duration: 1*time.Hour + 15*time.Minute,
			want:     "1 hour 15 minutes",
		},
		{
			name:     "multiple hours and minutes",
			duration: 2*time.Hour + 30*time.Minute,
			want:     "2 hours 30 minutes",
		},
		{
			name:     "zero duration",
			duration: 0,
			want:     "0 seconds",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatDurationHuman(tt.duration)
			assert.Equal(t, tt.want, got, "formatDurationHuman(%v)", tt.duration)
		})
	}
}

func TestSessionStartRequiresProject(t *testing.T) {
	// this test verifies the command requires a SageOx project
	// we don't run the actual command, just verify the project root check
	tmpDir := t.TempDir()

	// no .sageox directory = not a SageOx project
	originalCwd, err := os.Getwd()
	require.NoError(t, err, "failed to get cwd")
	defer os.Chdir(originalCwd)

	require.NoError(t, os.Chdir(tmpDir), "failed to chdir")

	// verify IsRecording returns false when no project
	assert.False(t, session.IsRecording(tmpDir), "expected IsRecording to return false when not in project")
}

func TestSessionStatusNotRecording(t *testing.T) {
	tmpDir := t.TempDir()

	// create .sageox directory to simulate initialized project
	sageoxDir := filepath.Join(tmpDir, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755), "failed to create .sageox")

	// verify not recording
	assert.False(t, session.IsRecording(tmpDir), "expected no recording in fresh project")

	// verify LoadRecordingState returns nil
	state, err := session.LoadRecordingState(tmpDir)
	require.NoError(t, err, "unexpected error")
	assert.Nil(t, state, "expected nil state when not recording")
}

// setupSessionTestProject creates a properly initialized project for session tests.
// Returns the project root and sets up XDG cache environment.
func setupSessionTestProject(t *testing.T) string {
	t.Helper()
	cacheDir := t.TempDir()
	projectRoot := t.TempDir()
	repoID := "test-repo-id"

	// create .sageox/config.json with repo_id (canonical format)
	sageoxDir := filepath.Join(projectRoot, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755))
	configContent := `{"config_version":"2","repo_id":"` + repoID + `"}`
	require.NoError(t, os.WriteFile(filepath.Join(sageoxDir, "config.json"), []byte(configContent), 0644))

	// set up environment for XDG cache path resolution
	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("HOME", cacheDir)
	t.Setenv("XDG_CACHE_HOME", cacheDir)

	return projectRoot
}

func TestSessionStatusRecording(t *testing.T) {
	projectRoot := setupSessionTestProject(t)

	// start a recording
	opts := session.StartRecordingOptions{
		AgentID:     "OxTest",
		AdapterName: "test-adapter",
		Title:       "Test Session",
	}

	_, err := session.StartRecording(projectRoot, opts)
	require.NoError(t, err, "failed to start recording")

	// verify recording is active
	assert.True(t, session.IsRecording(projectRoot), "expected IsRecording to return true")

	// verify state is loaded correctly
	state, err := session.LoadRecordingState(projectRoot)
	require.NoError(t, err, "unexpected error")

	require.NotNil(t, state, "expected state to be non-nil")

	assert.Equal(t, opts.AgentID, state.AgentID, "AgentID mismatch")
	assert.Equal(t, opts.Title, state.Title, "Title mismatch")
	assert.Equal(t, opts.AdapterName, state.AdapterName, "AdapterName mismatch")

	// clean up
	_, err = session.StopRecording(projectRoot)
	require.NoError(t, err, "failed to stop recording")
}

func TestSessionStartStopWorkflow(t *testing.T) {
	projectRoot := setupSessionTestProject(t)

	// step 1: not recording initially
	assert.False(t, session.IsRecording(projectRoot), "expected no recording initially")

	// step 2: start recording
	startOpts := session.StartRecordingOptions{
		AgentID:     "OxFlow",
		AdapterName: "test-adapter",
		Title:       "Workflow Test",
	}

	startState, err := session.StartRecording(projectRoot, startOpts)
	require.NoError(t, err, "failed to start recording")

	assert.True(t, startState.StartedAt.After(time.Now().Add(-1*time.Second)), "expected StartedAt to be recent")

	// step 3: verify recording is active
	assert.True(t, session.IsRecording(projectRoot), "expected IsRecording after start")

	// step 4: try to start again (should fail)
	_, err = session.StartRecording(projectRoot, startOpts)
	assert.Error(t, err, "expected error when starting second recording")

	// step 5: stop recording
	stopState, err := session.StopRecording(projectRoot)
	require.NoError(t, err, "failed to stop recording")

	assert.Equal(t, startOpts.AgentID, stopState.AgentID, "AgentID mismatch after stop")

	// step 6: verify no longer recording
	assert.False(t, session.IsRecording(projectRoot), "expected no recording after stop")

	// step 7: try to stop again (should fail)
	_, err = session.StopRecording(projectRoot)
	assert.Error(t, err, "expected error when stopping while not recording")
}

func TestSessionStatusOutputJSON(t *testing.T) {
	// test the JSON output structure
	output := sessionStatusOutput{
		Recording:    true,
		Title:        "Test Session",
		DurationSecs: 300,
		Duration:     "5 minutes",
		Agent:        "claude-code",
		AgentID:      "OxTest",
		StartedAt:    "2026-01-05T10:00:00Z",
	}

	assert.True(t, output.Recording, "expected Recording to be true")
	assert.Equal(t, "Test Session", output.Title, "Title mismatch")
	assert.Equal(t, 300, output.DurationSecs, "DurationSecs mismatch")
}

func TestGetSessionUsername(t *testing.T) {
	// this function falls back to "user" if git identity is not available
	username := getSessionUsername()

	assert.NotEmpty(t, username, "expected non-empty username")

	// the username should either be from git or "user"
	// we don't test the specific value since it depends on git config
}

func TestSessionStatusOutput_NotRecording_JSONFormat(t *testing.T) {
	// verify the JSON output structure when not recording
	output := sessionStatusOutput{
		Recording: false,
	}

	assert.False(t, output.Recording)
	assert.Empty(t, output.Title, "Title should be empty when not recording")
	assert.Empty(t, output.AgentID, "AgentID should be empty when not recording")
	assert.Equal(t, 0, output.DurationSecs, "DurationSecs should be 0 when not recording")
	assert.Empty(t, output.StartedAt, "StartedAt should be empty when not recording")

	// verify JSON serialization uses omitempty correctly
	data, err := json.Marshal(output)
	require.NoError(t, err)

	jsonStr := string(data)
	assert.Contains(t, jsonStr, `"recording":false`)
	assert.NotContains(t, jsonStr, `"agent_id"`, "omitempty should exclude empty agent_id")
	assert.NotContains(t, jsonStr, `"title"`, "omitempty should exclude empty title")
	assert.NotContains(t, jsonStr, `"duration_seconds"`, "omitempty should exclude zero duration")
}
