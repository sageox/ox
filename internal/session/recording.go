package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var (
	// ErrNotRecording is returned when stop is called but no recording is active
	ErrNotRecording = errors.New("not currently recording")

	// ErrAlreadyRecording is returned when start is called but already recording
	ErrAlreadyRecording = errors.New("already recording a session")

	// ErrNoLedger is returned when session recording is attempted but no ledger is configured
	ErrNoLedger = errors.New("no ledger configured for this project")
)

const recordingFile = ".recording.json"

// RecordingState tracks an active recording session.
// Stored in sessions/<session-name>/.recording.json
type RecordingState struct {
	AgentID          string    `json:"agent_id"`
	StartedAt        time.Time `json:"started_at"`
	AdapterName      string    `json:"adapter_name"`
	SessionFile      string    `json:"session_file"` // source file from adapter (Claude Code JSONL)
	OutputFile       string    `json:"output_file"`  // output file being recorded
	SessionPath      string    `json:"session_path"` // path to session folder
	Title            string    `json:"title,omitempty"`
	EntryCount       int       `json:"entry_count"`
	LastReminderSeq  int       `json:"last_reminder_seq"`
	ReminderInterval int       `json:"reminder_interval"`
	FilterMode       string    `json:"filter_mode,omitempty"` // "infra" or "all" - controls event filtering
	WorkspacePath    string    `json:"workspace_path,omitempty"` // git root / project directory
	Branch           string    `json:"branch,omitempty"`         // git branch at recording start

	// Parent-child session tracking for subagent workflows
	// When a parent spawns subagents, each subagent can report its session
	// back to the parent session for aggregation.
	ParentSessionPath string `json:"parent_session_path,omitempty"` // path to parent's session folder
	ParentAgentID     string `json:"parent_agent_id,omitempty"`     // parent's agent ID (e.g., "Oxa7b3")

	AgentType      string `json:"agent_type,omitempty"`       // original agent type for metadata: "codex", "amp", etc. Falls back to AdapterName if empty.
	StopIncomplete bool   `json:"stop_incomplete,omitempty"`  // set when stop returned retry guidance (empty file)
	Model          string `json:"model,omitempty"`            // LLM model for generic adapters where ReadMetadata returns nil
}

// Duration returns how long the recording has been running.
func (r *RecordingState) Duration() time.Duration {
	if r == nil || r.StartedAt.IsZero() {
		return 0
	}
	return time.Since(r.StartedAt)
}

// IsSubagent returns true if this session has a parent session.
func (r *RecordingState) IsSubagent() bool {
	return r != nil && r.ParentSessionPath != ""
}

// recordingStatePath returns the path to .recording.json for the given session folder.
func recordingStatePath(sessionPath string) string {
	return filepath.Join(sessionPath, recordingFile)
}

// SaveRecordingState persists recording state to the session folder.
func SaveRecordingState(projectRoot string, state *RecordingState) error {
	if projectRoot == "" {
		return fmt.Errorf("%w: project root", ErrEmptyPath)
	}
	if state == nil {
		return ErrNilState
	}
	if state.SessionPath == "" {
		return fmt.Errorf("%w: session path", ErrEmptyPath)
	}

	// ensure session directory exists
	if err := os.MkdirAll(state.SessionPath, 0755); err != nil {
		return fmt.Errorf("create session dir=%s: %w", state.SessionPath, err)
	}

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal recording state: %w", err)
	}

	// TODO(server-side): move to server-side for MVP+1; client should not write to ledger directly.
	statePath := recordingStatePath(state.SessionPath)
	if err := os.WriteFile(statePath, data, 0600); err != nil {
		return fmt.Errorf("write recording state file=%s: %w", statePath, err)
	}

	return nil
}

// LoadRecordingState loads active recording state by searching for .recording.json
// in session folders under the sessions directory.
// Returns nil, nil if no recording state exists.
func LoadRecordingState(projectRoot string) (*RecordingState, error) {
	if projectRoot == "" {
		return nil, fmt.Errorf("%w: project root", ErrEmptyPath)
	}

	for _, sessionsDir := range sessionsSearchPaths(projectRoot) {
		entries, err := os.ReadDir(sessionsDir)
		if err != nil {
			continue // directory doesn't exist, try next
		}

		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}

			recordingPath := filepath.Join(sessionsDir, entry.Name(), recordingFile)
			data, err := os.ReadFile(recordingPath)
			if err != nil {
				continue // no .recording.json in this session folder
			}

			var state RecordingState
			if err := json.Unmarshal(data, &state); err != nil {
				continue // invalid JSON, skip
			}

			return &state, nil
		}
	}

	return nil, nil // no recording state found
}

