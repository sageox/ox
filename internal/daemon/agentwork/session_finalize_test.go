package agentwork

import (
	"bufio"
	"encoding/json"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/session"
)

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

func TestDetect(t *testing.T) {
	handler := NewSessionFinalizeHandler(slog.Default())

	// create one incomplete session (missing all artifacts) and one complete session
	ledgerPath := t.TempDir()
	sessionsDir := filepath.Join(ledgerPath, "sessions")

	// incomplete session
	incompleteDir := filepath.Join(sessionsDir, "2026-01-06T14-32-testuser-Ox1234")
	if err := os.MkdirAll(incompleteDir, 0755); err != nil {
		t.Fatal(err)
	}
	rawContent := `{"_meta":{"schema_version":"1","agent_type":"claude-code"}}
{"type":"user","content":"hello","seq":1}
{"type":"assistant","content":"hi there","seq":2}
`
	if err := os.WriteFile(filepath.Join(incompleteDir, "raw.jsonl"), []byte(rawContent), 0644); err != nil {
		t.Fatal(err)
	}

	// complete session
	completeDir := filepath.Join(sessionsDir, "2026-01-06T15-00-testuser-Ox5678")
	if err := os.MkdirAll(completeDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(completeDir, "raw.jsonl"), []byte(rawContent), 0644); err != nil {
		t.Fatal(err)
	}
	for _, name := range requiredArtifacts {
		if err := os.WriteFile(filepath.Join(completeDir, name), []byte("done"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	items, err := handler.Detect(ledgerPath)
	if err != nil {
		t.Fatalf("Detect failed: %v", err)
	}

	if len(items) != 1 {
		t.Fatalf("expected 1 incomplete session, got %d", len(items))
	}

	item := items[0]
	if item.Type != sessionFinalizeType {
		t.Errorf("expected type %q, got %q", sessionFinalizeType, item.Type)
	}
	if item.Priority != sessionFinalizePriority {
		t.Errorf("expected priority %d, got %d", sessionFinalizePriority, item.Priority)
	}

	payload, ok := item.Payload.(*SessionFinalizePayload)
	if !ok {
		t.Fatalf("payload is not *SessionFinalizePayload: %T", item.Payload)
	}
	if len(payload.Missing) != len(requiredArtifacts) {
		t.Errorf("expected %d missing artifacts, got %d: %v", len(requiredArtifacts), len(payload.Missing), payload.Missing)
	}
}

func TestDetect_NoSessions(t *testing.T) {
	handler := NewSessionFinalizeHandler(slog.Default())

	// empty ledger with no sessions/ dir
	ledgerPath := t.TempDir()
	items, err := handler.Detect(ledgerPath)
	if err != nil {
		t.Fatalf("Detect failed: %v", err)
	}
	if len(items) != 0 {
		t.Errorf("expected 0 items for missing sessions dir, got %d", len(items))
	}

	// create empty sessions/ dir
	if err := os.MkdirAll(filepath.Join(ledgerPath, "sessions"), 0755); err != nil {
		t.Fatal(err)
	}
	items, err = handler.Detect(ledgerPath)
	if err != nil {
		t.Fatalf("Detect failed: %v", err)
	}
	if len(items) != 0 {
		t.Errorf("expected 0 items for empty sessions dir, got %d", len(items))
	}
}

func TestDetect_SkipsInvalidSessions(t *testing.T) {
	handler := NewSessionFinalizeHandler(slog.Default())

	ledgerPath := t.TempDir()
	sessionsDir := filepath.Join(ledgerPath, "sessions")

	// session dir with no raw.jsonl
	noRawDir := filepath.Join(sessionsDir, "2026-01-06T14-32-testuser-NoRaw")
	if err := os.MkdirAll(noRawDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(noRawDir, "summary.md"), []byte("orphan"), 0644); err != nil {
		t.Fatal(err)
	}

	// legacy dirs that should be skipped
	for _, name := range []string{"raw", "events"} {
		d := filepath.Join(sessionsDir, name)
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatal(err)
		}
	}

	// regular file (not a dir)
	if err := os.WriteFile(filepath.Join(sessionsDir, "stray-file.txt"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	items, err := handler.Detect(ledgerPath)
	if err != nil {
		t.Fatalf("Detect failed: %v", err)
	}
	if len(items) != 0 {
		t.Errorf("expected 0 items for invalid sessions, got %d", len(items))
	}
}

func TestDetect_SkipsLegacyDirs(t *testing.T) {
	handler := NewSessionFinalizeHandler(slog.Default())

	ledgerPath := t.TempDir()
	sessionsDir := filepath.Join(ledgerPath, "sessions")

	// "raw" and "events" are legacy dirs that should be ignored
	for _, name := range []string{"raw", "events"} {
		d := filepath.Join(sessionsDir, name)
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "raw.jsonl"), []byte(`{"type":"user","content":"x"}`), 0644); err != nil {
			t.Fatal(err)
		}
	}

	items, err := handler.Detect(ledgerPath)
	if err != nil {
		t.Fatalf("Detect failed: %v", err)
	}
	if len(items) != 0 {
		t.Errorf("expected 0 items, got %d", len(items))
	}
}

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
	// prompt should reference the raw file
	if len(req.Prompt) < 50 {
		t.Errorf("prompt seems too short: %d chars", len(req.Prompt))
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
	if _, statErr := os.Stat(summaryMDPath); statErr != nil {
		t.Errorf("summary.md not created: %v", statErr)
	} else {
		content, _ := os.ReadFile(summaryMDPath)
		mdStr := string(content)
		if !strings.Contains(mdStr, "# Session Summary") {
			t.Error("summary.md should contain structured markdown header")
		}
		if !strings.Contains(mdStr, "A test session.") {
			t.Errorf("summary.md should contain the summary text from LLM output, got:\n%s", mdStr)
		}
	}

	// verify summary.json was written
	summaryJSONPath := filepath.Join(sessionDir, "summary.json")
	if _, statErr := os.Stat(summaryJSONPath); statErr != nil {
		t.Errorf("summary.json not created: %v", statErr)
	}

	// verify session.html was created
	htmlPath := filepath.Join(sessionDir, "session.html")
	if _, statErr := os.Stat(htmlPath); statErr != nil {
		t.Errorf("session.html not created: %v", statErr)
	}

	// verify session.md was created
	mdPath := filepath.Join(sessionDir, "session.md")
	if _, statErr := os.Stat(mdPath); statErr != nil {
		t.Errorf("session.md not created: %v", statErr)
	}
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

	// all 4 artifacts should be written (unified code path always writes all)
	for _, artifact := range []string{"summary.md", "summary.json", "session.html", "session.md"} {
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
			resp, err := parseSummaryJSON(tt.input)
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

// TestDetect_StaleRecordingWithRaw verifies that a session abandoned by Ctrl-C
// (has .recording.json + raw.jsonl, but no session stop) is detected and
// finalized after the stale threshold. This is the core anti-entropy test.
func TestDetect_StaleRecordingWithRaw(t *testing.T) {
	handler := NewSessionFinalizeHandler(slog.Default())

	ledgerPath := t.TempDir()
	sessionName := "2026-01-10T09-00-testuser-OxCTRL"
	sessionDir := filepath.Join(ledgerPath, "sessions", sessionName)
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		t.Fatal(err)
	}

	// raw.jsonl with real content (as if hooks wrote entries before Ctrl-C)
	rawContent := `{"metadata":{"agent_id":"OxCTRL","agent_type":"claude","version":"1.0"},"type":"header"}
{"type":"user","content":"fix the login bug","seq":0,"timestamp":"2026-01-10T09:00:01Z"}
{"type":"tool","content":"","seq":1,"timestamp":"2026-01-10T09:00:05Z","tool_name":"Read","tool_input":"{\"file_path\":\"/src/auth.go\"}"}
{"type":"assistant","content":"I see the issue in the auth handler.","seq":2,"timestamp":"2026-01-10T09:00:08Z"}
`
	if err := os.WriteFile(filepath.Join(sessionDir, "raw.jsonl"), []byte(rawContent), 0644); err != nil {
		t.Fatal(err)
	}

	// .recording.json with StartedAt > 24h ago (simulates abandoned session)
	recState := map[string]any{
		"started_at": time.Now().Add(-25 * time.Hour).Format(time.RFC3339),
		"agent_id":   "OxCTRL",
		"session_id": "test-ctrl-c-session",
	}
	recData, _ := json.Marshal(recState)
	recPath := filepath.Join(sessionDir, recordingMarker)
	if err := os.WriteFile(recPath, recData, 0644); err != nil {
		t.Fatal(err)
	}

	// Detect should find this stale session
	items, err := handler.Detect(ledgerPath)
	if err != nil {
		t.Fatalf("Detect failed: %v", err)
	}

	if len(items) != 1 {
		t.Fatalf("expected 1 stale session, got %d", len(items))
	}

	// .recording.json should have been removed by Detect
	if _, statErr := os.Stat(recPath); !os.IsNotExist(statErr) {
		t.Error(".recording.json should have been removed after stale detection")
	}

	// payload should reference the correct session
	payload, ok := items[0].Payload.(*SessionFinalizePayload)
	if !ok {
		t.Fatalf("unexpected payload type: %T", items[0].Payload)
	}
	if payload.SessionDir != sessionDir {
		t.Errorf("session dir mismatch: got %q", payload.SessionDir)
	}
	if len(payload.Missing) != len(requiredArtifacts) {
		t.Errorf("expected %d missing artifacts, got %d", len(requiredArtifacts), len(payload.Missing))
	}
}

// TestDetect_RecentRecordingSkipped verifies that active sessions (< 24h old)
// are NOT finalized — they're still in progress.
func TestDetect_RecentRecordingSkipped(t *testing.T) {
	handler := NewSessionFinalizeHandler(slog.Default())

	ledgerPath := t.TempDir()
	sessionName := "2026-01-10T10-00-testuser-OxACTV"
	sessionDir := filepath.Join(ledgerPath, "sessions", sessionName)
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		t.Fatal(err)
	}

	// raw.jsonl exists
	rawContent := `{"metadata":{"agent_id":"OxACTV","agent_type":"claude","version":"1.0"},"type":"header"}
{"type":"user","content":"hello","seq":0,"timestamp":"2026-01-10T10:00:01Z"}
`
	if err := os.WriteFile(filepath.Join(sessionDir, "raw.jsonl"), []byte(rawContent), 0644); err != nil {
		t.Fatal(err)
	}

	// .recording.json is recent (1 hour ago — well within 24h threshold)
	recState := map[string]any{
		"started_at": time.Now().Add(-1 * time.Hour).Format(time.RFC3339),
		"agent_id":   "OxACTV",
	}
	recData, _ := json.Marshal(recState)
	recPath := filepath.Join(sessionDir, recordingMarker)
	if err := os.WriteFile(recPath, recData, 0644); err != nil {
		t.Fatal(err)
	}

	items, err := handler.Detect(ledgerPath)
	if err != nil {
		t.Fatalf("Detect failed: %v", err)
	}

	if len(items) != 0 {
		t.Errorf("expected 0 items for active session, got %d", len(items))
	}

	// .recording.json should still exist (not removed)
	if _, statErr := os.Stat(recPath); statErr != nil {
		t.Error(".recording.json should still exist for active sessions")
	}
}

// TestDetect_StaleRecordingWithoutRaw verifies that stale recordings with no
// raw.jsonl and no recoverable session file are skipped and their marker is cleared.
func TestDetect_StaleRecordingWithoutRaw(t *testing.T) {
	handler := NewSessionFinalizeHandler(slog.Default())

	ledgerPath := t.TempDir()
	sessionName := "2026-01-10T08-00-testuser-OxNORA"
	sessionDir := filepath.Join(ledgerPath, "sessions", sessionName)
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		t.Fatal(err)
	}

	// no raw.jsonl — just the marker (no session_file means recovery will fail)
	recState := map[string]any{
		"started_at": time.Now().Add(-48 * time.Hour).Format(time.RFC3339),
		"agent_id":   "OxNORA",
	}
	recData, _ := json.Marshal(recState)
	recPath := filepath.Join(sessionDir, recordingMarker)
	if err := os.WriteFile(recPath, recData, 0644); err != nil {
		t.Fatal(err)
	}

	items, err := handler.Detect(ledgerPath)
	if err != nil {
		t.Fatalf("Detect failed: %v", err)
	}

	if len(items) != 0 {
		t.Errorf("expected 0 items for stale recording without raw.jsonl, got %d", len(items))
	}

	// marker should be cleared for unrecoverable stale recordings
	if _, statErr := os.Stat(recPath); !os.IsNotExist(statErr) {
		t.Error(".recording.json should have been removed for unrecoverable stale recording")
	}
}

// TestCtrlC_FullFinalizationPipeline simulates the complete Ctrl-C anti-entropy
// scenario: session starts, entries are written by hooks, user Ctrl-C's without
// stopping, then the daemon detects and finalizes the session with all artifacts.
func TestCtrlC_FullFinalizationPipeline(t *testing.T) {
	handler := NewSessionFinalizeHandler(slog.Default())
	handler.skipGit = true

	ledgerPath := t.TempDir()
	sessionName := "2026-01-10T09-30-testuser-OxABRT"
	sessionDir := filepath.Join(ledgerPath, "sessions", sessionName)
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		t.Fatal(err)
	}

	// raw.jsonl with multi-turn content (as if PostToolUse hooks wrote entries)
	rawContent := `{"metadata":{"agent_id":"OxABRT","agent_type":"claude","created_at":"2026-01-10T09:30:00-07:00","version":"1.0"},"type":"header"}
{"type":"user","content":"Read the README and summarize it","seq":0,"timestamp":"2026-01-10T16:30:01Z"}
{"type":"tool","content":"","seq":1,"timestamp":"2026-01-10T16:30:05Z","tool_name":"Read","tool_input":"{\"file_path\":\"/project/README.md\"}"}
{"type":"assistant","content":"The README describes a web application framework with REST API support.","seq":2,"timestamp":"2026-01-10T16:30:08Z"}
{"type":"user","content":"Now add error handling to the main handler","seq":3,"timestamp":"2026-01-10T16:30:15Z"}
{"type":"tool","content":"","seq":4,"timestamp":"2026-01-10T16:30:20Z","tool_name":"Edit","tool_input":"{\"file_path\":\"/project/handler.go\"}"}
{"type":"assistant","content":"I've added error handling with proper HTTP status codes.","seq":5,"timestamp":"2026-01-10T16:30:25Z"}
`
	// note: NO footer entry — session was interrupted before stop could write it
	rawPath := filepath.Join(sessionDir, "raw.jsonl")
	if err := os.WriteFile(rawPath, []byte(rawContent), 0644); err != nil {
		t.Fatal(err)
	}

	// stale .recording.json (> 24h old)
	recState := map[string]any{
		"started_at": time.Now().Add(-26 * time.Hour).Format(time.RFC3339),
		"agent_id":   "OxABRT",
	}
	recData, _ := json.Marshal(recState)
	if err := os.WriteFile(filepath.Join(sessionDir, recordingMarker), recData, 0644); err != nil {
		t.Fatal(err)
	}

	// Step 1: Detect finds the stale session
	items, err := handler.Detect(ledgerPath)
	if err != nil {
		t.Fatalf("Detect failed: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 stale session, got %d", len(items))
	}

	// Step 2: BuildPrompt reads raw.jsonl and creates summarization prompt
	item := items[0]
	req, err := handler.BuildPrompt(item)
	if err != nil {
		t.Fatalf("BuildPrompt failed: %v", err)
	}
	if req.Prompt == "" {
		t.Error("expected non-empty prompt")
	}

	// Step 3: ProcessResult with simulated LLM output generates all artifacts
	summaryJSON2 := map[string]any{
		"title":         "README Review and Error Handling",
		"summary":       "Read the README and added error handling to the main HTTP handler with proper status codes.",
		"key_actions":   []string{"read README.md", "added error handling to handler.go"},
		"outcome":       "success",
		"topics_found":  []string{"error handling", "HTTP", "REST API"},
		"quality_score": 0.75,
		"score_reason":  "Feature implementation with code changes",
	}
	jsonBytes, _ := json.MarshalIndent(summaryJSON2, "", "  ")

	result := &RunResult{
		Output:   string(jsonBytes),
		Duration: 5 * time.Second,
		ExitCode: 0,
	}

	if err := handler.ProcessResult(item, result); err != nil {
		t.Fatalf("ProcessResult failed: %v", err)
	}

	// Step 4: Verify ALL artifacts exist (the core anti-entropy guarantee)
	for _, artifact := range requiredArtifacts {
		path := filepath.Join(sessionDir, artifact)
		if _, statErr := os.Stat(path); statErr != nil {
			t.Errorf("missing artifact after finalization: %s", artifact)
		}
	}

	// Verify summary.json has correct title
	summaryJSONData, err := os.ReadFile(filepath.Join(sessionDir, "summary.json"))
	if err != nil {
		t.Fatalf("failed to read summary.json: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(summaryJSONData, &parsed); err != nil {
		t.Fatalf("summary.json is invalid JSON: %v", err)
	}
	if title, ok := parsed["title"].(string); !ok || title != "README Review and Error Handling" {
		t.Errorf("summary.json title mismatch: got %q", parsed["title"])
	}

	// Verify session.html contains session content
	htmlData, err := os.ReadFile(filepath.Join(sessionDir, "session.html"))
	if err != nil {
		t.Fatalf("failed to read session.html: %v", err)
	}
	if len(htmlData) < 100 {
		t.Error("session.html seems too small")
	}

	// Verify session.md contains session content
	mdData, err := os.ReadFile(filepath.Join(sessionDir, "session.md"))
	if err != nil {
		t.Fatalf("failed to read session.md: %v", err)
	}
	if len(mdData) < 20 {
		t.Error("session.md seems too small")
	}
}

func TestConvertStoredEntries(t *testing.T) {
	stored := []map[string]any{
		{"type": "user", "content": "hello"},
		{"type": "assistant", "content": "hi there"},
		{"type": "tool", "content": "", "tool_name": "Read", "tool_input": "/tmp/foo"},
	}

	entries := convertStoredEntries(stored)
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}
	if string(entries[0].Type) != "user" {
		t.Errorf("entry 0 type: want 'user', got %q", entries[0].Type)
	}
	if entries[0].Content != "hello" {
		t.Errorf("entry 0 content: want 'hello', got %q", entries[0].Content)
	}
	if entries[2].ToolName != "Read" {
		t.Errorf("entry 2 tool_name: want 'Read', got %q", entries[2].ToolName)
	}
}

