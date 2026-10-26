// Package usage builds the `ox brief` report: what ox did in an AI coworker's
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

// Used statuses.
const (
	// UsedNotRecording — no live recording, so the AI coworker cannot add a
	// self-report now.
	UsedNotRecording = "not_recording"
	// UsedNotYetReported — recording, but no self-report yet.
	UsedNotYetReported = "not_yet_reported"
	// UsedReported — at least one self-report is in the trace.
	UsedReported = "reported"
)

// Caps keep the payload's token cost bounded.
const (
	maxRecent   = 10
	maxSurfaced = 15
	maxByCmd    = 12
)

// Unmetered names context ox injects that v1 does not measure. Reported so
// the token total never reads as exhaustive.
var Unmetered = []string{"recall preamble", "plan nudges", "agent-task surface"}

// Report is the `ox brief --json` payload for the session view.
type Report struct {
	View        string       `json:"view"`
	Session     SessionInfo  `json:"session"`
	Trace       TraceInfo    `json:"trace"`
	Calls       Calls        `json:"calls"`
	Injected    Injected     `json:"injected"`
	Surfaced    []Surfaced   `json:"surfaced"`
	Used        Used         `json:"used"`
	Display     string       `json:"display"`
	Guidance    string       `json:"guidance"`
	NextActions []NextAction `json:"next_actions"`
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

// Injected is the estimated context volume ox put in front of the model.
type Injected struct {
	TotalTokens int          `json:"total_tokens"`
	Estimated   bool         `json:"estimated"`
	BySource    []SourceStat `json:"by_source"`
	Prime       *PrimeBudget `json:"prime,omitempty"`
	Unmetered   []string     `json:"unmetered"`
}

// SourceStat is one injected-volume bucket.
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

// Surfaced is one team item put in front of the model by name.
type Surfaced struct {
	Ref     string `json:"ref"`
	RefType string `json:"ref_type"`
	From    string `json:"from,omitempty"`
	Topic   string `json:"topic,omitempty"`
	Via     string `json:"via"` // prime | whisper | retrieval
}

// Used is the evidence of what the AI coworker actually used, by grade.
type Used struct {
	Status          string         `json:"status"`
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

// SelfReportCmd tells the AI coworker how to report what it used.
type SelfReportCmd struct {
	Command     string `json:"command"`
	ExampleLine string `json:"example_line"`
}

// NextAction is a suggested follow-up command.
type NextAction struct {
	Action string `json:"action"`
	Why    string `json:"why"`
}

// guidanceText is the honesty contract for narrating a session report. It is
// load-bearing: the AI coworker must ground every "used" claim in the used
// section, report its own use before showing, and print the pre-rendered
// display so the report reads identically in every harness. Behavior lives
// here, in the command's JSON, rather than in the ox-cli-brief skill body.
const guidanceText = "Show the coworker what ox did in this session. " +
	"(1) REPORT FIRST: if used.status is \"not_yet_reported\", before showing anything decide which " +
	"surfaced[] items actually changed what you did this session. Report ALL of them in ONE call: pipe " +
	"one JSON line per item (JSONL) to used.self_report.command, each shaped like " +
	"used.self_report.example_line — type \"influenced\", mechanism \"self-report\", the item's ref and " +
	"ref_type, and a one-line decision saying what you did because of it. If no surfaced item changed " +
	"what you did, report nothing. After reporting, re-run `ox brief --json` and show THAT result, not " +
	"this one. " +
	"(2) Print the display field verbatim inside a fenced ```text block, then at most 3 sentences of " +
	"your own. " +
	"(3) Never claim ox context was used unless it appears in used: say \"you reported\" for " +
	"self_reported items and \"ran\" for retrieval items. Token counts are estimates (~4 bytes per " +
	"token) — say so once. Never state or imply time saved or dollar figures. " +
	"(4) If trace.available is false, say there is no usage trace for this session, give " +
	"trace.reason, and point to `ox session start` to record the next one."
