// Package recap answers "What value am I getting from SageOx?" in tight prose
// pointing at the explicit team-context artifacts (the Constitution, the
// glossary, recorded decisions) that reached the user's coding sessions.
//
// The package is a read-only miner over data that already exists on disk: each
// session's context-trace.jsonl records which team-context docs prime injected
// (`provided` events), and those docs are real files with quotable content. The
// miner assembles a grounded evidence bundle with receipts; the calling agent —
// already sitting in the user's session — narrates the personalized prose from
// it. No network, no LLM in the CLI, no writes.
//
// Design invariant: never lead with a bare statistic about SageOx itself
// ("47 sessions primed"). Every headline is a NAMED artifact or decision, and
// every claim carries a receipt (artifact path, session name, or commit SHA).
package recap

import (
	"time"

	"github.com/sageox/ox/internal/lfs"
)

// Output is the evidence bundle. In agent mode it is emitted as JSON for the
// calling agent to narrate; in human mode a deterministic renderer turns it
// into honest prose. Field order mirrors the narrative arc: what team knowledge
// reached you, what decisions you inherited, what you shipped, what your team
// has built, and — when the bundle is thin — what to do next.
type Output struct {
	User             string          `json:"user"`
	Scope            string          `json:"scope"` // "personal"
	Since            time.Time       `json:"since"`
	Until            time.Time       `json:"until"`
	Repo             string          `json:"repo,omitempty"`
	Team             string          `json:"team,omitempty"`
	Coverage         Coverage        `json:"coverage"`
	ArtifactsReached []ArtifactReach `json:"artifacts_that_reached_you,omitempty"`
	KnowledgeFlow    *KnowledgeFlow  `json:"knowledge_flow,omitempty"`
	SettledDecisions []Decision      `json:"settled_decisions,omitempty"`
	PlansEnriched    []PlanEnriched  `json:"plans_enriched,omitempty"`
	YourWork         []WorkItem      `json:"your_work,omitempty"`
	TeamContextBuilt []TeamArtifact  `json:"team_context_built,omitempty"`
	NextActions      []NextAction    `json:"next_actions,omitempty"`
	Guidance         string          `json:"guidance,omitempty"`
	Hints            *Hints          `json:"hints,omitempty"`

	// WindowLabel is a human phrase for the reporting window ("last 30 days"),
	// used only by the terminal renderer. Not serialized — JSON consumers read
	// the machine-precise Since/Until instead.
	WindowLabel string `json:"-"`
}

// Coverage is the honesty denominator — how much of the user's history we could
// actually read. Dehydrated traces are counted, never silently dropped.
// LedgerAllTime is the solo-value signal: the full depth of searchable memory
// the user has accumulated, independent of the reporting window.
type Coverage struct {
	SessionsInWindow int `json:"sessions_in_window"`
	LedgerAllTime    int `json:"ledger_all_time"`
	WithTraces       int `json:"with_traces"`
	TracesDehydrated int `json:"traces_dehydrated"`
}

// PlanEnriched records a plan SageOx enriched from the user's own history —
// what it caught before they wrote code. Pure solo value: it works with a team
// of one.
type PlanEnriched struct {
	Slug         string    `json:"slug"`
	Topic        string    `json:"topic,omitempty"`
	When         time.Time `json:"when"`
	Collisions   int       `json:"collisions"`
	PriorArt     int       `json:"prior_art"`
	ExpertRoutes int       `json:"expert_routes"`
	Receipt      string    `json:"receipt,omitempty"` // plan dir
}

// ArtifactReach is the spine of the report: a specific team-context artifact
// that prime injected into the user's sessions, with its real title and a
// quotable snippet so the agent can point at it by name. The receipt is the
// artifact's on-disk path; SampleWork names sessions it reached.
type ArtifactReach struct {
	Doc         string    `json:"doc"`                   // filename, e.g. "principles.md"
	Title       string    `json:"title,omitempty"`       // e.g. "The SageOx Constitution"
	Snippet     string    `json:"snippet,omitempty"`     // salient quotable content
	Source      string    `json:"source"`                // context-trace source type
	Sessions    int       `json:"sessions"`              // how many of your sessions it reached
	SampleWork  []string  `json:"sample_work,omitempty"` // titles of reached sessions (receipts, capped)
	LatestReach time.Time `json:"latest_reach,omitempty"`
	Receipt     string    `json:"receipt,omitempty"` // artifact path
}

// KnowledgeFlow is the report's most compelling axis — and the one that needs
// instrumentation before it can carry real receipts. It describes causal
// INFLUENCE, not mere co-occurrence:
//   - a team decision made outside coding (a discussion) that shaped a plan or
//     implementation,
//   - one coworker's session that influenced another's work,
//   - a knowledge-bubble distillation that shaped the plans and work within a
//     session.
//
// None of that is captured on-disk today (no influenced/consulted events, no
// distilled-discussion injection trace), so `Flows` is empty and `Pending`
// carries an honest placeholder. When the consulted/influenced instrumentation
// lands, Available flips true and Flows carries the real chains. The report
// NEVER fabricates a flow the data doesn't have — the placeholder is the honest
// stand-in, per the same receipts-not-vibes rule as the rest of the bundle.
type KnowledgeFlow struct {
	Available bool     `json:"available"`
	Flows     []string `json:"flows,omitempty"`
	Pending   string   `json:"pending,omitempty"`
}

// Decision is an already-settled call the user (or their agent) recorded in a
// prior session — team knowledge they inherited and did not have to
// re-litigate. Sourced from a session's summary.json.
type Decision struct {
	What    string `json:"what"`
	Why     string `json:"why,omitempty"`
	Owner   string `json:"owner,omitempty"`
	Session string `json:"session"`           // session name it was recorded in
	Receipt string `json:"receipt,omitempty"` // "session:<name>"
}

