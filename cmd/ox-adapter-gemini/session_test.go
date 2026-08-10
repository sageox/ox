package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/pkg/adapterprotocol"
)

// realSessionJSON is the on-disk fixture Gemini CLI 0.36.0 produced. Tests that
// need a session file on disk copy this rather than hand-writing JSON, because
// a hand-written fixture can only ever confirm the parser's own assumptions.
func realSessionJSON(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(realFixture)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return data
}

// writeSessionTree lays out ~/.gemini/tmp/<project>/chats/<name> under a fake
// HOME and returns the session file path.
func writeSessionTree(t *testing.T, home, project, name string, data []byte) string {
	t.Helper()
	chatsDir := filepath.Join(home, ".gemini", "tmp", project, "chats")
	if err := os.MkdirAll(chatsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(chatsDir, name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// --- A. Parsing what gemini actually writes ---

// TestParseRealSession_EntryShape pins the full entry list the real transcript
// produces. Before the fix this returned zero entries and no error, because the
// parser looked for a top-level array of {role, parts} that gemini never wrote.
func TestParseRealSession_EntryShape(t *testing.T) {
	t.Parallel()

	entries, meta, err := parseGeminiSession(realSessionJSON(t))
	if err != nil {
		t.Fatalf("parseGeminiSession: %v", err)
	}

	if len(entries) != 11 {
		t.Fatalf("got %d entries, want 11", len(entries))
	}
	if meta.Model != "gemini-3-flash-preview" {
		t.Errorf("model = %q, want gemini-3-flash-preview", meta.Model)
	}

	want := []struct {
		role     string
		toolName string
		hasOut   bool
	}{
		{adapterprotocol.RoleUser, "", false},
		{adapterprotocol.RoleAssistant, "", false},
		{adapterprotocol.RoleTool, "list_directory", false},
		{adapterprotocol.RoleTool, "list_directory", true},
		{adapterprotocol.RoleAssistant, "", false},
		{adapterprotocol.RoleTool, "list_directory", false},
		{adapterprotocol.RoleTool, "list_directory", true},
		{adapterprotocol.RoleAssistant, "", false},
		{adapterprotocol.RoleTool, "list_directory", false},
		{adapterprotocol.RoleTool, "list_directory", true},
		{adapterprotocol.RoleAssistant, "", false},
	}
	for i, w := range want {
		got := entries[i]
		if got.Role != w.role {
			t.Errorf("entry %d role = %q, want %q", i, got.Role, w.role)
		}
		if got.ToolName != w.toolName {
			t.Errorf("entry %d tool_name = %q, want %q", i, got.ToolName, w.toolName)
		}
		if (got.ToolOutput != "") != w.hasOut {
			t.Errorf("entry %d tool_output present = %v, want %v", i, got.ToolOutput != "", w.hasOut)
		}
	}
}

// TestParseRealSession_PolymorphicContent covers gemini's two content shapes:
// an array of {text} parts on user messages and a bare string on model
// messages. Handling only one silently drops every turn of the other kind.
func TestParseRealSession_PolymorphicContent(t *testing.T) {
	t.Parallel()

	entries, _, err := parseGeminiSession(realSessionJSON(t))
	if err != nil {
		t.Fatal(err)
	}

	if !strings.HasPrefix(entries[0].Content, "List the contents of the .sageox/ directory") {
		t.Errorf("user content = %q, want the text extracted from the parts array", entries[0].Content)
	}
	if !strings.HasPrefix(entries[1].Content, "I'll list the contents of") {
		t.Errorf("assistant content = %q, want the bare content string", entries[1].Content)
	}
}

// TestParseRealSession_ToolCallsPairAndCarryTimestamps checks the two fields the
// ledger needs from a tool exchange: a call id shared by call and result, and a
// real timestamp. The old parser emitted a zero time and no call id at all.
func TestParseRealSession_ToolCallsPairAndCarryTimestamps(t *testing.T) {
	t.Parallel()

	entries, _, err := parseGeminiSession(realSessionJSON(t))
	if err != nil {
		t.Fatal(err)
	}

	call, result := entries[2], entries[3]
	if call.CallID == "" || call.CallID != result.CallID {
		t.Fatalf("call id = %q, result id = %q — must be equal and non-empty", call.CallID, result.CallID)
	}
	if call.CallID != "toolaaa1" {
		t.Errorf("call id = %q, want toolaaa1", call.CallID)
	}
	if call.ToolInput != `{"dir_path":".sageox/"}` {
		t.Errorf("tool_input = %q", call.ToolInput)
	}
	if !strings.Contains(result.ToolOutput, "[DIR] agent_instances") {
		t.Errorf("tool_output = %q, want the extracted response.output text", result.ToolOutput)
	}
	if result.IsError {
		t.Error("a status:\"success\" tool call must not be flagged is_error")
	}

	for i, e := range entries {
		ts, err := time.Parse(time.RFC3339, e.Timestamp)
		if err != nil {
			t.Fatalf("entry %d timestamp %q does not parse: %v", i, e.Timestamp, err)
		}
		if ts.Year() != 2026 {
			t.Errorf("entry %d timestamp %s is not the transcript's real time", i, ts)
		}
	}
}

// TestParseGeminiSession_FormatDriftFailsLoudly is the guard against the exact
// failure this adapter shipped with: messages present, entries absent, error
// nil. A reader that returns nothing must say so.
func TestParseGeminiSession_FormatDriftFailsLoudly(t *testing.T) {
	t.Parallel()

	// a session whose messages carry no recognizable content at all
	drifted := []byte(`{"sessionId":"x","kind":"main","messages":[{"id":"1","futureShape":{}},{"id":"2"}]}`)

	_, _, err := parseGeminiSession(drifted)
	if err == nil {
		t.Fatal("want an error when messages exist but no entries are produced")
	}
	if !strings.Contains(err.Error(), "does not match the file format") {
		t.Errorf("error = %v, want it to name the format mismatch", err)
	}
}

// TestParseGeminiSession_EmptySessionIsNotAnError keeps a genuinely new session
// (zero messages) from being reported as drift.
func TestParseGeminiSession_EmptySessionIsNotAnError(t *testing.T) {
	t.Parallel()

	entries, _, err := parseGeminiSession([]byte(`{"sessionId":"x","kind":"main","messages":[]}`))
	if err != nil {
		t.Fatalf("empty session must not error: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("got %d entries, want 0", len(entries))
	}
}

// TestToolCallOutcome covers the failure signaling that the real fixture
// cannot prove — every captured tool call succeeded. The status values are
// gemini 0.36.0's own enum, reproduced verbatim below because they are the
// agent's tokens, not our prose; plus the response.error key documented in its
// bundled docs/hooks/reference.md.
func TestToolCallOutcome(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		call       geminiToolCall
		wantOutput string
		wantErr    bool
		wantOK     bool
	}{
		{
			name: "success",
			call: geminiToolCall{Status: "success", Result: []geminiToolResult{{
				FunctionResponse: &geminiFunctionResponse{Response: map[string]any{"output": "ok"}},
			}}},
			wantOutput: "ok", wantErr: false, wantOK: true,
		},
		{
			name: "error status",
			call: geminiToolCall{Status: "error", Result: []geminiToolResult{{
				FunctionResponse: &geminiFunctionResponse{Response: map[string]any{"output": "boom"}},
			}}},
			wantOutput: "boom", wantErr: true, wantOK: true,
		},
		{
			name: "canceled status",
			//nolint:misspell // gemini's own enum value, reproduced verbatim — Americanizing it would stop matching what the agent writes
			call: geminiToolCall{Status: "cancelled", Result: []geminiToolResult{{
				FunctionResponse: &geminiFunctionResponse{Response: map[string]any{"output": "stopped"}},
			}}},
			wantOutput: "stopped", wantErr: true, wantOK: true,
		},
		{
			name: "error key wins over a success status",
			call: geminiToolCall{Status: "success", Result: []geminiToolResult{{
				FunctionResponse: &geminiFunctionResponse{Response: map[string]any{"error": "permission denied"}},
			}}},
			wantOutput: "permission denied", wantErr: true, wantOK: true,
		},
		{
			name:   "in-flight call has no result yet",
			call:   geminiToolCall{Status: "executing"},
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			output, isErr, ok := tt.call.outcome()
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if !ok {
				return
			}
			if output != tt.wantOutput {
				t.Errorf("output = %q, want %q", output, tt.wantOutput)
			}
			if isErr != tt.wantErr {
				t.Errorf("is_error = %v, want %v", isErr, tt.wantErr)
			}
		})
	}
}

// TestParseGeminiToolCall_InFlightEmitsCallOnly makes sure a tool that has not
// finished contributes a call entry and no phantom empty result.
func TestParseGeminiToolCall_InFlightEmitsCallOnly(t *testing.T) {
	t.Parallel()

	call := geminiToolCall{
		ID: "abc12345", Name: "run_shell_command", Status: "executing",
		Timestamp: "2026-04-03T22:06:22.231Z",
	}
	entries := parseGeminiToolCall(&call, time.Time{})
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1 (call only)", len(entries))
	}
	if entries[0].ToolOutput != "" {
		t.Errorf("tool_output = %q, want empty for an unfinished call", entries[0].ToolOutput)
	}
}

// --- B. Session discovery against gemini's real filenames ---

// TestFindGeminiBySessionID_RealFilenameShape is the regression test for the
// lookup bug: gemini names files session-<date>T<HH-MM>-<first 8 of the
// uuid>.json, so building session-<full uuid>.json can never match a real file.
func TestFindGeminiBySessionID_RealFilenameShape(t *testing.T) {
	home := t.TempDir()

	want := writeSessionTree(t, home, "project-5",
		"session-2026-04-03T22-06-4b1c7e02.json", realSessionJSON(t))

	tmpDir := filepath.Join(home, ".gemini", "tmp")
	got, err := findGeminiBySessionID(tmpDir, "4b1c7e02-a3d9-4f18-9c26-7e5b0a1d2f34")
	if err != nil {
		t.Fatalf("findGeminiBySessionID: %v", err)
	}
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestFindGeminiBySessionID_PrefersTheMatchingSessionID guards the case where
// two sessions share an id8 prefix in different project dirs: the sessionId
// inside the file, not the filename, decides.
func TestFindGeminiBySessionID_PrefersTheMatchingSessionID(t *testing.T) {
	home := t.TempDir()

	decoy := strings.Replace(string(realSessionJSON(t)),
		"4b1c7e02-a3d9-4f18-9c26-7e5b0a1d2f34",
		"4b1c7e02-0000-0000-0000-000000000000", 1)
	writeSessionTree(t, home, "project-1",
		"session-2026-04-03T22-06-4b1c7e02.json", []byte(decoy))
	want := writeSessionTree(t, home, "project-2",
		"session-2026-04-03T22-06-4b1c7e02.json", realSessionJSON(t))

	tmpDir := filepath.Join(home, ".gemini", "tmp")
	got, err := findGeminiBySessionID(tmpDir, "4b1c7e02-a3d9-4f18-9c26-7e5b0a1d2f34")
	if err != nil {
		t.Fatalf("findGeminiBySessionID: %v", err)
	}
	if got != want {
		t.Errorf("got %q, want %q (the file whose sessionId matches)", got, want)
	}
}

// TestFindGeminiSession_TimestampFallback preserves most-recent-by-modtime
// discovery when no session id is supplied.
func TestFindGeminiSession_TimestampFallback(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	data := realSessionJSON(t)
	older := writeSessionTree(t, home, "project-1", "session-2026-04-03T21-59-6cc942fa.json", data)
	newer := writeSessionTree(t, home, "project-2", "session-2026-04-03T22-06-4b1c7e02.json", data)

	past := time.Now().Add(-1 * time.Hour)
	if err := os.Chtimes(older, past, past); err != nil {
		t.Fatal(err)
	}

	got, err := findGeminiSession("/tmp/repo", "", "", "")
	if err != nil {
		t.Fatalf("findGeminiSession: %v", err)
	}
	if got != newer {
		t.Errorf("got %q, want %q (most recent)", got, newer)
	}
}

// TestFindGeminiSession_UnknownIDFallsBackToScan keeps an unrecognized session
// id from turning into a hard failure.
func TestFindGeminiSession_UnknownIDFallsBackToScan(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	real := writeSessionTree(t, home, "project-1",
		"session-2026-04-03T22-06-4b1c7e02.json", realSessionJSON(t))

	got, err := findGeminiSession("/tmp/repo", "", "", "deadbeef-0000-4000-8000-000000000000")
	if err != nil {
		t.Fatalf("findGeminiSession: %v", err)
	}
	if got != real {
		t.Errorf("got %q, want %q (fallback)", got, real)
	}
}

// --- C. Diagnose must not stay green while the reader is dead ---

// TestDiagnoseReader_ReportsAnEmptyParse is the check that would have made the
// original bug visible: a transcript that parses to nothing is an error, not a
// clean bill of health.
func TestDiagnoseReader_ReportsAnEmptyParse(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	// a session in a shape this parser does not understand
	writeSessionTree(t, home, "project-1", "session-2026-04-03T22-06-4b1c7e02.json",
		[]byte(`{"sessionId":"x","kind":"main","messages":[{"id":"1","surprise":true}]}`))

	issues := diagnoseReader(filepath.Join(home, ".gemini", "tmp"))
	if len(issues) == 0 {
		t.Fatal("want an issue when the newest transcript yields no entries")
	}
	if issues[0].Severity != "error" {
		t.Errorf("severity = %q, want error", issues[0].Severity)
	}
}

// TestDiagnoseReader_SilentOnAHealthyTranscript keeps the new check from
// crying wolf on real output.
func TestDiagnoseReader_SilentOnAHealthyTranscript(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	writeSessionTree(t, home, "project-1",
		"session-2026-04-03T22-06-4b1c7e02.json", realSessionJSON(t))

	if issues := diagnoseReader(filepath.Join(home, ".gemini", "tmp")); len(issues) != 0 {
		t.Errorf("got %d issues on a real transcript, want 0: %+v", len(issues), issues)
	}
}

// TestHandleDiagnose_ReportsADeadReader asserts the reader-health check is
// actually wired into the diagnose entry point, not merely present in the
// package. Testing diagnoseReader directly proves the function works; only
// this proves `diagnose` can no longer answer ok:true while the reader is
// dead — which is what the shipped adapter did on every real machine.
func TestHandleDiagnose_ReportsADeadReader(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	writeSessionTree(t, home, "project-1", "session-2026-04-03T22-06-4b1c7e02.json",
		[]byte(`{"sessionId":"x","kind":"main","messages":[{"id":"1","surprise":true}]}`))

	result, err := handleDiagnose(adapterprotocol.DiagnoseParams{})
	if err != nil {
		t.Fatalf("diagnose: %v", err)
	}
	if result.OK {
		t.Fatal("diagnose reported ok:true while the session reader extracts nothing")
	}

	var found bool
	for _, issue := range result.Issues {
		if strings.HasPrefix(issue.Slug, "gemini:session-") {
			found = true
		}
	}
	if !found {
		t.Errorf("want a gemini:session-* issue from diagnose, got %+v", result.Issues)
	}
}
