package session

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewSummaryMarkdownGenerator(t *testing.T) {
	g := NewSummaryMarkdownGenerator()
	require.NotNil(t, g)
}

func TestSummaryMarkdownGenerator_Generate_NilMeta(t *testing.T) {
	g := NewSummaryMarkdownGenerator()
	result, err := g.Generate(nil, nil, nil)
	require.NoError(t, err)

	md := string(result)
	assert.Contains(t, md, "# Session Summary")
	assert.Contains(t, md, "_No metadata available_")
}

func TestSummaryMarkdownGenerator_Generate_WithMeta(t *testing.T) {
	g := NewSummaryMarkdownGenerator()
	meta := &StoreMeta{
		CreatedAt:    time.Date(2025, 1, 6, 10, 30, 0, 0, time.UTC),
		AgentType:    "claude-code",
		AgentVersion: "1.0.3",
		Model:        "claude-sonnet-4",
		Username:     "developer",
		AgentID:      "Oxa1b2",
	}

	result, err := g.Generate(meta, nil, nil)
	require.NoError(t, err)

	md := string(result)

	// check metadata section
	assert.Contains(t, md, "**Date:**")
	assert.Contains(t, md, "claude-code 1.0.3")
	assert.Contains(t, md, "claude-sonnet-4")
	assert.Contains(t, md, "developer")
	assert.Contains(t, md, "Oxa1b2")
}

func TestSummaryMarkdownGenerator_Generate_WithSummary(t *testing.T) {
	g := NewSummaryMarkdownGenerator()
	summary := &SummaryView{
		Text:        "This session implemented a new API endpoint for user authentication.",
		KeyActions:  []string{"Added auth middleware", "Created JWT token handler", "Wrote unit tests"},
		Outcome:     "success",
		TopicsFound: []string{"AWS RDS", "PostgreSQL", "OAuth2"},
	}

	result, err := g.Generate(nil, summary, nil)
	require.NoError(t, err)

	md := string(result)

	// check summary section
	assert.Contains(t, md, "## Summary")
	assert.Contains(t, md, "API endpoint for user authentication")

	// check key actions
	assert.Contains(t, md, "## Key Actions")
	assert.Contains(t, md, "- Added auth middleware")

	// check outcome
	assert.Contains(t, md, "**Outcome:** success")

	// check topics
	assert.Contains(t, md, "## Topics")
	assert.Contains(t, md, "`AWS RDS`")
	assert.Contains(t, md, "`PostgreSQL`")
}

func TestSummaryMarkdownGenerator_Generate_WithFileEdits(t *testing.T) {
	g := NewSummaryMarkdownGenerator()

	entries := []map[string]any{
		{
			"type": "tool",
			"data": map[string]any{
				"tool_name":  "write",
				"tool_input": "/path/to/new_file.go",
			},
		},
		{
			"type": "tool",
			"data": map[string]any{
				"tool_name":  "edit",
				"tool_input": "/path/to/existing.go",
			},
		},
	}

	result, err := g.Generate(nil, nil, entries)
	require.NoError(t, err)

	md := string(result)

	// check files modified section
	assert.Contains(t, md, "## Files Modified")
	assert.Contains(t, md, "| Path | Action |")
}

func TestSummaryMarkdownGenerator_ExtractFileModifications(t *testing.T) {
	g := NewSummaryMarkdownGenerator()

	tests := []struct {
		name     string
		entries  []map[string]any
		expected int // number of modifications
	}{
		{
			name:     "empty entries",
			entries:  nil,
			expected: 0,
		},
		{
			name: "file_edited event",
			entries: []map[string]any{
				{
					"type": "file_edited",
					"file": "/path/to/file.go",
				},
			},
			expected: 1,
		},
		{
			name: "tool write entry",
			entries: []map[string]any{
				{
					"type": "tool",
					"data": map[string]any{
						"tool_name":  "write",
						"tool_input": "/path/to/new.go",
					},
				},
			},
			expected: 1,
		},
		{
			name: "deduplicate same file",
			entries: []map[string]any{
				{
					"type": "tool",
					"data": map[string]any{
						"tool_name":  "write",
						"tool_input": "/path/to/file.go",
					},
				},
				{
					"type": "tool",
					"data": map[string]any{
						"tool_name":  "edit",
						"tool_input": "/path/to/file.go",
					},
				},
			},
			expected: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mods := g.extractFileModifications(tt.entries)
			assert.Len(t, mods, tt.expected)
		})
	}
}

func TestSummaryMarkdownGenerator_ParseCommandForFiles(t *testing.T) {
	g := NewSummaryMarkdownGenerator()

	tests := []struct {
		name     string
		cmd      string
		expected map[string]string
	}{
		{
			name:     "touch creates file",
			cmd:      "touch /path/to/new.txt",
			expected: map[string]string{"/path/to/new.txt": "Created"},
		},
		{
			name:     "rm deletes file",
			cmd:      "rm /path/to/old.txt",
			expected: map[string]string{"/path/to/old.txt": "Deleted"},
		},
		{
			name:     "rm with flags",
			cmd:      "rm -rf /path/to/dir",
			expected: map[string]string{"/path/to/dir": "Deleted"},
		},
		{
			name: "mv renames",
			cmd:  "mv /old/path.txt /new/path.txt",
			expected: map[string]string{
				"/old/path.txt": "Deleted",
				"/new/path.txt": "Created",
			},
		},
		{
			name:     "mkdir ignored",
			cmd:      "mkdir /path/to/dir",
			expected: map[string]string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			seen := make(map[string]string)
			g.parseCommandForFiles(tt.cmd, seen)

			assert.Len(t, seen, len(tt.expected))

			for path, action := range tt.expected {
				assert.Equal(t, action, seen[path])
			}
		})
	}
}

func TestSummaryExtractPath(t *testing.T) {
	tests := []struct {
		name     string
		summary  string
		expected string
	}{
		{
			name:     "edited prefix",
			summary:  "edited /path/to/file.go",
			expected: "/path/to/file.go",
		},
		{
			name:     "no prefix",
			summary:  "modified some content in /path/to/file.go",
			expected: "/path/to/file.go",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := summaryExtractPath(tt.summary)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestSummaryMarkdownGenerator_EscapePipeInPath(t *testing.T) {
	g := NewSummaryMarkdownGenerator()

	entries := []map[string]any{
		{
			"type": "file_edited",
			"file": "/path/with|pipe.go",
		},
	}

	result, err := g.Generate(nil, nil, entries)
	require.NoError(t, err)

	md := string(result)

	// pipe should be escaped
	assert.Contains(t, md, "with\\|pipe")
}
