package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// --- A. Line parsing ---
//
// Fixtures here are copied verbatim from transcripts that a real `pi` binary
// wrote (testdata/session-v3.jsonl, anonymized paths only). Hand-authored
// fixtures are what let this adapter ship a parser for a format Pi never
// emitted: every real line was dropped and every test still passed.

// TestParsePiLine_UserMessage verifies Pi's nested message envelope parses.
// Failure prevented: every user turn silently dropped from the recording.
func TestParsePiLine_UserMessage(t *testing.T) {
	line := []byte(`{"type":"message","id":"m1","timestamp":"2026-04-04T01:03:14.337Z","message":{"role":"user","content":[{"type":"text","text":"hello"}]}}`)

	entries := parsePiLine(line)
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1: %+v", len(entries), entries)
	}
	if entries[0].Role != "user" {
		t.Errorf("role = %q, want user", entries[0].Role)
	}
	if entries[0].Content != "hello" {
		t.Errorf("content = %q, want hello", entries[0].Content)
	}
}

// TestParsePiLine_AssistantDropsThinking verifies an assistant turn keeps its
// text and discards reasoning.
// Failure prevented: thinking blocks leaking into the shared team ledger.
func TestParsePiLine_AssistantDropsThinking(t *testing.T) {
	line := []byte(`{"type":"message","timestamp":"2026-04-04T01:03:15.000Z","message":{"role":"assistant","content":[{"type":"thinking","thinking":"internal reasoning"},{"type":"text","text":"visible answer"}]}}`)

	entries := parsePiLine(line)
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1: %+v", len(entries), entries)
	}
	if entries[0].Content != "visible answer" {
		t.Errorf("content = %q, want the text block only", entries[0].Content)
	}
	if strings.Contains(entries[0].Content, "internal reasoning") {
		t.Error("thinking block leaked into the entry")
	}
}

// TestParsePiLine_ToolCall verifies Pi's toolCall block becomes a tool entry.
// Failure prevented: tool use invisible in recorded sessions.
func TestParsePiLine_ToolCall(t *testing.T) {
	line := []byte(`{"type":"message","timestamp":"2026-04-04T01:03:15.000Z","message":{"role":"assistant","content":[{"type":"toolCall","id":"toolu_01","name":"read","arguments":{"path":"AGENTS.md"}}]}}`)

	entries := parsePiLine(line)
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1: %+v", len(entries), entries)
	}
	e := entries[0]
	if e.Role != "tool" || e.ToolName != "read" || e.CallID != "toolu_01" {
		t.Errorf("entry = %+v, want tool/read/toolu_01", e)
	}
	if !strings.Contains(e.ToolInput, `"path":"AGENTS.md"`) {
		t.Errorf("tool input = %q, want the raw arguments object", e.ToolInput)
	}
}

// TestParsePiLine_MultipleToolCallsInOneTurn verifies one message can yield
// several entries.
// Failure prevented: only the first tool call of a parallel batch recorded.
func TestParsePiLine_MultipleToolCallsInOneTurn(t *testing.T) {
	line := []byte(`{"type":"message","timestamp":"2026-04-04T01:03:15.000Z","message":{"role":"assistant","content":[{"type":"text","text":"reading both"},{"type":"toolCall","id":"t1","name":"read","arguments":{}},{"type":"toolCall","id":"t2","name":"bash","arguments":{}}]}}`)

	entries := parsePiLine(line)
	if len(entries) != 3 {
		t.Fatalf("got %d entries, want 3 (text + 2 tool calls): %+v", len(entries), entries)
	}
	if entries[1].CallID != "t1" || entries[2].CallID != "t2" {
		t.Errorf("call ids = %q/%q, want t1/t2", entries[1].CallID, entries[2].CallID)
	}
}

