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
		wantTypes []string // expected types of remaining entries
		wantTools []string // expected tool names of remaining tool entries
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
		// leading whitespace handled by TrimSpace
		{"  ls -la", true},
		{"\tpwd", true},
		// similar prefixes that should NOT match
		{"lsof -i :8080", false},
		{"catalog build", false},
		{"headless-chrome run", false},
		// empty input
		{"", false},
		{"   ", false},
	}

	for _, tt := range tests {
		t.Run(tt.cmd, func(t *testing.T) {
			assert.Equal(t, tt.want, IsNoiseCommand(tt.cmd))
		})
	}
}

func TestHasToolError(t *testing.T) {
	tests := []struct {
		name  string
		entry Entry
		want  bool
	}{
		{
			name:  "error in ToolOutput",
			entry: Entry{Type: EntryTypeTool, ToolOutput: "Error: file not found"},
			want:  true,
		},
		{
			name:  "error in Content when ToolOutput empty",
			entry: Entry{Type: EntryTypeTool, Content: "fatal: bad config"},
			want:  true,
		},
		{
			name:  "ToolOutput takes precedence over Content",
			entry: Entry{Type: EntryTypeTool, ToolOutput: "success", Content: "Error: something"},
			want:  false,
		},
		{
			name:  "both empty",
			entry: Entry{Type: EntryTypeTool},
			want:  false,
		},
		{
			name:  "clean ToolOutput",
			entry: Entry{Type: EntryTypeTool, ToolOutput: "file contents here"},
			want:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, hasToolError(tt.entry))
		})
	}
}

func TestDetectError(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{"error prefix", "Error: file not found", true},
		{"fatal prefix", "fatal: not a git repository", true},
		{"panic prefix", "panic: runtime error", true},
		{"exception keyword", "NullPointerException at line 42", true},
		{"failed keyword", "Build failed with 3 errors", true},
		{"exit code non-zero", "Process exited with exit code 1", true},
		{"exit code 127", "exit code 127", true},
		{"empty string", "", false},
		{"normal output", "PASS\nok  github.com/example 0.5s", false},
		{"exit code 0", "exit code 0", false},
		{"exit code 0 with surrounding text", "Command completed, exit code 0, output saved", false},
		// "exit code" without a number triggers error (no "exit code 0" match)
		{"exit code bare", "exit code", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, detectError(tt.content), "content: %q", tt.content)
		})
	}
}

func TestFilterForSummarization_CaseSensitiveToolNames(t *testing.T) {
	// readOnlyTools has "read" and "Read" but not "READ"
	entries := []Entry{
		{Type: EntryTypeTool, ToolName: "READ", ToolOutput: "contents"},
		{Type: EntryTypeTool, ToolName: "read", ToolOutput: "contents"},
		{Type: EntryTypeTool, ToolName: "Read", ToolOutput: "contents"},
	}
	result := FilterForSummarization(entries)
	assert.Len(t, result, 1)
	assert.Equal(t, "READ", result[0].ToolName)
}
