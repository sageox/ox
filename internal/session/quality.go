package session

// QualityDisposition is the action to take based on a session's quality.
type QualityDisposition string

const (
	// QualityUpload means the session should be pushed to the team ledger.
	QualityUpload QualityDisposition = "upload"
	// QualityDiscard means the session should be skipped.
	QualityDiscard QualityDisposition = "discard"
)

// EvaluateQuality maps a legacy quality score to an upload or discard disposition.
//
// The score argument is a real, scored value (0.0–1.0). Callers must not pass
// a negative sentinel or similar "unscored" placeholder: if the LLM did not
// produce a score, decide disposition at the callsite (typically
// QualityUpload with a stub summary) rather than routing through this
// function. An older version of this code treated score <= 0 as "unscored →
// upload," which conflated an empty session the LLM correctly scored 0 with
// the truly-unscored case and quietly published header-only sessions to the
// team ledger (see #525).
func EvaluateQuality(score, discardThreshold float64) QualityDisposition {
	if score < discardThreshold {
		return QualityDiscard
	}
	return QualityUpload
}

// EvaluateQualityCategory maps a categorical quality label to a
// disposition. Categorical scoring is the canonical signal as of 2026-05
// — numeric scales cluster on round numbers and ignore fine-grained
// rubric distinctions, while LLMs are far more reliable picking from a
// small named set with anchored examples.
//
// Legacy local_only, unknown, and empty categories return QualityUpload: an
// unrecognized string from a legacy or future summary should default to
// preserving the artifact, not silently discarding it. Callers that have
// a numeric score on hand should use EvaluateQuality instead — this
// function refuses to guess. The decision to upload an unknown category
// matches the existing "unscored fallbacks default to upload" policy in
// session_finalize.go's ProcessResult.
func EvaluateQualityCategory(category string) QualityDisposition {
	if category == "skip" {
		return QualityDiscard
	}
	return QualityUpload
}
