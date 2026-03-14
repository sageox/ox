package agentwork

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/html"
)

const (
	sessionFinalizeType     = "session-finalize"
	sessionFinalizePriority = 10

	artifactRaw       = "raw.jsonl"
	artifactSummaryMD = "summary.md"
	artifactSummJSON  = "summary.json"
	artifactHTML      = "session.html"
	artifactSessionMD = "session.md"

	// recordingMarker is the file that indicates a session is still being recorded.
	// Sessions with this file present are NOT safe to finalize unless stale.
	recordingMarker = ".recording.json"

	// staleRecordingThreshold is how long a recording can sit with no progress
	// before the daemon considers it abandoned and eligible for anti-entropy
	// finalization. More conservative than the 12h health check warning.
	staleRecordingThreshold = 24 * time.Hour
)

// requiredArtifacts lists artifacts that should exist alongside raw.jsonl.
// TODO: session.html will be deprecated in favor of the web viewer at sageox.ai.
// When removed, drop artifactHTML from this list and remove generateHTML.
var requiredArtifacts = []string{
	artifactSummaryMD,
	artifactSummJSON,
	artifactHTML,
	artifactSessionMD,
}

// SessionFinalizePayload carries per-item context through the pipeline.
type SessionFinalizePayload struct {
	SessionDir string   `json:"session_dir"` // absolute path to session directory
	RawPath    string   `json:"raw_path"`    // path to raw.jsonl
	Missing    []string `json:"missing"`     // which artifacts need generation
	LedgerPath string   `json:"ledger_path"` // ledger repo root (for git operations)

	// storedSession is populated by BuildPrompt and reused by ProcessResult
	// to avoid reading raw.jsonl twice.
	storedSession *session.StoredSession `json:"-"`
}

// SessionFinalizeHandler detects and finalizes incomplete sessions in the ledger.
// It generates missing artifacts: summary.md (via LLM), summary.json,
// session.html, and session.md (deterministic exports).
type SessionFinalizeHandler struct {
	logger *slog.Logger
	// skipGit disables git add/commit/push in tests
	skipGit bool
	// pidLookup returns the parent PID for a given agent ID from daemon in-memory state.
	// Used as fallback when .recording.json predates the ParentPID field (rollout compat).
	// Returns 0 if unknown.
	pidLookup func(agentID string) int
}

// NewSessionFinalizeHandler creates a handler with the given logger.
func NewSessionFinalizeHandler(logger *slog.Logger) *SessionFinalizeHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &SessionFinalizeHandler{logger: logger}
}

// SetPIDLookup sets the function used to look up agent PIDs from daemon in-memory state.
// This enables PID-based liveness detection for recordings that predate the ParentPID field.
func (h *SessionFinalizeHandler) SetPIDLookup(fn func(agentID string) int) {
	h.pidLookup = fn
}

// NewSessionFinalizeHandlerForTest creates a handler with git operations disabled.
// Use in tests that don't have a real git repository.
func NewSessionFinalizeHandlerForTest(logger *slog.Logger) *SessionFinalizeHandler {
	h := NewSessionFinalizeHandler(logger)
	h.skipGit = true
	return h
}

// Type implements WorkHandler.
func (h *SessionFinalizeHandler) Type() string { return sessionFinalizeType }

// Detect scans <ledgerPath>/sessions/ for sessions missing artifacts.
func (h *SessionFinalizeHandler) Detect(ledgerPath string) ([]*WorkItem, error) {
	sessionsDir := filepath.Join(ledgerPath, "sessions")

	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read sessions dir: %w", err)
	}

	var items []*WorkItem
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		name := entry.Name()
		// skip legacy subdirs
		if name == "raw" || name == "events" {
			continue
		}

		sessionDir := filepath.Join(sessionsDir, name)
		rawPath := filepath.Join(sessionDir, artifactRaw)

		// raw.jsonl must exist and be committed/pushed to the ledger.
		// Without it there is nothing to summarize. Recovery of abandoned
		// recordings that never produced raw.jsonl is tracked in #184.
		hasRaw := false
		if _, statErr := os.Stat(rawPath); statErr == nil {
			hasRaw = true
		}

		// check for active recording
		recPath := filepath.Join(sessionDir, recordingMarker)
		if recInfo, statErr := os.Stat(recPath); statErr == nil {
			// .recording.json exists — check if it's stale (abandoned)
			stale, recAge, method := isStaleRecording(recPath, recInfo, h.pidLookup, h.logger)
			if !stale {
				h.logger.Debug("skipping session with active recording", "session", name)
				continue
			}
			if !hasRaw {
				// stale recording but no raw.jsonl — nothing we can do yet (#184)
				h.logger.Info("stale recording without raw.jsonl, skipping",
					"session", name,
					"recording_age", recAge.Round(time.Hour),
				)
				continue
			}
			// stale recording with raw.jsonl: clear the marker so we can finalize
			h.logger.Info("clearing stale recording for anti-entropy finalization",
				"session", name,
				"recording_age", recAge.Round(time.Hour),
				"detection_method", method,
			)
			if err := os.Remove(recPath); err != nil {
				h.logger.Warn("failed to remove stale recording marker", "session", name, "err", err)
				continue
			}
		}

		if !hasRaw {
			continue
		}

		missing := missingArtifacts(sessionDir)
		if len(missing) == 0 {
			continue
		}

		payload := &SessionFinalizePayload{
			SessionDir: sessionDir,
			RawPath:    rawPath,
			Missing:    missing,
			LedgerPath: ledgerPath,
		}

		h.logger.Info("session needs finalization",
			"session", name,
			"missing", missing,
		)

		items = append(items, &WorkItem{
			Type:     sessionFinalizeType,
			Priority: sessionFinalizePriority,
			DedupKey: sessionFinalizeType + ":" + name,
			Payload:  payload,
		})
	}

	h.logger.Info("session finalize detect complete", "ledger", ledgerPath, "items", len(items))
	return items, nil
}

