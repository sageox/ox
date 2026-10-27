package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sageox/agentx"
	"github.com/sageox/ox/internal/session/contexttrace"
)

// mcpText wraps a JSON document as an MCP text-content response.
func mcpText(t *testing.T, doc string) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal([]map[string]string{{"type": "text", "text": doc}})
	require.NoError(t, err)
	return raw
}

const searchDoc = `{"team":{"id":"team_1","name":"Platform"},"results":[
 {"source_id":"cnv_1","doc_type":"discussion","title":"Auth rework","url":"https://sageox.ai/c/cnv_1","score":0.9,"text":"` + leakMarker + `"},
 {"source_id":"deploy","source_type":"skill","title":"deploy","text":"` + leakMarker + `"},
 {"source_id":"chunk-7","text":"` + leakMarker + `"}
]}`

func TestSurfacedToolHits(t *testing.T) {
	tests := []struct {
		name    string
		command string
		input   func(t *testing.T) *agentx.HookInput
		want    []providedHit
	}{
		{
			name: "MCP Search: per-item ref_type, title, url, score; team object and bare chunks skipped", command: "mcp Search",
			input: func(t *testing.T) *agentx.HookInput { return &agentx.HookInput{ToolResponse: mcpText(t, searchDoc)} },
			want: []providedHit{
				{SurfacedRef: refOf("cnv_1", "discussion", "Auth rework", "https://sageox.ai/c/cnv_1"), Score: 0.9},
				{SurfacedRef: refOf("deploy", "skill", "deploy", "")},
			},
		},
		{
			name: "MCP Conversation: structuredContent", command: "mcp Conversation",
			input: func(t *testing.T) *agentx.HookInput {
				return &agentx.HookInput{ToolResponse: json.RawMessage(`{"structuredContent":{"conversation_id":"cnv_2","title":"Standup","summary":"` + leakMarker + `"}}`)}
			},
			want: []providedHit{{SurfacedRef: refOf("cnv_2", "discussion", "Standup", "")}},
		},
		{
			name: "MCP GetPlan: content result", command: "mcp GetPlan",
			input: func(t *testing.T) *agentx.HookInput {
				return &agentx.HookInput{ToolResponse: json.RawMessage(`{"content":[{"type":"text","text":"{\"plan_id\":\"pln_3\",\"title\":\"Rollout\",\"url\":\"https://sageox.ai/plan/pln_3\",\"body\":\"` + leakMarker + `\"}"}]}`)}
			},
			want: []providedHit{{SurfacedRef: refOf("pln_3", "plan", "Rollout", "https://sageox.ai/plan/pln_3")}},
		},
		{
			name: "MCP ListMurmurs capped at five, topic as title", command: "mcp ListMurmurs",
			input: func(t *testing.T) *agentx.HookInput {
				var ms []string
				for _, id := range []string{"m1", "m2", "m3", "m4", "m5", "m6"} {
					ms = append(ms, `{"id":"`+id+`","topic":"wip","body":"`+leakMarker+`"}`)
				}
				return &agentx.HookInput{ToolResponse: mcpText(t, `{"murmurs":[`+strings.Join(ms, ",")+`]}`)}
			},
			want: []providedHit{
				{SurfacedRef: refOf("m1", "murmur", "wip", "")}, {SurfacedRef: refOf("m2", "murmur", "wip", "")},
				{SurfacedRef: refOf("m3", "murmur", "wip", "")}, {SurfacedRef: refOf("m4", "murmur", "wip", "")},
				{SurfacedRef: refOf("m5", "murmur", "wip", "")},
			},
		},
		{
			name: "prose response yields nothing", command: "mcp Search",
			input: func(t *testing.T) *agentx.HookInput {
				return &agentx.HookInput{ToolResponse: mcpText(t, "No results.")}
			},
		},
		{
			name: "failed call yields nothing", command: "mcp Search",
			input: func(t *testing.T) *agentx.HookInput {
				return &agentx.HookInput{ToolResponse: mcpText(t, searchDoc), ToolError: "boom"}
			},
		},
		{
			name: "oversized response is not parsed", command: "mcp Search",
			input: func(t *testing.T) *agentx.HookInput {
				pad := strings.Repeat("x", maxSurfacedParseBytes)
				return &agentx.HookInput{ToolResponse: mcpText(t, `{"pad":"`+pad+`","results":[{"source_id":"cnv_1","title":"t"}]}`)}
			},
		},
		{
			name: "non-surfacing MCP tool", command: "mcp PublishMurmur",
			input: func(t *testing.T) *agentx.HookInput {
				return &agentx.HookInput{ToolResponse: mcpText(t, `{"id":"m1","topic":"wip"}`)}
			},
		},
		{
			name: "ox conversation show stdout", command: "conversation show",
			input: func(t *testing.T) *agentx.HookInput {
				return &agentx.HookInput{ToolResponse: bashStdout(t, `{"success":true,"data":{"conversation_id":"cnv_4","recording_id":"rec_4","title":"Retro","summary":{"human_summary":"`+leakMarker+`"}}}`)}
			},
			want: []providedHit{{SurfacedRef: refOf("cnv_4", "discussion", "Retro", "")}},
		},
		{
			name: "other ox commands are not parsed", command: "murmur list",
			input: func(t *testing.T) *agentx.HookInput {
				return &agentx.HookInput{ToolResponse: bashStdout(t, `{"id":"cnv_5","title":"x"}`)}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := surfacedToolHits(tt.command, tt.input(t))
			assert.Equal(t, tt.want, got)
			assertNoContent(t, got)
		})
	}
}

