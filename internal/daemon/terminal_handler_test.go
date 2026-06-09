package daemon

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/pkg/adapterprotocol"
)

func seedSessionMeta(t *testing.T, sessionPath string, stopReason string) {
	t.Helper()
	if err := os.MkdirAll(sessionPath, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	meta := lfs.NewSessionMeta("test-session", "tester", "Ox1234", "claude-code", time.Now()).
		StopReason(stopReason).
		Build()
	if err := lfs.WriteSessionMetaOnly(sessionPath, meta); err != nil {
		t.Fatalf("write meta: %v", err)
	}
}

func newTestHandler(t *testing.T, ledgerPath string) *DefaultTerminalErrorHandler {
	t.Helper()
	return &DefaultTerminalErrorHandler{
		LedgerPath: ledgerPath,
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func TestProcessTerminalEvent_StampsMeta(t *testing.T) {
	root := t.TempDir()
	sessionPath := filepath.Join(root, "sessions", "s1")
	seedSessionMeta(t, sessionPath, "")

	h := newTestHandler(t, root)
	resetsAt := time.Now().Add(3 * time.Hour).UTC()
	data := adapterprotocol.TerminalErrorData{
		Reason:      "rate_limited",
		Source:      "regex",
		PatternID:   "claude_code.system_error.usage_limit",
		RawMessage:  "5-hour limit reached resets 3pm",
		ResetsAtRaw: "3pm",
		ResetsAt:    &resetsAt,
		DetectedAt:  time.Now().Add(-30 * time.Second).UTC(),
		ConfirmedAt: time.Now().UTC(),
	}
	if err := h.processTerminalEvent(context.Background(), "claude-code", "agent-1", sessionPath, data, true); err != nil {
		t.Fatalf("process: %v", err)
	}

	meta, err := lfs.ReadSessionMeta(sessionPath)
	if err != nil {
		t.Fatalf("read meta: %v", err)
	}
	if meta.StopReason != "rate_limited" {
		t.Errorf("StopReason=%q want rate_limited", meta.StopReason)
	}
	if meta.StopSource != "regex" {
		t.Errorf("StopSource=%q want regex", meta.StopSource)
	}
	if meta.StopPatternID != "claude_code.system_error.usage_limit" {
		t.Errorf("StopPatternID=%q", meta.StopPatternID)
	}
	if meta.StopDetail == "" {
		t.Errorf("StopDetail empty")
	}
	if meta.StopResetsAtRaw != "3pm" {
		t.Errorf("StopResetsAtRaw=%q want 3pm", meta.StopResetsAtRaw)
	}
	if meta.StopResetsAt == nil {
		t.Errorf("StopResetsAt nil")
	}
	// Marker must be deleted after success.
	if _, err := os.Stat(filepath.Join(sessionPath, terminalMarkerFile)); !os.IsNotExist(err) {
		t.Errorf("marker not deleted: err=%v", err)
	}
}

func TestProcessTerminalEvent_PrecedenceGate(t *testing.T) {
	root := t.TempDir()
	sessionPath := filepath.Join(root, "sessions", "user-stopped")
	seedSessionMeta(t, sessionPath, "stopped") // user already stopped

	h := newTestHandler(t, root)
	data := adapterprotocol.TerminalErrorData{
		Reason:     "rate_limited",
		Source:     "regex",
		PatternID:  "p1",
		RawMessage: "limit reached",
	}
	if err := h.processTerminalEvent(context.Background(), "claude-code", "agent-1", sessionPath, data, true); err != nil {
		t.Fatalf("process: %v", err)
	}
	meta, err := lfs.ReadSessionMeta(sessionPath)
	if err != nil {
		t.Fatalf("read meta: %v", err)
	}
	// user-stop wins — terminal-error fields must NOT have been written
	if meta.StopReason != "stopped" {
		t.Errorf("StopReason=%q want stopped (user-stop precedence)", meta.StopReason)
	}
	if meta.StopSource != "" {
		t.Errorf("StopSource leaked despite precedence gate: %q", meta.StopSource)
	}
	if meta.StopPatternID != "" {
		t.Errorf("StopPatternID leaked: %q", meta.StopPatternID)
	}
	// Marker must STILL be deleted so we don't replay on every restart.
	if _, err := os.Stat(filepath.Join(sessionPath, terminalMarkerFile)); !os.IsNotExist(err) {
		t.Errorf("marker not deleted after precedence skip: err=%v", err)
	}
}

func TestProcessTerminalEvent_TruncatesDetail(t *testing.T) {
	root := t.TempDir()
	sessionPath := filepath.Join(root, "sessions", "trunc")
	seedSessionMeta(t, sessionPath, "")

	h := newTestHandler(t, root)
	big := strings.Repeat("x", terminalMaxDetail+200)
	data := adapterprotocol.TerminalErrorData{
		Reason:     "rate_limited",
		Source:     "regex",
		PatternID:  "p1",
		RawMessage: big,
	}
	if err := h.processTerminalEvent(context.Background(), "claude-code", "agent-1", sessionPath, data, true); err != nil {
		t.Fatalf("process: %v", err)
	}
	meta, _ := lfs.ReadSessionMeta(sessionPath)
	if len(meta.StopDetail) > terminalMaxDetail {
		t.Errorf("StopDetail len=%d exceeds cap %d", len(meta.StopDetail), terminalMaxDetail)
	}
}

func TestProcessTerminalEvent_WritesMarkerBeforeMeta(t *testing.T) {
	root := t.TempDir()
	sessionPath := filepath.Join(root, "sessions", "marker-first")
	seedSessionMeta(t, sessionPath, "")

	h := newTestHandler(t, root)
	data := adapterprotocol.TerminalErrorData{Reason: "rate_limited", Source: "regex"}
	if err := h.writeMarker(sessionPath, "claude-code", data); err != nil {
		t.Fatalf("writeMarker: %v", err)
	}
	if _, err := os.Stat(filepath.Join(sessionPath, terminalMarkerFile)); err != nil {
		t.Fatalf("marker not present after writeMarker: %v", err)
	}
	// Decode and assert the marker carries the wire data.
	raw, _ := os.ReadFile(filepath.Join(sessionPath, terminalMarkerFile))
	var rec struct {
		AdapterType string                            `json:"adapter_type"`
		Data        adapterprotocol.TerminalErrorData `json:"data"`
	}
	if err := json.Unmarshal(raw, &rec); err != nil {
		t.Fatalf("marker decode: %v", err)
	}
	if rec.AdapterType != "claude-code" || rec.Data.Reason != "rate_limited" {
		t.Errorf("marker contents wrong: %+v", rec)
	}
}

func TestRecoverPendingTerminalFinalize_ReplaysMarker(t *testing.T) {
	root := t.TempDir()
	sessionPath := filepath.Join(root, "sessions", "abandoned")
	seedSessionMeta(t, sessionPath, "")

	h := newTestHandler(t, root)
	// Simulate the previous daemon writing a marker and then crashing.
	data := adapterprotocol.TerminalErrorData{
		Reason:    "rate_limited",
		Source:    "regex",
		PatternID: "p1",
	}
	if err := h.writeMarker(sessionPath, "claude-code", data); err != nil {
		t.Fatalf("writeMarker: %v", err)
	}

	if err := h.RecoverPendingTerminalFinalize(context.Background()); err != nil {
		t.Fatalf("recover: %v", err)
	}
	meta, _ := lfs.ReadSessionMeta(sessionPath)
	if meta.StopReason != "rate_limited" {
		t.Errorf("expected StopReason rate_limited after recovery, got %q", meta.StopReason)
	}
	if _, err := os.Stat(filepath.Join(sessionPath, terminalMarkerFile)); !os.IsNotExist(err) {
		t.Errorf("marker not cleaned up after recovery")
	}
}

func TestRecoverPendingTerminalFinalize_NoOpAlreadyFinalized(t *testing.T) {
	root := t.TempDir()
	sessionPath := filepath.Join(root, "sessions", "user-quit")
	seedSessionMeta(t, sessionPath, "stopped")

	h := newTestHandler(t, root)
	data := adapterprotocol.TerminalErrorData{Reason: "rate_limited"}
	if err := h.writeMarker(sessionPath, "claude-code", data); err != nil {
		t.Fatalf("writeMarker: %v", err)
	}

	if err := h.RecoverPendingTerminalFinalize(context.Background()); err != nil {
		t.Fatalf("recover: %v", err)
	}
	meta, _ := lfs.ReadSessionMeta(sessionPath)
	if meta.StopReason != "stopped" {
		t.Errorf("user-stop must survive recovery; got %q", meta.StopReason)
	}
	if _, err := os.Stat(filepath.Join(sessionPath, terminalMarkerFile)); !os.IsNotExist(err) {
		t.Errorf("marker not cleaned up on no-op recovery")
	}
}

func TestRecoverPendingTerminalFinalize_DropsCorruptMarker(t *testing.T) {
	root := t.TempDir()
	sessionPath := filepath.Join(root, "sessions", "corrupt")
	if err := os.MkdirAll(sessionPath, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sessionPath, terminalMarkerFile), []byte("not json"), 0o644); err != nil {
		t.Fatalf("seed corrupt marker: %v", err)
	}
	h := newTestHandler(t, root)
	if err := h.RecoverPendingTerminalFinalize(context.Background()); err != nil {
		t.Fatalf("recover: %v", err)
	}
	if _, err := os.Stat(filepath.Join(sessionPath, terminalMarkerFile)); !os.IsNotExist(err) {
		t.Errorf("corrupt marker not cleaned up")
	}
}

func TestProcessTerminalEvent_AuditLog(t *testing.T) {
	root := t.TempDir()
	sessionPath := filepath.Join(root, "sessions", "audit")
	seedSessionMeta(t, sessionPath, "")

	h := newTestHandler(t, root)
	data := adapterprotocol.TerminalErrorData{
		Reason:      "rate_limited",
		Source:      "regex",
		PatternID:   "claude_code.system_error.usage_limit",
		RawMessage:  "5-hour limit reached resets 3pm",
		DetectedAt:  time.Now().UTC(),
		ConfirmedAt: time.Now().UTC(),
	}
	if err := h.processTerminalEvent(context.Background(), "claude-code", "Ox1234", sessionPath, data, true); err != nil {
		t.Fatalf("process: %v", err)
	}
	auditPath := filepath.Join(root, ".sageox", "audit", terminalAuditFile)
	raw, err := os.ReadFile(auditPath)
	if err != nil {
		t.Fatalf("read audit: %v", err)
	}
	if !strings.Contains(string(raw), `"reason":"rate_limited"`) {
		t.Errorf("audit missing reason field: %s", raw)
	}
	if !strings.Contains(string(raw), `"agent_id":"Ox1234"`) {
		t.Errorf("audit missing agent_id: %s", raw)
	}
}

func TestProcessTerminalEvent_NoMetaIsNoOp(t *testing.T) {
	root := t.TempDir()
	sessionPath := filepath.Join(root, "sessions", "no-meta")
	if err := os.MkdirAll(sessionPath, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	h := newTestHandler(t, root)
	data := adapterprotocol.TerminalErrorData{Reason: "rate_limited", Source: "regex"}
	if err := h.processTerminalEvent(context.Background(), "claude-code", "agent-1", sessionPath, data, true); err != nil {
		t.Fatalf("process: %v", err)
	}
	// no meta.json before, no meta.json after — handler refuses to invent one.
	if _, err := os.Stat(filepath.Join(sessionPath, "meta.json")); !os.IsNotExist(err) {
		t.Errorf("handler should not synthesize meta.json from scratch")
	}
}