// BuildPrompt reads the raw session and constructs a summarization prompt.
func (h *SessionFinalizeHandler) BuildPrompt(item *WorkItem) (RunRequest, error) {
	payload, err := extractPayload(item)
	if err != nil {
		return RunRequest{}, err
	}

	stored, err := session.ReadSessionFromPath(payload.RawPath)
	if err != nil {
		return RunRequest{}, fmt.Errorf("read session %s: %w", payload.RawPath, err)
	}

	// validate session data quality
	if warnings := validateStoredEntries(stored.Entries, h.logger); len(warnings) > 0 {
		h.logger.Warn("session data quality issues detected",
			"session", filepath.Base(payload.SessionDir),
			"issues", len(warnings),
		)
	}

	// cache for ProcessResult to avoid re-reading
	payload.storedSession = stored

	entries := convertStoredEntries(stored.Entries)
	prompt := session.BuildSummaryPrompt(entries, payload.RawPath, payload.SessionDir)

	return RunRequest{
		Prompt:  prompt,
		WorkDir: payload.LedgerPath,
	}, nil
}

// ProcessResult writes the LLM output and generates deterministic artifacts.
//
// NOTE: this method performs git add/commit/push from the daemon, which is an
// intentional exception to the Daemon-CLI Git Operations Split documented in
// CLAUDE.md. Session finalization writes to unique, timestamped session paths
// with near-zero conflict risk, and must run asynchronously (no CLI available).
// If this architecture is rejected, the alternative is an IPC endpoint that
// delegates writes back to the CLI.
//
// When triggered via async session upload (SAGEOX_ASYNC_SESSION_UPLOAD=1),
// content files are committed as regular files (not LFS pointers). This is
// acceptable for the initial rollout; LFS upload will be added to this handler
// as a follow-up to avoid committing large blobs to git.
func (h *SessionFinalizeHandler) ProcessResult(item *WorkItem, result *RunResult) error {
	payload, err := extractPayload(item)
	if err != nil {
		return err
	}

	llmOutput := result.Output

	// parse LLM output into SummarizeResponse
	var summaryResp *session.SummarizeResponse
	parsed, parseErr := parseSummaryJSON(llmOutput)
	if parseErr != nil {
		h.logger.Warn("could not parse summary JSON from LLM output, using raw text", "err", parseErr)
		// fall back to raw LLM output as summary text
		summaryResp = &session.SummarizeResponse{
			Summary: llmOutput,
		}
	} else {
		summaryResp = parsed
	}

	// use cached session from BuildPrompt, fall back to re-reading
	stored := payload.storedSession
	if stored == nil {
		var readErr error
		stored, readErr = session.ReadSessionFromPath(payload.RawPath)
		if readErr != nil {
			h.logger.Warn("could not read session for export", "err", readErr)
			h.gitCommitAndPush(payload)
			return nil
		}
	}

	// generate all artifacts via shared path
	htmlGen, htmlErr := html.NewGenerator()
	var gen session.HTMLGenerator
	if htmlErr == nil {
		gen = htmlGen
	}
	artifactPaths, artifactErr := session.WriteSessionArtifacts(payload.SessionDir, stored, summaryResp, gen)
	sessionName := filepath.Base(payload.SessionDir)
	if artifactErr != nil {
		h.logger.Warn("artifact generation failed", "err", artifactErr)
		h.logger.Warn("session recovery incomplete",
			"session", sessionName,
			"err", artifactErr,
		)
	} else {
		h.logger.Info("wrote session artifacts",
			"summary_md", artifactPaths.SummaryMD,
			"summary_json", artifactPaths.SummaryJSON,
			"html", artifactPaths.HTML,
			"session_md", artifactPaths.SessionMD,
		)
		h.gitCommitAndPush(payload)
		h.logger.Info("session recovered via anti-entropy",
			"session", sessionName,
			"artifacts_generated", len(requiredArtifacts),
		)
		return nil
	}

	h.gitCommitAndPush(payload)
	return nil
}