// WorkItem is a session's worth of the user's own work — the "what you did"
// that the prose pairs each artifact against. Commits are trailered SHAs that
// resolve back to this session.
type WorkItem struct {
	Session string    `json:"session"`
	Title   string    `json:"title,omitempty"`
	When    time.Time `json:"when"`
	Commits []string  `json:"commits,omitempty"` // "<sha> <subject>"
}

// TeamArtifact is knowledge the team has built into shared context that now
// reaches everyone — independent of who authored it. Powers the "value to your
// team" half of the answer and the cold-start story ("your team has these;
// they will reach your future sessions").
type TeamArtifact struct {
	Doc     string `json:"doc"`
	Title   string `json:"title,omitempty"`
	Kind    string `json:"kind"` // "doc" | "discussion" | "memory"
	Snippet string `json:"snippet,omitempty"`
	Receipt string `json:"receipt,omitempty"`
}

// NextAction is a concrete step that would start (or increase) the value the
// user gets — surfaced when a section is thin, and leading the whole report for
// a cold-start user.
type NextAction struct {
	Action string `json:"action"` // command or step
	Why    string `json:"why"`    // the value it unlocks
}

// Hints are progressive-disclosure pointers for the agent: how to drill in and
// how to independently verify the numbers.
type Hints struct {
	Drilldown string `json:"drilldown,omitempty"`
	Verify    string `json:"verify,omitempty"`
}

// Identity resolves "you". The command fills it from auth/config; the miner
// matches it against each session's meta, strongest signal first.
type Identity struct {
	UserID      string
	DisplayName string
	Slug        string
}

// Matches reports whether a session belongs to this identity. The chain is
// strongest-first: stable user id, then privacy-safe display name, then the
// username slug parsed from the session folder name (covers legacy metas that
// predate the username field).
func (id Identity) Matches(meta *lfs.SessionMeta, sessionName string) bool {
	if id.UserID != "" && meta.UserID != "" {
		return id.UserID == meta.UserID
	}
	if id.DisplayName != "" && meta.Username != "" {
		return equalFold(id.DisplayName, meta.Username)
	}
	if id.Slug != "" {
		return equalFold(id.Slug, usernameSlug(sessionName))
	}
	return false
}

// BuildInput carries the resolved paths and scope the miner needs. The command
// resolves these (ledger path, team path, identity) and hands them in, keeping
// the package free of cmd-layer dependencies.
type BuildInput struct {
	LedgerPath  string
	TeamPath    string
	TeamName    string
	ProjectRoot string
	RepoName    string
	Identity    Identity
	Since       time.Time
	Until       time.Time
	Now         time.Time
}

const (
	// maxArtifacts caps the artifact list so the bundle stays scannable.
	maxArtifacts = 12
	// maxSampleWork caps session-title receipts per artifact.
	maxSampleWork = 3
	// maxDecisions caps inherited decisions.
	maxDecisions = 8
	// maxPlans caps enriched-plan entries (and the plan.Load calls behind them)
	// so a window with many plans stays scannable, like every sibling miner.
	maxPlans = 8
	// maxFlows caps knowledge-flow chains in the influence section.
	maxFlows = 8
	// maxWorkItems caps the "your work" list.
	maxWorkItems = 10
	// maxTeamArtifacts caps the team-context-built list.
	maxTeamArtifacts = 12
	// snippetMax caps a quoted snippet's length (chars).
	snippetMax = 320
)

// guidanceText instructs the calling agent how to narrate the bundle. It is the
// load-bearing honesty contract: the agent must ground every claim in a receipt
// and must never fall back to bare counts or invent value. Behavior lives here
// (in the command's JSON) rather than in the ox-recap skill body, so every
// adapter — Claude, Codex, others — gets identical behavior.
const guidanceText = "Write a tight, personal, prose answer to the user's question " +
	"\"what value am I getting from SageOx?\" — why to keep using it and what value it has " +
	"actually delivered. There are TWO value axes; use whichever the evidence supports. " +
	"(1) SOCIAL — team knowledge that reached this person's work (artifacts_that_reached_you, " +
	"team_context_built): quote the named artifact by title and say what it saved them from " +
	"re-deriving. (2) TEMPORAL / SOLO — their own ledger as compounding memory " +
	"(coverage.ledger_all_time recorded sessions they can search and reload, settled_decisions " +
	"they captured, plans_enriched where SageOx caught a collision or prior-art against their " +
	"OWN history, your_work they shipped). For a solo player with little or no team context, " +
	"LEAD WITH THE TEMPORAL AXIS — it is real value, not a deficiency: they never start cold, " +
	"never re-solve a problem, and their decisions resurface instead of being re-litigated. " +
	"Frame adding a teammate or recording a discussion as the NEXT unlock (the social axis), " +
	"never as something missing. Ground every claim in a receipt from this bundle — an artifact " +
	"path, a session name, a plan slug, a commit SHA. NEVER invent value, NEVER cite time-saved " +
	"or dollar figures, and NEVER lead with a bare statistic about SageOx itself. If the user " +
	"has no recorded sessions at all, lead with next_actions and say plainly the value starts " +
	"the moment they begin. knowledge_flow is the causal-influence axis (a discussion's decision " +
	"shaping a plan, a teammate's session steering your work, a distillation influencing a " +
	"session): if knowledge_flow.available is false, you MAY note it's coming, but NEVER invent an " +
	"influence chain — that data isn't instrumented yet."
