package session

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLocalSummary_Empty(t *testing.T) {
	assert.Equal(t, "Empty session", LocalSummary(nil))
	assert.Equal(t, "Empty session", LocalSummary([]Entry{}))
}

func TestLocalSummary_StatsOnly(t *testing.T) {
	entries := []Entry{
		{Type: EntryTypeAssistant, Content: "Hello"},
		{Type: EntryTypeTool, Content: "result", ToolName: "Bash"},
	}
	result := LocalSummary(entries)
	assert.Contains(t, result, "0 user messages")
	assert.Contains(t, result, "1 assistant responses")
	assert.Contains(t, result, "1 tool calls")
	assert.Contains(t, result, "Bash")
	assert.False(t, strings.Contains(result, "\n\n"), "should not have topic separator without user messages")
}

func TestLocalSummary_WithTopicHint(t *testing.T) {
	entries := []Entry{
		{Type: EntryTypeUser, Content: "Add a logout button to the navbar"},
		{Type: EntryTypeAssistant, Content: "Sure, I'll add that."},
		{Type: EntryTypeTool, Content: "ok", ToolName: "Read"},
	}
	result := LocalSummary(entries)
	assert.True(t, strings.HasPrefix(result, "Add a logout button to the navbar"))
	assert.Contains(t, result, "\n\n")
	assert.Contains(t, result, "1 user messages")
}

func TestLocalSummary_SkipsEmptyUserMessages(t *testing.T) {
	entries := []Entry{
		{Type: EntryTypeUser, Content: "   "},
		{Type: EntryTypeUser, Content: "Fix the login bug"},
	}
	result := LocalSummary(entries)
	assert.True(t, strings.HasPrefix(result, "Fix the login bug"))
}

func TestLocalSummary_ToolCountAndNames(t *testing.T) {
	entries := []Entry{
		{Type: EntryTypeUser, Content: "deploy"},
		{Type: EntryTypeTool, ToolName: "Bash"},
		{Type: EntryTypeTool, ToolName: "Read"},
		{Type: EntryTypeTool, ToolName: "Write"},
		{Type: EntryTypeTool, ToolName: "Glob"},
		{Type: EntryTypeTool, ToolName: "Grep"},
		{Type: EntryTypeTool, ToolName: "Edit"},
	}
	result := LocalSummary(entries)
	assert.Contains(t, result, "6 tool calls")
	assert.Contains(t, result, "and 1 more")
}

func TestLocalSummary_SkillInvocations(t *testing.T) {
	tests := []struct {
		name           string
		entries        []Entry
		wantPrefix     string
		wantContains   []string
		wantNotContain []string
	}{
		{
			name: "skips skill invocation uses second user message",
			entries: []Entry{
				{Type: EntryTypeUser, Content: "/ox-session-start"},
				{Type: EntryTypeUser, Content: "Fix the authentication bug in the login flow"},
				{Type: EntryTypeAssistant, Content: "I'll fix that."},
			},
			wantPrefix:     "Fix the authentication bug",
			wantNotContain: []string{"/ox-session-start"},
		},
		{
			name: "all skill invocations produces stats only",
			entries: []Entry{
				{Type: EntryTypeUser, Content: "/ox-session-start"},
				{Type: EntryTypeUser, Content: "/commit"},
				{Type: EntryTypeAssistant, Content: "Done."},
			},
			wantContains:   []string{"2 user messages"},
			wantNotContain: []string{"/ox-session-start", "/commit"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := LocalSummary(tt.entries)
			if tt.wantPrefix != "" {
				assert.True(t, strings.HasPrefix(result, tt.wantPrefix), "got: %s", result)
			}
			for _, s := range tt.wantContains {
				assert.Contains(t, result, s)
			}
			for _, s := range tt.wantNotContain {
				assert.NotContains(t, result, s)
			}
		})
	}
}

func TestFilterForSummarization(t *testing.T) {
	tests := []struct {
		name      string
		entries   []Entry
		wantCount int
		wantTypes []EntryType
		wantTools []string
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
			wantTypes: []EntryType{EntryTypeUser, EntryTypeAssistant},
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
			wantTypes: []EntryType{EntryTypeUser, EntryTypeAssistant},
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
			wantTypes: []EntryType{EntryTypeSystem},
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
			wantCount: 6,
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

func TestFilterForSummarization_CoworkerFieldsDropped(t *testing.T) {
	// CoworkerName/CoworkerModel exist on SessionEntry but not on pkg Entry.
	// The round-trip through entriesToPkg/pkgToEntries intentionally drops them
	// since the LLM summarizer doesn't need coworker attribution.
	entries := []Entry{
		{
			Type:          EntryTypeSystem,
			Content:       "Loaded coworker: code-reviewer",
			CoworkerName:  "code-reviewer",
			CoworkerModel: "sonnet",
		},
		{
			Type:    EntryTypeUser,
			Content: "Review this PR",
		},
	}

	result := FilterForSummarization(entries)
	assert.Len(t, result, 2)
	assert.Empty(t, result[0].CoworkerName, "CoworkerName intentionally dropped in summarization round-trip")
	assert.Empty(t, result[0].CoworkerModel, "CoworkerModel intentionally dropped in summarization round-trip")
}