// gitCommitAndPush stages, commits, and pushes the finalized session.
// Push failures are non-fatal.
func (h *SessionFinalizeHandler) gitCommitAndPush(payload *SessionFinalizePayload) {
	if h.skipGit {
		return
	}

	ledgerPath := payload.LedgerPath
	sessionName := filepath.Base(payload.SessionDir)

	// relative path from ledger root for git add
	relDir, err := filepath.Rel(ledgerPath, payload.SessionDir)
	if err != nil {
		h.logger.Warn("could not compute relative session path", "err", err)
		return
	}

	// git add --sparse <session-dir>/
	if err := h.runGit(ledgerPath, "add", "--sparse", relDir+"/"); err != nil {
		h.logger.Warn("git add failed", "err", err)
		return
	}

	// git commit
	msg := fmt.Sprintf("finalize session %s", sessionName)
	if err := h.runGit(ledgerPath, "commit", "-m", msg); err != nil {
		h.logger.Warn("git commit failed", "err", err)
		return
	}

	// git push (best-effort)
	if err := h.runGit(ledgerPath, "push"); err != nil {
		h.logger.Warn("git push failed (non-fatal)", "err", err)
	}
}

// runGit executes a git command in the ledger directory.
func (h *SessionFinalizeHandler) runGit(repoPath string, args ...string) error {
	fullArgs := append([]string{"-C", repoPath}, args...)
	cmd := exec.Command("git", fullArgs...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")

	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git %s: %s: %w", args[0], strings.TrimSpace(string(out)), err)
	}
	return nil
}

// --- helpers ---

// extractPayload type-asserts the work item payload.
func extractPayload(item *WorkItem) (*SessionFinalizePayload, error) {
	if item.Payload == nil {
		return nil, fmt.Errorf("nil payload for item %s", item.ID)
	}
	p, ok := item.Payload.(*SessionFinalizePayload)
	if !ok {
		return nil, fmt.Errorf("unexpected payload type %T for item %s", item.Payload, item.ID)
	}
	return p, nil
}

// isStaleRecording reads a .recording.json file and determines if the recording
// is abandoned. Checks PID liveness first (instant detection), then falls back
// to the 24h staleRecordingThreshold.
//
// pidLookup is an optional function that returns the parent PID for a given agent ID
// from daemon in-memory state. Used as fallback when .recording.json predates the
// ParentPID field (rollout compat). Pass nil if unavailable.
func isStaleRecording(recPath string, info os.FileInfo, pidLookup func(string) int, logger *slog.Logger) (stale bool, age time.Duration, method string) {
	data, err := os.ReadFile(recPath)
	if err != nil {
		// can't read file — fall back to mod time
		age = time.Since(info.ModTime())
		if age > staleRecordingThreshold {
			return true, age, "time_threshold"
		}
		return false, age, "time_not_reached"
	}

	var state struct {
		StartedAt time.Time `json:"started_at"`
		AgentID   string    `json:"agent_id"`
		ParentPID int       `json:"parent_pid"`
	}
	if jsonErr := json.Unmarshal(data, &state); jsonErr != nil {
		age = time.Since(info.ModTime())
		if age > staleRecordingThreshold {
			return true, age, "time_threshold"
		}
		return false, age, "time_not_reached"
	}

	// determine age
	age = time.Since(info.ModTime())
	if !state.StartedAt.IsZero() {
		age = time.Since(state.StartedAt)
	}

	// try PID from .recording.json first, then fall back to daemon in-memory PID
	pid := state.ParentPID
	if pid > 0 {
		logger.Debug("recording PID from marker", "pid", pid, "session", filepath.Base(recPath))
	}
	if pid <= 0 && pidLookup != nil && state.AgentID != "" {
		pid = pidLookup(state.AgentID)
		if pid > 0 {
			logger.Debug("recording PID from daemon lookup", "pid", pid, "agent_id", state.AgentID)
		}
	}

	// if we have a PID, check liveness — dead process = stale immediately
	if pid > 0 {
		proc, procErr := os.FindProcess(pid)
		if procErr != nil || proc.Signal(syscall.Signal(0)) != nil {
			logger.Debug("recording process dead, marking stale", "pid", pid, "age", age)
			return true, age, "pid_dead"
		}
		logger.Debug("recording process alive, skipping", "pid", pid)
		return false, age, "pid_alive"
	}

	logger.Debug("no PID available, using time threshold", "session", filepath.Base(recPath))

	// fall back to time-based threshold
	if age > staleRecordingThreshold {
		logger.Debug("recording exceeded stale threshold", "age", age, "threshold", staleRecordingThreshold)
		return true, age, "time_threshold"
	}
	logger.Debug("recording within stale threshold", "age", age, "threshold", staleRecordingThreshold)
	return false, age, "time_not_reached"
}

