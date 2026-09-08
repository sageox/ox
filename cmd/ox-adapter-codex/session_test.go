package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/sageox/ox/pkg/adapterruntime"
)

// TestToolCallsSurviveReadCommands covers the string and content-block outputs
// written by Codex 0.153.2 in Conductor. Custom calls were silently omitted;
// array outputs failed JSON decoding even for ordinary function calls.
func TestToolCallsSurviveReadCommands(t *testing.T) {
	for _, call := range []struct {
		kind     string
		inputKey string
		input    string
	}{
		{kind: "function_call", inputKey: "arguments", input: `{"cmd":"go test ./..."}`},
		{kind: "custom_tool_call", inputKey: "input", input: "text(await tools.exec_command({cmd: 'go test ./...'}));"},
	} {
		for _, output := range []struct {
			name    string
			json    string
			text    string
			isError bool
		}{
			{name: "string", json: `"Script completed\nPASS"`, text: "Script completed\nPASS"},
			{name: "empty string", json: `""`},
			{name: "empty content blocks", json: `[]`},
			{
				name: "content blocks",
				json: `[{"type":"input_text","text":"Script failed"},{"type":"input_image","image_url":"data:image/png;base64,AAAA"},{"type":"input_text","text":"Process exited with code 1\nFAIL"}]`,
				text: "Script failed\nProcess exited with code 1\nFAIL", isError: true,
			},
		} {
			for _, command := range []string{"read", "read-from-offset"} {
				t.Run(call.kind+"/"+output.name+"/"+command, func(t *testing.T) {
					path := filepath.Join(t.TempDir(), "session.jsonl")
					content := fmt.Sprintf(`{"timestamp":"2026-09-07T16:50:00Z","type":"response_item","payload":{"type":%q,"name":"exec","call_id":"call-1",%q:%q}}
{"timestamp":"2026-09-07T16:50:01Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Checking the tests."}]}}
{"timestamp":"2026-09-07T16:50:02Z","type":"response_item","payload":{"type":%q,"call_id":"call-1","output":%s}}
`, call.kind, call.inputKey, call.input, call.kind+"_output", output.json)
					if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
						t.Fatal(err)
					}
					var buf bytes.Buffer
					args := []string{command, "--session-file", path}
					if command == "read-from-offset" {
						args = append(args, "--offset", "0")
					}
					if err := adapterruntime.RunWithArgs(adapterConfig, args, nil, &buf); err != nil {
						t.Fatal(err)
					}
					var result adapterprotocol.ReadFromOffsetResult
					if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
						t.Fatal(err)
					}
					if len(result.Entries) != 2 {
						t.Fatalf("entries = %+v, want paired tool call and assistant message", result.Entries)
					}
					tool := result.Entries[0]
					if tool.Role != adapterprotocol.RoleTool || tool.ToolName != "exec" || tool.CallID != "call-1" || tool.ToolInput != call.input || tool.ToolOutput != output.text || tool.IsError != output.isError {
						t.Fatalf("tool = %+v, want call-1 with input %q, output %q, is_error=%v", tool, call.input, output.text, output.isError)
					}
					if command == "read-from-offset" && result.NewOffset != int64(len(content)) {
						t.Fatalf("offset = %d, want %d", result.NewOffset, len(content))
					}
				})
			}
		}
	}
}

