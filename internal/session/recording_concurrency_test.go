package session

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConcurrentAgentRecording(t *testing.T) {
	projectRoot, _ := createTestSessionProject(t)

	agentA := "OxAgentA"
	agentB := "OxAgentB"

	// both agents start recording
	stateA, err := StartRecording(projectRoot, StartRecordingOptions{
		AgentID:     agentA,
		AdapterName: "claude-code",
	})
	require.NoError(t, err)
	require.NotNil(t, stateA)

	stateB, err := StartRecording(projectRoot, StartRecordingOptions{
		AgentID:     agentB,
		AdapterName: "claude-code",
	})
	require.NoError(t, err)
	require.NotNil(t, stateB)

	// both should be recording
	assert.True(t, IsRecordingForAgent(projectRoot, agentA))
	assert.True(t, IsRecordingForAgent(projectRoot, agentB))
	assert.True(t, IsRecording(projectRoot)) // any-agent check

	// LoadAllRecordingStates returns both
	all, err := LoadAllRecordingStates(projectRoot)
	require.NoError(t, err)
	assert.Len(t, all, 2)
}

func TestConcurrentAgentStop(t *testing.T) {
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

	// stop agent A
	_, err = StopRecording(projectRoot, agentA)
	require.NoError(t, err)

	// agent A stopped, agent B survives
	assert.False(t, IsRecordingForAgent(projectRoot, agentA))
	assert.True(t, IsRecordingForAgent(projectRoot, agentB))
}

func TestConcurrentAgentClear(t *testing.T) {
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

	// clear agent A only
	err = ClearRecordingStateForAgent(projectRoot, agentA)
	require.NoError(t, err)

	assert.False(t, IsRecordingForAgent(projectRoot, agentA))
	assert.True(t, IsRecordingForAgent(projectRoot, agentB))
}

func TestStopThenStartSameAgent(t *testing.T) {
	cacheDir := t.TempDir()
	projectRoot := setupRecordingTest(t, cacheDir)

	// start recording
	_, err := StartRecording(projectRoot, StartRecordingOptions{
		AgentID: "OxSame1", AdapterName: "claude-code", Username: "testuser",
	})
	require.NoError(t, err)
	require.True(t, IsRecording(projectRoot))

	// stop recording
	_, err = StopRecording(projectRoot, "OxSame1")
	require.NoError(t, err)
	require.False(t, IsRecording(projectRoot))

	// start again with same agent — must succeed
	state2, err := StartRecording(projectRoot, StartRecordingOptions{
		AgentID: "OxSame1", AdapterName: "claude-code", Username: "testuser",
	})
	require.NoError(t, err, "start after stop with same agent must succeed")
	require.NotNil(t, state2)
	assert.Equal(t, "OxSame1", state2.AgentID)

	// cleanup
	_, _ = StopRecording(projectRoot, "OxSame1")
}

// TestMultiAgentLoadForAgent_ReturnsCorrectAgent is a regression test for
// https://github.com/sageox/ox/issues/168 — LoadRecordingState (first-found)
// returned the wrong agent's state when multiple agents recorded concurrently,
// causing session stop to process an empty subagent recording instead of the
// real one.
func TestMultiAgentLoadForAgent_ReturnsCorrectAgent(t *testing.T) {
	projectRoot, _ := createTestSessionProject(t)

	// simulate Conductor: main agent has a real session file, subagents have empty ones
	mainAgent := "OxzJGM" // alphabetically AFTER subagents
	subAgents := []string{"Ox0OE4", "Ox88vV", "OxHc7U", "Oxsv7q"}

	// start subagents first (empty session_file, generic adapter)
	for _, id := range subAgents {
		state, err := StartRecording(projectRoot, StartRecordingOptions{
			AgentID:     id,
			AdapterName: "claude",
		})
		require.NoError(t, err)
		require.NotNil(t, state)
		assert.Empty(t, state.SessionFile, "subagent %s should have empty session_file", id)
	}

	// start main agent with a real session file (claude-code adapter finds JSONL)
	mainState, err := StartRecording(projectRoot, StartRecordingOptions{
		AgentID:     mainAgent,
		AdapterName: "claude-code",
	})
	require.NoError(t, err)
	require.NotNil(t, mainState)

	// simulate claude-code adapter finding a real JSONL file
	fakeJSONL := filepath.Join(t.TempDir(), "session.jsonl")
	require.NoError(t, os.WriteFile(fakeJSONL, []byte(`{"type":"user","content":"hello"}`), 0600))
	err = UpdateRecordingStateForAgent(projectRoot, mainAgent, func(s *RecordingState) {
		s.SessionFile = fakeJSONL
	})
	require.NoError(t, err)

	// BUG SCENARIO: LoadRecordingState (first-found) returns Ox0OE4 or Ox88vV
	// (alphabetically first), which has empty SessionFile.
	// LoadRecordingStateForAgent must return the correct agent.
	firstFound, err := LoadRecordingState(projectRoot)
	require.NoError(t, err)
	require.NotNil(t, firstFound)

	agentSpecific, err := LoadRecordingStateForAgent(projectRoot, mainAgent)
	require.NoError(t, err)
	require.NotNil(t, agentSpecific)

	// the first-found state is NOT the main agent (it's a subagent sorted first)
	assert.NotEqual(t, mainAgent, firstFound.AgentID,
		"first-found should return a subagent (alphabetically first), demonstrating the bug")
	assert.Empty(t, firstFound.SessionFile,
		"first-found subagent has empty session_file — this is what the bug processed")

	// agent-specific returns the correct state with the real session file
	assert.Equal(t, mainAgent, agentSpecific.AgentID)
	assert.Equal(t, fakeJSONL, agentSpecific.SessionFile,
		"agent-specific must return the correct session file")
}

