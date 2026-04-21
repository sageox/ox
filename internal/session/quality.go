package session

// QualityDisposition is the action to take based on a session's quality score.
type QualityDisposition string

const (
	// QualityUpload means the session should be pushed to the team ledger.
	QualityUpload QualityDisposition = "upload"
	// QualityLocalOnly means artifacts are kept locally but not pushed.
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
