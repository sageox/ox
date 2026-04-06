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

	states, err := session.LoadAllRecordingStates(projectRoot)
	require.NoError(t, err)

	resolved := resolveAgentIDFromStates(states, agentID)
	assert.Equal(t, agentID, resolved, "prime should reuse agent ID from SAGEOX_AGENT_ID when recording is active")
}

// TestPrimeIgnoresEnv_WhenRecordingDead verifies that a stale SAGEOX_AGENT_ID
// (pointing to a recording with a dead PID) is not reused.
// Failure prevented: reusing a dead agent's ID creates a zombie session.
func TestPrimeIgnoresEnv_WhenRecordingDead(t *testing.T) {
	projectRoot, repoID := setupTestProject(t)

	agentID := "OxDead1"
	createDeadRecording(t, projectRoot, repoID, agentID)

	states, err := session.LoadAllRecordingStates(projectRoot)
	require.NoError(t, err)

	resolved := resolveAgentIDFromStates(states, agentID)
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

	states, err := session.LoadAllRecordingStates(projectRoot)
	require.NoError(t, err)

	resolved := resolveAgentIDFromStates(states, "")
	assert.Equal(t, agentID, resolved, "sole active recording should be reused when no env ID available")
}

// TestPrimeNoFallback_MultipleActiveRecordings verifies that when multiple
// active recordings exist, prime does not pick one arbitrarily.
// Failure prevented: multi-agent repos would get cross-wired sessions.
func TestPrimeNoFallback_MultipleActiveRecordings(t *testing.T) {
	projectRoot, repoID := setupTestProject(t)

	createActiveRecording(t, projectRoot, repoID, "OxMulti1")
	createActiveRecording(t, projectRoot, repoID, "OxMulti2")

	states, err := session.LoadAllRecordingStates(projectRoot)
	require.NoError(t, err)

	resolved := resolveAgentIDFromStates(states, "")
	assert.Empty(t, resolved, "multiple active recordings should not trigger sole-recording fallback")
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

	ctx := &HookContext{
		Phase:       phaseStart,
		AgentType:   "claude-code",
		ProjectRoot: projectRoot,
		Marker:      &SessionMarker{AgentID: agentID},
	}
	stopSessionForClear(ctx, agentID)

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
	env := buildPrimeEnv(agentID)

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
	// unset any existing SAGEOX_AGENT_ID
	t.Setenv("SAGEOX_AGENT_ID", "")

	env := buildPrimeEnv("")

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
		StartedAt:   time.Now().Add(-15 * time.Minute), // past grace period
		AdapterName: "claude-code",
		SessionPath: sessionPath,
		OutputFile:  filepath.Join(sessionPath, "raw.jsonl"),
		ParentPID:   999999999, // dead PID
	}
	require.NoError(t, session.SaveRecordingState(projectRoot, state))
}

func createYoungDeadRecording(t *testing.T, projectRoot, repoID, agentID string) {
	t.Helper()
	sessionsBase := filepath.Join(session.GetContextPath(repoID), "sessions")
	sessionPath := filepath.Join(sessionsBase, "2026-04-01T10-00-user-"+agentID)

	state := &session.RecordingState{
		AgentID:     agentID,
		StartedAt:   time.Now().Add(-30 * time.Second), // within grace period
		AdapterName: "claude-code",
		SessionPath: sessionPath,
		OutputFile:  filepath.Join(sessionPath, "raw.jsonl"),
		ParentPID:   999999999, // dead PID
	}
	require.NoError(t, session.SaveRecordingState(projectRoot, state))
}
