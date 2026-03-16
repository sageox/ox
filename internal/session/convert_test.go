package session

import (
	"testing"
	"time"

	"github.com/sageox/ox/internal/session/adapters"
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
}
