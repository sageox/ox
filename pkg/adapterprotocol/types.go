// Package adapterprotocol defines the wire types for the ox adapter protocol.
//
// External adapter authors import this package to get typed request/response
// structs, the protocol version constant, and standard error codes. This is
// the only ox package external adapters need to import.
//
// The canonical protocol reference is adapter/protocol/spec.md.
package adapterprotocol

import "encoding/json"

// ProtocolVersion is the current adapter protocol version.
// Adapters return this in their info response. ox refuses adapters
// whose major version is lower than its own minimum supported.
const ProtocolVersion = 1

// --- Adapter types ---

// AdapterType identifies what subsystem an adapter belongs to.
const (
	TypeSession = "session"
	TypeVCS     = "vcs"
	TypeIndexer = "indexer"
	TypeTest    = "test"
)

// --- Capabilities ---

const (
	CapSessionReader      = "session_reader"
	CapHookInstaller      = "hook_installer"
	CapIncrementalReader  = "incremental_reader"
	CapFileWatcher        = "file_watcher"
	CapSessionImporter    = "session_importer"
	CapServeMode          = "serve_mode"
	CapSubagentController = "subagent_controller"
	CapRulesInstaller     = "rules_installer"
	CapCapturePrior       = "capture_prior"
)

// --- Entry roles ---

// Role constants for RawEntry.Role field.
const (
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleSystem    = "system"
	RoleTool      = "tool"
)

// --- One-shot response types ---

// InfoResponse is returned by the `info` subcommand.
type InfoResponse struct {
	ProtocolVersion int             `json:"protocol_version"`
	Name            string          `json:"name"`
	DisplayName     string          `json:"display_name"`
	Version         string          `json:"version"`
	Type            string          `json:"type"` // "session", "vcs", "indexer", "test"
	Capabilities    []string        `json:"capabilities"`
	HookEnvValues   []string        `json:"hook_env_values,omitempty"`
	RequiredEnv     []string        `json:"required_env,omitempty"`
	ServeMode       bool            `json:"serve_mode"`
	SubagentConfig  *SubagentConfig `json:"subagent_config,omitempty"`
}

// DetectResponse is returned by the `detect` subcommand.
type DetectResponse struct {
	Detected bool   `json:"detected"`
	Reason   string `json:"reason"`
}

// HookParams are passed to install-hooks, check-hooks, and uninstall-hooks.
type HookParams struct {
	RepoRoot string `json:"repo_root"`
	Scope    string `json:"scope"` // "project" or "user"
}

// InstallHooksResponse is returned by `install-hooks`.
type InstallHooksResponse struct {
	Installed    bool     `json:"installed"`
	FilesWritten []string `json:"files_written"`
	Hooks        []string `json:"hooks"`
}

// CheckHooksResponse is returned by `check-hooks`.
type CheckHooksResponse struct {
	Installed bool     `json:"installed"`
	Scope     string   `json:"scope"`
	HookFiles []string `json:"hook_files"`
}

// UninstallHooksResponse is returned by `uninstall-hooks`.
type UninstallHooksResponse struct {
	Uninstalled   bool     `json:"uninstalled"`
	FilesModified []string `json:"files_modified"`
}

// RulesParams are passed to install-rules, check-rules, and uninstall-rules.
type RulesParams struct {
	RepoRoot string `json:"repo_root"`
	Version  string `json:"version"` // ox version for stamped content
}

// InstallRulesResponse is returned by `install-rules`.
type InstallRulesResponse struct {
	Installed    bool     `json:"installed"`
	FilesWritten []string `json:"files_written"`
}

// CheckRulesResponse is returned by `check-rules`.
type CheckRulesResponse struct {
	Installed bool     `json:"installed"`
	Missing   []string `json:"missing,omitempty"`
	Stale     []string `json:"stale,omitempty"`
	RulesDir  string   `json:"rules_dir"`
}

// UninstallRulesResponse is returned by `uninstall-rules`.
type UninstallRulesResponse struct {
	Uninstalled  bool     `json:"uninstalled"`
	FilesRemoved []string `json:"files_removed"`
}

// ReadParams are passed to `read` and `read-metadata`.
type ReadParams struct {
	SessionFile string `json:"session_file"`
}

// ReadResult is returned by `read`.
type ReadResult struct {
	Entries  []RawEntry       `json:"entries"`
	Metadata *SessionMetadata `json:"metadata,omitempty"`
}

// ReadMetadataResult is returned by `read-metadata`.
type ReadMetadataResult struct {
	AgentVersion string `json:"agent_version,omitempty"`
	Model        string `json:"model,omitempty"`
}

// DiagnoseParams are passed to `diagnose`.
type DiagnoseParams struct {
	RepoRoot string `json:"repo_root"`
	Scope    string `json:"scope"`   // "project" or "user"
	Version  string `json:"version"` // ox version for stale-rules detection
}