// LoadAllRecordingStates returns all active recording states by searching for
// .recording.json in session folders. Unlike LoadRecordingState which returns
// only the first match, this returns all concurrent recordings (e.g., from
// multiple worktrees or agents).
func LoadAllRecordingStates(projectRoot string) ([]*RecordingState, error) {
	if projectRoot == "" {
		return nil, fmt.Errorf("%w: project root", ErrEmptyPath)
	}

	seen := make(map[string]struct{}) // deduplicate by canonical recording file path
	var states []*RecordingState

	for _, sessionsDir := range sessionsSearchPaths(projectRoot) {
		entries, err := os.ReadDir(sessionsDir)
		if err != nil {
			continue
		}

		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}

			recordingPath := filepath.Join(sessionsDir, entry.Name(), recordingFile)
			canonicalKey := recordingPath
			if resolved, err := filepath.EvalSymlinks(recordingPath); err == nil {
				canonicalKey = resolved
			}
			if _, ok := seen[canonicalKey]; ok {
				continue
			}

			data, err := os.ReadFile(recordingPath)
			if err != nil {
				continue
			}

			var state RecordingState
			if err := json.Unmarshal(data, &state); err != nil {
				continue
			}

			seen[canonicalKey] = struct{}{}
			states = append(states, &state)
		}
	}

	return states, nil
}

// LoadRecordingStateForAgent loads recording state for a specific agent.
// Returns nil, nil if no recording for that agent exists.
// Use this instead of LoadRecordingState when you have an agent ID to avoid
// accidentally operating on another concurrent agent's recording.
func LoadRecordingStateForAgent(projectRoot, agentID string) (*RecordingState, error) {
	if agentID == "" {
		return nil, fmt.Errorf("%w: agent ID", ErrEmptyPath)
	}
	states, err := LoadAllRecordingStates(projectRoot)
	if err != nil {
		return nil, err
	}
	for _, s := range states {
		if s.AgentID == agentID {
			return s, nil
		}
	}
	return nil, nil
}

// IsRecordingForAgent checks if a specific agent has an active recording.
func IsRecordingForAgent(projectRoot, agentID string) bool {
	state, _ := LoadRecordingStateForAgent(projectRoot, agentID)
	return state != nil
}

// ClearRecordingStateForAgent removes recording state for a specific agent only.
// Safe for concurrent use: only touches this agent's .recording.json.
func ClearRecordingStateForAgent(projectRoot, agentID string) error {
	state, err := LoadRecordingStateForAgent(projectRoot, agentID)
	if err != nil {
		return err
	}
	if state == nil {
		return nil // idempotent: nothing to clear
	}
	statePath := recordingStatePath(state.SessionPath)
	if err := os.Remove(statePath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove recording state file=%s: %w", statePath, err)
	}
	return nil
}

// ClearRecordingState removes the recording state file from the session folder.
// Note: returns first-match state. Use ClearRecordingStateForAgent in agent-context
// code to avoid clearing another concurrent agent's recording.
func ClearRecordingState(projectRoot string) error {
	if projectRoot == "" {
		return fmt.Errorf("%w: project root", ErrEmptyPath)
	}

	// load the state to find the session path
	state, err := LoadRecordingState(projectRoot)
	if err != nil {
		return fmt.Errorf("load recording state: %w", err)
	}
	if state == nil {
		return nil // no state to clear
	}

	// remove .recording.json from session folder
	if state.SessionPath != "" {
		statePath := recordingStatePath(state.SessionPath)
		if err := os.Remove(statePath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove recording state file=%s: %w", statePath, err)
		}

		// clean up any stale .lock files left by crashed session log processes
		lockFiles, _ := filepath.Glob(filepath.Join(state.SessionPath, "*.lock"))
		for _, lockFile := range lockFiles {
			_ = os.Remove(lockFile)
		}
	}

	return nil
}

// IsRecording checks if a recording is active for the given project root.
func IsRecording(projectRoot string) bool {
	state, err := LoadRecordingState(projectRoot)
	return err == nil && state != nil
}

const explicitStopMarker = ".session_stopped"

// MarkExplicitStop writes a breadcrumb indicating the user explicitly stopped
// recording. This prevents the next auto-start cycle (e.g. from /clear hook
// re-prime) from silently restarting the session.
func MarkExplicitStop(projectRoot string) error {
	if projectRoot == "" {
		return fmt.Errorf("%w: project root", ErrEmptyPath)
	}
	for _, dir := range sessionsSearchPaths(projectRoot) {
		if err := os.MkdirAll(dir, 0755); err != nil {
			continue
		}
		markerPath := filepath.Join(dir, explicitStopMarker)
		if err := os.WriteFile(markerPath, []byte(time.Now().Format(time.RFC3339)), 0600); err != nil {
			continue
		}
		return nil
	}
	return fmt.Errorf("could not write explicit stop marker for project=%s", projectRoot)
}

