// Package decision discovers and enriches Decision Records (DRs — ADRs are
// one type, DDRs another) in a repo.
//
// Doctrine (inherited from internal/plan, per ADR-021 and ADR-024):
//   - ox computes DETERMINISTIC signals and context bundles locally — zero LLM
//     or network-judge calls. The client agent authors every word and every
//     judgment. ox never edits a DR file.
//   - Local retrieval is lexical/structured (no local embeddings); the
//     full-text search over these same files already lives in codedb
//     (`ox code search`), so this package keeps NO persisted index — the
//     corpus is walked fresh per call (hundreds of small files, milliseconds).
//   - A surfaced item is a CANDIDATE, never a verdict: whether a decision
//     aligns with, amends, or supersedes another stays the agent's call.
//   - Citations are composed by ox and pasted by the agent: enrich emits a
//     Cite only for items it just resolved, and re-running enrich on an edited
//     DR re-checks every ref — the sageox-mono ADR-061 "phantom decision #9"
//     failure class dies in this loop.
package decision

import "context"

// SchemaVersion stamps the enrich Result. Bump on incompatible shape changes.
const SchemaVersion = "v1"

// BadgeKind distinguishes ox-computed facts from agent-authored judgment.
// ox only ever emits BadgeDeterministic; BadgeJudgment exists so downstream
// consumers can carry agent badges alongside.
type BadgeKind string

const (
	BadgeDeterministic BadgeKind = "deterministic"
	BadgeJudgment      BadgeKind = "judgment"
)

// BadgeType is the specific signal an annotation carries.
type BadgeType string

const (
	// BadgeRelatedDecision: an existing DR overlaps the draft/topic (candidate only).
	BadgeRelatedDecision BadgeType = "related-decision"
	// BadgeNumbering: numbering suggestion or duplicate-number warning.
	BadgeNumbering BadgeType = "numbering"
	// BadgeDiagnostic: corpus-level diagnostic relevant to this input;
	// Rule carries the specific rule.
	BadgeDiagnostic BadgeType = "diagnostic"
	// BadgeDrift: files cited by the DR changed after the DR's date.
	BadgeDrift BadgeType = "drift"
	// BadgeUnresolvedRef: a ref in the DR does not resolve against the corpus.
	BadgeUnresolvedRef BadgeType = "unresolved-ref"
)

// Diagnostic rules (Annotation.Rule values) and relation markers.
const (
	RuleDuplicateNumber       = "duplicate-number"
	RuleUnnumbered            = "unnumbered"
	RuleMissingStatus         = "missing-status"
	RuleDanglingRef           = "dangling-ref"
	RuleSupersededNoSuccessor = "superseded-no-successor"
	RuleSageoxCreditOverflow  = "sageox-credit-overflow"
	// RuleRelatedOverflow reports bounded related-decision results with more
	// above-floor candidates available through --explain/code search.
	RuleRelatedOverflow = "related-decision-overflow"
	// RuleUnreadableCorpus: one or more intended DR files could not be read or
	// parsed — a source gap, not evidence that no prior decision exists.
	RuleUnreadableCorpus = "unreadable-corpus"

	// RelationCandidate is the ONLY relation ox asserts (marker doctrine):
	// related/conflicting/superseding is the agent's judgment.
	RelationCandidate         = "candidate"
	VariantSupersedeCandidate = "supersede-candidate"
)

// Annotation is a single deterministic badge attached to the enrich input.
type Annotation struct {
	Kind BadgeKind `json:"kind"`
	Type BadgeType `json:"type"`
	Why  string    `json:"why"`
	// Ref is the human-facing id ("ADR-017") and RefPath the repo-relative
	// path the agent can Read. Anchor targets a D-section ("D4").
	Ref       string   `json:"ref,omitempty"`
	RefPath   string   `json:"ref_path,omitempty"`
	Anchor    string   `json:"anchor,omitempty"`
	Relation  string   `json:"relation,omitempty"`
	Rule      string   `json:"rule,omitempty"`
	Files     []string `json:"files,omitempty"`
	SourceURL string   `json:"source_url,omitempty"`
	Date      string   `json:"date,omitempty"`
}

// Cite is a ready-to-paste citation pair: prose the agent may adapt, and the
// machine ref comment it must paste VERBATIM. ox only emits a Cite for an item
// it just resolved — the fabrication kill-switch.
type Cite struct {
	ProseHint string `json:"prose_hint"`
	Comment   string `json:"comment"`
}

