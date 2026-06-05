// Package plan enriches agent-generated implementation plans with SageOx team
// context. ox computes DETERMINISTIC badges locally (zero LLM tokens) and
// assembles a context bundle; the client agent does any inference. ox NEVER
// makes an LLM or network call in this path.
//
// Architecture:
//   - Detectors produce deterministic Annotations from local data (collision,
//     prior-art, expert-routing). They are fail-open: missing/unreadable data
//     returns (nil, nil), never an aborting error.
//   - Retrievers assemble a context bundle ([]ContextItem) the client agent
//     reasons over to author judgment badges (aligns/conflicts, expert
//     perspective). Also fail-open.
//   - Enrich() orchestrates registered detectors and retrievers, aggregating
//     results into a Result with a deterministic SignalSummary.
//
// Round 2 agents implement detectors/retrievers in collision.go, expert.go,
// priorart.go (and a context-bundle assembler) and register them via init().
package plan

import "context"

// SchemaVersion is the on-disk schema version stamped into both annotations.json
// (Result.SchemaVersion) and meta.json (Meta.SchemaVersion) on write. These are
// long-lived ledger artifacts a future ox may need to migrate; an explicit
// version lets a reader detect and adapt to an older layout instead of guessing.
// Bump this when the serialized shape of Result or Meta changes incompatibly.
const SchemaVersion = "v1"

// BadgeKind distinguishes who produces an annotation: ox locally (deterministic,
// zero tokens) versus the client agent (judgment, reasoned from the bundle).
type BadgeKind string

const (
	// BadgeDeterministic: ox computes the badge locally with zero LLM tokens.
	BadgeDeterministic BadgeKind = "deterministic"
	// BadgeJudgment: the client agent authors the badge from the context bundle.
	BadgeJudgment BadgeKind = "judgment"
)

// BadgeType is the specific signal an annotation carries.
type BadgeType string

const (
	// BadgeCollision: plan touches files in an open PR, hotspot, or recent murmur.
	BadgeCollision BadgeType = "collision"
	// BadgePriorArt: a teammate already did or planned this.
	BadgePriorArt BadgeType = "prior-art"
	// BadgeExpertRoute: who owns this area + their relevant work (deterministic).
	BadgeExpertRoute BadgeType = "expert-routing"
	// BadgeAligns: plan aligns with ADRs, decisions, conventions (judgment).
	BadgeAligns BadgeType = "aligns"
	// BadgeConflicts: plan conflicts with ADRs, decisions, conventions (judgment).
	BadgeConflicts BadgeType = "conflicts"
	// BadgeExpertPersp: synthesized expert stance, cited (judgment).
	BadgeExpertPersp BadgeType = "expert-perspective"
	// BadgeRigor: collaboration-rigor stance synthesized from CollabSignals —
	// how thoughtful the human↔agent path to this plan was (judgment). ox emits
	// the raw counts (CollabSignals); the agent/cloud authors this badge.
	BadgeRigor BadgeType = "rigor"
)

// Annotation is a single badge attached to a plan section.
type Annotation struct {
	Section   string    `json:"section,omitempty"`
	Kind      BadgeKind `json:"kind"`
	Type      BadgeType `json:"type"`
	Why       string    `json:"why"`
	SourceURL string    `json:"source_url,omitempty"`
	Expert    string    `json:"expert,omitempty"`
	Files     []string  `json:"files,omitempty"`
}

// ContextItem is one ranked, pre-retrieved slice of ledger / team context / code
// the client agent reasons over to author judgment badges.
type ContextItem struct {
	Kind    string  `json:"kind"` // murmur|session|decision|adr|commit|discussion
	Title   string  `json:"title"`
	Ref     string  `json:"ref"`
	Snippet string  `json:"snippet,omitempty"`
	Score   float64 `json:"score"`
	Author  string  `json:"author,omitempty"`
	When    string  `json:"when,omitempty"`
}

// Section is one H2-delimited block of a plan, with any file references it cites.
type Section struct {
	Heading string
	Body    string
	Files   []string
}

// Input is a resolved plan: its source path (if any), raw markdown, and parsed
// sections.
type Input struct {
	Path     string
	Raw      string
	Sections []Section
}

// SignalSummary is the deterministic rollup of which signals fired. Material is
// true when the plan warrants surfacing a nudge (any collision OR expert-route
// OR at least one strong prior-art hit).
type SignalSummary struct {
	Collisions   int  `json:"collisions"`
	PriorArt     int  `json:"prior_art"`
	ExpertRoutes int  `json:"expert_routes"`
	Material     bool `json:"material"`
}

// Result is the full output of Enrich: deterministic annotations, the context
// bundle, and the signal summary.
type Result struct {
	// SchemaVersion stamps the serialized annotations.json shape (set to
	// SchemaVersion on write) so a future reader can detect an older layout.
	SchemaVersion string        `json:"schema_version,omitempty"`
	Annotations   []Annotation  `json:"annotations"`
	Context       []ContextItem `json:"context"`
	Signals       SignalSummary `json:"signals"`
}

// Detector produces deterministic annotations from local data.
// MUST be fail-open: on missing/unreadable data return (nil, nil), never an
// error that aborts enrichment.
type Detector interface {
	Name() string
	Detect(ctx context.Context, in Input, gitRoot string) ([]Annotation, error)
}

// Retriever produces context-bundle items. Also fail-open.
type Retriever interface {
	Name() string
	Retrieve(ctx context.Context, in Input, gitRoot string) ([]ContextItem, error)
}
