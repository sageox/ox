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
- Output ONLY the requested summary in concise markdown.
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
// Optional discussionFactPaths are relative file paths to discussion fact files that
// the AI coworker should read and incorporate into the summary.
func DailyPrompt(observations []string, date, guidelines string, discussionFactPaths ...string) string {
	var sb strings.Builder

	sb.WriteString(systemPrompt)
	writeGuidelines(&sb, guidelines)

	sb.WriteString("<task>\n")
	sb.WriteString("Distill these team observations into a daily memory summary.\n")
	sb.WriteString("Focus on decisions, patterns, and learnings. Omit routine actions.\n")
	if len(discussionFactPaths) > 0 {
		sb.WriteString("Read each discussion fact file listed below and synthesize their content\n")
		sb.WriteString("together with the observations into a cohesive summary.\n")
		sb.WriteString("Exception: You MAY use the Read tool to access the discussion fact files listed below.\n")
	}
	sb.WriteString("</task>\n\n")

	if len(observations) > 0 {
		fmt.Fprintf(&sb, "## Observations (%s)\n\n", date)
		for i, obs := range observations {
			fmt.Fprintf(&sb, "%d. %s\n", i+1, obs)
		}
	}

	if len(discussionFactPaths) > 0 {
		sb.WriteString("\n## Discussion Fact Files\n\n")
		for _, path := range discussionFactPaths {
			fmt.Fprintf(&sb, "- [%s](%s)\n", path, path)
		}
	}

	return sb.String()
}

// DiscussionFactsPrompt builds a prompt for extracting structured facts from a discussion.
// The LLM extracts decisions, learnings, open questions, action items, and key context.
func DiscussionFactsPrompt(title, summary, transcript, guidelines string) string {
	var sb strings.Builder

	sb.WriteString(systemPrompt)
	writeGuidelines(&sb, guidelines)

	sb.WriteString("<task>\n")
	sb.WriteString("Extract structured facts from this team discussion.\n")
	sb.WriteString("Organize into these categories (omit empty categories):\n")
	sb.WriteString("- **Decisions**: Concrete choices the team made\n")
	sb.WriteString("- **Learnings**: New understanding or insights shared\n")
	sb.WriteString("- **Open Questions**: Unresolved items needing follow-up\n")
	sb.WriteString("- **Action Items**: Specific tasks someone committed to\n")
	sb.WriteString("- **Key Context**: Important background information mentioned\n")
	sb.WriteString("</task>\n\n")

	fmt.Fprintf(&sb, "## Discussion: %s\n\n", title)

	if summary != "" {
		sb.WriteString("### Summary\n\n")
		sb.WriteString(summary)
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
