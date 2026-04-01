package agentwork

import (
	"encoding/json"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/pkg/sessionsummary"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildPrompt(t *testing.T) {
	handler := NewSessionFinalizeHandler(slog.Default())

	ledgerPath := createTestSession(t, "2026-01-06T14-32-testuser-OxABCD", nil)
	sessionDir := filepath.Join(ledgerPath, "sessions", "2026-01-06T14-32-testuser-OxABCD")
	rawPath := filepath.Join(sessionDir, "raw.jsonl")

	item := &WorkItem{
		ID:   "test-item",
		Type: sessionFinalizeType,
		Payload: &SessionFinalizePayload{
			SessionDir: sessionDir,
			RawPath:    rawPath,
			Missing:    requiredArtifacts,
			LedgerPath: ledgerPath,
		},
	}

	req, err := handler.BuildPrompt(item)
	if err != nil {
		t.Fatalf("BuildPrompt failed: %v", err)
	}

	if req.Prompt == "" {
		t.Error("expected non-empty prompt")
	}
	if req.WorkDir != ledgerPath {
		t.Errorf("expected WorkDir=%q, got %q", ledgerPath, req.WorkDir)
	}
	// prompt must reference the concrete raw file path and push-summary instruction
	if !strings.Contains(req.Prompt, rawPath) {
		t.Errorf("prompt should contain raw path %q", rawPath)
	}
	if !strings.Contains(req.Prompt, "push-summary") {
		t.Error("prompt should contain push-summary instruction")
	}
}

func TestProcessResult(t *testing.T) {
	handler := NewSessionFinalizeHandler(slog.Default())
	handler.skipGit = true // no git repo in tests

	ledgerPath := createTestSession(t, "2026-01-06T14-32-testuser-OxPROC", nil)
	sessionDir := filepath.Join(ledgerPath, "sessions", "2026-01-06T14-32-testuser-OxPROC")
	rawPath := filepath.Join(sessionDir, "raw.jsonl")

	// simulate LLM output with valid JSON
	summaryJSON := map[string]any{
		"title":         "Test Session",
		"summary":       "A test session.",
		"key_actions":   []string{"said hello"},
		"outcome":       "success",
		"topics_found":  []string{"testing"},
		"quality_score": 0.8,
		"score_reason":  "Substantive test session",
	}
	jsonBytes, _ := json.MarshalIndent(summaryJSON, "", "  ")
	llmOutput := string(jsonBytes)

	item := &WorkItem{
		ID:   "test-proc",
		Type: sessionFinalizeType,
		Payload: &SessionFinalizePayload{
			SessionDir: sessionDir,
			RawPath:    rawPath,
			Missing:    requiredArtifacts,
			LedgerPath: ledgerPath,
		},
	}

	result := &RunResult{
		Output:   llmOutput,
		Duration: 5 * time.Second,
		ExitCode: 0,
	}

	err := handler.ProcessResult(item, result)
	if err != nil {
		t.Fatalf("ProcessResult failed: %v", err)
	}

	// verify summary.md was written (structured markdown, not raw LLM output)
	summaryMDPath := filepath.Join(sessionDir, "summary.md")
	require.FileExists(t, summaryMDPath, "summary.md must be created")
	summaryContent, err := os.ReadFile(summaryMDPath)
	require.NoError(t, err)
	assert.Contains(t, string(summaryContent), "# Session Summary", "summary.md should contain structured markdown header")
	assert.Contains(t, string(summaryContent), "A test session.", "summary.md should contain the summary text from LLM output")

	// verify summary.json was written
	summaryJSONPath := filepath.Join(sessionDir, "summary.json")
	require.FileExists(t, summaryJSONPath, "summary.json must be created")

	// verify session.md was created
	mdPath := filepath.Join(sessionDir, "session.md")
	require.FileExists(t, mdPath, "session.md must be created")
}

