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

// EvaluateQuality determines what to do with a session based on its quality score.
// A score of 0 means the LLM didn't provide one — default to upload (backward compat).
func EvaluateQuality(score, uploadThreshold, discardThreshold float64) QualityDisposition {
	if score <= 0 {
		return QualityUpload
	}
	if score < discardThreshold {
		return QualityDiscard
	}
	if score < uploadThreshold {
		return QualityLocalOnly
	}
	return QualityUpload
}
