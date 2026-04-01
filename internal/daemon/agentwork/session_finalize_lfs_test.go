package agentwork

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/session"
)

// TestWriteMetaAndUploadLFS_ContentFilesIntactAndMetaWritten is a regression test for
// bug #291 at the writeMetaAndUploadLFS layer. It verifies that when LFS is skipped
// (projectRoot=""), the function uses WriteSessionMetaOnly — writing meta.json without
// replacing content files with pointer stubs.
//
// The test would fail if writeMetaAndUploadLFS called WriteSessionMeta (which also
// writes pointer files) and somehow acquired non-empty fileRefs.
func TestWriteMetaAndUploadLFS_ContentFilesIntactAndMetaWritten(t *testing.T) {
	handler := NewSessionFinalizeHandler(slog.Default())
	handler.skipGit = true
	// skipLFS=false but projectRoot="" — LFS block is skipped at the early-return guard

	sessionName := "2026-01-15T13-00-testuser-OxPTR2"
	ledgerPath := t.TempDir()
	sessionDir := filepath.Join(ledgerPath, "sessions", sessionName)
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		t.Fatal(err)
	}

	rawContent := `{"metadata":{"agent_id":"OxPTR2","agent_type":"claude-code","created_at":"2026-01-15T13:00:00Z"},"type":"header"}
{"type":"user","content":"hello","seq":1}
{"type":"assistant","content":"world","seq":2}
`
	rawPath := filepath.Join(sessionDir, "raw.jsonl")
	if err := os.WriteFile(rawPath, []byte(rawContent), 0644); err != nil {
		t.Fatal(err)
	}

	stored := &session.StoredSession{
		Entries: []map[string]any{{"type": "user"}, {"type": "assistant"}},
	}
	summaryResp := &session.SummarizeResponse{
		Title:   "Pointer Regression",
		Summary: "No LFS pointer replacement",
	}
	payload := &SessionFinalizePayload{
		SessionDir: sessionDir,
		LedgerPath: ledgerPath,
	}

	// projectRoot="" → LFS early-return path: WriteSessionMetaOnly is called,
	// LFS upload is skipped, function returns nil fileRefs
	fileRefs := handler.writeMetaAndUploadLFS(payload, stored, summaryResp)

	if fileRefs != nil {
		t.Errorf("expected nil fileRefs with empty projectRoot, got %d refs", len(fileRefs))
	}

	// CRITICAL: raw.jsonl must remain as real content, not a pointer stub
	if lfs.IsPointerFile(rawPath) {
		t.Error("raw.jsonl was replaced with LFS pointer stub (bug #291 regression in writeMetaAndUploadLFS)")
	}
	afterContent, err := os.ReadFile(rawPath)
	if err != nil {
		t.Fatalf("read raw.jsonl: %v", err)
	}
	if string(afterContent) != rawContent {
		t.Errorf("raw.jsonl content changed: got %q", afterContent)
	}

	// meta.json must be written (WriteSessionMetaOnly was called)
	meta, err := lfs.ReadSessionMeta(sessionDir)
	if err != nil {
		t.Fatalf("meta.json not found after writeMetaAndUploadLFS: %v", err)
	}
	if meta.SessionName != sessionName {
		t.Errorf("session_name: got %q, want %q", meta.SessionName, sessionName)
	}
	if meta.Title != "Pointer Regression" {
		t.Errorf("title: got %q, want %q", meta.Title, "Pointer Regression")
	}
	// no LFS refs → Files must be empty
	if len(meta.Files) != 0 {
		t.Errorf("meta.Files must be empty, got %d entries", len(meta.Files))
	}

	// no stale .tmp file should remain (atomic write contract)
	if _, err := os.Stat(filepath.Join(sessionDir, "meta.json.tmp")); err == nil {
		t.Error("stale meta.json.tmp found — atomic write did not clean up")
	}
}