func TestProcessResult_UnparsableJSON(t *testing.T) {
	handler := NewSessionFinalizeHandler(slog.Default())
	handler.skipGit = true

	ledgerPath := createTestSession(t, "2026-01-06T14-32-testuser-OxBADJ", nil)
	sessionDir := filepath.Join(ledgerPath, "sessions", "2026-01-06T14-32-testuser-OxBADJ")
	rawPath := filepath.Join(sessionDir, "raw.jsonl")

	item := &WorkItem{
		ID:   "test-bad-json",
		Type: sessionFinalizeType,
		Payload: &SessionFinalizePayload{
			SessionDir: sessionDir,
			RawPath:    rawPath,
			Missing:    requiredArtifacts,
			LedgerPath: ledgerPath,
		},
	}

	// LLM returns free-text, not valid JSON
	result := &RunResult{
		Output:   "This session was about testing things. It went well.",
		Duration: 3 * time.Second,
		ExitCode: 0,
	}

	err := handler.ProcessResult(item, result)
	if err != nil {
		t.Fatalf("ProcessResult should not fail with unparsable JSON: %v", err)
	}

	// all 3 artifacts should be written (unified code path always writes all)
	for _, artifact := range []string{"summary.md", "summary.json", "session.md"} {
		if _, statErr := os.Stat(filepath.Join(sessionDir, artifact)); statErr != nil {
			t.Errorf("%s should be created even when JSON parsing fails: %v", artifact, statErr)
		}
	}

	// summary.json should contain the raw text as summary field (fallback)
	data, _ := os.ReadFile(filepath.Join(sessionDir, "summary.json"))
	if !strings.Contains(string(data), "This session was about testing things") {
		t.Error("summary.json fallback should contain the raw LLM output as summary text")
	}
}

func TestProcessResult_WithRealGitRepo(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}

	sessionName := "2026-01-06T14-32-testuser-OxGIT"
	ledgerPath, sessionDir := createTestSessionInGitRepo(t, sessionName)
	rawPath := filepath.Join(sessionDir, "raw.jsonl")

	handler := NewSessionFinalizeHandler(slog.Default())
	// skipGit=false — exercises the real git commit path

	llmOutput := `{"title":"Git Test","summary":"Testing git commit path.","key_actions":["tested git"],"outcome":"success","topics_found":["git"]}`

	item := &WorkItem{
		ID:   "test-git",
		Type: sessionFinalizeType,
		Payload: &SessionFinalizePayload{
			SessionDir: sessionDir,
			RawPath:    rawPath,
			Missing:    requiredArtifacts,
			LedgerPath: ledgerPath,
		},
	}

	result := &RunResult{
		Output:   llmOutput,
		Duration: 2 * time.Second,
		ExitCode: 0,
	}

	err := handler.ProcessResult(item, result)
	require.NoError(t, err, "ProcessResult with real git should succeed")

	// verify artifacts exist
	require.FileExists(t, filepath.Join(sessionDir, "summary.md"))
	require.FileExists(t, filepath.Join(sessionDir, "summary.json"))
	require.FileExists(t, filepath.Join(sessionDir, "session.md"))

	// verify git commit was made — should have >1 commit now
	out, gitErr := exec.Command("git", "-C", ledgerPath, "log", "--oneline").CombinedOutput()
	require.NoError(t, gitErr)
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	assert.GreaterOrEqual(t, len(lines), 2, "should have at least 2 commits (init + finalize)")
}

func TestParseSummaryJSON(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantOK  bool
		wantErr bool
	}{
		{
			name:   "raw JSON",
			input:  `{"title":"Test","summary":"A test","key_actions":["did stuff"],"outcome":"success","topics_found":["go"]}`,
			wantOK: true,
		},
		{
			name: "fenced JSON",
			input: "Here is the summary:\n```json\n" +
				`{"title":"Test","summary":"A test","key_actions":[],"outcome":"success","topics_found":[]}` +
				"\n```\n",
			wantOK: true,
		},
		{
			name:    "plain text",
			input:   "This is just a text summary with no JSON.",
			wantErr: true,
		},
		{
			name:    "empty JSON object",
			input:   `{}`,
			wantErr: true, // title is empty
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := sessionsummary.ParseSummaryJSON(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if tt.wantOK {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
					return
				}
				if resp.Title != "Test" {
					t.Errorf("expected title 'Test', got %q", resp.Title)
				}
			}
		})
	}
}

