package telemetry

import "time"

// Event represents a telemetry event to be sent to the API
type Event struct {
	Type      string            `json:"type"`
	Timestamp time.Time         `json:"timestamp"`
	SessionID string            `json:"session_id,omitempty"`
	AgentID   string            `json:"agent_id,omitempty"`
	Command   string            `json:"command,omitempty"`
	Duration  int64             `json:"duration_ms,omitempty"`
	Success   bool              `json:"success"`
	ErrorCode string            `json:"error_code,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`

	// context fields for all events
	RepoID      string `json:"repo_id,omitempty"`      // repository ID from .sageox/config.json
	APIEndpoint string `json:"api_endpoint,omitempty"` // API endpoint used for this event

	// agent-specific fields for guidance tracking
	Path      string `json:"path,omitempty"`       // guidance path fetched
	AgentType string `json:"agent_type,omitempty"` // detected agent (from --agent flag, AGENT_ENV, or native detection)
	Model     string `json:"model,omitempty"`      // claude-3.5-sonnet, gpt-4, etc.
	OxSID     string `json:"oxsid,omitempty"`      // session ID from ox agent prime

	// session tracking fields
	PrimeCallCount int `json:"prime_call_count,omitempty"` // number of times prime was called in this session

	// coworker tracking fields
	CoworkerName  string `json:"coworker_name,omitempty"`  // name of coworker loaded
	CoworkerModel string `json:"coworker_model,omitempty"` // model used by coworker
}

// EventType constants for telemetry events
const (
	EventCommandStart    = "command_start"
	EventCommandComplete = "command_complete"
	EventCommandError    = "command_error"
	EventSessionStart    = "session_start"
	EventSessionEnd      = "session_end"
	EventGuidanceFetch   = "guidance_fetch"
	EventAuthSuccess     = "auth_success"
	EventAuthFailure     = "auth_failure"

	// agent-specific events
	EventGuidanceFetchError = "guidance_fetch_error" // auth, 404, rate limit errors
	EventAttributionShown   = "attribution_shown"    // attribution footer displayed
	EventPrimeExcessive     = "prime_excessive"      // prime called more than threshold times

	// daemon events (sent directly from CLI when daemon unavailable)
	EventDaemonStartFailure = "daemon:start_failure" // daemon failed to start

	// session latency events
	EventSessionPushSummary = "session_push_summary" // summary pushed to ledger (stop-to-visible latency)

	// coworker events
	EventCoworkerLoad = "coworker_load" // coworker/subagent loaded into context
)

// Batch represents a batch of events for efficient transmission
type Batch struct {
	Events    []Event   `json:"events"`
	CLIVer    string    `json:"cli_version"`
	OS        string    `json:"os"`
	Arch      string    `json:"arch"`
	Timestamp time.Time `json:"batch_timestamp"`
}

// Stats holds aggregated session statistics
type Stats struct {
	SessionID     string
	CommandCount  int
	ErrorCount    int
	TotalDuration time.Duration
	CommandCounts map[string]int
	FirstCommand  time.Time
	LastCommand   time.Time
}

// NewStats creates a new Stats instance
func NewStats(sessionID string) *Stats {
	return &Stats{
		SessionID:     sessionID,
		CommandCounts: make(map[string]int),
	}
}

// RecordCommand records a command execution
func (s *Stats) RecordCommand(command string, duration time.Duration, success bool) {
	s.CommandCount++
	s.TotalDuration += duration
	s.CommandCounts[command]++

	now := time.Now()
	if s.FirstCommand.IsZero() {
		s.FirstCommand = now
	}
	s.LastCommand = now

	if !success {
		s.ErrorCount++
	}
}
