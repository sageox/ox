package agentwork

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
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
		"title":        "Test Session",
		"summary":      "A test session.",
		"key_actions":  []string{"said hello"},
		"outcome":      "success",
		"topics_found": []string{"testing"},
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

	// verify summary.md was written
	summaryMDPath := filepath.Join(sessionDir, "summary.md")
	if _, statErr := os.Stat(summaryMDPath); statErr != nil {
		t.Errorf("summary.md not created: %v", statErr)
	} else {
		content, _ := os.ReadFile(summaryMDPath)
		if string(content) != llmOutput {
			t.Error("summary.md content mismatch")
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

	// summary.md should still be written
	if _, statErr := os.Stat(filepath.Join(sessionDir, "summary.md")); statErr != nil {
		t.Error("summary.md should be created even when JSON parsing fails")
	}

	// summary.json should NOT be written
	if _, statErr := os.Stat(filepath.Join(sessionDir, "summary.json")); statErr == nil {
		t.Error("summary.json should not be created when JSON parsing fails")
	}

	// html and md should still be generated (they don't depend on the summary)
	if _, statErr := os.Stat(filepath.Join(sessionDir, "session.html")); statErr != nil {
		t.Error("session.html should be created even without summary")
	}
	if _, statErr := os.Stat(filepath.Join(sessionDir, "session.md")); statErr != nil {
		t.Error("session.md should be created even without summary")
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
