// Package usage builds the `ox session report` report: what ox did in an AI coworker's
// session — the ox calls it made, how much context ox put in front of the
// model, which team items were surfaced by name, and which of those the AI
// coworker reports it actually used.
//
// The package is pure: callers resolve the trace, recording state and score
// and hand them in (Input); Build turns them into a Report; Display renders
// the plain-text block both harnesses print verbatim. Behavior for the AI
// coworker lives in the Report's guidance field, not in the skill body, so
// every adapter gets identical behavior.
package usage

// View names which report a payload is.
const (
	ViewSession = "session"
	ViewMe      = "me"
	ViewTeam    = "team"
)

// Trace sources: where a session report's events were read from.
const (
	TraceSourceSession    = "session"     // the live recording's context-trace.jsonl, or a ledger session's
	TraceSourceAgentCache = "agent-cache" // .sageox/cache/context-trace/<agentID>.jsonl (not recording)
	TraceSourceNone       = "none"
)

// Caps keep the payload's token cost bounded.
const (
	maxRecent   = 10
	maxSurfaced = 15
	maxByCmd    = 12
)

// Unmetered names context ox puts in front of the model that v1 does not
// measure. Reported so the token total never reads as exhaustive.
var Unmetered = []string{"plan nudges", "agent-task surface"}

// Report is the `ox session report --json` payload for the session view.
type Report struct {
	View     string      `json:"view"`
	Session  SessionInfo `json:"session"`
	Trace    TraceInfo   `json:"trace"`
	Calls    Calls       `json:"calls"`
	Context  Context     `json:"context"`
	Surfaced []Surfaced  `json:"surfaced"`
	// SurfacedByType counts every surfaced item by ref_type, before the
	// surfaced list's display cap, so "available" counts stay whole.
	SurfacedByType map[string]int `json:"surfaced_by_type,omitempty"`
	Relevant       *Relevant      `json:"relevant,omitempty"`
	Used           Used           `json:"used"`
	Attribution    Attribution    `json:"attribution"`
	Display        string         `json:"display"`
	Guidance       string         `json:"guidance"`
	NextActions    []NextAction   `json:"next_actions"`
}

// SessionInfo identifies the session the report covers.
type SessionInfo struct {
	Name      string `json:"name,omitempty"`
	AgentID   string `json:"agent_id,omitempty"`
	Harness   string `json:"harness,omitempty"`
	Recording bool   `json:"recording"`
	Turns     int    `json:"turns,omitempty"`
}

// TraceInfo says whether a usage trace was found and where.
type TraceInfo struct {
	Available bool   `json:"available"`
	Source    string `json:"source"`
	Reason    string `json:"reason,omitempty"`
}

// Calls aggregates the ox calls (invoked events) in the trace.
type Calls struct {
	Total     int           `json:"total"`
	Failed    int           `json:"failed"`
	ByCommand []CommandStat `json:"by_command"`
	Recent    []RecentCall  `json:"recent"`
}

// CommandStat is one label+command bucket.
type CommandStat struct {
	Label   string `json:"label"`
	Command string `json:"command"`
	Count   int    `json:"count"`
	Tokens  int    `json:"tokens"`
	LastSeq int    `json:"last_seq,omitempty"`
}

// RecentCall is one ox call, newest first.
type RecentCall struct {
	Label   string `json:"label"`
	Command string `json:"command"`
	Seq     int    `json:"seq,omitempty"`
	Tokens  int    `json:"tokens"`
	Failed  bool   `json:"failed,omitempty"`
	TS      string `json:"ts,omitempty"`
}

// Context is the estimated context volume ox put in front of the model.
type Context struct {
	Tokens    int          `json:"tokens"`
	Estimated bool         `json:"estimated"`
	BySource  []SourceStat `json:"by_source"`
	Prime     *PrimeBudget `json:"prime,omitempty"`
	Unmetered []string     `json:"unmetered"`
}

// SourceStat is one context-volume bucket.
type SourceStat struct {
	Source string `json:"source"`
	Tokens int    `json:"tokens"`
	Count  int    `json:"count,omitempty"`
}

// PrimeBudget is the latest prime's size, and what a hook cap withheld.
type PrimeBudget struct {
	EmittedBytes int      `json:"emitted_bytes"`
	FullBytes    int      `json:"full_bytes,omitempty"`
	Deferred     []string `json:"deferred,omitempty"`
}

// Surfaced is one team item put in front of the model by name. Title, URL,
// FromName, Seq and Used are optional: they are rendered when the trace
// carries them, and URL is derived from an opaque discussion, session or plan id when
// Input.Endpoint is known — never guessed for a slug, doc or path.
type Surfaced struct {
	Ref      string    `json:"ref"`
	RefType  string    `json:"ref_type"`
	From     string    `json:"from,omitempty"`
	Topic    string    `json:"topic,omitempty"`
	Via      string    `json:"via"` // prime | whisper | recall | retrieval
	Title    string    `json:"title,omitempty"`
	URL      string    `json:"url,omitempty"`
	FromName string    `json:"from_name,omitempty"`
	Seq      int       `json:"seq,omitempty"`
	Used     *ItemUsed `json:"used,omitempty"`
}

// Use-evidence kinds for ItemUsed.Evidence.
const (
	EvidenceRead     = "read"
	EvidenceCited    = "cited"
	EvidenceReported = "reported"
	EvidenceJudged   = "judged"
)

// ItemUsed is the evidence that a surfaced item was actually used. Present
// only when there is evidence; absence means the item was available.
type ItemUsed struct {
	Evidence string `json:"evidence"` // read | cited | reported | judged
	Seq      int    `json:"seq,omitempty"`
	Grade    string `json:"grade,omitempty"` // provable | reported | judged
}

