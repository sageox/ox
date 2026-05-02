package main

import (
	"time"

	"github.com/sageox/ox/internal/distill/history/memoryio"
)

// distillHistoryEnvelope is the `--format=json` success/error response every
// distill history read command emits. It matches the agent-ux envelope contract
// documented in team-memory-journal.md §4.1–§4.2 (Success and Error
// envelope shapes) and §4.3 (Entry and Window object shapes).
//
// The type is shared across `ox distill history list`, `ox distill history show`, and
// `ox distill history since` so that a caller can decode one struct regardless
// of which command produced the envelope. Units 4 and 5 populate the
// Bodies / Entries fields in different combinations; only `list` uses
// the short-list shape. See the per-command helpers at the bottom of
// this file.
//
// Field encoding rules (see §4.5, §4.3):
//
//   - success is always present and non-null.
//   - data and error are mutually exclusive. On success, error is
//     omitted (omitempty, not null). On failure, data is omitted.
//   - timestamps are RFC3339 with the trailing "Z"; the resolver in
//     cmd/ox/distill_history_time.go guarantees UTC on every instant that
//     reaches this layer, so MarshalJSON never needs to reach for
//     time.Location.
//   - elapsed_ms is measured at the command layer (wall clock from
//     RunE entry to envelope flush), not inside the reader.
type distillHistoryEnvelope struct {
	Success   bool                         `json:"success"`
	Type      string                       `json:"type,omitempty"`
	Data      *distillHistoryEnvelopeData  `json:"data,omitempty"`
	Error     *distillHistoryEnvelopeError `json:"error,omitempty"`
	Guidance  string                       `json:"guidance,omitempty"`
	AgentHint string                       `json:"agent_hint,omitempty"`
	ElapsedMS int64                        `json:"elapsed_ms"`
}

// distillHistoryEnvelopeData carries the command-specific payload. Fields are
// a union across all three distill history read commands; each command only
// fills the subset it owns:
//
//   - list populates Entries and Window.
//   - show populates Entries (and, per-row, body_md on each row) and
//     never sets Bodies or Window.
//   - since populates Entries, Bodies, and Window.
//
// Bodies is `*[]string` — a pointer — so since can always emit
// `"bodies": []` even on the empty-window path (spec §3.6 requires an
// empty array, not a dropped field), while list and show leave the
// pointer nil and omitempty drops the field entirely. Plain []string
// with omitempty collapses empty-slice and nil-slice to the same
// dropped-field encoding, which would violate since's contract.
//
// Truncated is set by list and since when Limit was hit. It is never
// set by show.
type distillHistoryEnvelopeData struct {
	Entries   []distillHistoryEnvelopeEntry `json:"entries"`
	Bodies    *[]string                     `json:"bodies,omitempty"`
	Window    *distillHistoryEnvelopeWindow `json:"window,omitempty"`
	Truncated bool                          `json:"truncated,omitempty"`
}

// distillHistoryEnvelopeEntry mirrors the Entry shape in spec §4.3. Fields are
// laid out to match that doc's field order so a future spec change is a
// mechanical edit. RelPath is serialized as "path" per the spec.
//
// The Citations and BodyMD fields are populated only by show (and since
// under --format=json). list leaves them empty — but FactCount and
// CitationCount are always populated, so list callers can show counts
// without materializing bodies.
type distillHistoryEnvelopeEntry struct {
	ID            string              `json:"id"`
	Layer         string              `json:"layer"`
	Date          string              `json:"date"`
	Team          string              `json:"team,omitempty"`
	Path          string              `json:"path"`
	FactCount     int                 `json:"fact_count"`
	CitationCount int                 `json:"citation_count"`
	SourceFiles   []string            `json:"source_files"`
	CreatedAt     string              `json:"created_at,omitempty"`
	Citations     []memoryio.Citation `json:"citations,omitempty"`
	BodyMD        string              `json:"body_md,omitempty"`
	// Status and Error are populated only by `ox distill history show`'s
	// partial-success envelope. list leaves them empty. When Status
	// is "not_found" / "ambiguous" / "read_error", Error carries the
	// per-ID detail and the row's other fields may be partially
	// unset (only ID is guaranteed).
	Status string                       `json:"status,omitempty"`
	Error  *distillHistoryEnvelopeError `json:"error,omitempty"`
}

// distillHistoryEnvelopeWindow mirrors the Window object in spec §4.3. The
// reader's day-rounded effective window is serialized here as RFC3339
// with a "Z" suffix — never the raw user input.
type distillHistoryEnvelopeWindow struct {
	Since         string `json:"since"`
	Until         string `json:"until"`
	LayerResolved string `json:"layer_resolved,omitempty"`
}

// distillHistoryEnvelopeError mirrors the Error object in spec §4.2. code is a
// stable snake_case machine identifier; message is the human-readable
// detail; retryable is true ONLY for transient faults (never for usage
// errors or not-found).
type distillHistoryEnvelopeError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

// distillHistoryExitError wraps an envelope with an explicit exit code so the
// main command runner can surface both the JSON on stdout and the
// correct exit status to the shell. The error/envelope fan-out rule
// from spec §3 is encoded here: usage errors → exit 2, runtime errors
// → exit 1, success → exit 0 (and we do not construct a distillHistoryExitError
// for the success case).
type distillHistoryExitError struct {
	ExitCode int
	Envelope distillHistoryEnvelope
}

func (e *distillHistoryExitError) Error() string {
	if e.Envelope.Error != nil {
		return e.Envelope.Error.Message
	}
	return "distill history command failed"
}

// newDistillHistoryUsageExit wraps a *distillHistoryUsageError into an envelope +
// exit-code-2 distillHistoryExitError. The command layer's RunE funnels every
// usage-error return through this helper so the JSON shape stays
// consistent across commands.
func newDistillHistoryUsageExit(err *distillHistoryUsageError, elapsedMS int64) *distillHistoryExitError {
	return &distillHistoryExitError{
		ExitCode: 2,
		Envelope: distillHistoryEnvelope{
			Success:   false,
			ElapsedMS: elapsedMS,
			Error: &distillHistoryEnvelopeError{
				Code:      err.Code,
				Message:   err.Message,
				Retryable: false,
			},
		},
	}
}

// rfc3339Z formats a UTC instant with the trailing "Z" suffix. Separate
// helper because time.RFC3339 with a UTC instant produces "Z" already —
// but tests pin on the exact string, and hand-rolling the format
// guarantees no surprises if future Go versions tweak the layout.
func rfc3339Z(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}