// missingArtifacts returns the list of required artifacts not present in sessionDir.
func missingArtifacts(sessionDir string) []string {
	var missing []string
	for _, name := range requiredArtifacts {
		if _, err := os.Stat(filepath.Join(sessionDir, name)); os.IsNotExist(err) {
			missing = append(missing, name)
		}
	}
	return missing
}

// convertStoredEntries converts []map[string]any from StoredSession to []session.Entry.
func convertStoredEntries(stored []map[string]any) []session.Entry {
	entries := make([]session.Entry, 0, len(stored))
	for _, m := range stored {
		e := session.Entry{}
		if t, ok := m["type"].(string); ok {
			e.Type = session.EntryType(t)
		}
		if c, ok := m["content"].(string); ok {
			e.Content = c
		}
		if tn, ok := m["tool_name"].(string); ok {
			e.ToolName = tn
		}
		if ti, ok := m["tool_input"].(string); ok {
			e.ToolInput = ti
		}
		if ts, ok := m["timestamp"].(string); ok {
			if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
				e.Timestamp = t
			} else if t, err := time.Parse(time.RFC3339, ts); err == nil {
				e.Timestamp = t
			}
		}
		entries = append(entries, e)
	}
	return entries
}

// parseSummaryJSON attempts to extract a SummarizeResponse from the LLM output.
// The output may contain the JSON embedded in markdown code fences or as raw JSON.
func parseSummaryJSON(output string) (*session.SummarizeResponse, error) {
	// try raw JSON first
	var resp session.SummarizeResponse
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &resp); err == nil && resp.Title != "" {
		return &resp, nil
	}

	// try extracting from ```json ... ``` fences
	if idx := strings.Index(output, "```json"); idx >= 0 {
		start := idx + len("```json")
		if end := strings.Index(output[start:], "```"); end >= 0 {
			jsonStr := strings.TrimSpace(output[start : start+end])
			if err := json.Unmarshal([]byte(jsonStr), &resp); err == nil && resp.Title != "" {
				return &resp, nil
			}
		}
	}

	// try extracting from generic ``` ... ``` fences
	if idx := strings.Index(output, "```"); idx >= 0 {
		start := idx + len("```")
		// skip to newline if present (e.g., ```\n{...}\n```)
		if nlIdx := strings.Index(output[start:], "\n"); nlIdx >= 0 {
			start += nlIdx + 1
		}
		if end := strings.Index(output[start:], "```"); end >= 0 {
			jsonStr := strings.TrimSpace(output[start : start+end])
			if err := json.Unmarshal([]byte(jsonStr), &resp); err == nil && resp.Title != "" {
				return &resp, nil
			}
		}
	}

	return nil, fmt.Errorf("no valid summary JSON found in LLM output")
}

// invalidLeakedTypes are internal Claude Code types that should never appear
// in processed session data. Their presence indicates an adapter bug.
var invalidLeakedTypes = map[string]bool{
	"queue-operation":       true,
	"file-history-snapshot": true,
	"progress":              true,
	"summary":               true,
	"last-prompt":           true,
}

// validSessionEntryTypes are types the web viewer can display.
var validSessionEntryTypes = map[string]bool{
	"user":      true,
	"assistant": true,
	"system":    true,
	"tool":      true,
}

// validateStoredEntries checks stored session entries for data quality issues.
// Returns a list of warning strings. Logs each issue at appropriate level.
func validateStoredEntries(entries []map[string]any, logger *slog.Logger) []string {
	var warnings []string
	counts := make(map[string]int)

	for _, entry := range entries {
		entryType, _ := entry["type"].(string)
		counts[entryType]++

		if invalidLeakedTypes[entryType] {
			msg := fmt.Sprintf("internal type %q leaked into raw.jsonl (adapter bug)", entryType)
			warnings = append(warnings, msg)
			logger.Warn(msg, "type", entryType)
			if len(warnings) > 20 {
				break
			}
		}
	}

	if counts["user"] == 0 && len(entries) > 5 {
		msg := "no user messages in session — may be missing human prompts"
		warnings = append(warnings, msg)
		logger.Info(msg)
	}

	return warnings
}
