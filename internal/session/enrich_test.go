package session

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEnrichSummary_PopulatesFilesChanged(t *testing.T) {
	stored := &StoredSession{
		Meta: &StoreMeta{AgentType: "claude-code"},
		Entries: []map[string]any{
			{
				"type":      "user",
				"content":   "Fix the bug",
				"timestamp": time.Now().Format(time.RFC3339),
			},
			{
				"type":        "tool",
				"tool_name":   "Edit",
				"tool_input":  `{"file_path":"/home/user/project/main.go","old_string":"foo","new_string":"bar"}`,
				"tool_output": "--- a/main.go\n+++ b/main.go\n-foo\n+bar\n",
				"timestamp":   time.Now().Format(time.RFC3339),
			},
			{
				"type":        "tool",
				"tool_name":   "Write",
				"tool_input":  `{"file_path":"/home/user/project/new_file.go","content":"package main"}`,
				"tool_output": "+package main\n",
				"timestamp":   time.Now().Format(time.RFC3339),
			},
		},
	}

	summary := &SummarizeResponse{
		Title:   "Fix bug",
		Summary: "Fixed a bug",
	}

	assert.Empty(t, summary.FilesChanged)

	EnrichSummary(stored, summary)

	assert.NotEmpty(t, summary.FilesChanged, "EnrichSummary must populate FilesChanged")
	assert.Len(t, summary.FilesChanged, 2, "should have 2 files changed")

	paths := make([]string, len(summary.FilesChanged))
	for i, fc := range summary.FilesChanged {
		paths[i] = fc.Path
	}
	assert.Contains(t, paths, "project/main.go")
	assert.Contains(t, paths, "project/new_file.go")

	assert.NotEmpty(t, summary.Chapters, "EnrichSummary must populate Chapters")
}

func TestEnrichSummary_NilInputs(t *testing.T) {
	// should not panic on nil inputs
	EnrichSummary(nil, nil)
	EnrichSummary(&StoredSession{}, nil)
	EnrichSummary(nil, &SummarizeResponse{})
}

func TestExtractFilePathFromInput(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"json with file_path", `{"file_path":"/home/user/project/main.go"}`, "/home/user/project/main.go"},
		{"json with path", `{"path":"/home/user/project/config.yaml"}`, "/home/user/project/config.yaml"},
		{"empty input", "", ""},
		{"invalid json", "not json", ""},
		{"json without path fields", `{"command":"ls"}`, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractFilePathFromInput(tt.input)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestCountDiffLines(t *testing.T) {
	tests := []struct {
		name    string
		output  string
		added   int
		removed int
	}{
		{"empty", "", 0, 0},
		{"unified diff", "--- a/main.go\n+++ b/main.go\n-old line\n+new line\n context\n", 1, 1},
		{"additions only", "+line1\n+line2\n+line3\n", 3, 0},
		{"removals only", "-line1\n-line2\n", 0, 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, r := CountDiffLines(tt.output)
			assert.Equal(t, tt.added, a)
			assert.Equal(t, tt.removed, r)
		})
	}
}

func TestEnrichShortenPath(t *testing.T) {
	assert.Equal(t, "project/main.go", shortenPath("/home/user/project/main.go"))
	assert.Equal(t, "project/main.go", shortenPath("/Users/dev/project/main.go"))
	assert.Equal(t, "relative/path.go", shortenPath("relative/path.go"))
}

func TestEnrichReclassifyByContent(t *testing.T) {
	tests := []struct {
		name    string
		msgType string
		content string
		want    string
	}{
		{"skill expansion", "user", "<!-- ox-hash: abc123 -->\nSession start", "system"},
		{"system reminder", "user", "<system-reminder>context</system-reminder>", "system"},
		{"normal user", "user", "Fix the login bug", "user"},
		{"assistant stays", "assistant", "<!-- ox-hash: fake -->", "assistant"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := enrichReclassifyByContent(tt.msgType, tt.content)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestGroupIntoChapters_Basic(t *testing.T) {
	messages := []EnrichMessage{
		{ID: 1, Type: "user", Content: "Fix the bug"},
		{ID: 2, Type: "tool", Content: "", ToolCall: &EnrichToolCall{Name: "Read", Input: `{"file_path":"main.go"}`}},
		{ID: 3, Type: "assistant", Content: "Found the issue"},
		{ID: 4, Type: "user", Content: "Now fix it"},
		{ID: 5, Type: "tool", Content: "", ToolCall: &EnrichToolCall{Name: "Edit", Input: `{"file_path":"main.go"}`}},
		{ID: 6, Type: "assistant", Content: "Done"},
	}

	chapters := groupIntoChapters(messages, nil)
	require.Len(t, chapters, 2, "should have 2 chapters")
	assert.Equal(t, 1, chapters[0].ID)
	assert.Equal(t, 2, chapters[1].ID)
}

func TestGroupIntoChapters_WithTitles(t *testing.T) {
	messages := []EnrichMessage{
		{ID: 1, Type: "user", Content: "Fix the bug"},
		{ID: 2, Type: "assistant", Content: "On it"},
	}

	titles := []string{"Bug Investigation"}
	chapters := groupIntoChapters(messages, titles)
	require.Len(t, chapters, 1)
	assert.Equal(t, "Bug Investigation", chapters[0].Title)
}

func TestStripANSI(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"hello world", "hello world"},
		{"\x1b[38;2;224;165;106m\u2726\x1b[m Use ox", "\u2726 Use ox"},
		{"\x1b[1mBold\x1b[0m Normal", "Bold Normal"},
	}

	for _, tt := range tests {
		assert.Equal(t, tt.want, stripANSI(tt.input))
	}
}
