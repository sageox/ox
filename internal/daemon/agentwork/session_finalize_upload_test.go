package agentwork

import (
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
)

// rawContent is a minimal valid session with substantive entries.
const testRawContent = `{"metadata":{"schema_version":"1","agent_type":"claude-code","username":"testuser","agent_id":"OxTEST"}}
{"type":"user","content":"hello","seq":1}
{"type":"assistant","content":"world","seq":2}
{"entry_count":2}
`

// --- A. Detect: fully-finalized-but-not-pushed sessions ---
// TestDetect_FullyFinalizedInCache_NotPushed verifies that Detect() queues sessions
// that have all artifacts in the ledger cache but are absent from ledger/sessions/.
// Failure prevented: such sessions were silently skipped because missingArtifacts()
// returned [] — they accumulated indefinitely without being committed/pushed.
func TestDetect_FullyFinalizedInCache_NotPushed(t *testing.T) {
	handler := NewSessionFinalizeHandler(slog.Default())
	ledgerPath := t.TempDir()

	// create a session in ledger cache with ALL artifacts present
	sessionName := "2026-01-10T12-00-testuser-OxCACHE"
	cacheDir := filepath.Join(ledgerPath, ".sageox", "cache", "sessions", sessionName)
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cacheDir, "raw.jsonl"), []byte(testRawContent), 0644); err != nil {
		t.Fatal(err)
	}
	for _, name := range requiredArtifacts {
		if err := os.WriteFile(filepath.Join(cacheDir, name), []byte("artifact"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(cacheDir, "meta.json"), []byte(`{"session":"test"}`), 0644); err != nil {
		t.Fatal(err)
	}

	items, err := handler.Detect(ledgerPath)
	if err != nil {
		t.Fatalf("Detect failed: %v", err)
	}

	if len(items) != 1 {
		t.Fatalf("expected 1 upload-only item, got %d", len(items))
	}
	payload, ok := items[0].Payload.(*SessionFinalizePayload)
	if !ok {
		t.Fatalf("unexpected payload type %T", items[0].Payload)
	}
	if !payload.UploadOnly {
		t.Error("expected UploadOnly=true for fully-finalized cache session")
	}
}

// TestDetect_FullyFinalizedInCache_AlreadyPushed verifies that Detect() skips sessions
// that are in the cache AND already present in ledger/sessions/ (already uploaded).
// Failure prevented: re-uploading a session that was already pushed would create duplicates.
func TestDetect_FullyFinalizedInCache_AlreadyPushed(t *testing.T) {
	handler := NewSessionFinalizeHandler(slog.Default())
	ledgerPath := t.TempDir()

	sessionName := "2026-01-10T13-00-testuser-OxDONE"
	cacheDir := filepath.Join(ledgerPath, ".sageox", "cache", "sessions", sessionName)
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cacheDir, "raw.jsonl"), []byte(testRawContent), 0644); err != nil {
		t.Fatal(err)
	}
	for _, name := range requiredArtifacts {
		if err := os.WriteFile(filepath.Join(cacheDir, name), []byte("done"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	// also present in ledger/sessions/ (already uploaded)
	ledgerSessionDir := filepath.Join(ledgerPath, "sessions", sessionName)
	if err := os.MkdirAll(ledgerSessionDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ledgerSessionDir, "meta.json"), []byte(`{"session":"done"}`), 0644); err != nil {
		t.Fatal(err)
	}

	items, err := handler.Detect(ledgerPath)
	if err != nil {
		t.Fatalf("Detect failed: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("expected 0 items (already uploaded), got %d", len(items))
	}
}

// --- B. BuildPrompt: UploadOnly skips LLM ---
// TestBuildPrompt_UploadOnly_SkipsLLM verifies that UploadOnly payloads return
// SkipLLM=true, preventing an unnecessary and costly LLM invocation.
// Failure prevented: UploadOnly sessions triggering a full LLM summarization run.
func TestBuildPrompt_UploadOnly_SkipsLLM(t *testing.T) {
	handler := NewSessionFinalizeHandler(slog.Default())
	item := &WorkItem{
		ID:   "test-upload-only",
		Type: sessionFinalizeType,
		Payload: &SessionFinalizePayload{
			UploadOnly: true,
			SessionDir: "/tmp/fake",
			RawPath:    "/tmp/fake/raw.jsonl",
			LedgerPath: "/tmp/fake-ledger",
		},
	}

	req, err := handler.BuildPrompt(item)
	if err != nil {
		t.Fatalf("BuildPrompt failed: %v", err)
	}
	if !req.SkipLLM {
		t.Error("expected SkipLLM=true for UploadOnly payload")
	}
	if req.Prompt != "" {
		t.Errorf("expected empty prompt for UploadOnly, got %q", req.Prompt)
	}
}

// --- C. stageSessionInLedger: moves from cache to sessions/ ---
// TestStageSessionInLedger_CopiesFiles verifies that stageSessionInLedger copies
// all files from .sageox/cache/sessions/<name>/ to ledger/sessions/<name>/ and
// updates payload.SessionDir.
// Failure prevented: gitCommitAndPush silently staging nothing because .sageox/cache/
// is gitignored in the ledger.
func TestStageSessionInLedger_CopiesFiles(t *testing.T) {
	handler := NewSessionFinalizeHandler(slog.Default())
	ledgerPath := t.TempDir()

	sessionName := "2026-01-10T14-00-testuser-OxSTAGE"
	cacheDir := filepath.Join(ledgerPath, ".sageox", "cache", "sessions", sessionName)
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		t.Fatal(err)
	}
	testFiles := []string{"raw.jsonl", "summary.md", "meta.json"}
	for _, f := range testFiles {
		if err := os.WriteFile(filepath.Join(cacheDir, f), []byte("content of "+f), 0644); err != nil {
			t.Fatal(err)
		}
	}

	payload := &SessionFinalizePayload{
		SessionDir: cacheDir,
		LedgerPath: ledgerPath,
	}

	if err := handler.stageSessionInLedger(payload); err != nil {
		t.Fatalf("stageSessionInLedger failed: %v", err)
	}

	expectedDest := filepath.Join(ledgerPath, "sessions", sessionName)
	if payload.SessionDir != expectedDest {
		t.Errorf("payload.SessionDir not updated: got %q, want %q", payload.SessionDir, expectedDest)
	}

	for _, f := range testFiles {
		dst := filepath.Join(expectedDest, f)
		data, err := os.ReadFile(dst)
		if err != nil {
			t.Errorf("file %s not copied: %v", f, err)
			continue
		}
		if string(data) != "content of "+f {
			t.Errorf("file %s has wrong content: %q", f, data)
		}
	}
}

// TestStageSessionInLedger_NoopIfAlreadyInLedger verifies that stageSessionInLedger
// is a no-op when the session is already in ledger/sessions/.
func TestStageSessionInLedger_NoopIfAlreadyInLedger(t *testing.T) {
	handler := NewSessionFinalizeHandler(slog.Default())
	ledgerPath := t.TempDir()

	sessionName := "2026-01-10T14-30-testuser-OxNOOP"
	sessionDir := filepath.Join(ledgerPath, "sessions", sessionName)
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		t.Fatal(err)
	}

	payload := &SessionFinalizePayload{
		SessionDir: sessionDir,
		LedgerPath: ledgerPath,
	}

	if err := handler.stageSessionInLedger(payload); err != nil {
		t.Fatalf("stageSessionInLedger failed: %v", err)
	}
	if payload.SessionDir != sessionDir {
		t.Errorf("payload.SessionDir should not change: got %q", payload.SessionDir)
	}
}

