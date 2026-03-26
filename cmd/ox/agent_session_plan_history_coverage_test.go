package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTruncateString_Coverage(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		input  string
		maxLen int
		want   string
	}{
		{
			name:   "short string unchanged",
			input:  "hello",
			maxLen: 10,
			want:   "hello",
		},
		{
			name:   "exact length unchanged",
			input:  "hello",
			maxLen: 5,
			want:   "hello",
		},
		{
			name:   "truncated with ellipsis",
			input:  "hello world this is long",
			maxLen: 10,
			want:   "hello w...",
		},
		{
			name:   "newlines replaced with spaces",
			input:  "line1\nline2\nline3",
			maxLen: 50,
			want:   "line1 line2 line3",
		},
		{
			name:   "newlines replaced then truncated",
			input:  "line1\nline2\nline3 with more content",
			maxLen: 15,
			want:   "line1 line2 ...",
		},
		{
			name:   "leading and trailing whitespace trimmed",
			input:  "  hello  ",
			maxLen: 50,
			want:   "hello",
		},
		{
			name:   "empty string",
			input:  "",
			maxLen: 10,
			want:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := truncateString(tt.input, tt.maxLen)
			if got != tt.want {
				t.Errorf("truncateString(%q, %d) = %q, want %q", tt.input, tt.maxLen, got, tt.want)
			}
		})
	}
}

func TestParsePlanHistoryFile_Coverage(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "no file flag",
			args: []string{"plan-history"},
			want: "",
		},
		{
			name: "file flag separate",
			args: []string{"--file", "/tmp/plan.jsonl"},
			want: "/tmp/plan.jsonl",
		},
		{
			name: "file flag equals",
			args: []string{"--file=/tmp/plan.jsonl"},
			want: "/tmp/plan.jsonl",
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := parsePlanHistoryFile(tt.args)
			if got != tt.want {
				t.Errorf("parsePlanHistoryFile(%v) = %q, want %q", tt.args, got, tt.want)
			}
		})
	}
}

func TestExtractPlanFromEntries_Coverage(t *testing.T) {
	t.Parallel()

	t.Run("empty entries", func(t *testing.T) {
		t.Parallel()
		got := extractPlanFromEntries(nil)
		if got != "" {
			t.Errorf("expected empty string, got %q", got)
		}
	})

	t.Run("explicit is_plan marker", func(t *testing.T) {
		t.Parallel()
		entries := []planHistoryEntry{
			{Type: "user", Content: "make a plan"},
			{Type: "assistant", Content: "Here is the plan", IsPlan: true},
			{Type: "assistant", Content: "some other response"},
		}
		got := extractPlanFromEntries(entries)
		if got != "Here is the plan" {
			t.Errorf("got %q, want %q", got, "Here is the plan")
		}
	})

	t.Run("plan header in assistant message", func(t *testing.T) {
		t.Parallel()
		entries := []planHistoryEntry{
			{Type: "user", Content: "design the system"},
			{Type: "assistant", Content: "Let me think...\n\n## Plan\n\nStep 1: Do this\nStep 2: Do that"},
		}
		got := extractPlanFromEntries(entries)
		if got == "" {
			t.Error("expected non-empty plan from header match")
		}
	})

	t.Run("final plan header takes last assistant", func(t *testing.T) {
		t.Parallel()
		entries := []planHistoryEntry{
			{Type: "assistant", Content: "## Plan\nOld plan"},
			{Type: "user", Content: "revise it"},
			{Type: "assistant", Content: "## Final Plan\nNew plan"},
		}
		got := extractPlanFromEntries(entries)
		if got == "" {
			t.Error("expected non-empty plan")
		}
	})

	t.Run("no plan markers returns empty", func(t *testing.T) {
		t.Parallel()
		entries := []planHistoryEntry{
			{Type: "user", Content: "hello"},
			{Type: "assistant", Content: "just a normal response"},
		}
		got := extractPlanFromEntries(entries)
		if got != "" {
			t.Errorf("expected empty, got %q", got)
		}
	})

	t.Run("skips user messages when looking for headers", func(t *testing.T) {
		t.Parallel()
		entries := []planHistoryEntry{
			{Type: "user", Content: "## Plan\nThis is user content with plan header"},
		}
		got := extractPlanFromEntries(entries)
		if got != "" {
			t.Errorf("expected empty (user messages skipped), got %q", got)
		}
	})
}

