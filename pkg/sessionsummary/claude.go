package sessionsummary

import "os"

// defaultSummaryModel is the floating Haiku 4.5 alias. Pinned for
// summarization because the task is structured JSON extraction over a
// fixed schema — well within Haiku's capabilities and 5–15× cheaper
// than the user's local Sonnet/Opus default. Floating (not dated) so
// new Haiku 4.5 point releases roll forward without code changes.
const defaultSummaryModel = "claude-haiku-4-5"

// summaryModelEnv overrides defaultSummaryModel. Empty value (export
// OX_SUMMARY_MODEL=) is meaningful: it restores pre-Haiku behavior by
// passing no --model flag to claude, deferring to the user's local
// default. Useful as a fast rollback.
const summaryModelEnv = "OX_SUMMARY_MODEL"

// DefaultSummaryModel returns the model identifier to pass to `claude -p`
// for summarization. Reads OX_SUMMARY_MODEL if set (including empty), else
// returns "claude-haiku-4-5". Callers pass the result as the summary run's model.
//
// Empty string return = no --model flag = user's local Claude Code default.
func DefaultSummaryModel() string {
	if v, ok := os.LookupEnv(summaryModelEnv); ok {
		return v // explicit override, including empty for rollback
	}
	return defaultSummaryModel
}
