package agentwork

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/gitutil"
	"github.com/sageox/ox/internal/ledger"
	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/paths"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/adapters"
	"github.com/sageox/ox/pkg/sessionsummary"
	"github.com/sageox/ox/pkg/summaryeval"
)

const (
	sessionFinalizeType     = "session-finalize"
	sessionFinalizePriority = 10

	artifactRaw       = "raw.jsonl"
	artifactSummaryMD = "summary.md"
	artifactSummJSON  = "summary.json"
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
var requiredArtifacts = []string{
	artifactSummaryMD,
	artifactSummJSON,
	artifactSessionMD,
}

// SessionFinalizePayload carries per-item context through the pipeline.
type SessionFinalizePayload struct {
	SessionDir string   `json:"session_dir"` // absolute path to session directory
	RawPath    string   `json:"raw_path"`    // path to raw.jsonl
	Missing    []string `json:"missing"`     // which artifacts need generation
	LedgerPath string   `json:"ledger_path"` // ledger repo root (for git operations)
	// UploadOnly skips LLM summarization. All artifacts already exist locally;
	// the session just needs to be staged in ledger/sessions/ and pushed.
	UploadOnly bool `json:"upload_only,omitempty"`

	// storedSession is populated by BuildPrompt and reused by ProcessResult
	// to avoid reading raw.jsonl twice.
	storedSession *session.StoredSession `json:"-"`
}

// SessionFinalizeHandler detects and finalizes incomplete sessions in the ledger.
// It generates missing artifacts: summary.md (via LLM), summary.json,
// and session.md (deterministic exports).
type SessionFinalizeHandler struct {
	logger *slog.Logger
	// skipGit disables git add/commit/push in tests
	skipGit bool
	// skipLFS disables LFS upload in tests
	skipLFS bool
	// projectRoot is the workspace root for endpoint/credential resolution
	projectRoot string
	// ledgerMu guards concurrent git operations on the ledger repo.
	// Shared with SyncScheduler to prevent index.lock races.
	ledgerMu *sync.Mutex
	// pidLookup returns the parent PID for a given agent ID from daemon in-memory state.
	// Used as fallback when .recording.json predates the ParentPID field (rollout compat).
	// Returns 0 if unknown.
	pidLookup func(agentID string) int
	// quality thresholds (configurable via AgentWorkerConfig)
	qualityUploadThreshold  float64
	qualityDiscardThreshold float64
	// judgeCompleter, when non-nil and OX_SUMMARY_JUDGE=on is set, runs
	// a post-summary LLM-as-judge evaluation. Nil disables judging.
	judgeCompleter summaryeval.Completer
	// daemonCtx is the daemon's root context. Derivative deadlines for
	// judge work and other long-running subtasks are layered on top so
	// daemon shutdown (Stop()) cancels them promptly instead of forcing
	// ErrShutdownTimeout. Defaults to context.Background() when unset,
	// which preserves behavior for tests that don't call SetDaemonContext.
	daemonCtx context.Context
}

// NewSessionFinalizeHandler creates a handler with the given logger.
func NewSessionFinalizeHandler(logger *slog.Logger) *SessionFinalizeHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &SessionFinalizeHandler{
		logger:                  logger,
		qualityUploadThreshold:  0.3,
		qualityDiscardThreshold: 0.1,
	}
}

// SetQualityThresholds configures the quality score thresholds for upload/discard.
func (h *SessionFinalizeHandler) SetQualityThresholds(upload, discard float64) {
	h.qualityUploadThreshold = upload
	h.qualityDiscardThreshold = discard
}

// SetPIDLookup sets the function used to look up agent PIDs from daemon in-memory state.
// This enables PID-based liveness detection for recordings that predate the ParentPID field.
func (h *SessionFinalizeHandler) SetPIDLookup(fn func(agentID string) int) {
	h.pidLookup = fn
}

// SetLedgerMu sets the shared ledger mutex for git operation serialization.
func (h *SessionFinalizeHandler) SetLedgerMu(mu *sync.Mutex) {
	h.ledgerMu = mu
}

// SetProjectRoot sets the workspace root for endpoint/credential resolution.
// Required for LFS upload during session finalization.
func (h *SessionFinalizeHandler) SetProjectRoot(root string) {
	h.projectRoot = root
}

// SetJudgeCompleter installs the optional LLM-as-judge completer. When
// non-nil AND OX_SUMMARY_JUDGE=on is set in the environment, the
// finalize handler runs a judgment pass after every successful summary
// and writes the verdict to <ledger>/.sageox/cache/summary-judge/.
//
// Nil disables judging entirely. The env var gate is intentional: the
// daemon may be configured with a judge completer in advance, but
// operators may not want to pay the LLM cost on every anti-entropy run
// until they flip the switch deliberately.
func (h *SessionFinalizeHandler) SetJudgeCompleter(c summaryeval.Completer) {
	h.judgeCompleter = c
}

// SetDaemonContext provides the daemon's root context. The handler uses
// it as the parent for any long-running subtasks (currently the judge
// call) so daemon shutdown can cancel them promptly. Without this, such
// tasks use context.Background() and block shutdown up to their own
// deadlines — triggering ErrShutdownTimeout that operators see as hangs.
func (h *SessionFinalizeHandler) SetDaemonContext(ctx context.Context) {
	h.daemonCtx = ctx
}

// NewSessionFinalizeHandlerForTest creates a handler with git and LFS operations disabled.
// Use in tests that don't have a real git repository or LFS server.
func NewSessionFinalizeHandlerForTest(logger *slog.Logger) *SessionFinalizeHandler {
	h := NewSessionFinalizeHandler(logger)
	h.skipGit = true
	h.skipLFS = true
	return h
}

// Type implements WorkHandler.
func (h *SessionFinalizeHandler) Type() string { return sessionFinalizeType }

