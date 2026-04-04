package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/pkg/adapterprotocol"
)

// --- A. Line parsing ---

// TestParsePiLine_User verifies user messages parse into RawEntry.
// Failure prevented: user messages silently dropped from sessions.
func TestParsePiLine_User(t *testing.T) {
	line := []byte(`{"type":"user","timestamp":"2024-01-15T10:00:00Z","content":"hello"}`)
	entry := parsePiLine(line)
	if entry == nil {
		t.Fatal("expected entry, got nil")
	}
	if entry.Role != adapterprotocol.RoleUser {
		t.Errorf("role = %q, want %s", entry.Role, adapterprotocol.RoleUser)
	}
	if entry.Content != "hello" {
		t.Errorf("content = %q, want hello", entry.Content)
	}
}

// TestParsePiLine_Assistant verifies assistant messages parse correctly.
// Failure prevented: assistant responses missing from session playback.
func TestParsePiLine_Assistant(t *testing.T) {
	line := []byte(`{"type":"assistant","timestamp":"2024-01-15T10:00:01Z","content":"hi there"}`)
	entry := parsePiLine(line)
	if entry == nil {
		t.Fatal("expected entry, got nil")
	}
	if entry.Role != adapterprotocol.RoleAssistant {
		t.Errorf("role = %q, want %s", entry.Role, adapterprotocol.RoleAssistant)
	}
	if entry.Content != "hi there" {
		t.Errorf("content = %q, want 'hi there'", entry.Content)
	}
}

