package session

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/useragent"
)

const (
	summarizePath    = "/api/v1/session/summarize"
	summarizeTimeout = 30 * time.Second
)

// SummaryPromptGuidelines contains the shared guidelines for session summarization.
// Used by both the resummary command and should match the server-side prompt.
const SummaryPromptGuidelines = `## Output Format

Create a JSON object with this structure:

{
  "title": "Short descriptive title (5-10 words)",
  "summary": "One paragraph executive summary describing what was accomplished",
  "key_actions": [
    "Action 1 that was taken",
    "Action 2 that was taken"
  ],
  "outcome": "success|partial|failed",
  "topics_found": ["topic1", "topic2"],
  "diagrams": ["mermaid diagram code if any were created"],
  "aha_moments": [
    {
      "seq": 7,
      "role": "user|assistant",
      "type": "question|insight|decision|breakthrough|synthesis",
      "highlight": "The exact quote or key text from this moment",
      "why": "Brief explanation of why this was a pivotal moment"
    }
  ],
  "sageox_insights": [
    {
      "seq": 12,
      "topic": "react-patterns",
      "insight": "What SageOx guidance was applied",
      "impact": "The outcome or value it provided"
    }
  ]
}

## Aha Moments Guidelines

Identify **3-5 pivotal moments** where collaborative intelligence emerged.
Less is better - only capture truly impactful moments.

**IMPORTANT**: Human questions and insights are often MORE valuable than AI insights.
When a human asks a thoughtful question that redirects the conversation toward a better outcome,
that's a key moment. Prioritize capturing human contributions when they're insightful.

Types of moments:
- **question**: A question (often from human) that unlocked a better direction
- **insight**: A realization that changed the approach
- **decision**: A key architectural or design decision
- **breakthrough**: Solving a blocking problem
- **synthesis**: Combining ideas into something better

The seq field should match the message sequence number. The role is "user" or "assistant".

## SageOx Insights Guidelines

Identify moments where **SageOx guidance** provided unique value. Look for explicit attributions:
- "Based on SageOx domain guidance..."
- "SageOx's team pattern suggests..."
- "Following SageOx best practices for..."
- "SageOx guidance on [topic] indicates..."

For each insight, capture:
- **seq**: Message number where the insight was applied
- **topic**: Domain area (e.g., "react-patterns", "api-design", "testing")
- **insight**: What guidance was applied
- **impact**: The value it provided (avoided mistakes, saved time, better architecture)

Only include moments where SageOx guidance demonstrably improved the outcome.
If no SageOx attributions are present in the session, leave sageox_insights empty.
`

// SummarizeRequest contains the session data to summarize.
type SummarizeRequest struct {
	AgentID   string           `json:"agent_id"`
	AgentType string           `json:"agent_type"`
	Model     string           `json:"model,omitempty"`
	Entries   []SummarizeEntry `json:"entries"`
}

// SummarizeEntry is a simplified entry for summarization.
type SummarizeEntry struct {
	Type      string `json:"type"`
	Content   string `json:"content"`
	ToolName  string `json:"tool_name,omitempty"`
	Timestamp string `json:"timestamp,omitempty"`
}

// AhaMoment captures a pivotal point in the conversation where key insight emerged.
// These moments document collaborative intelligence - the interplay between
// human intuition/direction and AI exploration/synthesis.
type AhaMoment struct {
	Seq       int    `json:"seq"`       // message sequence number for navigation
	Role      string `json:"role"`      // user, assistant, or system
	Type      string `json:"type"`      // question, insight, decision, breakthrough, synthesis
	Highlight string `json:"highlight"` // the key text/quote from this moment
	Why       string `json:"why"`       // brief explanation of why this was important
}

