package session

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/sageox/ox/internal/config"
)

// CaptureResult contains the outcome of a capture-prior operation.
type CaptureResult struct {
	// Path where the history was stored
	Path string `json:"path"`

	// SessionName is the generated session folder name
	SessionName string `json:"session_name,omitempty"`

	// EntryCount is the number of entries captured
	EntryCount int `json:"entry_count"`

	// SecretsRedacted is the count of secrets found and redacted
	SecretsRedacted int `json:"secrets_redacted"`

	// TimeRange contains the time span of the captured history
	TimeRange *HistoryTimeRange `json:"time_range,omitempty"`

	// Title is the session title from metadata
	Title string `json:"title,omitempty"`

	// AgentID from the captured metadata
	AgentID string `json:"agent_id,omitempty"`
}

// CaptureOptions configures the capture-prior operation.
type CaptureOptions struct {
	// AgentID is the current agent's ID (used if not in history metadata)
	AgentID string

	// Title is an optional title for the captured session
	Title string

	// MergeWithActive if true, merges with any active recording
	MergeWithActive bool

	// SkipRedaction if true, skips secret redaction (for testing only)
	SkipRedaction bool
}

// CapturePrior reads, validates, redacts, and stores captured history from JSONL input.
//
// The input must be valid JSONL with:
//   - First line: {"_meta": {...}} with schema_version, source, agent_id
//   - Subsequent lines: entries with seq, type, content
//
// Returns the capture result with the storage path and statistics.
func CapturePrior(reader io.Reader, opts CaptureOptions) (*CaptureResult, error) {
	if reader == nil {
		return nil, ErrHistoryEmptyInput
	}

	// validate and parse input
	history, err := ValidateCapturePriorInput(reader)
	if err != nil {
		return nil, fmt.Errorf("validate input: %w", err)
	}

	// apply agent ID from options if not in metadata
	agentID := opts.AgentID
	if history.Meta != nil && history.Meta.AgentID != "" {
		agentID = history.Meta.AgentID
	}
	if agentID == "" {
		return nil, fmt.Errorf("agent_id required: provide in metadata or options")
	}

	// apply title from options if provided
	if opts.Title != "" && history.Meta != nil {
		history.Meta.SessionTitle = opts.Title
	}

	// redact secrets
	var redactedCount int
	if !opts.SkipRedaction {
		redactor := NewRedactor()
		redactedCount = redactor.RedactCapturedHistory(history)
	}

	// store the history
	storagePath, err := StoreCapturedHistory(history, agentID, opts.MergeWithActive)
	if err != nil {
		return nil, fmt.Errorf("store history: %w", err)
	}

	// build result
	result := &CaptureResult{
		Path:            storagePath,
		EntryCount:      len(history.Entries),
		SecretsRedacted: redactedCount,
		AgentID:         agentID,
	}

	if history.Meta != nil {
		result.TimeRange = history.Meta.TimeRange
		result.Title = history.Meta.SessionTitle
	}

	// extract session name from path
	dir := filepath.Dir(storagePath)
	result.SessionName = filepath.Base(dir)

	return result, nil
}

// CapturePriorFromFile reads captured history from a file.
func CapturePriorFromFile(path string, opts CaptureOptions) (*CaptureResult, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open file: %w", err)
	}
	defer f.Close()

	return CapturePrior(f, opts)
}

// CaptureOutput is the JSON output format for capture-prior command.
type CaptureOutput struct {
	Success         bool              `json:"success"`
	Type            string            `json:"type"` // "session_capture_prior"
	AgentID         string            `json:"agent_id"`
	Path            string            `json:"path"`
	SessionName     string            `json:"session_name,omitempty"`
	EntryCount      int               `json:"entry_count"`
	SecretsRedacted int               `json:"secrets_redacted,omitempty"`
	TimeRange       *HistoryTimeRange `json:"time_range,omitempty"`
	Title           string            `json:"title,omitempty"`
	Message         string            `json:"message,omitempty"`
}

// NewCaptureOutput creates a CaptureOutput from a CaptureResult.
func NewCaptureOutput(result *CaptureResult) *CaptureOutput {
	return &CaptureOutput{
		Success:         true,
		Type:            "session_capture_prior",
		AgentID:         result.AgentID,
		Path:            result.Path,
		SessionName:     result.SessionName,
		EntryCount:      result.EntryCount,
		SecretsRedacted: result.SecretsRedacted,
		TimeRange:       result.TimeRange,
		Title:           result.Title,
	}
}

// ToJSON marshals the output to indented JSON.
func (o *CaptureOutput) ToJSON() ([]byte, error) {
	return json.MarshalIndent(o, "", "  ")
}

// getRepoIDFromProject returns the repo ID from .sageox/config.json.
// Delegates to config.GetRepoID which is the canonical source of truth.
func getRepoIDFromProject(projectRoot string) string {
	return config.GetRepoID(projectRoot)
}

// getProjectEndpoint returns the endpoint from a project's .sageox/config.json.
// Returns empty string if the project has no explicit endpoint configured.
// Unlike endpoint.GetForProject, does NOT fall back to the default endpoint —
// this is intentional: ledger cache paths should only be used when the project
// has a real endpoint, not the global default.
func getProjectEndpoint(projectRoot string) string {
	cfg, err := config.LoadProjectConfig(projectRoot)
	if err != nil || cfg == nil {
		return ""
	}
	return cfg.Endpoint
}

// CreateCapturedHistoryMeta creates metadata for captured history with given parameters.
func CreateCapturedHistoryMeta(agentID, agentType, source, title string) *HistoryMeta {
	meta := NewHistoryMeta(agentID, source)
	meta.AgentType = agentType
	meta.SessionTitle = title
	meta.CapturedAt = time.Now()
	return meta
}