// ConsumeExplicitStop checks for and removes the explicit-stop marker.
// Returns true if the marker existed (meaning an auto-start should be skipped).
func ConsumeExplicitStop(projectRoot string) bool {
	if projectRoot == "" {
		return false
	}
	for _, dir := range sessionsSearchPaths(projectRoot) {
		markerPath := filepath.Join(dir, explicitStopMarker)
		if err := os.Remove(markerPath); err == nil {
			return true // marker existed and was removed
		}
	}
	return false
}

// sessionsSearchPaths returns the sessions directory paths to search
// (both project-local and XDG cache).
func sessionsSearchPaths(projectRoot string) []string {
	paths := []string{
		filepath.Join(projectRoot, "sessions"),
	}
	repoID := getRepoIDFromProject(projectRoot)
	if repoID != "" {
		contextPath := GetContextPath(repoID)
		if contextPath != "" {
			paths = append(paths, filepath.Join(contextPath, "sessions"))
		}
	}
	return paths
}

// GetRecordingDuration returns how long the current recording has been running.
// Returns 0 if no recording is active.
func GetRecordingDuration(projectRoot string) time.Duration {
	state, err := LoadRecordingState(projectRoot)
	if err != nil || state == nil {
		return 0
	}
	return time.Since(state.StartedAt)
}

// StartRecordingOptions contains options for starting a recording.
type StartRecordingOptions struct {
	AgentID          string
	AdapterName      string
	SessionFile      string // source file from adapter (Claude Code JSONL)
	OutputFile       string // output file being recorded
	Title            string
	Username         string // username for session folder name
	RepoContextPath  string // path to repo context directory (for storing sessions)
	ReminderInterval int    // defaults to DefaultReminderInterval if 0
	FilterMode       string // "infra" or "all" - controls event filtering on stop
	WorkspacePath    string // git root / project directory
	Branch           string // git branch at recording start

	// Parent session tracking for subagent workflows
	ParentSessionPath string // path to parent's session folder (optional)
	ParentAgentID     string // parent's agent ID (optional)

	AgentType string // original agent type for metadata (e.g., "codex", "amp")
	Model     string // LLM model for generic adapters
}

// StartRecording begins a new recording session.
// Returns ErrAlreadyRecording if a recording is already in progress.
func StartRecording(projectRoot string, opts StartRecordingOptions) (*RecordingState, error) {
	if projectRoot == "" {
		return nil, fmt.Errorf("%w: project root", ErrEmptyPath)
	}

	// check if THIS agent already has a recording; other agents' recordings are valid
	existing, err := LoadRecordingStateForAgent(projectRoot, opts.AgentID)
	if err != nil {
		return nil, fmt.Errorf("check recording state project=%s: %w", projectRoot, err)
	}
	if existing != nil {
		if existing.StopIncomplete {
			// previous stop returned retry but agent restarted — clear stale state
			slog.Info("clearing incomplete stop state", "agent_id", existing.AgentID)
			if err := ClearRecordingStateForAgent(projectRoot, opts.AgentID); err != nil {
				return nil, fmt.Errorf("clear incomplete recording state: %w", err)
			}
		} else {
			return nil, fmt.Errorf("%w: agent_id=%s started_at=%s", ErrAlreadyRecording, existing.AgentID, existing.StartedAt.Format(time.RFC3339))
		}
	}

	reminderInterval := opts.ReminderInterval
	if reminderInterval <= 0 {
		reminderInterval = DefaultReminderInterval
	}

	// determine username for session name
	username := opts.Username
	if username == "" {
		username = "user"
	}

	// generate session name
	sessionName := GenerateSessionName(opts.AgentID, username)

	// determine sessions base path
	var sessionsBasePath string
	if opts.RepoContextPath != "" {
		sessionsBasePath = filepath.Join(opts.RepoContextPath, "sessions")
	} else {
		// fallback to XDG cache location
		repoID := getRepoIDFromProject(projectRoot)
		if repoID != "" {
			contextPath := GetContextPath(repoID)
			if contextPath != "" {
				sessionsBasePath = filepath.Join(contextPath, "sessions")
			}
		}
	}

	if sessionsBasePath == "" {
		// sessions must go in the ledger or XDG cache, never inside the project
		return nil, ErrNoLedger
	}

	// validate session file is a regular file (not a directory) before creating dirs
	if opts.SessionFile != "" {
		info, err := os.Stat(opts.SessionFile)
		if err != nil {
			return nil, fmt.Errorf("session file not accessible: %w", err)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("session file is not a regular file: %s", opts.SessionFile)
		}
	}

	// create session folder path
	sessionPath := filepath.Join(sessionsBasePath, sessionName)

	// create the session directory
	if err := os.MkdirAll(sessionPath, 0755); err != nil {
		return nil, fmt.Errorf("create session dir=%s: %w", sessionPath, err)
	}

	// determine session file path (for backward compatibility)
	sessionFile := opts.SessionFile
	if sessionFile == "" {
		sessionFile = filepath.Join(sessionPath, "raw.jsonl")
	}

	state := &RecordingState{
		AgentID:           opts.AgentID,
		AdapterName:       opts.AdapterName,
		SessionFile:       opts.SessionFile,
		OutputFile:        sessionFile,
		SessionPath:       sessionPath,
		Title:             opts.Title,
		StartedAt:         time.Now(),
		EntryCount:        0,
		LastReminderSeq:   0,
		ReminderInterval:  reminderInterval,
		FilterMode:        opts.FilterMode,
		WorkspacePath:     opts.WorkspacePath,
		Branch:            opts.Branch,
		ParentSessionPath: opts.ParentSessionPath,
		ParentAgentID:     opts.ParentAgentID,
		AgentType:         opts.AgentType,
		Model:             opts.Model,
	}

	if err := SaveRecordingState(projectRoot, state); err != nil {
		return nil, err
	}

	return state, nil
}