// SageoxInsight captures moments where SageOx guidance provided unique value.
// These are explicitly attributed in the conversation using phrases like
// "Based on SageOx guidance..." and document the product's contribution.
type SageoxInsight struct {
	Seq     int    `json:"seq"`     // message sequence number for navigation
	Topic   string `json:"topic"`   // domain/topic area (e.g., "react-patterns", "api-design")
	Insight string `json:"insight"` // what guidance was applied
	Impact  string `json:"impact"`  // the outcome or value it provided
}

// SummarizeResponse contains the LLM-generated summary.
type SummarizeResponse struct {
	Title          string          `json:"title"`                     // short descriptive title for the session
	Summary        string          `json:"summary"`                   // one paragraph executive summary
	KeyActions     []string        `json:"key_actions"`               // bullet points of key actions taken
	Outcome        string          `json:"outcome"`                   // success/partial/failed
	TopicsFound    []string        `json:"topics_found"`              // topics detected during session
	FinalPlan      string          `json:"final_plan,omitempty"`      // final plan/architecture from session
	Diagrams       []string        `json:"diagrams,omitempty"`        // extracted mermaid diagrams
	AhaMoments     []AhaMoment     `json:"aha_moments,omitempty"`     // pivotal moments of collaborative intelligence
	SageoxInsights []SageoxInsight `json:"sageox_insights,omitempty"` // moments where SageOx guidance provided value
}