func TestProcessResult_QualityScoreDiscard(t *testing.T) {
	ledgerPath := createTestSession(t, "test-discard", nil)
	sessionDir := filepath.Join(ledgerPath, "sessions", "test-discard")

	handler := NewSessionFinalizeHandlerForTest(slog.Default())
	handler.SetQualityThresholds(0.3, 0.1)

	item := &WorkItem{
		ID:       "test-1",
		Type:     sessionFinalizeType,
		DedupKey: "session-finalize:test-discard",
		Payload: &SessionFinalizePayload{
			SessionDir: sessionDir,
			RawPath:    filepath.Join(sessionDir, "raw.jsonl"),
			Missing:    []string{"summary.md"},
			LedgerPath: ledgerPath,
		},
	}

	// score below discard threshold — session dir should be removed
	result := &RunResult{
		Output: `{"title":"Routine rebasing","summary":"Just a rebase","key_actions":[],"outcome":"success","topics_found":[],"quality_score":0.05,"score_reason":"Trivial maintenance"}`,
	}

	err := handler.ProcessResult(item, result)
	if err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	// session dir should be removed
	if _, statErr := os.Stat(sessionDir); !os.IsNotExist(statErr) {
		t.Error("expected session directory to be removed for quality below discard threshold")
	}
}

func TestProcessResult_QualityScoreBelowUpload(t *testing.T) {
	ledgerPath := createTestSession(t, "test-local", nil)
	sessionDir := filepath.Join(ledgerPath, "sessions", "test-local")

	handler := NewSessionFinalizeHandlerForTest(slog.Default())
	handler.SetQualityThresholds(0.3, 0.1)

	item := &WorkItem{
		ID:       "test-2",
		Type:     sessionFinalizeType,
		DedupKey: "session-finalize:test-local",
		Payload: &SessionFinalizePayload{
			SessionDir: sessionDir,
			RawPath:    filepath.Join(sessionDir, "raw.jsonl"),
			Missing:    []string{"summary.md", "summary.json", "session.md"},
			LedgerPath: ledgerPath,
		},
	}

	// score above discard but below upload — artifacts generated, no push
	result := &RunResult{
		Output: `{"title":"Quick fix","summary":"Minor config change","key_actions":["updated config"],"outcome":"success","topics_found":["config"],"quality_score":0.2,"score_reason":"Routine config update"}`,
	}

	err := handler.ProcessResult(item, result)
	if err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	// session dir should still exist (not discarded)
	if _, statErr := os.Stat(sessionDir); os.IsNotExist(statErr) {
		t.Error("expected session directory to still exist for quality between discard and upload thresholds")
	}

	// summary.json should be written (artifacts generated locally)
	if _, statErr := os.Stat(filepath.Join(sessionDir, "summary.json")); os.IsNotExist(statErr) {
		t.Error("expected summary.json to be generated even below upload threshold")
	}
}

func TestProcessResult_QualityScoreAboveUpload(t *testing.T) {
	ledgerPath := createTestSession(t, "test-upload", nil)
	sessionDir := filepath.Join(ledgerPath, "sessions", "test-upload")

	handler := NewSessionFinalizeHandlerForTest(slog.Default())
	handler.SetQualityThresholds(0.3, 0.1)

	item := &WorkItem{
		ID:       "test-3",
		Type:     sessionFinalizeType,
		DedupKey: "session-finalize:test-upload",
		Payload: &SessionFinalizePayload{
			SessionDir: sessionDir,
			RawPath:    filepath.Join(sessionDir, "raw.jsonl"),
			Missing:    []string{"summary.md", "summary.json", "session.md"},
			LedgerPath: ledgerPath,
		},
	}

	// score above upload threshold — should proceed to git push (skipped in test mode)
	result := &RunResult{
		Output: `{"title":"Feature implementation","summary":"Implemented quality scoring","key_actions":["added scoring","updated config"],"outcome":"success","topics_found":["architecture"],"quality_score":0.8,"score_reason":"New feature with design decisions"}`,
	}

	err := handler.ProcessResult(item, result)
	if err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	// session dir should still exist
	if _, statErr := os.Stat(sessionDir); os.IsNotExist(statErr) {
		t.Error("expected session directory to still exist for high quality session")
	}

	// artifacts should be generated
	for _, artifact := range []string{"summary.json", "summary.md"} {
		if _, statErr := os.Stat(filepath.Join(sessionDir, artifact)); os.IsNotExist(statErr) {
			t.Errorf("expected %s to be generated for high quality session", artifact)
		}
	}
}