// ContextItem is one ranked slice of context the agent reasons over while
// authoring or amending a DR.
type ContextItem struct {
	// Kind: decision | session | murmur
	Kind    string  `json:"kind"`
	Title   string  `json:"title"`
	Ref     string  `json:"ref"`
	Snippet string  `json:"snippet,omitempty"`
	Score   float64 `json:"score"`
	Author  string  `json:"author,omitempty"`
	When    string  `json:"when,omitempty"`
	// Cite is absent for non-durable sources (murmurs) — those are awareness
	// only and must never be cited in a committed DR.
	Cite *Cite `json:"cite,omitempty"`
}

// DecisionInfo describes the enrich input's own identity as parsed.
type DecisionInfo struct {
	ID          string `json:"id,omitempty"`
	SuggestedID string `json:"suggested_id,omitempty"`
	Title       string `json:"title,omitempty"`
	Status      string `json:"status,omitempty"`
}

// Conventions describes this repo's DR corpus so any agent drafts in-house
// style instead of inventing one.
type Conventions struct {
	Dir              string   `json:"dir,omitempty"`
	FilenamePattern  string   `json:"filename_pattern,omitempty"`
	NextNumber       int      `json:"next_number,omitempty"`
	NumberCollisions []string `json:"number_collisions,omitempty"`
	StatusesObserved []string `json:"statuses_observed,omitempty"`
	SectionsObserved []string `json:"sections_observed,omitempty"`
	AmendmentMarker  string   `json:"amendment_marker,omitempty"`
	DecisionAnchors  string   `json:"decision_anchors,omitempty"`
}

// SignalSummary rolls up what fired; Material means "team context has
// something to say about this DR".
type SignalSummary struct {
	Related        int  `json:"related"`
	PriorSessions  int  `json:"prior_sessions"`
	Murmurs        int  `json:"murmurs"`
	Diagnostics    int  `json:"diagnostics"`
	UnresolvedRefs int  `json:"unresolved_refs"`
	Material       bool `json:"material"`
	// Degraded is true when a retrieval source could not be read (a detector or
	// retriever errored, or a decision dir exists but nothing in it parsed as a
	// record). A degraded run is NOT a verified "no prior decision" — guidance
	// must never present its emptiness as a checked absence (#823).
	Degraded bool `json:"degraded,omitempty"`
}

// DroppedCandidate is a record omitted by a result cap or the relevance floor.
// Surfaced only under --explain so callers can distinguish a true miss from a
// bounded or thresholded result (#823).
type DroppedCandidate struct {
	Ref     string  `json:"ref,omitempty"`
	RefPath string  `json:"ref_path,omitempty"`
	Title   string  `json:"title,omitempty"`
	Score   float64 `json:"score"`
	Reason  string  `json:"reason"`
}

// Result is the `ox decision enrich` JSON payload.
type Result struct {
	SchemaVersion string        `json:"schema_version"`
	Decision      DecisionInfo  `json:"decision"`
	Conventions   Conventions   `json:"conventions"`
	Annotations   []Annotation  `json:"annotations"`
	Context       []ContextItem `json:"context"`
	Signals       SignalSummary `json:"signals"`
	Guidance      string        `json:"guidance"`
	// Dropped lists cap-omitted and sub-floor candidates under --explain.
	Dropped []DroppedCandidate `json:"dropped,omitempty"`
}

// Env is the shared, read-only environment Enrich resolves ONCE and hands to
// every detector/retriever. All fields are best-effort — an empty field means
// that corpus is unavailable and the consumer fails open.
type Env struct {
	GitRoot string
	// Corpus is the freshly-walked DR set for this repo.
	Corpus []Record
	// LedgerPath is the local ledger checkout (sessions, murmurs). Empty when
	// no ledger is provisioned.
	LedgerPath string
	// CorpusUnparsed counts unreadable files and files with strong DR intent that
	// did not make it into the catalog. Ordinary notes/templates are excluded
	// without degrading the corpus.
	CorpusUnparsed int
	// SourceUnavailable records a project/config source that could not be loaded.
	// Enrich remains fail-open, but its result must not describe the resulting
	// absence as verified.
	SourceUnavailable bool
	// Explain, when set, makes Enrich populate Result.Dropped with cap-omitted
	// and sub-floor candidates.
	Explain bool
}

// Detector produces deterministic annotations. Fail-open: return (nil, nil)
// on missing data, never an aborting error.
type Detector interface {
	Name() string
	Detect(ctx context.Context, env *Env, in Input) ([]Annotation, error)
}

// Retriever assembles context items. Fail-open like Detector.
type Retriever interface {
	Name() string
	Retrieve(ctx context.Context, env *Env, in Input) ([]ContextItem, error)
}
