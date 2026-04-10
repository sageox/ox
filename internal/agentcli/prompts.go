package agentcli

import (
	"fmt"
	"strings"
)

// systemPrompt is the universal persona and output rules prepended to all prompts.
const systemPrompt = `<system>
You are an expert summarizer and distiller of information.

This is a pure text-in/text-out task. All the input you need is provided below.
Do NOT read files, check the filesystem, or use any tools — EXCEPT when
explicitly permitted (e.g., reading team guidance or fact files listed below).
Do NOT consider any prior context or existing files beyond what is provided or
permitted in this prompt.

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

// WriteGuidelines wraps team guidelines in a <team-guidelines> tag and tells
// the LLM which pipeline stage it's in. The stage identifier lets the guidelines
// reference optional per-stage override files via progressive disclosure
// (e.g., "if extracting discussions, read EXTRACT-discussions.md").
// The LLM MAY use the Read tool to access files in memory/guidance/.
func WriteGuidelines(sb *strings.Builder, guidelines, stage string) {
	if guidelines == "" {
		return
	}
	sb.WriteString("<team-guidelines>\n")
	if stage != "" {
		fmt.Fprintf(sb, "Current pipeline stage: %s\n\n", stage)
	}
	sb.WriteString(guidelines)
	if !strings.HasSuffix(guidelines, "\n") {
		sb.WriteByte('\n')
	}
	sb.WriteString("\nYou MAY use the Read tool to access any file referenced above in memory/guidance/.\n")
	sb.WriteString("</team-guidelines>\n\n")
}

// FactCitation is a fact-with-citation-number passed into DailyPrompt for
// inline rendering. The Num field is the citation marker the LLM should use
// when referencing this fact in its synthesis. Multiple facts may share the
// same Num if they originate from the same source (deduped by URL upstream).
//
// Added for gh #476: distill summaries must carry source links so readers
// can drill down to the original discussion / PR / session.
type FactCitation struct {
	Num         int    // 1-based citation number (the [N] the LLM should emit)
	Headline    string // required
	Summary     string // optional
	Rationale   string // optional
	Who         string // optional
	SourceType  string // "discussion" | "github" | "session" | "observation"
	SourceTitle string // optional human label (e.g., PR or discussion title)
	Date        string // YYYY-MM-DD slice of the fact's timestamp
	Category    string // optional: decision | learning | ship | ...
}

// DailyPrompt builds a prompt for distilling observations into a daily memory summary.
// If guidelines is non-empty, it is prepended as team-specific distillation preferences.
//
// Facts are rendered inline with citation numbers. The LLM is instructed to
// include `[N]` markers (or `[anchor][N]` reference links) in its synthesis;
// the caller post-processes the output to append a "## Sources" section with
// resolved URLs. See cmd/ox/distill_citations.go for the rendering side.
func DailyPrompt(observations []string, date, guidelines string, citedFacts []FactCitation) string {
	var sb strings.Builder

	sb.WriteString(systemPrompt)
	WriteGuidelines(&sb, guidelines, "distill-daily")

	sb.WriteString("<task>\n")
	sb.WriteString("Distill these team observations and facts into a daily memory summary.\n")
	sb.WriteString("Focus on decisions, patterns, and learnings. Omit routine actions.\n")
	if len(citedFacts) > 0 {
		sb.WriteString("\n")
		sb.WriteString("Each fact below has a citation number in brackets, like [3].\n")
		sb.WriteString("When you reference a fact in your summary, attach its citation marker.\n")
		sb.WriteString("Prefer markdown reference-link syntax with a NATURAL anchor from the sentence:\n")
		sb.WriteString("    \"We merged [PR #152][2] last week.\"\n")
		sb.WriteString("    \"Following the [architecture discussion][1], the storage layer was rewritten.\"\n")
		sb.WriteString("If no natural anchor fits, end the sentence with a bare marker:\n")
		sb.WriteString("    \"Natural language is more merge-friendly than code. [1]\"\n")
		sb.WriteString("Combine multiple sources at one point as chained markers: [1][3].\n")
		sb.WriteString("\n")
		sb.WriteString("Rules:\n")
		sb.WriteString("- Do NOT invent citation numbers — only use numbers from the Facts list below.\n")
		sb.WriteString("- Do NOT write a Sources section yourself; it is appended programmatically.\n")
		sb.WriteString("- Observations have NO citation numbers — weave them into the narrative without markers.\n")
	}
	sb.WriteString("</task>\n\n")

	if len(observations) > 0 {
		fmt.Fprintf(&sb, "## Observations (%s)\n\n", date)
		for i, obs := range observations {
			fmt.Fprintf(&sb, "%d. %s\n", i+1, obs)
		}
		sb.WriteString("\n")
	}

	if len(citedFacts) > 0 {
		sb.WriteString("## Facts\n\n")
		for _, f := range citedFacts {
			writeFactCitationLine(&sb, f)
		}
	}

	return sb.String()
}

// writeFactCitationLine renders a single fact for the daily prompt.
// Format: "[N] (source_type · date · title · who · category) headline. summary. Rationale: rationale."
// Fields are omitted when empty so short facts stay short.
//
// Multiline fact content (LLM-extracted github PR descriptions can contain
// literal newlines) is collapsed to a single line so each fact occupies
// exactly one prompt line — keeps the [N]-per-line layout parseable.
func writeFactCitationLine(sb *strings.Builder, f FactCitation) {
	fmt.Fprintf(sb, "[%d] (", f.Num)
	sb.WriteString(f.SourceType)
	if f.Date != "" {
		sb.WriteString(" · ")
		sb.WriteString(f.Date)
	}
	if f.SourceTitle != "" {
		sb.WriteString(" · ")
		sb.WriteString(collapseWhitespace(f.SourceTitle))
	}
	if f.Who != "" {
		sb.WriteString(" · ")
		sb.WriteString(collapseWhitespace(f.Who))
	}
	if f.Category != "" {
		sb.WriteString(" · ")
		sb.WriteString(f.Category)
	}
	sb.WriteString(") ")
	headline := collapseWhitespace(f.Headline)
	sb.WriteString(headline)
	if !strings.HasSuffix(headline, ".") {
		sb.WriteString(".")
	}
	if f.Summary != "" {
		summary := collapseWhitespace(f.Summary)
		sb.WriteString(" ")
		sb.WriteString(summary)
		if !strings.HasSuffix(summary, ".") {
			sb.WriteString(".")
		}
	}
	if f.Rationale != "" {
		rationale := collapseWhitespace(f.Rationale)
		sb.WriteString(" Rationale: ")
		sb.WriteString(rationale)
		if !strings.HasSuffix(rationale, ".") {
			sb.WriteString(".")
		}
	}
	sb.WriteString("\n")
}

// collapseWhitespace replaces runs of whitespace (including newlines) with a
// single space and trims. Used to sanitize fact fields that may contain
// embedded newlines from LLM extraction (notably github PR descriptions).
func collapseWhitespace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// DiscussionFactsPrompt builds a prompt for extracting structured facts from a discussion.
// The LLM extracts decisions, learnings, open questions, action items, and key context
// as JSONL output (one JSON object per line).
// When annotations is non-empty, server-extracted annotations are included as ground truth.
func DiscussionFactsPrompt(title, summary, transcript, guidelines, annotations string) string {
	var sb strings.Builder

	sb.WriteString(systemPrompt)
	WriteGuidelines(&sb, guidelines, "extract-discussions")

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
//
// hasCitations should be true when the daily summaries contain `[N]` citation
// markers (the new format from gh #476). When true, the prompt instructs the
// LLM to preserve those markers verbatim — the caller has already renumbered
// them to a unified weekly numbering, and a fresh "## Sources" section will
// be appended after the LLM returns.
func WeeklyPrompt(dailySummaries []string, weekID, guidelines string, hasCitations bool) string {
	var sb strings.Builder

	sb.WriteString(systemPrompt)
	WriteGuidelines(&sb, guidelines, "distill-weekly")

	sb.WriteString("<task>\n")
	sb.WriteString("Synthesize these daily summaries into a weekly memory.\n")
	sb.WriteString("Identify themes, key decisions, and unresolved work. Compress — shorter than the combined input.\n")
	if hasCitations {
		writePreserveCitationsInstruction(&sb)
	}
	sb.WriteString("</task>\n\n")

	fmt.Fprintf(&sb, "## Dailies (%s)\n\n", weekID)
	for i, summary := range dailySummaries {
		fmt.Fprintf(&sb, "### Day %d\n\n%s\n\n", i+1, summary)
	}

	return sb.String()
}

// MonthlyPrompt builds a prompt for synthesizing weekly summaries into a monthly memory.
//
// hasCitations behaves the same way as in WeeklyPrompt — when true, the LLM
// is instructed to preserve `[N]` markers and the caller appends a unified
// "## Sources" section after generation.
func MonthlyPrompt(weeklySummaries []string, month, guidelines string, hasCitations bool) string {
	var sb strings.Builder

	sb.WriteString(systemPrompt)
	WriteGuidelines(&sb, guidelines, "distill-monthly")

	sb.WriteString("<task>\n")
	sb.WriteString("Synthesize these weekly summaries into a monthly memory.\n")
	sb.WriteString("Focus on milestones, architecture changes, and strategic direction. Omit day-to-day details.\n")
	if hasCitations {
		writePreserveCitationsInstruction(&sb)
	}
	sb.WriteString("</task>\n\n")

	fmt.Fprintf(&sb, "## Weeklies (%s)\n\n", month)
	for i, summary := range weeklySummaries {
		fmt.Fprintf(&sb, "### Week %d\n\n%s\n\n", i+1, summary)
	}

	return sb.String()
}

// writePreserveCitationsInstruction appends the standard preserve-markers
// guidance to a weekly or monthly prompt body.
func writePreserveCitationsInstruction(sb *strings.Builder) {
	sb.WriteString("\n")
	sb.WriteString("The summaries below contain markdown reference-link citations like `[N]` and\n")
	sb.WriteString("`[anchor][N]`. Preserve these markers VERBATIM in any text you carry forward.\n")
	sb.WriteString("Do NOT invent new numbers, do NOT renumber, do NOT remove them.\n")
	sb.WriteString("Do NOT write a Sources section — it is appended programmatically.\n")
}