func TestExtractLastAssistantMessage_Coverage(t *testing.T) {
	t.Parallel()

	t.Run("empty entries", func(t *testing.T) {
		t.Parallel()
		got := extractLastAssistantMessage(nil)
		if got != "" {
			t.Errorf("expected empty, got %q", got)
		}
	})

	t.Run("no assistant messages", func(t *testing.T) {
		t.Parallel()
		entries := []planHistoryEntry{
			{Type: "user", Content: "hello"},
			{Type: "system", Content: "context"},
		}
		got := extractLastAssistantMessage(entries)
		if got != "" {
			t.Errorf("expected empty, got %q", got)
		}
	})

	t.Run("returns last assistant", func(t *testing.T) {
		t.Parallel()
		entries := []planHistoryEntry{
			{Type: "assistant", Content: "first response"},
			{Type: "user", Content: "follow up"},
			{Type: "assistant", Content: "last response"},
		}
		got := extractLastAssistantMessage(entries)
		if got != "last response" {
			t.Errorf("got %q, want %q", got, "last response")
		}
	})

	t.Run("skips empty assistant content", func(t *testing.T) {
		t.Parallel()
		entries := []planHistoryEntry{
			{Type: "assistant", Content: "real content"},
			{Type: "assistant", Content: ""},
		}
		got := extractLastAssistantMessage(entries)
		if got != "real content" {
			t.Errorf("got %q, want %q", got, "real content")
		}
	})
}

func TestExtractDiagramsFromPlanEntries_Coverage(t *testing.T) {
	t.Parallel()

	t.Run("no diagrams", func(t *testing.T) {
		t.Parallel()
		entries := []planHistoryEntry{
			{Type: "assistant", Content: "no mermaid here"},
		}
		got := extractDiagramsFromPlanEntries(entries)
		if len(got) != 0 {
			t.Errorf("expected 0 diagrams, got %d", len(got))
		}
	})

	t.Run("deduplicates diagrams", func(t *testing.T) {
		t.Parallel()
		diagram := "```mermaid\ngraph LR\n  A --> B\n```"
		entries := []planHistoryEntry{
			{Type: "assistant", Content: "here:\n" + diagram},
			{Type: "assistant", Content: "repeated:\n" + diagram},
		}
		got := extractDiagramsFromPlanEntries(entries)
		if len(got) != 1 {
			t.Errorf("expected 1 unique diagram, got %d", len(got))
		}
	})
}

func TestReadPlanHistoryEntries_FromFile_Coverage(t *testing.T) {
	t.Parallel()

	t.Run("valid JSONL file", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		f := filepath.Join(dir, "plan.jsonl")
		content := `{"_meta":{"schema_version":"1","agent_type":"claude-code","session_id":"manual","started_at":"2024-01-01T00:00:00Z"}}
{"type":"user","content":"design the system","seq":1}
{"type":"assistant","content":"here is the plan","seq":2,"is_plan":true}
`
		os.WriteFile(f, []byte(content), 0644)

		entries, meta, err := readPlanHistoryEntries(f)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if meta == nil {
			t.Fatal("expected non-nil meta")
		}
		if meta.SchemaVersion != "1" {
			t.Errorf("meta.SchemaVersion = %q, want %q", meta.SchemaVersion, "1")
		}
		if len(entries) != 2 {
			t.Fatalf("expected 2 entries, got %d", len(entries))
		}
		if entries[0].Type != "user" {
			t.Errorf("entries[0].Type = %q, want %q", entries[0].Type, "user")
		}
		if !entries[1].IsPlan {
			t.Error("expected entries[1].IsPlan = true")
		}
	})

	t.Run("skips empty lines", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		f := filepath.Join(dir, "plan.jsonl")
		content := `{"type":"user","content":"hello","seq":1}

{"type":"assistant","content":"world","seq":2}

`
		os.WriteFile(f, []byte(content), 0644)

		entries, _, err := readPlanHistoryEntries(f)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(entries) != 2 {
			t.Fatalf("expected 2 entries, got %d", len(entries))
		}
	})

	t.Run("skips entries missing required fields", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		f := filepath.Join(dir, "plan.jsonl")
		content := `{"type":"user","content":"valid","seq":1}
{"type":"","content":"missing type"}
{"type":"user","content":""}
{"seq":99}
`
		os.WriteFile(f, []byte(content), 0644)

		entries, _, err := readPlanHistoryEntries(f)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(entries) != 1 {
			t.Errorf("expected 1 valid entry, got %d", len(entries))
		}
	})

	t.Run("nonexistent file", func(t *testing.T) {
		t.Parallel()
		_, _, err := readPlanHistoryEntries("/nonexistent/file.jsonl")
		if err == nil {
			t.Error("expected error for nonexistent file")
		}
	})
}

func TestExtractPlanSection_Coverage(t *testing.T) {
	t.Parallel()

	content := "Some intro\n\n## Plan\n\nStep 1\nStep 2\n\n## Other Section\n\nStuff"
	got := extractPlanSection(content, planHeaderPatterns[1]) // "## Plan"
	if got == "" {
		t.Error("expected non-empty plan section")
	}
	// should start from "## Plan"
	if len(got) < 7 || got[:7] != "## Plan" {
		t.Errorf("expected plan section to start with '## Plan', got %q", got[:min(20, len(got))])
	}
}
