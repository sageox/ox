package agentwork

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
}

// NewSessionFinalizeHandler creates a handler with the given logger.
func NewSessionFinalizeHandler(logger *slog.Logger) *SessionFinalizeHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &SessionFinalizeHandler{logger: logger}
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
			stale, recAge := isStaleRecording(recPath, recInfo)
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
func (h *SessionFinalizeHandler) ProcessResult(item *WorkItem, result *RunResult) error {
	payload, err := extractPayload(item)
	if err != nil {
		return err
	}

	sessionDir := payload.SessionDir
	llmOutput := result.Output

	// --- summary.md (always written first) ---
	summaryMDPath := filepath.Join(sessionDir, artifactSummaryMD)
	if err := os.WriteFile(summaryMDPath, []byte(llmOutput), 0644); err != nil {
		return fmt.Errorf("write summary.md: %w", err)
	}
	h.logger.Info("wrote summary.md", "path", summaryMDPath)

	// --- summary.json (best-effort parse of LLM JSON output) ---
	var summaryResp *session.SummarizeResponse
	parsed, parseErr := parseSummaryJSON(llmOutput)
	if parseErr != nil {
		h.logger.Warn("could not parse summary JSON from LLM output, skipping summary.json", "err", parseErr)
	} else {
		summaryResp = parsed
		summaryJSON, err := json.MarshalIndent(parsed, "", "  ")
		if err == nil {
			summaryJSONPath := filepath.Join(sessionDir, artifactSummJSON)
			if wErr := os.WriteFile(summaryJSONPath, summaryJSON, 0644); wErr != nil {
				h.logger.Warn("failed to write summary.json", "err", wErr)
			} else {
				h.logger.Info("wrote summary.json", "path", summaryJSONPath)
			}
		}
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

	// --- session.html ---
	htmlPath := filepath.Join(sessionDir, artifactHTML)
	if htmlErr := h.generateHTML(stored, summaryResp, htmlPath); htmlErr != nil {
		h.logger.Warn("html generation failed", "err", htmlErr)
	}

	// --- session.md ---
	mdPath := filepath.Join(sessionDir, artifactSessionMD)
	if mdErr := h.generateMarkdown(stored, mdPath); mdErr != nil {
		h.logger.Warn("markdown generation failed", "err", mdErr)
	}

	h.gitCommitAndPush(payload)
	return nil
}

// generateHTML creates session.html using the html generator.
func (h *SessionFinalizeHandler) generateHTML(stored *session.StoredSession, summary *session.SummarizeResponse, outputPath string) error {
	gen, err := html.NewGenerator()
	if err != nil {
		return fmt.Errorf("create html generator: %w", err)
	}

	if summary != nil {
		return gen.GenerateToFileWithSummary(stored, summary, outputPath)
	}
	return gen.GenerateToFile(stored, outputPath)
}

// generateMarkdown creates session.md using the markdown generator.
func (h *SessionFinalizeHandler) generateMarkdown(stored *session.StoredSession, outputPath string) error {
	gen := session.NewMarkdownGenerator()
	return gen.GenerateToFile(stored, outputPath)
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
// is abandoned (started more than staleRecordingThreshold ago). Falls back to
// file mod time if the JSON can't be parsed.
func isStaleRecording(recPath string, info os.FileInfo) (bool, time.Duration) {
	// try to read StartedAt from the recording state JSON
	data, err := os.ReadFile(recPath)
	if err == nil {
		var state struct {
			StartedAt time.Time `json:"started_at"`
		}
		if jsonErr := json.Unmarshal(data, &state); jsonErr == nil && !state.StartedAt.IsZero() {
			age := time.Since(state.StartedAt)
			return age > staleRecordingThreshold, age
		}
	}

	// fallback: use file modification time
	age := time.Since(info.ModTime())
	return age > staleRecordingThreshold, age
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
