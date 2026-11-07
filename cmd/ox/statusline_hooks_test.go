package main

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/sageox/agentx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/oxactivity"
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

// TestHandleAfterTool_CountsRetrievals drives the real PostToolUse handler
// with a sequence of tool calls and checks the counts the status line reads.
// Failure prevented: the bar never learns a plan or discussion shaped the
// session, counts one item read twice as two, or counts searches, listings
// and ox plumbing as if an item had been used.
func TestHandleAfterTool_CountsRetrievals(t *testing.T) {
	const cnv = "cnv_019ffe10-0000-7000-8000-000000000011"
	tests := []struct {
		name       string
		commands   []string
		input      *agentx.HookInput // used instead of commands when set
		statusline string
		want       oxactivity.Counts
		wantNoFile bool
	}{
		{name: "plan view", commands: []string{"ox plan view x"}, want: oxactivity.Counts{Plans: 1}},
		{name: "conversation show", commands: []string{"ox conversation show " + cnv}, want: oxactivity.Counts{Discussions: 1}},
		{name: "conversation via agent address", commands: []string{"ox agent OxHk01 conversation topics " + cnv}, want: oxactivity.Counts{Discussions: 1}},
		{name: "team context", commands: []string{"ox agent OxHk01 team-ctx"}, want: oxactivity.Counts{Docs: 1}},
		{name: "same ref twice counts once", commands: []string{"ox plan view x", "ox plan view x", "ox plan show x"}, want: oxactivity.Counts{Plans: 1}},
		{name: "two plans and a discussion", commands: []string{"ox plan view x", "ox plan view y", "ox conversation show " + cnv}, want: oxactivity.Counts{Discussions: 1, Plans: 2}},
		{name: "plan view without a slug", commands: []string{"ox plan view"}, wantNoFile: true},
		{name: "murmur list", commands: []string{"ox murmur list"}, wantNoFile: true},
		{name: "query", commands: []string{`ox query "auth decisions"`}, wantNoFile: true},
		{name: "code search", commands: []string{`ox code search "Load"`}, wantNoFile: true},
		{name: "session view", commands: []string{"ox session view 2026-10-01-foo"}, wantNoFile: true},
		{name: "decision enrich", commands: []string{"ox decision enrich --topic auth"}, wantNoFile: true},
		{name: "conversation search", commands: []string{"ox conversation search pricing"}, wantNoFile: true},
		{name: "agent prime", commands: []string{"ox agent OxHk01 prime"}, wantNoFile: true},
		{name: "heartbeat is plumbing", commands: []string{"ox agent OxHk01 heartbeat"}, wantNoFile: true},
		{name: "posting a murmur", commands: []string{`ox murmur --topic wip "my-plan"`}, wantNoFile: true},
		{name: "non-ox command", commands: []string{"go test ./..."}, wantNoFile: true},
		{name: "non-shell tool", input: &agentx.HookInput{SessionID: hookSessionID, ToolName: "Read", ToolInput: json.RawMessage(`{"file_path":"x"}`)}, wantNoFile: true},
		{name: "statusline off", commands: []string{"ox plan view x"}, statusline: "off", wantNoFile: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := newStatuslineHookProject(t)
			if tt.statusline != "" {
				require.NoError(t, SetConfigValue("statusline", tt.statusline, ConfigLevelUser, ""))
			}
			const agentID = "OxHk01"
			inputs := []*agentx.HookInput{tt.input}
			if tt.input == nil {
				inputs = nil
				for _, c := range tt.commands {
					inputs = append(inputs, bashInput(t, c))
				}
			}
			for _, in := range inputs {
				require.NoError(t, handleAfterTool(&HookContext{
					Phase:       phaseAfterTool,
					AgentType:   "claude-code",
					ProjectRoot: root,
					Marker:      &SessionMarker{AgentID: agentID},
					Input:       in,
				}))
			}

			key := oxactivity.StateKey(agentID, hookSessionID)
			if tt.wantNoFile {
				assert.NoFileExists(t, oxactivity.StatePath(root, key), "nothing countable was used")
				return
			}
			assert.Equal(t, tt.want, oxactivity.LoadState(root, key).Counts())
			assert.Equal(t, oxactivity.State{}, oxactivity.LoadState(root, agentID), "state is per harness session, never agent-wide")
		})
	}
}

// TestHandleAfterTool_NilInputIsNoop: a hook with no payload must not write.
func TestHandleAfterTool_NilInputIsNoop(t *testing.T) {
	root := newStatuslineHookProject(t)
	require.NoError(t, handleAfterTool(&HookContext{
		Phase: phaseAfterTool, AgentType: "claude-code", ProjectRoot: root,
		Marker: &SessionMarker{AgentID: "OxHk02"},
	}))
	assert.NoFileExists(t, oxactivity.StatePath(root, oxactivity.StateKey("OxHk02", "")))
}