// --- D. isInLedgerCacheDir ---
func TestIsInLedgerCacheDir(t *testing.T) {
	ledger := "/home/user/.local/share/sageox/sageox.ai/ledgers/repo_abc"
	cases := []struct {
		sessionDir string
		want       bool
	}{
		{ledger + "/.sageox/cache/sessions/2026-01-01T00-00-user-OxABC", true},
		{ledger + "/sessions/2026-01-01T00-00-user-OxABC", false},
		{"/other/path/sessions/something", false},
		// a sibling dir that shares a prefix with the cache path but isn't under it
		{ledger + "/.sageox/cache/sessions-extra/OxABC", false},
	}
	for _, tc := range cases {
		got := isInLedgerCacheDir(tc.sessionDir, ledger)
		if got != tc.want {
			t.Errorf("isInLedgerCacheDir(%q) = %v, want %v", tc.sessionDir, got, tc.want)
		}
	}
}

// --- E. End-to-end: UploadOnly session commits to git ---
// TestProcessResult_UploadOnly_CommitsToGit verifies the full pipeline for an
// UploadOnly session: cache → stage → git commit → push, without LLM involvement.
// Failure prevented: UploadOnly sessions silently never landing in the ledger.
func TestProcessResult_UploadOnly_CommitsToGit(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	_, clonePath := setupBareAndCloneLedger(t)
	sessionName := "2026-01-15T10-00-testuser-OxUPLOAD"

	// put session in cache (fully finalized, all artifacts present)
	cacheDir := filepath.Join(clonePath, ".sageox", "cache", "sessions", sessionName)
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cacheDir, "raw.jsonl"), []byte(testRawContent), 0644); err != nil {
		t.Fatal(err)
	}
	for _, name := range requiredArtifacts {
		if err := os.WriteFile(filepath.Join(cacheDir, name), []byte("artifact content"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(cacheDir, "meta.json"), []byte(`{"session_name":"`+sessionName+`"}`), 0644); err != nil {
		t.Fatal(err)
	}

	handler := NewSessionFinalizeHandler(slog.Default())
	handler.skipLFS = true
	handler.skipGit = false
	handler.ledgerMu = &sync.Mutex{}

	item := &WorkItem{
		ID:   "test-upload-only-git",
		Type: sessionFinalizeType,
		Payload: &SessionFinalizePayload{
			SessionDir: cacheDir,
			RawPath:    filepath.Join(cacheDir, "raw.jsonl"),
			LedgerPath: clonePath,
			UploadOnly: true,
		},
	}

	if err := handler.ProcessResult(item, &RunResult{}); err != nil {
		t.Fatalf("ProcessResult failed: %v", err)
	}

	// session must be in ledger/sessions/ after commit
	ledgerSessionDir := filepath.Join(clonePath, "sessions", sessionName)
	if _, err := os.Stat(filepath.Join(ledgerSessionDir, "raw.jsonl")); err != nil {
		t.Errorf("raw.jsonl not found in ledger/sessions/: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ledgerSessionDir, "meta.json")); err != nil {
		t.Errorf("meta.json not found in ledger/sessions/: %v", err)
	}

	// cache dir must be pruned after successful commit
	if _, err := os.Stat(cacheDir); !os.IsNotExist(err) {
		t.Error("cache dir should be removed after successful upload")
	}
}

// TestProcessResult_RegularFinalize_StagesToLedger verifies that the regular
// finalization path (with LLM output) also stages from cache to ledger/sessions/.
// Failure prevented: regular ProcessResult silently committing to the gitignored
// cache path, leaving sessions stuck in "local only" state forever.
func TestProcessResult_RegularFinalize_StagesToLedger(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	_, clonePath := setupBareAndCloneLedger(t)
	sessionName := "2026-01-15T11-00-testuser-OxREGULAR"

	// put raw-only session in cache (typical: session stop failed before summarization)
	cacheDir := filepath.Join(clonePath, ".sageox", "cache", "sessions", sessionName)
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cacheDir, "raw.jsonl"), []byte(testRawContent), 0644); err != nil {
		t.Fatal(err)
	}

	handler := NewSessionFinalizeHandler(slog.Default())
	handler.skipLFS = true
	handler.skipGit = false
	handler.ledgerMu = &sync.Mutex{}

	llmOutput := `{"title":"Test Session","summary":"A test session.","quality_score":0.8,"score_reason":"fine","outcome":"success","key_actions":["did something"]}`

	item := &WorkItem{
		ID:   "test-regular-stage",
		Type: sessionFinalizeType,
		Payload: &SessionFinalizePayload{
			SessionDir: cacheDir,
			RawPath:    filepath.Join(cacheDir, "raw.jsonl"),
			Missing:    requiredArtifacts,
			LedgerPath: clonePath,
		},
	}

	if err := handler.ProcessResult(item, &RunResult{Output: llmOutput}); err != nil {
		t.Fatalf("ProcessResult failed: %v", err)
	}

	// session must be in ledger/sessions/, not just in cache
	ledgerSessionDir := filepath.Join(clonePath, "sessions", sessionName)
	if _, err := os.Stat(filepath.Join(ledgerSessionDir, "raw.jsonl")); err != nil {
		t.Errorf("raw.jsonl not in ledger/sessions/: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ledgerSessionDir, "summary.json")); err != nil {
		t.Errorf("summary.json not in ledger/sessions/: %v", err)
	}

	// cache dir must be pruned
	if _, err := os.Stat(cacheDir); !os.IsNotExist(err) {
		t.Error("cache dir should be pruned after finalize+commit")
	}
}
