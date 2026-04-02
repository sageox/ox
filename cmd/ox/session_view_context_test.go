package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"testing"

	"github.com/sageox/ox/internal/session/contexttrace"
)

func TestViewContextTraceJSON_StructureAndCounts(t *testing.T) {
	events := []contexttrace.Event{
		{Type: contexttrace.EventProvided, Source: contexttrace.SourceTeamContext, Doc: "AGENTS.md", InlineTokens: 2048},
		{Type: contexttrace.EventProvided, Source: contexttrace.SourceTeamMemory, Doc: "MEMORY.md", ReadOnDemand: true},
		{Type: contexttrace.EventProvided, Source: contexttrace.SourceTeamWhisper, From: "OxABC", Topic: "wip"},
		{Type: contexttrace.EventInfluenced, Source: contexttrace.SourceTeamContext, Doc: "AGENTS.md", Decision: "used snake_case per team convention"},
	}

	// capture stdout
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := viewContextTraceJSON(events, "2026-04-01T10-00-ryan-OxTest", 0)

	w.Close()
	os.Stdout = old

	if err != nil {
		t.Fatalf("viewContextTraceJSON returned error: %v", err)
	}

	var buf bytes.Buffer
	io.Copy(&buf, r)

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
	events := []contexttrace.Event{
		{Type: contexttrace.EventProvided, Source: contexttrace.SourceTeamContext, Doc: "A.md"},
		{Type: contexttrace.EventProvided, Source: contexttrace.SourceTeamContext, Doc: "B.md"},
		{Type: contexttrace.EventProvided, Source: contexttrace.SourceTeamContext, Doc: "C.md"},
	}

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := viewContextTraceJSON(events, "test-session", 2)

	w.Close()
	os.Stdout = old

	if err != nil {
		t.Fatalf("viewContextTraceJSON returned error: %v", err)
	}

	var buf bytes.Buffer
	io.Copy(&buf, r)

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
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := viewContextTraceJSON(nil, "empty-session", 0)

	w.Close()
	os.Stdout = old

	if err != nil {
		t.Fatalf("viewContextTraceJSON returned error: %v", err)
	}

	var buf bytes.Buffer
	io.Copy(&buf, r)

	var out contextTraceJSONOutput
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatalf("invalid JSON output: %v", err)
	}

	if out.EventCount != 0 {
		t.Errorf("event_count = %d, want 0", out.EventCount)
	}
}

func TestFormatTraceTimestamp(t *testing.T) {
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
			got := formatTraceTimestamp(tt.input)
			if got != tt.want {
				t.Errorf("formatTraceTimestamp(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
