package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/sageox/ox/internal/daemon/agentwork"
	"github.com/sageox/ox/internal/flags"
	whisperstore "github.com/sageox/ox/internal/whisper/store"
)

// maxIPCMessageSize limits the maximum size of an IPC message to prevent DoS.
// A malicious client could send a multi-GB line without newline, causing memory exhaustion.
const maxIPCMessageSize = 1 * 1024 * 1024 // 1MB

// maxConcurrentConnections limits the number of concurrent IPC connections.
// Prevents file descriptor or memory exhaustion from connection floods.
const maxConcurrentConnections = 100

// Message types for IPC communication.
const (
	MsgTypeStatus            = "status"
	MsgTypeSync              = "sync"
	MsgTypeTeamSync          = "team_sync" // on-demand team context sync
	MsgTypePing              = "ping"
	MsgTypeStop              = "stop"
	MsgTypeVersion           = "version"
	MsgTypeSyncHistory       = "sync_history"
	MsgTypeHeartbeat         = "heartbeat"           // one-way, no response expected
	MsgTypeCheckout          = "checkout"            // synchronous git clone operation
	MsgTypeTelemetry         = "telemetry"           // one-way, no response expected
	MsgTypeFriction          = "friction"            // one-way, friction event for analytics
	MsgTypeGetErrors         = "get_errors"          // retrieve unviewed daemon errors
	MsgTypeMarkErrors        = "mark_errors"         // mark errors as viewed
	MsgTypeSessions          = "sessions"            // get active agent sessions (deprecated: use instances)
	MsgTypeInstances         = "instances"           // get active agent instances
	MsgTypeDoctor            = "doctor"              // trigger daemon health checks (anti-entropy, etc.)
	MsgTypeTriggerGC         = "trigger_gc"          // force GC reclone for team contexts
	MsgTypeCodeIndex         = "code_index"          // index local code with progress
	MsgTypeCodeStatus        = "code_status"         // get code index status/stats
	MsgTypeWhispers          = "whispers"            // query whisper entries for an agent
	MsgTypeWhisperHistory    = "whisper_history"     // query all whispers (pending + delivered) without advancing cursor
	MsgTypeSessionFinalize   = "session_finalize"    // one-way, trigger async session upload+finalization
	MsgTypeMurmur            = "murmur"              // one-way, write+commit a murmur file in ledger/team context
	MsgTypeMurmurPause       = "murmur_pause"        // one-way, pause murmur nudging for an agent
	MsgTypeMurmurResume      = "murmur_resume"       // one-way, resume murmur nudging for an agent
	MsgTypeSessionWatchStart = "session_watch_start" // one-way, start tailing a hookless agent session
	MsgTypeSessionWatchStop  = "session_watch_stop"  // one-way, stop tailing a session
	MsgTypeSettingsGet       = "settings_get"        // get cached CLI feature flag settings
	MsgTypeSessionUploaded   = "session_uploaded"    // one-way, session pushed to ledger
)

// Protocol Design Decision: NDJSON (Newline-Delimited JSON)
//
// We use NDJSON (one JSON object per line) instead of length-prefix framing because:
// - Debuggable with standard Unix tools: cat, socat, jq all work directly
// - Human-readable on the wire for troubleshooting
// - JSON encoding handles embedded newlines automatically (\n → \\n)
//
// Length-prefix framing (4-byte length + payload) was considered but rejected:
// - Breaks `echo '{"type":"ping"}' | socat - UNIX:/path/sock` debugging
// - Breaks piping to jq for inspection
// - The embedded newline "problem" doesn't exist: JSON encoding escapes them
//
// See: docs/analysis/february-2026-ipc-analysis.md

// Message represents an IPC message.
type Message struct {
	Type        string `json:"type"`
	WorkspaceID string `json:"workspace_id,omitempty"` // repo-scoped daemon identity
	CallerID    string `json:"caller_id,omitempty"`    // identifies calling clone/worktree (path-based hash)
	// CallerVersion is the ox CLI version of the process making this IPC
	// call. Used by the daemon to detect skew between a long-running
	// daemon and a CLI that's many releases behind (ox-mt3k). Empty
	// when an older client predates the field — the daemon treats
	// missing-version as "unknown skew" (no warning).
	CallerVersion string          `json:"caller_version,omitempty"`
	Payload       json.RawMessage `json:"payload,omitempty"`
}

