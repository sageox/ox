package session

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMdFormatToolCompact_EmptyName(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "Tool", mdFormatToolCompact("", ""))
}

func TestMdFormatToolCompact_Bash(t *testing.T) {
	t.Parallel()
	got := mdFormatToolCompact("bash", `{"command":"ls -la"}`)
	assert.Contains(t, got, "Bash")
	assert.Contains(t, got, "ls -la")
}

func TestMdFormatToolCompact_BashNoCommand(t *testing.T) {
	t.Parallel()
	got := mdFormatToolCompact("Bash", `{"other":"field"}`)
	assert.Contains(t, got, "Bash")
}

func TestMdFormatToolCompact_Read(t *testing.T) {
	t.Parallel()
	got := mdFormatToolCompact("read", `{"file_path":"/usr/src/app/main.go"}`)
	assert.Contains(t, got, "Read")
	assert.Contains(t, got, "main.go")
}

func TestMdFormatToolCompact_Edit(t *testing.T) {
	t.Parallel()
	got := mdFormatToolCompact("edit", `{"file_path":"/usr/src/app/config.go"}`)
	assert.Contains(t, got, "edit")
}

func TestMdFormatToolCompact_Write(t *testing.T) {
	t.Parallel()
	got := mdFormatToolCompact("write", `{"file_path":"/tmp/out.txt"}`)
	assert.Contains(t, strings.ToLower(got), "write")
}

func TestMdFormatToolCompact_Grep(t *testing.T) {
	t.Parallel()
	got := mdFormatToolCompact("grep", `{"pattern":"TODO","path":"/src"}`)
	assert.Contains(t, got, "grep")
	assert.Contains(t, got, "TODO")
}

func TestMdFormatToolCompact_Glob(t *testing.T) {
	t.Parallel()
	got := mdFormatToolCompact("glob", `{"pattern":"**/*.go"}`)
	assert.Contains(t, strings.ToLower(got), "glob")
	assert.Contains(t, got, "*.go")
}

func TestMdFormatToolCompact_UnknownTool(t *testing.T) {
	t.Parallel()
	got := mdFormatToolCompact("custom_tool", `{"key":"value"}`)
	assert.Contains(t, got, "custom_tool")
}

func TestMdFormatToolCompact_InvalidJSON(t *testing.T) {
	t.Parallel()
	got := mdFormatToolCompact("bash", "not json")
	assert.Contains(t, got, "bash")
}

func TestMdFormatToolCompact_CaseInsensitive(t *testing.T) {
	t.Parallel()
	got := mdFormatToolCompact("BASH", `{"command":"echo hi"}`)
	assert.Contains(t, got, "Bash")
}

func TestMdFormatFieldValue(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"empty", "", ""},
		{"short", "hello", "hello"},
		{"multiline", "line1\nline2", "line1\nline2"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := mdFormatFieldValue(tt.input)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestIsAhaMoment_NilSummary(t *testing.T) {
	t.Parallel()
	g := &MarkdownGenerator{summary: nil}
	idx, ok := g.isAhaMoment(1)
	assert.False(t, ok)
	assert.Equal(t, 0, idx)
}

func TestIsAhaMoment_Found(t *testing.T) {
	t.Parallel()
	g := &MarkdownGenerator{
		summary: &SummarizeResponse{
			AhaMoments: []AhaMoment{
				{Seq: 3},
				{Seq: 7},
			},
		},
	}
	idx, ok := g.isAhaMoment(7)
	assert.True(t, ok)
	assert.Equal(t, 2, idx) // 1-indexed
}

func TestIsAhaMoment_NotFound(t *testing.T) {
	t.Parallel()
	g := &MarkdownGenerator{
		summary: &SummarizeResponse{
			AhaMoments: []AhaMoment{{Seq: 3}},
		},
	}
	_, ok := g.isAhaMoment(99)
	assert.False(t, ok)
}

func TestMdShortenPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"short path", "main.go", "main.go"},
		{"reasonable path", "cmd/ox/main.go", "cmd/ox/main.go"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := mdShortenPath(tt.input)
			assert.Equal(t, tt.want, got)
		})
	}
}
