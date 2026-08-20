package read

// Typed error codes carried in the envelope (plan of record, step 4).
// Stable snake_case machine identifiers: agents branch on these, so a code is
// a contract. A missing artifact is never reported as a bad id — invalid_id
// means the id itself failed strict validation, nothing else.
const (
	// ErrCodeInvalidID: the supplied id is not a strictly valid cnv_/rec_
	// UUIDv7 or sageox:// citation URI (D16), or a topic id is not a strictly
	// valid tp_ UUIDv7 (D21).
	ErrCodeInvalidID = "invalid_id"
	// ErrCodeNoTeamContext: no local team-context checkout is resolvable for
	// this repo — covers ephemeral mode and pre-first-sync states (D14, D18).
	ErrCodeNoTeamContext = "no_team_context"
	// ErrCodeNotIndexed: the id is valid but INDEX.json has no live entry for
	// it (D3). The exceptional path; the future resolve-endpoint fallback
	// plugs in here.
	ErrCodeNotIndexed = "not_indexed"
	// ErrCodeNoDistillation: the conversation exists but has no distillation
	// episode on disk (D13).
	ErrCodeNoDistillation = "no_distillation"
	// ErrCodeTranscriptNotAvailable: the conversation exists but its
	// transcript.vtt is missing or unusable (D13).
	ErrCodeTranscriptNotAvailable = "transcript_not_available"
	// ErrCodeTopicNotFound: the distillation exists but carries no topic with
	// the requested id (D21).
	ErrCodeTopicNotFound = "topic_not_found"
	// ErrCodeInvalidSelector: a window selector (cue range / time window) is
	// structurally invalid — reversed range, zero ordinal — before any disk
	// read. Usage-shaped, never conflated with missing data.
	ErrCodeInvalidSelector = "invalid_selector"
	// ErrCodeReadError: an unexpected filesystem or parse failure outside the
	// typed absence cases. The only retryable code.
	ErrCodeReadError = "read_error"
)

// Error is the typed failure carried in Envelope.Error. It implements error
// so internal helpers can return it directly; match on Code, not message
// text.
type Error struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	return e.Code + ": " + e.Message
}

// newError builds a typed error. read_error is the only retryable code (a
// transient filesystem or parse failure may clear on retry); every other
// code is a stable fact about the request or the data.
func newError(code, message string) *Error {
	return &Error{Code: code, Message: message, Retryable: code == ErrCodeReadError}
}

// errorGuidance names the next step for a typed error code. D15 promises
// guidance on every envelope, error envelopes included: the guidance channel
// (not error-message prose) is where an agent reads what to do next.
func errorGuidance(code string) string {
	switch code {
	case ErrCodeInvalidID:
		return "Copy a valid id from ox conversation list (cnv_/rec_ UUIDv7 or a sageox:// citation URI)."
	case ErrCodeNoTeamContext:
		return "Check team-context sync with ox status; retry after the first sync completes."
	case ErrCodeNotIndexed:
		return "Browse known ids with ox conversation list; a recent recording may appear after the next sync."
	case ErrCodeNoDistillation:
		return "Read the summary with ox conversation show <cnv_id>; distillation may arrive on a later sync."
	case ErrCodeTranscriptNotAvailable:
		return "Read the summary with ox conversation show <cnv_id>; the transcript may arrive on a later sync."
	case ErrCodeTopicNotFound:
		return "List this conversation's topics with ox conversation topics <cnv_id>."
	case ErrCodeInvalidSelector:
		return "Use --cues N-M with 1-based ordinals, or a time window that ends after it starts."
	case ErrCodeReadError:
		return "Retry the command; if the failure persists, run ox doctor."
	default:
		return "See ox conversation --help for usage."
	}
}