// TestMultiAgentClearForAgent_OnlyClearsTargetAgent is a regression test for
// https://github.com/sageox/ox/issues/168 — ClearRecordingState (first-found)
// cleared the wrong agent's recording, leaving the target agent orphaned.
func TestMultiAgentClearForAgent_OnlyClearsTargetAgent(t *testing.T) {
	projectRoot, _ := createTestSessionProject(t)

	mainAgent := "OxzJGM"
	subAgent := "Ox88vV" // alphabetically before main

	_, err := StartRecording(projectRoot, StartRecordingOptions{
		AgentID: subAgent, AdapterName: "claude",
	})
	require.NoError(t, err)

	_, err = StartRecording(projectRoot, StartRecordingOptions{
		AgentID: mainAgent, AdapterName: "claude-code",
	})
	require.NoError(t, err)

	// clear the main agent specifically
	err = ClearRecordingStateForAgent(projectRoot, mainAgent)
	require.NoError(t, err)

	// main agent cleared
	assert.False(t, IsRecordingForAgent(projectRoot, mainAgent),
		"main agent should no longer be recording")

	// subagent must survive — the bug would have cleared this one instead
	assert.True(t, IsRecordingForAgent(projectRoot, subAgent),
		"subagent must still be recording after clearing a different agent")

	// verify via load
	subState, err := LoadRecordingStateForAgent(projectRoot, subAgent)
	require.NoError(t, err)
	require.NotNil(t, subState, "subagent state must still exist")
	assert.Equal(t, subAgent, subState.AgentID)
}

// TestMultiAgentUpdateForAgent_Isolation verifies that UpdateRecordingStateForAgent
// only modifies the target agent's state in a multi-agent scenario.
// Regression test for https://github.com/sageox/ox/issues/168
func TestMultiAgentUpdateForAgent_Isolation(t *testing.T) {
	projectRoot, _ := createTestSessionProject(t)

	mainAgent := "OxzJGM"
	subAgent := "Ox88vV"

	_, err := StartRecording(projectRoot, StartRecordingOptions{
		AgentID: subAgent, AdapterName: "claude",
	})
	require.NoError(t, err)

	_, err = StartRecording(projectRoot, StartRecordingOptions{
		AgentID: mainAgent, AdapterName: "claude-code",
	})
	require.NoError(t, err)

	// mark main agent as incomplete (the StopIncomplete path from the bug)
	err = UpdateRecordingStateForAgent(projectRoot, mainAgent, func(s *RecordingState) {
		s.StopIncomplete = true
	})
	require.NoError(t, err)

	// main agent updated
	mainState, err := LoadRecordingStateForAgent(projectRoot, mainAgent)
	require.NoError(t, err)
	assert.True(t, mainState.StopIncomplete)

	// subagent untouched
	subState, err := LoadRecordingStateForAgent(projectRoot, subAgent)
	require.NoError(t, err)
	assert.False(t, subState.StopIncomplete,
		"subagent must not be affected by updating a different agent")
}