// Summarize calls the SageOx API to generate an LLM summary of a session.
// If endpointURL is non-empty, uses that endpoint for auth and API calls;
// otherwise falls back to the default endpoint.
// Returns nil if summarization fails (non-critical feature).
func Summarize(entries []Entry, agentID, agentType, model, endpointURL string) (*SummarizeResponse, error) {
	var token *auth.StoredToken
	var err error
	if endpointURL != "" {
		token, err = auth.GetTokenForEndpoint(endpointURL)
	} else {
		token, err = auth.GetToken()
	}
	if err != nil || token == nil {
		return nil, fmt.Errorf("authentication required for summarization")
	}

	// prepare request with limited entries
	req := buildSummarizeRequest(entries, agentID, agentType, model)

	// make API call
	client := &http.Client{Timeout: summarizeTimeout}
	var reqURL string
	if endpointURL != "" {
		reqURL = endpointURL + summarizePath
	} else {
		reqURL = endpoint.Get() + summarizePath
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequest("POST", reqURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+token.AccessToken)
	httpReq.Header.Set("User-Agent", useragent.String())

	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("API request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API error: %d %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	var result SummarizeResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return &result, nil
}

// buildSummarizeRequest converts entries to the API request format.
func buildSummarizeRequest(entries []Entry, agentID, agentType, model string) *SummarizeRequest {
	req := &SummarizeRequest{
		AgentID:   agentID,
		AgentType: agentType,
		Model:     model,
		Entries:   make([]SummarizeEntry, 0, len(entries)),
	}

	for _, e := range entries {
		se := SummarizeEntry{
			Type:     string(e.Type),
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

// BuildSummaryPrompt builds a prompt for the calling agent to generate a session summary.
// The agent receives this prompt in the JSON output and produces the summary itself,
// avoiding a server-side API call.
// If ledgerSessionDir is non-empty, a step is added instructing the agent to push the
// summary to the ledger via `ox session push-summary`.
func BuildSummaryPrompt(entries []Entry, rawPath, ledgerSessionDir string) string {
	var sb strings.Builder

	sb.WriteString("# Summarize Session\n\n")
	sb.WriteString("Analyze the following session and generate a summary JSON object.\n\n")

	// shared guidelines
	sb.WriteString(SummaryPromptGuidelines)
	sb.WriteString("\n")

	// reference the raw session file on disk instead of embedding all entries
	// (the agent already has the session in its context window)
	sb.WriteString("## Session to Analyze\n\n")
	fmt.Fprintf(&sb, "Read the session recording at: `%s`\n\n", rawPath)
	fmt.Fprintf(&sb, "The file is JSONL format with %d entries. Each line is a JSON object with `type`, `content`, and optional `tool_name` fields.\n\n", len(entries))

	// save-to path
	summaryPath := filepath.Join(filepath.Dir(rawPath), "summary.json")
	sb.WriteString("## Instructions\n\n")
	sb.WriteString("1. Read the session recording file at the path above\n")
	sb.WriteString("2. Identify the main goal and what was accomplished\n")
	sb.WriteString("3. Find the pivotal aha moments (questions, insights, decisions)\n")
	sb.WriteString("4. Generate the JSON with all required fields from the Output Format above\n")
	fmt.Fprintf(&sb, "5. Save to: `%s`\n", summaryPath)

	// if ledger session dir is available, add push instruction
	if ledgerSessionDir != "" {
		fmt.Fprintf(&sb, "6. Push summary to ledger by running: `ox session push-summary --file %s --session-dir %s`\n", summaryPath, ledgerSessionDir)
	}

	return sb.String()
}

// localSummaryTopicMaxLen is the max runes to keep from the first user message
// when extracting a topic hint for the local summary.
const localSummaryTopicMaxLen = 120

// LocalSummary generates a simple local summary without API call.
// Used as fallback when API is unavailable.
// Extracts the first substantive user message as a topic hint so the summary
// conveys what the session was about, not just stats.
func LocalSummary(entries []Entry) string {
	if len(entries) == 0 {
		return "Empty session"
	}

	// count message types and find first substantive user message
	var userCount, assistantCount, toolCount int
	var tools []string
	toolSet := make(map[string]bool)
	var firstUserMsg string

	for _, e := range entries {
		switch e.Type {
		case EntryTypeUser:
			userCount++
			if firstUserMsg == "" {
				msg := strings.TrimSpace(e.Content)
				if len(msg) > 0 {
					firstUserMsg = msg
				}
			}
		case EntryTypeAssistant:
			assistantCount++
		case EntryTypeTool:
			toolCount++
			if e.ToolName != "" && !toolSet[e.ToolName] {
				toolSet[e.ToolName] = true
				tools = append(tools, e.ToolName)
			}
		}
	}

	var sb strings.Builder

	// lead with topic hint from first user message
	if firstUserMsg != "" {
		topic := extractTopicHint(firstUserMsg)
		if topic != "" {
			sb.WriteString(topic)
			sb.WriteString("\n\n")
		}
	}

	// stats line
	var parts []string
	parts = append(parts, fmt.Sprintf("%d user messages, %d assistant responses", userCount, assistantCount))
	if toolCount > 0 {
		parts = append(parts, fmt.Sprintf("%d tool calls", toolCount))
	}
	if len(tools) > 0 {
		if len(tools) > 5 {
			parts = append(parts, fmt.Sprintf("Tools: %s, and %d more", strings.Join(tools[:5], ", "), len(tools)-5))
		} else {
			parts = append(parts, fmt.Sprintf("Tools: %s", strings.Join(tools, ", ")))
		}
	}
	sb.WriteString(strings.Join(parts, ". "))

	return sb.String()
}

// extractTopicHint takes the first user message and returns a concise topic line.
// Strips markdown headers, takes only the first line/sentence, and truncates.
func extractTopicHint(msg string) string {
	// take first non-empty line
	var line string
	for _, l := range strings.Split(msg, "\n") {
		l = strings.TrimSpace(l)
		// skip markdown headers and empty lines
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		line = l
		break
	}
	if line == "" {
		return ""
	}

	// truncate to max runes, preserving word boundaries
	runes := []rune(line)
	if len(runes) > localSummaryTopicMaxLen {
		// find last space before limit
		truncAt := localSummaryTopicMaxLen
		for i := localSummaryTopicMaxLen; i > localSummaryTopicMaxLen/2; i-- {
			if runes[i] == ' ' {
				truncAt = i
				break
			}
		}
		line = string(runes[:truncAt]) + "\u2026"
	}

	return line
}
