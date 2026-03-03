package session

import (
	"encoding/json"
	"errors"
	"fmt"
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

	// search for .recording.json in sessions directory structure
	// check both project-local and XDG cache locations
	sessionsPaths := []string{
		filepath.Join(projectRoot, "sessions"),
	}

	// also check XDG cache location if we can determine repo ID
	repoID := getRepoIDFromProject(projectRoot)
	if repoID != "" {
		contextPath := GetContextPath(repoID)
		if contextPath != "" {
			sessionsPaths = append(sessionsPaths, filepath.Join(contextPath, "sessions"))
		}
	}

	for _, sessionsDir := range sessionsPaths {
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

	sessionsPaths := []string{
		filepath.Join(projectRoot, "sessions"),
	}

	repoID := getRepoIDFromProject(projectRoot)
	if repoID != "" {
		contextPath := GetContextPath(repoID)
		if contextPath != "" {
			sessionsPaths = append(sessionsPaths, filepath.Join(contextPath, "sessions"))
		}
	}

	seen := make(map[string]struct{}) // deduplicate by canonical recording file path
	var states []*RecordingState

	for _, sessionsDir := range sessionsPaths {
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

// ClearRecordingState removes the recording state file from the session folder.
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
	}

	return nil
}

// IsRecording checks if a recording is active for the given project root.
func IsRecording(projectRoot string) bool {
	state, err := LoadRecordingState(projectRoot)
	return err == nil && state != nil
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
}

// StartRecording begins a new recording session.
// Returns ErrAlreadyRecording if a recording is already in progress.
func StartRecording(projectRoot string, opts StartRecordingOptions) (*RecordingState, error) {
	if projectRoot == "" {
		return nil, fmt.Errorf("%w: project root", ErrEmptyPath)
	}

	// check if already recording
	existing, err := LoadRecordingState(projectRoot)
	if err != nil {
		return nil, fmt.Errorf("check recording state project=%s: %w", projectRoot, err)
	}
	if existing != nil {
		if existing.AgentID == opts.AgentID {
			// same agent trying to start again — genuine conflict
			return nil, fmt.Errorf("%w: agent_id=%s started_at=%s", ErrAlreadyRecording, existing.AgentID, existing.StartedAt.Format(time.RFC3339))
		}
		// different agent — ghost session from a previous instance.
		// auto-clear so this session can start. the caller (agent_session.go)
		// should have already cleared it, but handle it here as defense-in-depth.
		if err := ClearRecordingState(projectRoot); err != nil {
			return nil, fmt.Errorf("clear ghost recording state project=%s: %w", projectRoot, err)
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
	}

	if err := SaveRecordingState(projectRoot, state); err != nil {
		return nil, err
	}

	return state, nil
}

// UpdateRecordingState updates and persists the recording state.
// Useful for updating entry count or last reminder sequence.
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

// StopRecording ends an active recording session.
// Returns the final state and ErrNotRecording if no recording is active.
func StopRecording(projectRoot string) (*RecordingState, error) {
	if projectRoot == "" {
		return nil, fmt.Errorf("%w: project root", ErrEmptyPath)
	}

	state, err := LoadRecordingState(projectRoot)
	if err != nil {
		return nil, fmt.Errorf("load recording state project=%s: %w", projectRoot, err)
	}
	if state == nil {
		return nil, fmt.Errorf("%w: project=%s", ErrNotRecording, projectRoot)
	}

	// clear the recording state file from session folder
	if err := ClearRecordingState(projectRoot); err != nil {
		return nil, fmt.Errorf("clear recording state project=%s: %w", projectRoot, err)
	}

	return state, nil
}

// GetSessionName extracts the session name from a session path.
func GetSessionName(sessionPath string) string {
	return filepath.Base(strings.TrimSuffix(sessionPath, "/"))
}

// FindParentSessionPath looks up the active recording state and returns its session path.
// Used by subagents to discover where to report completion.
// Returns empty string if no recording is active.
func FindParentSessionPath(projectRoot string) string {
	state, err := LoadRecordingState(projectRoot)
	if err != nil || state == nil {
		return ""
	}
	return state.SessionPath
}
