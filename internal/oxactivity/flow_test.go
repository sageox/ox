package oxactivity

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFlowOf_FromRealCommandLines classifies real command lines and checks
// which way their data moves.
// Failure prevented: a murmur or a saved plan drawn as context arriving, a
// search drawn as data leaving, or plumbing (doctor, status, the report
// itself) drawn as team data at all.
func TestFlowOf_FromRealCommandLines(t *testing.T) {
	tests := []struct {
		command string
		dir     Direction // "" = draws no lane
		step    string
	}{
		{`ox query "auth"`, DirectionIn, "searching team discussions"},
		{`cd repo && ox agent OxAb12 query "auth" 2>&1 | head`, DirectionIn, "searching team discussions"},
		{`ox agent prime`, DirectionIn, "loading team context"},
		{`ox agent OxAb12 team-ctx`, DirectionIn, "loading team context"},
		{`ox plan view my-plan`, DirectionIn, "pulling plan"},
		{`ox plan list`, DirectionIn, "listing plans"},
		{`ox plan enrich --json`, DirectionIn, "enriching plan"},
		{`ox code search "x" type:symbol`, DirectionIn, "searching code"},
		{`ox code prs --sort stalled`, DirectionIn, "checking pull requests"},
		{`ox code activity --since 7d`, DirectionIn, "checking repo activity"},
		{`ox session list --limit 5`, DirectionIn, "reading sessions"},
		{`ox conversation show cnv_01a1e588-77e5-75ab-9537-d40f46ef768b`, DirectionIn, "reading discussion"},
		{`ox murmur list`, DirectionIn, "reading murmurs"},
		{`ox coworker load go-pro`, DirectionIn, "loading coworker"},
		{`ox recap --json`, DirectionIn, "reading recap"},

		{`ox murmur --topic=wip "building the lane"`, DirectionOut, "posting murmur"},
		{`ox plan save --file plan.html`, DirectionOut, "saving plan"},
		{`ox agent OxAb12 session stop`, DirectionOut, "uploading session"},
		{`ox agent OxAb12 context used cnv_x --why "y"`, DirectionOut, "recording context use"},

		{`ox doctor`, "", ""},
		{`ox status`, "", ""},
		{`ox session report --json`, "", ""},
		{`ox config set statusline off`, "", ""},
		{`ox murmur status`, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			act, ok := ClassifyCommand(tt.command)
			require.True(t, ok, "an ox command always classifies")
			flow, moves := FlowOf(act.Command)
			if tt.dir == "" {
				assert.False(t, moves, "command %q", act.Command)
				return
			}
			require.True(t, moves, "command %q", act.Command)
			assert.Equal(t, tt.dir, flow.Direction)
			assert.Equal(t, tt.step, flow.Step)
		})
	}
}

// Failure prevented: SageOx MCP tools (the path Codex and the ChatGPT app
// often use) drawing no lane, or a write tool drawn as a read.
func TestFlowOf_MCPTools(t *testing.T) {
	for tool, want := range map[string]Direction{
		"mcp__claude_ai_sageox__Search":         DirectionIn,
		"mcp__sageox__Prime":                    DirectionIn,
		"mcp__sageox__ConversationTranscript":   DirectionIn,
		"mcp__sageox__ConversationSomethingNew": DirectionIn,
		"mcp__sageox__EnrichPlan":               DirectionIn,
		"mcp__claude_ai_sageox__PublishMurmur":  DirectionOut,
		"mcp__sageox__SavePlan":                 DirectionOut,
		"mcp__sageox__SaveSessionSummary":       DirectionOut,
	} {
		act, ok := Classify(tool, json.RawMessage(`{}`))
		require.True(t, ok, tool)
		flow, moves := FlowOf(act.Command)
		require.True(t, moves, "%s (command %q)", tool, act.Command)
		assert.Equal(t, want, flow.Direction, tool)
		assert.NotEmpty(t, flow.Step, tool)
	}
	_, moves := FlowOf("mcp SomeToolNobodyListed")
	assert.False(t, moves, "an unknown tool draws nothing rather than guessing a direction")
}

// Every listed flow has both a direction and running words.
func TestFlows_TableIsComplete(t *testing.T) {
	for command, f := range flows {
		assert.Contains(t, []Direction{DirectionIn, DirectionOut}, f.Direction, command)
		assert.NotEmpty(t, f.Step, command)
	}
}
