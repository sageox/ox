package html

import (
	"html/template"
	"time"

	"github.com/sageox/ox/internal/theme"
)

// TemplateData is the root view model for the HTML template.
type TemplateData struct {
	Title          string
	Summary        *SummaryView // LLM-generated summary (may be nil)
	Metadata       *MetadataView
	Messages       []MessageView       // flat message list (kept for backward compat)
	Chapters       []ChapterView       // grouped conversation view (preferred for rendering)
	FilesChanged   []FileChangeView    // files modified during the session
	AhaMoments     []AhaMomentView     // pivotal moments of collaborative intelligence
	SageoxInsights []SageoxInsightView // moments where SageOx guidance provided value
	Statistics     *StatsView
	BrandColors    BrandColors
	Styles         template.CSS // CSS content (safe)
	Scripts        template.JS  // JS content (safe)
}

// AhaMomentView represents a pivotal conversation moment for display.
// Documents collaborative intelligence between human and AI.
type AhaMomentView struct {
	Seq       int    // message sequence number for navigation
	Role      string // user, assistant, system
	Type      string // question, insight, decision, breakthrough, synthesis
	Highlight string // the key text/quote
	Why       string // why this moment was important
}

// SageoxInsightView represents a moment where SageOx guidance provided value.
type SageoxInsightView struct {
	Seq     int    // message sequence number for navigation
	Topic   string // domain area (e.g., "react-patterns")
	Insight string // what guidance was applied
	Impact  string // the value it provided
}

// SummaryView holds LLM-generated session summary for display.
type SummaryView struct {
	Text           string              // one paragraph executive summary
	KeyActions     []string            // bullet points of key actions taken
	Outcome        string              // success/partial/failed
	TopicsFound    []string            // topics detected during session
	FinalPlan      string              // final plan/architecture from session
	Diagrams       []string            // extracted mermaid diagrams (raw mermaid code)
	ChapterTitles  []string            // LLM-generated chapter titles for narrative sections
	SageoxInsights []SageoxInsightView // moments where SageOx guidance provided value
}

// MessageView represents a single session entry for display.
type MessageView struct {
	ID          int
	Type        string // user, assistant, system, tool
	SenderLabel string // display name (e.g., "user", "assistant")
	Timestamp   time.Time
	Content     template.HTML // markdown rendered to HTML via goldmark (server-side)
	ToolCall    *ToolCallView
	IsAhaMoment bool           // true if this message is a key insight moment
	AhaMomentID int            // aha moment index (1-based) for navigation
	AhaMoment   *AhaMomentView // full aha moment details for inline display
}

// ToolCallView holds tool invocation details for display.
type ToolCallView struct {
	Name           string
	Summary        string        // compact summary like "Edit(file.go) -- +5 / -3 lines"
	FormattedInput template.HTML // pre-rendered compact command (e.g., ">_ Bash git status")
	IsSimple       bool          // true = render inline (no collapsible details)
	Input          string
	Output         string
}

// MetadataView holds session metadata for display.
type MetadataView struct {
	AgentType    string
	AgentVersion string
	Model        string
	Username     string
	StartedAt    time.Time
	EndedAt      time.Time
}

// StatsView holds session statistics for display.
type StatsView struct {
	TotalMessages int
	UserMessages  int
	ToolMessages  int    // count of tool call entries
	FilesChanged  int    // count of distinct files modified
	Duration      string // formatted session duration (e.g., "45m", "1h 23m")
}

// WorkBlockView groups consecutive tool/system calls between conversation turns.
// Renders as a collapsed summary like "12 tool calls (Read 5, Grep 4, Bash 3)".
type WorkBlockView struct {
	Messages   []MessageView  // the tool/system messages in this block
	ToolCounts map[string]int // tool name -> count (e.g., {"Read": 5, "Grep": 4})
	Summary    string         // formatted summary text
	TotalTools int            // total tool call count
	HasEdits   bool           // true if block contains Edit/Write/MultiEdit calls
}

// FileChangeView represents a file modified during the session.
type FileChangeView struct {
	Path    string // shortened file path (home dir stripped)
	Added   int    // lines added
	Removed int    // lines removed
}

// ChapterView is a narrative section of the session, grouping conversation
// turns and their associated work blocks into a coherent unit.
type ChapterView struct {
	ID    int           // 1-based chapter number
	Title string        // chapter title (LLM-generated or heuristic)
	Items []ChapterItem // interleaved conversation messages and work blocks
}

// ChapterItem is either a conversation message or a work block.
// Exactly one of Message or WorkBlock is non-nil.
type ChapterItem struct {
	IsWorkBlock bool
	Message     *MessageView  // non-nil for conversation messages
	WorkBlock   *WorkBlockView // non-nil for grouped tool/system blocks
}

// BrandColors defines the SageOx brand color palette for CSS variable injection.
type BrandColors struct {
	Primary   string // sage green
	Secondary string // copper gold
	Accent    string // forest green
	Text      string
	TextDim   string
	BgDark    string
	BgCard    string
	Border    string
	Error     string
	Info      string
}

// DefaultBrandColors returns the SageOx brand color palette from the theme.
func DefaultBrandColors() BrandColors {
	return BrandColors{
		Primary:   theme.HexPrimary,
		Secondary: theme.HexSecondary,
		Accent:    theme.HexAccent,
		Text:      theme.HexText,
		TextDim:   theme.HexTextDim,
		BgDark:    theme.HexBgDark,
		BgCard:    theme.HexBgCard,
		Border:    theme.HexBorder,
		Error:     theme.HexError,
		Info:      theme.HexInfo,
	}
}