// deadPID runs a short-lived process and returns its PID after it has exited.
func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start process for dead PID: %v", err)
	}
	pid := cmd.Process.Pid
	if err := cmd.Wait(); err != nil {
		t.Fatalf("process wait failed: %v", err)
	}
	return pid
}

// setupRecordingSession creates a session dir with raw.jsonl and .recording.json.
// Returns (ledgerPath, sessionDir, recPath).
func setupRecordingSession(t *testing.T, sessionName string, recState map[string]any) (string, string, string) {
	t.Helper()
	ledgerPath := t.TempDir()
	sessionDir := filepath.Join(ledgerPath, "sessions", sessionName)
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		t.Fatal(err)
	}

	rawContent := `{"_meta":{"schema_version":"1","agent_type":"claude-code"}}
{"type":"user","content":"hello","seq":1}
{"type":"assistant","content":"hi there","seq":2}
`
	if err := os.WriteFile(filepath.Join(sessionDir, "raw.jsonl"), []byte(rawContent), 0644); err != nil {
		t.Fatal(err)
	}

	recData, _ := json.Marshal(recState)
	recPath := filepath.Join(sessionDir, recordingMarker)
	if err := os.WriteFile(recPath, recData, 0644); err != nil {
		t.Fatal(err)
	}

	return ledgerPath, sessionDir, recPath
}

