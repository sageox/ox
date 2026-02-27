package session

import (
	"encoding/json"
	"fmt"
	"strings"
)

// DefaultReminderInterval is the default number of entries between reminders.
const DefaultReminderInterval = 50

// GuidancePhase represents when guidance is being emitted.
type GuidancePhase string

const (
	GuidancePhaseStart  GuidancePhase = "start"
	GuidancePhaseStop   GuidancePhase = "stop"
	GuidancePhaseRemind GuidancePhase = "remind"
)

// SessionGuidance contains structured guidance for session recording.
type SessionGuidance struct {
	Include          []string `json:"include"`
	Exclude          []string `json:"exclude"`
	Tips             []string `json:"tips,omitempty"`
	ReminderInterval int      `json:"reminder_interval,omitempty"`

	// UserNotification is a message the agent should relay to the user.
	// This is separate from agent instructions - it's for human awareness.
	UserNotification string `json:"user_notification,omitempty"`
}

// GuidanceOutput is the JSON output format for session guidance.
type GuidanceOutput struct {
	Success  bool            `json:"success"`
	Type     string          `json:"type"` // always "session_guidance"
	Phase    GuidancePhase   `json:"phase"`
	AgentID  string          `json:"agent_id"`
	Guidance SessionGuidance `json:"guidance"`
	Message  string          `json:"message,omitempty"`
}

// StartGuidanceOptions configures the guidance returned by StartGuidance.
type StartGuidanceOptions struct {
	AutoStarted bool // true if session was auto-started by ox agent prime
}

// StartGuidance returns detailed guidance for beginning a session recording.
func StartGuidance() SessionGuidance {
	return StartGuidanceWithOptions(StartGuidanceOptions{})
}

// StartGuidanceWithOptions returns detailed guidance with configurable options.
func StartGuidanceWithOptions(opts StartGuidanceOptions) SessionGuidance {
	tips := []string{
		"Write for someone continuing your work who has limited context",
		"Focus on 'what happened' and 'why' rather than 'how'",
		"After ~50 significant actions, run session remind for a refresher",
	}

	// build user notification
	notification := "Recording session. Discussions may be shared with your team."
	if opts.AutoStarted {
		notification += " (Tip: Disable auto-start with 'ox config set session_recording manual')"
	}

	return SessionGuidance{
		Include: []string{
			"User inputs - record exactly what users typed in prompts",
			"Key progress - file changes, successful builds, test results",
			"Commands run - with brief purpose (e.g., 'ran tests to verify fix')",
			"Tool calls - summarize what was done and outcome (not raw JSON)",
			"Decision points - when you chose between approaches, note why",
			"Errors that changed approach - failed attempts that informed solution",
		},
		Exclude: []string{
			"Verbose logs - summarize important details only (e.g., '3 tests passed, 1 failed')",
			"Failed retries - unless they changed your approach",
			"Raw tool JSON - summarize what the tool did instead",
			"Repetitive attempts - note 'retried 3x' not each attempt",
			"Internal reasoning - unless it affected the outcome",
			"Unchanged file contents - note what changed, not full files",
		},
		Tips:             tips,
		ReminderInterval: DefaultReminderInterval,
		UserNotification: notification,
	}
}

// StopGuidance returns guidance for wrapping up a session recording.
func StopGuidance() SessionGuidance {
	return SessionGuidance{
		Include: []string{
			"Final outcome - what was accomplished or blocked",
			"Key decisions - important choices made during session",
			"Open questions - anything unclear for next session",
			"Recommended next steps - what should happen next",
		},
		Exclude: []string{
			"Duplicate information already captured in session",
			"Implementation details - focus on outcomes not mechanics",
		},
		Tips: []string{
			"The session will be automatically summarized",
			"CONTEXT TIP: Delegate session summarize/html to a background subagent",
			"This preserves your main context and doesn't block the user",
		},
	}
}

// RemindGuidance returns condensed guidance for periodic reminders.
func RemindGuidance() SessionGuidance {
	return SessionGuidance{
		Include: []string{
			"User prompts (exact text)",
			"Key progress and outcomes",
			"Commands with purpose",
			"Decisions and reasoning",
		},
		Exclude: []string{
			"Verbose logs (summarize)",
			"Failed retries",
			"Raw tool output",
		},
		Tips: []string{
			"Keep entries focused on what matters for continuity",
		},
	}
}

