package sessionsummary

import (
	"fmt"
	"strings"
)

// summaryRedFlags are substrings that indicate agent meta-output contamination
// in a session summary. These patterns match permission requests, tool call
// artifacts, sandbox errors, and self-referential agent text that should never
// appear in a meaningful session summary.
var summaryRedFlags = []struct {
	pattern string
	reason  string
}{
	// permission / approval requests
	{"approve the write", "permission request"},
	{"file write permissions", "permission request"},
	{"could you please grant", "permission request"},
	{"grant write access", "permission request"},
	{"permission to write", "permission request"},

	// tool call / XML artifacts
	{"</function_results>", "tool call artifact"},
	{"<function_calls>", "tool call artifact"},
	{"<invoke", "tool call artifact"},
	{"<parameter", "tool call artifact"},
	{"tool_use", "tool call artifact"},
	{"mcp__", "tool call artifact"},

	// sandbox / file system errors
	{"i cannot write to", "sandbox error"},
	{"i need to save the summary", "agent process leak"},
	{"ox-summary.json", "agent process leak"},
	{"push-summary", "agent process leak"},

	// self-referential agent process text
	{"let me summarize", "self-referential agent text"},
	{"here is the summary json", "self-referential agent text"},
	{"i'll generate the summary", "self-referential agent text"},
	{"i'll create the summary", "self-referential agent text"},
	{"let me read the session", "self-referential agent text"},
	{"let me analyze the session", "self-referential agent text"},
}

// titleRedFlags are substrings that should never appear in a summary title.
// Titles should be short descriptive phrases, not conversational text.
var titleRedFlags = []struct {
	pattern string
	reason  string
}{
	{"approve", "permission request in title"},
	{"permission", "permission request in title"},
	{"could you", "conversational text in title"},
	{"i need to", "self-referential text in title"},
	{"let me", "self-referential text in title"},
	{"unfortunately", "conversational text in title"},
	{"i apologize", "conversational text in title"},
}

// ValidateSummaryContent checks whether a SummarizeResponse contains a
// meaningful session summary vs. agent meta-output (permission requests,
// tool call artifacts, error messages, conversational responses).
//
// Returns nil if the summary looks valid, or a descriptive error if
// contamination is detected.
//
// # Known gap: richness is asked for but not required
//
// This validator only enforces the MINIMUM viable summary:
//   - Title (3–200 chars, no red flags)
//   - Summary (≥20 chars, no red flags)
//   - Outcome ∈ {success, partial, failed}
//
// The prompt in BuildSummaryPrompt asks agents for a MUCH richer shape:
// key_actions, aha_moments, sageox_insights, diagrams, chapter_titles,
// topics_found, agent_summary. The validator does NOT require any of
// these, so a summary consisting only of {title, summary, outcome}
// passes the gate. Consequence: agents/LLMs (especially when pressed
// for tokens or misparsing the request) frequently ship minimal
// summaries that lack the fields coworkers most want.
//
// Filed as ox-jxn6 — a follow-up should either (a) fail validation
// when aha_moments/key_actions are empty on a non-trivial session
// (entry_count > N), or (b) introduce a separate ValidateSummaryRichness
// that returns non-fatal warnings surfaced to the caller. Anti-entropy
// (internal/daemon/agentwork/session_finalize.go) uses this same
// validator, so the fix applies to both the ox-session-stop path and
// the daemon-driven background finalization.
func ValidateSummaryContent(resp *SummarizeResponse) error {
	if resp == nil {
		return fmt.Errorf("nil summary response")
	}

	// structural: title required
	title := strings.TrimSpace(resp.Title)
	if len(title) < 3 {
		return fmt.Errorf("title too short (%d chars, minimum 3)", len(title))
	}
	if len(title) > 200 {
		return fmt.Errorf("title too long (%d chars, maximum 200)", len(title))
	}

	// structural: summary required
	summary := strings.TrimSpace(resp.Summary)
	if len(summary) < 20 {
		return fmt.Errorf("summary too short (%d chars, minimum 20)", len(summary))
	}

	// structural: outcome must be valid
	switch resp.Outcome {
	case "success", "partial", "failed":
		// ok
	case "":
		return fmt.Errorf("outcome is empty (must be success, partial, or failed)")
	default:
		return fmt.Errorf("invalid outcome %q (must be success, partial, or failed)", resp.Outcome)
	}

	// heuristic: check title for red flags
	titleLower := strings.ToLower(title)
	for _, rf := range titleRedFlags {
		if strings.Contains(titleLower, rf.pattern) {
			return fmt.Errorf("title contains %s: %q", rf.reason, truncateStr(title, 80))
		}
	}

	// heuristic: check summary for red flags
	summaryLower := strings.ToLower(summary)
	for _, rf := range summaryRedFlags {
		if strings.Contains(summaryLower, rf.pattern) {
			return fmt.Errorf("summary contains %s: %q", rf.reason, truncateStr(summary, 120))
		}
	}

	return nil
}

// truncateStr truncates s to maxLen runes, appending "..." if truncated.
func truncateStr(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen]) + "..."
}
