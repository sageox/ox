package sessionsummary

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// ParseSummaryJSON extracts a SummarizeResponse from LLM output text.
// The output may contain the JSON as raw text, inside ```json fences,
// or inside generic ``` fences.
//
// Acceptance gate: requires a parsed object whose Title field is
// non-whitespace AND non-zero KeyActions. A whitespace-only title
// (e.g. " " or "\n") is rejected — without that gate, claude's
// shell-out narration in the daemon path can produce incidental
// fenced text that round-trips to a parse-success but fails
// downstream ValidateSummaryContent with "title too short (0 chars)",
// putting the session into a permanent failure-stub state. Returning
// the parse error here funnels the caller into the unparsable-output
// branch, which retries instead of writing a sticky stub.
func ParseSummaryJSON(output string) (*SummarizeResponse, error) {
	// try raw JSON first. Each branch uses a fresh resp so a partial
	// unmarshal from an earlier failed pass cannot bleed into a later one.
	var resp SummarizeResponse
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &resp); err == nil && summaryHasContent(&resp) {
		return &resp, nil
	}

	// try extracting from ```json ... ``` fences
	if idx := strings.Index(output, "```json"); idx >= 0 {
		start := idx + len("```json")
		if end := strings.Index(output[start:], "```"); end >= 0 {
			jsonStr := strings.TrimSpace(output[start : start+end])
			var fenced SummarizeResponse
			if err := json.Unmarshal([]byte(jsonStr), &fenced); err == nil && summaryHasContent(&fenced) {
				return &fenced, nil
			}
		}
	}

	// try extracting from generic ``` ... ``` fences
	if idx := strings.Index(output, "```"); idx >= 0 {
		start := idx + len("```")
		// skip to newline if present (e.g., ```\n{...}\n```)
		if nlIdx := strings.Index(output[start:], "\n"); nlIdx >= 0 {
			start += nlIdx + 1
		}
		if end := strings.Index(output[start:], "```"); end >= 0 {
			jsonStr := strings.TrimSpace(output[start : start+end])
			var fenced SummarizeResponse
			if err := json.Unmarshal([]byte(jsonStr), &fenced); err == nil && summaryHasContent(&fenced) {
				return &fenced, nil
			}
		}
	}

	return nil, fmt.Errorf("no valid summary JSON found in LLM output")
}

// summaryHasContent reports whether a parsed SummarizeResponse looks
// like a real LLM output rather than an empty shell or whitespace-only
// stub. Title must trim to non-empty AND key_actions must have at
// least one entry — both fields are explicitly requested by
// BuildSummaryPrompt, so their absence indicates the LLM did not
// actually summarize.
//
// Skip-shape exception (mirrors ValidateSummaryContent's skip contract):
// when the LLM judges a session quality_category "skip", it legitimately
// has no key_actions — nothing was accomplished — and may carry only a
// title and score_reason. Rejecting that shape here funneled every skip
// verdict into the unparsable-output branch, which fabricated a blank
// stub that then failed validation with "title too short (0 chars)" —
// a permanent, daily-retried failure-stub state for trivial sessions
// (the 2026-07 ledger anti-entropy incident). A skip response is
// accepted when it carries a non-empty score_reason or title, so
// incidental fenced text that merely round-trips the struct still
// fails the gate.
func summaryHasContent(r *SummarizeResponse) bool {
	if r.QualityCategory == QualityCategorySkip {
		return strings.TrimSpace(r.ScoreReason) != "" || strings.TrimSpace(r.Title) != ""
	}
	if strings.TrimSpace(r.Title) == "" {
		return false
	}
	for _, a := range r.KeyActions {
		if strings.TrimSpace(a) != "" {
			return true
		}
	}
	return false
}

// EntriesFromRaw converts []map[string]any (from StoredSession.Entries) to
// []Entry for use with BuildSummaryPrompt. Shared by the daemon's session
// finalization and the CLI's --summary regeneration.
func EntriesFromRaw(raw []map[string]any) []Entry {
	entries := make([]Entry, 0, len(raw))
	for _, m := range raw {
		e := Entry{}
		if t, ok := m["type"].(string); ok {
			e.Type = t
		}
		if c, ok := m["content"].(string); ok {
			e.Content = c
		}
		if tn, ok := m["tool_name"].(string); ok {
			e.ToolName = tn
		}
		if ti, ok := m["tool_input"].(string); ok {
			e.ToolInput = ti
		}
		if to, ok := m["tool_output"].(string); ok {
			e.ToolOutput = to
		}
		if ts, ok := m["timestamp"].(string); ok {
			if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
				e.Timestamp = t
			} else if t, err := time.Parse(time.RFC3339, ts); err == nil {
				e.Timestamp = t
			}
		}
		entries = append(entries, e)
	}
	return entries
}
