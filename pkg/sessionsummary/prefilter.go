package sessionsummary

import (
	"fmt"
	"strings"
)

// Prefilter thresholds. Tuned conservatively — the goal is to skip
// sessions whose LLM-generated summary would be hallucinated, not to
// aggressively skip anything that *might* be thin. Err on the side of
// letting the LLM try; the existing quality gate (EvaluateQuality with
// thresholds at 0.1 / 0.3) will discard or local-only the result if the
// LLM correctly assesses it as low value.
//
// Tighten these only with telemetry data showing the prefilter's skip
// rate stays well below the LLM's "quality_score < 0.3" rate (i.e. we
// only skip what the LLM was going to discard anyway).
const (
	// minMeaningfulEntries is the floor below which any summary would be
	// guesswork. A real interaction has at minimum: 1 user prompt + 1
	// assistant response, so the floor is 2 entries.
	//
	// We do NOT raise this to 3 to filter "user + tool, no assistant"
	// sessions: that case is already caught by the silent-user heuristic
	// (`!hasAssistant` flips `silent=true` with the same effect). Setting
	// the floor to 3 would over-fire on legitimate single-exchange Q&A
	// sessions — 1 user prompt + 1 substantive assistant response that
	// happens to land under-budget by entry count but contains real
	// content. Per CodeRabbit on PR #583: a 2-entry user→assistant
	// session can be valuable; let the LLM (and later quality gate) judge.
	minMeaningfulEntries = 2

	// minUserContentChars is the floor on combined user-turn length. A
	// user typing "ok" or pressing enter to dismiss a hook starts a
	// session technically but contributes no intent worth summarizing.
	// 80 chars = roughly one short sentence; below that, the conversation
	// is unlikely to produce anything beyond what the user literally
	// typed (which we can echo deterministically without an LLM).
	minUserContentChars = 80

	// maxPromptsToEcho is the upper bound on how many user prompts we
	// echo into the deterministic skip-summary. Past this we just use
	// the first one — at that point the session is long enough that
	// echoing all prompts produces a meandering paragraph, not a summary.
	// (And if the session is THAT long, it probably won't trigger the
	// prefilter at all because of the entry-count threshold.)
	maxPromptsToEcho = 3
)

// MaybeBuildSkipSummary inspects a session's entries and, if the session
// is too thin to produce a meaningful LLM-generated summary, returns a
// deterministic stand-in summary built from user prompts. The boolean
// return is true when the LLM call should be skipped.
//
// # Why this exists
//
// At session-stop scale, a non-trivial fraction of sessions are
// exploratory pings: the user runs `ox agent session start` and asks one
// quick question, the agent answers it, the session ends. There is no
// architecture, no decision, no aha moment — just a Q&A that doesn't
// belong in the team ledger. Today every such session pays the full
// summarization cost: the prompt template's ~5KB of guidelines, the
// optimized session bytes, the Haiku call, the validation pass — all to
// produce an LLM summary that the quality gate then routes to "discard"
// or "local-only" anyway.
//
// This function short-circuits that path for sessions whose schema
// fields (chapter_titles, decisions, aha_moments) cannot meaningfully
// be filled even by a perfect LLM. Saves the entire LLM round-trip.
//
// # Detection heuristics (intentionally conservative)
//
//   - Entry count < minMeaningfulEntries: not enough conversation to
//     have a narrative arc.
//   - No assistant entries: the agent never responded, so there's
//     nothing to summarize beyond the user's question.
//   - Combined user content < minUserContentChars: user contributed
//     too little intent for any summary to capture more than what was
//     literally typed.
//
// All three are AND-equivalent in spirit (a session has to fail just
// ONE to be flagged) — a session can be thin in any dimension and still
// not produce useful output. We don't require all three.
//
// # The deterministic summary
//
// When skipping, we synthesize a SummarizeResponse where:
//   - Title is a generic "Brief session" marker.
//   - Summary echoes the user's first prompt, or all prompts up to
//     maxPromptsToEcho if there are few. This preserves the actual
//     intent the user expressed without LLM-fabricated commentary.
//   - QualityScore is set to 0.05, well below the discard threshold
//     (0.1 by default) — so the existing quality gate will route this
//     to QualityDiscard via session.EvaluateQuality. The session does
//     NOT reach the team ledger. This is intentional: low-value
//     sessions never had a place in the team's shared knowledge anyway,
//     and the prefilter just makes that judgment cheaper.
//   - ScoreReason explains what happened so the local consumer
//     (`ox session view`) shows the user *why* their session got the
//     stub treatment — not a mystery.
//   - SummaryStatus is set to SummaryStatusOK because the summary IS
//     a valid one, just deterministic. Distinguishing prefilter-stub
//     from LLM-stub matters for telemetry segmentation: a high
//     prefilter rate is a signal of healthy cost optimization; a high
//     LLM-stub rate is a signal of LLM trouble.
//
// # What it does NOT do
//
// It does NOT touch the user's intent semantics with heuristics — no
// stripping, no rephrasing, no synthesis. Echo only. The LLM is the
// only thing in this codebase allowed to write summary prose, and when
// it's not running we don't fake it.
func MaybeBuildSkipSummary(entries []Entry) (*SummarizeResponse, bool) {
	if len(entries) == 0 {
		return nil, false // empty session is handled elsewhere; not our case
	}

	userPrompts := collectUserPrompts(entries)
	totalUserContent := totalLen(userPrompts)
	hasAssistant := hasEntryOfType(entries, EntryTypeAssistant)

	// The session is "low-value" if any of these floor conditions hits.
	// Each captures a distinct failure mode where LLM summary would
	// either hallucinate or echo the trivial input.
	thin := len(entries) < minMeaningfulEntries
	silent := !hasAssistant
	terseUser := totalUserContent < minUserContentChars

	if !thin && !silent && !terseUser {
		// Not low-value by any criterion — let the LLM produce a real summary.
		return nil, false
	}

	// Build the deterministic summary from user prompts. This is the
	// only place in the codebase that synthesizes summary prose without
	// an LLM; the rationale is documented above. If userPrompts is
	// empty we still return a stub with a placeholder so the caller has
	// a valid SummarizeResponse to write — better than crashing the
	// finalize flow over an edge case.
	summary := buildPromptEcho(userPrompts)
	scoreReason := buildSkipReason(thin, silent, terseUser, len(userPrompts))

	return &SummarizeResponse{
		Title:           "Brief session",
		Summary:         summary,
		KeyActions:      nil, // intentionally empty — no LLM means no inferred actions
		Outcome:         "",  // unknown without LLM analysis
		TopicsFound:     nil, // see KeyActions
		QualityCategory: QualityCategorySkip,
		ScoreReason:     scoreReason,
		SummaryStatus:   SummaryStatusOK, // the deterministic summary IS valid; it's just not LLM-generated
	}, true
}

