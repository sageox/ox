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
	"sync"
	"time"
)

// maxIPCMessageSize limits the maximum size of an IPC message to prevent DoS.
// A malicious client could send a multi-GB line without newline, causing memory exhaustion.
const maxIPCMessageSize = 1 * 1024 * 1024 // 1MB

// maxConcurrentConnections limits the number of concurrent IPC connections.
// Prevents file descriptor or memory exhaustion from connection floods.
const maxConcurrentConnections = 100

// Message types for IPC communication.
const (
	MsgTypeStatus      = "status"
	MsgTypeSync        = "sync"
	MsgTypeTeamSync    = "team_sync" // on-demand team context sync
	MsgTypePing        = "ping"
	MsgTypeStop        = "stop"
	MsgTypeVersion     = "version"
	MsgTypeSyncHistory = "sync_history"
	MsgTypeHeartbeat   = "heartbeat"   // one-way, no response expected
	MsgTypeCheckout    = "checkout"    // synchronous git clone operation
	MsgTypeTelemetry   = "telemetry"   // one-way, no response expected
	MsgTypeFriction    = "friction"    // one-way, friction event for analytics
	MsgTypeGetErrors   = "get_errors"  // retrieve unviewed daemon errors
	MsgTypeMarkErrors  = "mark_errors" // mark errors as viewed
	MsgTypeSessions    = "sessions"    // get active agent sessions (deprecated: use instances)
	MsgTypeInstances   = "instances"   // get active agent instances
	MsgTypeDoctor      = "doctor"      // trigger daemon health checks (anti-entropy, etc.)
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
// See: docs/ai/analysis/february-2026-ipc-analysis.md

// Message represents an IPC message.
type Message struct {
	Type        string          `json:"type"`
	WorkspaceID string          `json:"workspace_id,omitempty"` // identifies the workspace/repo
	Payload     json.RawMessage `json:"payload,omitempty"`
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

	// workspaces being synced, keyed by type ("ledger", "team-context")
	// each type maps to a list of workspaces of that type (ledger has 1, team-context may have many)
	Workspaces map[string][]WorkspaceSyncStatus `json:"workspaces,omitempty"`

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
	ID       string    `json:"id"`                   // workspace ID (e.g., "ledger", team_id)
	Type     string    `json:"type"`                 // "ledger" or "team_context"
	Path     string    `json:"path"`                 // local filesystem path
	CloneURL string    `json:"clone_url,omitempty"`  // git remote URL
	Exists   bool      `json:"exists"`               // whether path exists locally
	TeamID   string    `json:"team_id,omitempty"`    // team ID (for team contexts)
	TeamName string    `json:"team_name,omitempty"`  // team name (for team contexts)
	TeamSlug string    `json:"team_slug,omitempty"` // kebab-case team slug
	LastSync time.Time `json:"last_sync,omitempty"`  // last successful sync
	LastErr  string    `json:"last_error,omitempty"` // last error message
	Syncing  bool      `json:"syncing,omitempty"`    // currently syncing
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
}

// InstancesResponse is the response for the instances IPC message.
type InstancesResponse struct {
	Instances []InstanceInfo `json:"instances"`
}

// DoctorResponse is the response for the doctor IPC message.
type DoctorResponse struct {
	AntiEntropyTriggered bool     `json:"anti_entropy_triggered"`
	ClonesTriggered      int      `json:"clones_triggered"`
	Errors               []string `json:"errors,omitempty"`
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

// Server handles IPC requests from clients.
type Server struct {
	logger   *slog.Logger
	listener net.Listener
	router   *MessageRouter
	mu       sync.Mutex
	connWg   sync.WaitGroup // tracks active connection handler goroutines
	connSem  chan struct{}  // semaphore for connection limit

	// callbacks for handling messages
	onSync             func() error                                                                     // simple sync (backward compat)
	onSyncWithProgress func(progress *ProgressWriter) error                                             // sync with progress
	onTeamSync         func(progress *ProgressWriter) error                                             // team context sync with progress
	onStop             func()                                                                           //
	onStatus           func() *StatusData                                                               //
	onActivity         func()                                                                           // called on any IPC activity
	onHeartbeat        func(payload json.RawMessage)                                                    //
	onCheckout         func(payload CheckoutPayload, progress *ProgressWriter) (*CheckoutResult, error) //
	onTelemetry        func(payload json.RawMessage)                                                    // fire-and-forget telemetry
	onFriction         func(payload FrictionPayload)                                                    // fire-and-forget friction event
	onGetErrors        func() []StoredError                                                             // get unviewed errors
	onMarkErrors       func(ids []string)                                                               // mark errors as viewed
	onSessions         func() []AgentSession                                                            // get active agent sessions (deprecated)
	onInstances        func() []InstanceInfo                                                            // get active agent instances
	onSyncHistory      func() []SyncEvent                                                               // get sync history
	onDoctor           func() *DoctorResponse                                                           // trigger health checks

	startTime time.Time
}

// NewServer creates a new IPC server.
func NewServer(logger *slog.Logger) *Server {
	s := &Server{
		logger:    logger,
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
	router.Register(MsgTypeGetErrors, handleGetErrors)
	router.Register(MsgTypeMarkErrors, handleMarkErrors)
	router.Register(MsgTypeCheckout, handleCheckout)
	router.Register(MsgTypeSessions, handleSessions)
	router.Register(MsgTypeInstances, handleInstances)
	router.Register(MsgTypeDoctor, handleDoctor)

	return router
}

// Handler implementations

// marshalResponse creates a success response with marshaled data.
// If marshaling fails, returns an error response instead of Success: true with nil data.
func marshalResponse(v any) *Response {
	data, err := json.Marshal(v)
	if err != nil {
		// this should never happen for our well-typed structs, but handle defensively
		return &Response{Success: false, Error: fmt.Sprintf("marshal error: %v", err)}
	}
	return &Response{Success: true, Data: data}
}

func handlePing(_ *Server, _ Message, _ net.Conn) HandlerResult {
	return HandlerResult{
		Response: &Response{Success: true, Data: json.RawMessage(`"pong"`)},
	}
}

func handleVersion(_ *Server, _ Message, _ net.Conn) HandlerResult {
	return HandlerResult{
		Response: &Response{Success: true, Data: json.RawMessage(`"1.0.0"`)},
	}
}

func handleStatus(s *Server, _ Message, _ net.Conn) HandlerResult {
	s.mu.Lock()
	handler := s.onStatus
	s.mu.Unlock()

	if handler != nil {
		status := handler()
		return HandlerResult{Response: marshalResponse(status)}
	}
	return HandlerResult{
		Response: &Response{Success: true, Data: json.RawMessage(`{}`)},
	}
}

func handleSyncHistory(s *Server, _ Message, _ net.Conn) HandlerResult {
	s.mu.Lock()
	handler := s.onSyncHistory
	s.mu.Unlock()

	if handler != nil {
		history := handler()
		return HandlerResult{Response: marshalResponse(history)}
	}
	return HandlerResult{
		Response: &Response{Success: true, Data: json.RawMessage(`[]`)},
	}
}

func handleSync(s *Server, _ Message, conn net.Conn) HandlerResult {
	s.mu.Lock()
	progressHandler := s.onSyncWithProgress
	legacyHandler := s.onSync
	s.mu.Unlock()

	var resp Response
	if progressHandler != nil {
		pw := &ProgressWriter{conn: conn}
		if err := progressHandler(pw); err != nil {
			resp = Response{Success: false, Error: err.Error()}
		} else {
			resp = Response{Success: true}
		}
	} else if legacyHandler != nil {
		if err := legacyHandler(); err != nil {
			resp = Response{Success: false, Error: err.Error()}
		} else {
			resp = Response{Success: true}
		}
	} else {
		resp = Response{Success: false, Error: "sync handler not set"}
	}

	return HandlerResult{Response: &resp}
}

func handleTeamSync(s *Server, _ Message, conn net.Conn) HandlerResult {
	s.mu.Lock()
	handler := s.onTeamSync
	s.mu.Unlock()

	var resp Response
	if handler != nil {
		pw := &ProgressWriter{conn: conn}
		if err := handler(pw); err != nil {
			resp = Response{Success: false, Error: err.Error()}
		} else {
			resp = Response{Success: true}
		}
	} else {
		resp = Response{Success: false, Error: "team sync handler not set"}
	}

	return HandlerResult{Response: &resp}
}

func handleStop(s *Server, _ Message, conn net.Conn) HandlerResult {
	s.mu.Lock()
	handler := s.onStop
	s.mu.Unlock()

	// send response before stopping
	resp := Response{Success: true}
	s.sendResponse(conn, resp)

	if handler != nil {
		handler()
	}

	return HandlerResult{SkipDefault: true}
}

// handleHeartbeat processes CLI heartbeat messages (fire-and-forget).
//
// Credential handling note: Heartbeat payloads may include credentials as an optimization
// for early refresh, but this is NOT a critical path. The daemon has its own credential
// management via refreshCredentialsIfNeeded() which uses gitserver.LoadCredentialsForEndpoint()
// and auth.GetTokenForEndpoint() to load fresh credentials from disk or refresh via API.
// If heartbeat credentials are lost (network issue, daemon busy), the daemon will refresh
// credentials normally on the next sync operation.
func handleHeartbeat(s *Server, msg Message, _ net.Conn) HandlerResult {
	s.mu.Lock()
	handler := s.onHeartbeat
	s.mu.Unlock()

	if handler != nil {
		handler(msg.Payload)
	}

	// fire-and-forget: no response needed, credential loss is acceptable
	return HandlerResult{SkipDefault: true}
}

func handleTelemetry(s *Server, msg Message, _ net.Conn) HandlerResult {
	s.mu.Lock()
	handler := s.onTelemetry
	s.mu.Unlock()

	if handler != nil {
		handler(msg.Payload)
	}

	// fire-and-forget: no response
	return HandlerResult{SkipDefault: true}
}

func handleFriction(s *Server, msg Message, _ net.Conn) HandlerResult {
	s.mu.Lock()
	handler := s.onFriction
	s.mu.Unlock()

	if handler != nil {
		var payload FrictionPayload
		if err := json.Unmarshal(msg.Payload, &payload); err != nil {
			s.logger.Debug("failed to parse friction payload", "error", err)
		} else {
			handler(payload)
		}
	}

	// fire-and-forget: no response
	return HandlerResult{SkipDefault: true}
}

func handleGetErrors(s *Server, _ Message, _ net.Conn) HandlerResult {
	s.mu.Lock()
	handler := s.onGetErrors
	s.mu.Unlock()

	if handler != nil {
		errs := handler()
		return HandlerResult{Response: marshalResponse(errs)}
	}
	return HandlerResult{
		Response: &Response{Success: true, Data: json.RawMessage(`[]`)},
	}
}

func handleMarkErrors(s *Server, msg Message, _ net.Conn) HandlerResult {
	s.mu.Lock()
	handler := s.onMarkErrors
	s.mu.Unlock()

	if handler != nil {
		var payload MarkErrorsPayload
		if err := json.Unmarshal(msg.Payload, &payload); err != nil {
			return HandlerResult{
				Response: &Response{Success: false, Error: fmt.Sprintf("invalid payload: %v", err)},
			}
		}
		handler(payload.IDs)
		return HandlerResult{
			Response: &Response{Success: true},
		}
	}
	return HandlerResult{
		Response: &Response{Success: false, Error: "mark errors handler not set"},
	}
}

func handleCheckout(s *Server, msg Message, conn net.Conn) HandlerResult {
	s.mu.Lock()
	handler := s.onCheckout
	s.mu.Unlock()

	if handler == nil {
		return HandlerResult{
			Response: &Response{Success: false, Error: "checkout handler not set"},
		}
	}

	var payload CheckoutPayload
	if err := json.Unmarshal(msg.Payload, &payload); err != nil {
		return HandlerResult{
			Response: &Response{Success: false, Error: fmt.Sprintf("invalid checkout payload: %v", err)},
		}
	}

	pw := &ProgressWriter{conn: conn}
	result, err := handler(payload, pw)
	if err != nil {
		return HandlerResult{
			Response: &Response{Success: false, Error: err.Error()},
		}
	}

	return HandlerResult{Response: marshalResponse(result)}
}

func handleSessions(s *Server, _ Message, _ net.Conn) HandlerResult {
	s.mu.Lock()
	handler := s.onSessions
	s.mu.Unlock()

	if handler != nil {
		sessions := handler()
		resp := SessionsResponse{Sessions: sessions}
		return HandlerResult{Response: marshalResponse(resp)}
	}
	return HandlerResult{
		Response: &Response{Success: true, Data: json.RawMessage(`{"sessions":[]}`)},
	}
}

func handleInstances(s *Server, _ Message, _ net.Conn) HandlerResult {
	s.mu.Lock()
	handler := s.onInstances
	s.mu.Unlock()

	if handler != nil {
		instances := handler()
		resp := InstancesResponse{Instances: instances}
		return HandlerResult{Response: marshalResponse(resp)}
	}
	return HandlerResult{
		Response: &Response{Success: true, Data: json.RawMessage(`{"instances":[]}`)},
	}
}

func handleDoctor(s *Server, _ Message, _ net.Conn) HandlerResult {
	s.mu.Lock()
	handler := s.onDoctor
	s.mu.Unlock()

	var resp *DoctorResponse
	if handler != nil {
		resp = handler()
	}
	if resp == nil {
		resp = &DoctorResponse{}
	}

	return HandlerResult{Response: marshalResponse(resp)}
}

// SetHandlers sets the message handlers.
func (s *Server) SetHandlers(onSync func() error, onStop func(), onStatus func() *StatusData) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onSync = onSync
	s.onStop = onStop
	s.onStatus = onStatus
}

// SetSyncHandler sets the sync handler with progress support.
// This supersedes the onSync callback set in SetHandlers.
func (s *Server) SetSyncHandler(cb func(progress *ProgressWriter) error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onSyncWithProgress = cb
}

// SetTeamSyncHandler sets the team context sync handler with progress support.
func (s *Server) SetTeamSyncHandler(cb func(progress *ProgressWriter) error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onTeamSync = cb
}

// SetActivityCallback sets the callback for activity tracking.
func (s *Server) SetActivityCallback(cb func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onActivity = cb
}

// SetHeartbeatHandler sets the handler for heartbeat messages.
func (s *Server) SetHeartbeatHandler(cb func(payload json.RawMessage)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onHeartbeat = cb
}

// SetCheckoutHandler sets the handler for checkout requests.
// The handler receives a ProgressWriter to send progress updates during long operations.
func (s *Server) SetCheckoutHandler(cb func(payload CheckoutPayload, progress *ProgressWriter) (*CheckoutResult, error)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onCheckout = cb
}

// SetTelemetryHandler sets the handler for telemetry messages.
// Telemetry is fire-and-forget - no response is sent.
func (s *Server) SetTelemetryHandler(cb func(payload json.RawMessage)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onTelemetry = cb
}

// SetFrictionHandler sets the handler for friction messages.
// Friction events are fire-and-forget - no response is sent.
func (s *Server) SetFrictionHandler(cb func(payload FrictionPayload)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onFriction = cb
}

// SetErrorsHandler sets the handler for retrieving unviewed errors.
func (s *Server) SetErrorsHandler(onGet func() []StoredError, onMark func(ids []string)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onGetErrors = onGet
	s.onMarkErrors = onMark
}

// SetSessionsHandler sets the handler for retrieving active agent sessions.
// Deprecated: Use SetInstancesHandler instead.
func (s *Server) SetSessionsHandler(cb func() []AgentSession) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onSessions = cb
}

// SetInstancesHandler sets the handler for retrieving active agent instances.
func (s *Server) SetInstancesHandler(cb func() []InstanceInfo) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onInstances = cb
}

