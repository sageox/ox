// Package session provides subagent session capture and aggregation.
//
// When a parent agent spawns subagents, the parent's session can capture
// subagent sessions and include them in the parent session's output.
// This enables comprehensive session capture for complex multi-agent workflows.
//
// Subagent capture flow:
//  1. Parent starts recording: `ox agent <id> session start`
//  2. Subagent spawns and inherits parent session ID (via environment or flag)
//  3. Subagent completes work and reports: `ox agent <id> session subagent-complete`
//  4. Parent stops recording: `ox agent <id> session stop`
//  5. Parent session includes references to all subagent sessions
package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// SubagentSession represents a completed subagent's session reference.
// These are stored in the parent session folder and aggregated on stop.
type SubagentSession struct {
	// SubagentID is the subagent's agent ID (e.g., "Oxa7b3")
	SubagentID string `json:"subagent_id"`

	// ParentSessionID is the parent's session ID
	ParentSessionID string `json:"parent_session_id"`

	// SessionPath is the path to the subagent's raw session
	SessionPath string `json:"session_path,omitempty"`

	// SessionName is the subagent's session folder name
	SessionName string `json:"session_name,omitempty"`

	// CompletedAt is when the subagent finished
	CompletedAt time.Time `json:"completed_at"`

	// EntryCount is the number of entries in the subagent session
	EntryCount int `json:"entry_count,omitempty"`

	// Summary is a brief description of what the subagent did
	Summary string `json:"summary,omitempty"`

	// DurationMs is how long the subagent ran in milliseconds
	DurationMs int64 `json:"duration_ms,omitempty"`

	// Model is the LLM model the subagent used
	Model string `json:"model,omitempty"`

	// AgentType is the coding agent type (claude-code, cursor, etc.)
	AgentType string `json:"agent_type,omitempty"`
}

// SubagentRegistry tracks subagent completions for a parent session.
// Thread-safe for concurrent subagent reporting.
type SubagentRegistry struct {
	mu           sync.RWMutex
	sessionPath  string // path to parent session folder
	registryPath string // path to subagents.jsonl file
}

const subagentsFilename = "subagents.jsonl"

// NewSubagentRegistry creates a registry for tracking subagent sessions.
// The sessionPath should be the parent session folder path.
func NewSubagentRegistry(sessionPath string) (*SubagentRegistry, error) {
	if sessionPath == "" {
		return nil, fmt.Errorf("%w: session path", ErrEmptyPath)
	}

	registryPath := filepath.Join(sessionPath, subagentsFilename)

	return &SubagentRegistry{
		sessionPath:  sessionPath,
		registryPath: registryPath,
	}, nil
}

// Register adds a completed subagent session to the registry.
// Thread-safe: multiple subagents can register concurrently.
func (r *SubagentRegistry) Register(session *SubagentSession) error {
	if session == nil {
		return fmt.Errorf("subagent session is nil")
	}
	if session.SubagentID == "" {
		return fmt.Errorf("subagent_id is required")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	// set completion time if not provided
	if session.CompletedAt.IsZero() {
		session.CompletedAt = time.Now()
	}

	// append to JSONL file
	f, err := os.OpenFile(r.registryPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return fmt.Errorf("open subagents registry file=%s: %w", r.registryPath, err)
	}
	defer f.Close()

	// Completion summaries are free-form session content, so metadata framing
	// cannot bypass the same mandatory guard used for recorded turns.
	if err := f.Chmod(0600); err != nil {
		return fmt.Errorf("secure subagent registry: %w", err)
	}
	writer, err := NewRawStreamWriter(f, "")
	if err != nil {
		return fmt.Errorf("subagent redaction policy: %w", err)
	}
	defer writer.Close()
	if err := writer.WriteValue(session); err != nil {
		return fmt.Errorf("encode subagent session: %w", err)
	}

	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync subagents registry: %w", err)
	}

	return nil
}

// List returns all registered subagent sessions.
func (r *SubagentRegistry) List() ([]*SubagentSession, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return r.listNoLock()
}

