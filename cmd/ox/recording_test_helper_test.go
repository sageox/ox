package main

import (
	"testing"

	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/require"
)

// startTestRecording lives outside the !short files: the fast tier compiles
// agent_hook_recall_test.go and agent_hook_context_use_nudge_test.go, which
// call it, so a !short-only definition broke the whole package's build there.
// startTestRecording starts a recording for the given agent and returns the state.
func startTestRecording(t *testing.T, projectRoot, agentID, adapterName string) *session.RecordingState {
	t.Helper()
	state, err := session.StartRecording(projectRoot, session.StartRecordingOptions{
		AgentID:     agentID,
		AdapterName: adapterName,
		Username:    "testuser",
	})
	require.NoError(t, err)
	require.NotNil(t, state)
	return state
}
