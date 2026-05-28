package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/pkg/adapterprotocol"
)

// terminalMarkerFile is the write-ahead marker name. Lives inside the
// session dir alongside meta.json. Its presence at daemon start signals
// that a terminal-error finalize was in flight when the previous daemon
// process exited — RecoverPendingTerminalFinalize replays it.
const terminalMarkerFile = ".terminal_pending.json"

// terminalAuditFile is the daemon-wide append-only audit log of every
// terminal-error event that progressed past the precedence gate. One
// JSONL record per finalize attempt; never rotated by the daemon
// (operator's responsibility if it ever gets large).
const terminalAuditFile = "terminal_events.jsonl"

// terminalMaxDetail caps StopDetail length when it is persisted to
// meta.json or the audit log. Defense-in-depth against an adapter that
// somehow bypasses the wire-side MaxRawMessageBytes cap.
const terminalMaxDetail = 512

// auditTruncateLen caps the raw_message field copied into the audit log.
// Smaller than terminalMaxDetail because audit lines accumulate forever
// and one row per terminal event is plenty of forensic context.
const auditTruncateLen = 256

// EndSessionRPC is the supervisor surface the handler uses to ask the
// adapter to clean up. Pulled out as an interface so tests can stub it.
type EndSessionRPC interface {
	EndSession(adapterType, agentID string) error
}

// DefaultTerminalErrorHandler is the production TerminalErrorHandler.
// It resolves session paths through the daemon's recording-state
// lookup, stamps terminal-stop metadata into meta.json via the
// existing MutateSessionMeta flock-protected RMW, and best-effort
// sends EndSession to the adapter.
//
// Wire it via AdapterSupervisor.SetTerminalErrorHandler at daemon start.
type DefaultTerminalErrorHandler struct {
	ProjectRoot string
	LedgerPath  string
	Logger      *slog.Logger
	Supervisor  EndSessionRPC

	// auditMu serializes audit-log appends to avoid interleaved JSONL
	// lines when multiple agents finalize simultaneously. The file is
	// O_APPEND-opened (so writes are atomic up to PIPE_BUF) but we
	// don't want even single-line interleaving in operator-facing logs.
	auditMu sync.Mutex
}

// NewDefaultTerminalErrorHandler constructs a handler with the bare
// minimum wiring needed for production. Supervisor may be nil — in
// that case EndSession is skipped (handler still stamps meta and writes
// audit). Useful for tests and the orphan-recovery path on startup.
func NewDefaultTerminalErrorHandler(projectRoot, ledgerPath string, logger *slog.Logger, sup EndSessionRPC) *DefaultTerminalErrorHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &DefaultTerminalErrorHandler{
		ProjectRoot: projectRoot,
		LedgerPath:  ledgerPath,
		Logger:      logger,
		Supervisor:  sup,
	}
}

// HandleTerminalError is the TerminalErrorHandler implementation.
func (h *DefaultTerminalErrorHandler) HandleTerminalError(ctx context.Context, adapterType string, evt *adapterprotocol.Event) {
	if evt == nil {
		return
	}
	var data adapterprotocol.TerminalErrorData
	if err := json.Unmarshal(evt.Data, &data); err != nil {
		h.Logger.Warn("decode terminal_error data failed",
			"adapter", adapterType, "agent_id", evt.AgentID, "err", err)
		return
	}
	sessionPath, err := h.resolveSessionPath(evt.AgentID)
	if err != nil {
		h.Logger.Warn("terminal_error: cannot resolve session path",
			"adapter", adapterType, "agent_id", evt.AgentID, "err", err)
		return
	}
	if err := h.processTerminalEvent(ctx, adapterType, evt.AgentID, sessionPath, data, true); err != nil {
		h.Logger.Warn("terminal_error finalize failed",
			"adapter", adapterType, "agent_id", evt.AgentID, "path", sessionPath, "err", err)
	}
}

// resolveSessionPath looks up the recording state by agent ID and
// returns the on-disk session directory.
func (h *DefaultTerminalErrorHandler) resolveSessionPath(agentID string) (string, error) {
	if agentID == "" {
		return "", errors.New("empty agent_id")
	}
	if h.ProjectRoot == "" {
		return "", errors.New("handler has no project root configured")
	}
	state, err := session.LoadRecordingStateForAgent(h.ProjectRoot, agentID)
	if err != nil {
		return "", fmt.Errorf("load recording state: %w", err)
	}
	if state == nil || state.SessionPath == "" {
		return "", fmt.Errorf("recording state missing SessionPath for agent %q", agentID)
	}
	return state.SessionPath, nil
}

