package agentwork

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/session"
)

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

	// Verify session.md contains session content
	mdData, err := os.ReadFile(filepath.Join(sessionDir, "session.md"))
	if err != nil {
		t.Fatalf("failed to read session.md: %v", err)
	}
	if len(mdData) < 20 {
		t.Error("session.md seems too small")
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
