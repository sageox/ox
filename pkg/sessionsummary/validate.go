package sessionsummary

import (
	"fmt"
	"strings"
)

// Richness thresholds — a summary missing these on a non-trivial session
// should be rejected rather than silently shipped. Output tokens cost
// nothing compared to the input tokens the LLM already paid to ingest
// the whole session, so there's no reason to accept skeletal summaries.
//
// Non-trivial session = entry_count > RichnessMinEntries. Below that
// threshold (e.g., a 10-entry session that was just a quick question),
// richness is optional because there genuinely may not be aha_moments
// or multiple key_actions to capture.
const (
	RichnessMinEntries      = 20 // sessions below this are "trivial" — only structural checks apply
	RichnessMinKeyActions   = 3  // minimum key_actions bullets on a non-trivial session
	RichnessAhaMinEntries   = 50 // sessions above this must have aha_moments (long-enough for pivotal turns)
	RichnessMinSummaryChars = 80 // one paragraph ~= 80+ chars of actual content, catches "Session ended." stubs
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

// ValidateSummaryRichness rejects summaries that lack the rich fields
// BuildSummaryPrompt explicitly requests. Applied IN ADDITION TO
// ValidateSummaryContent on non-trivial sessions (entry_count >
// RichnessMinEntries).
//
// Rationale: input-token cost is already paid when the LLM ingests the
// session; output tokens for a few bullets and aha_moments are effectively
// free. A summary consisting of {title, summary, outcome} and nothing
// else is a regression from the prompt's own schema — reject it so the
// caller can retry instead of silently shipping a degraded artifact.
//
// Trivial sessions (short conversations, one-turn questions) skip
// richness checks because there genuinely may not be 3 key_actions or
// pivotal moments to capture.
//
// Returns nil if rich enough, or a descriptive error naming the missing
// field(s) so callers can route to retry / tune / alert.
func ValidateSummaryRichness(resp *SummarizeResponse, entryCount int) error {
	if resp == nil {
		return fmt.Errorf("nil summary response")
	}
	if entryCount <= RichnessMinEntries {
		return nil // trivial session — no richness requirement
	}

	// Summary body should be a real paragraph, not "Session stopped."
	if len(strings.TrimSpace(resp.Summary)) < RichnessMinSummaryChars {
		return fmt.Errorf("summary too short for a session with %d entries (got %d chars, need %d)",
			entryCount, len(strings.TrimSpace(resp.Summary)), RichnessMinSummaryChars)
	}

	// Key actions spine — any real work produces 3+ discrete actions.
	nonEmptyActions := 0
	for _, a := range resp.KeyActions {
		if strings.TrimSpace(a) != "" {
			nonEmptyActions++
		}
	}
	if nonEmptyActions < RichnessMinKeyActions {
		return fmt.Errorf("key_actions missing or under-populated (got %d non-empty bullets, need ≥%d on a %d-entry session)",
			nonEmptyActions, RichnessMinKeyActions, entryCount)
	}

	// Aha moments on longer sessions — pivotal turns happen in extended work.
	if entryCount > RichnessAhaMinEntries && len(resp.AhaMoments) == 0 {
		return fmt.Errorf("aha_moments missing on a %d-entry session (need ≥1; pivotal decisions always happen in extended work)", entryCount)
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