func TestProcessResult_WritesMetaJSON(t *testing.T) {
	handler := NewSessionFinalizeHandlerForTest(slog.Default())

	sessionName := "2026-01-10T09-30-testuser-OxMETA"
	ledgerPath := createTestSession(t, sessionName, nil)
	sessionDir := filepath.Join(ledgerPath, "sessions", sessionName)
	rawPath := filepath.Join(sessionDir, "raw.jsonl")

	item := &WorkItem{
		ID:   "test-meta",
		Type: sessionFinalizeType,
		Payload: &SessionFinalizePayload{
			SessionDir: sessionDir,
			RawPath:    rawPath,
			Missing:    requiredArtifacts,
			LedgerPath: ledgerPath,
		},
	}

	summaryJSON := map[string]any{
		"title":         "Meta Test Session",
		"summary":       "Testing meta.json generation",
		"key_actions":   []string{"tested meta"},
		"outcome":       "success",
		"topics_found":  []string{"testing"},
		"quality_score": 0.8,
		"score_reason":  "Test session",
	}
	jsonBytes, _ := json.MarshalIndent(summaryJSON, "", "  ")

	result := &RunResult{
		Output:   string(jsonBytes),
		Duration: 5 * time.Second,
	}

	if err := handler.ProcessResult(item, result); err != nil {
		t.Fatalf("ProcessResult failed: %v", err)
	}

	// verify meta.json was written
	meta, err := lfs.ReadSessionMeta(sessionDir)
	if err != nil {
		t.Fatalf("meta.json not found after ProcessResult: %v", err)
	}

	if meta.SessionName != sessionName {
		t.Errorf("session_name mismatch: got %q, want %q", meta.SessionName, sessionName)
	}
	if meta.StopReason != session.StopReasonRecovered {
		t.Errorf("stop_reason mismatch: got %q, want %q", meta.StopReason, session.StopReasonRecovered)
	}
	if meta.Title != "Meta Test Session" {
		t.Errorf("title mismatch: got %q, want %q", meta.Title, "Meta Test Session")
	}
	if meta.Summary != "Testing meta.json generation" {
		t.Errorf("summary mismatch: got %q", meta.Summary)
	}
	if meta.EntryCount != 2 { // 2 entries in createTestSession raw.jsonl
		t.Errorf("entry_count mismatch: got %d, want 2", meta.EntryCount)
	}
}

func TestProcessResult_MetaJSON_NoLFS_EmptyFiles(t *testing.T) {
	handler := NewSessionFinalizeHandlerForTest(slog.Default())

	sessionName := "2026-01-10T10-00-testuser-OxNOLF"
	ledgerPath := createTestSession(t, sessionName, nil)
	sessionDir := filepath.Join(ledgerPath, "sessions", sessionName)
	rawPath := filepath.Join(sessionDir, "raw.jsonl")

	item := &WorkItem{
		ID:   "test-nolfs",
		Type: sessionFinalizeType,
		Payload: &SessionFinalizePayload{
			SessionDir: sessionDir,
			RawPath:    rawPath,
			Missing:    requiredArtifacts,
			LedgerPath: ledgerPath,
		},
	}

	result := &RunResult{
		Output: `{"title":"No LFS","summary":"Testing without LFS","key_actions":["test"],"outcome":"success","topics_found":[],"quality_score":0.8,"score_reason":"Test"}`,
	}

	if err := handler.ProcessResult(item, result); err != nil {
		t.Fatalf("ProcessResult failed: %v", err)
	}

	meta, err := lfs.ReadSessionMeta(sessionDir)
	if err != nil {
		t.Fatalf("meta.json not found: %v", err)
	}

	// with skipLFS=true, files map should be empty
	if len(meta.Files) != 0 {
		t.Errorf("expected empty files map when LFS is skipped, got %d entries", len(meta.Files))
	}
}