// processTerminalEvent runs the WAL + precedence-gated finalize for one
// terminal_error event. Shared between the live event path and the
// startup recovery sweep; writeMarker is false on recovery because the
// marker already exists on disk.
func (h *DefaultTerminalErrorHandler) processTerminalEvent(
	ctx context.Context,
	adapterType, agentID, sessionPath string,
	data adapterprotocol.TerminalErrorData,
	writeMarker bool,
) error {
	if writeMarker {
		if err := h.writeMarker(sessionPath, adapterType, data); err != nil {
			return fmt.Errorf("write marker: %w", err)
		}
	}
	markerPath := filepath.Join(sessionPath, terminalMarkerFile)

	// Stamp the terminal-stop metadata. The precedence gate runs INSIDE
	// the flocked read-modify-write so the "is this transition allowed?"
	// decision and the write are atomic — a concurrent user-stop can't
	// slip in between a separate pre-check and the write (TOCTOU). When
	// the transition is disallowed (user-stop already won) or there is no
	// meta.json to stamp, applied is false and we drop the marker so the
	// recovery sweep stops replaying it.
	detail := truncate(data.RawMessage, terminalMaxDetail)
	applied, existingReason, err := stampTerminalStop(ctx, sessionPath, data, detail)
	if err != nil {
		return fmt.Errorf("stamp meta: %w", err)
	}
	if !applied {
		h.Logger.Info("terminal_error skipped: precedence or missing meta",
			"adapter", adapterType, "agent_id", agentID,
			"existing_reason", existingReason, "incoming_reason", data.Reason)
		_ = os.Remove(markerPath)
		return nil
	}

	// Best-effort signal to the adapter that it can clean up. Adapter
	// may already be dead (the whole reason we're here) — log and move
	// on either way; the finalize stamp above is the load-bearing step.
	if h.Supervisor != nil {
		if err := h.Supervisor.EndSession(adapterType, agentID); err != nil {
			h.Logger.Debug("terminal_error EndSession best-effort RPC failed",
				"adapter", adapterType, "agent_id", agentID, "err", err)
		}
	}

	if err := os.Remove(markerPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		h.Logger.Warn("terminal_error: delete marker failed",
			"adapter", adapterType, "agent_id", agentID, "path", markerPath, "err", err)
	}

	h.writeAudit(ctx, adapterType, agentID, sessionPath, data, detail)
	return nil
}

func (h *DefaultTerminalErrorHandler) writeMarker(sessionPath, adapterType string, data adapterprotocol.TerminalErrorData) error {
	if err := os.MkdirAll(sessionPath, 0o755); err != nil {
		return err
	}
	rec := struct {
		AdapterType string                            `json:"adapter_type"`
		Data        adapterprotocol.TerminalErrorData `json:"data"`
		WrittenAt   time.Time                         `json:"written_at"`
	}{
		AdapterType: adapterType,
		Data:        data,
		WrittenAt:   time.Now().UTC(),
	}
	payload, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	return fileutil.AtomicWriteBytes(filepath.Join(sessionPath, terminalMarkerFile), payload, 0o644)
}

// stampTerminalStop mutates meta.json under the standard flock so the
// daemon and CLI never lose each other's writes, AND runs the stop-reason
// precedence gate inside the same locked section so the check and write
// are atomic. Returns:
//
//   - applied: true only when the metadata was actually written (meta
//     existed and the transition was allowed).
//   - existingReason: the StopReason found on disk (for logging when the
//     transition was blocked); "" when there was no meta.json.
//   - err: any error from the locked RMW.
func stampTerminalStop(ctx context.Context, sessionPath string, data adapterprotocol.TerminalErrorData, detail string) (applied bool, existingReason string, err error) {
	err = lfs.MutateSessionMeta(ctx, sessionPath, func(meta *lfs.SessionMeta) (*lfs.SessionMeta, error) {
		if meta == nil {
			// No meta.json yet — refuse to invent one from scratch in
			// the terminal path. The session was never committed and
			// the user probably already cleaned it up; the marker
			// being deleted by the caller is the right outcome.
			return nil, nil
		}
		existingReason = meta.StopReason
		// Atomic precedence gate: a user-initiated stop committed between
		// the caller's decision to finalize and this locked section still
		// wins, because we re-check here under the lock.
		if !session.CanTransitionStopReason(meta.StopReason, data.Reason) {
			return nil, nil // no write — caller treats applied=false as "skipped"
		}
		meta.StopReason = data.Reason
		meta.StopSource = data.Source
		meta.StopPatternID = data.PatternID
		meta.StopDetail = detail
		meta.StopResetsAtRaw = data.ResetsAtRaw
		meta.StopResetsAt = data.ResetsAt
		applied = true
		return meta, nil
	})
	return applied, existingReason, err
}