// TestParsePiLine_ToolResult verifies the standalone toolResult message shape.
// Failure prevented: tool output missing, breaking call/result pairing.
func TestParsePiLine_ToolResult(t *testing.T) {
	line := []byte(`{"type":"message","timestamp":"2026-04-04T01:03:16.000Z","message":{"role":"toolResult","toolCallId":"toolu_01","toolName":"read","isError":false,"content":[{"type":"text","text":"file contents"}]}}`)

	entries := parsePiLine(line)
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1: %+v", len(entries), entries)
	}
	e := entries[0]
	if e.ToolOutput != "file contents" || e.CallID != "toolu_01" || e.IsError {
		t.Errorf("entry = %+v, want output/toolu_01/no-error", e)
	}
}

// TestParsePiLine_ToolResultError verifies isError propagates.
// Failure prevented: failed tool calls recorded as successes.
func TestParsePiLine_ToolResultError(t *testing.T) {
	line := []byte(`{"type":"message","timestamp":"2026-04-04T01:03:16.000Z","message":{"role":"toolResult","toolCallId":"t1","toolName":"bash","isError":true,"content":[{"type":"text","text":"exit status 1"}]}}`)

	entries := parsePiLine(line)
	if len(entries) != 1 || !entries[0].IsError {
		t.Fatalf("entries = %+v, want one error result", entries)
	}
}

// TestParsePiLine_NonMessageRecords verifies Pi's bookkeeping lines are skipped.
// Failure prevented: header records surfacing as conversation turns.
func TestParsePiLine_NonMessageRecords(t *testing.T) {
	for _, line := range []string{
		`{"type":"session","version":3,"id":"s1","timestamp":"2026-04-04T01:03:14.337Z","cwd":"/tmp/fixture/project"}`,
		`{"type":"model_change","provider":"anthropic","modelId":"claude-haiku-4-5-20251001"}`,
		`{"type":"thinking_level_change","thinkingLevel":"medium"}`,
		`{"type":"message"}`,
		`not json`,
	} {
		if entries := parsePiLine([]byte(line)); len(entries) != 0 {
			t.Errorf("parsePiLine(%s) = %+v, want no entries", line, entries)
		}
	}
}

// TestParsePiLine_EmptyTextBlockSkipped keeps blank turns out of the ledger.
func TestParsePiLine_EmptyTextBlockSkipped(t *testing.T) {
	line := []byte(`{"type":"message","timestamp":"2026-04-04T01:03:14.337Z","message":{"role":"user","content":[{"type":"text","text":""}]}}`)
	if entries := parsePiLine(line); len(entries) != 0 {
		t.Errorf("got %+v, want no entries for an empty text block", entries)
	}
}

func TestParsePiLine_TimestampPreserved(t *testing.T) {
	line := []byte(`{"type":"message","timestamp":"2026-04-04T01:03:14.337Z","message":{"role":"user","content":[{"type":"text","text":"hi"}]}}`)

	entries := parsePiLine(line)
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	ts, err := time.Parse(time.RFC3339, entries[0].Timestamp)
	if err != nil {
		t.Fatalf("parse timestamp %q: %v", entries[0].Timestamp, err)
	}
	want := time.Date(2026, time.April, 4, 1, 3, 14, 0, time.UTC)
	if !ts.Equal(want) {
		t.Errorf("timestamp = %s, want %s — a parser that truncates milliseconds or shifts the day/time/offset must fail here", ts, want)
	}
}

// --- B. Whole-file reading ---

const fixtureTranscript = "testdata/session-v3.jsonl"