// TestDetect_DeadPID verifies that a recording with a dead parent PID is
// detected as stale immediately, regardless of how recent the session is.
func TestDetect_DeadPID(t *testing.T) {
	handler := NewSessionFinalizeHandler(slog.Default())

	pid := deadPID(t)

	// started_at is only 1 minute ago — well within the 24h threshold,
	// but the dead PID should override the time check
	recState := map[string]any{
		"started_at": time.Now().Add(-1 * time.Minute).Format(time.RFC3339),
		"agent_id":   "OxDEAD",
		"parent_pid": pid,
	}
	ledgerPath, _, recPath := setupRecordingSession(t, "2026-03-13T10-00-testuser-OxDEAD", recState)

	items, err := handler.Detect(ledgerPath)
	if err != nil {
		t.Fatalf("Detect failed: %v", err)
	}

	if len(items) != 1 {
		t.Fatalf("expected 1 stale session (dead PID), got %d", len(items))
	}

	// .recording.json should be removed
	if _, statErr := os.Stat(recPath); !os.IsNotExist(statErr) {
		t.Error(".recording.json should have been removed for dead PID session")
	}

	payload, ok := items[0].Payload.(*SessionFinalizePayload)
	if !ok {
		t.Fatalf("unexpected payload type: %T", items[0].Payload)
	}
	if len(payload.Missing) != len(requiredArtifacts) {
		t.Errorf("expected %d missing artifacts, got %d", len(requiredArtifacts), len(payload.Missing))
	}
}

