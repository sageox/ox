package main

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/agentx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/oxactivity"
	whisperstore "github.com/sageox/ox/internal/whisper/store"
)

func newStatuslineHookProject(t *testing.T) string {
	t.Helper()
	t.Setenv(config.EnvUserConfig, filepath.Join(t.TempDir(), "config.yaml"))
	root := t.TempDir()
	require.NoError(t, config.SaveProjectConfig(root, &config.ProjectConfig{RepoID: "repo_statusline_hooks"}))
	return root
}

func bashInput(t *testing.T, command string) *agentx.HookInput {
	t.Helper()
	raw, err := json.Marshal(map[string]string{"command": command})
	require.NoError(t, err)
	return &agentx.HookInput{SessionID: hookSessionID, ToolName: "Bash", ToolInput: raw}
}

// hookSessionID is the harness session every hook input in these tests comes
// from; state is keyed by it (oxactivity.StateKey), not by the agent alone.
const hookSessionID = "claude-session-hooks"

// TestHandleAfterTool_RecordsOxActivityForStatusline drives the real
// PostToolUse handler. Failure prevented: the status line never learns what
// ox did (the "● ox  checked murmurs" a coworker should see), or ox plumbing
// like heartbeats and unrelated shell commands flood it.
func TestHandleAfterTool_RecordsOxActivityForStatusline(t *testing.T) {
	tests := []struct {
		name    string
		input   *agentx.HookInput
		want    string
		wantNil bool
	}{
		{name: "ox murmur list", input: bashInput(t, "ox murmur list"), want: "checked murmurs"},
		{name: "ox query", input: bashInput(t, `ox query "auth decisions"`), want: "searched team memory"},
		{name: "heartbeat is plumbing", input: bashInput(t, "ox agent OxAb12 heartbeat"), wantNil: true},
		{name: "non-ox command", input: bashInput(t, "go test ./..."), wantNil: true},
		{name: "non-shell tool", input: &agentx.HookInput{ToolName: "Read", ToolInput: json.RawMessage(`{"file_path":"x"}`)}, wantNil: true},
		{name: "no input", input: nil, wantNil: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := newStatuslineHookProject(t)
			const agentID = "OxHk01"
			before := time.Now()
			require.NoError(t, handleAfterTool(&HookContext{
				Phase:       phaseAfterTool,
				AgentType:   "claude-code",
				ProjectRoot: root,
				Marker:      &SessionMarker{AgentID: agentID},
				Input:       tt.input,
			}))

			key := oxactivity.StateKey(agentID, hookSessionID)
			state := oxactivity.LoadState(root, key)
			if tt.wantNil {
				assert.Equal(t, oxactivity.State{}, state, "nothing coworker-visible happened")
				assert.NoFileExists(t, oxactivity.StatePath(root, key))
				return
			}
			assert.Equal(t, oxactivity.State{}, oxactivity.LoadState(root, agentID), "state is per harness session, never agent-wide")
			assert.Equal(t, tt.want, state.LastAction)
			assert.False(t, state.LastActionAt.Before(before), "the action is stamped now")
		})
	}
}

// TestRecordOxToolCall_StatuslineRespectsOffAndKeepsWhisper turns the line off
// and checks an action update keeps the stored whisper. Failure prevented:
// `statusline off` still writes on every tool call, or a new action erases
// the teammate whisper the line was showing.
func TestRecordOxToolCall_StatuslineRespectsOffAndKeepsWhisper(t *testing.T) {
	root := newStatuslineHookProject(t)
	const agentID = "OxHk02"
	key := oxactivity.StateKey(agentID, hookSessionID)
	whisperAt := time.Now().Add(-time.Minute).UTC()
	require.NoError(t, oxactivity.SaveState(root, key, oxactivity.State{LastWhisperFrom: "ryan", LastWhisperAt: whisperAt}))

	recordOxToolCall(root, agentID, bashInput(t, "ox murmur list"))
	state := oxactivity.LoadState(root, key)
	assert.Equal(t, "checked murmurs", state.LastAction)
	assert.Equal(t, "ryan", state.LastWhisperFrom)

	require.NoError(t, SetConfigValue("statusline", "off", ConfigLevelUser, ""))
	recordOxToolCall(root, agentID, bashInput(t, `ox query "x"`))
	assert.Equal(t, "checked murmurs", oxactivity.LoadState(root, key).LastAction, "off means no writes")
}

// TestRecordStatuslineWhisper keeps the newest delivered teammate murmur.
// Failure prevented: the bar shows a nudge or outside news as a teammate
// whisper, or an older batch overwrites a newer whisper.
func TestRecordStatuslineWhisper(t *testing.T) {
	root := newStatuslineHookProject(t)
	const agentID = "OxHk03"
	now := time.Now().UTC().Truncate(time.Second)

	recordStatuslineWhisper(root, agentID, []whisperstore.WhisperEntry{
		{Source: "nudge", Topic: "tip", Content: "not a teammate", CreatedAt: now},
		{Source: "murmur", PrincipalID: "avery", Topic: "wip", Content: "older", CreatedAt: now.Add(-2 * time.Minute)},
		{Source: "murmur", AgentID: "OxTm11", Topic: "decision", Content: "newest", CreatedAt: now.Add(-time.Minute)},
	})
	state := oxactivity.LoadState(root, agentID)
	assert.Equal(t, "OxTm11", state.LastWhisperFrom, "falls back to the agent ID without a principal")
	assert.Equal(t, "decision", state.LastWhisperTopic)
	assert.Equal(t, "newest", state.LastWhisperContent)
	assert.True(t, state.LastWhisperAt.Equal(now.Add(-time.Minute)))

	recordStatuslineWhisper(root, agentID, []whisperstore.WhisperEntry{
		{Source: "murmur", PrincipalID: "sam", Content: "stale batch", CreatedAt: now.Add(-time.Hour)},
	})
	assert.Equal(t, "newest", oxactivity.LoadState(root, agentID).LastWhisperContent)

	recordStatuslineWhisper(root, agentID, []whisperstore.WhisperEntry{{Source: "nudge", CreatedAt: now.Add(time.Hour)}})
	assert.Equal(t, "newest", oxactivity.LoadState(root, agentID).LastWhisperContent, "non-murmur whispers are ignored")
}
