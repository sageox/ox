package read

import (
	"encoding/json"
	"time"

	"github.com/sageox/ox/internal/tokens"
)

// Envelope is the JSON response every conversation read query produces
// (plan of record: success/data/error/guidance/token_estimate/last_sync/
// elapsed_ms plus warnings[]). Data and Error are mutually exclusive.
//
// Warnings is the channel D6 (both manifest names present) and D8 (revision
// mismatch cross-check note) report through — advisory, never fatal.
type Envelope struct {
	Success       bool     `json:"success"`
	Data          any      `json:"data,omitempty"`
	Error         *Error   `json:"error,omitempty"`
	Guidance      string   `json:"guidance,omitempty"`
	Warnings      []string `json:"warnings,omitempty"`
	TokenEstimate int      `json:"token_estimate,omitempty"`
	LastSync      string   `json:"last_sync,omitempty"`
	ElapsedMS     int64    `json:"elapsed_ms"`
}

// finishSuccess assembles a success envelope: token_estimate is measured on
// the marshaled data payload (the JSON is what an agent actually spends
// context on), last_sync comes from the local team-context state (D14 — no
// daemon round-trip), and elapsed_ms is wall clock from query entry.
func (r *Reader) finishSuccess(start time.Time, data any, guidance string, warnings []string) *Envelope {
	env := &Envelope{
		Success:  true,
		Data:     data,
		Guidance: guidance,
		Warnings: warnings,
	}
	if payload, err := json.Marshal(data); err == nil {
		env.TokenEstimate = tokens.EstimateTokens(string(payload))
	}
	r.stamp(env, start)
	return env
}

// finishError assembles a typed-failure envelope. D15 promises guidance and
// token_estimate on every envelope, so the error path carries the per-code
// next step and measures the error payload (what an agent spends context on).
func (r *Reader) finishError(start time.Time, e *Error, warnings []string) *Envelope {
	env := errorEnvelope(e)
	env.Warnings = warnings
	r.stamp(env, start)
	return env
}

// errorEnvelope builds the D15-complete envelope shell for a typed error:
// guidance from the error's code and token_estimate over the marshaled error
// payload. The command layer uses it (via ErrorEnvelope) for failures it
// detects before a Reader exists, so every envelope — reader-produced or
// command-produced — honors the same contract.
func errorEnvelope(e *Error) *Envelope {
	env := &Envelope{Error: e, Guidance: errorGuidance(e.Code)}
	if payload, err := json.Marshal(e); err == nil {
		env.TokenEstimate = tokens.EstimateTokens(string(payload))
	}
	return env
}

// ErrorEnvelope assembles an error envelope for callers outside the Reader
// (the command layer's usage and open failures). No Reader state exists on
// those paths, so last_sync stays empty and elapsed_ms is zero.
func ErrorEnvelope(e *Error) *Envelope {
	return errorEnvelope(e)
}

func (r *Reader) stamp(env *Envelope, start time.Time) {
	if !r.lastSync.IsZero() {
		env.LastSync = r.lastSync.UTC().Format(time.RFC3339)
	}
	env.ElapsedMS = r.now().Sub(start).Milliseconds()
}