// TestDetect_LivePID verifies that a recording with a live parent PID and
// recent start time is NOT considered stale. The live PID confirms the process
// is still running, and the recent timestamp is within the 24h threshold.
func TestDetect_LivePID(t *testing.T) {
	handler := NewSessionFinalizeHandler(slog.Default())

	// use our own PID — guaranteed alive, with recent start time
	recState := map[string]any{
		"started_at": time.Now().Add(-1 * time.Hour).Format(time.RFC3339),
		"agent_id":   "OxLIVE",
		"parent_pid": os.Getpid(),
	}
	ledgerPath, _, recPath := setupRecordingSession(t, "2026-03-13T10-00-testuser-OxLIVE", recState)

	items, err := handler.Detect(ledgerPath)
	if err != nil {
		t.Fatalf("Detect failed: %v", err)
	}

	if len(items) != 0 {
		t.Errorf("expected 0 items for live PID session, got %d", len(items))
	}

	// .recording.json must still exist
	if _, statErr := os.Stat(recPath); statErr != nil {
		t.Error(".recording.json should still exist for live PID session")
	}
}

// TestDetect_LivePID_OldSession verifies that a live PID is NEVER considered
// stale, regardless of session age. The daemon waits for the PID to die before
// finalizing — false positives (PID reuse) are acceptable, premature
// finalization of an active session is not.
func TestDetect_LivePID_OldSession(t *testing.T) {
	handler := NewSessionFinalizeHandler(slog.Default())

	// live PID but started_at > 24h ago
	recState := map[string]any{
		"started_at": time.Now().Add(-48 * time.Hour).Format(time.RFC3339),
		"agent_id":   "OxLVOL",
		"parent_pid": os.Getpid(),
	}
	ledgerPath, _, recPath := setupRecordingSession(t, "2026-03-13T10-00-testuser-OxLVOL", recState)

	items, err := handler.Detect(ledgerPath)
	if err != nil {
		t.Fatalf("Detect failed: %v", err)
	}

	// live PID → not stale, even if age exceeds threshold
	if len(items) != 0 {
		t.Fatalf("expected 0 items (live PID should never be stale), got %d", len(items))
	}

	if _, statErr := os.Stat(recPath); os.IsNotExist(statErr) {
		t.Error(".recording.json should still exist (live PID = not stale)")
	}
}

// TestDetect_NoPID_Old verifies that a recording without a parent PID
// falls back to the time-based threshold: old sessions are stale.
func TestDetect_NoPID_Old(t *testing.T) {
	handler := NewSessionFinalizeHandler(slog.Default())

	recState := map[string]any{
		"started_at": time.Now().Add(-25 * time.Hour).Format(time.RFC3339),
		"agent_id":   "OxNOPO",
	}
	ledgerPath, _, recPath := setupRecordingSession(t, "2026-03-13T10-00-testuser-OxNOPO", recState)

	items, err := handler.Detect(ledgerPath)
	if err != nil {
		t.Fatalf("Detect failed: %v", err)
	}

	if len(items) != 1 {
		t.Fatalf("expected 1 stale session (no PID, old), got %d", len(items))
	}

	if _, statErr := os.Stat(recPath); !os.IsNotExist(statErr) {
		t.Error(".recording.json should have been removed for stale no-PID session")
	}
}

