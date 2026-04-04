package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// --- A. Line parsing ---

// TestParseAmpLine_User verifies user messages parse into RawEntry.
// Failure prevented: user messages silently dropped from sessions.
func TestParseAmpLine_User(t *testing.T) {
	line := []byte(`{"type":"user","timestamp":"2024-01-15T10:00:00Z","content":"hello"}`)
	entry := parseAmpLine(line)
	if entry == nil {
		t.Fatal("expected entry, got nil")
	}
	if entry.Role != "user" {
		t.Errorf("role = %q, want user", entry.Role)
	}
	if entry.Content != "hello" {
		t.Errorf("content = %q, want hello", entry.Content)
	}
}

// TestParseAmpLine_Assistant verifies assistant messages parse correctly.
// Failure prevented: assistant responses missing from session playback.
func TestParseAmpLine_Assistant(t *testing.T) {
	line := []byte(`{"type":"assistant","timestamp":"2024-01-15T10:00:01Z","content":"hi there"}`)
	entry := parseAmpLine(line)
	if entry == nil {
		t.Fatal("expected entry, got nil")
	}
	if entry.Role != "assistant" {
		t.Errorf("role = %q, want assistant", entry.Role)
	}
	if entry.Content != "hi there" {
		t.Errorf("content = %q, want 'hi there'", entry.Content)
	}
}

// TestParseAmpLine_ToolUse verifies tool_use entries with name, input, call_id.
// Failure prevented: tool usage not captured in session data.
func TestParseAmpLine_ToolUse(t *testing.T) {
	line := []byte(`{"type":"tool_use","timestamp":"2024-01-15T10:00:02Z","tool_name":"read_file","tool_input":"path.go","call_id":"call-1"}`)
	entry := parseAmpLine(line)
	if entry == nil {
		t.Fatal("expected entry, got nil")
	}
	if entry.Role != "tool" {
		t.Errorf("role = %q, want tool", entry.Role)
	}
	if entry.ToolName != "read_file" {
		t.Errorf("tool_name = %q, want read_file", entry.ToolName)
	}
	if entry.ToolInput != "path.go" {
		t.Errorf("tool_input = %q, want path.go", entry.ToolInput)
	}
	if entry.CallID != "call-1" {
		t.Errorf("call_id = %q, want call-1", entry.CallID)
	}
}

// TestParseAmpLine_ToolResultError verifies error tool results are captured.
// Failure prevented: error signals from tools silently swallowed.
func TestParseAmpLine_ToolResultError(t *testing.T) {
	line := []byte(`{"type":"tool_result","timestamp":"2024-01-15T10:00:03Z","content":"file not found","is_error":true,"call_id":"call-1"}`)
	entry := parseAmpLine(line)
	if entry == nil {
		t.Fatal("expected entry, got nil")
	}
	if entry.ToolOutput != "file not found" {
		t.Errorf("tool_output = %q, want 'file not found'", entry.ToolOutput)
	}
	if !entry.IsError {
		t.Error("is_error = false, want true")
	}
	if entry.CallID != "call-1" {
		t.Errorf("call_id = %q, want call-1", entry.CallID)
	}
}

// TestParseAmpLine_EmptyContent verifies empty-content messages return nil.
// Failure prevented: empty entries polluting session data.
func TestParseAmpLine_EmptyContent(t *testing.T) {
	line := []byte(`{"type":"user","timestamp":"2024-01-15T10:00:00Z","content":""}`)
	entry := parseAmpLine(line)
	if entry != nil {
		t.Error("expected nil for empty content")
	}
}

// TestParseAmpLine_InvalidJSON verifies malformed lines return nil.
// Failure prevented: panic on corrupt session data.
func TestParseAmpLine_InvalidJSON(t *testing.T) {
	entry := parseAmpLine([]byte(`not json`))
	if entry != nil {
		t.Error("expected nil for invalid JSON")
	}
}

// --- B. File reading ---

// TestReadAmpFile reads a multi-line JSONL file and verifies all entries parsed.
// Failure prevented: partial session reads on multi-entry files.
func TestReadAmpFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")

	content := `{"type":"user","timestamp":"2024-01-15T10:00:00Z","content":"first"}
{"type":"assistant","timestamp":"2024-01-15T10:00:01Z","content":"second"}
{"type":"user","timestamp":"2024-01-15T10:00:02Z","content":"third"}
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	entries, err := readAmpFile(path)
	if err != nil {
		t.Fatalf("readAmpFile: %v", err)
	}
	if len(entries) != 3 {
		t.Errorf("got %d entries, want 3", len(entries))
	}
}

// TestReadAmpFromOffset reads from a byte offset and returns new entries.
// Failure prevented: duplicate entries on incremental reads.
func TestReadAmpFromOffset(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")

	line1 := `{"type":"user","timestamp":"2024-01-15T10:00:00Z","content":"first"}` + "\n"
	line2 := `{"type":"assistant","timestamp":"2024-01-15T10:00:01Z","content":"second"}` + "\n"

	if err := os.WriteFile(path, []byte(line1+line2), 0o644); err != nil {
		t.Fatal(err)
	}

	// read from offset past first line
	offset := int64(len(line1))
	entries, newOffset, err := readAmpFromOffset(path, offset)
	if err != nil {
		t.Fatalf("readAmpFromOffset: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("got %d entries from offset, want 1", len(entries))
	}
	if newOffset <= offset {
		t.Errorf("newOffset %d should be > offset %d", newOffset, offset)
	}
}

// --- C. Session discovery ---

// TestFindAmpSession_MostRecent verifies newest session file is returned.
// Failure prevented: stale session returned instead of active one.
func TestFindAmpSession_MostRecent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	sessionsDir := filepath.Join(home, ".amp", "sessions")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	older := filepath.Join(sessionsDir, "old.jsonl")
	newer := filepath.Join(sessionsDir, "new.jsonl")

	data := `{"type":"user","timestamp":"2024-01-15T10:00:00Z","content":"test"}` + "\n"
	if err := os.WriteFile(older, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-1 * time.Hour)
	if err := os.Chtimes(older, past, past); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newer, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := findAmpSession("", "", "", "")
	if err != nil {
		t.Fatalf("findAmpSession: %v", err)
	}
	if got != newer {
		t.Errorf("got %q, want %q (most recent)", got, newer)
	}
}

// TestFindAmpSession_NoSessions verifies error when no sessions exist.
// Failure prevented: nil pointer on empty sessions directory.
func TestFindAmpSession_NoSessions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	sessionsDir := filepath.Join(home, ".amp", "sessions")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := findAmpSession("", "", "", "")
	if err == nil {
		t.Error("expected error for no sessions")
	}
}