func TestProcessResult_ExtractsMetadataFromHeader(t *testing.T) {
	handler := NewSessionFinalizeHandlerForTest(slog.Default())

	sessionName := "2026-01-10T10-30-alice-OxHDR1"
	ledgerPath := t.TempDir()
	sessionDir := filepath.Join(ledgerPath, "sessions", sessionName)
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		t.Fatal(err)
	}

	// raw.jsonl with metadata header containing agent info
	rawContent := `{"metadata":{"agent_id":"OxHDR1","agent_type":"claude-code","created_at":"2026-01-10T10:30:00Z","username":"alice@example.com"},"type":"header"}
{"type":"user","content":"test","seq":1}
{"type":"assistant","content":"done","seq":2}
`
	rawPath := filepath.Join(sessionDir, "raw.jsonl")
	if err := os.WriteFile(rawPath, []byte(rawContent), 0644); err != nil {
		t.Fatal(err)
	}

	item := &WorkItem{
		ID:   "test-header",
		Type: sessionFinalizeType,
		Payload: &SessionFinalizePayload{
			SessionDir: sessionDir,
			RawPath:    rawPath,
			Missing:    requiredArtifacts,
			LedgerPath: ledgerPath,
		},
	}

	result := &RunResult{
		Output: `{"title":"Header Test","summary":"Testing header extraction","key_actions":["test"],"outcome":"success","topics_found":[],"quality_score":0.8,"score_reason":"Test"}`,
	}

	if err := handler.ProcessResult(item, result); err != nil {
		t.Fatalf("ProcessResult failed: %v", err)
	}

	meta, err := lfs.ReadSessionMeta(sessionDir)
	if err != nil {
		t.Fatalf("meta.json not found: %v", err)
	}

	if meta.AgentID != "OxHDR1" {
		t.Errorf("agent_id mismatch: got %q, want %q", meta.AgentID, "OxHDR1")
	}
	if meta.AgentType != "claude-code" {
		t.Errorf("agent_type mismatch: got %q, want %q", meta.AgentType, "claude-code")
	}
	if meta.Username != "alice@example.com" {
		t.Errorf("username mismatch: got %q, want %q", meta.Username, "alice@example.com")
	}
}

// TestProcessResult_ContentFilesNotPointers_AfterFinalization is a regression test for
// bug #291: WriteSessionMeta (with fileRefs) was called before git push, replacing
// content files with LFS pointer stubs. If push then failed, the content was lost.
//
// This test verifies the end-to-end contract: after ProcessResult with LFS disabled
// (skipLFS=true), content files are never replaced with LFS pointer stubs.
// The test would fail if the pipeline called WriteSessionMeta (with fileRefs)
// instead of WriteSessionMetaOnly followed by a post-push WritePointerFiles.
func TestProcessResult_ContentFilesNotPointers_AfterFinalization(t *testing.T) {
	handler := NewSessionFinalizeHandlerForTest(slog.Default()) // skipGit=true, skipLFS=true

	sessionName := "2026-01-15T12-00-testuser-OxPTR1"
	ledgerPath := createTestSession(t, sessionName, nil)
	sessionDir := filepath.Join(ledgerPath, "sessions", sessionName)
	rawPath := filepath.Join(sessionDir, "raw.jsonl")

	// record the original content so we can compare after ProcessResult
	originalContent, err := os.ReadFile(rawPath)
	if err != nil {
		t.Fatalf("read raw.jsonl: %v", err)
	}

	item := &WorkItem{
		ID:   "test-ptr-regression",
		Type: sessionFinalizeType,
		Payload: &SessionFinalizePayload{
			SessionDir: sessionDir,
			RawPath:    rawPath,
			Missing:    requiredArtifacts,
			LedgerPath: ledgerPath,
		},
	}

	result := &RunResult{
		Output: `{"title":"Pointer Regression Test","summary":"Verifying no pointer replacement","key_actions":["verified"],"outcome":"success","topics_found":[],"quality_score":0.8,"score_reason":"Test"}`,
	}

	if err := handler.ProcessResult(item, result); err != nil {
		t.Fatalf("ProcessResult failed: %v", err)
	}

	// CRITICAL: raw.jsonl must not be replaced with an LFS pointer stub.
	// Before the fix, WriteSessionMeta(meta_with_files) was called before push,
	// which would have replaced raw.jsonl with a tiny pointer file.
	if lfs.IsPointerFile(rawPath) {
		t.Error("raw.jsonl was replaced with an LFS pointer stub (bug #291 regression)")
	}

	// content must be unchanged — not a truncated pointer
	afterContent, err := os.ReadFile(rawPath)
	if err != nil {
		t.Fatalf("read raw.jsonl after ProcessResult: %v", err)
	}
	if string(afterContent) != string(originalContent) {
		t.Errorf("raw.jsonl content changed unexpectedly after ProcessResult\nbefore: %q\nafter:  %q",
			originalContent, afterContent)
	}

	// meta.json must be written with empty Files (no LFS refs with skipLFS=true)
	meta, err := lfs.ReadSessionMeta(sessionDir)
	if err != nil {
		t.Fatalf("meta.json not found after ProcessResult: %v", err)
	}
	if len(meta.Files) != 0 {
		t.Errorf("meta.Files must be empty when LFS is skipped, got %d entries", len(meta.Files))
	}
}

