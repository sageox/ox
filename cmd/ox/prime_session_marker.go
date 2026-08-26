package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gofrs/flock"
	"github.com/sageox/agentx"
	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/internal/paths"
	"github.com/sageox/ox/internal/proc"
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
//   - Hook context: agent_hook.go reads markers to pass agent state to handlers
type SessionMarker struct {
	AgentID            string    `json:"agent_id"`
	SessionID          string    `json:"session_id,omitempty"`           // ox-generated agent-instance server session ID
	RecordingSessionID string    `json:"recording_session_id,omitempty"` // durable ses_ recording ID for resume linkage
	AgentSessionID     string    `json:"agent_session_id"`               // coding agent's native session identifier
	PrimedAt           time.Time `json:"primed_at"`                      // when session was primed
	ParentPID          int       `json:"parent_pid,omitempty"`           // parent agent process ID
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

// FindSessionMarkerByPID scans the session marker directory for a marker whose
// ParentPID matches the given agent-ancestor PID. Returns the first match, or
// nil if no marker references this process.
//
// This is the fallback for #527/#529 re-entry detection when agent_session_id
// is unavailable — e.g. a second prime invoked from a CLAUDE.md BLOCKING
// instruction has no hook stdin JSON, so the session-id-keyed lookup misses.
// Process identity is a reliable alternative key: the hook-driven prime wrote
// the marker with the agent's PID, and any later prime inside the same agent
// process can find it by walking up to that same PID.
func FindSessionMarkerByPID(agentPID int) *SessionMarker {
	if agentPID <= 0 {
		return nil
	}
	entries, err := os.ReadDir(SessionMarkerDir())
	if err != nil {
		return nil
	}
	for _, entry := range entries {
		// WriteSessionMarker emits atomic temp files as "<sid>.json.tmp"
		// and renames to "<sid>.json" — the .json suffix check here also
		// excludes the .tmp variants, which end in .tmp not .json.
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(SessionMarkerDir(), entry.Name()))
		if err != nil {
			continue
		}
		var m SessionMarker
		if err := json.Unmarshal(data, &m); err != nil {
			continue
		}
		if m.ParentPID != agentPID {
			continue
		}
		// belt-and-suspenders against PID recycling / stale markers:
		// require the recorded agent process to still be alive. Without
		// this, a marker from a crashed prior session whose PID happens
		// to match the current shell/agent would silently cross-link
		// identities — the exact class of bug we're fixing.
		if !proc.IsAlive(m.ParentPID) {
			continue
		}
		return &m
	}
	return nil
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
// Uses atomic write (temp file + fsync + rename + parent-dir fsync) via
// the shared fileutil helper for consistency with every other user-touching
// write path in this file.
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

	if err := fileutil.AtomicWriteBytes(path, data, 0644); err != nil {
		return fmt.Errorf("failed to write marker: %w", err)
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
//
// Semantics: surgical upsert of SageOx-owned keys only. Lines unrelated to
// the keys in `vars` are preserved verbatim — including unrelated exports,
// comments, blank lines, and shell-expanding values like
// `export PATH="$HOME/bin:$PATH"` that would break if reformatted through
// Go's %q. Only keys in `vars` are replaced; the first occurrence of each
// such key in the file is overwritten in place, later duplicates are
// removed (dedup), and any key not present is appended at the end.
//
// Why surgical: a second prime that claims a different agent_type would
// otherwise stack `export AGENT_ENV="pi"` after an earlier
// `export AGENT_ENV="claude-code"`, poisoning every subsequent subprocess
// until the next explicit prime (#527). Earlier revisions re-emitted every
// export line with %q, which could mangle complex shell syntax in unrelated
// entries — the CodeRabbit review flagged this as a privacy + correctness
// regression.
//
// Permissions: written with at most 0600. If the file already exists with a
// more restrictive mode (e.g. 0400), that stricter mode is preserved.
func WriteToAgentEnvFile(vars map[string]string) error {
	envFilePath := os.Getenv("CLAUDE_ENV_FILE")
	if envFilePath == "" {
		return nil // not in an agent context with env file support
	}

	// Concurrent primes (e.g. SessionStart hook racing against the
	// CLAUDE.md BLOCKING re-prime — exactly the #527/#529 scenario) both
	// read-modify-write this file. Without a lock, last-renamer-wins and
	// the other prime's keys silently disappear. Serialize via flock on
	// a sibling .lock file. 5s budget matches agentinstance.Store's
	// lockTimeout so behavior under contention is consistent across the
	// two file-locked subsystems that prime touches.
	lock := flock.New(envFilePath + ".lock")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	locked, err := lock.TryLockContext(ctx, 100*time.Millisecond)
	if err != nil {
		return fmt.Errorf("failed to acquire agent env file lock: %w", err)
	}
	if !locked {
		return fmt.Errorf("could not acquire agent env file lock within timeout")
	}
	defer func() { _ = lock.Unlock() }()

	// read existing content + mode (both may be absent)
	existing, err := os.ReadFile(envFilePath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to read agent env file: %w", err)
	}

	// Default to 0600 (owner read/write). If the file already exists with a
	// stricter mode, keep the stricter mode — we never loosen permissions
	// because the env file can carry values callers consider private.
	var perm os.FileMode = 0600
	if info, statErr := os.Stat(envFilePath); statErr == nil {
		if existing := info.Mode().Perm(); existing != 0 && existing < perm {
			perm = existing
		}
	}

	out := upsertEnvFile(string(existing), vars)

	// atomic write with fsync via the shared helper (mirrors every other
	// instruction-file/env-file write path and gets parent-dir fsync too).
	if err := fileutil.AtomicWriteBytes(envFilePath, []byte(out), perm); err != nil {
		return fmt.Errorf("failed to write agent env file: %w", err)
	}
	return nil
}

// upsertEnvFile rewrites `content` so that each key in `vars` has a single
// canonical `export KEY="VALUE"` line. Lines matching a key in `vars`
// are overwritten in place on first occurrence and removed on subsequent
// occurrences (dedup). All other lines — unrelated exports, comments,
// blanks, shell expansions — are preserved verbatim, never reformatted.
// Keys not already present are appended at the end in sorted order for
// deterministic output.
func upsertEnvFile(content string, vars map[string]string) string {
	written := make(map[string]bool, len(vars))

	var out strings.Builder
	lines := strings.Split(content, "\n")
	// preserve trailing-newline semantics: splitting "foo\n" yields ["foo",""].
	// The "" sentinel lets us re-emit a final newline only if the source had one.
	for i, line := range lines {
		key, isExport := parseExportKey(line)
		if isExport {
			if newVal, owned := vars[key]; owned {
				if !written[key] {
					fmt.Fprintf(&out, "export %s=%q", key, newVal)
					written[key] = true
					// preserve newline if not the final sentinel
					if i < len(lines)-1 {
						out.WriteByte('\n')
					}
				}
				// skip this line (either wrote replacement or deduping)
				continue
			}
		}
		out.WriteString(line)
		if i < len(lines)-1 {
			out.WriteByte('\n')
		}
	}

	// append any owned keys that weren't already present, in sorted order
	var missing []string
	for k := range vars {
		if !written[k] {
			missing = append(missing, k)
		}
	}
	sort.Strings(missing)
	for _, k := range missing {
		// ensure we start on a fresh line
		s := out.String()
		if len(s) > 0 && !strings.HasSuffix(s, "\n") {
			out.WriteByte('\n')
		}
		fmt.Fprintf(&out, "export %s=%q\n", k, vars[k])
	}

	return out.String()
}

// parseExportKey returns (key, true) when line is of the shape
// `export KEY=...`, otherwise ("", false). Only bare `export ` prefix
// (possibly after leading whitespace) counts — `declare -x`, `readonly`,
// etc. are left alone as foreign syntax.
func parseExportKey(line string) (string, bool) {
	trimmed := strings.TrimLeft(line, " \t")
	if !strings.HasPrefix(trimmed, "export ") {
		return "", false
	}
	rest := strings.TrimPrefix(trimmed, "export ")
	eq := strings.IndexByte(rest, '=')
	if eq <= 0 {
		return "", false
	}
	return rest[:eq], true
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