// FormatGuidanceText formats guidance as human-readable text.
func FormatGuidanceText(phase GuidancePhase, agentID string, guidance SessionGuidance) string {
	var sb strings.Builder

	switch phase {
	case GuidancePhaseStart:
		sb.WriteString("=== Session Recording Started ===\n\n")
	case GuidancePhaseStop:
		sb.WriteString("=== Session Recording Stopping ===\n\n")
	case GuidancePhaseRemind:
		sb.WriteString("=== Session Recording Reminder ===\n\n")
	}

	if len(guidance.Include) > 0 {
		sb.WriteString("INCLUDE in session:\n")
		for _, item := range guidance.Include {
			fmt.Fprintf(&sb, "  - %s\n", item)
		}
		sb.WriteString("\n")
	}

	if len(guidance.Exclude) > 0 {
		sb.WriteString("EXCLUDE from session:\n")
		for _, item := range guidance.Exclude {
			fmt.Fprintf(&sb, "  - %s\n", item)
		}
		sb.WriteString("\n")
	}

	if len(guidance.Tips) > 0 {
		sb.WriteString("Tips:\n")
		for _, tip := range guidance.Tips {
			fmt.Fprintf(&sb, "  - %s\n", tip)
		}
		sb.WriteString("\n")
	}

	if phase == GuidancePhaseStart && guidance.ReminderInterval > 0 {
		fmt.Fprintf(&sb, "Reminder: After ~%d entries, run 'ox agent %s session remind'\n",
			guidance.ReminderInterval, agentID)
	}

	return sb.String()
}

// FormatGuidanceJSON formats guidance as JSON output.
func FormatGuidanceJSON(phase GuidancePhase, agentID string, guidance SessionGuidance, message string) ([]byte, error) {
	output := GuidanceOutput{
		Success:  true,
		Type:     "session_guidance",
		Phase:    phase,
		AgentID:  agentID,
		Guidance: guidance,
		Message:  message,
	}
	return json.MarshalIndent(output, "", "  ")
}

// SummarizeGuidance contains instructions for the coding agent on how to create
// and save session summaries for context recovery.
type SummarizeGuidance struct {
	Instructions   []string        `json:"instructions"`
	Format         string          `json:"format"`
	SavePath       string          `json:"save_path"`
	LedgerPath     string          `json:"ledger_path"`
	DelegationHint *DelegationHint `json:"delegation_hint,omitempty"`
}

// DelegationHint suggests how to delegate work to preserve context.
type DelegationHint struct {
	Recommended     bool   `json:"recommended"`
	SubagentType    string `json:"subagent_type,omitempty"`
	ModelTier       string `json:"model_tier,omitempty"` // "fast" (haiku), "balanced" (sonnet), "reasoning" (opus)
	RunInBackground bool   `json:"run_in_background"`
	Reason          string `json:"reason"`
	Command         string `json:"cmd,omitempty"` // actionable command for agent to run
}

// GetSummarizeGuidance returns detailed guidance for creating session summaries.
func GetSummarizeGuidance(agentID, contextPath string) SummarizeGuidance {
	return SummarizeGuidance{
		Instructions: []string{
			"Review the session summary below for accuracy",
			"Add any missing context that would help someone continuing this work",
			"Save the summary to the ledger for future sessions",
			"Include: what was accomplished, key decisions, blockers encountered",
			"Exclude: implementation details that can be re-derived from code",
		},
		DelegationHint: &DelegationHint{
			Recommended:     true,
			SubagentType:    "technical-writer",
			ModelTier:       "reasoning", // opus - expert writing requires deep comprehension
			RunInBackground: true,
			Reason:          "Preserves main agent context and doesn't block user",
			Command:         "ox agent " + agentID + " session summarize",
		},
		Format: `## Session Summary

**Agent:** <agent_id>
**Date:** <date>

### Accomplished
- <bullet points of completed work>

### Key Decisions
- <important choices made and why>

### Open Questions
- <unresolved issues or uncertainties>

### Next Steps
- <recommended actions for continuation>`,
		SavePath:   contextPath + "/summaries/",
		LedgerPath: contextPath,
	}
}