// listNoLock reads the registry without locking (caller must hold lock).
func (r *SubagentRegistry) listNoLock() ([]*SubagentSession, error) {
	f, err := os.Open(r.registryPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // no subagents registered
		}
		return nil, fmt.Errorf("open subagents registry file=%s: %w", r.registryPath, err)
	}
	defer f.Close()

	var sessions []*SubagentSession
	decoder := json.NewDecoder(f)

	for decoder.More() {
		var t SubagentSession
		if err := decoder.Decode(&t); err != nil {
			continue // skip invalid lines
		}
		sessions = append(sessions, &t)
	}

	return sessions, nil
}

// Count returns the number of registered subagent sessions.
func (r *SubagentRegistry) Count() (int, error) {
	sessions, err := r.List()
	if err != nil {
		return 0, err
	}
	return len(sessions), nil
}

// SubagentCompleteOptions configures subagent completion reporting.
type SubagentCompleteOptions struct {
	// SubagentID is the completing subagent's agent ID
	SubagentID string

	// ParentSessionPath is the path to the parent's session folder
	ParentSessionPath string

	// SessionPath is the path to the subagent's session (optional)
	SessionPath string

	// SessionName is the subagent's session folder name (optional)
	SessionName string

	// Summary describes what the subagent accomplished (optional)
	Summary string

	// DurationMs is how long the subagent ran (optional)
	DurationMs int64

	// EntryCount is the number of session entries (optional)
	EntryCount int

	// Model is the LLM model used (optional)
	Model string

	// AgentType is the coding agent type (optional)
	AgentType string
}

// ReportSubagentComplete registers a subagent's completion with the parent session.
// This should be called when a subagent finishes its work.
func ReportSubagentComplete(opts SubagentCompleteOptions) error {
	if opts.SubagentID == "" {
		return fmt.Errorf("subagent_id is required")
	}
	if opts.ParentSessionPath == "" {
		return fmt.Errorf("parent_session_path is required")
	}

	// verify parent session path exists
	if _, err := os.Stat(opts.ParentSessionPath); os.IsNotExist(err) {
		return fmt.Errorf("parent session path does not exist: %s", opts.ParentSessionPath)
	}

	registry, err := NewSubagentRegistry(opts.ParentSessionPath)
	if err != nil {
		return fmt.Errorf("create subagent registry: %w", err)
	}

	session := &SubagentSession{
		SubagentID:  opts.SubagentID,
		SessionPath: opts.SessionPath,
		SessionName: opts.SessionName,
		CompletedAt: time.Now(),
		EntryCount:  opts.EntryCount,
		Summary:     opts.Summary,
		DurationMs:  opts.DurationMs,
		Model:       opts.Model,
		AgentType:   opts.AgentType,
	}

	return registry.Register(session)
}

// GetSubagentSessions returns all subagent sessions for a session.
// The sessionPath should be the parent session folder path.
func GetSubagentSessions(sessionPath string) ([]*SubagentSession, error) {
	registry, err := NewSubagentRegistry(sessionPath)
	if err != nil {
		return nil, err
	}
	return registry.List()
}

// SubagentSummary provides aggregated statistics about subagent work.
type SubagentSummary struct {
	// Count is the number of subagents that completed
	Count int `json:"count"`

	// TotalEntries is the sum of all subagent session entries
	TotalEntries int `json:"total_entries"`

	// TotalDurationMs is the sum of all subagent durations
	TotalDurationMs int64 `json:"total_duration_ms"`

	// Subagents is the list of subagent references
	Subagents []*SubagentSession `json:"subagents,omitempty"`
}

// SummarizeSubagents creates an aggregated summary of subagent work.
func SummarizeSubagents(sessions []*SubagentSession) *SubagentSummary {
	summary := &SubagentSummary{
		Count:     len(sessions),
		Subagents: sessions,
	}

	for _, t := range sessions {
		summary.TotalEntries += t.EntryCount
		summary.TotalDurationMs += t.DurationMs
	}

	return summary
}