// Cleanup performs lightweight filesystem housekeeping that doesn't require LLM
// processing: ghost session removal (dead PID + no data). Safe to run regardless
// of agent_worker.enabled. Does NOT clear .recording.json markers on sessions
// that have recoverable data — that's Detect()'s job when LLM processing follows.
//
// Scans three locations where sessions can live:
//   - ledgerPath/.sageox/cache/sessions/ — primary ledger cache (StartRecording writes here)
//   - ledgerPath/sessions/ — git-tracked sessions (after finalization/upload)
//   - XDG cache paths — legacy and alternate cache locations
func (h *SessionFinalizeHandler) Cleanup(ledgerPath string) {
	// clean primary ledger cache (where StartRecording writes sessions)
	ledgerCacheSessionsDir := filepath.Join(ledgerPath, ".sageox", "cache", "sessions")
	if ghostResult := session.CleanupGhostSessionsInDir(ledgerCacheSessionsDir); ghostResult.Removed > 0 {
		h.logger.Info("ghost cleanup (ledger cache): removed abandoned recordings", "count", ghostResult.Removed, "sessions", ghostResult.Names)
	}

	// clean git-tracked ledger sessions (uploaded/finalized sessions)
	ledgerSessionsDir := filepath.Join(ledgerPath, "sessions")
	if ghostResult := session.CleanupGhostSessionsInDir(ledgerSessionsDir); ghostResult.Removed > 0 {
		h.logger.Info("ghost cleanup (ledger): removed abandoned recordings", "count", ghostResult.Removed, "sessions", ghostResult.Names)
	}

	// clean XDG/legacy cache paths (different XDG_CACHE_HOME resolution or older ox versions)
	repoID := filepath.Base(ledgerPath)
	if repoID != "" && repoID != "." {
		cacheSessionsDir := filepath.Join(paths.SessionCacheDir(repoID), "sessions")
		if ghostResult := session.CleanupGhostSessionsInDir(cacheSessionsDir); ghostResult.Removed > 0 {
			h.logger.Info("ghost cleanup (cache): removed abandoned recordings", "count", ghostResult.Removed, "sessions", ghostResult.Names)
		}

		for _, altDir := range paths.AlternateSessionCacheDirs(repoID) {
			altSessionsDir := filepath.Join(altDir, "sessions")
			if ghostResult := session.CleanupGhostSessionsInDir(altSessionsDir); ghostResult.Removed > 0 {
				h.logger.Info("ghost cleanup (alternate cache): removed abandoned recordings", "dir", altDir, "count", ghostResult.Removed, "sessions", ghostResult.Names)
			}
		}
	}
}

// Detect scans all locations where sessions can live for sessions missing artifacts.
// Sessions are initially written to ledgerPath/.sageox/cache/sessions/ (primary),
// and moved to ledgerPath/sessions/ after finalization/upload. XDG cache paths
// are also scanned for sessions written by older ox versions or different environments.
func (h *SessionFinalizeHandler) Detect(ledgerPath string) ([]*WorkItem, error) {
	// deduplicate across all scan dirs (prefer earlier-found copy)
	seen := make(map[string]bool)
	var items []*WorkItem

	mergeItems := func(newItems []*WorkItem) {
		for _, item := range newItems {
			if !seen[item.DedupKey] {
				seen[item.DedupKey] = true
				items = append(items, item)
			}
		}
	}

	// 1. primary ledger cache — where StartRecording writes sessions
	ledgerCacheSessionsDir := filepath.Join(ledgerPath, ".sageox", "cache", "sessions")
	if ghostResult := session.CleanupGhostSessionsInDir(ledgerCacheSessionsDir); ghostResult.Removed > 0 {
		h.logger.Info("ghost cleanup (ledger cache): removed abandoned recordings", "count", ghostResult.Removed, "sessions", ghostResult.Names)
	}
	if cacheItems, err := h.detectInDir(ledgerCacheSessionsDir, ledgerPath); err == nil {
		mergeItems(cacheItems)
	}

	// 2. git-tracked ledger sessions — uploaded/finalized sessions
	ledgerSessionsDir := filepath.Join(ledgerPath, "sessions")
	if ghostResult := session.CleanupGhostSessionsInDir(ledgerSessionsDir); ghostResult.Removed > 0 {
		h.logger.Info("ghost cleanup (ledger): removed abandoned recordings", "count", ghostResult.Removed, "sessions", ghostResult.Names)
	}
	if ledgerItems, err := h.detectInDir(ledgerSessionsDir, ledgerPath); err == nil {
		mergeItems(ledgerItems)
	}

	// 3. XDG/legacy cache paths — older ox versions or different XDG_CACHE_HOME environments
	repoID := filepath.Base(ledgerPath)
	if repoID != "" && repoID != "." {
		xdgDirs := append(
			[]string{filepath.Join(paths.SessionCacheDir(repoID), "sessions")},
			func() []string {
				var out []string
				for _, d := range paths.AlternateSessionCacheDirs(repoID) {
					out = append(out, filepath.Join(d, "sessions"))
				}
				return out
			}()...,
		)
		for _, dir := range xdgDirs {
			if dir == ledgerCacheSessionsDir || dir == ledgerSessionsDir {
				continue
			}
			if ghostResult := session.CleanupGhostSessionsInDir(dir); ghostResult.Removed > 0 {
				h.logger.Info("ghost cleanup (xdg cache): removed abandoned recordings", "dir", dir, "count", ghostResult.Removed, "sessions", ghostResult.Names)
			}
			if xdgItems, err := h.detectInDir(dir, ledgerPath); err == nil {
				mergeItems(xdgItems)
			}
		}
	}

	h.logger.Info("session finalize detect complete", "ledger", ledgerPath, "items", len(items))
	return items, nil
}