// DiagnoseResult is returned by `diagnose`.
type DiagnoseResult struct {
	OK     bool            `json:"ok"`
	Issues []DiagnoseIssue `json:"issues"`
}

// DiagnoseIssue is a single diagnostic finding.
type DiagnoseIssue struct {
	Slug     string `json:"slug"`
	Severity string `json:"severity"` // "error", "warning", "info"
	Title    string `json:"title"`
	Detail   string `json:"detail"`
	Fix      string `json:"fix,omitempty"`
	FixSafe  bool   `json:"fix_safe"`
}

// --- Session data types ---

// RawEntry represents a single conversation entry from any agent.
type RawEntry struct {
	Timestamp  string `json:"timestamp"`       // RFC3339 UTC
	Role       string `json:"role"`             // "user" | "assistant" | "system" | "tool"
	Content    string `json:"content"`
	EID        string `json:"eid,omitempty"`    // 5-char alphanumeric entry identifier
	ToolName   string `json:"tool_name,omitempty"`
	ToolInput  string `json:"tool_input,omitempty"`
	ToolOutput string `json:"tool_output,omitempty"`
	IsError    bool   `json:"is_error,omitempty"`
	CallID     string `json:"call_id,omitempty"`
}

// SessionMetadata contains metadata extracted from agent session files.
type SessionMetadata struct {
	AgentVersion string `json:"agent_version,omitempty"`
	Model        string `json:"model,omitempty"`
}

// --- Serve mode wire types ---

