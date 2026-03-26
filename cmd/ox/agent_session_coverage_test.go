package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseTitle_Coverage(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "no title flag",
			args: []string{"start"},
			want: "",
		},
		{
			name: "title with separate arg",
			args: []string{"start", "--title", "My Session"},
			want: "My Session",
		},
		{
			name: "title with equals",
			args: []string{"start", "--title=My Session"},
			want: "My Session",
		},
		{
			name: "title at end without value",
			args: []string{"start", "--title"},
			want: "",
		},
		{
			name: "empty args",
			args: []string{},
			want: "",
		},
		{
			name: "title between other flags",
			args: []string{"--verbose", "--title", "Plan A", "--json"},
			want: "Plan A",
		},
		{
			name: "title with empty value",
			args: []string{"--title", ""},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := parseTitle(tt.args)
			if got != tt.want {
				t.Errorf("parseTitle(%v) = %q, want %q", tt.args, got, tt.want)
			}
		})
	}
}

func TestConvertStoredEntries_Coverage(t *testing.T) {
	t.Parallel()

	t.Run("empty input", func(t *testing.T) {
		t.Parallel()
		entries := convertStoredEntries(nil)
		if len(entries) != 0 {
			t.Errorf("expected 0 entries, got %d", len(entries))
		}
	})

	t.Run("basic fields", func(t *testing.T) {
		t.Parallel()
		stored := []map[string]any{
			{
				"type":    "user",
				"content": "hello world",
			},
			{
				"type":       "tool",
				"content":    "result",
				"tool_name":  "bash",
				"tool_input": "ls",
			},
		}
		entries := convertStoredEntries(stored)
		if len(entries) != 2 {
			t.Fatalf("expected 2 entries, got %d", len(entries))
		}
		if string(entries[0].Type) != "user" {
			t.Errorf("entries[0].Type = %q, want %q", entries[0].Type, "user")
		}
		if entries[0].Content != "hello world" {
			t.Errorf("entries[0].Content = %q, want %q", entries[0].Content, "hello world")
		}
		if entries[1].ToolName != "bash" {
			t.Errorf("entries[1].ToolName = %q, want %q", entries[1].ToolName, "bash")
		}
	})

	t.Run("valid timestamp", func(t *testing.T) {
		t.Parallel()
		ts := "2024-06-15T10:30:00Z"
		stored := []map[string]any{
			{"type": "assistant", "content": "hi", "timestamp": ts},
		}
		entries := convertStoredEntries(stored)
		if entries[0].Timestamp.IsZero() {
			t.Error("expected non-zero timestamp")
		}
		if entries[0].Timestamp.Year() != 2024 {
			t.Errorf("timestamp year = %d, want 2024", entries[0].Timestamp.Year())
		}
	})

	t.Run("invalid timestamp ignored", func(t *testing.T) {
		t.Parallel()
		stored := []map[string]any{
			{"type": "user", "content": "msg", "timestamp": "not-a-date"},
		}
		entries := convertStoredEntries(stored)
		if !entries[0].Timestamp.IsZero() {
			t.Error("expected zero timestamp for invalid date string")
		}
	})

	t.Run("missing fields produce zero values", func(t *testing.T) {
		t.Parallel()
		stored := []map[string]any{
			{"unrelated_key": 42},
		}
		entries := convertStoredEntries(stored)
		if len(entries) != 1 {
			t.Fatalf("expected 1 entry, got %d", len(entries))
		}
		if entries[0].Content != "" {
			t.Errorf("expected empty content, got %q", entries[0].Content)
		}
	})
}

func TestRawJSONLHasEntries_Coverage(t *testing.T) {
	t.Parallel()

	t.Run("nonexistent file", func(t *testing.T) {
		t.Parallel()
		if rawJSONLHasEntries("/does/not/exist.jsonl") {
			t.Error("expected false for nonexistent file")
		}
	})

	t.Run("empty file", func(t *testing.T) {
		t.Parallel()
		f := filepath.Join(t.TempDir(), "raw.jsonl")
		os.WriteFile(f, []byte(""), 0644)
		if rawJSONLHasEntries(f) {
			t.Error("expected false for empty file")
		}
	})

	t.Run("header only", func(t *testing.T) {
		t.Parallel()
		f := filepath.Join(t.TempDir(), "raw.jsonl")
		os.WriteFile(f, []byte(`{"_meta":{"version":"1"}}`+"\n"), 0644)
		if rawJSONLHasEntries(f) {
			t.Error("expected false for header-only file")
		}
	})

	t.Run("header plus entry", func(t *testing.T) {
		t.Parallel()
		f := filepath.Join(t.TempDir(), "raw.jsonl")
		os.WriteFile(f, []byte(`{"_meta":{"version":"1"}}`+"\n"+`{"type":"user","content":"hi"}`+"\n"), 0644)
		if !rawJSONLHasEntries(f) {
			t.Error("expected true for file with entries beyond header")
		}
	})

	t.Run("multiple entries", func(t *testing.T) {
		t.Parallel()
		f := filepath.Join(t.TempDir(), "raw.jsonl")
		lines := `{"header":true}
{"type":"user","content":"a"}
{"type":"assistant","content":"b"}
`
		os.WriteFile(f, []byte(lines), 0644)
		if !rawJSONLHasEntries(f) {
			t.Error("expected true for file with multiple lines")
		}
	})
}

func TestBuildSessionStartOutput_Coverage(t *testing.T) {
	t.Parallel()

	ts := time.Date(2024, 6, 15, 10, 0, 0, 0, time.UTC)
	out := buildSessionStartOutput("agent-123", "claude-code", "/tmp/session.jsonl", "Test Plan", "notice text", ts)

	if !out.Success {
		t.Error("expected Success = true")
	}
	if out.Type != "session_start" {
		t.Errorf("Type = %q, want %q", out.Type, "session_start")
	}
	if out.AgentID != "agent-123" {
		t.Errorf("AgentID = %q, want %q", out.AgentID, "agent-123")
	}
	if out.Title != "Test Plan" {
		t.Errorf("Title = %q, want %q", out.Title, "Test Plan")
	}
	if out.Notice != "notice text" {
		t.Errorf("Notice = %q, want %q", out.Notice, "notice text")
	}
	if out.Hint != "Run /ox-session-stop to end recording" {
		t.Errorf("unexpected Hint: %q", out.Hint)
	}
}

func TestIsManualSessionAgent_Coverage(t *testing.T) {
	t.Parallel()
	tests := []struct {
		agentType string
		want      bool
	}{
		{"codex", true},
		{"claude-code", false},
		{"gemini", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.agentType, func(t *testing.T) {
			t.Parallel()
			got := isManualSessionAgent(tt.agentType)
			if got != tt.want {
				t.Errorf("isManualSessionAgent(%q) = %v, want %v", tt.agentType, got, tt.want)
			}
		})
	}
}
