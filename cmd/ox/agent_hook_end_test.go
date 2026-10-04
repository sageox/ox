package main

import (
	"testing"
	"time"

	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- SessionEnd hook tests ---
//
// handleEnd auto-finalizes the active recording when the calling agent exits.
// Without it, sessions only finalize via manual `ox session stop` (rarely used)
// or the daemon's 24h stale-recording sweep — leaving recordings stranded for
// up to a day in the common case where the user just closes their agent.
//
// The handler must be:
//   1. idempotent — SessionEnd can fire multiple times (window close, debugger
//      reattach, IDE reload). Only the first call should mark StoppedAt and
//      dispatch finalization.
//   2. graceful — if no recording exists or it's already finalized, no error.
//   3. delegated-only — at SessionEnd the calling agent process is gone, so
//      inline summarization (which whispers a prompt back to the agent) has
//      no agent to whisper to. Routing through the daemon IPC is the only
//      viable mode in this window. The handler does not consult
//      `agent.summarizer`; it always dispatches via daemon.

// TestHandleEnd_FinalizesActiveRecording verifies that handleEnd marks the
// recording as stopped on the first call.
// Failure prevented: user closes their agent, recording sits unfinalized in
// the cache for up to 24h until the daemon's anti-entropy sweep notices.
func TestHandleEnd_FinalizesActiveRecording(t *testing.T) {
	projectRoot, repoID := setupTestProject(t)

	agentID := "OxEndTest"
	createActiveRecording(t, projectRoot, repoID, agentID)

	loaded, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
	require.NoError(t, err)
	require.NotNil(t, loaded)
	require.Nil(t, loaded.StoppedAt, "precondition: StoppedAt nil before end")

	ctx := &HookContext{
		Phase:       phaseEnd,
		AgentType:   "claude-code",
		ProjectRoot: projectRoot,
		Marker:      &SessionMarker{AgentID: agentID},
	}
	require.NoError(t, handleEnd(ctx))

	// recording state should be cleared (so subsequent ox calls don't see a
	// zombie active recording). LoadRecordingStateForAgent returns nil with
	// no error when the state file doesn't exist.
	cleared, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
	require.NoError(t, err)
	assert.Nil(t, cleared, "handleEnd must clear recording state after dispatch")
}

// TestHandleEnd_Idempotent_NoStateAfterFirstCall verifies that handleEnd is
// safe to call multiple times — the first call clears state, subsequent calls
// are no-ops.
// Failure prevented: SessionEnd fires twice (e.g., on IDE reload), the second
// call double-dispatches to the daemon or corrupts state.
func TestHandleEnd_Idempotent_NoStateAfterFirstCall(t *testing.T) {
	projectRoot, repoID := setupTestProject(t)

	agentID := "OxEndIdem"
	createActiveRecording(t, projectRoot, repoID, agentID)

	ctx := &HookContext{
		Phase:       phaseEnd,
		AgentType:   "claude-code",
		ProjectRoot: projectRoot,
		Marker:      &SessionMarker{AgentID: agentID},
	}

	// fire SessionEnd 3 times in a row — handler must not error on any
	for i := 0; i < 3; i++ {
		require.NoErrorf(t, handleEnd(ctx), "handleEnd call %d should not error", i+1)
	}

	cleared, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
	require.NoError(t, err)
	assert.Nil(t, cleared, "recording state should remain cleared after multiple end calls")
}

// TestHandleEnd_AlreadyStopped_NoOp verifies that handleEnd respects an
// already-set StoppedAt and does not redispatch.
// Failure prevented: a recording explicitly stopped via `ox session stop` is
// then re-finalized when SessionEnd subsequently fires, racing with the
// inline-mode finalization that's already in flight.
func TestHandleEnd_AlreadyStopped_NoOp(t *testing.T) {
	projectRoot, repoID := setupTestProject(t)

	agentID := "OxEndAlreadyStopped"
	createActiveRecording(t, projectRoot, repoID, agentID)

	earlier := time.Now().Add(-1 * time.Minute)
	require.NoError(t, session.UpdateRecordingStateForAgent(projectRoot, agentID, func(s *session.RecordingState) {
		s.StoppedAt = &earlier
	}))

	ctx := &HookContext{
		Phase:       phaseEnd,
		AgentType:   "claude-code",
		ProjectRoot: projectRoot,
		Marker:      &SessionMarker{AgentID: agentID},
	}
	require.NoError(t, handleEnd(ctx))

	// state must remain (was not cleared) and StoppedAt must remain pinned to
	// the original timestamp — the no-op path skipped both clear and re-stamp.
	loaded, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
	require.NoError(t, err)
	require.NotNil(t, loaded, "already-stopped recording state should not be cleared by no-op end")
	require.NotNil(t, loaded.StoppedAt)
	assert.WithinDuration(t, earlier, *loaded.StoppedAt, time.Second,
		"already-set StoppedAt must not be overwritten")
}

// TestHandleEnd_NoRecording_NoError verifies that handleEnd gracefully handles
// the case where no recording exists for the agent (e.g., the agent exited
// before ever starting a session).
func TestHandleEnd_NoRecording_NoError(t *testing.T) {
	projectRoot, _ := setupTestProject(t)

	ctx := &HookContext{
		Phase:       phaseEnd,
		AgentType:   "claude-code",
		ProjectRoot: projectRoot,
		Marker:      &SessionMarker{AgentID: "OxEndNoRec"},
	}
	assert.NoError(t, handleEnd(ctx))
}

// TestHandleEnd_NoAgentID_NoError verifies that handleEnd is a silent no-op
// when there's no agent ID to identify the recording.
func TestHandleEnd_NoAgentID_NoError(t *testing.T) {
	ctx := &HookContext{
		Phase:       phaseEnd,
		AgentType:   "claude-code",
		ProjectRoot: t.TempDir(),
	}
	assert.NoError(t, handleEnd(ctx))
}

// TestPhaseEnd_HasActiveBehavior verifies that the SessionEnd phase is wired
// into activePhaseBehavior — without this, the hook fires but dispatchPhase
// short-circuits and silently noops, which is exactly the bug this work fixes.
func TestPhaseEnd_HasActiveBehavior(t *testing.T) {
	assert.True(t, activePhaseBehavior[phaseEnd],
		"phaseEnd must be marked active so dispatchPhase routes it to handleEnd")
}

// A quarantined recording holds a captured prefix of a session known to span
// repositories. The agent exiting (SessionEnd) or the context being wiped
// (/clear) is the normal end of every session, so it must not finalize the
// prefix or remove the marker that carries the quarantine.
func TestQuarantinedRecordingSurvivesSessionEndAndClear(t *testing.T) {
	for _, tt := range []struct {
		name string
		end  func(t *testing.T, projectRoot, agentID string)
	}{
		{"session end", func(t *testing.T, projectRoot, agentID string) {
			ctx := &HookContext{Phase: phaseEnd, AgentType: "claude-code", ProjectRoot: projectRoot, Marker: &SessionMarker{AgentID: agentID}}
			require.NoError(t, handleEnd(ctx))
		}},
		{"clear", func(t *testing.T, projectRoot, agentID string) {
			ctx := &HookContext{Phase: phaseStart, AgentType: "claude-code", ProjectRoot: projectRoot, Marker: &SessionMarker{AgentID: agentID}}
			stopSessionForClear(ctx, agentID)
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			projectRoot, repoID := setupTestProject(t)
			agentID := "OxQuarantineEnd"
			createActiveRecording(t, projectRoot, repoID, agentID)
			require.NoError(t, session.MarkSourceRejected(projectRoot, agentID))

			tt.end(t, projectRoot, agentID)

			kept, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
			require.NoError(t, err)
			require.NotNil(t, kept, "the quarantine marker must not be cleared")
			assert.True(t, kept.SourceRejected)
			assert.Nil(t, kept.StoppedAt, "a quarantined recording must not be marked stopped, which hands it to finalization")
		})
	}
}