// writeAudit appends a single JSONL row to <ledger>/.sageox/audit/terminal_events.jsonl.
// Best-effort: logs on failure but never fails the finalize call.
func (h *DefaultTerminalErrorHandler) writeAudit(
	ctx context.Context,
	adapterType, agentID, sessionPath string,
	data adapterprotocol.TerminalErrorData,
	detail string,
) {
	if h.LedgerPath == "" {
		return
	}
	auditDir := filepath.Join(h.LedgerPath, ".sageox", "audit")
	if err := os.MkdirAll(auditDir, 0o755); err != nil {
		h.Logger.Warn("terminal_error audit: mkdir failed", "err", err)
		return
	}
	auditPath := filepath.Join(auditDir, terminalAuditFile)
	record := map[string]any{
		"timestamp":    time.Now().UTC().Format(time.RFC3339Nano),
		"session_path": sessionPath,
		"agent_id":     agentID,
		"adapter_type": adapterType,
		"pattern_id":   data.PatternID,
		"source":       data.Source,
		"reason":       data.Reason,
		"raw_message":  truncate(data.RawMessage, auditTruncateLen),
		"detected_at":  data.DetectedAt.UTC().Format(time.RFC3339Nano),
		"confirmed_at": data.ConfirmedAt.UTC().Format(time.RFC3339Nano),
	}
	if data.ResetsAt != nil {
		record["resets_at"] = data.ResetsAt.UTC().Format(time.RFC3339Nano)
	}
	if data.ResetsAtRaw != "" {
		record["resets_at_raw"] = data.ResetsAtRaw
	}
	line, err := json.Marshal(record)
	if err != nil {
		h.Logger.Warn("terminal_error audit: marshal failed", "err", err)
		return
	}
	line = append(line, '\n')

	h.auditMu.Lock()
	defer h.auditMu.Unlock()
	f, err := os.OpenFile(auditPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		h.Logger.Warn("terminal_error audit: open failed", "err", err)
		return
	}
	defer f.Close()
	if _, err := f.Write(line); err != nil {
		h.Logger.Warn("terminal_error audit: write failed", "err", err)
	}
}

// RecoverPendingTerminalFinalize re-runs finalize for every session
// that has a `.terminal_pending.json` marker but no committed StopReason
// at the marker's level. Idempotent: deletes markers whose underlying
// session has already advanced past their reason.
//
// Call this from the daemon's startup recovery path (alongside the
// existing orphan-recovery sweep). It is safe to call concurrently
// with live event handling — finalize for a given session is gated
// by MutateSessionMeta's flock.
func (h *DefaultTerminalErrorHandler) RecoverPendingTerminalFinalize(ctx context.Context) error {
	if h.LedgerPath == "" {
		return errors.New("ledger path not configured")
	}
	sessionsDir := filepath.Join(h.LedgerPath, "sessions")
	matches, err := filepath.Glob(filepath.Join(sessionsDir, "*", terminalMarkerFile))
	if err != nil {
		return fmt.Errorf("glob terminal markers: %w", err)
	}
	for _, markerPath := range matches {
		sessionPath := filepath.Dir(markerPath)
		raw, readErr := os.ReadFile(markerPath)
		if readErr != nil {
			h.Logger.Warn("terminal_error recovery: read marker failed",
				"path", markerPath, "err", readErr)
			continue
		}
		var rec struct {
			AdapterType string                            `json:"adapter_type"`
			Data        adapterprotocol.TerminalErrorData `json:"data"`
			WrittenAt   time.Time                         `json:"written_at"`
		}
		if err := json.Unmarshal(raw, &rec); err != nil {
			h.Logger.Warn("terminal_error recovery: decode marker failed",
				"path", markerPath, "err", err)
			// Drop the unparseable marker so we stop tripping over it.
			_ = os.Remove(markerPath)
			continue
		}
		// Use the marker's recorded agent_id if present (the wire payload
		// did not include it explicitly, but the supervisor knows it at
		// dispatch time — for the recovery path we infer from the
		// session dir's recording state).
		agentID := h.agentIDForSession(sessionPath)
		if err := h.processTerminalEvent(ctx, rec.AdapterType, agentID, sessionPath, rec.Data, false); err != nil {
			h.Logger.Warn("terminal_error recovery: replay failed",
				"path", sessionPath, "err", err)
		}
	}
	return nil
}

// agentIDForSession reads .recording.json from the session dir to
// recover the agent ID. Returns "" if not found; the recovery path
// still completes the finalize stamp + audit write without it.
func (h *DefaultTerminalErrorHandler) agentIDForSession(sessionPath string) string {
	data, err := os.ReadFile(filepath.Join(sessionPath, ".recording.json"))
	if err != nil {
		return ""
	}
	var state struct {
		AgentID string `json:"agent_id"`
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return ""
	}
	return state.AgentID
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
