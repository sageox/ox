package sessionsummary

import (
	"time"

	"github.com/sageox/ox/internal/session/contextuse"
)

// Entry type constants.
const (
	EntryTypeUser      = "user"
	EntryTypeAssistant = "assistant"
	EntryTypeSystem    = "system"
	EntryTypeTool      = "tool"
)

// SummaryStatus values describe the lifecycle state of a session's
// summary. Consumers should prefer this structured signal over sniffing
// for sentinel strings in the user-visible Summary/Title fields.
//
// Older ledgers may have an empty SummaryStatus — readers MUST treat
// empty as "unknown / pre-status" and fall back to the existing logic
// (e.g. Summary populated → ok, Summary empty → pending).
const (
	// SummaryStatusOK means the LLM-generated summary passed all
	// content + richness validation and is the source of truth for
	// the user-visible Title / Summary fields.
	SummaryStatusOK = "ok"

	// SummaryStatusPending means the summary has not been generated
	// yet (e.g. session stop has finished but push-summary or the
	// daemon-side runner has not yet completed).
	SummaryStatusPending = "pending"

	// SummaryStatusFailedValidation means the LLM produced output but
	// validators rejected it. ValidationError carries the diagnostic
	// for ops; user-visible fields stay empty so the UI never displays
	// the error string as if it were the session's title.
	SummaryStatusFailedValidation = "failed_validation"

	// SummaryStatusUnrecoverable means the summary cannot be produced
	// for this session at all (e.g. session has no substantive turns,
	// raw.jsonl is corrupt). User-visible fields stay empty.
	SummaryStatusUnrecoverable = "unrecoverable"
)

// QualityCategory values describe the LLM-judged team-value of a session.
// Categorical (not numeric) because as of April 2026 best practices on LLM
// rubric scoring: numeric scales cluster on round numbers and ignore the
// fine-grained distinctions they pretend to capture; LLMs are far more
// reliable at picking from a small named set with anchored examples.
//
// Two categories matching session.QualityDisposition 1:1 — adding more
// categories invites the same fine-distinction problem numeric had. The
// rubric in the summarization prompt anchors each category with concrete
// examples so the LLM can place a session unambiguously.
//
// Older summaries on the ledger may have empty QualityCategory and
// non-zero QualityScore. Readers MUST treat empty as "category unknown,
// fall back to numeric score via session.EvaluateQuality" — see
// EvaluateQualityCategory for the canonical mapping.
const (
	// QualityCategorySkip — the session has no content worth recording
	// at all. Routine maintenance, abandoned attempts, single-Q&A
	// pings with no decisions or learnings. Maps to QualityDiscard.
	// When the LLM emits this category it MAY also emit a minimal
	// skip-shape (no title/summary/key_actions/...) — saves output
	// tokens that would otherwise produce a stub teammates can't use.
	QualityCategorySkip = "skip"

	// QualityCategoryShare — substantive work or useful context, including
	// routine implementation, fixes, configuration, and refactoring as well
	// as architectural decisions and root-cause analyses. Maps to QualityUpload.
	QualityCategoryShare = "share"
)

// IsSkipCategory reports whether resp's quality category indicates the
// LLM (or prefilter) decided this session should not be summarized.
// Centralized so callers don't sniff the literal string in five places.
func IsSkipCategory(resp *SummarizeResponse) bool {
	return resp != nil && resp.QualityCategory == QualityCategorySkip
}

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
	//
	// QualityCategory is the canonical quality signal as of 2026-05.
	// Values: QualityCategorySkip and QualityCategoryShare. See those constants for rubric.
	// Empty on legacy summaries — readers fall back to QualityScore.
	QualityCategory string `json:"quality_category,omitempty"`
	// QualityScore is the legacy 0.0-1.0 numeric value. Retained for
	// backward compatibility with existing summary.json files on the
	// ledger; new summaries do NOT populate this. Numeric scoring is
	// against April 2026 best practices for LLM rubric evaluation —
	// see QualityCategory.
	QualityScore float64 `json:"quality_score,omitempty"`
	ScoreReason  string  `json:"score_reason,omitempty"` // brief explanation of the quality decision

	// SageOx contribution (injected from cache file, not LLM-generated)
	SageoxScore         *float64 `json:"sageox_score,omitempty"`          // 0.0-1.0 self-reported contribution score
	SageoxScoreCategory string   `json:"sageox_score_category,omitempty"` // named category: none, minor, moderate, significant, critical
	SageoxScoreReason   string   `json:"sageox_score_reason,omitempty"`   // detailed paragraph explaining SageOx influence

	// ContextUse mirrors meta.json's context_use: the surfaced-vs-used rollup
	// derived from the session's context trace at finalize (refs only, never
	// content). Injected from meta.json by push-summary, never LLM-generated.
	ContextUse *contextuse.Rollup `json:"context_use,omitempty"`

	// Lifecycle / failure signals.
	//
	// SummaryStatus is the canonical structured signal for "did summary
	// generation succeed?". See SummaryStatus* constants. Readers should
	// prefer this over sniffing the Summary string for known failure
	// prefixes ("Summary failed content validation: ...") — that
	// sniffing pattern is what motivated the ox-qqka fix.
	//
	// ValidationError, when populated, carries the *ops-facing* diagnostic
	// from ValidateSummaryContent / ValidateSummaryRichness. It MUST NEVER
	// be displayed as a user-visible session title or summary; the UI
	// should treat it as engineer-visible only.
	//
	// Both are omitempty so older readers (and older meta.json on disk)
	// keep working without migration.
	SummaryStatus   string `json:"summary_status,omitempty"`
	ValidationError string `json:"validation_error,omitempty"`
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
	ID          int            `json:"id"`                     // 1-based chapter number
	Title       string         `json:"title"`                  // LLM or heuristic title
	StartSeq    int            `json:"start_seq"`              // first message seq in this chapter
	EndSeq      int            `json:"end_seq"`                // last message seq in this chapter
	ToolCounts  map[string]int `json:"tool_counts,omitempty"`  // aggregated tool usage {"Read": 5, "Edit": 3}
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
