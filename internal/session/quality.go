package session

// QualityDisposition is the action to take based on a session's quality score.
type QualityDisposition string

const (
	// QualityUpload means the session should be pushed to the team ledger.
	QualityUpload QualityDisposition = "upload"
	// QualityLocalOnly marks real but primarily individual work. The daemon's
	// finalize publishes it like QualityUpload, pending a team decision (see
	// ProcessResult in internal/daemon/agentwork/session_finalize.go);
	// ox session push-summary and session import do not push it.
	QualityLocalOnly QualityDisposition = "local_only"
	// QualityDiscard means the session should be deleted entirely.
	QualityDiscard QualityDisposition = "discard"
)

// EvaluateQuality maps a quality score to a disposition using the two thresholds.
//
// The score argument is a real, scored value (0.0–1.0). Callers must not pass
// a negative sentinel or similar "unscored" placeholder: if the LLM did not
// produce a score, decide disposition at the callsite (typically
// QualityUpload with a stub summary) rather than routing through this
// function. An older version of this code treated score <= 0 as "unscored →
// upload," which conflated an empty session the LLM correctly scored 0 with
// the truly-unscored case and quietly published header-only sessions to the
// team ledger (see #525).
func EvaluateQuality(score, uploadThreshold, discardThreshold float64) QualityDisposition {
	if score < discardThreshold {
		return QualityDiscard
	}
	if score < uploadThreshold {
		return QualityLocalOnly
	}
	return QualityUpload
}

// EvaluateQualityCategory maps a categorical quality label to a
// disposition. Categorical scoring is the canonical signal as of 2026-05
// — numeric scales cluster on round numbers and ignore fine-grained
// rubric distinctions, while LLMs are far more reliable picking from a
// small named set with anchored examples.
//
// Unknown / empty category returns QualityUpload by design: an
// unrecognized string from a legacy or future summary should default to
// preserving the artifact, not silently discarding it. Callers that have
// a numeric score on hand should use EvaluateQuality instead — this
// function refuses to guess. The decision to upload an unknown category
// matches the existing "unscored fallbacks default to upload" policy in
// session_finalize.go's ProcessResult.
func EvaluateQualityCategory(category string) QualityDisposition {
	switch category {
	case "skip":
		return QualityDiscard
	case "local_only":
		return QualityLocalOnly
	case "share":
		return QualityUpload
	default:
		return QualityUpload
	}
}
