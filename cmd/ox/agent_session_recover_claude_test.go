package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/agentinstance"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/adapters"
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
	require.ErrorContains(t, err, "nothing was published", "an undiscovered source does not prove the session empty")
	require.ErrorContains(t, err, "session recover", "the error says how to retry")
	require.ErrorContains(t, err, "ox session import", "the error names the full-session import")

	kept, loadErr := session.LoadRecordingStateForAgent(projectRoot, agentID)
	require.NoError(t, loadErr)
	require.NotNil(t, kept, "the recording and its native session ID stay for a later stop")
	rawAfter, err := os.ReadFile(filepath.Join(state.SessionPath, "raw.jsonl"))
	require.NoError(t, err)
	assert.Equal(t, string(rawBefore), string(rawAfter), "nothing was finalized")
}

type discoveredClaudeAdapter struct {
	testClaudeCodeAdapter
	file string
}

func (a *discoveredClaudeAdapter) FindSessionFile(_ adapters.SessionLookup) (string, error) {
	return a.file, nil
}

// Discovery by session ID does not compare turn times with the recording's
// start, so recover can accept a transcript whose every turn predates it.
// Failure prevented: recover cleared the recording and reported success
// although the start filter left nothing to publish.
func TestRecover_KeepsADiscoveredClaudeRecordingThatPublishesNothing(t *testing.T) {
	projectRoot, agentID, sourceFile := setupHandleAfterToolTest(t)
	t.Chdir(projectRoot)
	adapters.Unregister("claude-code")
	adapters.Register(&discoveredClaudeAdapter{file: sourceFile})
	state, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
	require.NoError(t, err)
	early := fmt.Sprintf("{\"type\":\"user\",\"sessionId\":\"session\",\"cwd\":%q,\"timestamp\":%q,\"message\":{\"role\":\"user\",\"content\":\"before the recording\"}}\n",
		projectRoot, state.StartedAt.Add(-time.Hour).Format(time.RFC3339Nano))
	f, err := os.OpenFile(sourceFile, os.O_APPEND|os.O_WRONLY, 0)
	require.NoError(t, err)
	_, err = f.WriteString(early)
	require.NoError(t, err)
	require.NoError(t, f.Close())
	require.NoError(t, session.UpdateRecordingStateForAgent(projectRoot, agentID, func(s *session.RecordingState) {
		s.SessionFile = ""
		s.WatchMode = "hook"
	}))

	err = runAgentSessionRecover(&agentinstance.Instance{AgentID: agentID})
	require.ErrorContains(t, err, "nothing was published")
	require.ErrorContains(t, err, "ox session import", "the error names the full-session import")

	kept, loadErr := session.LoadRecordingStateForAgent(projectRoot, agentID)
	require.NoError(t, loadErr)
	require.NotNil(t, kept, "the recording stays for a retry or an import")
}