// createTestSession creates a session directory with raw.jsonl and optional artifacts.
// Returns the ledger path.
func createTestSession(t *testing.T, sessionName string, includeArtifacts []string) string {
	t.Helper()
	ledgerPath := t.TempDir()
	sessionsDir := filepath.Join(ledgerPath, "sessions", sessionName)
	if err := os.MkdirAll(sessionsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// always create raw.jsonl with minimal content
	rawContent := `{"_meta":{"schema_version":"1","agent_type":"claude-code"}}
{"type":"user","content":"hello","seq":1}
{"type":"assistant","content":"hi there","seq":2}
`
	if err := os.WriteFile(filepath.Join(sessionsDir, "raw.jsonl"), []byte(rawContent), 0644); err != nil {
		t.Fatal(err)
	}

	for _, name := range includeArtifacts {
		if err := os.WriteFile(filepath.Join(sessionsDir, name), []byte("placeholder"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	return ledgerPath
}

// createTestSessionInGitRepo creates a session inside a real git repo.
// Returns (ledgerPath, sessionDir).
func createTestSessionInGitRepo(t *testing.T, sessionName string) (string, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	ledgerPath := t.TempDir()

	// init git repo with isolated config to avoid host git settings (gpgsign, hooksPath, etc.)
	require.NoError(t, exec.Command("git", "init", "--initial-branch=main", ledgerPath).Run())
	require.NoError(t, exec.Command("git", "-C", ledgerPath, "config", "user.email", "test@test.com").Run())
	require.NoError(t, exec.Command("git", "-C", ledgerPath, "config", "user.name", "Test").Run())
	require.NoError(t, exec.Command("git", "-C", ledgerPath, "config", "commit.gpgsign", "false").Run())

	// create sessions dir and raw.jsonl
	sessionsDir := filepath.Join(ledgerPath, "sessions", sessionName)
	require.NoError(t, os.MkdirAll(sessionsDir, 0755))

	rawContent := `{"_meta":{"schema_version":"1","agent_type":"claude-code"}}
{"type":"user","content":"hello","seq":1}
{"type":"assistant","content":"hi there","seq":2}
`
	require.NoError(t, os.WriteFile(filepath.Join(sessionsDir, "raw.jsonl"), []byte(rawContent), 0644))

	// initial commit
	require.NoError(t, exec.Command("git", "-C", ledgerPath, "add", "-A").Run())
	require.NoError(t, exec.Command("git", "-C", ledgerPath, "commit", "-m", "init").Run())

	return ledgerPath, sessionsDir
}
