package agentcli

import (
	"fmt"
	"strings"
)

// systemPrompt is the universal persona and output rules prepended to all prompts.
const systemPrompt = `<system>
You are an expert summarizer and distiller of information.

This is a pure text-in/text-out task. All the input you need is provided below.
Do NOT read files, check the filesystem, or use any tools. Do NOT consider any
prior context or existing files — work ONLY from the input provided in this prompt.

Your sole output is the summary or extraction described in the <task> section.

Rules:
- Follow the format specified in the <task> section exactly.
- Do NOT include preamble, commentary, or meta-statements about your work.
- Do NOT say things like "Here is the summary", "I have distilled the following",
  "No update needed", or "Let me proceed with...".
- Do NOT describe what you did, will do, or decided not to do.
- Do NOT reference tools, permissions, or capabilities.
- Just output the result directly.
- No code fences unless the content itself contains code.
</system>

`

// writeGuidelines wraps team distillation guidelines in a <team-guidelines> tag.
// Guidelines come from DISTILL.md in the team context — they let teams
// customize what gets emphasized, omitted, or structured differently.
func writeGuidelines(sb *strings.Builder, guidelines string) {
	if guidelines == "" {
		return
	}
	sb.WriteString("<team-guidelines>\n")
	sb.WriteString(guidelines)
	if !strings.HasSuffix(guidelines, "\n") {
		sb.WriteByte('\n')
	}
	sb.WriteString("</team-guidelines>\n\n")
}

// DailyPrompt builds a prompt for distilling observations into a daily memory summary.
// If guidelines is non-empty, it is prepended as team-specific distillation preferences.
// Optional factPaths are relative file paths to fact JSONL files (discussion, github, etc.)
// that the AI coworker should read and incorporate into the summary.
func DailyPrompt(observations []string, date, guidelines string, factPaths ...string) string {
	var sb strings.Builder

	sb.WriteString(systemPrompt)
	writeGuidelines(&sb, guidelines)

	sb.WriteString("<task>\n")
	sb.WriteString("Distill these team observations into a daily memory summary.\n")
	sb.WriteString("Focus on decisions, patterns, and learnings. Omit routine actions.\n")
	if len(factPaths) > 0 {
		sb.WriteString("Read each fact file listed below and synthesize their content\n")
		sb.WriteString("together with the observations into a cohesive summary.\n")
		sb.WriteString("Fact files are JSONL — each line is a JSON object with a headline, summary, and category.\n")
		sb.WriteString("Incorporate facts from ALL sources (discussions, github activity, etc.).\n")
		sb.WriteString("Exception: You MAY use the Read tool to access the fact files listed below.\n")
	}
	sb.WriteString("</task>\n\n")

	if len(observations) > 0 {
		fmt.Fprintf(&sb, "## Observations (%s)\n\n", date)
		for i, obs := range observations {
			fmt.Fprintf(&sb, "%d. %s\n", i+1, obs)
		}
	}

	if len(factPaths) > 0 {
		sb.WriteString("\n## Fact Files\n\n")
		for _, path := range factPaths {
			fmt.Fprintf(&sb, "- [%s](%s)\n", path, path)
		}
	}

	return sb.String()
}

// DiscussionFactsPrompt builds a prompt for extracting structured facts from a discussion.
// The LLM extracts decisions, learnings, open questions, action items, and key context
// as JSONL output (one JSON object per line).
// When annotations is non-empty, server-extracted annotations are included as ground truth.
func DiscussionFactsPrompt(title, summary, transcript, guidelines, annotations string) string {
	var sb strings.Builder

	sb.WriteString(systemPrompt)
	writeGuidelines(&sb, guidelines)

	sb.WriteString("<task>\n")
	sb.WriteString("Extract structured facts from this team discussion as JSONL.\n")
	sb.WriteString("Output one JSON object per line (NOT a JSON array). Each line is a fact with these fields:\n")
	sb.WriteString("- \"headline\" (required): One sentence — the fact or decision\n")
	sb.WriteString("- \"summary\" (optional): 2-3 sentences of supporting detail\n")
	sb.WriteString("- \"category\" (required): One of: decision, learning, open_question, action_item, context\n")
	sb.WriteString("- \"who\" (optional): Primary person involved\n")
	sb.WriteString("- \"timestamp\" (required): ISO 8601 timestamp of the discussion\n")
	sb.WriteString("\n")
	sb.WriteString("Example output (two facts):\n")
	sb.WriteString(`{"headline":"Team chose PostgreSQL for analytics","category":"decision","timestamp":"2026-03-10T14:23:00Z"}` + "\n")
	sb.WriteString(`{"headline":"Auth module needs refactoring before launch","summary":"Current token refresh has race conditions under load.","category":"action_item","who":"Alice","timestamp":"2026-03-10T14:23:00Z"}` + "\n")
	sb.WriteString("\n")
	sb.WriteString("Output ONLY the JSONL lines — no markdown, no commentary, no code fences.\n")
	sb.WriteString("</task>\n\n")

	fmt.Fprintf(&sb, "## Discussion: %s\n\n", title)

	if summary != "" {
		sb.WriteString("### Summary\n\n")
		sb.WriteString(summary)
		sb.WriteString("\n\n")
	}

	if annotations != "" {
		sb.WriteString("### Server Annotations\n\n")
		sb.WriteString("The following structured annotations were pre-extracted from the recording. ")
		sb.WriteString("Use these as ground truth and supplement with additional facts from the transcript.\n\n")
		sb.WriteString(annotations)
		sb.WriteString("\n\n")
	}

	if transcript != "" {
		sb.WriteString("### Transcript\n\n")
		sb.WriteString(transcript)
		sb.WriteString("\n")
	}

	return sb.String()
}

// WeeklyPrompt builds a prompt for synthesizing daily summaries into a weekly memory.
func WeeklyPrompt(dailySummaries []string, weekID, guidelines string) string {
	var sb strings.Builder

	sb.WriteString(systemPrompt)
	writeGuidelines(&sb, guidelines)

	sb.WriteString("<task>\n")
	sb.WriteString("Synthesize these daily summaries into a weekly memory.\n")
	sb.WriteString("Identify themes, key decisions, and unresolved work. Compress — shorter than the combined input.\n")
	sb.WriteString("</task>\n\n")

	fmt.Fprintf(&sb, "## Dailies (%s)\n\n", weekID)
	for i, summary := range dailySummaries {
		fmt.Fprintf(&sb, "### Day %d\n\n%s\n\n", i+1, summary)
	}

	return sb.String()
}

// MonthlyPrompt builds a prompt for synthesizing weekly summaries into a monthly memory.
func MonthlyPrompt(weeklySummaries []string, month, guidelines string) string {
	var sb strings.Builder

	sb.WriteString(systemPrompt)
	writeGuidelines(&sb, guidelines)

	sb.WriteString("<task>\n")
	sb.WriteString("Synthesize these weekly summaries into a monthly memory.\n")
	sb.WriteString("Focus on milestones, architecture changes, and strategic direction. Omit day-to-day details.\n")
	sb.WriteString("</task>\n\n")

	fmt.Fprintf(&sb, "## Weeklies (%s)\n\n", month)
	for i, summary := range weeklySummaries {
		fmt.Fprintf(&sb, "### Week %d\n\n%s\n\n", i+1, summary)
	}

	return sb.String()
}
