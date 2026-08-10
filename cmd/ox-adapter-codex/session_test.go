package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/sageox/ox/pkg/adapterruntime"
)

// --- A. Direct lookup via agent_session_id ---

// TestFindCodexSession_DirectLookup verifies that providing an agent session ID
// finds the file by matching the session_meta.id field in the JSONL.
// Failure prevented: slow scan of all date directories when the session ID is known.
func TestFindCodexSession_DirectLookup(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	now := time.Now()
	dateDir := filepath.Join(home, ".codex", "sessions",
		now.Format("2006"), now.Format("01"), now.Format("02"))
	if err := os.MkdirAll(dateDir, 0o755); err != nil {
		t.Fatal(err)
	}

	sessionID := "019d2c0d-ac3c-7b72-bb1d-0f246ad1f0d0"
	repoRoot := "/tmp/test-repo"
	sessionFile := filepath.Join(dateDir, "session-001.jsonl")
	content := fmt.Sprintf(
		`{"timestamp":"2026-04-02T10:00:00Z","type":"session_meta","payload":{"id":"%s","cwd":"%s","cli_version":"0.107.0"}}`+"\n",
		sessionID, repoRoot,
	)
	if err := os.WriteFile(sessionFile, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := findCodexSession(repoRoot, "", "", sessionID)
	if err != nil {
		t.Fatalf("findCodexSession: %v", err)
	}
	if got != sessionFile {
		t.Errorf("got %q, want %q", got, sessionFile)
	}
}

// TestFindCodexSession_DirectLookup_InvalidFallsBack verifies that a
// non-matching session ID gracefully falls back to CWD-based scanning.
// Failure prevented: error instead of fallback when session ID doesn't match any file.
func TestFindCodexSession_DirectLookup_InvalidFallsBack(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	now := time.Now()
	dateDir := filepath.Join(home, ".codex", "sessions",
		now.Format("2006"), now.Format("01"), now.Format("02"))
	if err := os.MkdirAll(dateDir, 0o755); err != nil {
		t.Fatal(err)
	}

	repoRoot := "/tmp/test-repo"
	sessionFile := filepath.Join(dateDir, "session-001.jsonl")
	content := fmt.Sprintf(
		`{"timestamp":"2026-04-02T10:00:00Z","type":"session_meta","payload":{"id":"real-id","cwd":"%s","cli_version":"0.107.0"}}`+"\n",
		repoRoot,
	)
	if err := os.WriteFile(sessionFile, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := findCodexSession(repoRoot, "", "", "nonexistent-session-id")
	if err != nil {
		t.Fatalf("findCodexSession: %v", err)
	}
	if got != sessionFile {
		t.Errorf("got %q, want %q (fallback)", got, sessionFile)
	}
}

// --- C2. mergeToolEntries cross-batch pairing ---

// TestMergeToolEntries_CrossBatchPairing is the regression gate for the
// actual historical bug (see the package doc comment and commit
// "fix(codex): pair tool results to their calls across non-adjacent lines"):
// a call read in one incremental window and its result read in a LATER
// window must still pair, via the pending map serve.go's pendingCallStore
// carries across ReadFromOffset polls. The old merge required strict
// adjacency within a single read, so a call/result split across two
// fsnotify batches surfaced as a nameless orphan result forever.
//
// Failure prevented: the daemon's live tail-watch path silently drops the
// tool name off any call whose result arrives in a later poll than its call
// — most real sessions, since Codex routinely fires several tool calls
// before any of their results return.
func TestMergeToolEntries_CrossBatchPairing(t *testing.T) {
	ts := time.Now()
	call := adapterruntime.ToolUseWithID(ts, "bash", `{"cmd":"ls"}`, "call-1")
	result := adapterruntime.ToolResultWithID(ts, "ls output", false, "call-1")

	pending := map[string]adapterprotocol.RawEntry{}

	// window 1: only the call arrives.
	batch1 := mergeToolEntries([]adapterprotocol.RawEntry{call}, pending)
	if len(batch1) != 1 || batch1[0].ToolName != "bash" || batch1[0].ToolOutput != "" {
		t.Fatalf("window 1 = %+v, want the call unresolved (no output yet)", batch1)
	}
	if _, ok := pending["call-1"]; !ok {
		t.Fatal("call-1 should be recorded as pending after window 1")
	}

	// window 2: only the result arrives, in a LATER, separate call to
	// mergeToolEntries — reusing the SAME pending map, exactly like
	// pendingCallStore.merge does across two ReadFromOffset polls.
	batch2 := mergeToolEntries([]adapterprotocol.RawEntry{result}, pending)
	if len(batch2) != 1 {
		t.Fatalf("window 2 = %+v, want exactly one (labeled) result entry", batch2)
	}
	if batch2[0].ToolName != "bash" {
		t.Errorf("window 2 result ToolName = %q, want %q — the call from an earlier window must label it", batch2[0].ToolName, "bash")
	}
	if batch2[0].ToolOutput != "ls output" {
		t.Errorf("window 2 result ToolOutput = %q, want %q", batch2[0].ToolOutput, "ls output")
	}
	if _, ok := pending["call-1"]; ok {
		t.Error("call-1 should be cleared from pending once its result labels it")
	}
}

// --- C. Tool error detection ---

// TestIsCodexToolError_RealExecCommandFormat is the regression gate for a
// failed command being silently reported as successful. Real
// exec_command/write_stdin output embeds "Process exited with code N" as one
// line inside a multi-line block ("Command: ...\nChunk ID: ...\nWall time:
// ...\nProcess exited with code N\n..."), never as a prefix of the whole
// string — a strict HasPrefix check against the entire output therefore
// never matched, and every real failed command surfaced with IsError false.
// Failure prevented: a failed tool call recorded as successful misleads
// every later reader of the Ledger.
func TestIsCodexToolError_RealExecCommandFormat(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   bool
	}{
		{
			name:   "real exec_command failure",
			output: "Command: /bin/zsh -lc 'ox agent prime'\nChunk ID: 510c80\nWall time: 0.0000 seconds\nProcess exited with code 1\nOriginal token count: 180\nOutput:\nwarning: session recording failed to start\n",
			want:   true,
		},
		{
			name:   "real exec_command success",
			output: "Command: /bin/zsh -lc \"sed -n '1,220p' AGENTS.md\"\nChunk ID: 82a950\nWall time: 0.2035 seconds\nProcess exited with code 0\nOriginal token count: 241\nOutput:\n# Project\n",
			want:   false,
		},
		{name: "bare legacy failure", output: "Process exited with code 1", want: true},
		{name: "bare legacy success", output: "Process exited with code 0", want: false},
		{name: "empty output", output: "", want: false},
		{name: "no exit-code line", output: "some other tool output\nwith no exit code"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isCodexToolError(tc.output); got != tc.want {
				t.Errorf("isCodexToolError(%q) = %v, want %v", tc.output, got, tc.want)
			}
		})
	}
}