// Response represents an IPC response.
type Response struct {
	Success bool            `json:"success"`
	Error   string          `json:"error,omitempty"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// StatusData represents daemon status information.
type StatusData struct {
	Running          bool          `json:"running"`
	Pid              int           `json:"pid"`
	Version          string        `json:"version"`
	Uptime           time.Duration `json:"uptime"`
	WorkspacePath    string        `json:"workspace_path,omitempty"`
	LedgerPath       string        `json:"ledger_path"`
	LastSync         time.Time     `json:"last_sync"`
	SyncIntervalRead time.Duration `json:"sync_interval_read"`

	// error tracking
	RecentErrorCount int    `json:"recent_error_count,omitempty"`
	LastError        string `json:"last_error,omitempty"`
	LastErrorTime    string `json:"last_error_time,omitempty"`

	// sync insights
	TotalSyncs    int           `json:"total_syncs,omitempty"`
	SyncsLastHour int           `json:"syncs_last_hour,omitempty"`
	AvgSyncTime   time.Duration `json:"avg_sync_time,omitempty"`

	// workspaces being synced, keyed by type ("ledger", "team-context", "kb")
	// each type maps to a list of workspaces of that type (ledger has 1,
	// team-context and kb may have many)
	Workspaces    map[string][]WorkspaceSyncStatus `json:"workspaces,omitempty"`
	ProjectTeamID string                           `json:"project_team_id,omitempty"` // primary team for this project

	// GlobalSyncOwner reports whether THIS daemon holds the per-endpoint
	// flock lease that gates team-context pulls and knowledge-bubble sync.
	// Followers (false) consume the on-disk state the owner keeps fresh — so
	// kb rows can still be listed by any daemon, but only the owner writes them.
	// See internal/daemon/global_lease.go (bead ox-6zme).
	GlobalSyncOwner bool `json:"global_sync_owner,omitempty"`
	// GlobalSyncEndpoint is the normalized endpoint this daemon's global-sync
	// ownership applies to. Surfaced so a follower can say "synced by another
	// daemon at <endpoint>" rather than implying nothing is syncing.
	GlobalSyncEndpoint string `json:"global_sync_endpoint,omitempty"`

	// team context sync (deprecated: use Workspaces["team-context"] instead)
	TeamContexts []TeamContextSyncStatus `json:"team_contexts,omitempty"`

	// inactivity tracking
	InactivityTimeout time.Duration `json:"inactivity_timeout,omitempty"`
	TimeSinceActivity time.Duration `json:"time_since_activity,omitempty"`

	// heartbeat activity tracking (for sparklines)
	Activity *ActivitySummary `json:"activity,omitempty"`

	// authenticated user (from heartbeat credentials)
	AuthenticatedUser *AuthenticatedUser `json:"authenticated_user,omitempty"`

	// NeedsHelp is true when the daemon has issues requiring LLM reasoning.
	// If the daemon could solve it with deterministic code, it already would have.
	// This is the fast-path check for CLI - just reading a boolean.
	NeedsHelp bool `json:"needs_help"`

	// Issues contains problems the daemon cannot resolve alone.
	// Keyed by (Type, Repo) - only one issue per combination.
	// The LLM inspects repos directly to understand details; daemon just flags repo-level issues.
	// Severity levels: "warning" (address soon), "error" (blocking), "critical" (urgent).
	// No "info" level - if daemon needs help, it's at least a warning.
	Issues []DaemonIssue `json:"issues,omitempty"`

	// UnviewedErrorCount is the number of persisted errors that haven't been viewed.
	// These are errors that persist across daemon restarts for user notification.
	UnviewedErrorCount int `json:"unviewed_error_count,omitempty"`

	// startup timing (how long the daemon took to start)
	StartupDurationMs  int64 `json:"startup_duration_ms,omitempty"`
	ThrottleDurationMs int64 `json:"throttle_duration_ms,omitempty"`

	// open file descriptor count for the daemon process (sampled at status
	// query time). -1 if the platform can't expose it. OpenFDLimit is the
	// soft RLIMIT_NOFILE (0 if unknown). Used to surface FD pressure before
	// the daemon hits the wall — see internal/daemon/fd_count.go for the
	// rationale (a leak that hung lsof in prod went undetected for months
	// because nothing graphed FD count).
	OpenFDs     int    `json:"open_fds,omitempty"`
	OpenFDLimit uint64 `json:"open_fd_limit,omitempty"`

	// code index status
	CodeDB *CodeDBStats `json:"code_db,omitempty"`

	// agent work manager status
	AgentWork *agentwork.AgentWorkStatus `json:"agent_work,omitempty"`

	// adapter process status (populated when supervisor is wired into daemon)
	Adapters map[string]AdapterStatus `json:"adapters,omitempty"`

	// connected clones/worktrees that have sent heartbeats
	Callers []CallerInfo `json:"callers,omitempty"`
}

// BootstrapGracePeriod is the window after daemon start during which zero syncs
// is not considered a problem — the daemon is still performing its first pull.
const BootstrapGracePeriod = 3 * time.Minute

// HasConfiguredRepos reports whether the daemon has repos it should be syncing.
func (s *StatusData) HasConfiguredRepos() bool {
	if s == nil {
		return false
	}
	for _, workspaces := range s.Workspaces {
		if len(workspaces) > 0 {
			return true
		}
	}
	return s.LedgerPath != "" || len(s.TeamContexts) > 0
}

// IsBootstrapping reports true when the daemon just started and hasn't completed
// its first sync yet. Callers use this to soften warnings during the grace period.
// Only true when repos are configured — a daemon with nothing to sync is never bootstrapping.
func (s *StatusData) IsBootstrapping() bool {
	if s == nil || !s.Running {
		return false
	}
	return s.TotalSyncs == 0 && s.Uptime < BootstrapGracePeriod && s.HasConfiguredRepos()
}

// ExtendedStatus provides additional status info for diagnostics.
type ExtendedStatus struct {
	RecentErrorCount int
	LastError        string
	LastErrorTime    string
}

// GetExtendedStatus extracts extended status from StatusData.
// Returns the extended status and true if available.
func GetExtendedStatus(s *StatusData) (ExtendedStatus, bool) {
	if s == nil {
		return ExtendedStatus{}, false
	}
	return ExtendedStatus{
		RecentErrorCount: s.RecentErrorCount,
		LastError:        s.LastError,
		LastErrorTime:    s.LastErrorTime,
	}, s.RecentErrorCount > 0 || s.LastError != ""
}

// WorkspaceSyncStatus represents the sync status of a workspace (ledger or team context).
// Provides a unified view of all repos the daemon is syncing.
type WorkspaceSyncStatus struct {
	ID             string    `json:"id"`                         // workspace ID (e.g., "ledger", team_id, kb_id)
	Type           string    `json:"type"`                       // "ledger", "team-context", or "kb"
	Path           string    `json:"path"`                       // local filesystem path
	CloneURL       string    `json:"clone_url,omitempty"`        // git remote URL
	Exists         bool      `json:"exists"`                     // whether path exists locally
	TeamID         string    `json:"team_id,omitempty"`          // team ID (for team contexts)
	TeamName       string    `json:"team_name,omitempty"`        // team name (for team contexts)
	TeamSlug       string    `json:"team_slug,omitempty"`        // kebab-case team slug
	KBType         string    `json:"kb_type,omitempty"`          // bubble type (for kb: personal, team, repo, custom)
	Slug           string    `json:"slug,omitempty"`             // bubble slug (for kb)
	LastSync       time.Time `json:"last_sync,omitempty"`        // last successful sync
	LastErr        string    `json:"last_error,omitempty"`       // last error message
	Syncing        bool      `json:"syncing,omitempty"`          // currently syncing
	LastGCTime     time.Time `json:"last_gc_time,omitempty"`     // last successful GC reclone
	GCIntervalDays int       `json:"gc_interval_days,omitempty"` // configured GC cadence (0 = default 7)
}

// LastSyncForPath returns the last sync time for a workspace at the given path.
// Returns (time, true) if found and non-zero, (zero, false) otherwise.
// Safe to call on nil receiver.
func (s *StatusData) LastSyncForPath(path string) (time.Time, bool) {
	if s == nil {
		return time.Time{}, false
	}
	for _, wsList := range s.Workspaces {
		for _, ws := range wsList {
			if ws.Path == path && !ws.LastSync.IsZero() {
				return ws.LastSync, true
			}
		}
	}
	return time.Time{}, false
}

// CheckoutPayload is the payload for checkout requests.
type CheckoutPayload struct {
	RepoPath string `json:"repo_path"` // target path for clone
	CloneURL string `json:"clone_url"` // git clone URL
	RepoType string `json:"repo_type"` // "ledger" or "team_context"
}

// CheckoutResult is the result of a checkout operation.
type CheckoutResult struct {
	Path          string `json:"path"`           // actual path where repo exists
	AlreadyExists bool   `json:"already_exists"` // true if repo already existed
	Cloned        bool   `json:"cloned"`         // true if we performed a clone
}

// TeamSyncResult is the per-team outcome of a team-context sync, returned as the
// final IPC response payload of a TeamSync request. The CLI uses it to report
// accurate per-team status (synced/skipped/error) and metadata (team_name, path)
// instead of inferring "synced" from a bare success/error of the IPC call.
type TeamSyncResult struct {
	TeamID   string `json:"team_id"`
	TeamName string `json:"team_name,omitempty"`
	TeamSlug string `json:"team_slug,omitempty"` // kebab-case selector users may pass to --team
	Path     string `json:"path,omitempty"`
	Status   string `json:"status"` // "synced", "skipped", "cloning", "error"
	Error    string `json:"error,omitempty"`
}

// CheckoutProgress is sent during long-running checkout operations.
type CheckoutProgress struct {
	Stage   string `json:"stage"`             // "connecting", "cloning", "verifying"
	Percent *int   `json:"percent,omitempty"` // 0-100, nil if unknown
	Message string `json:"message"`           // human-readable progress message
}

// ProgressResponse is a response that indicates ongoing progress.
type ProgressResponse struct {
	Progress *CheckoutProgress `json:"progress,omitempty"` // non-nil = still in progress
	Success  bool              `json:"success"`            // final result
	Error    string            `json:"error,omitempty"`
	Data     json.RawMessage   `json:"data,omitempty"`
}

// TelemetryPayload is the payload for telemetry events from CLI.
type TelemetryPayload struct {
	Event string         `json:"event"` // event name (e.g., "sync:complete")
	Props map[string]any `json:"props"` // event properties
}

// FrictionPayload is the payload for friction events from CLI.
// These events capture CLI usage friction (unknown commands, typos, etc.)
// and are forwarded to the friction analytics service.
type FrictionPayload struct {
	// Timestamp in ISO8601 format (RFC3339 UTC).
	Timestamp string `json:"ts"`

	// Kind categorizes the failure type (unknown-command, unknown-flag, invalid-arg, parse-error).
	Kind string `json:"kind"`

	// Command is the top-level command.
	Command string `json:"command,omitempty"`

	// Subcommand is the subcommand if applicable.
	Subcommand string `json:"subcommand,omitempty"`

	// Actor identifies who ran the command (human or agent).
	Actor string `json:"actor"`

	// AgentType is the specific agent type when Actor is "agent" (e.g., "claude-code").
	AgentType string `json:"agent_type,omitempty"`

	// PathBucket categorizes the working directory (home, repo, other).
	PathBucket string `json:"path_bucket"`

	// Input is the redacted command input (max 500 chars).
	Input string `json:"input"`

	// ErrorMsg is the redacted, truncated error message (max 200 chars).
	ErrorMsg string `json:"error_msg"`
}

// SessionFinalizeIPCPayload carries the minimum info needed for the daemon
// to upload and finalize a session that was saved locally by the CLI.
type SessionFinalizeIPCPayload struct {
	SessionName string `json:"session_name"` // e.g. "2026-03-12T11-09-ryan-OxTndR"
	LedgerPath  string `json:"ledger_path"`  // ledger repo root
	CachePath   string `json:"cache_path"`   // local cache session dir (source files)
	ProjectRoot string `json:"project_root"` // for endpoint/auth resolution
}

// MurmurPayload carries all data the daemon needs to write and commit a murmur.
// The CLI passes the full MurmurFile JSON so the daemon owns all disk I/O —
// no temp file is written by the CLI when the daemon is available.
type MurmurPayload struct {
	TargetDir  string `json:"target_dir"`  // ledger or team context repo path
	Content    string `json:"content"`     // murmur content (for commit message summary)
	RelPath    string `json:"rel_path"`    // relative path to write within TargetDir
	MurmurJSON []byte `json:"murmur_json"` // serialized ledger.MurmurFile to write at RelPath
}

// SessionWatchStartPayload carries info for the daemon to start tailing
// a hookless agent's session file and writing entries to raw.jsonl.
type SessionWatchStartPayload struct {
	SessionName string `json:"session_name"` // session folder name (e.g. "2026-03-12T11-09-ryan-OxTndR")
	SessionFile string `json:"session_file"` // path to agent's native session file (e.g. ~/.codex/sessions/...jsonl)
	AdapterName string `json:"adapter_name"` // "codex", "claude-code", etc.
}

// SessionWatchStopPayload signals the daemon to stop tailing a session.
type SessionWatchStopPayload struct {
	SessionName string `json:"session_name"`
}

// MurmurPausePayload carries the agent ID for pause/resume murmur nudging.
type MurmurPausePayload struct {
	AgentID string `json:"agent_id"`
}

// MarkErrorsPayload is the payload for marking errors as viewed.
type MarkErrorsPayload struct {
	// IDs to mark as viewed. If empty, marks all errors as viewed.
	IDs []string `json:"ids,omitempty"`
}

// AgentSession represents an active agent session from a daemon.
// Used by the sessions IPC message to report connected agents.
type AgentSession struct {
	// AgentID is the short agent identifier (e.g., "Oxa7b3").
	AgentID string `json:"agent_id"`

	// WorkspacePath is the workspace/repo the agent is working in.
	WorkspacePath string `json:"workspace_path"`

	// LastHeartbeat is when the agent last sent a heartbeat.
	LastHeartbeat time.Time `json:"last_heartbeat"`

	// HeartbeatCount is the number of heartbeats received from this agent.
	HeartbeatCount int `json:"heartbeat_count"`

	// Status is "active" (recent heartbeat) or "idle" (stale heartbeat).
	Status string `json:"status"`
}

// SessionsResponse is the response for the sessions IPC message.
// Deprecated: Use InstancesResponse instead.
type SessionsResponse struct {
	Sessions []AgentSession `json:"sessions"`
}

// InstanceInfo represents an active agent instance from a daemon.
// Used by the instances IPC message to report connected agents.
type InstanceInfo struct {
	// AgentID is the short agent identifier (e.g., "Oxa7b3").
	AgentID string `json:"agent_id"`

	// WorkspacePath is the workspace/repo the agent is working in.
	WorkspacePath string `json:"workspace_path"`

	// LastHeartbeat is when the agent last sent a heartbeat.
	LastHeartbeat time.Time `json:"last_heartbeat"`

	// HeartbeatCount is the number of heartbeats received from this agent.
	HeartbeatCount int `json:"heartbeat_count"`

	// Status is "active" (recent heartbeat) or "idle" (stale heartbeat).
	Status string `json:"status"`

	// CumulativeContextTokens is the estimated total tokens of context this
	// agent consumed from ox commands. Equal to the sum across all
	// CumulativeContextTokensBySource entries.
	CumulativeContextTokens int64 `json:"cumulative_context_tokens,omitempty"`

	// CumulativeContextTokensBySource splits the total by content source
	// (prime.BudgetSource* keys: "sageox", "team", "project", "user",
	// and any future knowledge bubble identifier).
	//
	// SageOx is judged on the "sageox" bucket — every word in it is a
	// SageOx product decision. Other buckets reflect content the team,
	// project, user, or another knowledge bubble authored; SageOx
	// delivers it but does not control the size. This split lets
	// ox status, ox agent list, telemetry, and tests measure the tool
	// independently from the content it carries.
	//
	// The map is open: a future knowledge bubble adds an entry by
	// tagging emit sites in cmd/ox/agent_prime_xml.go with a new source
	// constant from internal/prime. No daemon or IPC schema change.
	CumulativeContextTokensBySource map[string]int64 `json:"cumulative_context_tokens_by_source,omitempty"`

	// CumulativeContextTokensByKBType splits the total by knowledge-bubble
	// kind (api.KBType slug: "personal", "profile", "team", "repo",
	// "custom", "unknown"). Lets dashboards split, e.g., "personal vs
	// team load" without dereferencing each source's bubble metadata.
	//
	// Forward-compat: a kb_type the CLI doesn't recognize aggregates
	// under "unknown" rather than being dropped. When the heartbeat
	// declared no explicit kb_type split, the daemon synthesizes one
	// from the per-source map (legacy "team" → "team", legacy "project"
	// ledger → "repo", everything else → "unknown") so dashboards
	// always see populated buckets even with older clients.
	CumulativeContextTokensByKBType map[string]int64 `json:"cumulative_context_tokens_by_kb_type,omitempty"`

	// CommandCount is the number of ox commands that produced context output for this agent.
	CommandCount int `json:"command_count,omitempty"`

	// ParentAgentID is the parent agent that spawned this agent (empty for top-level agents).
	// Populated from heartbeat tracking, enabling cross-worktree tree display.
	ParentAgentID string `json:"parent_agent_id,omitempty"`

	// AgentType identifies the kind of agent (e.g., "claude-code", "explore").
	// Populated from heartbeat tracking, enabling cross-worktree type display.
	AgentType string `json:"agent_type,omitempty"`

	// ParentPID is the parent process ID of the agent.
	// Enables instant liveness detection without heartbeat timeout.
	ParentPID int `json:"parent_pid,omitempty"`

	// LastWhisper is when whispers were last delivered to this agent.
	// Zero if no whispers have been delivered in the current daemon session.
	LastWhisper time.Time `json:"last_whisper,omitempty"`

	// PrincipalID is the human principal's identifier (e.g., "ryan").
	// Used for teammate attribution in activity displays.
	PrincipalID string `json:"principal_id,omitempty"`
}

// InstancesResponse is the response for the instances IPC message.
type InstancesResponse struct {
	Instances []InstanceInfo `json:"instances"`
}

// WhispersPayload is the payload for whisper queries.
type WhispersPayload struct {
	AgentID   string   `json:"agent_id"`
	Attention string   `json:"attention,omitempty"` // "all", "normal", "focused" (default: "normal")
	Topics    []string `json:"topics,omitempty"`    // nil = all topics
}

// WhispersResponse is the response for whisper queries.
type WhispersResponse struct {
	Entries []whisperstore.WhisperEntry `json:"entries"`
}

// WhisperHistoryPayload is the payload for whisper history queries.
type WhisperHistoryPayload struct {
	AgentID string    `json:"agent_id"`         // empty = all agents
	Before  time.Time `json:"before,omitempty"` // cursor: only return entries older than this
	Limit   int       `json:"limit,omitempty"`  // max entries per page; 0 = default (50), max 200
}

// WhisperHistoryResponse returns a page of whispers with delivery status.
type WhisperHistoryResponse struct {
	Entries    []whisperstore.WhisperEntry `json:"entries"`
	Cursor     time.Time                   `json:"cursor"`                // agent's delivery cursor (entries at/before this are "delivered")
	HasCursor  bool                        `json:"has_cursor"`            // false if agent has never received whispers
	HasMore    bool                        `json:"has_more,omitempty"`    // true if more entries exist beyond this page
	NextCursor time.Time                   `json:"next_cursor,omitempty"` // pass as Before in next request to get the next page
}

// DoctorResponse is the response for the doctor IPC message.
//
// The Doctor RPC is asynchronous: the daemon kicks off the heavy work
// (autofix scheduler RunNow, ForceDetect across all session caches,
// session-watcher detect/restart/cleanup) in a background goroutine and
// returns immediately. Results surface via the daemon's IssueTracker
// (visible in `ox daemon status`) and the agent worker queue (also
// reflected in status). This shape exists so the IPC ping itself is a
// few milliseconds even on ledgers with thousands of sessions.
//
// The legacy synchronous result fields (Autofix*, SessionFinalize*) are
// retained for backwards compatibility with any caller that still reads
// them, but newly written code should rely on BackgroundStarted /
// AlreadyRunning and route the user to status/logs for results.
type DoctorResponse struct {
	// Async lifecycle indicators (preferred).
	BackgroundStarted bool `json:"background_started,omitempty"` // a fresh doctor pass was kicked off
	AlreadyRunning    bool `json:"already_running,omitempty"`    // a prior pass is still in progress; this call was a no-op

	// Legacy synchronous fields. Populated only when the daemon ran
	// the work inline (e.g., older daemon versions, or unit tests
	// driving the impl synchronously). Production callers in 0.7.2+
	// see these empty.
	AntiEntropyTriggered     bool     `json:"anti_entropy_triggered"`
	ClonesTriggered          int      `json:"clones_triggered"`
	SessionFinalizeTriggered bool     `json:"session_finalize_triggered"`
	SessionFinalizeQueued    int      `json:"session_finalize_queued"`
	AutofixRan               bool     `json:"autofix_ran,omitempty"`
	AutofixSummaries         []string `json:"autofix_summaries,omitempty"`
	MetaTitlesRepaired       int      `json:"meta_titles_repaired,omitempty"`

	Errors []string `json:"errors,omitempty"`
}

// TriggerGCResponse is the response for trigger_gc requests.
// Errors include both failures (clone/validation errors) and skips due to
// uncommitted changes — GC is a disk-space optimization and must never
// destroy user work.
type TriggerGCResponse struct {
	Triggered       int      `json:"triggered"`
	Skipped         int      `json:"skipped,omitempty"`
	LedgerTriggered bool     `json:"ledger_triggered,omitempty"`
	Errors          []string `json:"errors,omitempty"`
}

// ProgressWriter allows handlers to send progress updates during long operations.
type ProgressWriter struct {
	conn net.Conn
}

// WriteProgress sends a progress update with known percentage.
func (pw *ProgressWriter) WriteProgress(stage string, percent int, message string) error {
	p := percent // take address of local var
	return pw.write(&CheckoutProgress{
		Stage:   stage,
		Percent: &p,
		Message: message,
	})
}

// WriteMessage sends a progress update with just a message (no stage or percent).
func (pw *ProgressWriter) WriteMessage(message string) error {
	return pw.write(&CheckoutProgress{
		Message: message,
	})
}

// WriteStage sends a progress update with stage and message (no percent).
func (pw *ProgressWriter) WriteStage(stage string, message string) error {
	return pw.write(&CheckoutProgress{
		Stage:   stage,
		Message: message,
	})
}

// write sends a CheckoutProgress to the client.
// Progress updates are best-effort: if the client can't keep up, we skip rather than block.
func (pw *ProgressWriter) write(progress *CheckoutProgress) error {
	// Use short write deadline (100ms) for progress updates.
	// If client is slow to read, skip the update rather than blocking the daemon.
	// Progress is informational, not critical to the operation.
	pw.conn.SetWriteDeadline(time.Now().Add(100 * time.Millisecond))

	resp := ProgressResponse{Progress: progress}
	data, err := json.Marshal(resp)
	if err != nil {
		return fmt.Errorf("marshal progress: %w", err)
	}
	data = append(data, '\n')
	if _, err = pw.conn.Write(data); err != nil {
		// Log but don't fail - progress is best-effort
		// Client may have disconnected or be slow to read
		return nil
	}
	return nil
}

// HandlerResult represents the result of a message handler.
type HandlerResult struct {
	Response    *Response // response to send (nil = no response)
	SkipDefault bool      // if true, don't send the default response
}

// MessageHandler handles a specific message type.
// It receives the server (for accessing callbacks), the message, and the connection.
// Returns the handler result.
type MessageHandler func(s *Server, msg Message, conn net.Conn) HandlerResult

// MessageRouter routes messages to their handlers.
type MessageRouter struct {
	handlers map[string]MessageHandler
	logger   *slog.Logger
}

// NewMessageRouter creates a new message router.
func NewMessageRouter(logger *slog.Logger) *MessageRouter {
	return &MessageRouter{
		handlers: make(map[string]MessageHandler),
		logger:   logger,
	}
}

// Register registers a handler for a message type.
func (r *MessageRouter) Register(msgType string, handler MessageHandler) {
	r.handlers[msgType] = handler
}

// Handle routes a message to its handler.
// Returns the handler result and whether a handler was found.
func (r *MessageRouter) Handle(s *Server, msg Message, conn net.Conn) (HandlerResult, bool) {
	handler, ok := r.handlers[msg.Type]
	if !ok {
		return HandlerResult{
			Response: &Response{Success: false, Error: "unknown message type"},
		}, false
	}
	return handler(s, msg, conn), true
}

// DaemonService is the interface the IPC server calls for all daemon operations.
// Grouping methods by concern makes it easy to see what each new IPC message needs.
type DaemonService interface {
	// sync operations
	Sync() error
	SyncWithProgress(progress *ProgressWriter) error
	TeamSync(progress *ProgressWriter) ([]TeamSyncResult, error)
	SyncHistory() []SyncEvent

	// status / query operations
	Status() *StatusData
	SettingsGet() *flags.CLISettingsResponse
	GetErrors() []StoredError
	Sessions() []AgentSession // deprecated: use Instances
	Instances() []InstanceInfo
	Whispers(agentID string, attention whisperstore.Attention, topics []string) ([]whisperstore.WhisperEntry, error)
	WhisperHistory(agentID string, before time.Time, limit int) (*WhisperHistoryResponse, error)
	CodeStatus() *CodeDBStats

	// mutation operations
	Stop()
	Checkout(payload CheckoutPayload, progress *ProgressWriter) (*CheckoutResult, error)
	MarkErrors(ids []string)
	TriggerGC() *TriggerGCResponse
	CodeIndex(payload CodeIndexPayload, progress *ProgressWriter) (*CodeIndexResult, error)
	Doctor() *DoctorResponse
	SessionFinalize(payload SessionFinalizeIPCPayload)
	SessionWatchStart(payload SessionWatchStartPayload)
	SessionWatchStop(payload SessionWatchStopPayload)

	// fire-and-forget operations
	Activity()
	Heartbeat(callerID string, payload json.RawMessage)
	Telemetry(payload json.RawMessage)
	Friction(payload FrictionPayload)
	PublishMurmur(payload MurmurPayload)
	PauseMurmuring(agentID string)
	ResumeMurmuring(agentID string)
	SessionUploaded(name, url, agentID string, dur time.Duration)
}

// CallbackService implements DaemonService using individual callback functions.
// It lets callers wire handlers incrementally via Set*Handler methods,
// which is useful in tests and during staged daemon startup.
type CallbackService struct {
	mu sync.Mutex

	onSync              func() error
	onSyncWithProgress  func(progress *ProgressWriter) error
	onTeamSync          func(progress *ProgressWriter) ([]TeamSyncResult, error)
	onStop              func()
	onStatus            func() *StatusData
	onActivity          func()
	onHeartbeat         func(callerID string, payload json.RawMessage)
	onCheckout          func(payload CheckoutPayload, progress *ProgressWriter) (*CheckoutResult, error)
	onTelemetry         func(payload json.RawMessage)
	onFriction          func(payload FrictionPayload)
	onSessionFinalize   func(payload SessionFinalizeIPCPayload)
	onSessionWatchStart func(payload SessionWatchStartPayload)
	onSessionWatchStop  func(payload SessionWatchStopPayload)
	onPublishMurmur     func(payload MurmurPayload)
	onPauseMurmuring    func(agentID string)
	onResumeMurmuring   func(agentID string)
	onGetErrors         func() []StoredError
	onMarkErrors        func(ids []string)
	onSessions          func() []AgentSession
	onInstances         func() []InstanceInfo
	onSyncHistory       func() []SyncEvent
	onDoctor            func() *DoctorResponse
	onTriggerGC         func() *TriggerGCResponse
	onCodeIndex         func(payload CodeIndexPayload, progress *ProgressWriter) (*CodeIndexResult, error)
	onCodeStatus        func() *CodeDBStats
	onWhispers          func(agentID string, attention whisperstore.Attention, topics []string) ([]whisperstore.WhisperEntry, error)
	onWhisperHistory    func(agentID string, before time.Time, limit int) (*WhisperHistoryResponse, error)
	onSettingsGet       func() *flags.CLISettingsResponse
}

func (c *CallbackService) Sync() error {
	c.mu.Lock()
	fn := c.onSync
	c.mu.Unlock()
	if fn != nil {
		return fn()
	}
	return nil
}

func (c *CallbackService) SyncWithProgress(progress *ProgressWriter) error {
	c.mu.Lock()
	fn := c.onSyncWithProgress
	c.mu.Unlock()
	if fn != nil {
		return fn(progress)
	}
	return nil
}

func (c *CallbackService) TeamSync(progress *ProgressWriter) ([]TeamSyncResult, error) {
	c.mu.Lock()
	fn := c.onTeamSync
	c.mu.Unlock()
	if fn != nil {
		return fn(progress)
	}
	return nil, nil
}

func (c *CallbackService) SyncHistory() []SyncEvent {
	c.mu.Lock()
	fn := c.onSyncHistory
	c.mu.Unlock()
	if fn != nil {
		return fn()
	}
	return nil
}

func (c *CallbackService) Status() *StatusData {
	c.mu.Lock()
	fn := c.onStatus
	c.mu.Unlock()
	if fn != nil {
		return fn()
	}
	return nil
}

func (c *CallbackService) GetErrors() []StoredError {
	c.mu.Lock()
	fn := c.onGetErrors
	c.mu.Unlock()
	if fn != nil {
		return fn()
	}
	return nil
}

func (c *CallbackService) Sessions() []AgentSession {
	c.mu.Lock()
	fn := c.onSessions
	c.mu.Unlock()
	if fn != nil {
		return fn()
	}
	return nil
}

func (c *CallbackService) Instances() []InstanceInfo {
	c.mu.Lock()
	fn := c.onInstances
	c.mu.Unlock()
	if fn != nil {
		return fn()
	}
	return nil
}

func (c *CallbackService) Whispers(agentID string, attention whisperstore.Attention, topics []string) ([]whisperstore.WhisperEntry, error) {
	c.mu.Lock()
	fn := c.onWhispers
	c.mu.Unlock()
	if fn != nil {
		return fn(agentID, attention, topics)
	}
	return nil, nil
}

func (c *CallbackService) WhisperHistory(agentID string, before time.Time, limit int) (*WhisperHistoryResponse, error) {
	c.mu.Lock()
	fn := c.onWhisperHistory
	c.mu.Unlock()
	if fn != nil {
		return fn(agentID, before, limit)
	}
	return &WhisperHistoryResponse{Entries: []whisperstore.WhisperEntry{}}, nil
}

func (c *CallbackService) CodeStatus() *CodeDBStats {
	c.mu.Lock()
	fn := c.onCodeStatus
	c.mu.Unlock()
	if fn != nil {
		return fn()
	}
	return nil
}

func (c *CallbackService) SettingsGet() *flags.CLISettingsResponse {
	c.mu.Lock()
	fn := c.onSettingsGet
	c.mu.Unlock()
	if fn != nil {
		return fn()
	}
	return nil
}

func (c *CallbackService) Stop() {
	c.mu.Lock()
	fn := c.onStop
	c.mu.Unlock()
	if fn != nil {
		fn()
	}
}

func (c *CallbackService) Checkout(payload CheckoutPayload, progress *ProgressWriter) (*CheckoutResult, error) {
	c.mu.Lock()
	fn := c.onCheckout
	c.mu.Unlock()
	if fn != nil {
		return fn(payload, progress)
	}
	return nil, nil
}

func (c *CallbackService) MarkErrors(ids []string) {
	c.mu.Lock()
	fn := c.onMarkErrors
	c.mu.Unlock()
	if fn != nil {
		fn(ids)
	}
}

func (c *CallbackService) TriggerGC() *TriggerGCResponse {
	c.mu.Lock()
	fn := c.onTriggerGC
	c.mu.Unlock()
	if fn != nil {
		return fn()
	}
	return nil
}

func (c *CallbackService) CodeIndex(payload CodeIndexPayload, progress *ProgressWriter) (*CodeIndexResult, error) {
	c.mu.Lock()
	fn := c.onCodeIndex
	c.mu.Unlock()
	if fn != nil {
		return fn(payload, progress)
	}
	return nil, nil
}

func (c *CallbackService) Doctor() *DoctorResponse {
	c.mu.Lock()
	fn := c.onDoctor
	c.mu.Unlock()
	if fn != nil {
		return fn()
	}
	return nil
}

func (c *CallbackService) SessionFinalize(payload SessionFinalizeIPCPayload) {
	c.mu.Lock()
	fn := c.onSessionFinalize
	c.mu.Unlock()
	if fn != nil {
		fn(payload)
	}
}

func (c *CallbackService) SessionWatchStart(payload SessionWatchStartPayload) {
	c.mu.Lock()
	fn := c.onSessionWatchStart
	c.mu.Unlock()
	if fn != nil {
		fn(payload)
	}
}

func (c *CallbackService) SessionWatchStop(payload SessionWatchStopPayload) {
	c.mu.Lock()
	fn := c.onSessionWatchStop
	c.mu.Unlock()
	if fn != nil {
		fn(payload)
	}
}

func (c *CallbackService) Activity() {
	c.mu.Lock()
	fn := c.onActivity
	c.mu.Unlock()
	if fn != nil {
		fn()
	}
}

func (c *CallbackService) Heartbeat(callerID string, payload json.RawMessage) {
	c.mu.Lock()
	fn := c.onHeartbeat
	c.mu.Unlock()
	if fn != nil {
		fn(callerID, payload)
	}
}

func (c *CallbackService) Telemetry(payload json.RawMessage) {
	c.mu.Lock()
	fn := c.onTelemetry
	c.mu.Unlock()
	if fn != nil {
		fn(payload)
	}
}

func (c *CallbackService) Friction(payload FrictionPayload) {
	c.mu.Lock()
	fn := c.onFriction
	c.mu.Unlock()
	if fn != nil {
		fn(payload)
	}
}

func (c *CallbackService) PublishMurmur(payload MurmurPayload) {
	c.mu.Lock()
	fn := c.onPublishMurmur
	c.mu.Unlock()
	if fn != nil {
		fn(payload)
	}
}

func (c *CallbackService) PauseMurmuring(agentID string) {
	c.mu.Lock()
	fn := c.onPauseMurmuring
	c.mu.Unlock()
	if fn != nil {
		fn(agentID)
	}
}

func (c *CallbackService) ResumeMurmuring(agentID string) {
	c.mu.Lock()
	fn := c.onResumeMurmuring
	c.mu.Unlock()
	if fn != nil {
		fn(agentID)
	}
}

func (c *CallbackService) SessionUploaded(_, _, _ string, _ time.Duration) {
	// no-op for callback service; daemon wires via daemonServiceImpl
}

// Server handles IPC requests from clients.
type Server struct {
	logger   *slog.Logger
	svc      *CallbackService // mutable callback adapter; non-nil when created via NewServer
	service  DaemonService    // active service; set to svc by default, overridable via NewServerWithService
	listener net.Listener
	router   *MessageRouter
	mu       sync.Mutex
	connWg   sync.WaitGroup // tracks active connection handler goroutines
	connSem  chan struct{}  // semaphore for connection limit

	startTime time.Time

	// version-skew tracking (ox-mt3k). Records the most recent CLI
	// version observed via IPC so the daemon can warn when a long-
	// running daemon is being driven by a CLI N+ releases behind. Both
	// fields are guarded by skewMu — IPC is concurrent.
	skewMu                sync.Mutex
	lastCallerVersion     string
	lastCallerVersionAt   time.Time
	skewWarnLoggedVersion string // de-dup warn log per skewing version

	// peerCredDisabled, when true, skips the SO_PEERCRED / LOCAL_PEERCRED
	// check in handleConnection. Test-only — exercised by in-process IPC
	// tests where peer credentials are technically the same UID but the
	// loopback conn dance with mocked listeners can interfere with the
	// real syscall. Production code never sets this.
	peerCredDisabled bool
}

// DisablePeerCredForTesting marks the server to skip peer-credential
// checks. Only intended for tests that drive IPC over Unix sockets in a
// way that doesn't survive the real check (e.g. listener wrapping that
// breaks the connection-type assertion). Production callers MUST NOT
// invoke this.
func (s *Server) DisablePeerCredForTesting() {
	s.peerCredDisabled = true
}

// recordCallerVersion stamps the most recent caller version observed
// via IPC and emits a warn log (once per distinct skewing version)
// when the gap between the caller and the daemon's own version is
// big enough to plausibly cause behavior drift.
//
// "Big enough" lives in IsSignificantVersionSkew below — currently
// any minor-or-greater gap. Patch-level differences are routine
// during a rolling upgrade and intentionally don't warn.
func (s *Server) recordCallerVersion(v string) {
	if v == "" {
		return // older clients predate the field; no signal
	}
	s.skewMu.Lock()
	defer s.skewMu.Unlock()
	s.lastCallerVersion = v
	s.lastCallerVersionAt = time.Now()
	if !IsSignificantVersionSkew(v, Version()) {
		return
	}
	if s.skewWarnLoggedVersion == v {
		return // already warned for this exact version this run
	}
	s.skewWarnLoggedVersion = v
	s.logger.Warn("CLI version skew detected — please upgrade `ox`",
		"caller_version", v,
		"daemon_version", Version())
}

// LastCallerVersion returns the most recent CLI version observed via
// IPC (or "" if none observed). Exposed for status rendering and
// tests; do not write through this — use recordCallerVersion.
func (s *Server) LastCallerVersion() (version string, observedAt time.Time) {
	s.skewMu.Lock()
	defer s.skewMu.Unlock()
	return s.lastCallerVersion, s.lastCallerVersionAt
}

// IsSignificantVersionSkew reports whether a caller's version lags the
// daemon's far enough to warn the user about. Both arguments are
// expected to be the strings produced by Version() (e.g.
// "0.6.5+2026-04-30T..." or "dev"). Skew detection is intentionally
// conservative:
//
//   - "dev" or "" on either side → no skew (development builds, older
//     callers predating CallerVersion).
//   - Identical major.minor → no skew (patch-level upgrade in flight).
//   - Different major OR different minor → significant skew. A new
//     daemon version that changes wire shape or expected on-disk
//     state is reason enough to warn.
//
// We deliberately don't try to parse build metadata or pre-release
// tags; the version semantics in ox are coarse enough (0.<release>.0)
// that a minor-level check matches what 'a release behind' means
// operationally.
func IsSignificantVersionSkew(caller, daemon string) bool {
	cMaj, cMin, cOK := majorMinor(caller)
	dMaj, dMin, dOK := majorMinor(daemon)
	if !cOK || !dOK {
		return false
	}
	if cMaj != dMaj {
		return true
	}
	return cMin != dMin
}

// majorMinor parses the leading "<major>.<minor>" out of a Version()
// string. Returns (0, 0, false) for unparseable inputs (development
// builds, empty strings) so callers treat them as "skew unknown".
func majorMinor(v string) (major, minor int, ok bool) {
	if v == "" || v == "dev" {
		return 0, 0, false
	}
	// strip everything after first '+' (build metadata) or '-' (pre-release)
	for i, r := range v {
		if r == '+' || r == '-' {
			v = v[:i]
			break
		}
	}
	parts := strings.SplitN(v, ".", 3)
	if len(parts) < 2 {
		return 0, 0, false
	}
	if _, err := fmt.Sscanf(parts[0], "%d", &major); err != nil {
		return 0, 0, false
	}
	if _, err := fmt.Sscanf(parts[1], "%d", &minor); err != nil {
		return 0, 0, false
	}
	return major, minor, true
}

// NewServer creates a new IPC server with a mutable CallbackService.
// Handlers are wired incrementally via Set*Handler methods.
func NewServer(logger *slog.Logger) *Server {
	svc := &CallbackService{}
	s := &Server{
		logger:    logger,
		svc:       svc,
		service:   svc,
		startTime: time.Now(),
		connSem:   make(chan struct{}, maxConcurrentConnections),
	}
	s.router = s.buildRouter()
	return s
}

// NewServerWithService creates an IPC server backed by an explicit DaemonService.
// Use this when all operations are available upfront (e.g., in daemon.go).
func NewServerWithService(logger *slog.Logger, service DaemonService) *Server {
	s := &Server{
		logger:    logger,
		service:   service,
		startTime: time.Now(),
		connSem:   make(chan struct{}, maxConcurrentConnections),
	}
	s.router = s.buildRouter()
	return s
}

// buildRouter creates and configures the message router with all handlers.
func (s *Server) buildRouter() *MessageRouter {
	router := NewMessageRouter(s.logger)

	router.Register(MsgTypePing, handlePing)
	router.Register(MsgTypeVersion, handleVersion)
	router.Register(MsgTypeStatus, handleStatus)
	router.Register(MsgTypeSyncHistory, handleSyncHistory)
	router.Register(MsgTypeSync, handleSync)
	router.Register(MsgTypeTeamSync, handleTeamSync)
	router.Register(MsgTypeStop, handleStop)
	router.Register(MsgTypeHeartbeat, handleHeartbeat)
	router.Register(MsgTypeTelemetry, handleTelemetry)
	router.Register(MsgTypeFriction, handleFriction)
	router.Register(MsgTypeSessionFinalize, handleSessionFinalize)
	router.Register(MsgTypeGetErrors, handleGetErrors)
	router.Register(MsgTypeMarkErrors, handleMarkErrors)
	router.Register(MsgTypeCheckout, handleCheckout)
	router.Register(MsgTypeSessions, handleSessions)
	router.Register(MsgTypeInstances, handleInstances)
	router.Register(MsgTypeDoctor, handleDoctor)
	router.Register(MsgTypeTriggerGC, handleTriggerGC)
	router.Register(MsgTypeCodeIndex, handleCodeIndex)
	router.Register(MsgTypeCodeStatus, handleCodeStatus)
	router.Register(MsgTypeWhispers, handleWhispers)
	router.Register(MsgTypeWhisperHistory, handleWhisperHistory)
	router.Register(MsgTypeMurmur, handleMurmur)
	router.Register(MsgTypeMurmurPause, handleMurmurPause)
	router.Register(MsgTypeMurmurResume, handleMurmurResume)
	router.Register(MsgTypeSessionWatchStart, handleSessionWatchStart)
	router.Register(MsgTypeSessionWatchStop, handleSessionWatchStop)
	router.Register(MsgTypeSettingsGet, handleSettingsGet)
	router.Register(MsgTypeSessionUploaded, handleSessionUploaded)

	return router
}

// SetHandlers sets the core sync/stop/status handlers on the CallbackService.
// Panics if the server was created via NewServerWithService (no mutable adapter).
func (s *Server) SetHandlers(onSync func() error, onStop func(), onStatus func() *StatusData) {
	svc := s.mustCallbackService("SetHandlers")
	svc.mu.Lock()
	defer svc.mu.Unlock()
	svc.onSync = onSync
	svc.onStop = onStop
	svc.onStatus = onStatus
}

// SetSyncHandler sets the sync handler with progress support.
// This supersedes the onSync callback set in SetHandlers.
func (s *Server) SetSyncHandler(cb func(progress *ProgressWriter) error) {
	svc := s.mustCallbackService("SetSyncHandler")
	svc.mu.Lock()
	defer svc.mu.Unlock()
	svc.onSyncWithProgress = cb
}

// SetTeamSyncHandler sets the team context sync handler with progress support.
func (s *Server) SetTeamSyncHandler(cb func(progress *ProgressWriter) ([]TeamSyncResult, error)) {
	svc := s.mustCallbackService("SetTeamSyncHandler")
	svc.mu.Lock()
	defer svc.mu.Unlock()
	svc.onTeamSync = cb
}

// SetActivityCallback sets the callback for activity tracking.
func (s *Server) SetActivityCallback(cb func()) {
	svc := s.mustCallbackService("SetActivityCallback")
	svc.mu.Lock()
	defer svc.mu.Unlock()
	svc.onActivity = cb
}

// SetHeartbeatHandler sets the handler for heartbeat messages.
// callerID identifies which clone/worktree sent the heartbeat (path-based hash).
func (s *Server) SetHeartbeatHandler(cb func(callerID string, payload json.RawMessage)) {
	svc := s.mustCallbackService("SetHeartbeatHandler")
	svc.mu.Lock()
	defer svc.mu.Unlock()
	svc.onHeartbeat = cb
}

// SetCheckoutHandler sets the handler for checkout requests.
// The handler receives a ProgressWriter to send progress updates during long operations.
func (s *Server) SetCheckoutHandler(cb func(payload CheckoutPayload, progress *ProgressWriter) (*CheckoutResult, error)) {
	svc := s.mustCallbackService("SetCheckoutHandler")
	svc.mu.Lock()
	defer svc.mu.Unlock()
	svc.onCheckout = cb
}

// SetTelemetryHandler sets the handler for telemetry messages.
// Telemetry is fire-and-forget - no response is sent.
func (s *Server) SetTelemetryHandler(cb func(payload json.RawMessage)) {
	svc := s.mustCallbackService("SetTelemetryHandler")
	svc.mu.Lock()
	defer svc.mu.Unlock()
	svc.onTelemetry = cb
}

// SetFrictionHandler sets the handler for friction messages.
// Friction events are fire-and-forget - no response is sent.
func (s *Server) SetFrictionHandler(cb func(payload FrictionPayload)) {
	svc := s.mustCallbackService("SetFrictionHandler")
	svc.mu.Lock()
	defer svc.mu.Unlock()
	svc.onFriction = cb
}

// SetSessionFinalizeHandler sets the handler for session finalize messages.
// Session finalize events are fire-and-forget - no response is sent.
func (s *Server) SetSessionFinalizeHandler(fn func(payload SessionFinalizeIPCPayload)) {
	svc := s.mustCallbackService("SetSessionFinalizeHandler")
	svc.mu.Lock()
	defer svc.mu.Unlock()
	svc.onSessionFinalize = fn
}

// SetSessionWatchStartHandler sets the handler for session watch start messages.
// Session watch start events are fire-and-forget - no response is sent.
func (s *Server) SetSessionWatchStartHandler(fn func(payload SessionWatchStartPayload)) {
	svc := s.mustCallbackService("SetSessionWatchStartHandler")
	svc.mu.Lock()
	defer svc.mu.Unlock()
	svc.onSessionWatchStart = fn
}

// SetSessionWatchStopHandler sets the handler for session watch stop messages.
// Session watch stop events are fire-and-forget - no response is sent.
func (s *Server) SetSessionWatchStopHandler(fn func(payload SessionWatchStopPayload)) {
	svc := s.mustCallbackService("SetSessionWatchStopHandler")
	svc.mu.Lock()
	defer svc.mu.Unlock()
	svc.onSessionWatchStop = fn
}

// SetMurmurHandler sets the handler for murmur write+commit messages.
// Murmur events are fire-and-forget - no response is sent.
func (s *Server) SetMurmurHandler(fn func(payload MurmurPayload)) {
	svc := s.mustCallbackService("SetMurmurHandler")
	svc.mu.Lock()
	defer svc.mu.Unlock()
	svc.onPublishMurmur = fn
}

// SetPauseMurmuringHandler sets the handler for pausing murmur nudging.
// Murmur pause events are fire-and-forget - no response is sent.
func (s *Server) SetPauseMurmuringHandler(fn func(agentID string)) {
	svc := s.mustCallbackService("SetPauseMurmuringHandler")
	svc.mu.Lock()
	defer svc.mu.Unlock()
	svc.onPauseMurmuring = fn
}

// SetResumeMurmuringHandler sets the handler for resuming murmur nudging.
// Murmur resume events are fire-and-forget - no response is sent.
func (s *Server) SetResumeMurmuringHandler(fn func(agentID string)) {
	svc := s.mustCallbackService("SetResumeMurmuringHandler")
	svc.mu.Lock()
	defer svc.mu.Unlock()
	svc.onResumeMurmuring = fn
}

// SetErrorsHandler sets the handler for retrieving unviewed errors.
func (s *Server) SetErrorsHandler(onGet func() []StoredError, onMark func(ids []string)) {
	svc := s.mustCallbackService("SetErrorsHandler")
	svc.mu.Lock()
	defer svc.mu.Unlock()
	svc.onGetErrors = onGet
	svc.onMarkErrors = onMark
}

// SetSessionsHandler sets the handler for retrieving active agent sessions.
// Deprecated: Use SetInstancesHandler instead.
func (s *Server) SetSessionsHandler(cb func() []AgentSession) {
	svc := s.mustCallbackService("SetSessionsHandler")
	svc.mu.Lock()
	defer svc.mu.Unlock()
	svc.onSessions = cb
}

// SetInstancesHandler sets the handler for retrieving active agent instances.
func (s *Server) SetInstancesHandler(cb func() []InstanceInfo) {
	svc := s.mustCallbackService("SetInstancesHandler")
	svc.mu.Lock()
	defer svc.mu.Unlock()
	svc.onInstances = cb
}

// SetSyncHistoryHandler sets the sync history handler.
func (s *Server) SetSyncHistoryHandler(handler func() []SyncEvent) {
	svc := s.mustCallbackService("SetSyncHistoryHandler")
	svc.mu.Lock()
	defer svc.mu.Unlock()
	svc.onSyncHistory = handler
}

// SetDoctorHandler sets the doctor (health check) handler.
func (s *Server) SetDoctorHandler(handler func() *DoctorResponse) {
	svc := s.mustCallbackService("SetDoctorHandler")
	svc.mu.Lock()
	defer svc.mu.Unlock()
	svc.onDoctor = handler
}

// SetTriggerGCHandler sets the handler for forced GC reclone.
func (s *Server) SetTriggerGCHandler(handler func() *TriggerGCResponse) {
	svc := s.mustCallbackService("SetTriggerGCHandler")
	svc.mu.Lock()
	defer svc.mu.Unlock()
	svc.onTriggerGC = handler
}

// SetCodeIndexHandler sets the handler for code indexing requests.
// The handler receives a ProgressWriter to send progress updates during indexing.
func (s *Server) SetCodeIndexHandler(cb func(payload CodeIndexPayload, progress *ProgressWriter) (*CodeIndexResult, error)) {
	svc := s.mustCallbackService("SetCodeIndexHandler")
	svc.mu.Lock()
	defer svc.mu.Unlock()
	svc.onCodeIndex = cb
}

// SetCodeStatusHandler sets the handler for code index status requests.
func (s *Server) SetCodeStatusHandler(cb func() *CodeDBStats) {
	svc := s.mustCallbackService("SetCodeStatusHandler")
	svc.mu.Lock()
	defer svc.mu.Unlock()
	svc.onCodeStatus = cb
}

// SetWhispersHandler sets the handler for whisper queries.
func (s *Server) SetWhispersHandler(cb func(agentID string, attention whisperstore.Attention, topics []string) ([]whisperstore.WhisperEntry, error)) {
	svc := s.mustCallbackService("SetWhispersHandler")
	svc.mu.Lock()
	defer svc.mu.Unlock()
	svc.onWhispers = cb
}

// SetWhisperHistoryHandler sets the handler for whisper history (inspection) queries.
func (s *Server) SetWhisperHistoryHandler(cb func(agentID string, before time.Time, limit int) (*WhisperHistoryResponse, error)) {
	svc := s.mustCallbackService("SetWhisperHistoryHandler")
	svc.mu.Lock()
	defer svc.mu.Unlock()
	svc.onWhisperHistory = cb
}

// SetSettingsGetHandler sets the handler for CLI feature flag settings queries.
func (s *Server) SetSettingsGetHandler(cb func() *flags.CLISettingsResponse) {
	svc := s.mustCallbackService("SetSettingsGetHandler")
	svc.mu.Lock()
	defer svc.mu.Unlock()
	svc.onSettingsGet = cb
}

// mustCallbackService returns the mutable callback adapter.
// Panics if the server was created with NewServerWithService (adapters can't be mixed).
func (s *Server) mustCallbackService(method string) *CallbackService {
	if s.svc == nil {
		panic("daemon: " + method + " called on a server created with NewServerWithService; use the DaemonService interface instead")
	}
	return s.svc
}

// Start starts the IPC server.
func (s *Server) Start(ctx context.Context) error {
	socketPath := SocketPath()

	listener, err := listen(socketPath)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}

	s.mu.Lock()
	s.listener = listener
	s.mu.Unlock()

	s.logger.Info("ipc server started", "socket", socketPath)

	// accept connections in goroutine
	go func() {
		backoff := 100 * time.Millisecond
		maxBackoff := 10 * time.Second

		for {
			conn, err := listener.Accept()
			if err != nil {
				select {
				case <-ctx.Done():
					return
				default:
					s.logger.Error("accept error", "error", err, "backoff", backoff)
					// exponential backoff to prevent spin loop on persistent errors (e.g., fd exhaustion)
					time.Sleep(backoff)
					backoff = min(backoff*2, maxBackoff)
					continue
				}
			}
			backoff = 100 * time.Millisecond // reset on success

			// rate limit: try to acquire a slot from the semaphore
			select {
			case s.connSem <- struct{}{}:
				// got slot, proceed with connection
				s.connWg.Add(1)
				go func(c net.Conn) {
					defer func() {
						<-s.connSem // release slot
						s.connWg.Done()
					}()
					s.handleConnection(ctx, c)
				}(conn)
			default:
				// at connection limit, reject
				s.logger.Warn("connection limit reached, rejecting", "limit", maxConcurrentConnections)
				conn.Close()
			}
		}
	}()

	// wait for context cancellation
	<-ctx.Done()

	s.mu.Lock()
	if s.listener != nil {
		s.listener.Close()
		s.listener = nil
	}
	s.mu.Unlock()

	// wait for all connection handlers to finish
	s.connWg.Wait()

	// socket file removal is owned by Daemon.cleanup, which knows whether
	// this daemon was superseded. when supersede happens, the file at the
	// socket path now belongs to the replacement daemon — unlinking it here
	// would leave the new daemon unreachable (kernel-side socket survives,
	// on-disk path gone). the legitimate pre-bind unlink of stale paths
	// still happens in listen() for the next daemon to start at this path.
	return ctx.Err()
}

// handleConnection handles a single client connection.
func (s *Server) handleConnection(_ context.Context, conn net.Conn) {
	defer conn.Close()

	// Peer authentication (ox-79cg): kernel-mediated check that the peer
	// process belongs to the same UID as the daemon. SO_PEERCRED on Linux,
	// LOCAL_PEERCRED (Getpeereid) on macOS. Stub returns an error on
	// unsupported platforms — we fail CLOSED because the alternative is
	// silently accepting connections from any local process on the box.
	//
	// Tests can opt out via DisablePeerCred (set during test setup) — real
	// production code paths never set this.
	if !s.peerCredDisabled {
		ownerUID := uint32(os.Geteuid())
		peer, err := peerUID(conn)
		if err != nil {
			s.logger.Warn("ipc: rejecting connection — peercred lookup failed",
				"error", err, "owner_uid", ownerUID)
			return
		}
		if peer != ownerUID {
			s.logger.Warn("ipc: rejecting connection — peer uid mismatch",
				"peer_uid", peer, "owner_uid", ownerUID)
			return
		}
	}

	// set read timeout for initial message parsing only
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))

	// wrap with LimitReader to prevent DoS from oversized messages
	reader := bufio.NewReader(io.LimitReader(conn, maxIPCMessageSize))
	line, err := reader.ReadBytes('\n')
	if err != nil {
		s.logger.Debug("read error", "error", err)
		return
	}

	// Clear deadline for handler execution.
	// Handlers (sync, checkout, etc.) may take much longer than 5s.
	// Each handler is responsible for setting its own write deadlines.
	conn.SetDeadline(time.Time{})

	var msg Message
	if err := json.Unmarshal(line, &msg); err != nil {
		s.sendError(conn, "invalid message format")
		return
	}

	s.logger.Debug("received message", "type", msg.Type, "workspace_id", msg.WorkspaceID, "caller_id", msg.CallerID)

	// validate workspace ID if provided (warn on mismatch, still process for backward compatibility)
	if msg.WorkspaceID != "" && msg.WorkspaceID != CurrentWorkspaceID() {
		s.logger.Warn("workspace mismatch", "expected", CurrentWorkspaceID(), "got", msg.WorkspaceID, "caller_id", msg.CallerID)
	}

	// record CLI version skew (ox-mt3k) — track the most recent caller
	// version and warn (once per version) when it lags the daemon.
	s.recordCallerVersion(msg.CallerVersion)

	// record activity on any IPC message
	s.service.Activity()

	// route message to handler
	result, _ := s.router.Handle(s, msg, conn)

	// send response unless handler opted out
	if !result.SkipDefault && result.Response != nil {
		s.sendResponse(conn, *result.Response)
	}
}

// sendResponse sends a response to the client.
func (s *Server) sendResponse(conn net.Conn, resp Response) {
	data, err := json.Marshal(resp)
	if err != nil {
		s.logger.Error("failed to marshal IPC response", "error", err)
		return
	}
	data = append(data, '\n')
	if _, err := conn.Write(data); err != nil {
		s.logger.Debug("failed to write IPC response", "error", err)
	}
}

// sendError sends an error response to the client.
func (s *Server) sendError(conn net.Conn, errMsg string) {
	s.sendResponse(conn, Response{Success: false, Error: errMsg})
}

// Client provides IPC communication with the daemon.
type Client struct {
	socketPath string
	timeout    time.Duration
}

// newDirectClient creates an IPC client using the direct socket path (no registry fallback).
// Only for use within the daemon package (daemon-to-self IPC, liveness checks).
// CLI code must use NewClientForCurrentRepo() or NewClientForCurrentRepoWithTimeout()
// which fall back to the daemon registry when workspace IDs drift.
func newDirectClient() *Client {
	return &Client{
		socketPath: SocketPath(),
		// Localhost Unix socket IPC is <5ms in practice.
		// 50ms provides 10x headroom while enabling fast failure detection.
		timeout: 50 * time.Millisecond,
	}
}

// newDirectClientWithTimeout creates an IPC client with custom timeout (no registry fallback).
// Same restriction as newDirectClient: daemon-package-internal only.
func newDirectClientWithTimeout(timeout time.Duration) *Client {
	return &Client{
		socketPath: SocketPath(),
		timeout:    timeout,
	}
}

// NewClientForCurrentRepo creates an IPC client that uses the registry to find
// the daemon for the current repo, even if its workspace ID differs from what
// the current binary computes (e.g., after a workspace ID format change).
// Use this in status/stop/restart commands where you need to reach the daemon
// for the project in the current directory regardless of workspace ID drift.
func NewClientForCurrentRepo() *Client {
	return &Client{
		socketPath: resolveSocketPath(),
		timeout:    50 * time.Millisecond,
	}
}

// NewClientForCurrentRepoWithTimeout is like NewClientForCurrentRepo but with a custom timeout.
func NewClientForCurrentRepoWithTimeout(timeout time.Duration) *Client {
	return &Client{
		socketPath: resolveSocketPath(),
		timeout:    timeout,
	}
}

// NewClientWithSocket creates an IPC client for a specific socket path.
// Used when connecting to daemons for other workspaces.
func NewClientWithSocket(socketPath string) *Client {
	return &Client{
		socketPath: socketPath,
		timeout:    50 * time.Millisecond,
	}
}

// NewClientWithSocketAndTimeout creates an IPC client for a specific socket path with custom timeout.
// Use longer timeouts for stop operations where the daemon may be busy.
func NewClientWithSocketAndTimeout(socketPath string, timeout time.Duration) *Client {
	return &Client{
		socketPath: socketPath,
		timeout:    timeout,
	}
}

// Connect attempts to connect to the daemon.
// Returns error if daemon is not running.
func (c *Client) Connect() (net.Conn, error) {
	conn, err := dial(c.socketPath)
	if err != nil {
		return nil, fmt.Errorf("connect to daemon: %w", err)
	}
	return conn, nil
}

// sendMessage sends a message and receives the response.
func (c *Client) sendMessage(msg Message) (*Response, error) {
	conn, err := c.Connect()
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	// Use separate deadlines for write and read phases.
	// Write should be fast (local socket); read may take longer for complex operations.
	// This prevents long-running operations from timing out mid-stream because
	// the combined deadline was consumed during the read phase.
	writeDeadline := 5 * time.Second
	if c.timeout < writeDeadline {
		writeDeadline = c.timeout
	}
	conn.SetWriteDeadline(time.Now().Add(writeDeadline))

	// always include workspace ID for request routing/validation
	if msg.WorkspaceID == "" {
		msg.WorkspaceID = CurrentWorkspaceID()
	}

	// identify which clone/worktree is sending this message
	if msg.CallerID == "" {
		msg.CallerID = LegacyWorkspaceID()
	}

	// stamp the caller's CLI version so the daemon can detect when a
	// long-running daemon is being driven by a CLI that's many releases
	// behind (ox-mt3k). Older callers leave this empty; daemon treats
	// missing as "unknown".
	if msg.CallerVersion == "" {
		msg.CallerVersion = Version()
	}

	// send message
	data, _ := json.Marshal(msg)
	data = append(data, '\n')
	if _, err := conn.Write(data); err != nil {
		return nil, fmt.Errorf("write: %w", err)
	}

	// read response with full timeout (not reduced by write time)
	conn.SetReadDeadline(time.Now().Add(c.timeout))

	// Limit response size to prevent OOM from malicious/buggy daemon
	reader := bufio.NewReader(io.LimitReader(conn, maxIPCMessageSize))
	line, err := reader.ReadBytes('\n')
	if err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}

	var resp Response
	if err := json.Unmarshal(line, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	return &resp, nil
}

// SendOneWay sends a message without waiting for response.
// Connect, write, close immediately - truly fire-and-forget at IPC layer.
// Used for heartbeats and other non-blocking notifications.
func (c *Client) SendOneWay(msg Message) error {
	conn, err := dial(c.socketPath)
	if err != nil {
		return err
	}
	defer conn.Close()

	// short write deadline (50ms should be plenty for localhost)
	conn.SetWriteDeadline(time.Now().Add(50 * time.Millisecond))

	if msg.WorkspaceID == "" {
		msg.WorkspaceID = CurrentWorkspaceID()
	}
	if msg.CallerID == "" {
		msg.CallerID = LegacyWorkspaceID()
	}

	data, _ := json.Marshal(msg)
	data = append(data, '\n')
	_, err = conn.Write(data)
	return err // don't wait for response
}

// Ping checks if the daemon is responsive.
func (c *Client) Ping() error {
	resp, err := c.sendMessage(Message{Type: MsgTypePing})
	if err != nil {
		return err
	}
	if !resp.Success {
		return errors.New(resp.Error)
	}
	return nil
}

// IsHealthy checks if the daemon is running AND responsive.
// Returns nil if healthy, error describing the failure mode otherwise.
//
// Uses a 100ms timeout - plenty for localhost IPC. If you need custom
// timeouts, use NewClientForCurrentRepoWithTimeout(t).Ping() directly.
func IsHealthy() error {
	client := NewClientForCurrentRepoWithTimeout(100 * time.Millisecond)
	if err := client.Ping(); err != nil {
		// distinguish "no socket" from "socket exists but unresponsive"
		socketPath := client.socketPath
		if socketPath == "" {
			socketPath = SocketPath()
		}
		if _, statErr := os.Stat(socketPath); os.IsNotExist(statErr) {
			return errors.New("daemon not running")
		}
		return fmt.Errorf("daemon not responsive: %w", err)
	}

	return nil
}

// Status gets the daemon status.
func (c *Client) Status() (*StatusData, error) {
	resp, err := c.sendMessage(Message{Type: MsgTypeStatus})
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, errors.New(resp.Error)
	}

	var status StatusData
	if err := json.Unmarshal(resp.Data, &status); err != nil {
		return nil, fmt.Errorf("unmarshal status: %w", err)
	}
	return &status, nil
}

// Sessions gets active agent sessions from this daemon.
// Deprecated: Use Instances() instead.
func (c *Client) Sessions() ([]AgentSession, error) {
	resp, err := c.sendMessage(Message{Type: MsgTypeSessions})
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, errors.New(resp.Error)
	}

	var sessResp SessionsResponse
	if err := json.Unmarshal(resp.Data, &sessResp); err != nil {
		return nil, fmt.Errorf("unmarshal sessions: %w", err)
	}
	return sessResp.Sessions, nil
}

// Instances gets active agent instances from this daemon.
func (c *Client) Instances() ([]InstanceInfo, error) {
	resp, err := c.sendMessage(Message{Type: MsgTypeInstances})
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, errors.New(resp.Error)
	}

	var instResp InstancesResponse
	if err := json.Unmarshal(resp.Data, &instResp); err != nil {
		return nil, fmt.Errorf("unmarshal instances: %w", err)
	}
	return instResp.Instances, nil
}

// SyncHistory gets the recent sync history.
func (c *Client) SyncHistory() ([]SyncEvent, error) {
	resp, err := c.sendMessage(Message{Type: MsgTypeSyncHistory})
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, errors.New(resp.Error)
	}

	var history []SyncEvent
	if err := json.Unmarshal(resp.Data, &history); err != nil {
		return nil, fmt.Errorf("unmarshal sync history: %w", err)
	}
	return history, nil
}

// Doctor triggers daemon health checks including anti-entropy (self-healing).
// Returns the results of the health checks.
func (c *Client) Doctor() (*DoctorResponse, error) {
	resp, err := c.sendMessage(Message{Type: MsgTypeDoctor})
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, errors.New(resp.Error)
	}

	var doctorResp DoctorResponse
	if err := json.Unmarshal(resp.Data, &doctorResp); err != nil {
		return nil, fmt.Errorf("unmarshal doctor response: %w", err)
	}
	return &doctorResp, nil
}

// TriggerGC requests the daemon to force a GC reclone of team contexts.
func (c *Client) TriggerGC() (*TriggerGCResponse, error) {
	resp, err := c.sendMessage(Message{Type: MsgTypeTriggerGC})
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, errors.New(resp.Error)
	}

	var gcResp TriggerGCResponse
	if err := json.Unmarshal(resp.Data, &gcResp); err != nil {
		return nil, fmt.Errorf("unmarshal trigger_gc response: %w", err)
	}
	return &gcResp, nil
}

// Whispers queries whisper entries for an agent from the daemon.
// Returns entries since the agent last checked, filtered by attention and topics.
func (c *Client) Whispers(agentID string, attention string, topics []string) (*WhispersResponse, error) {
	payload, _ := json.Marshal(WhispersPayload{
		AgentID:   agentID,
		Attention: attention,
		Topics:    topics,
	})
	resp, err := c.sendMessage(Message{
		Type:    MsgTypeWhispers,
		Payload: payload,
	})
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, errors.New(resp.Error)
	}
	var result WhispersResponse
	if err := json.Unmarshal(resp.Data, &result); err != nil {
		return nil, fmt.Errorf("unmarshal whispers: %w", err)
	}
	return &result, nil
}

// WhisperHistory queries a page of whispers for an agent without advancing the cursor.
// Used for inspection — shows what has been or will be whispered to an agent.
// Pass before=zero and limit=0 to get the first page (most recent 50 entries).
// Use resp.NextCursor as before in subsequent calls when resp.HasMore is true.
func (c *Client) WhisperHistory(agentID string, before time.Time, limit int) (*WhisperHistoryResponse, error) {
	payload, _ := json.Marshal(WhisperHistoryPayload{AgentID: agentID, Before: before, Limit: limit})
	resp, err := c.sendMessage(Message{
		Type:    MsgTypeWhisperHistory,
		Payload: payload,
	})
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, errors.New(resp.Error)
	}
	var result WhisperHistoryResponse
	if err := json.Unmarshal(resp.Data, &result); err != nil {
		return nil, fmt.Errorf("unmarshal whisper history: %w", err)
	}
	return &result, nil
}

// RequestSync requests the daemon to perform a sync.
func (c *Client) RequestSync() error {
	resp, err := c.sendMessage(Message{Type: MsgTypeSync})
	if err != nil {
		return err
	}
	if !resp.Success {
		return errors.New(resp.Error)
	}
	return nil
}

// SyncWithProgress requests the daemon to perform a sync with progress updates.
// The onProgress callback is called for each progress update (may be nil).
// Uses an idle timeout: the deadline resets on each progress message, so the
// connection stays alive as long as the daemon is making progress.
func (c *Client) SyncWithProgress(onProgress ProgressCallback) error {
	conn, err := c.Connect()
	if err != nil {
		return err
	}
	defer conn.Close()

	// idle timeout — reset on each progress message
	idleTimeout := 30 * time.Second
	if c.timeout > 0 && c.timeout < idleTimeout {
		idleTimeout = c.timeout
	}
	conn.SetDeadline(time.Now().Add(idleTimeout))

	msg := Message{
		Type:        MsgTypeSync,
		WorkspaceID: CurrentWorkspaceID(),
	}

	// send request
	data, _ := json.Marshal(msg)
	data = append(data, '\n')
	if _, err := conn.Write(data); err != nil {
		return fmt.Errorf("write: %w", err)
	}

	// read responses until we get a final one (no progress field)
	reader := bufio.NewReader(conn)
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			return fmt.Errorf("read: %w", err)
		}

		var resp ProgressResponse
		if err := json.Unmarshal(line, &resp); err != nil {
			return fmt.Errorf("unmarshal response: %w", err)
		}

		// check for progress update
		if resp.Progress != nil {
			conn.SetDeadline(time.Now().Add(idleTimeout))
			if onProgress != nil {
				onProgress(resp.Progress.Stage, resp.Progress.Percent, resp.Progress.Message)
			}
			continue // keep reading
		}

		// final response
		if !resp.Success {
			return errors.New(resp.Error)
		}
		return nil
	}
}

// TeamSyncWithProgress requests the daemon to sync all team contexts with progress updates.
// The onProgress callback is called for each progress update (may be nil).
// Uses an idle timeout: the deadline resets on each progress message, so the
// connection stays alive as long as the daemon is making progress.
//
// It returns the per-team results carried in the final IPC response so callers
// can report accurate per-team status and metadata. The results are returned
// even when err is non-nil (the daemon reports a non-nil error when any team
// failed to sync), so callers can still surface partial outcomes.
func (c *Client) TeamSyncWithProgress(onProgress ProgressCallback) ([]TeamSyncResult, error) {
	conn, err := c.Connect()
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	// idle timeout — reset on each progress message
	idleTimeout := 60 * time.Second
	if c.timeout > 0 && c.timeout < idleTimeout {
		idleTimeout = c.timeout
	}
	conn.SetDeadline(time.Now().Add(idleTimeout))

	msg := Message{
		Type:        MsgTypeTeamSync,
		WorkspaceID: CurrentWorkspaceID(),
	}

	// send request
	data, _ := json.Marshal(msg)
	data = append(data, '\n')
	if _, err := conn.Write(data); err != nil {
		return nil, fmt.Errorf("write: %w", err)
	}

	// read responses until we get a final one (no progress field)
	reader := bufio.NewReader(conn)
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			return nil, fmt.Errorf("read: %w", err)
		}

		var resp ProgressResponse
		if err := json.Unmarshal(line, &resp); err != nil {
			return nil, fmt.Errorf("unmarshal response: %w", err)
		}

		// check for progress update
		if resp.Progress != nil {
			conn.SetDeadline(time.Now().Add(idleTimeout))
			if onProgress != nil {
				onProgress(resp.Progress.Stage, resp.Progress.Percent, resp.Progress.Message)
			}
			continue // keep reading
		}

		// final response — parse per-team results (present on both success and
		// partial-failure responses) before deciding the overall error.
		//
		// nil-vs-empty contract: a current daemon always sends a `data` array
		// (possibly `[]`), so results is non-nil here. A legacy pre-change daemon
		// sends no `data` field, leaving results nil. Callers use that distinction
		// to tell "team genuinely not found" (non-nil, no match) apart from "old
		// daemon, status unknown" (nil).
		var results []TeamSyncResult
		if len(resp.Data) > 0 {
			if uErr := json.Unmarshal(resp.Data, &results); uErr != nil {
				return nil, fmt.Errorf("unmarshal team sync results: %w", uErr)
			}
		}
		if !resp.Success {
			return results, errors.New(resp.Error)
		}
		return results, nil
	}
}

// Stop requests the daemon to stop.
func (c *Client) Stop() error {
	resp, err := c.sendMessage(Message{Type: MsgTypeStop})
	if err != nil {
		return err
	}
	if !resp.Success {
		return errors.New(resp.Error)
	}
	return nil
}

// ProgressCallback is called for each progress update during long operations.
// Percent is nil when unknown.
type ProgressCallback func(stage string, percent *int, message string)

// Checkout requests the daemon to clone a repository.
// The onProgress callback is called for each progress update (may be nil).
// Uses a long timeout (60s) since clones can take time.
func (c *Client) Checkout(payload CheckoutPayload, onProgress ProgressCallback) (*CheckoutResult, error) {
	conn, err := c.Connect()
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	// use configured timeout, with minimum floor for clone operations
	checkoutTimeout := 60 * time.Second
	if c.timeout > 0 && c.timeout < checkoutTimeout {
		checkoutTimeout = c.timeout
	}
	conn.SetDeadline(time.Now().Add(checkoutTimeout))

	// marshal payload
	payloadData, _ := json.Marshal(payload)
	msg := Message{
		Type:        MsgTypeCheckout,
		WorkspaceID: CurrentWorkspaceID(),
		Payload:     payloadData,
	}

	// send request
	data, _ := json.Marshal(msg)
	data = append(data, '\n')
	if _, err := conn.Write(data); err != nil {
		return nil, fmt.Errorf("write: %w", err)
	}

	// read responses until we get a final one (no progress field)
	reader := bufio.NewReader(conn)
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			return nil, fmt.Errorf("read: %w", err)
		}

		var resp ProgressResponse
		if err := json.Unmarshal(line, &resp); err != nil {
			return nil, fmt.Errorf("unmarshal response: %w", err)
		}

		// check for progress update
		if resp.Progress != nil {
			if onProgress != nil {
				onProgress(resp.Progress.Stage, resp.Progress.Percent, resp.Progress.Message)
			}
			continue // keep reading
		}

		// final response
		if !resp.Success {
			return nil, errors.New(resp.Error)
		}

		var result CheckoutResult
		if err := json.Unmarshal(resp.Data, &result); err != nil {
			return nil, fmt.Errorf("unmarshal result: %w", err)
		}
		return &result, nil
	}
}

// GetUnviewedErrors retrieves unviewed daemon errors.
func (c *Client) GetUnviewedErrors() ([]StoredError, error) {
	resp, err := c.sendMessage(Message{Type: MsgTypeGetErrors})
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, errors.New(resp.Error)
	}

	var storedErrors []StoredError
	if err := json.Unmarshal(resp.Data, &storedErrors); err != nil {
		return nil, fmt.Errorf("unmarshal errors: %w", err)
	}
	return storedErrors, nil
}

// MarkErrorsViewed marks errors as viewed.
// If ids is empty, marks all errors as viewed.
func (c *Client) MarkErrorsViewed(ids []string) error {
	payload := MarkErrorsPayload{IDs: ids}
	payloadData, _ := json.Marshal(payload)
	resp, err := c.sendMessage(Message{
		Type:    MsgTypeMarkErrors,
		Payload: payloadData,
	})
	if err != nil {
		return err
	}
	if !resp.Success {
		return errors.New(resp.Error)
	}
	return nil
}

// CodeIndex requests the daemon to index code with progress updates.
// Uses an idle timeout: the deadline resets on each progress message, so the
// connection stays alive as long as the daemon is making progress. Indexing
// large repos can take many minutes but emits frequent progress updates.
func (c *Client) CodeIndex(payload CodeIndexPayload, onProgress ProgressCallback) (*CodeIndexResult, error) {
	conn, err := c.Connect()
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	// idle timeout — reset on each progress message
	idleTimeout := 60 * time.Second
	if c.timeout > 0 && c.timeout < idleTimeout {
		idleTimeout = c.timeout
	}
	conn.SetDeadline(time.Now().Add(idleTimeout))

	payloadData, _ := json.Marshal(payload)
	msg := Message{
		Type:        MsgTypeCodeIndex,
		WorkspaceID: CurrentWorkspaceID(),
		Payload:     payloadData,
	}

	data, _ := json.Marshal(msg)
	data = append(data, '\n')
	if _, err := conn.Write(data); err != nil {
		return nil, fmt.Errorf("write: %w", err)
	}

	reader := bufio.NewReader(conn)
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			return nil, fmt.Errorf("read: %w", err)
		}

		var resp ProgressResponse
		if err := json.Unmarshal(line, &resp); err != nil {
			return nil, fmt.Errorf("unmarshal response: %w", err)
		}

		if resp.Progress != nil {
			conn.SetDeadline(time.Now().Add(idleTimeout))
			if onProgress != nil {
				onProgress(resp.Progress.Stage, resp.Progress.Percent, resp.Progress.Message)
			}
			continue
		}

		if !resp.Success {
			return nil, errors.New(resp.Error)
		}

		var result CodeIndexResult
		if len(resp.Data) > 0 {
			if err := json.Unmarshal(resp.Data, &result); err != nil {
				return nil, fmt.Errorf("unmarshal result: %w", err)
			}
		}
		return &result, nil
	}
}

// CodeStatus requests the current code index status from the daemon.
func (c *Client) CodeStatus() (*CodeDBStats, error) {
	resp, err := c.sendMessage(Message{Type: MsgTypeCodeStatus})
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, errors.New(resp.Error)
	}
	var stats CodeDBStats
	if err := json.Unmarshal(resp.Data, &stats); err != nil {
		return nil, fmt.Errorf("unmarshal code status: %w", err)
	}
	return &stats, nil
}

// SettingsGet returns the daemon's cached CLI feature flag settings.
// Returns nil with no error if the daemon hasn't fetched settings yet.
func (c *Client) SettingsGet() (*flags.CLISettingsResponse, error) {
	resp, err := c.sendMessage(Message{Type: MsgTypeSettingsGet})
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, errors.New(resp.Error)
	}
	// null response means daemon has no cached settings
	if string(resp.Data) == "null" {
		return nil, nil
	}
	var settings flags.CLISettingsResponse
	if err := json.Unmarshal(resp.Data, &settings); err != nil {
		return nil, fmt.Errorf("unmarshal settings: %w", err)
	}
	return &settings, nil
}

// SessionFinalize sends a fire-and-forget request to finalize a session.
// The daemon will upload to LFS, commit, push, and generate summary artifacts.
func (c *Client) SessionFinalize(payload SessionFinalizeIPCPayload) error {
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal session_finalize payload: %w", err)
	}
	// fire-and-forget: ignore response
	return c.SendOneWay(Message{
		Type:    MsgTypeSessionFinalize,
		Payload: payloadBytes,
	})
}

// SessionWatchStart sends a fire-and-forget request to start tailing a session file.
// The daemon begins tailing the agent's native session file and writing to raw.jsonl.
func (c *Client) SessionWatchStart(payload SessionWatchStartPayload) error {
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal session_watch_start payload: %w", err)
	}
	return c.SendOneWay(Message{
		Type:    MsgTypeSessionWatchStart,
		Payload: payloadBytes,
	})
}

// SessionWatchStop sends a fire-and-forget request to stop tailing a session.
func (c *Client) SessionWatchStop(payload SessionWatchStopPayload) error {
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal session_watch_stop payload: %w", err)
	}
	return c.SendOneWay(Message{
		Type:    MsgTypeSessionWatchStop,
		Payload: payloadBytes,
	})
}

// Murmur sends a murmur write+commit request to the daemon (fire-and-forget).
// The daemon writes the murmur file and commits it; the CLI returns immediately.
func (c *Client) Murmur(payload MurmurPayload) error {
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal murmur payload: %w", err)
	}
	return c.SendOneWay(Message{
		Type:    MsgTypeMurmur,
		Payload: payloadBytes,
	})
}

// MurmurPause sends a fire-and-forget request to pause murmur nudging for an agent.
func (c *Client) MurmurPause(agentID string) error {
	payloadBytes, err := json.Marshal(MurmurPausePayload{AgentID: agentID})
	if err != nil {
		return fmt.Errorf("marshal murmur_pause payload: %w", err)
	}
	return c.SendOneWay(Message{
		Type:    MsgTypeMurmurPause,
		Payload: payloadBytes,
	})
}

// MurmurResume sends a fire-and-forget request to resume murmur nudging for an agent.
func (c *Client) MurmurResume(agentID string) error {
	payloadBytes, err := json.Marshal(MurmurPausePayload{AgentID: agentID})
	if err != nil {
		return fmt.Errorf("marshal murmur_resume payload: %w", err)
	}
	return c.SendOneWay(Message{
		Type:    MsgTypeMurmurResume,
		Payload: payloadBytes,
	})
}

// TryConnectForCheckout attempts to connect for checkout operations.
// Uses a long timeout since clones can take time.
func TryConnectForCheckout() *Client {
	client := NewClientForCurrentRepoWithTimeout(60 * time.Second)
	if err := client.Ping(); err != nil {
		return nil
	}
	return client
}

// TryConnect attempts to connect to the daemon.
// Returns the client if connected, nil otherwise.
func TryConnect() *Client {
	client := NewClientForCurrentRepo()
	if err := client.Ping(); err != nil {
		return nil
	}
	return client
}

// TryConnectForSync attempts to connect for sync operations.
// Uses a longer timeout since syncs can take time.
func TryConnectForSync() *Client {
	client := NewClientForCurrentRepoWithTimeout(30 * time.Second)
	if err := client.Ping(); err != nil {
		return nil
	}
	return client
}

// GetAllSessions queries all running daemons and aggregates their agent sessions.
// Returns sessions from all workspaces, sorted by last heartbeat (most recent first).
// Deprecated: Use GetAllInstances instead.
func GetAllSessions() ([]AgentSession, error) {
	daemons, err := ListRunningDaemons()
	if err != nil {
		return nil, fmt.Errorf("list daemons: %w", err)
	}

	var allSessions []AgentSession
	for _, d := range daemons {
		client := NewClientWithSocket(d.SocketPath)
		sessions, err := client.Sessions()
		if err != nil {
			// daemon might have died between list and query, skip it
			continue
		}
		// enrich sessions with workspace path from daemon info
		for i := range sessions {
			if sessions[i].WorkspacePath == "" {
				sessions[i].WorkspacePath = d.WorkspacePath
			}
		}
		allSessions = append(allSessions, sessions...)
	}

	// sort by last heartbeat (most recent first)
	slices.SortFunc(allSessions, func(a, b AgentSession) int {
		return b.LastHeartbeat.Compare(a.LastHeartbeat)
	})

	return allSessions, nil
}

// GetAllInstances queries all running daemons and aggregates their agent instances.
// Returns instances from all workspaces, sorted by last heartbeat (most recent first).
func GetAllInstances() ([]InstanceInfo, error) {
	daemons, err := ListRunningDaemons()
	if err != nil {
		return nil, fmt.Errorf("list daemons: %w", err)
	}

	var allInstances []InstanceInfo
	for _, d := range daemons {
		client := NewClientWithSocket(d.SocketPath)
		instances, err := client.Instances()
		if err != nil {
			// daemon might have died between list and query, skip it
			continue
		}
		// enrich instances with workspace path from daemon info
		for i := range instances {
			if instances[i].WorkspacePath == "" {
				instances[i].WorkspacePath = d.WorkspacePath
			}
		}
		allInstances = append(allInstances, instances...)
	}

	// sort by last heartbeat (most recent first)
	slices.SortFunc(allInstances, func(a, b InstanceInfo) int {
		return b.LastHeartbeat.Compare(a.LastHeartbeat)
	})

	return allInstances, nil
}