// TestDetect_NoPID_Recent verifies that a recording without a parent PID
// but within the time threshold is NOT considered stale.
func TestDetect_NoPID_Recent(t *testing.T) {
	handler := NewSessionFinalizeHandler(slog.Default())

	recState := map[string]any{
		"started_at": time.Now().Add(-30 * time.Minute).Format(time.RFC3339),
		"agent_id":   "OxNOPR",
	}
	ledgerPath, _, recPath := setupRecordingSession(t, "2026-03-13T10-00-testuser-OxNOPR", recState)

	items, err := handler.Detect(ledgerPath)
	if err != nil {
		t.Fatalf("Detect failed: %v", err)
	}

	if len(items) != 0 {
		t.Errorf("expected 0 items for recent no-PID session, got %d", len(items))
	}

	if _, statErr := os.Stat(recPath); statErr != nil {
		t.Error(".recording.json should still exist for recent no-PID session")
	}
}

// TestDetect_CorruptRecording verifies that a corrupt .recording.json (invalid
// JSON) falls back to mod time for staleness. We set the mod time to be old
// enough to trigger the threshold.
func TestDetect_CorruptRecording(t *testing.T) {
	handler := NewSessionFinalizeHandler(slog.Default())

	ledgerPath := t.TempDir()
	sessionName := "2026-03-13T10-00-testuser-OxCRPT"
	sessionDir := filepath.Join(ledgerPath, "sessions", sessionName)
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		t.Fatal(err)
	}

	rawContent := `{"_meta":{"schema_version":"1","agent_type":"claude-code"}}
{"type":"user","content":"hello","seq":1}
`
	if err := os.WriteFile(filepath.Join(sessionDir, "raw.jsonl"), []byte(rawContent), 0644); err != nil {
		t.Fatal(err)
	}

	// write corrupt JSON
	recPath := filepath.Join(sessionDir, recordingMarker)
	if err := os.WriteFile(recPath, []byte("{not valid json!!!"), 0644); err != nil {
		t.Fatal(err)
	}

	// backdate mod time to trigger threshold
	oldTime := time.Now().Add(-25 * time.Hour)
	if err := os.Chtimes(recPath, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}

	items, err := handler.Detect(ledgerPath)
	if err != nil {
		t.Fatalf("Detect failed: %v", err)
	}

	if len(items) != 1 {
		t.Fatalf("expected 1 stale session (corrupt JSON, old mod time), got %d", len(items))
	}

	if _, statErr := os.Stat(recPath); !os.IsNotExist(statErr) {
		t.Error(".recording.json should have been removed for stale corrupt recording")
	}
}

// TestDetect_PIDLookupFallback verifies that when .recording.json has no
// parent_pid but the pidLookup function returns a PID for the agent ID,
// that PID is used for liveness detection.
func TestDetect_PIDLookupFallback(t *testing.T) {
	t.Run("lookup returns dead PID", func(t *testing.T) {
		handler := NewSessionFinalizeHandler(slog.Default())

		pid := deadPID(t)
		handler.SetPIDLookup(func(agentID string) int {
			if agentID == "OxLKDY" {
				return pid
			}
			return 0
		})

		// no parent_pid in recording, but recent timestamp
		recState := map[string]any{
			"started_at": time.Now().Add(-5 * time.Minute).Format(time.RFC3339),
			"agent_id":   "OxLKDY",
		}
		ledgerPath, _, recPath := setupRecordingSession(t, "2026-03-13T10-00-testuser-OxLKDY", recState)

		items, err := handler.Detect(ledgerPath)
		if err != nil {
			t.Fatalf("Detect failed: %v", err)
		}

		if len(items) != 1 {
			t.Fatalf("expected 1 stale session (pidLookup dead PID), got %d", len(items))
		}

		if _, statErr := os.Stat(recPath); !os.IsNotExist(statErr) {
			t.Error(".recording.json should have been removed")
		}
	})

	t.Run("lookup returns live PID recent session", func(t *testing.T) {
		handler := NewSessionFinalizeHandler(slog.Default())

		handler.SetPIDLookup(func(agentID string) int {
			if agentID == "OxLKLV" {
				return os.Getpid()
			}
			return 0
		})

		// no parent_pid, recent timestamp + live PID from lookup = not stale
		recState := map[string]any{
			"started_at": time.Now().Add(-1 * time.Hour).Format(time.RFC3339),
			"agent_id":   "OxLKLV",
		}
		ledgerPath, _, recPath := setupRecordingSession(t, "2026-03-13T10-00-testuser-OxLKLV", recState)

		items, err := handler.Detect(ledgerPath)
		if err != nil {
			t.Fatalf("Detect failed: %v", err)
		}

		if len(items) != 0 {
			t.Errorf("expected 0 items for live PID via lookup, got %d", len(items))
		}

		if _, statErr := os.Stat(recPath); statErr != nil {
			t.Error(".recording.json should still exist")
		}
	})

	t.Run("lookup returns zero falls back to time", func(t *testing.T) {
		handler := NewSessionFinalizeHandler(slog.Default())

		handler.SetPIDLookup(func(agentID string) int {
			return 0 // unknown agent
		})

		// no parent_pid, lookup returns 0, old timestamp → stale via time
		recState := map[string]any{
			"started_at": time.Now().Add(-25 * time.Hour).Format(time.RFC3339),
			"agent_id":   "OxLKZR",
		}
		ledgerPath, _, _ := setupRecordingSession(t, "2026-03-13T10-00-testuser-OxLKZR", recState)

		items, err := handler.Detect(ledgerPath)
		if err != nil {
			t.Fatalf("Detect failed: %v", err)
		}

		if len(items) != 1 {
			t.Fatalf("expected 1 stale session (lookup zero, old), got %d", len(items))
		}
	})
}