// FormatSummarizeGuidanceText formats summarization guidance as human-readable text.
func FormatSummarizeGuidanceText(agentID string, guidance SummarizeGuidance) string {
	var sb strings.Builder

	sb.WriteString("=== Session Summary Guidance ===\n\n")

	// show delegation hint prominently at top
	if guidance.DelegationHint != nil && guidance.DelegationHint.Recommended {
		sb.WriteString("DELEGATION RECOMMENDED:\n")
		fmt.Fprintf(&sb, "  Subagent: %s\n", guidance.DelegationHint.SubagentType)
		fmt.Fprintf(&sb, "  Model: %s\n", guidance.DelegationHint.ModelTier)
		if guidance.DelegationHint.RunInBackground {
			sb.WriteString("  Background: yes (non-blocking)\n")
		}
		fmt.Fprintf(&sb, "  Reason: %s\n", guidance.DelegationHint.Reason)
		sb.WriteString("\n")
	}

	sb.WriteString("Instructions:\n")
	for _, instr := range guidance.Instructions {
		fmt.Fprintf(&sb, "  - %s\n", instr)
	}
	sb.WriteString("\n")

	sb.WriteString("Recommended format:\n")
	sb.WriteString("```markdown\n")
	sb.WriteString(guidance.Format)
	sb.WriteString("\n```\n\n")

	if guidance.SavePath != "" {
		fmt.Fprintf(&sb, "Save to: %s<session-date>-%s.md\n", guidance.SavePath, agentID)
	}

	if guidance.LedgerPath != "" {
		fmt.Fprintf(&sb, "Ledger: %s\n", guidance.LedgerPath)
	}

	return sb.String()
}

// SummarizeGuidanceOutput is the JSON output format for summarize guidance.
type SummarizeGuidanceOutput struct {
	Success  bool              `json:"success"`
	Type     string            `json:"type"` // always "session_summarize_guidance"
	AgentID  string            `json:"agent_id"`
	Guidance SummarizeGuidance `json:"guidance"`
	Summary  *SummarizeOutput  `json:"summary,omitempty"`
}

// FormatSummarizeGuidanceJSON formats summarization guidance as JSON output.
func FormatSummarizeGuidanceJSON(agentID string, guidance SummarizeGuidance, summary *SummarizeOutput) ([]byte, error) {
	output := SummarizeGuidanceOutput{
		Success:  true,
		Type:     "session_summarize_guidance",
		AgentID:  agentID,
		Guidance: guidance,
		Summary:  summary,
	}
	return json.MarshalIndent(output, "", "  ")
}

// HTMLGuidance contains instructions for generating HTML session viewer.
type HTMLGuidance struct {
	Instructions   []string        `json:"instructions"`
	OutputPath     string          `json:"output_path"`
	SourcePath     string          `json:"source_path"`
	Features       []string        `json:"features"`
	DelegationHint *DelegationHint `json:"delegation_hint,omitempty"`
}

// GetHTMLGuidance returns detailed guidance for generating HTML session viewer.
func GetHTMLGuidance(agentID, rawPath, outputPath string) HTMLGuidance {
	return HTMLGuidance{
		Instructions: []string{
			"Generate an interactive HTML viewer for the session",
			"Include the summary section at the top",
			"Enable collapsible sections for long entries",
			"Add syntax highlighting for code blocks",
			"Include a timeline view of events",
		},
		OutputPath: outputPath,
		SourcePath: rawPath,
		Features: []string{
			"Collapsible tool call details",
			"Syntax highlighting for code",
			"Timeline navigation",
			"Search within session",
			"Summary panel with key actions",
		},
		DelegationHint: &DelegationHint{
			Recommended:     true,
			SubagentType:    "frontend-developer",
			ModelTier:       "fast", // haiku - templating/formatting doesn't need deep reasoning
			RunInBackground: true,
			Reason:          "HTML generation is context-heavy; background execution doesn't block user",
			Command:         "ox agent " + agentID + " session html",
		},
	}
}

// FormatHTMLGuidanceText formats HTML generation guidance as human-readable text.
func FormatHTMLGuidanceText(agentID string, guidance HTMLGuidance) string {
	var sb strings.Builder

	sb.WriteString("=== HTML Session Viewer ===\n\n")

	// show delegation hint prominently at top
	if guidance.DelegationHint != nil && guidance.DelegationHint.Recommended {
		sb.WriteString("DELEGATION RECOMMENDED:\n")
		fmt.Fprintf(&sb, "  Subagent: %s\n", guidance.DelegationHint.SubagentType)
		fmt.Fprintf(&sb, "  Model: %s\n", guidance.DelegationHint.ModelTier)
		if guidance.DelegationHint.RunInBackground {
			sb.WriteString("  Background: yes (non-blocking)\n")
		}
		fmt.Fprintf(&sb, "  Reason: %s\n", guidance.DelegationHint.Reason)
		sb.WriteString("\n")
	}

	sb.WriteString("Instructions:\n")
	for _, instr := range guidance.Instructions {
		fmt.Fprintf(&sb, "  - %s\n", instr)
	}
	sb.WriteString("\n")

	if guidance.SourcePath != "" {
		fmt.Fprintf(&sb, "Source: %s\n", guidance.SourcePath)
	}
	if guidance.OutputPath != "" {
		fmt.Fprintf(&sb, "Output: %s\n", guidance.OutputPath)
	}

	if len(guidance.Features) > 0 {
		sb.WriteString("\nFeatures included:\n")
		for _, feature := range guidance.Features {
			fmt.Fprintf(&sb, "  - %s\n", feature)
		}
	}

	return sb.String()
}

