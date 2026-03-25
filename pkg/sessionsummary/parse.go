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
func ParseSummaryJSON(output string) (*SummarizeResponse, error) {
	// try raw JSON first
	var resp SummarizeResponse
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &resp); err == nil && resp.Title != "" {
		return &resp, nil
	}

	// try extracting from ```json ... ``` fences
	if idx := strings.Index(output, "```json"); idx >= 0 {
		start := idx + len("```json")
		if end := strings.Index(output[start:], "```"); end >= 0 {
			jsonStr := strings.TrimSpace(output[start : start+end])
			if err := json.Unmarshal([]byte(jsonStr), &resp); err == nil && resp.Title != "" {
				return &resp, nil
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
			if err := json.Unmarshal([]byte(jsonStr), &resp); err == nil && resp.Title != "" {
				return &resp, nil
			}
		}
	}

	return nil, fmt.Errorf("no valid summary JSON found in LLM output")
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
