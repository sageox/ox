package sessionsummary

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBuildInlineSummaryPrompt_EmbedsContent verifies that the inline prompt
// includes actual session content in the prompt text, not a file-read
// instruction.
//
// Failure prevented: the daemon's cold-start subprocess cannot reliably read
// files via tool calls. 96% of sessions failed when the prompt said "Read
// the file at <path>" because the subprocess (configurable coding agent) had
// unreliable tool access. Embedding content directly is the fix.
func TestBuildInlineSummaryPrompt_EmbedsContent(t *testing.T) {
	entries := []Entry{
		{Type: EntryTypeUser, Content: "Fix the flaky integration test in daemon", Timestamp: time.Now()},
		{Type: EntryTypeAssistant, Content: "I'll investigate the race condition in the session watcher.", Timestamp: time.Now()},
		{Type: "tool_mark", Content: ""},
		{Type: EntryTypeAssistant, Content: "Found it — the watcher was not waiting for the goroutine to drain.", Timestamp: time.Now()},
	}

	prompt := BuildInlineSummaryPrompt(entries)

	assert.Contains(t, prompt, "Fix the flaky integration test")
	assert.Contains(t, prompt, "race condition in the session watcher")
	assert.Contains(t, prompt, "Output ONLY the JSON")
	assert.NotContains(t, prompt, "Read the session recording at")
	assert.Contains(t, prompt, "tool activity")
}

// TestBuildInlineSummaryPrompt_LargeSession_FiltersToConversation verifies
// that sessions exceeding maxInlineEntries filter down to user+assistant only.
//
// Failure prevented: prompt blowup on large sessions that would exceed the
// model's context window.
func TestBuildInlineSummaryPrompt_LargeSession_FiltersToConversation(t *testing.T) {
	entries := make([]Entry, 0, 1000)
	for i := 0; i < 500; i++ {
		entries = append(entries, Entry{Type: EntryTypeUser, Content: "question", Timestamp: time.Now()})
		entries = append(entries, Entry{Type: "tool_mark", Content: ""})
	}
	require.Greater(t, len(entries), maxInlineEntries)

	prompt := BuildInlineSummaryPrompt(entries)

	// tool_mark entries should be filtered out for large sessions —
	// the rendered marker is "*[tool activity]*"; check the bracketed
	// form so the assertion does not collide with the prompt's own
	// disclosure note ("tool activity is dropped...") which is
	// supposed to be present.
	assert.NotContains(t, prompt, "*[tool activity]*")
	// user entries should remain
	assert.Contains(t, prompt, "question")
}

// TestBuildInlineSummaryPrompt_TruncatesLongContent verifies that individual
// entries with extremely long content are truncated to avoid prompt blowup.
func TestBuildInlineSummaryPrompt_TruncatesLongContent(t *testing.T) {
	longContent := strings.Repeat("x", 5000)
	entries := []Entry{
		{Type: EntryTypeUser, Content: longContent, Timestamp: time.Now()},
	}

	prompt := BuildInlineSummaryPrompt(entries)

	// user content truncated to 2000 chars + truncation marker
	assert.Contains(t, prompt, "[...truncated]")
	// the 5000-char content should not appear in full
	assert.NotContains(t, prompt, longContent)
}

// TestBuildInlineSummaryPrompt_IncludesGuidelines verifies the prompt contains
// the full summary guidelines (output format, quality categories, etc).
func TestBuildInlineSummaryPrompt_IncludesGuidelines(t *testing.T) {
	entries := []Entry{
		{Type: EntryTypeUser, Content: "hello", Timestamp: time.Now()},
	}

	prompt := BuildInlineSummaryPrompt(entries)

	assert.Contains(t, prompt, "quality_category")
	assert.Contains(t, prompt, "key_actions")
	assert.Contains(t, prompt, "aha_moments")
}
