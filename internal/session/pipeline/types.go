// Package pipeline defines types and constants for the session upload pipeline.
//
// The session upload pipeline processes agent session data through these phases:
//
//	Phase 1 (cache): redact secrets -> write raw.jsonl, markdown
//	Phase 2 (ledger): copy files -> LFS upload -> write meta.json -> git commit+push
//
// This package contains the shared types, constants, and pure functions used across
// both phases. It does NOT contain cobra commands, LFS upload logic, or git operations.
package pipeline

// Ledger artifact filenames — single source of truth used by both
// the upload path (write) and post-prune path rewrite (read-back).
const (
	LedgerFileRaw          = "raw.jsonl"
	LedgerFileSummaryMD    = "summary.md"
	LedgerFileSessionMD    = "session.md"
	LedgerFilePlan         = "plan.md"             // may contain multiple plans as separate Markdown sections
	LedgerFileContextTrace = "context-trace.jsonl" // context influence trace (what context was provided and what influenced decisions)
)

// LedgerContentFiles is the canonical ordered list of session artifact
// filenames that are part of a committed session: copied from cache to
// ledger (CopySessionToLedger + SecondaryArtifacts) AND uploaded to LFS
// (internal/lfs.ContentFiles derives from this list).
//
// Previously this list was duplicated across internal/lfs and the copy
// path — a silent drift hazard: adding a new artifact to one location
// and not the other would cause the file to land in the ledger dir but
// never upload to LFS, surfacing days later as a GitLab LFS-objects-
// missing error on an unrelated push.
//
// To add a new session artifact: add its const above and append its
// filename here. Both call sites pick it up automatically.
var LedgerContentFiles = []string{
	LedgerFileRaw,
	LedgerFileSummaryMD,
	LedgerFileSessionMD,
	LedgerFilePlan,
	LedgerFileContextTrace,
}

// Result contains outcomes from session processing.
// Populated incrementally: first by processAgentSession (phase 1),
// then by uploadSessionToLedger (phase 2).
type Result struct {
	RawPath          string
	SummaryMDPath    string
	SessionMDPath    string
	EntryCount       int
	SecretsRedacted  int
	AgentVersion     string
	Model            string
	Summary          string   // local summary text
	SummaryPrompt    string   // prompt for calling agent to generate full summary
	PlanPath         string   // path to plan.md (empty if no plan captured)
	ContextTracePath string   // path to context-trace.jsonl (empty if no context trace captured)
	SessionName      string   // ledger session folder name (e.g. 2026-02-06T14-32-ryan-Ox7f3a)
	LedgerSessionDir string   // full path to session dir in ledger (empty if upload failed)
	UploadWarning    string   // non-empty when ledger upload failed (explains recovery)
	DataWarnings     []string // data quality warnings from validation (reported to agent)
	UploadMs         int64    // time spent on LFS upload + git push (ms)
}

// StartOutput is the JSON output format for session start.
type StartOutput struct {
	Success  bool   `json:"success"`
	Type     string `json:"type"`
	AgentID  string `json:"agent_id"`
	Title    string `json:"title,omitempty"`
	Adapter  string `json:"adapter"`
	Started  string `json:"started"`
	Hint     string `json:"hint,omitempty"`     // suggests how to end recording
	Notice   string `json:"notice,omitempty"`   // one-time notice the agent MUST show to the user
	Guidance string `json:"guidance,omitempty"` // behavioral guidance for the agent during the session
	// Generic adapter fields (omitted for deep adapters)
	SessionFile string   `json:"session_file,omitempty"`
	FormatHint  string   `json:"format_hint,omitempty"`
	NextActions []string `json:"next_actions,omitempty"`
}

// StopOutput is the JSON output format for session stop.
type StopOutput struct {
	Success          bool             `json:"success"`
	Type             string           `json:"type"`
	AgentID          string           `json:"agent_id"`
	Title            string           `json:"title,omitempty"`
	Duration         string           `json:"duration"`
	RawPath          string           `json:"raw_path,omitempty"`
	SummaryMDPath    string           `json:"summary_md_path,omitempty"`
	SessionMDPath    string           `json:"session_md_path,omitempty"`
	PlanPath         string           `json:"plan_path,omitempty"`
	ContextTracePath string           `json:"context_trace_path,omitempty"`
	EntryCount       int              `json:"entry_count,omitempty"`
	SecretsRedacted  int              `json:"secrets_redacted,omitempty"`
	Summary          string           `json:"summary,omitempty"`
	SummaryPrompt    string           `json:"summary_prompt,omitempty"`
	Model            string           `json:"model,omitempty"`
	AgentVersion     string           `json:"agent_version,omitempty"`
	LedgerSessionDir string           `json:"ledger_session_dir,omitempty"` // path to session dir in ledger
	UploadWarning    string           `json:"upload_warning,omitempty"`     // set when ledger upload failed
	DataWarnings     []string         `json:"data_warnings,omitempty"`      // data quality warnings from validation
	Guidance         string           `json:"guidance,omitempty"`           // behavioral guidance for the agent
	TotalMs          int64            `json:"total_ms,omitempty"`           // wall clock for entire session stop
	Timing           map[string]int64 `json:"timing,omitempty"`             // per-phase breakdown (ms)
}

// RemindOutput is the JSON output format for session remind.
type RemindOutput struct {
	Success bool   `json:"success"`
	Type    string `json:"type"`
	AgentID string `json:"agent_id"`
	Message string `json:"message"`
}

// SummarizeOutput is the JSON output format for session summarize.
type SummarizeOutput struct {
	Success       bool     `json:"success"`
	Type          string   `json:"type"`
	AgentID       string   `json:"agent_id"`
	Summary       string   `json:"summary"`
	KeyActions    []string `json:"key_actions,omitempty"`
	Outcome       string   `json:"outcome,omitempty"`
	TopicsFound   []string `json:"topics_found,omitempty"`
	EntryCount    int      `json:"entry_count"`
	FilePath      string   `json:"file_path,omitempty"`
	SummaryPrompt string   `json:"summary_prompt,omitempty"`
}

// SecondaryArtifacts returns the mapping of ledger filenames to source paths
// from a Result. Used by upload and copy logic to iterate over non-critical files.
func (r *Result) SecondaryArtifacts() map[string]string {
	return map[string]string{
		LedgerFileSummaryMD:    r.SummaryMDPath,
		LedgerFileSessionMD:    r.SessionMDPath,
		LedgerFilePlan:         r.PlanPath,
		LedgerFileContextTrace: r.ContextTracePath,
	}
}
