package main

import (
	"encoding/json"
	"testing"

	"github.com/sageox/ox/internal/session"
)

func TestParseCapturePriorFile(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "no file flag",
			args: []string{"capture-prior"},
			want: "",
		},
		{
			name: "file flag separate",
			args: []string{"--file", "/tmp/history.jsonl"},
			want: "/tmp/history.jsonl",
		},
		{
			name: "file flag equals",
			args: []string{"--file=/tmp/history.jsonl"},
			want: "/tmp/history.jsonl",
		},
		{
			name: "file flag at end without value",
			args: []string{"--file"},
			want: "",
		},
		{
			name: "empty args",
			args: []string{},
			want: "",
		},
		{
			name: "file flag between other args",
			args: []string{"--title", "test", "--file", "/path/data.jsonl", "--verbose"},
			want: "/path/data.jsonl",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := parseCapturePriorFile(tt.args)
			if got != tt.want {
				t.Errorf("parseCapturePriorFile(%v) = %q, want %q", tt.args, got, tt.want)
			}
		})
	}
}

func TestFormatCapturePriorOutput(t *testing.T) {
	t.Parallel()

	t.Run("basic result", func(t *testing.T) {
		t.Parallel()
		result := &session.CaptureResult{
			AgentID:         "agent-001",
			Path:            "/tmp/sessions/session-001",
			SessionName:     "session-001",
			EntryCount:      5,
			SecretsRedacted: 2,
			Title:           "Planning discussion",
		}

		data, err := formatCapturePriorOutput(result)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		var output capturePriorOutput
		if err := json.Unmarshal(data, &output); err != nil {
			t.Fatalf("failed to parse output: %v", err)
		}

		if !output.Success {
			t.Error("expected Success = true")
		}
		if output.Type != "session_capture_prior" {
			t.Errorf("Type = %q, want %q", output.Type, "session_capture_prior")
		}
		if output.AgentID != "agent-001" {
			t.Errorf("AgentID = %q, want %q", output.AgentID, "agent-001")
		}
		if output.EntryCount != 5 {
			t.Errorf("EntryCount = %d, want 5", output.EntryCount)
		}
		if output.SecretsRedacted != 2 {
			t.Errorf("SecretsRedacted = %d, want 2", output.SecretsRedacted)
		}
		if output.Title != "Planning discussion" {
			t.Errorf("Title = %q, want %q", output.Title, "Planning discussion")
		}
	})

	t.Run("minimal result", func(t *testing.T) {
		t.Parallel()
		result := &session.CaptureResult{
			AgentID:    "agent-002",
			Path:       "/tmp/s",
			EntryCount: 0,
		}

		data, err := formatCapturePriorOutput(result)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		var output capturePriorOutput
		if err := json.Unmarshal(data, &output); err != nil {
			t.Fatalf("failed to parse output: %v", err)
		}

		if output.EntryCount != 0 {
			t.Errorf("EntryCount = %d, want 0", output.EntryCount)
		}
		if output.SessionName != "" {
			t.Errorf("SessionName = %q, want empty", output.SessionName)
		}
	})
}
