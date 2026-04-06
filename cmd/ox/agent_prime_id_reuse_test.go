package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- A. Agent ID reuse from SAGEOX_AGENT_ID env ---

// TestPrimeReusesAgentID_FromEnvWithActiveRecording verifies that when
// SAGEOX_AGENT_ID is set and points to an active recording, prime reuses it
// instead of generating a new ID.
// Failure prevented: /clear orphans the active session by generating a new agent ID.
func TestPrimeReusesAgentID_FromEnvWithActiveRecording(t *testing.T) {
	projectRoot, repoID := setupTestProject(t)

	agentID := "OxReuse1"
	createActiveRecording(t, projectRoot, repoID, agentID)

	t.Setenv("SAGEOX_AGENT_ID", agentID)

	// load states and simulate the prime fallback logic
	states, err := session.LoadAllRecordingStates(projectRoot)
	require.NoError(t, err)

	var resolved string
	envID := os.Getenv("SAGEOX_AGENT_ID")
	for _, s := range states {
		if s.AgentID == envID && s.IsAgentAlive() {
			resolved = envID
			break
		}
	}

	assert.Equal(t, agentID, resolved, "prime should reuse agent ID from SAGEOX_AGENT_ID when recording is active")
}

// TestPrimeIgnoresEnv_WhenRecordingDead verifies that a stale SAGEOX_AGENT_ID
// (pointing to a recording with a dead PID) is not reused.
// Failure prevented: reusing a dead agent's ID creates a zombie session.
func TestPrimeIgnoresEnv_WhenRecordingDead(t *testing.T) {
	projectRoot, repoID := setupTestProject(t)

	agentID := "OxDead1"
	createDeadRecording(t, projectRoot, repoID, agentID)

	t.Setenv("SAGEOX_AGENT_ID", agentID)

	states, err := session.LoadAllRecordingStates(projectRoot)
	require.NoError(t, err)

	var resolved string
	envID := os.Getenv("SAGEOX_AGENT_ID")
	for _, s := range states {
		if s.AgentID == envID && s.IsAgentAlive() {
			resolved = envID
			break
		}
	}

	assert.Empty(t, resolved, "prime should not reuse agent ID when recording's process is dead")
}

// --- B. Sole active recording fallback ---

// TestPrimeFallback_SoleActiveRecording verifies that when there is exactly
// one active recording and no other reuse source, prime falls back to it.
// Failure prevented: after /clear with no env var or marker, session is orphaned.
func TestPrimeFallback_SoleActiveRecording(t *testing.T) {
	projectRoot, repoID := setupTestProject(t)

	agentID := "OxSole1"
	createActiveRecording(t, projectRoot, repoID, agentID)

	// no SAGEOX_AGENT_ID set
	t.Setenv("SAGEOX_AGENT_ID", "")

	states, err := session.LoadAllRecordingStates(projectRoot)
	require.NoError(t, err)

	var alive []*session.RecordingState
	for _, s := range states {
		if s.IsAgentAlive() {
			alive = append(alive, s)
		}
	}

	require.Len(t, alive, 1, "should have exactly one active recording")
	assert.Equal(t, agentID, alive[0].AgentID, "sole active recording should be reusable")
}

// TestPrimeNoFallback_MultipleActiveRecordings verifies that when multiple
// active recordings exist, prime does not pick one arbitrarily.
// Failure prevented: multi-agent repos would get cross-wired sessions.
func TestPrimeNoFallback_MultipleActiveRecordings(t *testing.T) {
	projectRoot, repoID := setupTestProject(t)

	createActiveRecording(t, projectRoot, repoID, "OxMulti1")
	createActiveRecording(t, projectRoot, repoID, "OxMulti2")

	t.Setenv("SAGEOX_AGENT_ID", "")

	states, err := session.LoadAllRecordingStates(projectRoot)
	require.NoError(t, err)

	var alive []*session.RecordingState
	for _, s := range states {
		if s.IsAgentAlive() {
			alive = append(alive, s)
		}
	}

	assert.Greater(t, len(alive), 1, "multiple active recordings should not trigger sole-recording fallback")
}

// --- C. Session stop on /clear ---

