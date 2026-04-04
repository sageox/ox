package main

import (
	"testing"
	"time"
)

func TestParseMessageParts_Text(t *testing.T) {
	ts := time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)
	partsJSON := `[{"type":"text","data":{"text":"hello world"}}]`

	entries := parseMessageParts("user", partsJSON, ts)
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].Role != "user" {
		t.Errorf("expected role user, got %s", entries[0].Role)
	}
	if entries[0].Content != "hello world" {
		t.Errorf("expected content 'hello world', got %q", entries[0].Content)
	}
}

func TestParseMessageParts_AssistantText(t *testing.T) {
	ts := time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)
	partsJSON := `[{"type":"text","data":{"text":"I'll help you with that."}}]`

	entries := parseMessageParts("assistant", partsJSON, ts)
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].Role != "assistant" {
		t.Errorf("expected role assistant, got %s", entries[0].Role)
	}
}

func TestParseMessageParts_ToolCall(t *testing.T) {
	ts := time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)
	partsJSON := `[{"type":"tool_call","data":{"id":"tc_1","name":"Bash","input":"{\"command\":\"ls\"}"}}]`

	entries := parseMessageParts("assistant", partsJSON, ts)
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].Role != "tool" {
		t.Errorf("expected role tool, got %s", entries[0].Role)
	}
	if entries[0].ToolName != "Bash" {
		t.Errorf("expected tool name Bash, got %s", entries[0].ToolName)
	}
	if entries[0].CallID != "tc_1" {
		t.Errorf("expected call ID tc_1, got %s", entries[0].CallID)
	}
	if entries[0].ToolInput != `{"command":"ls"}` {
		t.Errorf("unexpected tool input: %s", entries[0].ToolInput)
	}
}

func TestParseMessageParts_ToolResult(t *testing.T) {
	ts := time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)
	partsJSON := `[{"type":"tool_result","data":{"tool_call_id":"tc_1","name":"Bash","content":"file.txt\ndir/","is_error":false}}]`

	entries := parseMessageParts("tool", partsJSON, ts)
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].ToolOutput != "file.txt\ndir/" {
		t.Errorf("unexpected tool output: %q", entries[0].ToolOutput)
	}
	if entries[0].IsError {
		t.Error("expected is_error=false")
	}
	if entries[0].CallID != "tc_1" {
		t.Errorf("expected call ID tc_1, got %s", entries[0].CallID)
	}
}

func TestParseMessageParts_ToolResultError(t *testing.T) {
	ts := time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)
	partsJSON := `[{"type":"tool_result","data":{"tool_call_id":"tc_2","name":"Bash","content":"command not found","is_error":true}}]`

	entries := parseMessageParts("tool", partsJSON, ts)
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if !entries[0].IsError {
		t.Error("expected is_error=true")
	}
}

func TestParseMessageParts_SkipsReasoning(t *testing.T) {
	ts := time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)
	partsJSON := `[{"type":"reasoning","data":{"thinking":"let me think..."}}]`

	entries := parseMessageParts("assistant", partsJSON, ts)
	if len(entries) != 0 {
		t.Errorf("expected 0 entries for reasoning, got %d", len(entries))
	}
}

func TestParseMessageParts_SkipsFinish(t *testing.T) {
	ts := time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)
	partsJSON := `[{"type":"finish","data":{"reason":"end_turn","time":1234567890}}]`

	entries := parseMessageParts("assistant", partsJSON, ts)
	if len(entries) != 0 {
		t.Errorf("expected 0 entries for finish, got %d", len(entries))
	}
}

func TestParseMessageParts_MixedParts(t *testing.T) {
	ts := time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)
	partsJSON := `[
		{"type":"text","data":{"text":"Let me run that."}},
		{"type":"tool_call","data":{"id":"tc_1","name":"Bash","input":"ls"}},
		{"type":"reasoning","data":{"thinking":"analyzing output"}},
		{"type":"finish","data":{"reason":"end_turn","time":1234567890}}
	]`

	entries := parseMessageParts("assistant", partsJSON, ts)
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries (text + tool_call), got %d", len(entries))
	}
	if entries[0].Role != "assistant" {
		t.Errorf("first entry: expected role assistant, got %s", entries[0].Role)
	}
	if entries[1].ToolName != "Bash" {
		t.Errorf("second entry: expected tool Bash, got %s", entries[1].ToolName)
	}
}

func TestParseMessageParts_EmptyText(t *testing.T) {
	ts := time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)
	partsJSON := `[{"type":"text","data":{"text":""}}]`

	entries := parseMessageParts("user", partsJSON, ts)
	if len(entries) != 0 {
		t.Errorf("expected 0 entries for empty text, got %d", len(entries))
	}
}

func TestParseMessageParts_InvalidJSON(t *testing.T) {
	ts := time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)

	// plain text fallback for user messages
	entries := parseMessageParts("user", "not json", ts)
	if len(entries) != 1 {
		t.Fatalf("expected 1 fallback entry, got %d", len(entries))
	}
	if entries[0].Content != "not json" {
		t.Errorf("expected fallback content, got %q", entries[0].Content)
	}
}

func TestParseMessageParts_EmptyArray(t *testing.T) {
	ts := time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)
	entries := parseMessageParts("user", "[]", ts)
	if len(entries) != 0 {
		t.Errorf("expected 0 entries for empty array, got %d", len(entries))
	}
}

func TestExtractSessionID(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"opencode:abc123", "abc123"},
		{"opencode:", ""},
		{"other:abc123", ""},
		{"", ""},
		{"opencode:01JXYZ123456789", "01JXYZ123456789"},
	}
	for _, tt := range tests {
		got := extractSessionID(tt.input)
		if got != tt.want {
			t.Errorf("extractSessionID(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
