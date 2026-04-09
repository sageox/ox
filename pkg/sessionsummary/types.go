package sessionsummary

import "time"

// Entry type constants.
const (
	EntryTypeUser      = "user"
	EntryTypeAssistant = "assistant"
	EntryTypeSystem    = "system"
	EntryTypeTool      = "tool"
)

// Entry is a minimal session entry for summarization.
// Uses plain strings for the Type field (not an enum) so this package
// has no dependency on internal/session.
type Entry struct {
	Timestamp  time.Time `json:"ts"`
	Type       string    `json:"type"`
	Content    string    `json:"content"`
	ToolName   string    `json:"tool_name,omitempty"`
	ToolInput  string    `json:"tool_input,omitempty"`
	ToolOutput string    `json:"tool_output,omitempty"`
	IsError    bool      `json:"is_error,omitempty"`
}

// SummarizeResponse contains the LLM-generated summary plus computed metadata.
//
// Follows a dual-summary pattern: human-readable fields (Summary, KeyActions, Outcome)
// and structured agent data (AgentSummary) for machine consumption.
//
// The LLM produces: title, summary, key_actions, outcome, topics_found,
// chapter_titles, aha_moments, sageox_insights, agent_summary.
// The CLI computes and appends: chapters, files_changed.
type SummarizeResponse struct {
	// Identity
	Title    string `json:"title"`              // short descriptive title (5-10 words)
	Provider string `json:"provider,omitempty"` // LLM provider (e.g. "bedrock", "anthropic")

	// Human-readable
	Summary    string   `json:"summary"`     // one paragraph executive summary
	KeyActions []string `json:"key_actions"` // bullet points of key actions taken
	Outcome    string   `json:"outcome"`     // success/partial/failed

	// Structured agent data (for machine consumption)
	AgentSummary *AgentSummary `json:"agent_summary,omitempty"`

	// Session content
	TopicsFound    []string         `json:"topics_found"`              // topics detected during session
	FinalPlan      string           `json:"final_plan,omitempty"`      // final plan/architecture from session
	Diagrams       []string         `json:"diagrams,omitempty"`        // extracted mermaid diagrams
	ChapterTitles  []string         `json:"chapter_titles,omitempty"`  // LLM-generated narrative chapter titles
	Chapters       []ChapterSummary `json:"chapters,omitempty"`        // structured chapter data (computed from JSONL)
	FilesChanged   []FileSummary    `json:"files_changed,omitempty"`   // files modified during session (computed from JSONL)
	AhaMoments     []AhaMoment      `json:"aha_moments,omitempty"`     // pivotal moments of collaborative intelligence
	SageoxInsights []SageoxInsight  `json:"sageox_insights,omitempty"` // moments where SageOx guidance provided value

	// Quality gate
	QualityScore float64 `json:"quality_score"`          // 0.0-1.0 session value for team sharing
	ScoreReason  string  `json:"score_reason,omitempty"` // brief explanation of the quality score

	// SageOx contribution (injected from cache file, not LLM-generated)
	SageoxScore         *float64 `json:"sageox_score,omitempty"`          // 0.0-1.0 self-reported contribution score
	SageoxScoreCategory string   `json:"sageox_score_category,omitempty"` // named category: none, minor, moderate, significant, critical
	SageoxScoreReason   string   `json:"sageox_score_reason,omitempty"`   // detailed paragraph explaining SageOx influence
}

// AgentSummary contains structured data for AI agents to consume.
// Adopted from sageox-mono's recording summarization pattern where
// human narrative and machine-structured data are separated.
type AgentSummary struct {
	Decisions        []Decision        `json:"decisions,omitempty"`
	ActionItems      []ActionItem      `json:"action_items,omitempty"`
	OpenQuestions    []OpenQuestion    `json:"open_questions,omitempty"`
	TechnicalContext *TechnicalContext `json:"technical_context,omitempty"`
	Constraints      []string          `json:"constraints,omitempty"`
	NonGoals         []string          `json:"non_goals,omitempty"`
}

// Decision records an architectural or design decision made during the session.
type Decision struct {
	What  string `json:"what"`            // what was decided
	Why   string `json:"why,omitempty"`   // rationale
	Owner string `json:"owner,omitempty"` // who owns it
}

// ActionItem records a task identified during the session.
type ActionItem struct {
	Task     string `json:"task"`
	Assignee string `json:"assignee,omitempty"`
	Priority string `json:"priority,omitempty"` // high/medium/low
}

// OpenQuestion records an unresolved question from the session.
type OpenQuestion struct {
	Question string `json:"question"`
	Context  string `json:"context,omitempty"` // why it matters
}

// TechnicalContext captures the technical landscape of the session.
type TechnicalContext struct {
	Technologies []string `json:"technologies,omitempty"`
	Architecture []string `json:"architecture,omitempty"`
	Integrations []string `json:"integrations,omitempty"`
}

// AhaMoment captures a pivotal point in the conversation where key insight emerged.
// These moments document collaborative intelligence — the interplay between
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

// ChapterSummary is a structured chapter for summary.json.
// Computed from the raw JSONL by the grouping algorithm, enriched with
// LLM-generated titles when available.
type ChapterSummary struct {
	ID         int            `json:"id"`                    // 1-based chapter number
	Title      string         `json:"title"`                 // LLM or heuristic title
	StartSeq   int            `json:"start_seq"`             // first message seq in this chapter
	EndSeq     int            `json:"end_seq"`               // last message seq in this chapter
	ToolCounts map[string]int `json:"tool_counts,omitempty"` // aggregated tool usage {"Read": 5, "Edit": 3}
	TotalTools  int            `json:"total_tools"`            // total tool calls in chapter
	TotalErrors int            `json:"total_errors,omitempty"` // tool calls that failed
	HasEdits    bool           `json:"has_edits"`              // true if chapter contains file modifications
}

// FileSummary records a file modified during the session.
// Extracted from Edit/Write tool calls in the raw JSONL.
type FileSummary struct {
	Path    string `json:"path"`              // shortened file path
	Added   int    `json:"added"`             // lines added
	Removed int    `json:"removed,omitempty"` // lines removed
}
