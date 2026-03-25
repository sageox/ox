package sessionsummary

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseSummaryJSON_RawJSON(t *testing.T) {
	input := `{"title":"Fix auth bug","summary":"Fixed token refresh","key_actions":["patched refresh"],"outcome":"success","quality_score":0.8}`
	resp, err := ParseSummaryJSON(input)
	require.NoError(t, err)
	assert.Equal(t, "Fix auth bug", resp.Title)
	assert.Equal(t, 0.8, resp.QualityScore)
}

func TestParseSummaryJSON_JSONFence(t *testing.T) {
	input := "Here is the summary:\n\n```json\n{\"title\":\"Add caching\",\"summary\":\"Added Redis cache\",\"key_actions\":[\"added cache layer\"],\"outcome\":\"success\",\"quality_score\":0.7}\n```\n\nDone."
	resp, err := ParseSummaryJSON(input)
	require.NoError(t, err)
	assert.Equal(t, "Add caching", resp.Title)
}

func TestParseSummaryJSON_GenericFence(t *testing.T) {
	input := "Summary:\n\n```\n{\"title\":\"Refactor DB\",\"summary\":\"Moved to connection pool\",\"key_actions\":[\"pooling\"],\"outcome\":\"success\",\"quality_score\":0.6}\n```"
	resp, err := ParseSummaryJSON(input)
	require.NoError(t, err)
	assert.Equal(t, "Refactor DB", resp.Title)
}

func TestParseSummaryJSON_NoTitle(t *testing.T) {
	input := `{"summary":"no title here","quality_score":0.5}`
	_, err := ParseSummaryJSON(input)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no valid summary JSON")
}

func TestParseSummaryJSON_Empty(t *testing.T) {
	_, err := ParseSummaryJSON("")
	assert.Error(t, err)
}

func TestParseSummaryJSON_InvalidJSON(t *testing.T) {
	_, err := ParseSummaryJSON("this is not json at all")
	assert.Error(t, err)
}

func TestEntriesFromRaw(t *testing.T) {
	ts := "2026-03-24T10:00:00.000Z"
	raw := []map[string]any{
		{"type": "user", "content": "Fix the bug", "timestamp": ts},
		{"type": "assistant", "content": "Looking at it.", "timestamp": ts},
		{"type": "tool", "tool_name": "Read", "tool_input": "main.go", "tool_output": "package main"},
	}

	entries := EntriesFromRaw(raw)
	require.Len(t, entries, 3)

	assert.Equal(t, "user", entries[0].Type)
	assert.Equal(t, "Fix the bug", entries[0].Content)
	assert.False(t, entries[0].Timestamp.IsZero())

	assert.Equal(t, "assistant", entries[1].Type)
	assert.Equal(t, "Looking at it.", entries[1].Content)

	assert.Equal(t, "tool", entries[2].Type)
	assert.Equal(t, "Read", entries[2].ToolName)
	assert.Equal(t, "main.go", entries[2].ToolInput)
	assert.Equal(t, "package main", entries[2].ToolOutput)
}

func TestEntriesFromRaw_Empty(t *testing.T) {
	entries := EntriesFromRaw(nil)
	assert.Empty(t, entries)
}

func TestEntriesFromRaw_TimestampFormats(t *testing.T) {
	raw := []map[string]any{
		{"type": "user", "content": "nano", "timestamp": "2026-03-24T10:00:00.123456789Z"},
		{"type": "user", "content": "rfc3339", "timestamp": "2026-03-24T10:00:00Z"},
		{"type": "user", "content": "invalid", "timestamp": "not-a-date"},
		{"type": "user", "content": "missing"},
	}

	entries := EntriesFromRaw(raw)
	require.Len(t, entries, 4)

	assert.False(t, entries[0].Timestamp.IsZero(), "nano timestamp should parse")
	assert.False(t, entries[1].Timestamp.IsZero(), "rfc3339 timestamp should parse")
	assert.True(t, entries[2].Timestamp.Equal(time.Time{}), "invalid timestamp should be zero")
	assert.True(t, entries[3].Timestamp.Equal(time.Time{}), "missing timestamp should be zero")
}
