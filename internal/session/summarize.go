package session

import (
	ss "github.com/sageox/ox/pkg/sessionsummary"
)

// SummaryPromptGuidelines contains the shared guidelines for session summarization.
// Delegates to pkg/sessionsummary for the canonical prompt.
const SummaryPromptGuidelines = ss.SummaryPromptGuidelines

// Type aliases — all consumers continue using session.SummarizeResponse etc.
//
// SummarizeRequest/SummarizeEntry aliases were removed when the dead
// BuildSummarizeRequest path was deleted — no callers in the codebase
// used session.SummarizeRequest or session.SummarizeEntry.
type (
	SummarizeResponse = ss.SummarizeResponse
	AhaMoment         = ss.AhaMoment
	SageoxInsight     = ss.SageoxInsight
	ChapterSummary    = ss.ChapterSummary
	FileSummary       = ss.FileSummary
	AgentSummary      = ss.AgentSummary
	Decision          = ss.Decision
	ActionItem        = ss.ActionItem
	OpenQuestion      = ss.OpenQuestion
	TechnicalContext  = ss.TechnicalContext
)

// BuildSummaryPrompt builds a prompt for the calling agent to generate a session summary.
// Delegates to pkg/sessionsummary.
func BuildSummaryPrompt(entries []Entry, rawPath, ledgerSessionDir string) string {
	return ss.BuildSummaryPrompt(entriesToPkg(entries), rawPath, ledgerSessionDir)
}

// LocalSummary generates a simple local summary without API call.
// Delegates to pkg/sessionsummary.
func LocalSummary(entries []Entry) string {
	return ss.LocalSummary(entriesToPkg(entries))
}

// entriesToPkg converts internal SessionEntry slice to pkg Entry slice.
func entriesToPkg(entries []Entry) []ss.Entry {
	out := make([]ss.Entry, len(entries))
	for i, e := range entries {
		out[i] = ss.Entry{
			Timestamp:  e.Timestamp,
			Type:       string(e.Type),
			Content:    e.Content,
			ToolName:   e.ToolName,
			ToolInput:  e.ToolInput,
			ToolOutput: e.ToolOutput,
			IsError:    e.IsError,
		}
	}
	return out
}
