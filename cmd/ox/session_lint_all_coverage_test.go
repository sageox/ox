package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestLintAllSessions_EmptyDir(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	err := lintAllSessions(io.Discard, dir, true)
	if err != nil {
		t.Errorf("lintAllSessions on empty dir: %v", err)
	}
}

func TestLintAllSessions_NonexistentDir(t *testing.T) {
	t.Parallel()

	err := lintAllSessions(io.Discard, "/nonexistent/sessions/dir", true)
	if err == nil {
		t.Error("expected error for nonexistent dir")
	}
}

func TestLintAllSessions_WithValidSession(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	sessionDir := filepath.Join(dir, "2026-03-11T10-00-testuser-OxABC1")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}

	content := `{"type":"user","content":"hello","ts":"2026-03-11T10:00:00Z","seq":1}
{"type":"assistant","content":"hi there","ts":"2026-03-11T10:00:01Z","seq":2}
`
	if err := os.WriteFile(filepath.Join(sessionDir, "raw.jsonl"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	// should not error; we capture output via JSON mode
	err := lintAllSessions(io.Discard, dir, true)
	if err != nil {
		t.Errorf("lintAllSessions with valid session: %v", err)
	}
}

func TestLintAllSessions_SkipsDirsWithoutRawJSONL(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	// session dir without raw.jsonl
	sessionDir := filepath.Join(dir, "2026-03-11T10-00-testuser-OxABC2")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// only a summary file
	if err := os.WriteFile(filepath.Join(sessionDir, "summary.md"), []byte("summary"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := lintAllSessions(io.Discard, dir, true)
	if err != nil {
		t.Errorf("lintAllSessions should skip dirs without raw.jsonl: %v", err)
	}
}

func TestPrintLintResults_JSONOutput(t *testing.T) {
	t.Parallel()

	results := []lintResult{
		{
			SessionName: "test-session",
			Valid:       true,
			EntryCount:  5,
			TypeCounts:  map[string]int{"user": 2, "assistant": 3},
			HasHeader:   true,
			HasFooter:   true,
		},
	}

	// printLintResults writes to stdout; just verify it doesn't panic
	// with JSON output mode
	err := printLintResults(io.Discard, results, true)
	if err != nil {
		t.Errorf("printLintResults (json): %v", err)
	}
}

func TestPrintLintResults_TextOutput(t *testing.T) {
	t.Parallel()

	results := []lintResult{
		{
			SessionName: "test-session",
			Valid:       true,
			EntryCount:  5,
			TypeCounts:  map[string]int{"user": 2, "assistant": 3},
		},
		{
			SessionName: "bad-session",
			Valid:       false,
			Errors:      []string{"invalid JSON on line 5"},
			Warnings:    []string{"missing timestamp on line 3"},
			EntryCount:  1,
			TypeCounts:  map[string]int{"user": 1},
		},
	}

	err := printLintResults(io.Discard, results, false)
	// printLintResults returns "lint failed" error when any result is invalid
	if err == nil {
		t.Error("expected error when results contain invalid sessions")
	}
}

func TestLintResult_JSONSerialization(t *testing.T) {
	t.Parallel()

	result := lintResult{
		SessionName: "test",
		Valid:       true,
		Errors:      []string{},
		Warnings:    []string{"warn1"},
		EntryCount:  3,
		TypeCounts:  map[string]int{"user": 1, "assistant": 2},
		HasHeader:   true,
		HasFooter:   false,
	}

	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}

	var decoded lintResult
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}

	if decoded.SessionName != "test" {
		t.Errorf("session_name = %q, want 'test'", decoded.SessionName)
	}
	if !decoded.Valid {
		t.Error("expected valid=true")
	}
	if decoded.EntryCount != 3 {
		t.Errorf("entry_count = %d, want 3", decoded.EntryCount)
	}
	if !decoded.HasHeader {
		t.Error("expected has_header=true")
	}
	if decoded.HasFooter {
		t.Error("expected has_footer=false")
	}
}
