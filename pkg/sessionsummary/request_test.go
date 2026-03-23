package sessionsummary

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildSummarizeRequest(t *testing.T) {
	ts := time.Date(2024, 6, 15, 10, 30, 0, 0, time.UTC)

	tests := []struct {
		name           string
		entries        []Entry
		agentID        string
		agentType      string
		model          string
		wantEntryCount int
		checkFn        func(t *testing.T, req *SummarizeRequest)
	}{
		{
			name:           "empty entries",
			entries:        nil,
			agentID:        "agent-1",
			agentType:      "claude-code",
			model:          "opus",
			wantEntryCount: 0,
			checkFn: func(t *testing.T, req *SummarizeRequest) {
				assert.Equal(t, "agent-1", req.AgentID)
				assert.Equal(t, "claude-code", req.AgentType)
				assert.Equal(t, "opus", req.Model)
			},
		},
		{
			name: "filters read-only tools before building request",
			entries: []Entry{
				{Type: EntryTypeUser, Content: "Fix bug", Timestamp: ts},
				{Type: EntryTypeTool, ToolName: "Read", ToolOutput: "contents"},
				{Type: EntryTypeAssistant, Content: "Fixed"},
			},
			agentID:        "a1",
			agentType:      "claude-code",
			wantEntryCount: 2,
		},
		{
			name: "zero timestamp omitted",
			entries: []Entry{
				{Type: EntryTypeUser, Content: "hello"},
			},
			agentID:        "a1",
			agentType:      "claude-code",
			wantEntryCount: 1,
			checkFn: func(t *testing.T, req *SummarizeRequest) {
				assert.Empty(t, req.Entries[0].Timestamp, "zero time should produce empty timestamp")
			},
		},
		{
			name: "non-zero timestamp formatted as RFC3339",
			entries: []Entry{
				{Type: EntryTypeUser, Content: "hello", Timestamp: ts},
			},
			agentID:        "a1",
			agentType:      "claude-code",
			wantEntryCount: 1,
			checkFn: func(t *testing.T, req *SummarizeRequest) {
				assert.Equal(t, "2024-06-15T10:30:00Z", req.Entries[0].Timestamp)
			},
		},
		{
			name: "tool name and content carried through",
			entries: []Entry{
				{Type: EntryTypeTool, ToolName: "Write", Content: "new file", ToolInput: "path.go"},
			},
			agentID:        "a1",
			agentType:      "claude-code",
			wantEntryCount: 1,
			checkFn: func(t *testing.T, req *SummarizeRequest) {
				assert.Equal(t, "Write", req.Entries[0].ToolName)
				assert.Equal(t, "new file", req.Entries[0].Content)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := BuildSummarizeRequest(tt.entries, tt.agentID, tt.agentType, tt.model)
			require.NotNil(t, req)
			assert.Len(t, req.Entries, tt.wantEntryCount)
			if tt.checkFn != nil {
				tt.checkFn(t, req)
			}
		})
	}
}