// collectUserPrompts returns the trimmed content of every user-typed
// entry. Order preserved (chronological) so the echo summary reads in
// the order the user typed.
func collectUserPrompts(entries []Entry) []string {
	out := make([]string, 0, 4)
	for _, e := range entries {
		if e.Type != EntryTypeUser {
			continue
		}
		c := strings.TrimSpace(e.Content)
		if c == "" {
			continue
		}
		out = append(out, c)
	}
	return out
}

func totalLen(strs []string) int {
	n := 0
	for _, s := range strs {
		n += len(s)
	}
	return n
}

func hasEntryOfType(entries []Entry, t string) bool {
	for _, e := range entries {
		if e.Type == t {
			return true
		}
	}
	return false
}

// buildPromptEcho composes the deterministic summary text from the
// collected user prompts. Format choice: a leading sentence framing
// the echo, then the prompts. The leading sentence is critical — without
// it the summary reads like a verbatim user prompt, which is confusing
// to teammates browsing the ledger.
func buildPromptEcho(prompts []string) string {
	if len(prompts) == 0 {
		return "Session was too short for a meaningful summary; no user prompt was recorded."
	}

	if len(prompts) == 1 {
		return fmt.Sprintf("Session was too short for a meaningful summary. The user's prompt was: %q", prompts[0])
	}

	if len(prompts) <= maxPromptsToEcho {
		var sb strings.Builder
		sb.WriteString("Session was too short for a meaningful summary. The user's prompts were:")
		for _, p := range prompts {
			sb.WriteString("\n  - ")
			sb.WriteString(p)
		}
		return sb.String()
	}

	// More than maxPromptsToEcho — echo only the first one and note the
	// rest. Sessions hitting this branch have a lot of user input but
	// failed some other low-value criterion (e.g. zero assistant entries,
	// which usually means the agent crashed early). Echoing all prompts
	// would produce a wall of text; the first one captures the original
	// intent.
	return fmt.Sprintf(
		"Session was too short for a meaningful summary. The user's first prompt was: %q (plus %d more).",
		prompts[0], len(prompts)-1,
	)
}

// buildSkipReason produces a human-readable score_reason explaining
// why the prefilter triggered. Ops needs this to distinguish "we saved
// money on a thin session" from "the summarizer is broken." Multiple
// criteria can fire at once; we list them all so the diagnostic is
// honest.
func buildSkipReason(thin, silent, terseUser bool, promptCount int) string {
	var reasons []string
	if thin {
		reasons = append(reasons, fmt.Sprintf("fewer than %d entries", minMeaningfulEntries))
	}
	if silent {
		reasons = append(reasons, "no assistant response")
	}
	if terseUser {
		reasons = append(reasons, fmt.Sprintf("user content under %d chars", minUserContentChars))
	}
	if len(reasons) == 0 {
		// Defensive — caller should have early-returned, but fall through gracefully.
		reasons = append(reasons, "low-value heuristics matched")
	}
	return fmt.Sprintf(
		"prefilter skip — %s; %d user prompt(s) echoed; LLM summarization skipped to save tokens",
		strings.Join(reasons, ", "),
		promptCount,
	)
}
