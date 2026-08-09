package main

import (
	"math/rand"
	"testing"

	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Turn counting at the HOOK level.
//
// The pure draftDecision tests pin the scheduling arithmetic, but they know
// nothing about which hook feeds them. That leaves the feature's single most
// load-bearing structural claim completely unverified: the turn counter is
// driven by the Stop hook ("the agent finished responding", once per turn) and
// NOT by the afterTool hook (PostToolUse, once per tool call).
//
// Get that wrong and every arithmetic test still passes while the draft
// publishes on tool call 2 instead of turn 2 — which for an agent that opens
// with a few file reads means publishing during the first response, and
// refreshing roughly once per ten tool calls instead of ten turns. On the
// shared ledger that is an order of magnitude more commits than designed.

func stopHookCtx(projectRoot, agentID string) *HookContext {
	return &HookContext{
		Phase:       phaseStop,
		AgentType:   "claude-code",
		ProjectRoot: projectRoot,
		Marker:      &SessionMarker{AgentID: agentID},
	}
}

func afterToolHookCtx(projectRoot, agentID string) *HookContext {
	return &HookContext{
		Phase:       phaseAfterTool,
		AgentType:   "claude-code",
		ProjectRoot: projectRoot,
		Marker:      &SessionMarker{AgentID: agentID},
	}
}

func turnCountFor(t *testing.T, projectRoot, agentID string) int {
	t.Helper()
	state, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
	require.NoError(t, err)
	require.NotNil(t, state)
	return state.TurnCount
}

// TestTurnCount_IncrementedByStopHookOnly is the structural invariant.
//
// Red-first check: move maybePublishSessionDraft from handleStop into
// handleAfterTool and this test fails on both assertions.
func TestTurnCount_IncrementedByStopHookOnly(t *testing.T) {
	projectRoot, repoID := setupTestProject(t)
	const agentID = "OxTurn01"
	createActiveRecording(t, projectRoot, repoID, agentID)

	require.Equal(t, 0, turnCountFor(t, projectRoot, agentID), "fresh recording starts at zero")

	// A realistic turn: several tool calls, then the agent finishes responding.
	for i := 0; i < 5; i++ {
		require.NoError(t, handleAfterTool(afterToolHookCtx(projectRoot, agentID)))
	}
	assert.Equal(t, 0, turnCountFor(t, projectRoot, agentID),
		"PostToolUse is a TOOL CALL, not a turn — counting it publishes the draft during the first response")

	require.NoError(t, handleStop(stopHookCtx(projectRoot, agentID)))
	assert.Equal(t, 1, turnCountFor(t, projectRoot, agentID), "the Stop hook is the turn signal")

	// Second turn, same shape.
	for i := 0; i < 3; i++ {
		require.NoError(t, handleAfterTool(afterToolHookCtx(projectRoot, agentID)))
	}
	assert.Equal(t, 1, turnCountFor(t, projectRoot, agentID))
	require.NoError(t, handleStop(stopHookCtx(projectRoot, agentID)))
	assert.Equal(t, 2, turnCountFor(t, projectRoot, agentID))
}

// TestTurnCount_MonotonicAcrossManyTurns — the counter must never go backwards
// or skip. RecordingState is an unlocked load-modify-save, so a bug that
// rebuilt the state instead of mutating it would reset the count and make the
// draft republish forever.
func TestTurnCount_MonotonicAcrossManyTurns(t *testing.T) {
	projectRoot, repoID := setupTestProject(t)
	const agentID = "OxTurn02"
	createActiveRecording(t, projectRoot, repoID, agentID)

	prev := 0
	for turn := 1; turn <= 25; turn++ {
		require.NoError(t, handleStop(stopHookCtx(projectRoot, agentID)))
		got := turnCountFor(t, projectRoot, agentID)
		assert.Equal(t, turn, got, "turn %d", turn)
		assert.Greater(t, got, prev, "the counter must be strictly monotonic")
		prev = got
	}
}

// TestTurnCount_IsNotEntryCountOrHookInvocations.
//
// Three counters live on RecordingState and it is genuinely easy to reach for
// the wrong one: EntryCount counts raw.jsonl entries (many per turn),
// HookInvocations counts afterTool calls (i.e. tool calls). A draft scheduled
// off either of those fires at the wrong time and refreshes at the wrong rate.
func TestTurnCount_IsNotEntryCountOrHookInvocations(t *testing.T) {
	projectRoot, repoID := setupTestProject(t)
	const agentID = "OxTurn03"
	createActiveRecording(t, projectRoot, repoID, agentID)

	for i := 0; i < 7; i++ {
		require.NoError(t, handleAfterTool(afterToolHookCtx(projectRoot, agentID)))
	}
	require.NoError(t, handleStop(stopHookCtx(projectRoot, agentID)))

	state, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
	require.NoError(t, err)
	require.NotNil(t, state)

	assert.Equal(t, 1, state.TurnCount, "one completed response turn")
	assert.NotEqual(t, state.TurnCount, state.HookInvocations,
		"HookInvocations counts tool calls; conflating it with turns is the bug this guards")
}

// TestTurnCount_HookLevelProperty drives the REAL hooks with randomized
// interleavings and checks the turn counter against a reference model
// recomputed by linear scan.
//
// The existing draftDecision property test operates on integers and cannot see
// a miswiring between hooks. This one closes that gap at the boundary that
// actually ships. Fixed seed for determinism, no third-party generator — the
// same convention as TestApplySegmentMask_Property in internal/session.
func TestTurnCount_HookLevelProperty(t *testing.T) {
	if testing.Short() {
		t.Skip("short: drives real hooks with filesystem state")
	}
	projectRoot, repoID := setupTestProject(t)
	const agentID = "OxTurn04"
	createActiveRecording(t, projectRoot, repoID, agentID)

	rng := rand.New(rand.NewSource(1)) //nolint:gosec // deterministic test input, not security
	modelTurns := 0

	for op := 0; op < 120; op++ {
		if rng.Intn(2) == 0 {
			require.NoError(t, handleAfterTool(afterToolHookCtx(projectRoot, agentID)))
			// model unchanged: a tool call is not a turn
		} else {
			require.NoError(t, handleStop(stopHookCtx(projectRoot, agentID)))
			modelTurns++
		}
		require.Equal(t, modelTurns, turnCountFor(t, projectRoot, agentID),
			"op=%d: turn counter diverged from the reference model", op)
	}
}

// TestTurnCount_SurvivesNoRecordingState — a Stop hook for an agent with no
// recording must be a silent no-op, not a panic and not a created state file.
// Hooks fire in every repo, including ones that never started a session.
func TestTurnCount_SurvivesNoRecordingState(t *testing.T) {
	projectRoot, _ := setupTestProject(t)

	assert.NotPanics(t, func() {
		require.NoError(t, handleStop(stopHookCtx(projectRoot, "OxNoRec")))
	})
	state, err := session.LoadRecordingStateForAgent(projectRoot, "OxNoRec")
	require.NoError(t, err)
	assert.Nil(t, state, "no recording state may be conjured by a hook")
}

// TestTurnCount_NoMarkerIsNoOp — the hook can fire before an agent marker
// exists. It must not panic on the nil Marker.
func TestTurnCount_NoMarkerIsNoOp(t *testing.T) {
	projectRoot, _ := setupTestProject(t)

	assert.NotPanics(t, func() {
		maybePublishSessionDraft(&HookContext{Phase: phaseStop, ProjectRoot: projectRoot})
		maybePublishSessionDraft(nil)
		maybePublishSessionDraft(&HookContext{Phase: phaseStop, ProjectRoot: projectRoot,
			Marker: &SessionMarker{AgentID: ""}})
	})
}
