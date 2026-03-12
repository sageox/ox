package agentcli

import (
	"fmt"
	"strings"
)

// baseInstruction is the universal output format instruction appended to all prompts.
const baseInstruction = "Output concise markdown. No code fences or preamble.\n\n"

// writeGuidelines prepends team distillation guidelines if provided.
// Guidelines come from DISTILL.md in the team context — they let teams
// customize what gets emphasized, omitted, or structured differently.
func writeGuidelines(sb *strings.Builder, guidelines string) {
	if guidelines == "" {
		return
	}
	sb.WriteString("## Team Distillation Guidelines\n\n")
	sb.WriteString(guidelines)
	if !strings.HasSuffix(guidelines, "\n") {
		sb.WriteByte('\n')
	}
	sb.WriteByte('\n')
}

// DailyPrompt builds a prompt for distilling observations into a daily memory summary.
// If guidelines is non-empty, it is prepended as team-specific distillation preferences.
// Optional discussionFacts are appended as additional context from team discussions.
func DailyPrompt(observations []string, date, guidelines string, discussionFacts ...string) string {
	var sb strings.Builder

	writeGuidelines(&sb, guidelines)
	sb.WriteString("Distill these team observations into a daily memory summary.\n")
	sb.WriteString("Focus on decisions, patterns, and learnings. Omit routine actions.\n")
	if len(discussionFacts) > 0 {
		sb.WriteString("Synthesize both observations and discussion facts into a cohesive summary.\n")
		sb.WriteString("Discussion facts represent key takeaways from recorded team discussions.\n")
	}
	sb.WriteString(baseInstruction)

	if len(observations) > 0 {
		fmt.Fprintf(&sb, "## Observations (%s)\n\n", date)
		for i, obs := range observations {
			fmt.Fprintf(&sb, "%d. %s\n", i+1, obs)
		}
	}

	if len(discussionFacts) > 0 {
		sb.WriteString("\n## Discussion Facts\n\n")
		for i, fact := range discussionFacts {
			fmt.Fprintf(&sb, "### Discussion %d\n\n%s\n\n", i+1, fact)
		}
	}

	return sb.String()
}

// DiscussionFactsPrompt builds a prompt for extracting structured facts from a discussion.
// The LLM extracts decisions, learnings, open questions, action items, and key context.
func DiscussionFactsPrompt(title, summary, transcript, guidelines string) string {
	var sb strings.Builder

	writeGuidelines(&sb, guidelines)
	sb.WriteString("Extract structured facts from this team discussion.\n")
	sb.WriteString("Organize into these categories (omit empty categories):\n")
	sb.WriteString("- **Decisions**: Concrete choices the team made\n")
	sb.WriteString("- **Learnings**: New understanding or insights shared\n")
	sb.WriteString("- **Open Questions**: Unresolved items needing follow-up\n")
	sb.WriteString("- **Action Items**: Specific tasks someone committed to\n")
	sb.WriteString("- **Key Context**: Important background information mentioned\n")
	sb.WriteString(baseInstruction)

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

	writeGuidelines(&sb, guidelines)
	sb.WriteString("Synthesize these daily summaries into a weekly memory.\n")
	sb.WriteString("Identify themes, key decisions, and unresolved work. Compress — shorter than the combined input.\n")
	sb.WriteString(baseInstruction)

	fmt.Fprintf(&sb, "## Dailies (%s)\n\n", weekID)
	for i, summary := range dailySummaries {
		fmt.Fprintf(&sb, "### Day %d\n\n%s\n\n", i+1, summary)
	}

	return sb.String()
}

// MonthlyPrompt builds a prompt for synthesizing weekly summaries into a monthly memory.
func MonthlyPrompt(weeklySummaries []string, month, guidelines string) string {
	var sb strings.Builder

	writeGuidelines(&sb, guidelines)
	sb.WriteString("Synthesize these weekly summaries into a monthly memory.\n")
	sb.WriteString("Focus on milestones, architecture changes, and strategic direction. Omit day-to-day details.\n")
	sb.WriteString(baseInstruction)

	fmt.Fprintf(&sb, "## Weeklies (%s)\n\n", month)
	for i, summary := range weeklySummaries {
		fmt.Fprintf(&sb, "### Week %d\n\n%s\n\n", i+1, summary)
	}

	return sb.String()
}
