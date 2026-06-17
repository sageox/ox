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
	// Date, Summary, RefKind carry the structured prior-art fields the renderer
	// composes into a crisp "person · date · summary" link. Environment-
	// independent (no URL), so they're safe to emit in `ox plan enrich --json`;
	// the web URL is built only at render time from the local project config.
	Date    string `json:"date,omitempty"`
	Summary string `json:"summary,omitempty"`
	RefKind string `json:"ref_kind,omitempty"` // session | plan | murmur
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

// DiagramKind is a suggested diagram form for a plan section. The values are the
// literal Mermaid diagram keyword (or "swimlane-timeline" for the hand-built CSS
// timeline) so the agent can paste the suggestion straight into a fenced block.
type DiagramKind string

const (
	DiagramSequence  DiagramKind = "sequenceDiagram"   // ordered call/response path
	DiagramState     DiagramKind = "stateDiagram-v2"   // states + time-bounded transitions
	DiagramSwimlane  DiagramKind = "swimlane-timeline" // phased/parallel work (CSS, not Mermaid)
	DiagramTopology  DiagramKind = "flowchart-LR"      // dependency/topology graph
	DiagramFlowchart DiagramKind = "flowchart-TB"      // branching procedure (hero default)
)

// DiagramHint is a deterministic, per-section suggestion of which diagram form
// best captures the structure ox detected in that section. Rendering an HTML
// plan is now deterministic and free, so the only remaining lever on diagram
// QUALITY is the Mermaid the agent authors into the plan markdown — these hints
// point any agent (Claude, Codex, Gemini, …) at the right diagram for THIS plan,
// per section, instead of defaulting every section to a flowchart. Computed
// locally with zero LLM/network calls, same lane as the badge detectors.
type DiagramHint struct {
	Section       string      `json:"section"`        // H2 heading the hint applies to
	SuggestedType DiagramKind `json:"suggested_type"` // the diagram form that fits
	Reason        string      `json:"reason"`         // what structure was detected, in one clause
}

// VizHint is the data-visualization counterpart of DiagramHint: a per-section
// suggestion of which PARAMETERIZED catalog pattern (one with a deterministic
// `ox plan viz render` renderer — risk-matrix, file-impact-map, cost-waterfall,
// stat-cards, flag-rollout-matrix, …) fits a section. It closes the gap that
// DiagramHint only covers Mermaid/CSS diagram FORMS, leaving the data-viz catalog
// invisible to content-aware matching. The match signal is DERIVED from each
// pattern's `use:` line (no separate catalog field) — see computeVizHints.
type VizHint struct {
	Section   string `json:"section"`    // H2 heading the hint applies to
	PatternID string `json:"pattern_id"` // catalog id, e.g. "risk-matrix" (renderable via `ox plan viz render`)
	Reason    string `json:"reason"`     // the trigger terms matched, in one clause
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

// SignalSummary is the deterministic rollup of which signals fired.
//
// Material is the TEAM-CONTEXT axis: true when the plan warrants surfacing a
// nudge because team context had something to say (any collision OR
// expert-route OR at least one strong prior-art hit).
//
// NonTrivial is the STRUCTURAL axis, independent of team context: true when the
// plan is substantial enough to warrant an enriched HTML render for human
// review even on greenfield work where zero team-context signals fire —
// multi-file (Files >= 2) OR many-step (Steps >= 5). Files counts distinct file
// references cited across all sections; Steps counts H2 sections (excluding the
// preamble). These mirror the prime non-triviality criteria; hotspot/open-PR is
// already covered by Material, and "architectural" is left to agent judgment.
type SignalSummary struct {
	Collisions   int  `json:"collisions"`
	PriorArt     int  `json:"prior_art"`
	ExpertRoutes int  `json:"expert_routes"`
	Material     bool `json:"material"`
	Files        int  `json:"files"`
	Steps        int  `json:"steps"`
	NonTrivial   bool `json:"non_trivial"`
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
	// DiagramHints are deterministic per-section diagram suggestions (which
	// Mermaid/timeline form fits the structure ox detected). Empty when no
	// section had strong enough structure to suggest one.
	DiagramHints []DiagramHint `json:"diagram_hints,omitempty"`
	// VizHints are deterministic per-section data-visualization suggestions
	// (which parameterized catalog pattern fits — risk-matrix, file-impact-map,
	// …), each renderable via `ox plan viz render <id> --data`. Empty when no
	// section matched a pattern's use: signal strongly enough.
	VizHints []VizHint `json:"viz_hints,omitempty"`
	// Guidance is a concise, cross-agent authoring contract for rendering a
	// fantastic HTML plan (decision-first, ten-minute reader, diagrams over
	// prose). It folds in the DiagramHints so the agent gets plan-specific
	// direction, not a generic spec. Empty for a trivial/empty plan.
	Guidance string `json:"guidance,omitempty"`
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