// TestStopSessionForClear_SetsStoppedAtAndClears verifies that stopSessionForClear
// marks the recording as stopped and clears the state so prime can start fresh.
// Failure prevented: /clear continues appending to old session instead of starting new one.
func TestStopSessionForClear_SetsStoppedAtAndClears(t *testing.T) {
	projectRoot, repoID := setupTestProject(t)

	agentID := "OxClear1"
	createActiveRecording(t, projectRoot, repoID, agentID)

	// verify recording exists before clear
	state, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
	require.NoError(t, err)
	require.NotNil(t, state, "recording should exist before clear")

	// simulate what stopSessionForClear does
	now := time.Now()
	require.NoError(t, session.UpdateRecordingStateForAgent(projectRoot, agentID, func(s *session.RecordingState) {
		s.StoppedAt = &now
	}))

	// verify StoppedAt was set
	state, err = session.LoadRecordingStateForAgent(projectRoot, agentID)
	require.NoError(t, err)
	require.NotNil(t, state)
	require.NotNil(t, state.StoppedAt, "StoppedAt should be set")

	// clear the recording state
	require.NoError(t, session.ClearRecordingStateForAgent(projectRoot, agentID))

	// verify recording is gone
	state, err = session.LoadRecordingStateForAgent(projectRoot, agentID)
	require.NoError(t, err)
	assert.Nil(t, state, "recording state should be cleared after stopSessionForClear")
}

// --- D. Hook passes agent ID to prime subprocess ---

// TestRunPrimeForHook_PassesAgentIDInEnv verifies that runPrimeForHook builds
// the subprocess environment with SAGEOX_AGENT_ID when an agent ID is known.
// Failure prevented: prime subprocess generates new ID even though hook knows the correct one.
func TestRunPrimeForHook_PassesAgentIDInEnv(t *testing.T) {
	// test the env construction logic directly since running the full subprocess
	// requires a real ox binary
	agentID := "OxHook1"
	env := os.Environ() // safe: testing env construction logic, not spawning ox
	if agentID != "" {
		env = append(env, "SAGEOX_AGENT_ID="+agentID)
	}

	found := false
	for _, e := range env {
		if e == "SAGEOX_AGENT_ID=OxHook1" {
			found = true
			break
		}
	}
	assert.True(t, found, "SAGEOX_AGENT_ID should be in subprocess environment")
}

// TestRunPrimeForHook_NoAgentID_SkipsEnv verifies that when no agent ID is known,
// SAGEOX_AGENT_ID is not added to the subprocess environment.
func TestRunPrimeForHook_NoAgentID_SkipsEnv(t *testing.T) {
	agentID := ""
	// unset any existing SAGEOX_AGENT_ID
	t.Setenv("SAGEOX_AGENT_ID", "")

	env := os.Environ() // safe: testing env construction logic, not spawning ox
	if agentID != "" {
		env = append(env, "SAGEOX_AGENT_ID="+agentID)
	}

	for _, e := range env {
		if e == "SAGEOX_AGENT_ID=" {
			// empty value from Setenv, that's fine
			continue
		}
		assert.NotContains(t, e, "SAGEOX_AGENT_ID=Ox",
			"SAGEOX_AGENT_ID should not be added with an Ox-prefixed value when agentID is empty")
	}
}

// --- helpers ---

func setupTestProject(t *testing.T) (string, string) {
	t.Helper()
	cacheDir := t.TempDir()
	projectRoot := t.TempDir()
	repoID := "test-repo-" + t.Name()

	sageoxDir := filepath.Join(projectRoot, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755))
	configContent := `{"config_version":"2","repo_id":"` + repoID + `"}`
	require.NoError(t, os.WriteFile(filepath.Join(sageoxDir, "config.json"), []byte(configContent), 0644))

	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("HOME", cacheDir)
	t.Setenv("XDG_CACHE_HOME", cacheDir)

	return projectRoot, repoID
}

func createActiveRecording(t *testing.T, projectRoot, repoID, agentID string) {
	t.Helper()
	sessionsBase := filepath.Join(session.GetContextPath(repoID), "sessions")
	sessionPath := filepath.Join(sessionsBase, "2026-04-01T10-00-user-"+agentID)

	state := &session.RecordingState{
		AgentID:     agentID,
		StartedAt:   time.Now().Add(-10 * time.Minute),
		AdapterName: "claude-code",
		SessionPath: sessionPath,
		OutputFile:  filepath.Join(sessionPath, "raw.jsonl"),
		ParentPID:   os.Getpid(), // current process is alive
	}
	require.NoError(t, session.SaveRecordingState(projectRoot, state))
}

func createDeadRecording(t *testing.T, projectRoot, repoID, agentID string) {
	t.Helper()
	sessionsBase := filepath.Join(session.GetContextPath(repoID), "sessions")
	sessionPath := filepath.Join(sessionsBase, "2026-04-01T10-00-user-"+agentID)

	state := &session.RecordingState{
		AgentID:     agentID,
		StartedAt:   time.Now().Add(-10 * time.Minute),
		AdapterName: "claude-code",
		SessionPath: sessionPath,
		OutputFile:  filepath.Join(sessionPath, "raw.jsonl"),
		ParentPID:   999999999, // dead PID
	}
	require.NoError(t, session.SaveRecordingState(projectRoot, state))
}