// TestHasSubstantiveEntries verifies the guard that prevents header-only
// raw.jsonl files from being finalized. This is the regression test for the
// zero-entry upload bug: sessions with only a metadata header were being
// uploaded to the ledger, creating ghost stubs.
func TestHasSubstantiveEntries(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{
			name:    "header plus entries",
			content: "{\"metadata\":{\"agent_id\":\"Ox1\"}}\n{\"type\":\"user\",\"content\":\"hello\"}\n",
			want:    true,
		},
		{
			name:    "header only",
			content: "{\"metadata\":{\"agent_id\":\"Ox1\"}}\n",
			want:    false,
		},
		{
			name:    "empty file",
			content: "",
			want:    false,
		},
		{
			name:    "nonexistent file",
			content: "", // won't be written
			want:    false,
		},
		{
			name:    "multi-turn session",
			content: "{\"metadata\":{}}\n{\"type\":\"user\"}\n{\"type\":\"assistant\"}\n{\"type\":\"tool\"}\n",
			want:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.name == "nonexistent file" {
				if session.HasSubstantiveEntries("/nonexistent/raw.jsonl") {
					t.Error("nonexistent file should return false")
				}
				return
			}
			dir := t.TempDir()
			path := filepath.Join(dir, "raw.jsonl")
			if err := os.WriteFile(path, []byte(tt.content), 0644); err != nil {
				t.Fatal(err)
			}
			got := session.HasSubstantiveEntries(path)
			if got != tt.want {
				t.Errorf("session.HasSubstantiveEntries() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestDetect_SkipsHeaderOnlySessions verifies that Detect skips sessions where
// raw.jsonl exists but contains only the metadata header (zero substantive entries).
// These are ghost sessions created by ox session start without any actual work.
func TestDetect_SkipsHeaderOnlySessions(t *testing.T) {
	handler := NewSessionFinalizeHandler(slog.Default())

	ledgerPath := t.TempDir()
	sessionName := "2026-01-10T09-00-testuser-OxHDR0"
	sessionDir := filepath.Join(ledgerPath, "sessions", sessionName)
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		t.Fatal(err)
	}

	// raw.jsonl with ONLY the metadata header — no real session content
	headerOnly := `{"metadata":{"agent_id":"OxHDR0","agent_type":"claude","version":"1.0"},"type":"header"}` + "\n"
	if err := os.WriteFile(filepath.Join(sessionDir, "raw.jsonl"), []byte(headerOnly), 0644); err != nil {
		t.Fatal(err)
	}

	items, err := handler.Detect(ledgerPath)
	if err != nil {
		t.Fatalf("Detect failed: %v", err)
	}

	if len(items) != 0 {
		t.Errorf("expected 0 items for header-only session, got %d — "+
			"header-only sessions should be skipped to prevent ghost stubs in the ledger", len(items))
	}
}

// TestDetect_ConcurrentRemoval verifies that Detect handles gracefully the
// race condition where .recording.json disappears mid-scan (e.g., concurrent
// `ox session stop`). No panics, no corrupt results.
func TestDetect_ConcurrentRemoval(t *testing.T) {
	const iterations = 50

	for i := 0; i < iterations; i++ {
		ledgerPath := t.TempDir()
		sessionName := "2026-03-13T10-00-testuser-OxRACE"
		sessionDir := filepath.Join(ledgerPath, "sessions", sessionName)
		if err := os.MkdirAll(sessionDir, 0755); err != nil {
			t.Fatal(err)
		}

		rawContent := `{"_meta":{"schema_version":"1","agent_type":"claude-code"}}
{"type":"user","content":"hello","seq":1}
`
		if err := os.WriteFile(filepath.Join(sessionDir, "raw.jsonl"), []byte(rawContent), 0644); err != nil {
			t.Fatal(err)
		}

		// write a stale recording that Detect will try to process
		recState := map[string]any{
			"started_at": time.Now().Add(-25 * time.Hour).Format(time.RFC3339),
			"agent_id":   "OxRACE",
		}
		recData, _ := json.Marshal(recState)
		recPath := filepath.Join(sessionDir, recordingMarker)
		if err := os.WriteFile(recPath, recData, 0644); err != nil {
			t.Fatal(err)
		}

		handler := NewSessionFinalizeHandler(slog.Default())

		var wg sync.WaitGroup
		wg.Add(2)

		var detectErr error
		var detectItems []*WorkItem

		// goroutine 1: run Detect
		go func() {
			defer wg.Done()
			detectItems, detectErr = handler.Detect(ledgerPath)
		}()

		// goroutine 2: remove .recording.json concurrently
		go func() {
			defer wg.Done()
			// small jitter to increase chance of hitting the race window
			os.Remove(recPath)
		}()

		wg.Wait()

		if detectErr != nil {
			t.Fatalf("iteration %d: Detect returned error: %v", i, detectErr)
		}

		// valid outcomes: 0 items (marker gone before Detect read it, so session
		// looks like a normal incomplete session OR Detect read it but Remove
		// failed because it was already gone) or 1 item (Detect processed it
		// before removal). Both are fine — the key is no panic or error.
		if len(detectItems) > 1 {
			t.Fatalf("iteration %d: expected 0 or 1 items, got %d", i, len(detectItems))
		}
	}
}

// writeClaudeCodeJSONL creates a Claude Code JSONL source file with entries at
// the given timestamps. Each timestamp gets a user+assistant pair.
func writeClaudeCodeJSONL(t *testing.T, path string, timestamps []time.Time) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	enc := json.NewEncoder(f)
	for i, ts := range timestamps {
		// user message
		user := map[string]any{
			"type":      "user",
			"timestamp": ts.Format(time.RFC3339),
			"message": map[string]any{
				"role":    "user",
				"content": "question " + strings.Repeat("x", i),
			},
		}
		if err := enc.Encode(user); err != nil {
			t.Fatal(err)
		}

		// assistant message
		assistant := map[string]any{
			"type":      "assistant",
			"timestamp": ts.Add(2 * time.Second).Format(time.RFC3339),
			"message": map[string]any{
				"role": "assistant",
				"content": []map[string]any{
					{"type": "text", "text": "answer " + strings.Repeat("y", i)},
				},
			},
		}
		if err := enc.Encode(assistant); err != nil {
			t.Fatal(err)
		}
	}
}

// writeRecordingState writes a .recording.json with the given state fields.
func writeRecordingState(t *testing.T, recPath string, state session.RecordingState) {
	t.Helper()
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(recPath, data, 0644); err != nil {
		t.Fatal(err)
	}
}

// countRawJSONLEntries counts non-header lines in a raw.jsonl file.
func countRawJSONLEntries(t *testing.T, rawPath string) int {
	t.Helper()
	f, err := os.Open(rawPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	count := 0
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(line, &m); err != nil {
			continue
		}
		if _, hasMeta := m["_meta"]; hasMeta {
			continue // skip header
		}
		count++
	}
	return count
}

func TestRecoverRawFromSessionFile(t *testing.T) {
	logger := slog.Default()

	t.Run("recovers_from_valid_session_file", func(t *testing.T) {
		sessionDir := t.TempDir()
		recPath := filepath.Join(sessionDir, recordingMarker)
		rawPath := filepath.Join(sessionDir, artifactRaw)

		// create source JSONL file
		sourceFile := filepath.Join(t.TempDir(), "session.jsonl")
		startedAt := time.Now().Add(-2 * time.Hour).Truncate(time.Second)
		writeClaudeCodeJSONL(t, sourceFile, []time.Time{
			startedAt.Add(1 * time.Minute),
			startedAt.Add(5 * time.Minute),
		})

		writeRecordingState(t, recPath, session.RecordingState{
			AgentID:     "OxRCVR",
			AdapterName: "claude-code",
			SessionFile: sourceFile,
			StartedAt:   startedAt,
		})

		result := recoverRawFromSessionFile(logger, recPath, sessionDir, rawPath)
		if !result {
			t.Fatal("expected recovery to succeed")
		}

		// raw.jsonl should exist
		if _, err := os.Stat(rawPath); err != nil {
			t.Fatalf("raw.jsonl not created: %v", err)
		}

		// should contain entries (2 timestamps * 2 entries each = 4 entries)
		count := countRawJSONLEntries(t, rawPath)
		if count != 4 {
			t.Errorf("expected 4 entries in raw.jsonl, got %d", count)
		}

		// verify header has recovered=true
		f, _ := os.Open(rawPath)
		defer f.Close()
		scanner := bufio.NewScanner(f)
		if scanner.Scan() {
			var header map[string]any
			if err := json.Unmarshal(scanner.Bytes(), &header); err == nil {
				meta, _ := header["_meta"].(map[string]any)
				if meta == nil {
					t.Error("header missing _meta")
				} else if recovered, _ := meta["recovered"].(bool); !recovered {
					t.Error("header _meta should have recovered=true")
				}
			}
		}
	})

	t.Run("empty_session_file", func(t *testing.T) {
		sessionDir := t.TempDir()
		recPath := filepath.Join(sessionDir, recordingMarker)
		rawPath := filepath.Join(sessionDir, artifactRaw)

		sourceFile := filepath.Join(t.TempDir(), "empty.jsonl")
		if err := os.WriteFile(sourceFile, nil, 0644); err != nil {
			t.Fatal(err)
		}

		writeRecordingState(t, recPath, session.RecordingState{
			AgentID:     "OxEMPT",
			AdapterName: "claude-code",
			SessionFile: sourceFile,
			StartedAt:   time.Now().Add(-2 * time.Hour),
		})

		if recoverRawFromSessionFile(logger, recPath, sessionDir, rawPath) {
			t.Error("expected recovery to fail for empty session file")
		}
	})

	t.Run("missing_session_file", func(t *testing.T) {
		sessionDir := t.TempDir()
		recPath := filepath.Join(sessionDir, recordingMarker)
		rawPath := filepath.Join(sessionDir, artifactRaw)

		writeRecordingState(t, recPath, session.RecordingState{
			AgentID:     "OxMISS",
			AdapterName: "claude-code",
			SessionFile: "/nonexistent/path/session.jsonl",
			StartedAt:   time.Now().Add(-2 * time.Hour),
		})

		if recoverRawFromSessionFile(logger, recPath, sessionDir, rawPath) {
			t.Error("expected recovery to fail for missing session file")
		}
	})

	t.Run("no_session_file_in_state", func(t *testing.T) {
		sessionDir := t.TempDir()
		recPath := filepath.Join(sessionDir, recordingMarker)
		rawPath := filepath.Join(sessionDir, artifactRaw)

		writeRecordingState(t, recPath, session.RecordingState{
			AgentID:     "OxNOSF",
			AdapterName: "claude-code",
			StartedAt:   time.Now().Add(-2 * time.Hour),
		})

		if recoverRawFromSessionFile(logger, recPath, sessionDir, rawPath) {
			t.Error("expected recovery to fail when no session file in state")
		}
	})

	t.Run("invalid_recording_json", func(t *testing.T) {
		sessionDir := t.TempDir()
		recPath := filepath.Join(sessionDir, recordingMarker)
		rawPath := filepath.Join(sessionDir, artifactRaw)

		if err := os.WriteFile(recPath, []byte("{not valid json!!!"), 0644); err != nil {
			t.Fatal(err)
		}

		if recoverRawFromSessionFile(logger, recPath, sessionDir, rawPath) {
			t.Error("expected recovery to fail for invalid recording JSON")
		}
	})

	t.Run("unknown_adapter", func(t *testing.T) {
		sessionDir := t.TempDir()
		recPath := filepath.Join(sessionDir, recordingMarker)
		rawPath := filepath.Join(sessionDir, artifactRaw)

		sourceFile := filepath.Join(t.TempDir(), "session.jsonl")
		writeClaudeCodeJSONL(t, sourceFile, []time.Time{time.Now()})

		writeRecordingState(t, recPath, session.RecordingState{
			AgentID:     "OxUNKN",
			AdapterName: "nonexistent",
			SessionFile: sourceFile,
			StartedAt:   time.Now().Add(-2 * time.Hour),
		})

		if recoverRawFromSessionFile(logger, recPath, sessionDir, rawPath) {
			t.Error("expected recovery to fail for unknown adapter")
		}
	})

	t.Run("filters_entries_by_start_time", func(t *testing.T) {
		sessionDir := t.TempDir()
		recPath := filepath.Join(sessionDir, recordingMarker)
		rawPath := filepath.Join(sessionDir, artifactRaw)

		sourceFile := filepath.Join(t.TempDir(), "session.jsonl")
		startedAt := time.Date(2026, 1, 10, 10, 0, 0, 0, time.UTC)
		writeClaudeCodeJSONL(t, sourceFile, []time.Time{
			startedAt.Add(-10 * time.Minute), // before start — should be filtered
			startedAt.Add(-5 * time.Minute),  // before start — should be filtered
			startedAt.Add(1 * time.Minute),   // after start — should be kept
			startedAt.Add(5 * time.Minute),   // after start — should be kept
		})

		writeRecordingState(t, recPath, session.RecordingState{
			AgentID:     "OxFLTR",
			AdapterName: "claude-code",
			SessionFile: sourceFile,
			StartedAt:   startedAt,
		})

		result := recoverRawFromSessionFile(logger, recPath, sessionDir, rawPath)
		if !result {
			t.Fatal("expected recovery to succeed")
		}

		// 2 timestamps after start * 2 entries each = 4 entries
		count := countRawJSONLEntries(t, rawPath)
		if count != 4 {
			t.Errorf("expected 4 entries after filtering, got %d", count)
		}
	})

	t.Run("no_entries_after_start_time", func(t *testing.T) {
		sessionDir := t.TempDir()
		recPath := filepath.Join(sessionDir, recordingMarker)
		rawPath := filepath.Join(sessionDir, artifactRaw)

		sourceFile := filepath.Join(t.TempDir(), "session.jsonl")
		startedAt := time.Date(2026, 1, 10, 12, 0, 0, 0, time.UTC)
		writeClaudeCodeJSONL(t, sourceFile, []time.Time{
			startedAt.Add(-10 * time.Minute), // all before start
			startedAt.Add(-5 * time.Minute),
		})

		writeRecordingState(t, recPath, session.RecordingState{
			AgentID:     "OxNOAF",
			AdapterName: "claude-code",
			SessionFile: sourceFile,
			StartedAt:   startedAt,
		})

		if recoverRawFromSessionFile(logger, recPath, sessionDir, rawPath) {
			t.Error("expected recovery to fail when all entries are before start time")
		}
	})
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
			Missing:    []string{"summary.md", "summary.json", "session.html", "session.md"},
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
			Missing:    []string{"summary.md", "summary.json", "session.html", "session.md"},
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

func TestSetQualityThresholds(t *testing.T) {
	handler := NewSessionFinalizeHandler(slog.Default())

	// defaults
	if handler.qualityUploadThreshold != 0.3 {
		t.Errorf("expected default upload threshold 0.3, got %f", handler.qualityUploadThreshold)
	}
	if handler.qualityDiscardThreshold != 0.1 {
		t.Errorf("expected default discard threshold 0.1, got %f", handler.qualityDiscardThreshold)
	}

	// custom
	handler.SetQualityThresholds(0.6, 0.2)
	if handler.qualityUploadThreshold != 0.6 {
		t.Errorf("expected upload threshold 0.6, got %f", handler.qualityUploadThreshold)
	}
	if handler.qualityDiscardThreshold != 0.2 {
		t.Errorf("expected discard threshold 0.2, got %f", handler.qualityDiscardThreshold)
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

func TestCtrlC_FullFinalizationPipeline_WritesMetaJSON(t *testing.T) {
	// strengthened version of TestCtrlC_FullFinalizationPipeline
	// that also verifies meta.json is written with correct fields
	handler := NewSessionFinalizeHandlerForTest(slog.Default())

	ledgerPath := t.TempDir()
	sessionName := "2026-01-10T11-00-testuser-OxMETA"
	sessionDir := filepath.Join(ledgerPath, "sessions", sessionName)
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		t.Fatal(err)
	}

	rawContent := `{"metadata":{"agent_id":"OxMETA","agent_type":"claude-code","created_at":"2026-01-10T11:00:00Z"},"type":"header"}
{"type":"user","content":"implement feature X","seq":0}
{"type":"assistant","content":"done implementing","seq":1}
`
	rawPath := filepath.Join(sessionDir, "raw.jsonl")
	if err := os.WriteFile(rawPath, []byte(rawContent), 0644); err != nil {
		t.Fatal(err)
	}

	// stale recording marker
	recState := map[string]any{
		"started_at": time.Now().Add(-26 * time.Hour).Format(time.RFC3339),
		"agent_id":   "OxMETA",
	}
	recData, _ := json.Marshal(recState)
	if err := os.WriteFile(filepath.Join(sessionDir, recordingMarker), recData, 0644); err != nil {
		t.Fatal(err)
	}

	// step 1: detect
	items, err := handler.Detect(ledgerPath)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 stale session, got %d", len(items))
	}

	// step 2: build prompt
	item := items[0]
	if _, err := handler.BuildPrompt(item); err != nil {
		t.Fatalf("BuildPrompt: %v", err)
	}

	// step 3: process result
	result := &RunResult{
		Output: `{"title":"Feature X Implementation","summary":"Implemented feature X","key_actions":["implemented feature"],"outcome":"success","topics_found":["feature"],"quality_score":0.9,"score_reason":"Full feature implementation"}`,
	}
	if err := handler.ProcessResult(item, result); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	// step 4: verify artifacts + meta.json
	for _, artifact := range requiredArtifacts {
		path := filepath.Join(sessionDir, artifact)
		if _, statErr := os.Stat(path); statErr != nil {
			t.Errorf("missing artifact: %s", artifact)
		}
	}

	meta, err := lfs.ReadSessionMeta(sessionDir)
	if err != nil {
		t.Fatalf("meta.json not found after full pipeline: %v", err)
	}

	if meta.SessionName != sessionName {
		t.Errorf("session_name: got %q, want %q", meta.SessionName, sessionName)
	}
	if meta.StopReason != session.StopReasonRecovered {
		t.Errorf("stop_reason: got %q, want %q", meta.StopReason, session.StopReasonRecovered)
	}
	if meta.Title != "Feature X Implementation" {
		t.Errorf("title: got %q, want %q", meta.Title, "Feature X Implementation")
	}
	if meta.EntryCount != 2 {
		t.Errorf("entry_count: got %d, want 2", meta.EntryCount)
	}
	if meta.AgentID != "OxMETA" {
		t.Errorf("agent_id: got %q, want %q", meta.AgentID, "OxMETA")
	}
}