// TestAfterTool_MCPSearchTracesSurfacedItems drives the real PostToolUse
// hook: an MCP Search lands one invoked event plus one provided event per
// returned item, and no item text reaches the trace.
func TestAfterTool_MCPSearchTracesSurfacedItems(t *testing.T) {
	root := usageTraceEnv(t, nil)
	state := startUsageRecording(t, root)

	runAfterTool(t, root, "claude-code", usageAgentID, &agentx.HookInput{
		ToolName: "mcp__claude_ai_sageox__Search", ToolInput: json.RawMessage(`{"query":"auth"}`), ToolResponse: mcpText(t, searchDoc),
	})

	inv := invokedEvents(t, sessionTracePath(state))
	require.Len(t, inv, 1)
	assert.Empty(t, inv[0].Ref, "a multi-item search names no single item")

	prov := providedEvents(t, sessionTracePath(state))
	require.Len(t, prov, 2)
	assert.Equal(t, "cnv_1", prov[0].Ref)
	assert.Equal(t, "discussion", prov[0].RefType)
	assert.Equal(t, "Auth rework", prov[0].Title)
	assert.Equal(t, "https://sageox.ai/c/cnv_1", prov[0].URL)
	assert.Equal(t, contexttrace.SourceOnDemand, prov[0].Source)
	assert.Equal(t, contexttrace.MechanismRetrieval, prov[0].Mechanism)
	assert.Equal(t, inv[0].Seq, prov[0].Seq, "same turn frame as the invoked event")
	assert.Equal(t, "deploy", prov[1].Ref)

	raw, err := os.ReadFile(sessionTracePath(state))
	require.NoError(t, err)
	assert.NotContains(t, string(raw), leakMarker)
}

// TestAfterTool_SingleItemNamesInvokedEvent: a single-item call fills the
// invoked event's ref/title/url; the ox CLI command (which traces its own
// item) gets no second provided event from the hook.
func TestAfterTool_SingleItemNamesInvokedEvent(t *testing.T) {
	tests := []struct {
		name         string
		input        *agentx.HookInput
		wantRef      string
		wantProvided int
		untitled     bool // named by its argument; the command traces its own titled item
	}{
		{
			name: "MCP Conversation",
			input: &agentx.HookInput{ToolName: "mcp__claude_ai_sageox__Conversation", ToolInput: json.RawMessage(`{}`),
				ToolResponse: json.RawMessage(`[{"type":"text","text":"{\"conversation_id\":\"cnv_2\",\"title\":\"Standup\"}"}]`)},
			wantRef: "cnv_2", wantProvided: 1,
		},
		{
			name: "ox conversation show",
			input: &agentx.HookInput{ToolName: "Bash", ToolInput: shellInput(t, "command", "ox conversation show cnv_4"),
				ToolResponse: bashStdout(t, `{"success":true,"data":{"conversation_id":"cnv_4","title":"Retro"}}`)},
			wantRef: "cnv_4", wantProvided: 0,
		},
		{
			name: "ox conversation transcript by rec id names the canonical cnv_",
			input: &agentx.HookInput{ToolName: "Bash", ToolInput: shellInput(t, "command", "ox conversation transcript "+convTestFullRec+" --full"),
				ToolResponse: bashStdout(t, `{"success":true,"data":{"cues":[]}}`)},
			wantRef: convTestFullCnv, wantProvided: 0, untitled: true,
		},
		{
			name: "ox conversation topics by link",
			input: &agentx.HookInput{ToolName: "Bash", ToolInput: shellInput(t, "command", "ox conversation topics https://sageox.ai/c/"+convTestFullRec),
				ToolResponse: bashStdout(t, `{"success":true,"data":{"topics":[]}}`)},
			wantRef: convTestFullCnv, wantProvided: 0, untitled: true,
		},
		{
			name: "ox conversation topic by citation",
			input: &agentx.HookInput{ToolName: "Bash", ToolInput: shellInput(t, "command", "ox conversation topic '"+convTestCueURI+"' "+convTestTopicID),
				ToolResponse: bashStdout(t, `{"success":true,"data":{"topic":{}}}`)},
			wantRef: convTestFullCnv, wantProvided: 0, untitled: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := usageTraceEnv(t, nil)
			state := startUsageRecording(t, root)
			runAfterTool(t, root, "claude-code", usageAgentID, tt.input)

			inv := invokedEvents(t, sessionTracePath(state))
			require.Len(t, inv, 1)
			assert.Equal(t, tt.wantRef, inv[0].Ref)
			if !tt.untitled {
				assert.NotEmpty(t, inv[0].Title)
			}
			assert.Len(t, providedEvents(t, sessionTracePath(state)), tt.wantProvided)
		})
	}
}
