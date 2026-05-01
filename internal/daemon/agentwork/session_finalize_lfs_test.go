package agentwork

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/session"
)

// TestDetect_SkipsLFSStubSessions verifies that detectInDir skips sessions whose
// raw.jsonl is an LFS pointer stub instead of real content. Without this check,
// stub sessions would be enqueued every 5 minutes and immediately fail in BuildPrompt.
func TestDetect_SkipsLFSStubSessions(t *testing.T) {
	handler := NewSessionFinalizeHandler(slog.Default())

	ledgerPath := t.TempDir()
	sessionsDir := filepath.Join(ledgerPath, "sessions")

	// LFS stub session — should be skipped
	stubDir := filepath.Join(sessionsDir, "2026-03-29T16-58-testuser-OxSTUB")
	if err := os.MkdirAll(stubDir, 0755); err != nil {
		t.Fatal(err)
	}
	lfsPointer := fmt.Sprintf("version https://git-lfs.github.com/spec/v1\noid sha256:%s\nsize 4096\n",
		"abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890")
	if err := os.WriteFile(filepath.Join(stubDir, "raw.jsonl"), []byte(lfsPointer), 0644); err != nil {
		t.Fatal(err)
	}

	// real session — should be detected
	realDir := filepath.Join(sessionsDir, "2026-03-30T10-00-testuser-OxREAL")
	if err := os.MkdirAll(realDir, 0755); err != nil {
		t.Fatal(err)
	}
	rawContent := "{\"_meta\":{\"schema_version\":\"1\",\"agent_type\":\"claude-code\"}}\n{\"type\":\"user\",\"content\":\"hello\",\"seq\":1}\n{\"type\":\"assistant\",\"content\":\"hi\",\"seq\":2}\n"
	if err := os.WriteFile(filepath.Join(realDir, "raw.jsonl"), []byte(rawContent), 0644); err != nil {
		t.Fatal(err)
	}

	// sanity: confirm the stub file is recognized as an LFS pointer
	if !lfs.IsPointerFile(filepath.Join(stubDir, "raw.jsonl")) {
		t.Fatal("test setup: stub raw.jsonl not recognized as LFS pointer")
	}

	items, err := handler.Detect(ledgerPath)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}

	// only the real session should be returned
	if len(items) != 1 {
		t.Fatalf("expected 1 item (real session only), got %d", len(items))
	}
	payload := items[0].Payload.(*SessionFinalizePayload)
	if filepath.Base(payload.SessionDir) != "2026-03-30T10-00-testuser-OxREAL" {
		t.Errorf("expected real session, got %s", filepath.Base(payload.SessionDir))
	}
}

// TestWriteMetaAndUploadLFS_CorruptExistingMetaIsFatal asserts that if
// meta.json already exists on disk but cannot be read or parsed,
// writeMetaAndUploadLFS returns a non-nil error and does NOT overwrite
// the file. The caller (ProcessResult) must use this error to abort the
// finalize flow before staging/committing — silently rotating an ID we
// cannot verify would break dedup.
//
// Failure prevented: a regression where the function only logs and
// returns nil on PreservedSessionID errors lets the daemon stage and
// commit a fresh meta.json over the corrupted one, silently rotating
// any SessionID that was there.
func TestWriteMetaAndUploadLFS_CorruptExistingMetaIsFatal(t *testing.T) {
	handler := NewSessionFinalizeHandler(slog.Default())
	handler.skipGit = true

	sessionName := "2026-05-01T10-00-testuser-OxCRPT"
	ledgerPath := t.TempDir()
	sessionDir := filepath.Join(ledgerPath, "sessions", sessionName)
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// plant a corrupt meta.json that ReadSessionMeta cannot parse
	corruptBytes := []byte("{this is not valid JSON")
	metaPath := filepath.Join(sessionDir, "meta.json")
	if err := os.WriteFile(metaPath, corruptBytes, 0o644); err != nil {
		t.Fatal(err)
	}

	stored := &session.StoredSession{
		Entries: []map[string]any{{"type": "user"}},
	}
	summaryResp := &session.SummarizeResponse{Title: "x", Summary: "x"}
	payload := &SessionFinalizePayload{SessionDir: sessionDir, LedgerPath: ledgerPath}

	fileRefs, err := handler.writeMetaAndUploadLFS(payload, stored, summaryResp)
	if err == nil {
		t.Fatal("expected error from writeMetaAndUploadLFS when existing meta.json is corrupt")
	}
	if fileRefs != nil {
		t.Errorf("fileRefs must be nil on fatal error, got %d entries", len(fileRefs))
	}

	// CRITICAL: the corrupt meta.json must be untouched on disk; rotating
	// it to a fresh write is exactly the failure this guard prevents.
	got, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatalf("read meta.json after fatal-error path: %v", err)
	}
	if string(got) != string(corruptBytes) {
		t.Errorf("meta.json was modified despite fatal-error path; got %q want %q", got, corruptBytes)
	}
}

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
	// LFS upload is skipped, function returns (nil, nil) fileRefs.
	fileRefs, err := handler.writeMetaAndUploadLFS(payload, stored, summaryResp)
	if err != nil {
		t.Fatalf("writeMetaAndUploadLFS returned unexpected error: %v", err)
	}
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
