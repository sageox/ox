package summaryeval

import "time"

// GoldenSession is a single entry in the reference corpus: a session name
// + the hand-reviewed reference summary we score candidates against.
// Stored on disk as <corpus>/<session_name>/reference.json.
type GoldenSession struct {
	// Name identifies the session (matches ledger session dir name).
	Name string `json:"name"`

	// Notes is free-form human context explaining why this session was
	// chosen and what a good summary should capture. Not scored; helps
	// future curators.
	Notes string `json:"notes,omitempty"`

	// Reference is the trusted summary: what a great distillation looks
	// like for this session. Candidates are scored against this.
	Reference Summary `json:"reference"`
}

// Summary is a minimal shape mirroring the fields from
// pkg/sessionsummary.SummarizeResponse that the rubric actually scores.
// Kept separate so summaryeval has no dependency on sessionsummary —
// the two evolve independently, and the eval harness doesn't care about
// the full summary schema (quality_score, sageox_score, chapter lists,
// etc. are out of scope).
type Summary struct {
	Title       string      `json:"title"`
	Summary     string      `json:"summary"`
	KeyActions  []string    `json:"key_actions"`
	Outcome     string      `json:"outcome"`
	AhaMoments  []AhaMoment `json:"aha_moments,omitempty"`
	TopicsFound []string    `json:"topics_found,omitempty"`
}

// AhaMoment is a minimal moment shape for scoring. We score count match
// and approximate sequence-position overlap, not the full highlight text.
type AhaMoment struct {
	Seq  int    `json:"seq"`
	Type string `json:"type"`
}

// Dimension names the scored rubric dimensions. Constants so typos fail
// at compile time and callers can iterate a known set.
const (
	DimTitle      = "title"
	DimSummary    = "summary"
	DimKeyActions = "key_actions"
	DimOutcome    = "outcome"
	DimAhaMoments = "aha_moments"
)

// Dimensions returns the canonical ordered list. Callers use this for
// stable report ordering.
func Dimensions() []string {
	return []string{DimTitle, DimSummary, DimKeyActions, DimOutcome, DimAhaMoments}
}

// DimensionScore is one dimension's 0.0–1.0 result for a single session
// with a reason string explaining how it was computed.
type DimensionScore struct {
	Dimension string  `json:"dimension"`
	Score     float64 `json:"score"`
	Reason    string  `json:"reason,omitempty"`
}

// SessionScore is the full per-session result: all dimensions + a
// weighted aggregate.
type SessionScore struct {
	Name       string           `json:"name"`
	Dimensions []DimensionScore `json:"dimensions"`
	Overall    float64          `json:"overall"`
}

// Report is the aggregate result of running an eval over a corpus.
type Report struct {
	ScoredAt       time.Time        `json:"scored_at"`
	CorpusSize     int              `json:"corpus_size"`
	OverallMean    float64          `json:"overall_mean"`
	OverallMin     float64          `json:"overall_min"`
	OverallMax     float64          `json:"overall_max"`
	DimensionMeans map[string]float64 `json:"dimension_means"`
	Sessions       []SessionScore   `json:"sessions"`

	// GatesFailed lists any minimum-threshold gates that didn't pass.
	// Empty = all gates met (or no gates configured).
	GatesFailed []string `json:"gates_failed,omitempty"`
}

// Weights are the rubric's per-dimension contribution to the overall
// score. Must sum to 1.0. Defaults chosen deliberately:
//
//   - Title carries user-visible fidelity of the session's identity
//   - Summary is the longest-form signal
//   - Key actions are the "what was done" spine
//   - Outcome is small but binary-important (success vs failed)
//   - Aha moments are secondary but measurable
type Weights struct {
	Title      float64 `json:"title"`
	Summary    float64 `json:"summary"`
	KeyActions float64 `json:"key_actions"`
	Outcome    float64 `json:"outcome"`
	AhaMoments float64 `json:"aha_moments"`
}

// DefaultWeights returns the canonical rubric weights (sum = 1.0).
func DefaultWeights() Weights {
	return Weights{
		Title:      0.20,
		Summary:    0.30,
		KeyActions: 0.30,
		Outcome:    0.10,
		AhaMoments: 0.10,
	}
}

// Gates is an optional set of minimum-score thresholds. Any dimension
// or overall score below its threshold fails the gate and surfaces in
// Report.GatesFailed. Useful as a CI regression guard.
type Gates struct {
	MinOverall    float64            `json:"min_overall,omitempty"`
	MinDimensions map[string]float64 `json:"min_dimensions,omitempty"`
}