// --- B. Timestamp-based fallback (existing behavior) ---

// TestFindCodexSession_TimestampFallback verifies that an empty agent session ID
// preserves the existing CWD-match + most-recent-by-modtime behavior.
// Failure prevented: regression in existing session discovery.
func TestFindCodexSession_TimestampFallback(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	now := time.Now()
	dateDir := filepath.Join(home, ".codex", "sessions",
		now.Format("2006"), now.Format("01"), now.Format("02"))
	if err := os.MkdirAll(dateDir, 0o755); err != nil {
		t.Fatal(err)
	}

	repoRoot := "/tmp/test-repo"

	olderFile := filepath.Join(dateDir, "session-older.jsonl")
	newerFile := filepath.Join(dateDir, "session-newer.jsonl")

	content := fmt.Sprintf(
		`{"timestamp":"2026-04-02T10:00:00Z","type":"session_meta","payload":{"id":"id1","cwd":"%s","cli_version":"0.107.0"}}`+"\n",
		repoRoot,
	)
	if err := os.WriteFile(olderFile, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-1 * time.Hour)
	if err := os.Chtimes(olderFile, past, past); err != nil {
		t.Fatal(err)
	}

	content2 := fmt.Sprintf(
		`{"timestamp":"2026-04-02T11:00:00Z","type":"session_meta","payload":{"id":"id2","cwd":"%s","cli_version":"0.107.0"}}`+"\n",
		repoRoot,
	)
	if err := os.WriteFile(newerFile, []byte(content2), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := findCodexSession(repoRoot, "", "", "")
	if err != nil {
		t.Fatalf("findCodexSession: %v", err)
	}
	if got != newerFile {
		t.Errorf("got %q, want %q (most recent)", got, newerFile)
	}
}
