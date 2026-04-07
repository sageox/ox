package session

import (
	"testing"
	"time"

	"github.com/sageox/ox/internal/session/adapters"
	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMapRoleToEntryType(t *testing.T) {
	tests := []struct {
		role     string
		expected EntryType
	}{
		{"user", EntryTypeUser},
		{"assistant", EntryTypeAssistant},
		{"system", EntryTypeSystem},
		{"tool", EntryTypeTool},
		{"unknown", EntryTypeSystem},
		{"", EntryTypeSystem},
	}

	for _, tt := range tests {
		t.Run(tt.role, func(t *testing.T) {
			result := MapRoleToEntryType(tt.role)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestConvertRawEntries(t *testing.T) {
	t.Run("empty input returns empty slice", func(t *testing.T) {
		result := ConvertRawEntries([]adapters.RawEntry{})
		require.NotNil(t, result)
		assert.Empty(t, result)
	})

	t.Run("preserves fields and maps roles", func(t *testing.T) {
		now := time.Now().Truncate(time.Second)
		rawEntries := []adapters.RawEntry{
			{
				Timestamp: now,
				Role:      "user",
				Content:   "hello",
			},
			{
				Timestamp: now.Add(time.Second),
				Role:      "assistant",
				Content:   "hi there",
			},
			{
				Timestamp: now.Add(2 * time.Second),
				Role:      "tool",
				Content:   "result output",
				ToolName:  "Bash",
				ToolInput: "ls -la",
			},
		}

		result := ConvertRawEntries(rawEntries)
		require.Len(t, result, 3)

		assert.Equal(t, EntryTypeUser, result[0].Type)
		assert.Equal(t, now, result[0].Timestamp)
		assert.Equal(t, "hello", result[0].Content)

		assert.Equal(t, EntryTypeAssistant, result[1].Type)
		assert.Equal(t, now.Add(time.Second), result[1].Timestamp)
		assert.Equal(t, "hi there", result[1].Content)

		assert.Equal(t, EntryTypeTool, result[2].Type)
		assert.Equal(t, now.Add(2*time.Second), result[2].Timestamp)
		assert.Equal(t, "result output", result[2].Content)
		assert.Equal(t, "Bash", result[2].ToolName)
		assert.Equal(t, "ls -la", result[2].ToolInput)
	})

	t.Run("preserves tool error fields", func(t *testing.T) {
		now := time.Now().Truncate(time.Second)
		rawEntries := []adapters.RawEntry{
			{
				Timestamp:  now,
				Role:       "tool",
				ToolName:   "Bash",
				ToolInput:  `{"command":"make build"}`,
				ToolOutput: "exit code 1: compilation failed",
				IsError:    true,
			},
			{
				Timestamp: now.Add(time.Second),
				Role:      "tool",
				ToolName:  "Read",
				ToolInput: "/path/to/file.go",
			},
		}

		result := ConvertRawEntries(rawEntries)
		require.Len(t, result, 2)

		assert.Equal(t, "Bash", result[0].ToolName)
		assert.Equal(t, "exit code 1: compilation failed", result[0].ToolOutput)
		assert.True(t, result[0].IsError)

		assert.Equal(t, "Read", result[1].ToolName)
		assert.Empty(t, result[1].ToolOutput)
		assert.False(t, result[1].IsError)
	})

	// Failure prevented: successful tool entries accidentally get ToolOutput set.
	t.Run("empty ToolOutput preserved as empty", func(t *testing.T) {
		rawEntries := []adapters.RawEntry{
			{
				Role:     "tool",
				ToolName: "Read",
			},
		}

		result := ConvertRawEntries(rawEntries)
		require.Len(t, result, 1)
		assert.Empty(t, result[0].ToolOutput)
	})
}

// --- E. Protocol entry → CapturedHistory conversion ---

// TestConvertProtocolEntriesToHistory_BasicConversion verifies that protocol
// RawEntry values are correctly mapped to HistoryEntry with auto-incrementing seq.
// Failure prevented: seq numbering gaps or wrong type mapping.
func TestConvertProtocolEntriesToHistory_BasicConversion(t *testing.T) {
	entries := []adapterprotocol.RawEntry{
		{Timestamp: "2026-04-07T10:00:00Z", Role: "user", Content: "hello"},
		{Timestamp: "2026-04-07T10:00:05Z", Role: "assistant", Content: "hi there"},
		{Timestamp: "2026-04-07T10:00:10Z", Role: "tool", ToolName: "Read", ToolInput: `{"file":"/src/main.go"}`},
		{Timestamp: "2026-04-07T10:00:11Z", Role: "tool", ToolOutput: "package main", CallID: "toolu_123"},
	}

	history := ConvertProtocolEntriesToHistory(entries, "Oxtest1", "claude-code")

	require.NotNil(t, history)
	require.NotNil(t, history.Meta)
	assert.Equal(t, "Oxtest1", history.Meta.AgentID)
	assert.Equal(t, "claude-code", history.Meta.AgentType)
	assert.Equal(t, HistorySourceAdapterImport, history.Meta.Source)
	assert.Equal(t, 4, history.Meta.MessageCount)

	require.Len(t, history.Entries, 4)

	// verify seq numbers are monotonically increasing
	for i, e := range history.Entries {
		assert.Equal(t, i+1, e.Seq, "seq mismatch at index %d", i)
	}

	// verify type mapping
	assert.Equal(t, "user", history.Entries[0].Type)
	assert.Equal(t, "hello", history.Entries[0].Content)

	assert.Equal(t, "assistant", history.Entries[1].Type)

	assert.Equal(t, "tool", history.Entries[2].Type)
	assert.Equal(t, "Read", history.Entries[2].ToolName)
	assert.Equal(t, `{"file":"/src/main.go"}`, history.Entries[2].ToolInput)

	assert.Equal(t, "tool", history.Entries[3].Type)
	assert.Equal(t, "package main", history.Entries[3].ToolOutput)

	// verify source is set on all entries
	for _, e := range history.Entries {
		assert.Equal(t, HistorySourceAdapterImport, e.Source)
	}
}

// TestConvertProtocolEntriesToHistory_TimeRange verifies that the computed
// time range spans all entries.
// Failure prevented: time range missing or wrong bounds.
func TestConvertProtocolEntriesToHistory_TimeRange(t *testing.T) {
	entries := []adapterprotocol.RawEntry{
		{Timestamp: "2026-04-07T10:00:00Z", Role: "user", Content: "first"},
		{Timestamp: "2026-04-07T11:30:00Z", Role: "assistant", Content: "last"},
	}

	history := ConvertProtocolEntriesToHistory(entries, "Oxtest2", "claude-code")

	require.NotNil(t, history.Meta.TimeRange)
	earliest, _ := time.Parse(time.RFC3339, "2026-04-07T10:00:00Z")
	latest, _ := time.Parse(time.RFC3339, "2026-04-07T11:30:00Z")
	assert.Equal(t, earliest, history.Meta.TimeRange.Earliest)
	assert.Equal(t, latest, history.Meta.TimeRange.Latest)
}

// TestConvertProtocolEntriesToHistory_EmptyInput verifies that empty input
// produces a valid but empty history.
// Failure prevented: nil pointer on empty input.
func TestConvertProtocolEntriesToHistory_EmptyInput(t *testing.T) {
	history := ConvertProtocolEntriesToHistory([]adapterprotocol.RawEntry{}, "Oxtest3", "claude-code")

	require.NotNil(t, history)
	require.NotNil(t, history.Meta)
	assert.Empty(t, history.Entries)
	assert.Equal(t, 0, history.Meta.MessageCount)
	assert.Nil(t, history.Meta.TimeRange)
}

// TestConvertProtocolEntriesToHistory_ToolErrorPreserved verifies that the
// IsError flag survives the conversion.
// Failure prevented: tool errors silently treated as successes.
func TestConvertProtocolEntriesToHistory_ToolErrorPreserved(t *testing.T) {
	entries := []adapterprotocol.RawEntry{
		{Timestamp: "2026-04-07T10:00:00Z", Role: "tool", ToolOutput: "exit code 1", IsError: true},
	}

	history := ConvertProtocolEntriesToHistory(entries, "Oxtest4", "claude-code")

	require.Len(t, history.Entries, 1)
	assert.True(t, history.Entries[0].IsError)
	assert.Equal(t, "exit code 1", history.Entries[0].ToolOutput)
}