// Request is a daemon-to-adapter request in serve mode.
type Request struct {
	ID     int             `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

// Response is an adapter-to-daemon response in serve mode.
type Response struct {
	ID     int       `json:"id"`
	Result any       `json:"result,omitempty"`
	Error  *RPCError `json:"error,omitempty"`
}

// RPCError is a structured error in serve-mode responses.
type RPCError struct {
	Code    string `json:"code"` // "method_not_found", "invalid_params", "internal_error"
	Message string `json:"message"`
}

// Standard error codes for RPCError.Code.
const (
	ErrCodeMethodNotFound = "method_not_found"
	ErrCodeInvalidParams  = "invalid_params"
	ErrCodeInternalError  = "internal_error"
)

// Event is an adapter-initiated push message (no request ID).
type Event struct {
	Event   string          `json:"event"` // "entries"
	AgentID string          `json:"agent_id"`
	Data    json.RawMessage `json:"data"`
}

// EntriesEventData is the data payload for "entries" events.
type EntriesEventData struct {
	Entries   []RawEntry `json:"entries"`
	NewOffset int64      `json:"new_offset"`
}

// --- Serve mode method names ---

const (
	MethodFindSession    = "find-session"
	MethodReadFromOffset = "read-from-offset"
	MethodEndSession     = "end-session"
	MethodShutdown       = "shutdown"
)

// --- Subagent serve mode method names ---

const (
	MethodSpawnSubagent  = "spawn-subagent"
	MethodSubagentStatus = "subagent-status"
	MethodCancelSubagent = "cancel-subagent"
)

// --- Subagent event names ---

const (
	EventSubagentProgress  = "subagent.progress"
	EventSubagentCompleted = "subagent.completed"
	EventSubagentFailed    = "subagent.failed"
)

// --- Subagent worker statuses ---

const (
	WorkerStatusStarting  = "starting"
	WorkerStatusRunning   = "running"
	WorkerStatusCompleted = "completed"
	WorkerStatusFailed    = "failed"
	WorkerStatusCanceled  = "canceled"
	WorkerStatusTimedOut  = "timed_out"
	WorkerStatusCanceling = "canceling"
)

// --- Subagent capability config ---

// SubagentConfig describes the adapter's subagent controller capabilities.
// Returned in InfoResponse when the adapter declares CapSubagentController.
type SubagentConfig struct {
	MaxConcurrent  int      `json:"max_concurrent"`
	Models         []string `json:"models,omitempty"`
	DefaultModel   string   `json:"default_model,omitempty"`
	CredentialHint string   `json:"credential_hint,omitempty"`
}

// --- Subagent serve mode param/result types ---

// SpawnSubagentParams are the params for the spawn-subagent serve method.
type SpawnSubagentParams struct {
	WorkerID   string         `json:"worker_id"`
	AgentID    string         `json:"agent_id"`
	RepoID     string         `json:"repo_id"`
	TeamID     string         `json:"team_id,omitempty"`
	RepoRoot   string         `json:"repo_root"`
	Model      string         `json:"model,omitempty"`
	Task       string         `json:"task"`
	Context    map[string]any `json:"context,omitempty"`
	TimeoutSec int            `json:"timeout_sec,omitempty"`
}

// SpawnSubagentResult is the result for the spawn-subagent serve method.
type SpawnSubagentResult struct {
	WorkerID string `json:"worker_id"`
	Status   string `json:"status"` // "starting"
}

// SubagentStatusParams are the params for the subagent-status serve method.
type SubagentStatusParams struct {
	WorkerID string `json:"worker_id"`
}

// SubagentStatusResult is the result for the subagent-status serve method.
type SubagentStatusResult struct {
	WorkerID     string `json:"worker_id"`
	Status       string `json:"status"`
	StartedAt    string `json:"started_at,omitempty"`
	ElapsedSec   int    `json:"elapsed_sec,omitempty"`
	OutputLines  int    `json:"output_lines,omitempty"`
	LastActivity string `json:"last_activity,omitempty"`
}

// CancelSubagentParams are the params for the cancel-subagent serve method.
type CancelSubagentParams struct {
	WorkerID string `json:"worker_id"`
	Reason   string `json:"reason,omitempty"`
}

// CancelSubagentResult is the result for the cancel-subagent serve method.
type CancelSubagentResult struct {
	WorkerID string `json:"worker_id"`
	Status   string `json:"status"` // "canceling"
}

// --- Subagent event data types ---

// SubagentProgressData is the data payload for "subagent.progress" events.
type SubagentProgressData struct {
	WorkerID    string `json:"worker_id"`
	OutputType  string `json:"output_type"` // "tool_use", "message", "thinking", "error"
	Tool        string `json:"tool,omitempty"`
	Description string `json:"description,omitempty"`
	Offset      int64  `json:"offset,omitempty"`
}

// SubagentCompletedData is the data payload for "subagent.completed" events.
type SubagentCompletedData struct {
	WorkerID      string   `json:"worker_id"`
	ExitCode      int      `json:"exit_code"`
	DurationSec   int      `json:"duration_sec"`
	FilesModified []string `json:"files_modified,omitempty"`
	Summary       string   `json:"summary,omitempty"`
	SessionFile   string   `json:"session_file,omitempty"`
	FinalOffset   int64    `json:"final_offset,omitempty"`
}

// SubagentFailedData is the data payload for "subagent.failed" events.
type SubagentFailedData struct {
	WorkerID    string `json:"worker_id"`
	ExitReason  string `json:"exit_reason"` // "error", "canceled", "timed_out", "adapter_crash"
	ExitCode    int    `json:"exit_code"`
	DurationSec int    `json:"duration_sec"`
	Error       string `json:"error,omitempty"`
	SessionFile string `json:"session_file,omitempty"`
}

// --- Serve mode param/result types ---

// FindSessionParams are the params for the find-session serve method.
type FindSessionParams struct {
	AgentID        string `json:"agent_id"`
	RepoID         string `json:"repo_id"`
	TeamID         string `json:"team_id,omitempty"`
	RepoRoot       string `json:"repo_root"`
	Since          string `json:"since"`                      // RFC3339
	AgentSessionID string `json:"agent_session_id,omitempty"` // native session ID from agent, enables direct file lookup
}

// FindSessionResult is the result for the find-session serve method.
type FindSessionResult struct {
	SessionFile string `json:"session_file"`
	Offset      int64  `json:"offset"`
}

// ReadFromOffsetParams are the params for read-from-offset.
type ReadFromOffsetParams struct {
	AgentID     string `json:"agent_id"`
	RepoID      string `json:"repo_id"`
	SessionFile string `json:"session_file"`
	Offset      int64  `json:"offset"`
}

// ReadFromOffsetResult is the result for read-from-offset.
type ReadFromOffsetResult struct {
	Entries   []RawEntry `json:"entries"`
	NewOffset int64      `json:"new_offset"`
}

// ImportSessionParams are the params for the import-session one-shot command.
// It reads an entire session by its native agent session identifier.
type ImportSessionParams struct {
	SessionID string `json:"session_id"` // native session identifier (agent-specific)
	RepoRoot  string `json:"repo_root"`  // project root for context
}

// ImportSessionResult is the result for import-session.
type ImportSessionResult struct {
	Metadata *SessionMetadata `json:"metadata,omitempty"`
	Entries  []RawEntry       `json:"entries"`
}

// CapturePriorParams are the params for the capture-prior one-shot command.
// Each adapter implements its own logic to find and parse the most recent
// (or specified) session from the agent's native storage format.
type CapturePriorParams struct {
	SessionID string `json:"session_id,omitempty"` // native session ID (optional; adapter finds most recent if empty)
	RepoRoot  string `json:"repo_root"`            // project root for session discovery
	AgentID   string `json:"agent_id"`             // ox agent ID for metadata
	Title     string `json:"title,omitempty"`       // optional session title
}

// CapturePriorResult is the result for capture-prior.
type CapturePriorResult struct {
	Entries    []RawEntry       `json:"entries"`
	Metadata   *SessionMetadata `json:"metadata,omitempty"`
	AgentType  string           `json:"agent_type"`            // adapter name (e.g., "claude-code")
	SessionID  string           `json:"session_id,omitempty"`  // resolved native session ID
}

// EndSessionParams are the params for end-session.
type EndSessionParams struct {
	AgentID string `json:"agent_id"`
	RepoID  string `json:"repo_id"`
}