// UpdateRecordingStateForAgent updates recording state for a specific agent.
// Safe for concurrent use: only touches this agent's .recording.json.
func UpdateRecordingStateForAgent(projectRoot, agentID string, updateFn func(*RecordingState)) error {
	state, err := LoadRecordingStateForAgent(projectRoot, agentID)
	if err != nil {
		return fmt.Errorf("load recording state: %w", err)
	}
	if state == nil {
		return ErrNotRecording
	}
	updateFn(state)
	if err := SaveRecordingState(projectRoot, state); err != nil {
		return fmt.Errorf("save recording state: %w", err)
	}
	return nil
}

// UpdateRecordingState updates and persists the recording state.
// Useful for updating entry count or last reminder sequence.
// Note: uses first-match state. Use UpdateRecordingStateForAgent in agent-context code.
func UpdateRecordingState(projectRoot string, updateFn func(*RecordingState)) error {
	state, err := LoadRecordingState(projectRoot)
	if err != nil {
		return fmt.Errorf("load recording state: %w", err)
	}
	if state == nil {
		return ErrNotRecording
	}

	updateFn(state)

	if err := SaveRecordingState(projectRoot, state); err != nil {
		return fmt.Errorf("save recording state: %w", err)
	}
	return nil
}

// StopRecording ends an active recording session for a specific agent.
// Returns the final state and ErrNotRecording if no recording is active for this agent.
func StopRecording(projectRoot, agentID string) (*RecordingState, error) {
	if projectRoot == "" {
		return nil, fmt.Errorf("%w: project root", ErrEmptyPath)
	}
	if agentID == "" {
		return nil, fmt.Errorf("%w: agent ID", ErrEmptyPath)
	}

	state, err := LoadRecordingStateForAgent(projectRoot, agentID)
	if err != nil {
		return nil, fmt.Errorf("load recording state project=%s agent=%s: %w", projectRoot, agentID, err)
	}
	if state == nil {
		return nil, fmt.Errorf("%w: project=%s agent=%s", ErrNotRecording, projectRoot, agentID)
	}

	if err := ClearRecordingStateForAgent(projectRoot, agentID); err != nil {
		return nil, fmt.Errorf("clear recording state project=%s: %w", projectRoot, err)
	}

	return state, nil
}

// GetSessionName extracts the session name from a session path.
func GetSessionName(sessionPath string) string {
	return filepath.Base(strings.TrimSuffix(sessionPath, "/"))
}

// FindParentSessionPathForAgent looks up the recording state for a specific agent
// and returns its session path. Used by subagents to discover parent session.
func FindParentSessionPathForAgent(projectRoot, agentID string) string {
	state, _ := LoadRecordingStateForAgent(projectRoot, agentID)
	if state == nil {
		return ""
	}
	return state.SessionPath
}

// FindParentSessionPath looks up the active recording state and returns its session path.
// Used by subagents to discover where to report completion.
// Returns empty string if no recording is active.
// Note: returns first-match. Use FindParentSessionPathForAgent when agent ID is known.
func FindParentSessionPath(projectRoot string) string {
	state, err := LoadRecordingState(projectRoot)
	if err != nil || state == nil {
		return ""
	}
	return state.SessionPath
}
