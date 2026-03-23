package sessionsummary

import "time"

// SummarizeRequest contains the session data to summarize.
type SummarizeRequest struct {
	AgentID   string           `json:"agent_id"`
	AgentType string           `json:"agent_type"`
	Model     string           `json:"model,omitempty"`
	Entries   []SummarizeEntry `json:"entries"`
}

// SummarizeEntry is a simplified entry for the summarization API.
type SummarizeEntry struct {
	Type      string `json:"type"`
	Content   string `json:"content"`
	ToolName  string `json:"tool_name,omitempty"`
	Timestamp string `json:"timestamp,omitempty"`
}

// BuildSummarizeRequest converts entries to the API request format.
// Filters out low-value exploratory tool calls to reduce noise for the LLM.
func BuildSummarizeRequest(entries []Entry, agentID, agentType, model string) *SummarizeRequest {
	filtered := FilterForSummarization(entries)

	req := &SummarizeRequest{
		AgentID:   agentID,
		AgentType: agentType,
		Model:     model,
		Entries:   make([]SummarizeEntry, 0, len(filtered)),
	}

	for _, e := range filtered {
		se := SummarizeEntry{
			Type:     e.Type,
			Content:  e.Content,
			ToolName: e.ToolName,
		}
		if !e.Timestamp.IsZero() {
			se.Timestamp = e.Timestamp.Format(time.RFC3339)
		}
		req.Entries = append(req.Entries, se)
	}

	return req
}