// detectInDir scans a single sessions directory for sessions needing finalization.
func (h *SessionFinalizeHandler) detectInDir(sessionsDir, ledgerPath string) ([]*WorkItem, error) {
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
		// Without it there is nothing to summarize. For stale recordings
		// missing raw.jsonl, recovery is attempted from the adapter source file.
		hasRaw := false
		if _, statErr := os.Stat(rawPath); statErr == nil {
			// skip LFS stubs — they can't be finalized and would fail in BuildPrompt
			if lfs.IsPointerFile(rawPath) {
				h.logger.Debug("skipping LFS stub session", "session", name, "raw_path", rawPath)
				continue
			}
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
				// attempt recovery from adapter source file (#184)
				if recoverRawFromSessionFile(h.logger, recPath, sessionDir, rawPath) {
					hasRaw = true
				} else {
					h.logger.Warn("stale recording unrecoverable, clearing marker",
						"session", name,
						"recording_age", recAge.Round(time.Hour),
					)
					if rmErr := os.Remove(recPath); rmErr != nil {
						h.logger.Warn("failed to remove unrecoverable recording marker", "err", rmErr)
					}
					continue
				}
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

		// skip raw.jsonl files with zero substantive entries (header-only)
		if !session.HasSubstantiveEntries(rawPath) {
			h.logger.Debug("skipping header-only session", "session", name)
			continue
		}

		missing := missingArtifacts(sessionDir)
		if len(missing) == 0 {
			// All artifact files exist on disk. Check whether they are stubs
			// written by "ox session stop" that need LLM regeneration.
			if session.HasNeedsSummaryMarker(sessionDir) {
				h.logger.Info("session has stub artifacts, needs LLM summarization",
					"session", name,
				)
				items = append(items, &WorkItem{
					Type:     sessionFinalizeType,
					Priority: sessionFinalizePriority,
					DedupKey: sessionFinalizeType + ":" + name,
					Payload: &SessionFinalizePayload{
						SessionDir: sessionDir,
						RawPath:    rawPath,
						Missing:    requiredArtifacts, // regenerate all artifacts
						LedgerPath: ledgerPath,
					},
				})
				continue
			}

			// Truly finalized. If this session is in the ledger cache
			// but not yet in the git-tracked sessions/ dir, it needs to be pushed.
			if isInLedgerCacheDir(sessionDir, ledgerPath) {
				ledgerMeta := filepath.Join(ledgerPath, "sessions", name, "meta.json")
				if _, statErr := os.Stat(ledgerMeta); os.IsNotExist(statErr) {
					h.logger.Info("session needs upload (fully finalized, not pushed)",
						"session", name,
					)
					items = append(items, &WorkItem{
						Type:     sessionFinalizeType,
						Priority: sessionFinalizePriority,
						DedupKey: sessionFinalizeType + ":" + name,
						Payload: &SessionFinalizePayload{
							SessionDir: sessionDir,
							RawPath:    rawPath,
							LedgerPath: ledgerPath,
							UploadOnly: true,
						},
					})
				}
			}
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

	return items, nil
}

// DetectOrphanedForAgent scans for recordings belonging to a specific agent
// and returns work items for immediate finalization. Unlike Detect(), this
// skips the normal stale-recording threshold and immediately considers any
// recording for the given agent as orphaned.
//
// heartbeatPID is the PID from the heartbeat tracker. The function cross-checks
// it against the recording's ParentPID to avoid misclassifying live sessions
// whose heartbeat recorded a short-lived shell PID.
func (h *SessionFinalizeHandler) DetectOrphanedForAgent(ledgerPath, agentID string, heartbeatPID int) []*WorkItem {
	if agentID == "" || ledgerPath == "" {
		return nil
	}

	var items []*WorkItem
	seen := make(map[string]bool)

	scanDirs := []string{
		filepath.Join(ledgerPath, ".sageox", "cache", "sessions"),
		filepath.Join(ledgerPath, "sessions"),
	}

	// also scan XDG/legacy cache paths (mirrors Detect())
	repoID := filepath.Base(ledgerPath)
	if repoID != "" && repoID != "." {
		xdgDirs := []string{filepath.Join(paths.SessionCacheDir(repoID), "sessions")}
		for _, d := range paths.AlternateSessionCacheDirs(repoID) {
			xdgDirs = append(xdgDirs, filepath.Join(d, "sessions"))
		}
		for _, d := range xdgDirs {
			if d == scanDirs[0] || d == scanDirs[1] {
				continue // already covered
			}
			scanDirs = append(scanDirs, d)
		}
	}

	for _, sessionsDir := range scanDirs {
		entries, err := os.ReadDir(sessionsDir)
		if err != nil {
			continue
		}

		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}

			name := entry.Name()
			if name == "raw" || name == "events" {
				continue
			}

			sessionDir := filepath.Join(sessionsDir, name)
			recPath := filepath.Join(sessionDir, recordingMarker)

			// read .recording.json to check agent ID
			data, err := os.ReadFile(recPath)
			if err != nil {
				continue // no active recording
			}

			var state struct {
				AgentID   string     `json:"agent_id"`
				ParentPID int        `json:"parent_pid,omitempty"`
				StoppedAt *time.Time `json:"stopped_at,omitempty"`
			}
			if jsonErr := json.Unmarshal(data, &state); jsonErr != nil {
				continue
			}
			if state.AgentID != agentID {
				continue
			}

			// cross-check heartbeat PID against recording's ParentPID to avoid
			// misclassifying live sessions whose heartbeat recorded a short-lived shell PID
			if state.StoppedAt == nil {
				if state.ParentPID > 0 && state.ParentPID != heartbeatPID {
					if isPIDAlive(state.ParentPID) {
						h.logger.Debug("skipping live session (ParentPID alive, heartbeat PID mismatch)",
							"session", name, "agent_id", agentID,
							"parent_pid", state.ParentPID, "heartbeat_pid", heartbeatPID,
						)
						continue
					}
				} else if state.ParentPID == 0 && heartbeatPID > 0 {
					// rollout compat: old recordings missing parent_pid —
					// fall back to checking the heartbeat PID itself
					if isPIDAlive(heartbeatPID) {
						h.logger.Debug("skipping live session (parent_pid missing, heartbeat PID alive)",
							"session", name, "agent_id", agentID,
							"heartbeat_pid", heartbeatPID,
						)
						continue
					}
				}
			}

			// check if already fully finalized
			rawPath := filepath.Join(sessionDir, artifactRaw)
			hasRaw := false
			if _, statErr := os.Stat(rawPath); statErr == nil {
				hasRaw = true
			}

			if !hasRaw {
				// attempt recovery from adapter source file
				if recoverRawFromSessionFile(h.logger, recPath, sessionDir, rawPath) {
					hasRaw = true
				} else {
					h.logger.Warn("orphaned recording unrecoverable for agent",
						"session", name, "agent_id", agentID,
					)
					_ = os.Remove(recPath)
					continue
				}
			}

			// clear the recording marker so finalization can proceed
			h.logger.Info("clearing orphaned recording for agent exit finalization",
				"session", name, "agent_id", agentID,
			)
			if err := os.Remove(recPath); err != nil {
				h.logger.Warn("failed to remove orphaned recording marker", "session", name, "err", err)
				continue
			}

			if !session.HasSubstantiveEntries(rawPath) {
				continue
			}

			missing := missingArtifacts(sessionDir)
			if len(missing) == 0 {
				continue
			}

			dedupKey := sessionFinalizeType + ":" + name
			if seen[dedupKey] {
				continue
			}
			seen[dedupKey] = true

			items = append(items, &WorkItem{
				Type:     sessionFinalizeType,
				Priority: sessionFinalizePriority,
				DedupKey: dedupKey,
				Payload: &SessionFinalizePayload{
					SessionDir: sessionDir,
					RawPath:    rawPath,
					Missing:    missing,
					LedgerPath: ledgerPath,
				},
			})
		}
	}

	if len(items) > 0 {
		h.logger.Info("detected orphaned sessions for agent", "agent_id", agentID, "count", len(items))
	}
	return items
}