// TestCustomToolResultsPairAcrossReads prevents delayed custom tool results
// losing their name and input when calls and results arrive in separate polls.
func TestCustomToolResultsPairAcrossReads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	calls := `{"timestamp":"2026-09-07T16:50:00Z","type":"response_item","payload":{"type":"custom_tool_call","name":"exec","call_id":"custom-1","input":"text(await tools.exec_command({cmd: 'go test ./...'}));"}}
{"timestamp":"2026-09-07T16:50:01Z","type":"response_item","payload":{"type":"function_call","name":"write_stdin","call_id":"function-1","arguments":"{\"session_id\":123}"}}
`
	if err := os.WriteFile(path, []byte(calls), 0o600); err != nil {
		t.Fatal(err)
	}
	pending := newPendingCallStore()
	entries, offset, err := readCodexFromOffset(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	first := pending.merge(path, entries)
	if len(first) != 2 || first[0].ToolName != "exec" || first[1].ToolName != "write_stdin" {
		t.Fatalf("first read = %+v, want both pending calls", first)
	}
	outputs := `{"timestamp":"2026-09-07T16:50:02Z","type":"response_item","payload":{"type":"function_call_output","call_id":"function-1","output":[{"type":"input_text","text":"tests still running"}]}}
{"timestamp":"2026-09-07T16:50:03Z","type":"response_item","payload":{"type":"custom_tool_call_output","call_id":"custom-1","output":[{"type":"input_text","text":"Script completed"},{"type":"input_text","text":"PASS"}]}}
`
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, writeErr := f.WriteString(outputs)
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil {
		t.Fatalf("append outputs: write=%v close=%v", writeErr, closeErr)
	}
	entries, newOffset, err := readCodexFromOffset(path, offset)
	if err != nil {
		t.Fatal(err)
	}
	second := pending.merge(path, entries)
	if len(second) != 2 || second[0].ToolName != "write_stdin" || second[0].ToolInput != `{"session_id":123}` || second[0].ToolOutput != "tests still running" || second[1].ToolName != "exec" || second[1].ToolInput != "text(await tools.exec_command({cmd: 'go test ./...'}));" || second[1].ToolOutput != "Script completed\nPASS" {
		t.Fatalf("second read = %+v, want results paired with earlier calls", second)
	}
	if newOffset != int64(len(calls)+len(outputs)) {
		t.Fatalf("offset = %d, want %d", newOffset, len(calls)+len(outputs))
	}
}

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

// A delayed native session must not bind its recording to a sibling conversation.
func TestFindCodexSession_DirectLookup_WaitsForRequestedSession(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	now := time.Now()
	dateDir := filepath.Join(home, ".codex", "sessions",
		now.Format("2006"), now.Format("01"), now.Format("02"))
	if err := os.MkdirAll(dateDir, 0o755); err != nil {
		t.Fatal(err)
	}

	repoRoot := "/tmp/test-repo"
	siblingFile := filepath.Join(dateDir, "session-sibling.jsonl")
	content := fmt.Sprintf(
		`{"timestamp":"2026-04-02T10:00:00Z","type":"session_meta","payload":{"id":"sibling-id","cwd":"%s","cli_version":"0.107.0"}}`+"\n",
		repoRoot,
	)
	if err := os.WriteFile(siblingFile, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	sessionID := "019d2c0d-ac3c-7b72-bb1d-0f246ad1f0d0"
	since := now.Add(-5 * time.Minute).Format(time.RFC3339)
	got, err := findCodexSession(repoRoot, "", since, sessionID)
	if err == nil || got != "" {
		t.Fatalf("missing native session = %q, %v; must wait instead of selecting %q", got, err, siblingFile)
	}

	sessionFile := filepath.Join(dateDir, "session-requested.jsonl")
	content = fmt.Sprintf(`{"type":"session_meta","payload":{"id":%q,"cwd":%q}}`+"\n", sessionID, repoRoot)
	if err := os.WriteFile(sessionFile, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err = findCodexSession(repoRoot, "", since, sessionID)
	if err != nil {
		t.Fatalf("discover delayed native session: %v", err)
	}
	if got != sessionFile {
		t.Errorf("got %q, want requested session %q", got, sessionFile)
	}
}

// Malformed native identities are rejected rather than triggering a heuristic lookup.
func TestFindCodexSession_DirectLookup_RejectsMalformedID(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, id := range []string{"../session", "/absolute", "nested/session", `nested\session`} {
		t.Run(id, func(t *testing.T) {
			got, err := findCodexSession("/tmp/test-repo", "", "", id)
			if err == nil || got != "" || !strings.Contains(err.Error(), "invalid session ID") {
				t.Fatalf("malformed native session ID %q = %q, %v; want error", id, got, err)
			}
		})
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
