package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sageox/ox/internal/paths"
	"github.com/sageox/ox/pkg/agentx"
)

// SessionMarkerDir returns the per-user directory for session markers.
//
// Markers are stored in /tmp/<user>/sageox/sessions/ intentionally — they are
// ephemeral and the OS cleans them on reboot. No explicit cleanup is needed.
// Stale markers from crashed or abandoned sessions are harmless.
//
// See paths.TempDir() for why /tmp/<user>/sageox/ instead of /tmp/sageox/.
func SessionMarkerDir() string {
	return filepath.Join(paths.TempDir(), "sessions")
}

// SessionMarker tracks a primed coding agent session.
//
// Created by `ox agent prime`, one marker per coding agent session (any agent,
// not just Claude Code). Keyed by the agent's native session identifier, which
// comes from hook stdin JSON (HookInput.SessionID) or an agent-specific env var
// (e.g., CLAUDE_CODE_SESSION_ID, CODEX_THREAD_ID, AMP_THREAD_URL).
//
// Purpose:
//   - Idempotency: re-priming the same session reuses the ox agent ID
//   - Notification throttling: LastNotified prevents spamming context-update alerts
//   - Hook context: agent_hook.go reads markers to pass agent state to handlers
type SessionMarker struct {
	AgentID        string    `json:"agent_id"`
	SessionID      string    `json:"session_id,omitempty"`       // ox-generated server session ID
	AgentSessionID string    `json:"agent_session_id"`           // coding agent's native session identifier
	PrimedAt       time.Time `json:"primed_at"`                  // when session was primed
	LastNotified   time.Time `json:"last_notified,omitempty"`    // mtime of last context file check
}

// AgentHookInput is an alias for agentx.HookInput.
// All coding agents that support hooks pipe session context via stdin JSON.
type AgentHookInput = agentx.HookInput

// markerPath returns the path to the marker file for a given agent session ID.
func markerPath(agentSessionID string) string {
	// sanitize session ID to prevent path traversal
	sanitized := strings.ReplaceAll(agentSessionID, "/", "_")
	sanitized = strings.ReplaceAll(sanitized, "\\", "_")
	sanitized = strings.ReplaceAll(sanitized, "..", "_")
	return filepath.Join(SessionMarkerDir(), sanitized+".json")
}

// ReadSessionMarker reads a session marker from disk.
// Returns nil, nil if the marker doesn't exist.
func ReadSessionMarker(agentSessionID string) (*SessionMarker, error) {
	if agentSessionID == "" {
		return nil, nil
	}

	path := markerPath(agentSessionID)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read marker: %w", err)
	}

	var marker SessionMarker
	if err := json.Unmarshal(data, &marker); err != nil {
		return nil, fmt.Errorf("failed to parse marker: %w", err)
	}

	// ensure AgentSessionID is set (may not be in old marker files)
	if marker.AgentSessionID == "" {
		marker.AgentSessionID = agentSessionID
	}

	return &marker, nil
}

// WriteSessionMarker writes a session marker to disk.
// Creates the marker directory if it doesn't exist.
// Uses atomic write pattern (temp file + rename) for safety.
func WriteSessionMarker(marker *SessionMarker) error {
	if marker.AgentSessionID == "" {
		return fmt.Errorf("agent session ID is required")
	}

	// ensure directory exists
	if err := os.MkdirAll(SessionMarkerDir(), 0700); err != nil {
		return fmt.Errorf("failed to create marker directory: %w", err)
	}

	path := markerPath(marker.AgentSessionID)

	data, err := json.MarshalIndent(marker, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal marker: %w", err)
	}

	// atomic write: temp file + rename
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write marker temp: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath) // clean up on failure
		return fmt.Errorf("failed to rename marker: %w", err)
	}

	return nil
}

// UpdateLastNotified updates the LastNotified field and writes to disk.
// On write failure, rolls back the in-memory value.
func (m *SessionMarker) UpdateLastNotified(t time.Time) error {
	oldValue := m.LastNotified
	m.LastNotified = t

	if err := WriteSessionMarker(m); err != nil {
		m.LastNotified = oldValue // rollback on failure
		return err
	}
	return nil
}

// DeleteSessionMarker removes a session marker from disk.
// Used for test cleanup only — production markers are ephemeral in /tmp
// and cleaned by the OS on reboot.
func DeleteSessionMarker(agentSessionID string) error {
	if agentSessionID == "" {
		return nil
	}
	path := markerPath(agentSessionID)
	err := os.Remove(path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// ReadAgentHookInput reads hook input from stdin.
// Delegates to agentx.ReadHookInputFromStdin and validates session_id is present.
// Works for any coding agent that pipes hook context via stdin JSON.
func ReadAgentHookInput() *AgentHookInput {
	input := agentx.ReadHookInputFromStdin()
	if input == nil {
		return nil
	}

	// validate we got a session_id (required for marker keying)
	if input.SessionID == "" {
		return nil
	}

	return input
}

// WriteToAgentEnvFile writes environment variables to the agent's env file if available.
// Currently supports CLAUDE_ENV_FILE (Claude Code). Other agents may use different
// mechanisms for injecting env vars into subsequent tool calls.
func WriteToAgentEnvFile(vars map[string]string) error {
	envFilePath := os.Getenv("CLAUDE_ENV_FILE")
	if envFilePath == "" {
		return nil // not in an agent context with env file support
	}

	file, err := os.OpenFile(envFilePath, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0644)
	if err != nil {
		return fmt.Errorf("failed to open agent env file: %w", err)
	}
	defer file.Close()

	for key, value := range vars {
		// write as export statements for bash sourcing
		fmt.Fprintf(file, "export %s=%q\n", key, value)
	}

	return nil
}

// IsAgentHookContext detects if we're running in a coding agent's hook context.
// Currently checks Claude Code env vars; extend for other agents as needed.
func IsAgentHookContext() bool {
	// check CLAUDE_PROJECT_DIR (set by Claude Code)
	if os.Getenv("CLAUDE_PROJECT_DIR") != "" {
		return true
	}

	// check CLAUDECODE env var
	if os.Getenv("CLAUDECODE") == "1" {
		return true
	}

	// check CLAUDE_CODE_ENTRYPOINT
	if os.Getenv("CLAUDE_CODE_ENTRYPOINT") != "" {
		return true
	}

	return false
}
