package main

import (
	"os"
	"testing"
	"time"

	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// Session recording INTENT tests
//
// These tests verify behavioral invariants — the "why" of the system — not
// implementation details. Each test documents the user-visible failure it
// prevents. They exist because unit tests for individual functions missed
// bugs that lived in the seams between components.
//
// Organized by behavioral domain:
//   A. Multi-turn session lifecycle
//   B. /clear boundary behavior
//   C. Multi-agent isolation
//   D. Recording state machine invariants
// =============================================================================

// --- A. Multi-turn session lifecycle ---
// Intent: A recording must survive an entire multi-turn conversation.
// PhaseStop fires after EVERY response turn, not just at session end.

// TestIntent_RecordingSurvivesFullConversation simulates a realistic
// multi-turn conversation: start → (prompt → afterTool → stop) × N.
// The recording must remain active with no StoppedAt throughout.
// Failure prevented: handleStop setting StoppedAt on every turn caused the
// daemon to finalize still-active sessions after 2-9 seconds.
func TestIntent_RecordingSurvivesFullConversation(t *testing.T) {
	projectRoot, repoID := setupTestProject(t)
	agentID := "OxConvo1"
	createActiveRecording(t, projectRoot, repoID, agentID)

	marker := &SessionMarker{AgentID: agentID}

	// simulate 10 response turns (realistic for a debugging session)
	for turn := range 10 {
		// each turn: prompt → afterTool → stop
		for _, phase := range []string{phasePrompt, phaseAfterTool, phaseStop} {
			ctx := &HookContext{
				Phase:       phase,
				AgentType:   "claude-code",
				ProjectRoot: projectRoot,
				Marker:      marker,
			}

			var err error
			switch phase {
			case phasePrompt:
				// prompt phase emits whispers — skip for this test
				continue
			case phaseAfterTool:
				err = handleAfterTool(ctx)
			case phaseStop:
				err = handleStop(ctx)
			}
			require.NoError(t, err, "turn %d, phase %s", turn, phase)
		}

		// INVARIANT: recording must still be active after every turn
		state, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
		require.NoError(t, err, "turn %d: load failed", turn)
		require.NotNil(t, state, "turn %d: recording vanished", turn)
		assert.Nil(t, state.StoppedAt, "turn %d: StoppedAt must be nil during active conversation", turn)
		assert.Equal(t, agentID, state.AgentID, "turn %d: agent ID changed", turn)
	}
}

// TestIntent_OnlyExplicitStopEndsRecording verifies the invariant that
// StoppedAt is only set by explicit user action (ox agent <id> session stop),
// never by hook lifecycle events.
// Failure prevented: any hook path accidentally setting StoppedAt would cause
// premature session finalization.
func TestIntent_OnlyExplicitStopEndsRecording(t *testing.T) {
	projectRoot, repoID := setupTestProject(t)
	agentID := "OxExplicit1"
	createActiveRecording(t, projectRoot, repoID, agentID)

	marker := &SessionMarker{AgentID: agentID}

	// exercise all non-start hook phases
	phases := []struct {
		name    string
		handler func(*HookContext) error
	}{
		{"afterTool", handleAfterTool},
		{"stop", handleStop},
	}

	for _, p := range phases {
		ctx := &HookContext{
			Phase:       p.name,
			AgentType:   "claude-code",
			ProjectRoot: projectRoot,
			Marker:      marker,
		}
		require.NoError(t, p.handler(ctx), "phase %s failed", p.name)

		state, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
		require.NoError(t, err)
		require.NotNil(t, state, "recording vanished after %s", p.name)
		assert.Nil(t, state.StoppedAt, "StoppedAt must not be set by %s hook", p.name)
	}

	// NOW set StoppedAt via the explicit stop path
	now := time.Now()
	require.NoError(t, session.UpdateRecordingStateForAgent(projectRoot, agentID, func(s *session.RecordingState) {
		s.StoppedAt = &now
	}))

	state, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
	require.NoError(t, err)
	require.NotNil(t, state.StoppedAt, "explicit stop must set StoppedAt")
}

// --- B. /clear boundary behavior ---
// Intent: /clear creates a clean session boundary — old session stops, new one starts.
// The agent ID may be reused but the recording state is fresh.

// TestIntent_ClearCreatesSessionBoundary verifies that /clear stops the old
// session, clears recording state, and enables prime to start a fresh session.
// Failure prevented: /clear orphaned the active recording by generating a new
// agent ID without stopping the old session.
func TestIntent_ClearCreatesSessionBoundary(t *testing.T) {
	projectRoot, repoID := setupTestProject(t)
	agentID := "OxBoundary1"
	createActiveRecording(t, projectRoot, repoID, agentID)

	// verify session is active
	state, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
	require.NoError(t, err)
	require.NotNil(t, state)
	assert.Nil(t, state.StoppedAt)

	// simulate what handleStart does on /clear: stopSessionForClear
	ctx := &HookContext{
		Phase:       phaseStart,
		AgentType:   "claude-code",
		ProjectRoot: projectRoot,
		Marker:      &SessionMarker{AgentID: agentID},
	}
	stopSessionForClear(ctx, agentID)

	// INVARIANT: old recording state is cleared — prime can start fresh
	state, err = session.LoadRecordingStateForAgent(projectRoot, agentID)
	require.NoError(t, err)
	assert.Nil(t, state, "old recording must be cleared after /clear so prime can start fresh")
}

// TestIntent_ClearPreservesAgentIDForPrime verifies that the agent ID is
// available to prime after /clear via SAGEOX_AGENT_ID env.
// Failure prevented: prime generates a new ID, orphaning the session that
// was just stopped by /clear.
func TestIntent_ClearPreservesAgentIDForPrime(t *testing.T) {
	projectRoot, repoID := setupTestProject(t)
	agentID := "OxPreserve1"

	// after /clear: old recording is gone, but env carries the ID
	t.Setenv("SAGEOX_AGENT_ID", agentID)

	// create a new active recording with the same agent ID (simulating prime
	// reusing the ID after /clear)
	createActiveRecording(t, projectRoot, repoID, agentID)

	// INVARIANT: prime's fallback chain finds the ID via env
	states, err := session.LoadAllRecordingStates(projectRoot)
	require.NoError(t, err)

	resolved := resolveAgentIDFromStates(states, os.Getenv("SAGEOX_AGENT_ID"))
	assert.Equal(t, agentID, resolved, "prime must find agent ID via SAGEOX_AGENT_ID after /clear")
}

// --- C. Multi-agent isolation ---
// Intent: Multiple agents on the same repo must not interfere with each other.
// Each agent has its own recording, identified by agent ID.

// TestIntent_MultiAgentRecordingsAreIndependent verifies that two agents
// recording on the same repo have completely independent lifecycle.
// Failure prevented: one agent's stop/cleanup affects another agent's recording.
func TestIntent_MultiAgentRecordingsAreIndependent(t *testing.T) {
	projectRoot, repoID := setupTestProject(t)

	agentA := "OxAgentA"
	agentB := "OxAgentB"
	createActiveRecording(t, projectRoot, repoID, agentA)
	createActiveRecording(t, projectRoot, repoID, agentB)

	// Agent A goes through multiple turns
	for range 5 {
		ctx := &HookContext{
			Phase:       phaseStop,
			AgentType:   "claude-code",
			ProjectRoot: projectRoot,
			Marker:      &SessionMarker{AgentID: agentA},
		}
		require.NoError(t, handleStop(ctx))
	}

	// INVARIANT: Agent B's recording is completely untouched
	stateB, err := session.LoadRecordingStateForAgent(projectRoot, agentB)
	require.NoError(t, err)
	require.NotNil(t, stateB, "Agent B's recording must survive Agent A's activity")
	assert.Nil(t, stateB.StoppedAt, "Agent B's StoppedAt must be nil")
	assert.Equal(t, agentB, stateB.AgentID)

	// Agent A's recording is also untouched
	stateA, err := session.LoadRecordingStateForAgent(projectRoot, agentA)
	require.NoError(t, err)
	require.NotNil(t, stateA, "Agent A's recording must also survive")
	assert.Nil(t, stateA.StoppedAt)
}

// TestIntent_StoppingOneAgentDoesNotAffectAnother verifies that explicitly
// stopping one agent's session has no effect on another agent's recording.
// Failure prevented: agent-agnostic cleanup paths removing all recordings.
func TestIntent_StoppingOneAgentDoesNotAffectAnother(t *testing.T) {
	projectRoot, repoID := setupTestProject(t)

	agentA := "OxStopA"
	agentB := "OxStopB"
	createActiveRecording(t, projectRoot, repoID, agentA)
	createActiveRecording(t, projectRoot, repoID, agentB)

	// explicitly stop Agent A
	now := time.Now()
	require.NoError(t, session.UpdateRecordingStateForAgent(projectRoot, agentA, func(s *session.RecordingState) {
		s.StoppedAt = &now
	}))
	require.NoError(t, session.ClearRecordingStateForAgent(projectRoot, agentA))

	// INVARIANT: Agent B is completely unaffected
	stateB, err := session.LoadRecordingStateForAgent(projectRoot, agentB)
	require.NoError(t, err)
	require.NotNil(t, stateB, "Agent B must survive Agent A being stopped")
	assert.Nil(t, stateB.StoppedAt)
	assert.Equal(t, agentB, stateB.AgentID)

	// Agent A is gone
	stateA, err := session.LoadRecordingStateForAgent(projectRoot, agentA)
	require.NoError(t, err)
	assert.Nil(t, stateA, "Agent A should be cleared")
}

// --- D. Recording state machine invariants ---
// Intent: Recording state transitions follow a strict lifecycle.

// TestIntent_GhostCleanupRespectsGracePeriod verifies that a recording with a
// dead PID is not removed if it was created recently. Young recordings may have
// stored a transient shell PID that died immediately.
// Failure prevented: recording deleted within seconds of creation because
// FindAgentAncestorPID() returned a transient bash PID.
func TestIntent_GhostCleanupRespectsGracePeriod(t *testing.T) {
	projectRoot, repoID := setupTestProject(t)

	agentID := "OxGrace1"
	// create a young recording with dead PID (within grace period)
	createYoungDeadRecording(t, projectRoot, repoID, agentID)

	// INVARIANT: ghost cleanup does not remove recordings younger than grace period
	result := session.CleanupGhostSessions(projectRoot)
	assert.Equal(t, 0, result.Removed, "young recording with dead PID must be protected by grace period")

	// recording must still exist
	state, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
	require.NoError(t, err)
	assert.NotNil(t, state, "recording must survive ghost cleanup within grace period")
}

// TestIntent_PrimeNeverGeneratesNewIDWhenActiveRecordingExists verifies that
// prime's ID resolution chain always finds an existing active recording before
// falling through to generate a new ID.
// Failure prevented: new agent ID orphans active session.
func TestIntent_PrimeNeverGeneratesNewIDWhenActiveRecordingExists(t *testing.T) {
	projectRoot, repoID := setupTestProject(t)

	agentID := "OxExisting1"
	createActiveRecording(t, projectRoot, repoID, agentID)

	// simulate prime's fallback chain: marker → parent PID → env → sole-active
	// with only sole-active available (worst case: no marker, no env, no PID match)
	states, err := session.LoadAllRecordingStates(projectRoot)
	require.NoError(t, err)

	// INVARIANT: when exactly one active recording exists, prime must find it
	resolved := resolveAgentIDFromStates(states, "")
	assert.Equal(t, agentID, resolved, "sole active recording must be discoverable by prime")
}
