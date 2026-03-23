package sessionsummary

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBuildSummaryPrompt(t *testing.T) {
	entries := []Entry{
		{Type: EntryTypeUser, Content: "hello"},
		{Type: EntryTypeAssistant, Content: "hi"},
	}

	t.Run("includes raw path and entry count", func(t *testing.T) {
		result := BuildSummaryPrompt(entries, "/tmp/raw.jsonl", "")
		assert.Contains(t, result, "/tmp/raw.jsonl")
		assert.Contains(t, result, "2 entries")
	})

	t.Run("includes push step when ledger dir provided", func(t *testing.T) {
		result := BuildSummaryPrompt(entries, "/tmp/raw.jsonl", "/ledger/sessions/abc")
		assert.Contains(t, result, "ox session push-summary")
		assert.Contains(t, result, "/ledger/sessions/abc")
	})

	t.Run("omits push step when ledger dir empty", func(t *testing.T) {
		result := BuildSummaryPrompt(entries, "/tmp/raw.jsonl", "")
		assert.NotContains(t, result, "ox session push-summary")
	})

	t.Run("empty entries", func(t *testing.T) {
		result := BuildSummaryPrompt(nil, "/tmp/raw.jsonl", "")
		assert.Contains(t, result, "0 entries")
	})

	t.Run("path with spaces", func(t *testing.T) {
		result := BuildSummaryPrompt(entries, "/tmp/my session/raw.jsonl", "")
		assert.Contains(t, result, "/tmp/my session/raw.jsonl")
	})

	t.Run("includes agent_summary guidelines", func(t *testing.T) {
		result := BuildSummaryPrompt(entries, "/tmp/raw.jsonl", "")
		assert.True(t, strings.Contains(result, "agent_summary"))
		assert.True(t, strings.Contains(result, "quality_score"))
	})
}
