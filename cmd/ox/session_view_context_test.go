package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sageox/ox/internal/session/contexttrace"
)

func TestViewContextTraceJSON_StructureAndCounts(t *testing.T) {
	t.Parallel()
	events := []contexttrace.Event{
		{Type: contexttrace.EventProvided, Source: contexttrace.SourceTeamContext, Doc: "AGENTS.md", InlineTokens: 2048},
		{Type: contexttrace.EventProvided, Source: contexttrace.SourceTeamMemory, Doc: "MEMORY.md", ReadOnDemand: true},
		{Type: contexttrace.EventProvided, Source: contexttrace.SourceTeamWhisper, From: "OxABC", Topic: "wip"},
		{Type: contexttrace.EventInfluenced, Source: contexttrace.SourceTeamContext, Doc: "AGENTS.md", Decision: "used snake_case per team convention"},
	}

	var buf bytes.Buffer
	if err := viewContextTraceJSON(&buf, events, "2026-04-01T10-00-ryan-OxTest", 0); err != nil {
		t.Fatalf("viewContextTraceJSON returned error: %v", err)
	}

	var out contextTraceJSONOutput
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatalf("invalid JSON output: %v\n%s", err, buf.String())
	}

	if out.SessionName != "2026-04-01T10-00-ryan-OxTest" {
		t.Errorf("session_name = %q, want %q", out.SessionName, "2026-04-01T10-00-ryan-OxTest")
	}
	if out.Provided != 3 {
		t.Errorf("provided = %d, want 3", out.Provided)
	}
	if out.Influenced != 1 {
		t.Errorf("influenced = %d, want 1", out.Influenced)
	}
	if out.EventCount != 4 {
		t.Errorf("event_count = %d, want 4", out.EventCount)
	}
	if len(out.Events) != 4 {
		t.Errorf("events length = %d, want 4", len(out.Events))
	}
}

func TestViewContextTraceJSON_Limit(t *testing.T) {
	t.Parallel()
	events := []contexttrace.Event{
		{Type: contexttrace.EventProvided, Source: contexttrace.SourceTeamContext, Doc: "A.md"},
		{Type: contexttrace.EventProvided, Source: contexttrace.SourceTeamContext, Doc: "B.md"},
		{Type: contexttrace.EventProvided, Source: contexttrace.SourceTeamContext, Doc: "C.md"},
	}

	var buf bytes.Buffer
	if err := viewContextTraceJSON(&buf, events, "test-session", 2); err != nil {
		t.Fatalf("viewContextTraceJSON returned error: %v", err)
	}

	var out contextTraceJSONOutput
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatalf("invalid JSON output: %v", err)
	}

	// event_count reflects total, events array is limited
	if out.EventCount != 3 {
		t.Errorf("event_count = %d, want 3 (total)", out.EventCount)
	}
	if len(out.Events) != 2 {
		t.Errorf("events length = %d, want 2 (limited)", len(out.Events))
	}
}

func TestViewContextTraceJSON_Empty(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	if err := viewContextTraceJSON(&buf, nil, "empty-session", 0); err != nil {
		t.Fatalf("viewContextTraceJSON returned error: %v", err)
	}

	var out contextTraceJSONOutput
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatalf("invalid JSON output: %v", err)
	}

	if out.EventCount != 0 {
		t.Errorf("event_count = %d, want 0", out.EventCount)
	}
}

func TestFormatTraceTimestamp(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "valid rfc3339", input: "2026-04-01T14:30:00Z", want: "14:30:00"},
		{name: "empty", input: "", want: ""},
		{name: "invalid format", input: "not-a-timestamp", want: "not-a-timestamp"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := formatTraceTimestamp(tt.input)
			if got != tt.want {
				t.Errorf("formatTraceTimestamp(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// TestViewContextTrace_CountsInvokedSeparately mixes old event types with the
// new `invoked` ox-call events. Failure prevented: every ox call the AI
// coworker made is counted (and listed) as "provided context", inflating what
// the session view says reached the coworker.
func TestViewContextTrace_CountsInvokedSeparately(t *testing.T) {
	t.Parallel()
	events := []contexttrace.Event{
		{Type: contexttrace.EventProvided, Source: contexttrace.SourceTeamContext, Doc: "AGENTS.md", InlineTokens: 2048},
		{Type: contexttrace.EventConsulted, Source: contexttrace.SourceOnDemand, Mechanism: contexttrace.MechanismRetrieval, RefType: "query", Query: "auth"},
		{Type: contexttrace.EventInfluenced, Source: contexttrace.SourceTeamContext, Doc: "AGENTS.md", Decision: "kept TTL"},
		{Type: contexttrace.EventInvoked, Source: contexttrace.SourceOxCLI, Command: "murmur list", Label: "checked murmurs", OutputBytes: 800, InlineTokens: 200},
		{Type: contexttrace.EventInvoked, Source: contexttrace.SourceOxCLI, Command: "query", Label: "searched team memory", Failed: true},
	}

	var buf bytes.Buffer
	require.NoError(t, viewContextTraceJSON(&buf, events, "s", 0))
	var out contextTraceJSONOutput
	require.NoError(t, json.Unmarshal(buf.Bytes(), &out))
	assert.Equal(t, 2, out.Provided, "provided keeps provided + consulted, never ox calls")
	assert.Equal(t, 1, out.Influenced)
	var raw map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &raw))
	assert.EqualValues(t, 2, raw["invoked"], "ox calls get their own count")
	assert.Equal(t, 5, out.EventCount)

	var text bytes.Buffer
	viewContextTraceText(&text, events, "s")
	got := text.String()
	assert.Contains(t, got, "2 sources")
	assert.Contains(t, got, "2 ox calls")
	assert.Contains(t, got, "checked murmurs")
	provided := got[strings.Index(got, "Provided Context"):strings.Index(got, "Influenced Decisions")]
	assert.NotContains(t, provided, "ox-cli", "ox calls must not be listed under provided context")
}