// TestReadPiFile_RealTranscript is the gate: a transcript a real pi wrote must
// produce a real conversation. The previous parser returned zero entries here.
func TestReadPiFile_RealTranscript(t *testing.T) {
	entries, err := readPiFile(fixtureTranscript)
	if err != nil {
		t.Fatalf("readPiFile: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("real pi transcript produced zero entries — the parser does not match Pi's format")
	}

	var roles []string
	for _, e := range entries {
		roles = append(roles, e.Role)
	}
	for _, want := range []string{"user", "assistant", "tool"} {
		if !contains(roles, want) {
			t.Errorf("no %q entry in %v — round trip incomplete", want, roles)
		}
	}
}

// TestReadPiFile_ToolCallResultPairing verifies call ids match across the two
// separate records Pi writes.
// Failure prevented: orphaned tool results with no call to attach to.
func TestReadPiFile_ToolCallResultPairing(t *testing.T) {
	entries, err := readPiFile(fixtureTranscript)
	if err != nil {
		t.Fatalf("readPiFile: %v", err)
	}

	calls := map[string]bool{}
	for _, e := range entries {
		if e.ToolName != "" {
			calls[e.CallID] = true
		}
	}
	if len(calls) == 0 {
		t.Fatal("no tool calls found in the fixture")
	}

	var matched int
	for _, e := range entries {
		if e.ToolOutput != "" && calls[e.CallID] {
			matched++
		}
	}
	if matched == 0 {
		t.Errorf("no tool result matched a recorded call id (calls: %v)", calls)
	}
}

func TestExtractPiMetadata_RealTranscript(t *testing.T) {
	meta := extractPiMetadata(fixtureTranscript)
	if meta == nil {
		t.Fatal("no metadata extracted from a real transcript")
	}
	if meta.AgentVersion != "pi-v3" {
		t.Errorf("agent version = %q, want pi-v3 (real transcripts are version 3)", meta.AgentVersion)
	}
	// the model lives on model_change, not on the session header
	if meta.Model == "" {
		t.Error("model not extracted — model_change record ignored?")
	}
}

func TestReadPiFile_SkipsEmptyLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	content := `{"type":"session","version":3,"id":"s1"}

{"type":"message","timestamp":"2026-04-04T01:03:14.337Z","message":{"role":"user","content":[{"type":"text","text":"hello"}]}}

{"type":"message","timestamp":"2026-04-04T01:03:15.337Z","message":{"role":"assistant","content":[{"type":"text","text":"hi"}]}}
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	entries, err := readPiFile(path)
	if err != nil {
		t.Fatalf("readPiFile: %v", err)
	}
	if len(entries) != 2 {
		t.Errorf("got %d entries, want 2 (header and blank lines skipped)", len(entries))
	}
}

// TestReadPiFromOffset verifies incremental reads don't replay old turns.
// Failure prevented: duplicate entries on every daemon poll.
func TestReadPiFromOffset(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")

	line1 := `{"type":"message","timestamp":"2026-04-04T01:03:14.337Z","message":{"role":"user","content":[{"type":"text","text":"first"}]}}` + "\n"
	line2 := `{"type":"message","timestamp":"2026-04-04T01:03:15.337Z","message":{"role":"assistant","content":[{"type":"text","text":"second"}]}}` + "\n"

	if err := os.WriteFile(path, []byte(line1+line2), 0o644); err != nil {
		t.Fatal(err)
	}

	entries, newOffset, err := readPiFromOffset(path, int64(len(line1)))
	if err != nil {
		t.Fatalf("readPiFromOffset: %v", err)
	}
	if len(entries) != 1 || entries[0].Content != "second" {
		t.Fatalf("entries = %+v, want only the second turn", entries)
	}
	if newOffset != int64(len(line1)+len(line2)) {
		t.Errorf("newOffset = %d, want %d", newOffset, len(line1)+len(line2))
	}
}

func TestReadPiFromOffset_ZeroOffsetReadsAll(t *testing.T) {
	entries, newOffset, err := readPiFromOffset(fixtureTranscript, 0)
	if err != nil {
		t.Fatalf("readPiFromOffset: %v", err)
	}
	if len(entries) == 0 {
		t.Error("zero offset returned no entries")
	}
	if newOffset == 0 {
		t.Error("newOffset should advance past the file")
	}
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
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

// TestFindPiSession_ScopedRepoWithNoSessions_DoesNotLeakOtherProject is the
// regression gate for a cross-project data leak: a project-scoped lookup for
// a repo with no session directory of its own must return "not found", not
// silently fall back to the newest session anywhere on disk. Ledgers are
// shared with teammates, so returning a foreign project's transcript here
// would write one repo's conversation content into a different repo's
// ledger.
//
// Failure prevented: findPiSession("/repo-a", ...) returning a transcript
// that belongs to "/repo-b" because "/repo-a"'s mangled-cwd directory
// doesn't exist yet and the code fell through to a global newest-file scan.
func TestFindPiSession_ScopedRepoWithNoSessions_DoesNotLeakOtherProject(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Only repo B has a session directory. Repo A's mangled-cwd directory
	// does not exist at all.
	repoB := "/Users/dev/repo-b"
	projectDirB := filepath.Join(home, ".pi", "agent", "sessions", cwdToDirName(repoB))
	if err := os.MkdirAll(projectDirB, 0o755); err != nil {
		t.Fatal(err)
	}
	data := `{"type":"user","timestamp":"2024-01-15T10:00:00Z","content":"repo b conversation"}` + "\n"
	if err := os.WriteFile(filepath.Join(projectDirB, "session-b.jsonl"), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}

	repoA := "/Users/dev/repo-a"
	got, err := findPiSession(repoA, "", "", "")
	if err == nil {
		t.Fatalf("findPiSession(%q) = %q, want a not-found error — repo B's transcript must never be attributed to repo A", repoA, got)
	}
}

// TestFindPiSession_DirectIDLookupScopedToRepo is the same defect class as
// the leak above, on the OTHER lookup path: when both repoRoot and
// agentSessionID are set, the direct by-ID lookup must be confined to
// repoRoot's own project directory, not scan every project directory on
// disk. A session recorded under a different project must never be returned
// just because its filename happens to match the requested ID — Ledgers are
// shared with teammates, so that would upload another repo's conversation
// into the wrong Ledger.
//
// Failure prevented: findPiSession("/repo-a", "", "", sharedID) returning
// "/repo-b"'s session file because the direct-ID search walked every
// subdirectory under ~/.pi/agent/sessions instead of only repo A's.
func TestFindPiSession_DirectIDLookupScopedToRepo(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	baseDir := filepath.Join(home, ".pi", "agent", "sessions")

	// Repo A has a project directory but no sessions in it.
	repoA := "/Users/dev/repo-a"
	if err := os.MkdirAll(filepath.Join(baseDir, cwdToDirName(repoA)), 0o755); err != nil {
		t.Fatal(err)
	}

	// Repo B has a session file whose name happens to match the ID repo A's
	// lookup will ask for.
	repoB := "/Users/dev/repo-b"
	projectDirB := filepath.Join(baseDir, cwdToDirName(repoB))
	if err := os.MkdirAll(projectDirB, 0o755); err != nil {
		t.Fatal(err)
	}
	const sharedID = "22222222-2222-2222-2222-222222222222"
	leakedSession := filepath.Join(projectDirB, sharedID+".jsonl")
	data := `{"type":"user","timestamp":"2024-01-15T10:00:00Z","content":"repo b conversation"}` + "\n"
	if err := os.WriteFile(leakedSession, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := findPiSession(repoA, "", "", sharedID)
	if got == leakedSession {
		t.Fatalf("findPiSession(%q, ..., %q) returned repo B's session %q — cross-project leak via the direct-ID lookup", repoA, sharedID, leakedSession)
	}
	if err == nil {
		t.Fatalf("findPiSession(%q, ..., %q) = %q, want a not-found error — repo A has no session with that ID and must not fall back to another project's", repoA, sharedID, got)
	}
}

// TestFindPiSession_UnscopedFallsBackToNewestAnywhere pins the one case where
// scanning every subdirectory IS correct: no repoRoot was supplied at all
// (an unscoped query), which callers exercise via handleDetect's format-check
// path and a bare CapturePrior call. Losing this fallback would break that
// case while fixing the scoped-leak bug above.
func TestFindPiSession_UnscopedFallsBackToNewestAnywhere(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	projectDir := filepath.Join(home, ".pi", "agent", "sessions", "--some--project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sessionFile := filepath.Join(projectDir, "session.jsonl")
	data := `{"type":"user","timestamp":"2024-01-15T10:00:00Z","content":"test"}` + "\n"
	if err := os.WriteFile(sessionFile, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := findPiSession("", "", "", "")
	if err != nil {
		t.Fatalf("findPiSession(\"\"): %v", err)
	}
	if got != sessionFile {
		t.Errorf("got %q, want %q", got, sessionFile)
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
