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
	prompt := DailyPrompt(obs, "2026-03-11", "", nil)

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
	prompt := WeeklyPrompt(summaries, "2026-W11", "", false)

	if !strings.Contains(prompt, "2026-W11") {
		t.Error("prompt should contain the week ID")
	}
	// Daily summaries are now wrapped in <daily-summary index="N">...</daily-summary>
	// envelopes (SECREVIEW llm-trust MEDIUM — multi-hop memory poisoning).
	if !strings.Contains(prompt, `<daily-summary index="1">`) {
		t.Error("prompt should wrap day 1 in <daily-summary index=\"1\"> envelope")
	}
	if !strings.Contains(prompt, "</daily-summary>") {
		t.Error("prompt should close the daily-summary envelope")
	}
	if !strings.Contains(prompt, "weekly memory") {
		t.Error("prompt should mention weekly")
	}
}

func TestMonthlyPromptFormat(t *testing.T) {
	summaries := []string{
		"## Week highlights\n- Major refactor completed",
	}
	prompt := MonthlyPrompt(summaries, "2026-03", "", false)

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
	prompt := DailyPrompt(obs, "2026-03-11", guidelines, nil)

	if !strings.Contains(prompt, "<team-guidelines-") {
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
	prompt := DailyPrompt(obs, "2026-03-11", "", nil)

	if strings.Contains(prompt, "<team-guidelines-") {
		t.Error("prompt should not contain guidelines header when empty")
	}
}

// TestDailyPromptWithCitedFacts verifies the new gh #476 prompt shape: facts
// are rendered inline with citation numbers and the LLM is instructed to use
// markdown reference-link syntax.
func TestDailyPromptWithCitedFacts(t *testing.T) {
	obs := []string{"observation 1"}
	cited := []FactCitation{
		{
			Num:         1,
			Headline:    "Team chose Postgres for analytics",
			SourceType:  "discussion",
			SourceTitle: "Architecture sync",
			Date:        "2026-03-10",
		},
		{
			Num:         2,
			Headline:    "Adopted token bucket rate limiting",
			SourceType:  "github",
			SourceTitle: "Add rate limiting",
			Date:        "2026-03-11",
		},
	}
	prompt := DailyPrompt(obs, "2026-03-11", "", cited)

	if !strings.Contains(prompt, "## Facts") {
		t.Error("prompt should contain Facts section")
	}
	if !strings.Contains(prompt, "[1]") || !strings.Contains(prompt, "[2]") {
		t.Error("prompt should contain citation numbers [1] and [2]")
	}
	if !strings.Contains(prompt, "Team chose Postgres for analytics") {
		t.Error("prompt should contain fact headline inline")
	}
	if !strings.Contains(prompt, "reference-link") {
		t.Error("prompt should mention reference-link syntax")
	}
	if !strings.Contains(prompt, "[PR #152][2]") {
		t.Error("prompt should include the reference-link example")
	}
	if !strings.Contains(prompt, "Do NOT invent citation numbers") {
		t.Error("prompt should warn against inventing citation numbers")
	}
	if !strings.Contains(prompt, "Do NOT write a Sources section") {
		t.Error("prompt should warn against writing a Sources section")
	}
	if !strings.Contains(prompt, "1. observation 1") {
		t.Error("prompt should still contain observations")
	}
}

func TestDailyPromptWithoutFacts(t *testing.T) {
	obs := []string{"observation 1"}
	prompt := DailyPrompt(obs, "2026-03-11", "", nil)

	if strings.Contains(prompt, "## Facts") {
		t.Error("prompt should not contain Facts section when no facts")
	}
	if strings.Contains(prompt, "citation marker") {
		t.Error("prompt should not mention citation markers when no facts")
	}
}

// TestWeeklyPromptPreservesCitations verifies the gh #476 weekly prompt
// instruction set: when hasCitations=true, the LLM is told to keep [N]
// markers verbatim and not write its own Sources section.
func TestWeeklyPromptPreservesCitations(t *testing.T) {
	prompt := WeeklyPrompt([]string{"day 1 [1] body"}, "2026-W11", "", true)
	if !strings.Contains(prompt, "Preserve these markers VERBATIM") {
		t.Error("weekly prompt should instruct preserving citation markers when hasCitations=true")
	}
	if !strings.Contains(prompt, "Do NOT write a Sources section") {
		t.Error("weekly prompt should warn against writing a Sources section")
	}
}

// TestWeeklyPromptNoCitationsClause omits the preserve instruction when there
// are no citations to preserve (avoids confusing LLM with irrelevant rules).
func TestWeeklyPromptNoCitationsClause(t *testing.T) {
	prompt := WeeklyPrompt([]string{"day 1 body"}, "2026-W11", "", false)
	if strings.Contains(prompt, "Preserve these markers") {
		t.Error("weekly prompt should NOT include preserve clause when hasCitations=false")
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

	if !strings.Contains(prompt, "<team-guidelines-") {
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

func TestWriteGuidelines(t *testing.T) {
	t.Run("empty guidelines is no-op", func(t *testing.T) {
		var sb strings.Builder
		WriteGuidelines(&sb, "", "distill-daily")
		if sb.Len() != 0 {
			t.Errorf("expected empty output, got %q", sb.String())
		}
	})

	t.Run("with stage", func(t *testing.T) {
		var sb strings.Builder
		WriteGuidelines(&sb, "focus on security", "extract-discussions")
		out := sb.String()
		// Tag is nonce-suffixed for trust-boundary protection (SECREVIEW
		// llm-trust MEDIUM): the closing tag cannot be pre-embedded by a
		// content author. Match on the prefix and confirm the symmetric
		// open/close.
		if !strings.Contains(out, "<team-guidelines-") {
			t.Error("should contain opening team-guidelines tag with nonce")
		}
		if !strings.Contains(out, "</team-guidelines-") {
			t.Error("should contain closing team-guidelines tag with nonce")
		}
		if !strings.Contains(out, "Current pipeline stage: extract-discussions") {
			t.Error("should contain stage identifier")
		}
		if !strings.Contains(out, "focus on security") {
			t.Error("should contain guidelines content")
		}
		if !strings.Contains(out, "You MAY use the Read tool") {
			t.Error("should contain Read tool permission")
		}
	})

	t.Run("empty stage omits stage line", func(t *testing.T) {
		var sb strings.Builder
		WriteGuidelines(&sb, "some guidance", "")
		out := sb.String()
		if strings.Contains(out, "Current pipeline stage") {
			t.Error("should not contain stage line when stage is empty")
		}
		if !strings.Contains(out, "some guidance") {
			t.Error("should still contain guidelines content")
		}
	})

	t.Run("adds trailing newline if missing", func(t *testing.T) {
		var sb strings.Builder
		WriteGuidelines(&sb, "no trailing newline", "test")
		out := sb.String()
		// guidelines content should end with \n before the Read tool line
		if !strings.Contains(out, "no trailing newline\n") {
			t.Error("should add trailing newline to guidelines content")
		}
	})
}
