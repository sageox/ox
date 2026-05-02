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

// --- A. Ghost grace period ---

// TestGhostCleanup_SkipsYoungRecordingWithDeadPID verifies that ghost cleanup
// does not remove a recording younger than GhostGracePeriod, even if its PID is dead.
// Failure prevented: transient shell PID from FindAgentAncestorPID() causes
// recording to be removed within seconds of creation.
func TestGhostCleanup_SkipsYoungRecordingWithDeadPID(t *testing.T) {
	sessionsDir := t.TempDir()
	sessionDir := filepath.Join(sessionsDir, "2026-01-10T09-00-user-OxYoung")
	require.NoError(t, os.MkdirAll(sessionDir, 0755))

	state := &RecordingState{
		AgentID:     "OxYoung",
		StartedAt:   time.Now().Add(-2 * time.Minute), // 2 min old — within grace period
		ParentPID:   999999999,                        // dead PID
		SessionPath: sessionDir,
	}
	data := mustMarshal(t, state)
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, ".recording.json"), data, 0600))

	result := CleanupGhostSessionsInDir(sessionsDir)
	assert.Equal(t, 0, result.Removed, "young recording with dead PID should survive ghost cleanup")

	// verify .recording.json still exists
	_, err := os.Stat(filepath.Join(sessionDir, ".recording.json"))
	assert.NoError(t, err, ".recording.json should still exist")
}

// TestGhostCleanup_RemovesOldRecordingWithDeadPID verifies that ghost cleanup
// removes a recording older than GhostGracePeriod when its PID is dead.
// Failure prevented: abandoned recordings accumulate forever.
func TestGhostCleanup_RemovesOldRecordingWithDeadPID(t *testing.T) {
	sessionsDir := t.TempDir()
	sessionDir := filepath.Join(sessionsDir, "2026-01-10T09-00-user-OxOld")
	require.NoError(t, os.MkdirAll(sessionDir, 0755))

	state := &RecordingState{
		AgentID:     "OxOld",
		StartedAt:   time.Now().Add(-15 * time.Minute), // 15 min old — past grace period
		ParentPID:   999999999,                         // dead PID
		SessionPath: sessionDir,
	}
	data := mustMarshal(t, state)
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, ".recording.json"), data, 0600))

	result := CleanupGhostSessionsInDir(sessionsDir)
	assert.Equal(t, 1, result.Removed, "old recording with dead PID should be removed by ghost cleanup")
}

// TestGhostCleanup_SkipsAlivePIDRegardlessOfAge verifies that ghost cleanup
// never removes recordings with alive PIDs, regardless of age.
// Failure prevented: active sessions killed by cleanup.
func TestGhostCleanup_SkipsAlivePIDRegardlessOfAge(t *testing.T) {
	sessionsDir := t.TempDir()
	sessionDir := filepath.Join(sessionsDir, "2026-01-10T09-00-user-OxAlive")
	require.NoError(t, os.MkdirAll(sessionDir, 0755))

	state := &RecordingState{
		AgentID:     "OxAlive",
		StartedAt:   time.Now().Add(-2 * time.Hour), // old
		ParentPID:   os.Getpid(),                    // alive PID
		SessionPath: sessionDir,
	}
	data := mustMarshal(t, state)
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, ".recording.json"), data, 0600))

	result := CleanupGhostSessionsInDir(sessionsDir)
	assert.Equal(t, 0, result.Removed, "recording with alive PID should never be removed")
}

// --- B. Multi-agent isolation ---

// TestGhostCleanup_MultiAgent_IndependentLifecycles verifies that multiple
// agents' recordings coexist and ghost cleanup only affects dead-PID recordings.
// Failure prevented: one agent's cleanup interferes with another agent's recording.
func TestGhostCleanup_MultiAgent_IndependentLifecycles(t *testing.T) {
	sessionsDir := t.TempDir()

	// Agent A: alive PID
	sessionDirA := filepath.Join(sessionsDir, "2026-01-10T09-00-user-OxAgentA")
	require.NoError(t, os.MkdirAll(sessionDirA, 0755))
	stateA := &RecordingState{
		AgentID:     "OxAgentA",
		StartedAt:   time.Now().Add(-30 * time.Minute),
		ParentPID:   os.Getpid(), // alive
		SessionPath: sessionDirA,
	}
	require.NoError(t, os.WriteFile(filepath.Join(sessionDirA, ".recording.json"), mustMarshal(t, stateA), 0600))

	// Agent B: dead PID, old enough
	sessionDirB := filepath.Join(sessionsDir, "2026-01-10T09-00-user-OxAgentB")
	require.NoError(t, os.MkdirAll(sessionDirB, 0755))
	stateB := &RecordingState{
		AgentID:     "OxAgentB",
		StartedAt:   time.Now().Add(-30 * time.Minute),
		ParentPID:   999999999, // dead
		SessionPath: sessionDirB,
	}
	require.NoError(t, os.WriteFile(filepath.Join(sessionDirB, ".recording.json"), mustMarshal(t, stateB), 0600))

	result := CleanupGhostSessionsInDir(sessionsDir)
	assert.Equal(t, 1, result.Removed, "only dead-PID recording should be removed")

	// Agent A's recording must survive
	_, err := os.Stat(filepath.Join(sessionDirA, ".recording.json"))
	assert.NoError(t, err, "Agent A's recording must survive ghost cleanup")

	// Agent B's recording should be gone
	_, err = os.Stat(filepath.Join(sessionDirB, ".recording.json"))
	assert.True(t, os.IsNotExist(err), "Agent B's recording should be removed")
}

// --- helpers ---

func mustMarshal(t *testing.T, state *RecordingState) []byte {
	t.Helper()
	data, err := json.MarshalIndent(state, "", "  ")
	require.NoError(t, err)
	return data
}
