package sessionsummary

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFilterForSummarization(t *testing.T) {
	tests := []struct {
		name      string
		entries   []Entry
		wantCount int
		wantTypes []string   // expected types of remaining entries
		wantTools []string   // expected tool names of remaining tool entries
	}{
		{
			name:      "empty entries",
			entries:   nil,
			wantCount: 0,
		},
		{
			name: "keeps user and assistant messages",
			entries: []Entry{
				{Type: EntryTypeUser, Content: "Fix the bug"},
				{Type: EntryTypeAssistant, Content: "I'll fix it"},
			},
			wantCount: 2,
			wantTypes: []string{EntryTypeUser, EntryTypeAssistant},
		},
		{
			name: "filters successful read-only tools",
			entries: []Entry{
				{Type: EntryTypeUser, Content: "Fix the bug"},
				{Type: EntryTypeTool, ToolName: "Read", ToolInput: "/path/to/file.go", ToolOutput: "file contents here"},
				{Type: EntryTypeTool, ToolName: "Glob", ToolInput: "**/*.go", ToolOutput: "main.go\nutil.go"},
				{Type: EntryTypeTool, ToolName: "Grep", ToolInput: "pattern", ToolOutput: "match found"},
				{Type: EntryTypeAssistant, Content: "Found the issue"},
			},
			wantCount: 2,
			wantTypes: []string{EntryTypeUser, EntryTypeAssistant},
		},
		{
			name: "keeps write and edit tools",
			entries: []Entry{
				{Type: EntryTypeTool, ToolName: "Write", ToolInput: "/path/to/file.go", Content: "new content"},
				{Type: EntryTypeTool, ToolName: "Edit", ToolInput: "/path/to/file.go", Content: "edited"},
			},
			wantCount: 2,
			wantTools: []string{"Write", "Edit"},
		},
		{
			name: "keeps failed read tools",
			entries: []Entry{
				{Type: EntryTypeTool, ToolName: "Read", ToolInput: "/nonexistent", ToolOutput: "Error: file not found"},
				{Type: EntryTypeTool, ToolName: "Grep", ToolInput: "pattern", ToolOutput: "fatal: not a git repo"},
			},
			wantCount: 2,
			wantTools: []string{"Read", "Grep"},
		},
		{
			name: "filters noise bash commands",
			entries: []Entry{
				{Type: EntryTypeTool, ToolName: "Bash", ToolInput: "ls -la", ToolOutput: "file1 file2"},
				{Type: EntryTypeTool, ToolName: "Bash", ToolInput: "pwd", ToolOutput: "/home/user"},
				{Type: EntryTypeTool, ToolName: "Bash", ToolInput: "cat README.md", ToolOutput: "readme content"},
			},
			wantCount: 0,
		},
		{
			name: "keeps meaningful bash commands",
			entries: []Entry{
				{Type: EntryTypeTool, ToolName: "Bash", ToolInput: "make test", ToolOutput: "PASS"},
				{Type: EntryTypeTool, ToolName: "Bash", ToolInput: "git commit -m 'fix'", ToolOutput: "committed"},
				{Type: EntryTypeTool, ToolName: "Bash", ToolInput: "npm install", ToolOutput: "added 5 packages"},
			},
			wantCount: 3,
			wantTools: []string{"Bash", "Bash", "Bash"},
		},
		{
			name: "keeps system entries",
			entries: []Entry{
				{Type: EntryTypeSystem, Content: "Session started"},
			},
			wantCount: 1,
			wantTypes: []string{EntryTypeSystem},
		},
		{
			name: "realistic session with mixed entries",
			entries: []Entry{
				{Type: EntryTypeUser, Content: "Add authentication to the API"},
				{Type: EntryTypeTool, ToolName: "Read", ToolInput: "main.go", ToolOutput: "package main"},
				{Type: EntryTypeTool, ToolName: "Glob", ToolInput: "**/*.go", ToolOutput: "main.go"},
				{Type: EntryTypeTool, ToolName: "Grep", ToolInput: "auth", ToolOutput: "no matches"},
				{Type: EntryTypeTool, ToolName: "Bash", ToolInput: "ls src/", ToolOutput: "api/ models/"},
				{Type: EntryTypeAssistant, Content: "I'll add JWT auth middleware"},
				{Type: EntryTypeTool, ToolName: "Write", ToolInput: "auth.go", Content: "package auth"},
				{Type: EntryTypeTool, ToolName: "Edit", ToolInput: "main.go", Content: "import auth"},
				{Type: EntryTypeTool, ToolName: "Bash", ToolInput: "go test ./...", ToolOutput: "PASS"},
				{Type: EntryTypeAssistant, Content: "Authentication added and tests pass"},
			},
			wantCount: 6, // user + 2 assistant + write + edit + go test
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := FilterForSummarization(tt.entries)
			assert.Len(t, result, tt.wantCount)

			if tt.wantTypes != nil {
				for i, wantType := range tt.wantTypes {
					if i < len(result) {
						assert.Equal(t, wantType, result[i].Type, "entry %d type", i)
					}
				}
			}

			if tt.wantTools != nil {
				var gotTools []string
				for _, e := range result {
					if e.Type == EntryTypeTool {
						gotTools = append(gotTools, e.ToolName)
					}
				}
				assert.Equal(t, tt.wantTools, gotTools)
			}
		})
	}
}

func TestFilterForSummarization_PreservesOrder(t *testing.T) {
	entries := []Entry{
		{Type: EntryTypeUser, Content: "first"},
		{Type: EntryTypeTool, ToolName: "Read", ToolOutput: "contents"},
		{Type: EntryTypeAssistant, Content: "second"},
		{Type: EntryTypeTool, ToolName: "Write", Content: "new file"},
		{Type: EntryTypeUser, Content: "third"},
	}
	result := FilterForSummarization(entries)
	assert.Len(t, result, 4)
	assert.Equal(t, "first", result[0].Content)
	assert.Equal(t, "second", result[1].Content)
	assert.Equal(t, "Write", result[2].ToolName)
	assert.Equal(t, "third", result[3].Content)
}

func TestIsNoiseCommand(t *testing.T) {
	tests := []struct {
		cmd  string
		want bool
	}{
		{"ls -la", true},
		{"pwd", true},
		{"cat README.md", true},
		{"head -n 10 file.go", true},
		{"echo hello", true},
		{"make test", false},
		{"git commit -m 'fix'", false},
		{"npm install", false},
		{"go test ./...", false},
	}

	for _, tt := range tests {
		t.Run(tt.cmd, func(t *testing.T) {
			assert.Equal(t, tt.want, IsNoiseCommand(tt.cmd))
		})
	}
}
