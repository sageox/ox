package agentcli

import (
	"context"
	"strings"
	"testing"
)

func TestClaudeName(t *testing.T) {
	c := &Claude{}
	if c.Name() != "claude" {
		t.Errorf("expected name 'claude', got %q", c.Name())
	}
}

func TestDetectNoBackend(t *testing.T) {
	// save and clear PATH to ensure no backends are found
	t.Setenv("PATH", "")
	_, err := Detect()
	if err == nil {
		t.Error("expected error when no backends available")
	}
	if !strings.Contains(err.Error(), "no supported AI coworker CLI found") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestDailyPromptFormat(t *testing.T) {
	obs := []string{
		"Decided to use PostgreSQL for analytics",
		"Auth module needs refactoring",
	}
	prompt := DailyPrompt(obs, "2026-03-11", "")

	if !strings.Contains(prompt, "2026-03-11") {
		t.Error("prompt should contain the date")
	}
	if !strings.Contains(prompt, "1. Decided to use PostgreSQL") {
		t.Error("prompt should contain numbered observations")
	}
	if !strings.Contains(prompt, "2. Auth module needs refactoring") {
		t.Error("prompt should contain all observations")
	}
	if !strings.Contains(prompt, "daily memory") {
		t.Error("prompt should mention daily memory")
	}
}

func TestWeeklyPromptFormat(t *testing.T) {
	summaries := []string{
		"## Key Decisions\n- Use PostgreSQL",
		"## Progress\n- Auth module refactored",
	}
	prompt := WeeklyPrompt(summaries, "2026-W11", "")

	if !strings.Contains(prompt, "2026-W11") {
		t.Error("prompt should contain the week ID")
	}
	if !strings.Contains(prompt, "Day 1") {
		t.Error("prompt should label daily summaries")
	}
	if !strings.Contains(prompt, "weekly memory") {
		t.Error("prompt should mention weekly")
	}
}

func TestMonthlyPromptFormat(t *testing.T) {
	summaries := []string{
		"## Week highlights\n- Major refactor completed",
	}
	prompt := MonthlyPrompt(summaries, "2026-03", "")

	if !strings.Contains(prompt, "2026-03") {
		t.Error("prompt should contain the month")
	}
	if !strings.Contains(prompt, "monthly memory") {
		t.Error("prompt should mention monthly")
	}
}

func TestDailyPromptWithGuidelines(t *testing.T) {
	obs := []string{"observation 1"}
	guidelines := "Always highlight security decisions.\nIgnore dependency update noise."
	prompt := DailyPrompt(obs, "2026-03-11", guidelines)

	if !strings.Contains(prompt, "<team-guidelines>") {
		t.Error("prompt should contain guidelines header")
	}
	if !strings.Contains(prompt, "security decisions") {
		t.Error("prompt should contain team guidelines content")
	}
	if !strings.Contains(prompt, "1. observation 1") {
		t.Error("prompt should still contain observations")
	}
}

func TestDailyPromptWithoutGuidelines(t *testing.T) {
	obs := []string{"observation 1"}
	prompt := DailyPrompt(obs, "2026-03-11", "")

	if strings.Contains(prompt, "<team-guidelines>") {
		t.Error("prompt should not contain guidelines header when empty")
	}
}

func TestDailyPromptWithDiscussionFactPaths(t *testing.T) {
	obs := []string{"observation 1"}
	paths := []string{"memory/.discussion-facts/2026-03-10.md", "memory/.discussion-facts/2026-03-11.md"}
	prompt := DailyPrompt(obs, "2026-03-11", "", paths...)

	if !strings.Contains(prompt, "Discussion Fact Files") {
		t.Error("prompt should contain Discussion Fact Files section")
	}
	if !strings.Contains(prompt, "memory/.discussion-facts/2026-03-10.md") {
		t.Error("prompt should contain file path")
	}
	if !strings.Contains(prompt, "Read each discussion fact file") {
		t.Error("prompt should instruct reading of fact files")
	}
	if !strings.Contains(prompt, "1. observation 1") {
		t.Error("prompt should still contain observations")
	}
}

func TestDailyPromptWithoutDiscussionFacts(t *testing.T) {
	obs := []string{"observation 1"}
	prompt := DailyPrompt(obs, "2026-03-11", "")

	if strings.Contains(prompt, "Discussion Fact Files") {
		t.Error("prompt should not contain Discussion Fact Files section when empty")
	}
	if strings.Contains(prompt, "Read each discussion fact file") {
		t.Error("prompt should not mention reading files when no discussion facts")
	}
}

func TestDiscussionFactsPrompt(t *testing.T) {
	prompt := DiscussionFactsPrompt("Arch Review", "We discussed architecture", "Speaker 1: Let's review\nSpeaker 2: Sounds good", "")

	if !strings.Contains(prompt, "Arch Review") {
		t.Error("prompt should contain the discussion title")
	}
	if !strings.Contains(prompt, "We discussed architecture") {
		t.Error("prompt should contain the summary")
	}
	if !strings.Contains(prompt, "Speaker 1:") {
		t.Error("prompt should contain transcript text")
	}
	if !strings.Contains(prompt, "Decisions") {
		t.Error("prompt should mention expected categories")
	}
	if !strings.Contains(prompt, "Action Items") {
		t.Error("prompt should mention Action Items category")
	}
}

func TestDiscussionFactsPromptEmptySummary(t *testing.T) {
	prompt := DiscussionFactsPrompt("Title", "", "transcript text", "")

	if strings.Contains(prompt, "### Summary") {
		t.Error("prompt should not contain Summary section when empty")
	}
	if !strings.Contains(prompt, "transcript text") {
		t.Error("prompt should contain transcript even with empty summary")
	}
}

func TestDiscussionFactsPromptEmptyTranscript(t *testing.T) {
	prompt := DiscussionFactsPrompt("Title", "summary text", "", "")

	if !strings.Contains(prompt, "summary text") {
		t.Error("prompt should contain summary")
	}
	if strings.Contains(prompt, "### Transcript") {
		t.Error("prompt should not contain Transcript section when empty")
	}
}

func TestDiscussionFactsPromptWithGuidelines(t *testing.T) {
	prompt := DiscussionFactsPrompt("Title", "summary", "transcript", "Focus on security decisions")

	if !strings.Contains(prompt, "<team-guidelines>") {
		t.Error("prompt should contain guidelines header")
	}
	if !strings.Contains(prompt, "security decisions") {
		t.Error("prompt should contain guideline content")
	}
}

// TestClaudeRunRequiresCLI verifies Run fails gracefully when claude is not available.
func TestClaudeRunRequiresCLI(t *testing.T) {
	t.Setenv("PATH", "")
	c := &Claude{}
	_, err := c.Run(context.Background(), "test")
	if err == nil {
		t.Error("expected error when claude CLI not in PATH")
	}
}
