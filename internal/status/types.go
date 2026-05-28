package status

import "time"

// JSONOutput is the JSON output structure for ox status --json.
//
// Ledger and TeamContexts are deprecated mirrors retained for one release
// while consumers migrate to the unified Bubbles field. New consumers
// should read Bubbles; legacy consumers continue to read Ledger /
// TeamContexts unchanged. See plan: "ox status" — collapse to bubbles
// summary; keep mirrors one release.
type JSONOutput struct {
	Auth         *AuthJSON         `json:"auth"`
	Config       *ConfigJSON       `json:"config"`
	Project      *ProjectJSON      `json:"project"`
	Bubbles      *BubblesJSON      `json:"bubbles,omitempty"`
	Ledger       *LedgerJSON       `json:"ledger"`
	TeamContexts []TeamContextJSON `json:"team_contexts,omitempty"`
	AICoworkers  []AICoworkerJSON  `json:"ai_coworkers,omitempty"`
	Daemon       *DaemonJSON       `json:"daemon,omitempty"`
	Version      *VersionJSON      `json:"version,omitempty"`
}

// BubblesJSON is the unified knowledge-bubble summary surfaced by
// `ox status --json`. Counts come from the F3 three-source merger
// (internal/kb.Merger). ByType is keyed by kb_type slug ("personal",
// "profile", "team", "repo", "custom", "unknown") and contains only
// non-zero buckets — zero counts are omitted to keep the line short
// and to match the human-readable format. Warnings is one entry per
// fan-out source that errored non-fatally.
type BubblesJSON struct {
	Total    int                 `json:"total"`
	ByType   map[string]int      `json:"by_type,omitempty"`
	Warnings []BubbleWarningJSON `json:"warnings,omitempty"`
}

// BubbleWarningJSON mirrors kb.SourceWarning for JSON output. Defined
// in internal/status to avoid pulling internal/kb into this package.
type BubbleWarningJSON struct {
	Source string `json:"source"`
	Error  string `json:"error"`
}

// AICoworkerJSON represents an AI coworker in JSON output.
//
// ContextTokens is the rolled-up total. ContextTokensBySource splits it
// by content source — SageOx tool overhead, team-authored content,
// project-authored content, and any future knowledge bubble (per-user,
// per-org, etc.). SageOx is judged on the "sageox" entry only; other
// entries reflect authoring choices SageOx does not control. See
// prime.ContextBudget for the rationale.
//
// The map is open: future knowledge bubbles add entries by tagging
// emit sites in cmd/ox/agent_prime_xml.go with a new source constant.
// No schema migration required.
type AICoworkerJSON struct {
	AgentID               string           `json:"agent_id"`
	ContextTokens         int64            `json:"context_tokens"`
	ContextTokensBySource map[string]int64 `json:"context_tokens_by_source,omitempty"`
	CommandCount          int              `json:"command_count"`
	Status                string           `json:"status"`
	Age                   string           `json:"age"`
}

// VersionJSON represents version info in JSON output.
type VersionJSON struct {
	Current         string `json:"current"`
	Latest          string `json:"latest,omitempty"`
	UpdateAvailable bool   `json:"update_available"`
}

// AuthJSON represents authentication info in JSON output.
type AuthJSON struct {
	Authenticated bool       `json:"authenticated"`
	Endpoint      string     `json:"endpoint"`
	User          string     `json:"user,omitempty"`
	Email         string     `json:"email,omitempty"`
	ExpiresAt     *time.Time `json:"expires_at,omitempty"`
	GitPATValid   *bool      `json:"git_pat_valid,omitempty"`
	GitPATReason  string     `json:"git_pat_reason,omitempty"`
	Error         string     `json:"error,omitempty"`
}

// ConfigJSON represents configuration info in JSON output.
type ConfigJSON struct {
	UserConfigDir  string `json:"user_config_dir"`
	AuthFile       string `json:"auth_file"`
	AuthFileExists bool   `json:"auth_file_exists"`
}

// ProjectJSON represents project info in JSON output.
type ProjectJSON struct {
	Initialized bool           `json:"initialized"`
	Directory   string         `json:"directory"`
	ConfigPath  string         `json:"config_path,omitempty"`
	CodeIndex   *CodeIndexJSON `json:"code_index,omitempty"`
}