// Relevant is the surfaced-vs-used rollup: Count of the SurfacedCount items
// have use evidence, by evidence bucket, with the used items and the
// attribution category that evidence derives on its own. Nil when the trace
// is unavailable.
type Relevant struct {
	Count         int            `json:"count"`
	SurfacedCount int            `json:"surfaced_count"`
	ByEvidence    map[string]int `json:"by_evidence,omitempty"` // always read, cited, reported, judged
	Items         []RelevantItem `json:"items,omitempty"`
	// ReportedNone — the AI coworker explicitly reported that nothing
	// surfaced was used, and no other use evidence contradicts it.
	ReportedNone bool     `json:"reported_none,omitempty"`
	Derived      *Derived `json:"derived,omitempty"`
}

// Derived is the category a use-evidence summary implies.
type Derived struct {
	Category string `json:"category"`
	Reason   string `json:"reason,omitempty"`
}

// RelevantItem is one used ref at its strongest evidence. Evidence is the
// display bucket (read | cited | reported | judged); Detail is the raw
// evidence behind it (read | requery | cited | cited_commit | reported |
// judged) so a renderer can say "re-opened" or "cited in commit 3e1c9d0".
// Why is the AI coworker's own one-line --why — the only free text here.
type RelevantItem struct {
	Ref      string `json:"ref"`
	RefType  string `json:"ref_type,omitempty"`
	Title    string `json:"title,omitempty"`
	URL      string `json:"url,omitempty"`
	Evidence string `json:"evidence"` // display bucket
	Detail   string `json:"detail,omitempty"`
	Grade    string `json:"grade"`
	Seq      int    `json:"seq,omitempty"`
	Commit   string `json:"commit,omitempty"` // short sha, cited_commit only
	Why      string `json:"why,omitempty"`
}

// Attribution is display-only: whether the session's score clears the
// project's attribution threshold. It never changes commit trailers.
type Attribution struct {
	WouldAttribute bool    `json:"would_attribute"`
	Category       string  `json:"category"`
	Reason         string  `json:"reason,omitempty"`
	Source         string  `json:"source"` // self-reported (a manual score exists) | derived
	Threshold      float64 `json:"threshold"`
}

// Attribution sources.
const (
	AttributionSelfReported = "self-reported"
	AttributionDerived      = "derived"
)

// Used is the evidence of what the AI coworker actually used, by grade.
type Used struct {
	SelfReported    []SelfReported `json:"self_reported"`
	Retrieval       []Retrieval    `json:"retrieval"`
	Consulted       []Consulted    `json:"consulted"`
	SageoxScore     *Score         `json:"sageox_score,omitempty"`
	ProducedCommits int            `json:"produced_commits"`
	SelfReport      *SelfReportCmd `json:"self_report,omitempty"`
}

// SelfReported is an `influenced` event the AI coworker wrote.
type SelfReported struct {
	Ref             string `json:"ref"`
	RefType         string `json:"ref_type,omitempty"`
	Decision        string `json:"decision,omitempty"`
	Grade           string `json:"grade"` // always "self-assessed"
	MatchedSurfaced bool   `json:"matched_surfaced"`
}

// Retrieval is an ox retrieval call the AI coworker ran.
type Retrieval struct {
	Label string `json:"label"`
	Query string `json:"query,omitempty"`
	Seq   int    `json:"seq,omitempty"`
	Grade string `json:"grade"` // always "retrieval"
}

// Consulted is a deterministic consultation tag from the session scan.
type Consulted struct {
	Ref       string `json:"ref,omitempty"`
	RefType   string `json:"ref_type,omitempty"`
	Mechanism string `json:"mechanism,omitempty"`
	Seq       int    `json:"seq,omitempty"`
}

// Score is the session's SageOx contribution score.
type Score struct {
	Category string `json:"category"`
	Reason   string `json:"reason,omitempty"`
}

// SelfReportCmd tells the AI coworker how to report what it used: Command
// records one use at the moment it happens; the batch path takes JSONL lines
// shaped like BatchExampleLine on stdin.
type SelfReportCmd struct {
	Command          string `json:"command"`
	BatchCommand     string `json:"batch_command"`
	BatchExampleLine string `json:"batch_example_line"`
}

// NextAction is a suggested follow-up command.
type NextAction struct {
	Action string `json:"action"`
	Why    string `json:"why"`
}

// guidanceText is the honesty contract for narrating a session report. It is
// load-bearing: the AI coworker claims use only from relevant.items evidence,
// prints the pre-rendered display so the report reads identically in every
// harness, and reports its own use quietly, at the moment of use. Behavior
// lives here, in the command's JSON, rather than in the ox-cli-session-report skill body.
const guidanceText = "Show the coworker what SageOx gave this session. " +
	"(1) Print the display field verbatim inside a fenced ```text block, then at most 3 sentences of " +
	"your own. " +
	"(2) Items in relevant.items were used: name each one's evidence (detail). Items only in surfaced[] " +
	"were available, not used. Token counts are estimates, say so once. Never state or imply time saved " +
	"or dollar figures. attribution is display-only and never changes commit trailers. " +
	"(3) If something SageOx surfaced changed what you did and is not in relevant.items, run " +
	"used.self_report.command for it once, now (its ref, --why one line saying what you did because " +
	"of it). Do not tell the coworker about reporting or its absence. " +
	"(4) If trace.available is false, say there is no trace, give trace.reason, and point to " +
	"`ox session start`."
