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

// `ox agent prime` names Claude Code's adapter after the agent type, "claude",
// while every Claude-only check compares against "claude-code". A recording is
// read and started under the one spelling those checks use; other adapters keep
// theirs. Failure prevented: Claude recordings skipped the hook ownership check
// and recover's refusal to publish an unverified, empty capture.
func TestRecordingState_SpellsClaudeTheWayItsChecksDo(t *testing.T) {
	for stored, want := range map[string]string{
		"claude":      "claude-code",
		"Claude Code": "claude-code",
		"claude-code": "claude-code",
		"codex":       "codex",
		"cursor":      "cursor",
		"cody":        "cody", // an alias of generic, but its checks were written against "cody"
		"":            "",
	} {
		var state RecordingState
		data, err := json.Marshal(map[string]any{"agent_id": "OxName", "adapter_name": stored})
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(data, &state))
		assert.Equal(t, want, state.AdapterName, "a recording saved as %q", stored)
		assert.Equal(t, "OxName", state.AgentID, "the rest of the state still decodes")
	}

	projectRoot := setupRecordingTest(t, t.TempDir())
	state, err := StartRecording(projectRoot, StartRecordingOptions{AgentID: "OxPrime", AdapterName: "claude", Username: "testuser"})
	require.NoError(t, err)
	assert.Equal(t, "claude-code", state.AdapterName)
	loaded, err := LoadRecordingStateForAgent(projectRoot, "OxPrime")
	require.NoError(t, err)
	require.NotNil(t, loaded)
	assert.Equal(t, "claude-code", loaded.AdapterName)
}

// The stale and ghost sweeps keep a Claude recording whose transcript has not
// been found yet: it still names its native conversation. Failure prevented: a
// resumed session's recording, saved as "claude" by an earlier ox, was swept
// away with the native session ID a later stop needs to find the conversation
// (#1398).
func TestRecordingCleanup_KeepsAnUndiscoveredClaudeRecordingSavedAsClaude(t *testing.T) {
	for _, cleanup := range []string{"stale", "ghost"} {
		t.Run(cleanup, func(t *testing.T) {
			projectRoot, sessionsBase := setupRecordingTestWithSessionsBase(t, t.TempDir())
			sessionPath := filepath.Join(sessionsBase, "2026-01-01T00-00-user-OxResum")
			require.NoError(t, os.MkdirAll(sessionPath, 0o700))
			state := &RecordingState{
				AgentID: "OxResum", AgentSessionID: "230457d4-c384-4e38-90b5-3289ff44be62", SessionPath: sessionPath,
				ParentPID: 99999999, StartedAt: time.Now().Add(-72 * time.Hour), AdapterName: "claude",
			}
			require.NoError(t, SaveRecordingState(projectRoot, state))
			onDisk, err := os.ReadFile(filepath.Join(sessionPath, recordingFile))
			require.NoError(t, err)
			require.Contains(t, string(onDisk), `"adapter_name": "claude"`, "precondition: saved the way an earlier prime saved it")
			require.NoError(t, os.WriteFile(filepath.Join(sessionPath, "raw.jsonl"), []byte("{\"type\":\"header\"}\n"), 0o600))
			require.NoError(t, os.Chtimes(sessionPath, state.StartedAt, state.StartedAt))

			switch cleanup {
			case "stale":
				cleanupStaleEmptyRecordings(projectRoot)
			case "ghost":
				assert.Zero(t, CleanupGhostSessionsInDir(sessionsBase).Removed)
			}
			assert.FileExists(t, filepath.Join(sessionPath, recordingFile))
		})
	}
}
