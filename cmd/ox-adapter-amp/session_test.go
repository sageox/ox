package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/sageox/ox/pkg/adapterruntime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

// TestFindAmpSession_PrefersBridgeDir verifies the new sidecar directory
// (~/.cache/amp/ox-sessions) is preferred over the legacy
// ~/.amp/sessions when both exist. This is the migration path: a user
// upgrades, install-hooks drops a bridge plugin, and from that moment
// the adapter must read fresh sidecar JSONL — not stale legacy files.
// Failure prevented: adapter reading stale legacy session after upgrade.
func TestFindAmpSession_PrefersBridgeDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	legacyDir := filepath.Join(home, ".amp", "sessions")
	bridgeDir := filepath.Join(home, ".cache", "amp", "ox-sessions")
	if err := os.MkdirAll(legacyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(bridgeDir, 0o755); err != nil {
		t.Fatal(err)
	}

	data := `{"type":"user","timestamp":"2024-01-15T10:00:00Z","content":"x"}` + "\n"
	legacyFile := filepath.Join(legacyDir, "old.jsonl")
	bridgeFile := filepath.Join(bridgeDir, "new.jsonl")
	if err := os.WriteFile(legacyFile, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bridgeFile, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := findAmpSession("", "", "", "")
	if err != nil {
		t.Fatalf("findAmpSession: %v", err)
	}
	if got != bridgeFile {
		t.Errorf("got %q, want %q (bridge dir must win over legacy)", got, bridgeFile)
	}
}

// TestFindAmpSession_EmptyBridgeFallsBackToLegacy guards the case where
// the bridge dir exists but is still empty (e.g. install-hooks just
// ran, no session has flushed yet) and the user has live legacy
// sessions in ~/.amp/sessions. The previous implementation stopped
// at the first existing dir and returned "no sessions found"; now we
// continue walking until we get usable candidates.
// Failure prevented: discovery returning "no sessions" when legacy
// data is right there.
func TestFindAmpSession_EmptyBridgeFallsBackToLegacy(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	bridgeDir := filepath.Join(home, ".cache", "amp", "ox-sessions")
	legacyDir := filepath.Join(home, ".amp", "sessions")
	if err := os.MkdirAll(bridgeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(legacyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// bridge dir is intentionally empty
	legacyFile := filepath.Join(legacyDir, "old.jsonl")
	data := `{"type":"user","timestamp":"2024-01-15T10:00:00Z","content":"x"}` + "\n"
	if err := os.WriteFile(legacyFile, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := findAmpSession("", "", "", "")
	if err != nil {
		t.Fatalf("findAmpSession: %v", err)
	}
	if got != legacyFile {
		t.Errorf("got %q, want %q (empty bridge must fall through to legacy)", got, legacyFile)
	}
}

// TestFindAmpSession_LegacyFallback verifies that on a host with no
// bridge sidecar dir, discovery still finds files in the legacy
// ~/.amp/sessions location. This is what keeps pre-2026 Amp users
// working until they reinstall hooks.
func TestFindAmpSession_LegacyFallback(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	legacyDir := filepath.Join(home, ".amp", "sessions")
	if err := os.MkdirAll(legacyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	legacyFile := filepath.Join(legacyDir, "old.jsonl")
	data := `{"type":"user","timestamp":"2024-01-15T10:00:00Z","content":"x"}` + "\n"
	if err := os.WriteFile(legacyFile, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := findAmpSession("", "", "", "")
	if err != nil {
		t.Fatalf("findAmpSession: %v", err)
	}
	if got != legacyFile {
		t.Errorf("got %q, want %q (legacy fallback)", got, legacyFile)
	}
}

// TestFindAmpSession_ByThreadURL: Amp exports the thread as AMP_THREAD_URL,
// and that URL is the native session id ox records and passes back for
// lookup. The ox-bridge sidecar is named by the bare thread id. Failure
// prevented (sageox/ox#1314): the URL's slashes were rejected as a path
// before any lookup, so a recording that started without its sidecar path
// could never find it — at prime or at stop — and never uploaded.
func TestFindAmpSession_ByThreadURL(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	bridgeDir := filepath.Join(home, ".cache", "amp", "ox-sessions")
	require.NoError(t, os.MkdirAll(bridgeDir, 0o755))
	line := []byte(`{"type":"user","timestamp":"2026-10-14T10:00:00Z","content":"rename the retry helper"}` + "\n")
	want := filepath.Join(bridgeDir, "T-5f0c2a71-3b9e.jsonl")
	require.NoError(t, os.WriteFile(want, line, 0o644))
	// a newer sidecar from another thread: only a direct lookup picks want
	other := filepath.Join(bridgeDir, "T-other.jsonl")
	require.NoError(t, os.WriteFile(other, line, 0o644))
	past := time.Now().Add(-time.Hour)
	require.NoError(t, os.Chtimes(want, past, past))

	for _, id := range []string{
		"T-5f0c2a71-3b9e",
		"https://ampcode.com/threads/T-5f0c2a71-3b9e",
		"https://ampcode.com/threads/T-5f0c2a71-3b9e/",
		"https://amp.example.com/threads/T-5f0c2a71-3b9e?view=full#m-4",
	} {
		t.Run(id, func(t *testing.T) {
			got, err := findAmpSession("", "", "", id)
			require.NoError(t, err)
			assert.Equal(t, want, got)
		})
	}

	for _, id := range []string{
		"https://ampcode.com/threads/../../.ssh/id_ed25519",
		"https://ampcode.com/threads/",
		"https://ampcode.com/not-a-thread/T-5f0c2a71-3b9e",
		"sub/dir",
	} {
		t.Run("rejects "+id, func(t *testing.T) {
			_, err := findAmpSession("", "", "", id)
			require.Error(t, err)
		})
	}
}

// TestDispatch_FindSession_ByThreadURL drives the same lookup through the
// adapter protocol, the way ox calls it at prime and at session stop.
func TestDispatch_FindSession_ByThreadURL(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	bridgeDir := filepath.Join(home, ".cache", "amp", "ox-sessions")
	require.NoError(t, os.MkdirAll(bridgeDir, 0o755))
	want := filepath.Join(bridgeDir, "T-abc123.jsonl")
	require.NoError(t, os.WriteFile(want, []byte(`{"type":"user","content":"hi"}`+"\n"), 0o644))

	repo := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(repo, ".sageox"), 0o755))

	var out bytes.Buffer
	args := []string{"find-session", "--repo-root", repo, "--agent-session-id", "https://ampcode.com/threads/T-abc123"}
	require.NoError(t, adapterruntime.RunWithArgs(newConfig(), args, nil, &out), out.String())
	var got adapterprotocol.FindSessionResult
	require.NoError(t, json.Unmarshal(out.Bytes(), &got), out.String())
	assert.Equal(t, want, got.SessionFile)
}

// TestFindAmpSession_ExplicitThreadMissIsNotFound: a lookup that names a
// thread must find that thread's sidecar or nothing. Failure prevented:
// session stop (or import) for thread A, whose sidecar is missing, silently
// picks the newest sidecar on the machine, thread B, and uploads B's
// conversation under A's recording.
func TestFindAmpSession_ExplicitThreadMissIsNotFound(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	line := []byte(`{"type":"user","timestamp":"2026-10-14T10:00:00Z","content":"another thread"}` + "\n")
	other := filepath.Join(home, ".cache", "amp", "ox-sessions", "T-other.jsonl")
	legacy := filepath.Join(home, ".amp", "sessions", "legacy.jsonl")
	for _, p := range []string{other, legacy} {
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, line, 0o644))
	}

	for _, id := range []string{"T-missing", "https://ampcode.com/threads/T-missing"} {
		t.Run(id, func(t *testing.T) {
			got, err := findAmpSession("", "", "", id)
			require.Error(t, err, "a named thread with no sidecar must not resolve to another thread's")
			assert.Empty(t, got)
		})
	}

	t.Run("no id still finds the newest sidecar", func(t *testing.T) {
		got, err := findAmpSession("", "", "", "")
		require.NoError(t, err)
		assert.Equal(t, other, got)
	})

	t.Run("import by a missing id fails", func(t *testing.T) {
		_, err := handleImportSession(adapterprotocol.ImportSessionParams{SessionID: "T-missing"})
		require.Error(t, err)
	})
}
