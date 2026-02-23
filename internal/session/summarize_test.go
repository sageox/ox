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
	// entries with no user content — should produce stats without topic
	entries := []Entry{
		{Type: EntryTypeAssistant, Content: "Hello"},
		{Type: EntryTypeTool, Content: "result", ToolName: "Bash"},
	}
	result := LocalSummary(entries)
	assert.Contains(t, result, "0 user messages")
	assert.Contains(t, result, "1 assistant responses")
	assert.Contains(t, result, "1 tool calls")
	assert.Contains(t, result, "Bash")
	// no topic hint prefix
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

func TestExtractTopicHint_Simple(t *testing.T) {
	assert.Equal(t, "Add a logout button", extractTopicHint("Add a logout button"))
}

func TestExtractTopicHint_SkipsMarkdownHeaders(t *testing.T) {
	msg := "# Plan\n\nImplement the auth system"
	assert.Equal(t, "Implement the auth system", extractTopicHint(msg))
}

func TestExtractTopicHint_FirstNonEmptyLine(t *testing.T) {
	msg := "\n\n  \nFix the bug in checkout\nMore details here"
	assert.Equal(t, "Fix the bug in checkout", extractTopicHint(msg))
}

func TestExtractTopicHint_TruncatesLongMessages(t *testing.T) {
	long := strings.Repeat("word ", 50) // 250 chars
	result := extractTopicHint(long)
	assert.True(t, len([]rune(result)) <= localSummaryTopicMaxLen+1, "should be truncated (allow 1 for ellipsis)")
	assert.True(t, strings.HasSuffix(result, "\u2026"), "should end with ellipsis")
}

func TestExtractTopicHint_Empty(t *testing.T) {
	assert.Equal(t, "", extractTopicHint(""))
	assert.Equal(t, "", extractTopicHint("   "))
	assert.Equal(t, "", extractTopicHint("# Header Only\n"))
}

func TestLocalSummary_SkipsSkillInvocation(t *testing.T) {
	entries := []Entry{
		{Type: EntryTypeUser, Content: "/ox-session-start"},
		{Type: EntryTypeUser, Content: "Fix the authentication bug in the login flow"},
		{Type: EntryTypeAssistant, Content: "I'll fix that."},
	}
	result := LocalSummary(entries)
	assert.True(t, strings.HasPrefix(result, "Fix the authentication bug"), "should skip /ox-session-start and use second user message")
	assert.NotContains(t, result, "/ox-session-start")
}

func TestLocalSummary_AllSkillInvocations(t *testing.T) {
	// when all user messages are skill invocations, no topic hint
	entries := []Entry{
		{Type: EntryTypeUser, Content: "/ox-session-start"},
		{Type: EntryTypeUser, Content: "/commit"},
		{Type: EntryTypeAssistant, Content: "Done."},
	}
	result := LocalSummary(entries)
	assert.False(t, strings.Contains(result, "/ox-session-start"))
	assert.False(t, strings.Contains(result, "/commit"))
	assert.Contains(t, result, "2 user messages")
}

func TestIsSkillInvocation(t *testing.T) {
	assert.True(t, isSkillInvocation("/ox-session-start"))
	assert.True(t, isSkillInvocation("/commit"))
	assert.True(t, isSkillInvocation("/ox-session-stop"))
	assert.False(t, isSkillInvocation("Fix the bug"))
	assert.False(t, isSkillInvocation("Add a /path to the config"))
	assert.False(t, isSkillInvocation(""))
}