// BuildPrompt reads the raw session and constructs a summarization prompt.
// For UploadOnly items, returns a SkipLLM request — no prompt needed.
func (h *SessionFinalizeHandler) BuildPrompt(item *WorkItem) (RunRequest, error) {
	payload, err := extractPayload(item)
	if err != nil {
		return RunRequest{}, err
	}

	if payload.UploadOnly {
		return RunRequest{SkipLLM: true}, nil
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

	entries := sessionsummary.EntriesFromRaw(stored.Entries)
	prompt := sessionsummary.BuildSummaryPrompt(entries, payload.RawPath, payload.SessionDir)

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
// Content files are uploaded to LFS (when projectRoot is set) before git commit.
// LFS upload is best-effort: on failure, content is committed as regular git
// blobs as a fallback to avoid losing the session.
func (h *SessionFinalizeHandler) ProcessResult(item *WorkItem, result *RunResult) error {
	payload, err := extractPayload(item)
	if err != nil {
		return err
	}

	if payload.UploadOnly {
		return h.processUploadOnly(payload)
	}

	// Re-verify the precondition at processing time.
	//
	// The Detect() snapshot is taken on a periodic timer; ProcessResult runs
	// asynchronously, sometimes 30+ seconds later. In that window another
	// path (CLI `ox session regenerate`, `ox session push-summary`, or a
	// concurrent finalize) may have already produced the missing artifacts.
	// Without re-verification, the daemon races and overwrites a freshly-
	// produced good summary with whatever its LLM call returned (often a
	// permission-prompt narration that fails validation, persisting a
	// failure-marker stub on the ledger).
	//
	// Observed 2026-04-25 Phase 2: 31 of 71 freshly regen'd summaries got
	// clobbered ~30s after the regen commits. Filed as bd ox-91sl.
	sessionName := filepath.Base(payload.SessionDir)
	if h.workNoLongerNeeded(payload.SessionDir) {
		h.logger.Info("session no longer needs finalization, skipping (work landed via another path)",
			"session", sessionName,
		)
		return nil
	}

	llmOutput := result.Output

	// non-zero exit = summarization failed; don't trust the output at all
	if result.ExitCode != 0 {
		h.logger.Warn("summarization agent exited with error, discarding output",
			"session", filepath.Base(payload.SessionDir),
			"exit_code", result.ExitCode,
			"output_len", len(llmOutput),
		)
		return nil
	}

	// parse LLM output into SummarizeResponse. Track whether we produced a real
	// summary or a fallback stub so the quality gate only runs on real scores
	// — a fallback with QualityScore=0 must NOT flow through EvaluateQuality
	// and get discarded, otherwise transient LLM failures lose sessions (#525).
	var summaryResp *session.SummarizeResponse
	scored := true
	parsed, parseErr := sessionsummary.ParseSummaryJSON(llmOutput)
	if parseErr != nil {
		preview := llmOutput
		if len(preview) > 200 {
			preview = preview[:200]
		}
		h.logger.Warn("LLM output not parsable as summary JSON, using fallback stub",
			"err", parseErr,
			"output_len", len(llmOutput),
			"output_preview", preview,
		)
		summaryResp = &session.SummarizeResponse{
			Summary:      "Summary generation failed: LLM output was not valid JSON",
			QualityScore: 0.0,
			ScoreReason:  "unparsable LLM output",
		}
		scored = false
	} else {
		summaryResp = parsed
	}

	// validate summary content for agent meta-output contamination.
	// On failure we REPLACE the parsed response with a stub — not merely
	// flip a flag — because downstream code writes summary.json /
	// summary.md and uploads to the ledger. If we kept the original
	// invalid summary in summaryResp, its contaminated text would leak
	// onto the ledger as the teammate-visible artifact even though the
	// quality score reported it as failed.
	if valErr := sessionsummary.ValidateSummaryContent(summaryResp); valErr != nil {
		h.logger.Warn("summary content validation failed, using fallback stub",
			"session", filepath.Base(payload.SessionDir),
			"error", valErr,
		)
		summaryResp = &session.SummarizeResponse{
			Summary:      fmt.Sprintf("Summary failed content validation: %v", valErr),
			QualityScore: 0.0,
			ScoreReason:  fmt.Sprintf("content validation failed: %v", valErr),
		}
		scored = false
	}

	// Enforce richness schema on non-trivial sessions. The prompt asks for
	// key_actions / aha_moments; rejecting summaries that omit them is
	// essentially free — output tokens are negligible vs. the input tokens
	// already paid to ingest the session. Anti-entropy path mirrors the
	// CLI push-summary path (cmd/ox/session_push_summary.go).
	//
	// Same replace-don't-just-flag pattern as content validation above: a
	// thin summary whose title / key_actions are missing must not survive
	// on the ledger as the visible artifact for teammates.
	entryCount := 0
	if payload.storedSession != nil {
		entryCount = len(payload.storedSession.Entries)
	}
	if scored {
		if richErr := sessionsummary.ValidateSummaryRichness(summaryResp, entryCount); richErr != nil {
			h.logger.Warn("summary richness validation failed, replacing with fallback stub",
				"session", filepath.Base(payload.SessionDir),
				"entry_count", entryCount,
				"error", richErr,
			)
			summaryResp = &session.SummarizeResponse{
				Summary:      fmt.Sprintf("Summary failed richness validation: %v", richErr),
				QualityScore: 0.0,
				ScoreReason:  fmt.Sprintf("richness validation failed: %v", richErr),
			}
			scored = false
		}
	}

	// sessionName already computed above for the workNoLongerNeeded check.

	// Optional LLM-as-judge quality scoring. Runs only when
	// OX_SUMMARY_JUDGE=on is set AND a judge completer is configured on
	// the handler. Diagnostic-only: writes the judge's verdict to the
	// ledger cache at .sageox/cache/summary-judge/<session>.json and
	// logs the path. Failures are swallowed — never block finalization
	// on a judgment run.
	if scored {
		h.maybeRunJudge(sessionName, payload.LedgerPath, summaryResp)
	}

	// unscored fallbacks default to upload (preserve stub for teammates / doctor).
	// Only real LLM-scored summaries are gated by the quality thresholds.
	var disposition session.QualityDisposition
	if scored {
		disposition = session.EvaluateQuality(summaryResp.QualityScore, h.qualityUploadThreshold, h.qualityDiscardThreshold)
	} else {
		disposition = session.QualityUpload
	}

	if disposition == session.QualityDiscard {
		h.logger.Info("session below discard threshold, removing",
			"session", sessionName,
			"quality_score", summaryResp.QualityScore,
			"threshold", h.qualityDiscardThreshold,
			"reason", summaryResp.ScoreReason,
		)
		if err := os.RemoveAll(payload.SessionDir); err != nil {
			h.logger.Warn("failed to remove low-quality session", "session", sessionName, "err", err)
		}
		return nil
	}

	// use cached session from BuildPrompt, fall back to re-reading
	stored := payload.storedSession
	if stored == nil {
		var readErr error
		stored, readErr = session.ReadSessionFromPath(payload.RawPath)
		if readErr != nil {
			h.logger.Warn("could not read session for export", "err", readErr)
			return nil
		}
	}

	// generate all artifacts via shared path
	artifactPaths, artifactErr := session.WriteSessionArtifacts(payload.SessionDir, stored, summaryResp)
	if artifactErr != nil {
		h.logger.Warn("artifact generation failed", "err", artifactErr)
		h.logger.Warn("session recovery incomplete",
			"session", sessionName,
			"err", artifactErr,
		)
		return nil
	}

	// clear .needs-summary marker now that LLM artifacts have replaced stubs
	_ = session.ClearNeedsSummaryMarker(payload.SessionDir)

	h.logger.Info("wrote session artifacts",
		"summary_md", artifactPaths.SummaryMD,
		"summary_json", artifactPaths.SummaryJSON,
		"session_md", artifactPaths.SessionMD,
		"quality_score", summaryResp.QualityScore,
	)

	if disposition == session.QualityLocalOnly {
		h.logger.Info("session below upload threshold, keeping locally",
			"session", sessionName,
			"quality_score", summaryResp.QualityScore,
			"threshold", h.qualityUploadThreshold,
			"reason", summaryResp.ScoreReason,
		)
		return nil
	}

	// write meta.json and attempt LFS upload before git commit; returns file refs
	// so we can write pointer files only after a successful push
	fileRefs := h.writeMetaAndUploadLFS(payload, stored, summaryResp)

	// save original cache path before stageSessionInLedger may update payload.SessionDir
	origCacheDir := payload.SessionDir

	// stage in ledger/sessions/ if the session is still in the cache dir
	// (.sageox/cache/ is gitignored in the ledger, so we must copy first)
	if err := h.stageSessionInLedger(payload); err != nil {
		h.logger.Warn("failed to stage session in ledger, skipping commit", "session", sessionName, "err", err)
		return nil
	}

	// ensure sessions/.gitignore before commit
	sessionsDir := filepath.Dir(payload.SessionDir)
	if err := lfs.EnsureSessionsGitignore(sessionsDir); err != nil {
		h.logger.Warn("gitignore setup failed", "err", err)
	}

	pushed := h.gitCommitAndPush(payload)

	// push succeeded — now safe to replace content files with LFS pointer stubs
	if pushed && len(fileRefs) > 0 {
		if _, err := lfs.WritePointerFiles(payload.SessionDir, fileRefs); err != nil {
			h.logger.Warn("LFS pointer file write failed after push", "session", sessionName, "err", err)
		}
	}

	// prune cache dir only after a successful push — on push failure the cache
	// is the only surviving copy of the session content
	if pushed && origCacheDir != payload.SessionDir {
		if err := os.RemoveAll(origCacheDir); err != nil {
			h.logger.Debug("prune cache after finalize", "dir", origCacheDir, "err", err)
		}
	}

	h.logger.Info("session recovered via anti-entropy",
		"session", sessionName,
		"quality_score", summaryResp.QualityScore,
		"artifacts_generated", len(requiredArtifacts),
	)
	return nil
}

// writeMetaAndUploadLFS writes meta.json and attempts LFS upload for a finalized session.
// LFS upload is best-effort: on failure, content files remain as regular blobs.
// Returns the LFS file refs so the caller can write pointer files after a successful push.
func (h *SessionFinalizeHandler) writeMetaAndUploadLFS(payload *SessionFinalizePayload, stored *session.StoredSession, summaryResp *session.SummarizeResponse) map[string]lfs.FileRef {
	sessionName := filepath.Base(payload.SessionDir)

	// extract identity from raw.jsonl header
	var agentID, agentType, username string
	var createdAt time.Time
	if stored.Meta != nil {
		agentID = stored.Meta.AgentID
		agentType = stored.Meta.AgentType
		username = stored.Meta.Username
		createdAt = stored.Meta.CreatedAt
	}
	if agentID == "" {
		// parse from session name: YYYY-MM-DDTHH-MM-username-AgentID
		parts := strings.Split(sessionName, "-")
		if len(parts) >= 2 {
			agentID = parts[len(parts)-1]
		}
	}

	metaBuilder := lfs.NewSessionMeta(sessionName, username, agentID, agentType, createdAt).
		Title(summaryResp.Title).
		Summary(summaryResp.Summary).
		EntryCount(len(stored.Entries)).
		StopReason(session.StopReasonRecovered)

	// inject sageox contribution score from cache file (matches synchronous path),
	// then clean up to prevent stale scores leaking into future sessions
	if agentID != "" {
		if scoreFile, _ := session.ReadSageoxScore(agentID); scoreFile != nil {
			metaBuilder.SageoxScore(scoreFile.Score, string(scoreFile.Category), scoreFile.Reason)
		}
		_ = session.CleanupSageoxScore(agentID)
	}

	meta := metaBuilder.Build()

	// write meta.json (without LFS refs initially)
	if err := lfs.WriteSessionMetaOnly(payload.SessionDir, meta); err != nil {
		h.logger.Warn("meta.json write failed", "session", sessionName, "err", err)
		return nil
	}

	// attempt LFS upload (best-effort)
	if h.skipLFS || h.projectRoot == "" {
		return nil
	}

	ep := endpoint.GetForProject(h.projectRoot)
	client, err := lfs.NewClientFromLedger(payload.LedgerPath, ep)
	if err != nil {
		h.logger.Warn("LFS client creation failed, committing raw content as fallback", "session", sessionName, "err", err)
		return nil
	}

	fileRefs, err := lfs.UploadSessionFiles(client, payload.SessionDir, h.logger)
	if err != nil {
		h.logger.Warn("LFS upload failed, committing raw content as fallback", "session", sessionName, "err", err)
		return nil
	}

	// update meta.json with LFS file references; use WriteSessionMetaOnly so content
	// files remain intact until after the push (caller writes pointer files post-push)
	meta.Files = fileRefs
	if err := lfs.WriteSessionMetaOnly(payload.SessionDir, meta); err != nil {
		h.logger.Warn("meta.json update with LFS refs failed", "session", sessionName, "err", err)
		return nil
	}

	return fileRefs
}

// isInLedgerCacheDir reports whether sessionDir is inside the ledger's
// .sageox/cache/sessions/ directory. The cache is gitignored, so sessions
// there must be copied to ledger/sessions/ before they can be committed.
func isInLedgerCacheDir(sessionDir, ledgerPath string) bool {
	cacheDir := filepath.Join(ledgerPath, ".sageox", "cache", "sessions")
	return strings.HasPrefix(filepath.Clean(sessionDir)+string(filepath.Separator), filepath.Clean(cacheDir)+string(filepath.Separator))
}

// stageSessionInLedger copies session files from the ledger cache dir to the
// git-tracked ledger/sessions/<name>/ directory and updates payload.SessionDir.
// If the session is already in ledger/sessions/, this is a no-op.
func (h *SessionFinalizeHandler) stageSessionInLedger(payload *SessionFinalizePayload) error {
	if !isInLedgerCacheDir(payload.SessionDir, payload.LedgerPath) {
		return nil // already in ledger/sessions/ or another tracked path
	}

	sessionName := filepath.Base(payload.SessionDir)
	destDir := filepath.Join(payload.LedgerPath, "sessions", sessionName)

	if err := os.MkdirAll(destDir, 0755); err != nil {
		return fmt.Errorf("create ledger session dir: %w", err)
	}

	entries, err := os.ReadDir(payload.SessionDir)
	if err != nil {
		return fmt.Errorf("read cache session dir: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		src := filepath.Join(payload.SessionDir, entry.Name())
		dst := filepath.Join(destDir, entry.Name())
		if err := copySessionFile(src, dst); err != nil {
			return fmt.Errorf("copy %s: %w", entry.Name(), err)
		}
	}

	// update payload to point at the staged location
	payload.SessionDir = destDir
	return nil
}

// processUploadOnly handles sessions that are fully finalized in the cache
// but were never committed/pushed to the ledger. Skips LLM summarization.
func (h *SessionFinalizeHandler) processUploadOnly(payload *SessionFinalizePayload) error {
	sessionName := filepath.Base(payload.SessionDir)
	origCacheDir := payload.SessionDir

	// Before staging: if any content files are already LFS pointer stubs, verify
	// their backing blobs exist in the remote LFS store. Committing pointer stubs
	// whose blobs are absent from the remote poisons the push queue — GitLab's
	// pre-receive hook rejects the entire push for all sessions until fixed.
	if !h.skipLFS && h.projectRoot != "" {
		ep := endpoint.GetForProject(h.projectRoot)
		if client, err := lfs.NewClientFromLedger(payload.LedgerPath, ep); err == nil {
			if missing := lfs.FindPointerStubsWithMissingBlobs(client, payload.SessionDir, h.logger); len(missing) > 0 {
				h.logger.Warn("upload-only: pointer stubs reference LFS blobs not in remote — session cannot be pushed, skipping",
					"session", sessionName, "missing_files", missing)
				return nil // leave cache intact for manual recovery
			}
		}
	}

	// copy all artifacts from cache to ledger/sessions/<name>/
	if err := h.stageSessionInLedger(payload); err != nil {
		h.logger.Warn("upload-only: failed to stage session", "session", sessionName, "err", err)
		return nil
	}

	// LFS upload and meta.json update (best-effort — fallback to regular git blob)
	var fileRefs map[string]lfs.FileRef
	if !h.skipLFS && h.projectRoot != "" {
		ep := endpoint.GetForProject(h.projectRoot)
		client, err := lfs.NewClientFromLedger(payload.LedgerPath, ep)
		if err != nil {
			h.logger.Warn("upload-only: LFS client creation failed, committing raw content", "session", sessionName, "err", err)
		} else {
			refs, err := lfs.UploadSessionFiles(client, payload.SessionDir, h.logger)
			if err != nil {
				h.logger.Warn("upload-only: LFS upload failed, committing raw content", "session", sessionName, "err", err)
			} else {
				fileRefs = refs
				// update meta.json with LFS refs
				if len(fileRefs) > 0 {
					metaPath := filepath.Join(payload.SessionDir, "meta.json")
					if metaData, readErr := os.ReadFile(metaPath); readErr == nil {
						var meta lfs.SessionMeta
						if jsonErr := json.Unmarshal(metaData, &meta); jsonErr == nil {
							meta.Files = fileRefs
							if writeErr := lfs.WriteSessionMetaOnly(payload.SessionDir, &meta); writeErr != nil {
								h.logger.Warn("upload-only: meta.json LFS update failed", "session", sessionName, "err", writeErr)
							}
						}
					}
				}
			}
		}
	}

	sessionsDir := filepath.Dir(payload.SessionDir)
	if err := lfs.EnsureSessionsGitignore(sessionsDir); err != nil {
		h.logger.Warn("upload-only: gitignore setup failed", "err", err)
	}

	pushed := h.gitCommitAndPush(payload)

	// only write pointer stubs and prune cache after a successful push —
	// on failure the cache is the only surviving copy of the session content
	if pushed {
		if len(fileRefs) > 0 {
			if _, err := lfs.WritePointerFiles(payload.SessionDir, fileRefs); err != nil {
				h.logger.Warn("upload-only: LFS pointer write failed after push", "session", sessionName, "err", err)
			}
		}
		if err := os.RemoveAll(origCacheDir); err != nil {
			h.logger.Debug("upload-only: prune cache", "dir", origCacheDir, "err", err)
		}
		h.logger.Info("session uploaded via anti-entropy (upload-only)", "session", sessionName)
	}

	return nil
}

// gitCommitAndPush stages, commits, and pushes the finalized session.
// Returns true if the push succeeded, false otherwise.
// Push failures are non-fatal — callers should gate pointer-file writes and
// cache pruning on the return value to avoid data loss.
func (h *SessionFinalizeHandler) gitCommitAndPush(payload *SessionFinalizePayload) bool {
	if h.skipGit {
		return true // treat skip as success so tests can prune cache
	}

	// serialize with daemon's ledger git ops (sync, murmur push, github sync)
	if h.ledgerMu != nil {
		h.ledgerMu.Lock()
		defer h.ledgerMu.Unlock()
	}

	ledgerPath := payload.LedgerPath
	sessionName := filepath.Base(payload.SessionDir)

	// relative path from ledger root for git add
	relDir, err := filepath.Rel(ledgerPath, payload.SessionDir)
	if err != nil {
		h.logger.Warn("could not compute relative session path", "err", err)
		return false
	}

	// git add --sparse <session-dir>/
	if err := h.runGit(ledgerPath, "add", "--sparse", relDir+"/"); err != nil {
		h.logger.Warn("git add failed", "err", err)
		return false
	}

	// git commit
	msg := fmt.Sprintf("finalize session %s", sessionName)
	if err := h.runGit(ledgerPath, "commit", "-m", msg); err != nil {
		h.logger.Warn("git commit failed", "err", err)
		return false
	}

	// push with retry (best-effort — failures are non-fatal)
	ep := endpoint.GetForProject(h.projectRoot)
	if err := gitutil.PushWithRetry(context.Background(), ledgerPath, gitutil.PushOpts{
		AutoResolvePrefixes: ledger.AutoResolvePrefixes,
		Logger:              h.logger,
		ReconcileLFS: func(repoPath string) (bool, error) {
			if ep == "" {
				return false, nil
			}
			result, reconcileErr := lfs.ReconcileUnpushedPointers(
				context.Background(), repoPath, ep, h.logger)
			if reconcileErr != nil {
				return false, reconcileErr
			}
			return result.Replaced > 0, nil
		},
	}); err != nil {
		h.logger.Warn("git push failed (non-fatal)", "err", err)
		return false
	}
	return true
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

// copySessionFile copies src to dst, creating dst if it doesn't exist.
func copySessionFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

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

	// if we have a PID, check liveness — dead process = stale immediately,
	// live process = never stale (wait for next cycle)
	if pid > 0 {
		proc, procErr := os.FindProcess(pid)
		if procErr != nil || proc.Signal(syscall.Signal(0)) != nil {
			// grace period: young recordings with dead PIDs may have stored a
			// transient shell PID. Don't mark stale until grace period expires.
			if age < session.GhostGracePeriod {
				logger.Debug("recording process dead but within grace period, skipping", "pid", pid, "age", age)
				return false, age, "pid_dead_grace_period"
			}
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

// workNoLongerNeeded reports whether a session that was previously enqueued
// for finalization no longer needs the work — typically because a concurrent
// path (CLI regenerate / push-summary / a prior finalize) already wrote the
// artifacts. Re-checks the same predicates Detect() uses so the answers
// stay consistent.
//
// Returns true ONLY when ALL of the following hold:
//   - All required artifacts exist (no missing)
//   - No .needs-summary marker
//   - The on-disk summary.json has substantive content (not a failure-
//     marker stub from an earlier round we'd just overwrite again)
//
// The substantive-summary check uses internal/session.IsStubSummary's
// inverse: a daemon failure-marker stub has ScoreReason set, which
// IsStubSummary considers "not a stub" — but we want to RE-finalize those
// because they're known-bad. So we explicitly look for non-empty Title.
func (h *SessionFinalizeHandler) workNoLongerNeeded(sessionDir string) bool {
	if len(missingArtifacts(sessionDir)) > 0 {
		return false
	}
	if session.HasNeedsSummaryMarker(sessionDir) {
		return false
	}
	// Read summary.json; if it has a non-empty title, treat the work as
	// done. An empty-title summary indicates either a never-finalized
	// session (impossible at this point — missingArtifacts would have
	// caught it) or a daemon failure-marker stub that should be redone.
	data, err := os.ReadFile(filepath.Join(sessionDir, "summary.json"))
	if err != nil {
		return false
	}
	var head struct {
		Title string `json:"title"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		return false
	}
	return strings.TrimSpace(head.Title) != ""
}

// convertStoredEntries converts []map[string]any from StoredSession to []session.Entry.
func convertStoredEntries(stored []map[string]any) []session.Entry {
	// delegate to shared conversion, then map sessionsummary.Entry → session.Entry
	ssEntries := sessionsummary.EntriesFromRaw(stored)
	entries := make([]session.Entry, len(ssEntries))
	for i, e := range ssEntries {
		entries[i] = session.Entry{
			Timestamp:  e.Timestamp,
			Type:       session.EntryType(e.Type),
			Content:    e.Content,
			ToolName:   e.ToolName,
			ToolInput:  e.ToolInput,
			ToolOutput: e.ToolOutput,
		}
	}
	return entries
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

// recoverRawFromSessionFile attempts to generate raw.jsonl from the adapter's
// source file when a stale recording has no raw.jsonl. Handles the case where
// an agent crashed before any hooks fired but the source file still exists.
func recoverRawFromSessionFile(logger *slog.Logger, recPath, sessionDir, rawPath string) bool {
	// read .recording.json
	data, err := os.ReadFile(recPath)
	if err != nil {
		logger.Warn("recovery: cannot read recording state", "path", recPath, "err", err)
		return false
	}
	var state session.RecordingState
	if err := json.Unmarshal(data, &state); err != nil {
		logger.Warn("recovery: invalid recording state", "path", recPath, "err", err)
		return false
	}

	// check SessionFile exists and has content
	if state.SessionFile == "" {
		logger.Info("recovery: no session file path in recording state", "session_dir", sessionDir)
		return false
	}
	info, err := os.Stat(state.SessionFile)
	if err != nil || info.Size() == 0 {
		logger.Info("recovery: session file missing or empty",
			"session_file", state.SessionFile,
			"exists", err == nil,
		)
		return false
	}

	// get adapter
	adapter, err := adapters.GetAdapter(state.AdapterName)
	if err != nil {
		logger.Warn("recovery: unknown adapter", "adapter", state.AdapterName, "err", err)
		return false
	}

	// read all entries from source
	rawEntries, err := adapter.Read(state.SessionFile)
	if err != nil {
		logger.Warn("recovery: failed to read session file", "path", state.SessionFile, "err", err)
		return false
	}

	// filter entries by StartedAt
	var filtered []adapters.RawEntry
	for _, e := range rawEntries {
		if !e.Timestamp.IsZero() && e.Timestamp.Before(state.StartedAt) {
			continue
		}
		filtered = append(filtered, e)
	}

	if len(filtered) == 0 {
		logger.Info("recovery: no entries after session start time",
			"session_file", state.SessionFile,
			"started_at", state.StartedAt,
			"total_entries", len(rawEntries),
		)
		return false
	}

	// convert to session entries
	entries := session.ConvertRawEntries(filtered)

	// redact secrets
	projectRoot := state.WorkspacePath
	if projectRoot != "" {
		redactor, _ := session.NewRedactorWithCustomRules(projectRoot)
		if redactor != nil {
			redactor.RedactEntries(entries)
		}
	}

	// write to a temp file first so a partial write never leaves a corrupt
	// raw.jsonl that a future Detect cycle would treat as valid
	tmpPath := rawPath + ".tmp"
	f, err := os.Create(tmpPath)
	if err != nil {
		logger.Warn("recovery: cannot create temp file", "path", tmpPath, "err", err)
		return false
	}
	defer func() {
		f.Close()
		os.Remove(tmpPath) // no-op after successful rename
	}()

	enc := json.NewEncoder(f)

	// write header
	header := map[string]any{
		"_meta": map[string]any{
			"schema_version": "1",
			"agent_id":       state.AgentID,
			"agent_type":     state.AdapterName,
			"started_at":     state.StartedAt,
			"recovered":      true,
		},
	}
	if err := enc.Encode(header); err != nil {
		logger.Warn("recovery: failed to write header", "err", err)
		return false
	}

	for _, entry := range entries {
		if err := enc.Encode(entry); err != nil {
			logger.Warn("recovery: failed to write entry", "err", err)
			return false
		}
	}

	if err := f.Sync(); err != nil {
		logger.Warn("recovery: fsync failed", "err", err)
		return false
	}

	if err := f.Close(); err != nil {
		logger.Warn("recovery: close failed", "err", err)
		return false
	}

	if err := os.Rename(tmpPath, rawPath); err != nil {
		logger.Warn("recovery: atomic rename failed", "err", err)
		return false
	}

	logger.Info("recovery: raw.jsonl recovered from session file",
		"session_dir", sessionDir,
		"entries", len(entries),
		"source", state.SessionFile,
	)
	return true
}

// isPIDAlive checks if a process with the given PID exists.
func isPIDAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

// maybeRunJudge runs the LLM-as-judge scorer against a validated
// summary and writes the verdict to the ledger cache when enabled. See
// SetJudgeCompleter for the activation contract.
//
// Design rationale (nuanced bits worth preserving inline):
//
//   - Absolute mode (nil reference). The daemon doesn't have a curated
//     golden corpus to compare against; judging on-merits is what's
//     actionable in the anti-entropy context.
//
//   - Cache location under ledger/.sageox/cache/summary-judge/. Follows
//     the local-only-derived-data convention documented in
//     .claude/rules/ledger-cache.md. Deliberately outside the LFS
//     allowlist (internal/lfs.ContentFiles) so it never uploads.
//
//   - Failures never block finalization. A judge timeout or malformed
//     LLM response is diagnostic noise, not a correctness issue; swallow
//     with a warn-level log so operators see it but sessions still ship.
//
//   - Uses context.Background rather than inheriting a request-scoped
//     context. The judge is fire-and-forget diagnostic work after the
//     summary has already been validated; attaching it to a ctx that
//     might get canceled when the parent work item completes would
//     cause spurious cancellations.
func (h *SessionFinalizeHandler) maybeRunJudge(sessionName, ledgerPath string, summaryResp *session.SummarizeResponse) {
	if h.judgeCompleter == nil {
		return
	}
	if v := strings.ToLower(strings.TrimSpace(os.Getenv("OX_SUMMARY_JUDGE"))); v != "on" && v != "1" && v != "true" && v != "yes" {
		return
	}
	if summaryResp == nil {
		return
	}

	// Construct the eval-facing Summary shape from the richer
	// SummarizeResponse. summaryeval.Summary is intentionally narrower;
	// it only scores fields the rubric covers.
	cand := summaryeval.Summary{
		Title:       summaryResp.Title,
		Summary:     summaryResp.Summary,
		KeyActions:  summaryResp.KeyActions,
		Outcome:     summaryResp.Outcome,
		TopicsFound: summaryResp.TopicsFound,
	}
	for _, a := range summaryResp.AhaMoments {
		cand.AhaMoments = append(cand.AhaMoments, summaryeval.AhaMoment{
			Seq:  a.Seq,
			Type: a.Type,
		})
	}

	j := summaryeval.NewJudge(h.judgeCompleter, summaryeval.JudgeOptions{
		ModelHint:          "haiku",
		IncludeSuggestions: true,
	})

	// Use the daemon's root context as the parent so shutdown cancels
	// judge work promptly. Without this, context.Background would
	// block daemon shutdown up to 3 minutes and trigger
	// ErrShutdownTimeout. Falls back to Background when no daemon
	// context has been installed (e.g., in tests).
	parent := h.daemonCtx
	if parent == nil {
		parent = context.Background()
	}
	// 3-minute hard ceiling — the Runner adapter already caps, but a
	// context deadline is a second layer of defense against a runner
	// that ignores its own timeout.
	ctx, cancel := context.WithTimeout(parent, 3*time.Minute)
	defer cancel()

	result, err := j.Score(ctx, sessionName, nil, cand)
	if err != nil {
		h.logger.Warn("summary judge failed, continuing without verdict",
			"session", sessionName,
			"error", err,
		)
		return
	}

	// Write verdict to cache. Errors here are operator-visible but
	// don't affect the session finalize outcome.
	cacheDir := filepath.Join(ledgerPath, ".sageox", "cache", "summary-judge")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		h.logger.Warn("summary judge: mkdir cache failed",
			"session", sessionName,
			"path", cacheDir,
			"error", err,
		)
		return
	}
	cachePath := filepath.Join(cacheDir, sessionName+".json")
	body, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		h.logger.Warn("summary judge: marshal verdict failed",
			"session", sessionName,
			"error", err,
		)
		return
	}
	if err := os.WriteFile(cachePath, body, 0o644); err != nil {
		h.logger.Warn("summary judge: write verdict failed",
			"session", sessionName,
			"path", cachePath,
			"error", err,
		)
		return
	}

	// One-line key=value log so operators can grep for judge verdicts
	// and pull the cache path to read the rationale + suggestions.
	//
	// IMPORTANT: emit ONLY scalar, non-session-derived fields here.
	// result.Rationale and result.Suggestions are LLM-generated text that
	// can paraphrase session content — potentially including fragments
	// of secrets that escaped redaction, proprietary code, or user PII.
	// Those stay in the cache file (local, gitignored) where operators
	// who need the narrative can read them. The logs, which may flow to
	// aggregators / dashboards / remote observability, get only numeric
	// scores and model attribution.
	h.logger.Info("summary_judge",
		"session", sessionName,
		"cache_path", cachePath,
		"overall", result.Overall,
		"model", result.ModelUsed,
		"duration_ms", result.DurationMs,
		"prompt_tokens", result.PromptTokens,
		"completion_tokens", result.CompletionTokens,
		"suggestion_count", len(result.Suggestions),
	)
}