// CodeIndexJSON represents code index info in JSON output.
type CodeIndexJSON struct {
	Indexed     bool       `json:"indexed"`
	LastIndexed *time.Time `json:"last_indexed,omitempty"`
	IndexingNow bool       `json:"indexing_now"`
	Commits     int        `json:"commits,omitempty"`
	Blobs       int        `json:"blobs,omitempty"`
	Symbols     int        `json:"symbols,omitempty"`
	Error       string     `json:"error,omitempty"`
}

// LedgerJSON represents ledger info in JSON output.
type LedgerJSON struct {
	Configured  bool   `json:"configured"`
	Path        string `json:"path,omitempty"`
	Exists      bool   `json:"exists"`
	Branch      string `json:"branch,omitempty"`
	Status      string `json:"status,omitempty"`
	Error       string `json:"error,omitempty"`
	Visibility  string `json:"visibility,omitempty"`
	AccessLevel string `json:"access_level,omitempty"`
}

// TeamContextJSON represents team context info in JSON output.
type TeamContextJSON struct {
	TeamID   string     `json:"team_id"`
	TeamName string     `json:"team_name,omitempty"`
	Path     string     `json:"path"`
	Exists   bool       `json:"exists"`
	Branch   string     `json:"branch,omitempty"`
	Status   string     `json:"status,omitempty"`
	Error    string     `json:"error,omitempty"`
	LastSync *time.Time `json:"last_sync,omitempty"`
	Stale    bool       `json:"stale,omitempty"`
}

// DaemonJSON represents daemon info in JSON output.
type DaemonJSON struct {
	Running       bool             `json:"running"`
	Pid           int              `json:"pid,omitempty"`
	UptimeSeconds int64            `json:"uptime_seconds,omitempty"`
	TotalSyncs    int              `json:"total_syncs,omitempty"`
	SyncsLastHour int              `json:"syncs_last_hour,omitempty"`
	LastError     string           `json:"last_error,omitempty"`
	AgentWorker   *AgentWorkerJSON `json:"agent_worker,omitempty"`
}

// AgentWorkerJSON represents the agent worker status in JSON output.
type AgentWorkerJSON struct {
	Agent         string `json:"agent"`  // resolved agent: "claude", "codex", or "none"
	Source        string `json:"source"` // "auto", "configured", or "disabled"
	Authenticated bool   `json:"authenticated"`
	AuthDetail    string `json:"auth_detail,omitempty"`
}

// GitRepoStatus holds information about a git repository's status.
//
// Wedge fields (RebaseInProgress, AheadCount, BehindCount) are populated
// by the `ox status` collector and consumed by the renderer to surface
// 'wedged ledger' guidance to the user — see ox-un4u for the failure
// mode they prevent (a stuck rebase invisible to ox status until the
// user's next session-stop fails to push).
type GitRepoStatus struct {
	Path             string
	Exists           bool
	Branch           string
	UncommittedCount int
	IsSynced         bool
	HasLastSync      bool
	LastSync         time.Time

	// AheadCount is the number of local commits not yet on the upstream
	// branch. Zero when no upstream tracking is configured.
	AheadCount int

	// BehindCount is the number of upstream commits not yet pulled
	// locally. Zero when no upstream tracking is configured.
	BehindCount int

	// RebaseInProgress reports whether the working tree has an
	// in-progress rebase (.git/rebase-merge or .git/rebase-apply
	// directory present). The most direct signal of a wedge.
	RebaseInProgress bool

	// IncompleteHistory reports whether the repo is shallow or partial
	// (lazy-fetch promisor). When true, AheadCount/BehindCount are not
	// computable; both are zero and UI should render a sentinel ("—" /
	// null) rather than treat the zeros as a confident answer.
	IncompleteHistory bool

	// IncompleteReason is a short human-readable description of why
	// history is incomplete (e.g. "shallow clone", "partial clone").
	// Empty when IncompleteHistory is false.
	IncompleteReason string

	Error string
}

// IsWedged reports whether the repo is in a state that needs user (or
// `ox doctor --fix`) intervention before the next push can succeed.
// True when a rebase is stuck or the branch has diverged with both
// sides ahead — the two states `ox doctor --fix` is built to repair.
func (s GitRepoStatus) IsWedged() bool {
	if s.RebaseInProgress {
		return true
	}
	// True divergence: both sides have commits the other doesn't.
	// Pure ahead (uncommitted local progress) and pure behind (just
	// haven't pulled yet) are not wedges.
	return s.AheadCount > 0 && s.BehindCount > 0
}
