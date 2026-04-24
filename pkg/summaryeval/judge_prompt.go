package summaryeval

import (
	"encoding/json"
	"fmt"
	"strings"
)

// BuildJudgePrompt constructs the prompt text sent to the LLM judge.
// Paired mode (reference != nil) asks for semantic equivalence; absolute
// mode (reference == nil) asks for on-merits evaluation against the
// rubric's definition of a good summary.
//
// The prompt asks for a strict JSON response to simplify parsing and
// make the judge's output CI-gateable. Models that free-form their
// response will parse-fail and surface as errors.
func BuildJudgePrompt(reference *Summary, candidate Summary, opts JudgeOptions) string {
	var sb strings.Builder

	sb.WriteString("# Session Summary Quality Judge\n\n")

	if reference != nil {
		sb.WriteString("You are grading a candidate session summary against a hand-reviewed reference summary that represents the quality bar. Score whether the candidate conveys the same information and judgment as the reference.\n\n")
	} else {
		sb.WriteString("You are grading a session summary on its own merits. The summary describes what happened during an AI coding agent session. A good summary is specific, action-oriented, honest about outcome, and captures pivotal moments (user questions that redirected the work, key decisions, breakthroughs).\n\n")
	}

	sb.WriteString("## Rubric\n\n")
	sb.WriteString("Score each dimension independently from 0.0 (fails completely) to 1.0 (perfect):\n\n")
	sb.WriteString("- **title**: Is the title a short descriptive phrase (5-10 words) that captures the actual work? Penalize generic ('Session recording'), conversational ('Let me help you'), or command-like ('bd create ...') titles.\n")
	sb.WriteString("- **summary**: One-paragraph executive summary. Does it cover motivation, approach, and outcome? Penalize skeleton summaries ('Session stopped.') or stats-only ('5 user messages, 10 assistant responses').\n")
	sb.WriteString("- **key_actions**: 3-10 concrete bullets of what was actually done. Real verbs, specifics. Penalize vague ('Did some work') or missing.\n")
	sb.WriteString("- **outcome**: Correctly classifies as success, partial, or failed. Penalize mismatched or missing.\n")
	sb.WriteString("- **aha_moments**: 3-5 pivotal moments (user questions that redirected, key decisions, breakthroughs). Penalize missing on non-trivial sessions; penalize over-eager if trivial.\n\n")

	sb.WriteString("## Response format\n\n")
	sb.WriteString("Respond with a single JSON object, no surrounding prose:\n\n")
	sb.WriteString("```json\n")
	sb.WriteString("{\n")
	sb.WriteString("  \"dimensions\": [\n")
	sb.WriteString("    {\"dimension\": \"title\", \"score\": 0.9, \"reason\": \"specific and action-oriented\"},\n")
	sb.WriteString("    {\"dimension\": \"summary\", \"score\": 0.7, \"reason\": \"...\"},\n")
	sb.WriteString("    {\"dimension\": \"key_actions\", \"score\": 0.8, \"reason\": \"...\"},\n")
	sb.WriteString("    {\"dimension\": \"outcome\", \"score\": 1.0, \"reason\": \"...\"},\n")
	sb.WriteString("    {\"dimension\": \"aha_moments\", \"score\": 0.6, \"reason\": \"...\"}\n")
	sb.WriteString("  ],\n")
	sb.WriteString("  \"overall\": 0.82,\n")
	fmt.Fprintf(&sb, "  \"rationale\": \"Brief overall verdict, up to %d chars\",\n", opts.MaxRationaleChars)
	if opts.IncludeSuggestions {
		sb.WriteString("  \"suggestions\": [\"specific actionable fix 1\", \"fix 2\"]\n")
	} else {
		sb.WriteString("  \"suggestions\": []\n")
	}
	sb.WriteString("}\n")
	sb.WriteString("```\n\n")

	if reference != nil {
		sb.WriteString("## Reference summary\n\n")
		sb.WriteString(renderSummaryForPrompt(*reference))
		sb.WriteString("\n")
	}

	sb.WriteString("## Candidate summary\n\n")
	sb.WriteString(renderSummaryForPrompt(candidate))
	sb.WriteString("\n")

	sb.WriteString("## Guidelines\n\n")
	sb.WriteString("- Be strict but fair. Give 1.0 only for dimensions that are genuinely excellent.\n")
	sb.WriteString("- 0.5 means 'acceptable but with clear room for improvement'.\n")
	sb.WriteString("- Below 0.3 means the dimension is significantly broken or missing.\n")
	sb.WriteString("- Overall should reflect your holistic judgment, not a strict weighted average of dimensions.\n")
	if opts.IncludeSuggestions {
		sb.WriteString("- Suggestions should be concrete and actionable. Avoid 'add more detail' — specify WHAT detail.\n")
	}
	sb.WriteString("- Return ONLY the JSON object. No markdown fences, no prose before or after.\n")

	return sb.String()
}

// renderSummaryForPrompt formats a Summary as JSON for the judge to read.
// Using JSON (not pretty prose) keeps the prompt compact and lets the
// judge see exactly what fields are present vs missing.
func renderSummaryForPrompt(s Summary) string {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		// Shouldn't happen for our types; fall back to a terse form.
		return fmt.Sprintf("(failed to render: %v)", err)
	}
	return "```json\n" + string(data) + "\n```\n"
}