// SetSyncHistoryHandler sets the sync history handler.
func (s *Server) SetSyncHistoryHandler(handler func() []SyncEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onSyncHistory = handler
}

// SetDoctorHandler sets the doctor (health check) handler.
func (s *Server) SetDoctorHandler(handler func() *DoctorResponse) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onDoctor = handler
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

	cleanupSocket(socketPath)
	return ctx.Err()
}

// handleConnection handles a single client connection.
func (s *Server) handleConnection(_ context.Context, conn net.Conn) {
	defer conn.Close()

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

	s.logger.Debug("received message", "type", msg.Type, "workspace_id", msg.WorkspaceID)

	// validate workspace ID if provided (warn on mismatch, still process for backward compatibility)
	if msg.WorkspaceID != "" && msg.WorkspaceID != CurrentWorkspaceID() {
		s.logger.Warn("workspace mismatch", "expected", CurrentWorkspaceID(), "got", msg.WorkspaceID)
	}

	// record activity on any IPC message
	s.mu.Lock()
	activityCb := s.onActivity
	s.mu.Unlock()
	if activityCb != nil {
		activityCb()
	}

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

// NewClient creates a new IPC client.
func NewClient() *Client {
	return &Client{
		socketPath: SocketPath(),
		// Localhost Unix socket IPC is <5ms in practice.
		// 50ms provides 10x headroom while enabling fast failure detection.
		timeout: 50 * time.Millisecond,
	}
}

// NewClientWithTimeout creates an IPC client with custom timeout.
func NewClientWithTimeout(timeout time.Duration) *Client {
	return &Client{
		socketPath: SocketPath(),
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
// timeouts, use NewClientWithTimeout(t).Ping() directly.
func IsHealthy() error {
	client := NewClientWithTimeout(100 * time.Millisecond)
	if err := client.Ping(); err != nil {
		// distinguish "no socket" from "socket exists but unresponsive"
		if _, statErr := os.Stat(SocketPath()); os.IsNotExist(statErr) {
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
// Uses a 30s timeout since syncs can take time.
func (c *Client) SyncWithProgress(onProgress ProgressCallback) error {
	conn, err := c.Connect()
	if err != nil {
		return err
	}
	defer conn.Close()

	// longer timeout for sync operations
	conn.SetDeadline(time.Now().Add(30 * time.Second))

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
// Uses a 60s timeout since syncing multiple teams can take time.
func (c *Client) TeamSyncWithProgress(onProgress ProgressCallback) error {
	conn, err := c.Connect()
	if err != nil {
		return err
	}
	defer conn.Close()

	// longer timeout for team sync operations
	conn.SetDeadline(time.Now().Add(60 * time.Second))

	msg := Message{
		Type:        MsgTypeTeamSync,
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

	// long timeout for clone operations
	conn.SetDeadline(time.Now().Add(60 * time.Second))

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

// TryConnectForCheckout attempts to connect for checkout operations.
// Uses a long timeout since clones can take time.
func TryConnectForCheckout() *Client {
	client := NewClientWithTimeout(60 * time.Second)
	if err := client.Ping(); err != nil {
		return nil
	}
	return client
}

// TryConnect attempts to connect to the daemon.
// Returns the client if connected, nil otherwise.
func TryConnect() *Client {
	client := NewClient()
	if err := client.Ping(); err != nil {
		return nil
	}
	return client
}

// TryConnectForSync attempts to connect for sync operations.
// Uses a longer timeout since syncs can take time.
func TryConnectForSync() *Client {
	client := NewClientWithTimeout(30 * time.Second)
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
