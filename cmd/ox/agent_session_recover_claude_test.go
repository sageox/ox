package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/agentinstance"
	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// `ox agent prime` saved Claude recordings as "claude", and recover's refusal
// to publish an unverified, header-only capture only matched "claude-code".
// Failure prevented: recover published the 441-byte header of a session whose
// transcript was never found as the session, the empty copy that PR links
// already pointed to, which then made import skip the real conversation (#1398).
func TestRecover_KeepsAnUndiscoveredClaudeRecordingInsteadOfPublishingItEmpty(t *testing.T) {
	projectRoot, agentID, _ := setupHandleAfterToolTest(t)
	t.Chdir(projectRoot)
	state, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
	require.NoError(t, err)
	marker := filepath.Join(state.SessionPath, ".recording.json")
	data, err := os.ReadFile(marker)
	require.NoError(t, err)
	var saved map[string]any
	require.NoError(t, json.Unmarshal(data, &saved))
	saved["adapter_name"], saved["session_file"] = "claude", ""
	data, err = json.Marshal(saved)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(marker, data, 0o600))
	rawBefore, err := os.ReadFile(filepath.Join(state.SessionPath, "raw.jsonl"))
	require.NoError(t, err)
	require.False(t, session.HasSubstantiveEntries(filepath.Join(state.SessionPath, "raw.jsonl")), "precondition: nothing captured")

	err = runAgentSessionRecover(&agentinstance.Instance{AgentID: agentID})
	require.ErrorContains(t, err, "not yet verified", "an undiscovered source does not prove the session empty")

	kept, loadErr := session.LoadRecordingStateForAgent(projectRoot, agentID)
	require.NoError(t, loadErr)
	require.NotNil(t, kept, "the recording and its native session ID stay for a later stop")
	rawAfter, err := os.ReadFile(filepath.Join(state.SessionPath, "raw.jsonl"))
	require.NoError(t, err)
	assert.Equal(t, string(rawBefore), string(rawAfter), "nothing was finalized")
}