// HTMLGuidanceOutput is the JSON output format for HTML generation guidance.
type HTMLGuidanceOutput struct {
	Success   bool         `json:"success"`
	Type      string       `json:"type"` // always "session_html_guidance"
	AgentID   string       `json:"agent_id"`
	Guidance  HTMLGuidance `json:"guidance"`
	Generated bool         `json:"generated"`
	HTMLPath  string       `json:"html_path,omitempty"`
	Message   string       `json:"message,omitempty"`
}

// FormatHTMLGuidanceJSON formats HTML generation guidance as JSON output.
func FormatHTMLGuidanceJSON(agentID string, guidance HTMLGuidance, generated bool, htmlPath, message string) ([]byte, error) {
	output := HTMLGuidanceOutput{
		Success:   true,
		Type:      "session_html_guidance",
		AgentID:   agentID,
		Guidance:  guidance,
		Generated: generated,
		HTMLPath:  htmlPath,
		Message:   message,
	}
	return json.MarshalIndent(output, "", "  ")
}

// SummarizeOutput is the JSON output format for session summarization.
type SummarizeOutput struct {
	Success       bool     `json:"success"`
	Type          string   `json:"type"` // always "session_summary"
	AgentID       string   `json:"agent_id"`
	Summary       string   `json:"summary"`
	KeyActions    []string `json:"key_actions,omitempty"`
	Outcome       string   `json:"outcome,omitempty"`
	TopicsFound   []string `json:"topics_found,omitempty"`
	FinalPlan     string   `json:"final_plan,omitempty"` // final plan/architecture from session
	Diagrams      []string `json:"diagrams,omitempty"`   // extracted mermaid diagrams
	EntryCount    int      `json:"entry_count,omitempty"`
	FilePath      string   `json:"file_path,omitempty"`
	Message       string   `json:"message,omitempty"`
	SummaryPrompt string   `json:"summary_prompt,omitempty"` // prompt for calling agent to generate summary
}

// FormatSummaryText formats a summary as human-readable text.
func FormatSummaryText(agentID string, summary *SummarizeResponse, entryCount int) string {
	var sb strings.Builder

	sb.WriteString("=== Session Summary ===\n\n")
	fmt.Fprintf(&sb, "Agent: %s\n", agentID)
	if entryCount > 0 {
		fmt.Fprintf(&sb, "Entries: %d\n", entryCount)
	}
	sb.WriteString("\n")

	if summary.Summary != "" {
		sb.WriteString("Summary:\n")
		fmt.Fprintf(&sb, "  %s\n\n", summary.Summary)
	}

	if len(summary.KeyActions) > 0 {
		sb.WriteString("Key Actions:\n")
		for _, action := range summary.KeyActions {
			fmt.Fprintf(&sb, "  - %s\n", action)
		}
		sb.WriteString("\n")
	}

	if len(summary.Diagrams) > 0 {
		fmt.Fprintf(&sb, "Diagrams: %d diagram(s) captured\n\n", len(summary.Diagrams))
	}

	if summary.FinalPlan != "" {
		sb.WriteString("Final Plan:\n")
		// show first 500 chars of plan (full plan in file output)
		planPreview := summary.FinalPlan
		if len(planPreview) > 500 {
			planPreview = planPreview[:500] + "... [see full output]"
		}
		fmt.Fprintf(&sb, "  %s\n\n", planPreview)
	}

	if summary.Outcome != "" {
		fmt.Fprintf(&sb, "Outcome: %s\n", summary.Outcome)
	}

	if len(summary.TopicsFound) > 0 {
		fmt.Fprintf(&sb, "Topics: %s\n", strings.Join(summary.TopicsFound, ", "))
	}

	return sb.String()
}

// FormatSummaryJSON formats a summary as JSON output.
func FormatSummaryJSON(agentID string, summary *SummarizeResponse, entryCount int, filePath, message string) ([]byte, error) {
	output := SummarizeOutput{
		Success:     true,
		Type:        "session_summary",
		AgentID:     agentID,
		Summary:     summary.Summary,
		KeyActions:  summary.KeyActions,
		Outcome:     summary.Outcome,
		TopicsFound: summary.TopicsFound,
		FinalPlan:   summary.FinalPlan,
		Diagrams:    summary.Diagrams,
		EntryCount:  entryCount,
		FilePath:    filePath,
		Message:     message,
	}
	return json.MarshalIndent(output, "", "  ")
}
