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

func TestDailyPromptWithFactPaths(t *testing.T) {
	obs := []string{"observation 1"}
	paths := []string{"memory/.discussion-facts/2026-03-10.jsonl", "memory/.github-facts/2026-03-10-uuid.jsonl"}
	prompt := DailyPrompt(obs, "2026-03-11", "", paths...)

	if !strings.Contains(prompt, "## Fact Files") {
		t.Error("prompt should contain Fact Files section")
	}
	if !strings.Contains(prompt, "memory/.discussion-facts/2026-03-10.jsonl") {
		t.Error("prompt should contain discussion fact path")
	}
	if !strings.Contains(prompt, "memory/.github-facts/2026-03-10-uuid.jsonl") {
		t.Error("prompt should contain github fact path")
	}
	if !strings.Contains(prompt, "Read each fact file") {
		t.Error("prompt should instruct reading of fact files")
	}
	if !strings.Contains(prompt, "ALL sources") {
		t.Error("prompt should instruct incorporating all fact sources")
	}
	if !strings.Contains(prompt, "1. observation 1") {
		t.Error("prompt should still contain observations")
	}
}

func TestDailyPromptWithoutFacts(t *testing.T) {
	obs := []string{"observation 1"}
	prompt := DailyPrompt(obs, "2026-03-11", "")

	if strings.Contains(prompt, "Fact Files") {
		t.Error("prompt should not contain Fact Files section when empty")
	}
	if strings.Contains(prompt, "Read each fact file") {
		t.Error("prompt should not mention reading files when no facts")
	}
}

func TestDiscussionFactsPrompt(t *testing.T) {
	prompt := DiscussionFactsPrompt("Arch Review", "We discussed architecture", "Speaker 1: Let's review\nSpeaker 2: Sounds good", "", "")

	if !strings.Contains(prompt, "Arch Review") {
		t.Error("prompt should contain the discussion title")
	}
	if !strings.Contains(prompt, "We discussed architecture") {
		t.Error("prompt should contain the summary")
	}
	if !strings.Contains(prompt, "Speaker 1:") {
		t.Error("prompt should contain transcript text")
	}
	if !strings.Contains(prompt, "JSONL") {
		t.Error("prompt should mention JSONL format")
	}
	if !strings.Contains(prompt, "headline") {
		t.Error("prompt should mention headline field")
	}
	if !strings.Contains(prompt, "category") {
		t.Error("prompt should mention category field")
	}
	if !strings.Contains(prompt, "decision") {
		t.Error("prompt should list decision as a category")
	}
	if !strings.Contains(prompt, "action_item") {
		t.Error("prompt should list action_item as a category")
	}
}

func TestDiscussionFactsPromptEmptySummary(t *testing.T) {
	prompt := DiscussionFactsPrompt("Title", "", "transcript text", "", "")

	if strings.Contains(prompt, "### Summary") {
		t.Error("prompt should not contain Summary section when empty")
	}
	if !strings.Contains(prompt, "transcript text") {
		t.Error("prompt should contain transcript even with empty summary")
	}
}

func TestDiscussionFactsPromptEmptyTranscript(t *testing.T) {
	prompt := DiscussionFactsPrompt("Title", "summary text", "", "", "")

	if !strings.Contains(prompt, "summary text") {
		t.Error("prompt should contain summary")
	}
	if strings.Contains(prompt, "### Transcript") {
		t.Error("prompt should not contain Transcript section when empty")
	}
}

func TestDiscussionFactsPromptWithGuidelines(t *testing.T) {
	prompt := DiscussionFactsPrompt("Title", "summary", "transcript", "Focus on security decisions", "")

	if !strings.Contains(prompt, "<team-guidelines>") {
		t.Error("prompt should contain guidelines header")
	}
	if !strings.Contains(prompt, "security decisions") {
		t.Error("prompt should contain guideline content")
	}
}

func TestDiscussionFactsPromptWithAnnotations(t *testing.T) {
	annotations := "- [decision] Use rotating session tokens\n- [action-item] Migrate auth by end of sprint"
	prompt := DiscussionFactsPrompt("Security Review", "Reviewed auth approach", "Speaker 1: We need tokens", "", annotations)

	if !strings.Contains(prompt, "### Server Annotations") {
		t.Error("prompt should contain Server Annotations section")
	}
	if !strings.Contains(prompt, "Use rotating session tokens") {
		t.Error("prompt should contain annotation content")
	}
	if !strings.Contains(prompt, "Migrate auth by end of sprint") {
		t.Error("prompt should contain all annotations")
	}
	if !strings.Contains(prompt, "ground truth") {
		t.Error("prompt should instruct LLM to treat annotations as ground truth")
	}

	// annotations should appear before transcript
	annotationsIdx := strings.Index(prompt, "### Server Annotations")
	transcriptIdx := strings.Index(prompt, "### Transcript")
	if annotationsIdx >= transcriptIdx {
		t.Error("annotations section should appear before transcript section")
	}
}

func TestDiscussionFactsPromptWithoutAnnotations(t *testing.T) {
	prompt := DiscussionFactsPrompt("Title", "summary", "transcript", "", "")

	if strings.Contains(prompt, "### Server Annotations") {
		t.Error("prompt should not contain Server Annotations section when empty")
	}
	if strings.Contains(prompt, "ground truth") {
		t.Error("prompt should not mention ground truth when no annotations")
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