// TestParsePiLine_ToolCall verifies tool_call entries with name, input, call_id.
// Failure prevented: tool usage not captured in session data.
func TestParsePiLine_ToolCall(t *testing.T) {
	line := []byte(`{"type":"tool_call","timestamp":"2024-01-15T10:00:02Z","name":"read_file","input":"path.go","call_id":"call-1"}`)
	entry := parsePiLine(line)
	if entry == nil {
		t.Fatal("expected entry, got nil")
	}
	if entry.Role != adapterprotocol.RoleTool {
		t.Errorf("role = %q, want %s", entry.Role, adapterprotocol.RoleTool)
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

// TestParsePiLine_ToolResultError verifies error tool results are captured.
// Failure prevented: error signals from tools silently swallowed.
func TestParsePiLine_ToolResultError(t *testing.T) {
	line := []byte(`{"type":"tool_result","timestamp":"2024-01-15T10:00:03Z","content":"file not found","is_error":true,"call_id":"call-1"}`)
	entry := parsePiLine(line)
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

// TestParsePiLine_ToolResultSuccess verifies successful tool results are captured.
// Failure prevented: successful tool outputs missing from session data.
func TestParsePiLine_ToolResultSuccess(t *testing.T) {
	line := []byte(`{"type":"tool_result","timestamp":"2024-01-15T10:00:03Z","content":"success output","is_error":false,"call_id":"call-2"}`)
	entry := parsePiLine(line)
	if entry == nil {
		t.Fatal("expected entry, got nil")
	}
	if entry.ToolOutput != "success output" {
		t.Errorf("tool_output = %q, want 'success output'", entry.ToolOutput)
	}
	if entry.IsError {
		t.Error("is_error = true, want false")
	}
	if entry.CallID != "call-2" {
		t.Errorf("call_id = %q, want call-2", entry.CallID)
	}
}

// TestParsePiLine_System verifies system messages parse correctly.
// Failure prevented: system context missing from session data.
func TestParsePiLine_System(t *testing.T) {
	line := []byte(`{"type":"system","timestamp":"2024-01-15T10:00:04Z","content":"context loaded"}`)
	entry := parsePiLine(line)
	if entry == nil {
		t.Fatal("expected entry, got nil")
	}
	if entry.Role != adapterprotocol.RoleSystem {
		t.Errorf("role = %q, want %s", entry.Role, adapterprotocol.RoleSystem)
	}
	if entry.Content != "context loaded" {
		t.Errorf("content = %q, want 'context loaded'", entry.Content)
	}
}

// TestParsePiLine_EmptyContent verifies empty-content messages return nil.
// Failure prevented: empty entries polluting session data.
func TestParsePiLine_EmptyContent(t *testing.T) {
	tests := []struct {
		name string
		line string
	}{
		{
			name: "user empty content",
			line: `{"type":"user","timestamp":"2024-01-15T10:00:00Z","content":""}`,
		},
		{
			name: "assistant empty content",
			line: `{"type":"assistant","timestamp":"2024-01-15T10:00:01Z","content":""}`,
		},
		{
			name: "system empty content",
			line: `{"type":"system","timestamp":"2024-01-15T10:00:02Z","content":""}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entry := parsePiLine([]byte(tt.line))
			if entry != nil {
				t.Error("expected nil for empty content")
			}
		})
	}
}

// TestParsePiLine_InvalidJSON verifies malformed lines return nil.
// Failure prevented: panic on corrupt session data.
func TestParsePiLine_InvalidJSON(t *testing.T) {
	entry := parsePiLine([]byte(`not json`))
	if entry != nil {
		t.Error("expected nil for invalid JSON")
	}
}

// TestParsePiLine_SessionHeader verifies session headers are skipped.
// Failure prevented: metadata entries appearing as conversation content.
func TestParsePiLine_SessionHeader(t *testing.T) {
	line := []byte(`{"type":"session","id":"test-session","version":1,"model":"claude-3"}`)
	entry := parsePiLine(line)
	if entry != nil {
		t.Error("expected nil for session header")
	}
}

// TestParsePiLine_UnknownType verifies unknown types return nil gracefully.
// Failure prevented: panic on new/unknown message types.
func TestParsePiLine_UnknownType(t *testing.T) {
	line := []byte(`{"type":"unknown","timestamp":"2024-01-15T10:00:00Z","content":"test"}`)
	entry := parsePiLine(line)
	if entry != nil {
		t.Error("expected nil for unknown type")
	}
}

// --- B. File reading ---

// TestReadPiFile reads a multi-line JSONL file and verifies all entries parsed.
// Failure prevented: partial session reads on multi-entry files.
func TestReadPiFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")

	content := `{"type":"user","timestamp":"2024-01-15T10:00:00Z","content":"first"}
{"type":"assistant","timestamp":"2024-01-15T10:00:01Z","content":"second"}
{"type":"user","timestamp":"2024-01-15T10:00:02Z","content":"third"}
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	entries, err := readPiFile(path)
	if err != nil {
		t.Fatalf("readPiFile: %v", err)
	}
	if len(entries) != 3 {
		t.Errorf("got %d entries, want 3", len(entries))
	}
}

// TestReadPiFile_SkipsEmptyLinesAndHeaders reads file with empty lines and session headers.
// Failure prevented: empty lines or metadata causing parsing failures.
func TestReadPiFile_SkipsEmptyLinesAndHeaders(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")

	content := `{"type":"session","id":"test-session","version":1}

{"type":"user","timestamp":"2024-01-15T10:00:00Z","content":"hello"}

{"type":"assistant","timestamp":"2024-01-15T10:00:01Z","content":"response"}
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	entries, err := readPiFile(path)
	if err != nil {
		t.Fatalf("readPiFile: %v", err)
	}
	if len(entries) != 2 {
		t.Errorf("got %d entries, want 2 (should skip session header and empty lines)", len(entries))
	}
}

// TestReadPiFromOffset reads from a byte offset and returns new entries.
// Failure prevented: duplicate entries on incremental reads.
func TestReadPiFromOffset(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")

	line1 := `{"type":"user","timestamp":"2024-01-15T10:00:00Z","content":"first"}` + "\n"
	line2 := `{"type":"assistant","timestamp":"2024-01-15T10:00:01Z","content":"second"}` + "\n"

	if err := os.WriteFile(path, []byte(line1+line2), 0o644); err != nil {
		t.Fatal(err)
	}

	// read from offset past first line
	offset := int64(len(line1))
	entries, newOffset, err := readPiFromOffset(path, offset)
	if err != nil {
		t.Fatalf("readPiFromOffset: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("got %d entries from offset, want 1", len(entries))
	}
	if newOffset <= offset {
		t.Errorf("newOffset %d should be > offset %d", newOffset, offset)
	}
}

// TestReadPiFromOffset_ZeroOffset reads entire file when offset is 0.
// Failure prevented: initial read missing content when offset is explicitly 0.
func TestReadPiFromOffset_ZeroOffset(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")

	content := `{"type":"user","timestamp":"2024-01-15T10:00:00Z","content":"first"}
{"type":"assistant","timestamp":"2024-01-15T10:00:01Z","content":"second"}
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	entries, newOffset, err := readPiFromOffset(path, 0)
	if err != nil {
		t.Fatalf("readPiFromOffset: %v", err)
	}
	if len(entries) != 2 {
		t.Errorf("got %d entries from zero offset, want 2", len(entries))
	}
	if newOffset == 0 {
		t.Error("newOffset should be > 0 after reading content")
	}
}

// --- C. Session discovery ---

// TestFindPiSession_MostRecent verifies newest session file is returned.
// Failure prevented: stale session returned instead of active one.
func TestFindPiSession_MostRecent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Create Pi sessions directory structure
	projectDir := filepath.Join(home, ".pi", "agent", "sessions", "--test--project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}

	older := filepath.Join(projectDir, "old-session.jsonl")
	newer := filepath.Join(projectDir, "new-session.jsonl")

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

	got, err := findPiSession("/test/project", "", "", "")
	if err != nil {
		t.Fatalf("findPiSession: %v", err)
	}
	if got != newer {
		t.Errorf("got %q, want %q (most recent)", got, newer)
	}
}

// TestFindPiSession_NoSessions verifies error when no sessions exist.
// Failure prevented: nil pointer on empty sessions directory.
func TestFindPiSession_NoSessions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Create empty sessions directory
	sessionsDir := filepath.Join(home, ".pi", "agent", "sessions")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := findPiSession("", "", "", "")
	if err == nil {
		t.Error("expected error for no sessions")
	}
}

// TestFindPiSession_BySessionID verifies direct session ID lookup works.
// Failure prevented: session ID lookup failing when session exists.
func TestFindPiSession_BySessionID(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Create sessions directory with specific session file
	projectDir := filepath.Join(home, ".pi", "agent", "sessions", "--test--project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}

	sessionFile := filepath.Join(projectDir, "specific-session-id.jsonl")
	data := `{"type":"user","timestamp":"2024-01-15T10:00:00Z","content":"test"}` + "\n"
	if err := os.WriteFile(sessionFile, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := findPiSession("", "", "", "specific-session-id")
	if err != nil {
		t.Fatalf("findPiSession by session ID: %v", err)
	}
	if got != sessionFile {
		t.Errorf("got %q, want %q", got, sessionFile)
	}
}

// --- D. Path conversion ---

// TestCwdToDirName verifies working directory conversion to Pi session dir names.
// Failure prevented: incorrect path mapping causing session lookup failures.
func TestCwdToDirName(t *testing.T) {
	tests := []struct {
		name string
		cwd  string
		want string
	}{
		{
			name: "simple path",
			cwd:  "/Users/dev/project",
			want: "--Users--dev--project",
		},
		{
			name: "root path",
			cwd:  "/",
			want: "--",
		},
		{
			name: "nested path",
			cwd:  "/home/user/workspace/my-project",
			want: "--home--user--workspace--my-project",
		},
		{
			name: "path with trailing slash",
			cwd:  "/Users/dev/project/",
			want: "--Users--dev--project--",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cwdToDirName(tt.cwd)
			if got != tt.want {
				t.Errorf("cwdToDirName(%q) = %q, want %q", tt.cwd, got, tt.want)
			}
		})
	}
}
