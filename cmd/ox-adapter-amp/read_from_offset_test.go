package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/sageox/ox/pkg/adapterruntime"
)

// --- D. One-shot dispatch wiring ---

// TestDispatch_ReadFromOffset_Wired drives the exact dispatch table main()
// builds — not a hand-copied stand-in — through the one-shot "read-from-
// offset" subcommand, the same path the daemon's catch-up read uses via
// ExternalAdapter.ReadFromOffset (internal/session/adapters/external.go).
//
// Failure prevented: Config.ReadFromOffset was nil, so this subcommand
// always answered {"error":"read-from-offset not implemented"} even though
// serve.go's srv.OnReadFromOffset was correctly wired. The daemon's catch-up
// read (internal/daemon/agentwork/session_watcher.go: runWatcher) always
// goes through the one-shot path, so every restart silently dropped turns
// written since the last persisted offset.
func TestDispatch_ReadFromOffset_Wired(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "thread-1.jsonl")
	content := `{"type":"user","timestamp":"2024-01-15T10:00:00Z","content":"hello"}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	args := []string{"read-from-offset", "--session-file", path, "--offset", "0"}
	if err := adapterruntime.RunWithArgs(newConfig(), args, nil, &out); err != nil {
		t.Fatalf("read-from-offset failed: %v\nresponse: %s", err, out.String())
	}

	if strings.Contains(out.String(), "not implemented") {
		t.Fatalf("read-from-offset reports not implemented: %s", out.String())
	}

	var result adapterprotocol.ReadFromOffsetResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("read-from-offset did not return JSON: %v\nresponse: %s", err, out.String())
	}
	if len(result.Entries) != 1 {
		t.Fatalf("got %d entries, want 1: %s", len(result.Entries), out.String())
	}
}

// TestDispatch_ReadFromOffset_MissingFileReportsRealProblem proves the
// probe shape the contract test uses (a nonexistent session file) surfaces
// a genuine I/O error rather than the "not implemented" sentinel — the
// signal the contract test's capabilityProbe relies on to distinguish an
// unwired handler from a wired one hitting a real problem.
func TestDispatch_ReadFromOffset_MissingFileReportsRealProblem(t *testing.T) {
	var out bytes.Buffer
	args := []string{"read-from-offset", "--session-file", "/nonexistent/ox-probe.jsonl", "--offset", "0"}
	err := adapterruntime.RunWithArgs(newConfig(), args, nil, &out)
	if err == nil {
		t.Fatal("expected an error for a nonexistent session file")
	}
	if strings.Contains(out.String(), "not implemented") {
		t.Fatalf("a missing file must report a real I/O problem, not 'not implemented': %s", out.String())
	}
	if !strings.Contains(out.String(), "failed to open session file") {
		t.Errorf("expected an open-failure message, got: %s", out.String())
	}
}

// --- E. Resume exactness ---
//
// These prove the contract the daemon's catch-up read depends on: reading
// from offset 0 returns everything, reading again from the offset that read
// left off at returns nothing new (no re-emitted turns on a live poll), and
// resuming from a mid-transcript offset returns exactly the tail of a full
// read — never a duplicated or a dropped turn.

func ampFixtureLines() []string {
	return []string{
		`{"type":"session_meta","timestamp":"2024-01-15T09:59:59Z","session_id":"t-1"}`,
		`{"type":"user","timestamp":"2024-01-15T10:00:00Z","content":"first question"}`,
		`{"type":"tool_use","timestamp":"2024-01-15T10:00:01Z","tool_name":"read_file","tool_input":"a.go","call_id":"call-1"}`,
		`{"type":"tool_result","timestamp":"2024-01-15T10:00:02Z","content":"package main","call_id":"call-1"}`,
		`{"type":"assistant","timestamp":"2024-01-15T10:00:03Z","content":"here is the answer"}`,
	}
}

func writeAmpFixture(t *testing.T, dir string) (path string, full []adapterprotocol.RawEntry) {
	t.Helper()
	path = filepath.Join(dir, "thread-1.jsonl")
	lines := ampFixtureLines()
	content := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	full, err := readAmpFile(path)
	if err != nil {
		t.Fatalf("readAmpFile: %v", err)
	}
	// session_meta produces no RawEntry, so the fixture's 5 lines yield 4
	// entries: user, tool_use, tool_result, assistant.
	if len(full) != 4 {
		t.Fatalf("fixture drifted: got %d entries, want 4", len(full))
	}
	return path, full
}

// TestResumeExact_FromZeroMatchesFullRead verifies reading from offset 0
// through the offset path returns the same entries as a plain full read.
func TestResumeExact_FromZeroMatchesFullRead(t *testing.T) {
	dir := t.TempDir()
	path, full := writeAmpFixture(t, dir)

	got, _, err := readAmpFromOffset(path, 0)
	if err != nil {
		t.Fatalf("readAmpFromOffset(0): %v", err)
	}
	if len(got) != len(full) {
		t.Fatalf("offset-0 read returned %d entries, full read returned %d", len(got), len(full))
	}
	for i := range full {
		if got[i] != full[i] {
			t.Fatalf("entry %d differs: got %+v, want %+v", i, got[i], full[i])
		}
	}
}

// TestResumeExact_AtEndOffsetReturnsNothingNew verifies that resuming at the
// offset a prior read left off at returns zero entries — the shape a live
// poll loop depends on to avoid re-emitting turns forever.
func TestResumeExact_AtEndOffsetReturnsNothingNew(t *testing.T) {
	dir := t.TempDir()
	path, _ := writeAmpFixture(t, dir)

	_, endOffset, err := readAmpFromOffset(path, 0)
	if err != nil {
		t.Fatalf("readAmpFromOffset(0): %v", err)
	}

	got, newOffset, err := readAmpFromOffset(path, endOffset)
	if err != nil {
		t.Fatalf("readAmpFromOffset(endOffset): %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("re-reading from the end offset returned %d entries, want 0 (would re-emit forever on a live poll)", len(got))
	}
	if newOffset != endOffset {
		t.Errorf("newOffset = %d, want unchanged %d", newOffset, endOffset)
	}
}

// TestResumeExact_MidTranscriptReturnsExactTail verifies a mid-transcript
// resume point returns exactly the tail of a full read — no duplicated and
// no skipped turns across the boundary.
func TestResumeExact_MidTranscriptReturnsExactTail(t *testing.T) {
	dir := t.TempDir()
	path, full := writeAmpFixture(t, dir)
	lines := ampFixtureLines()

	// resume right after line 2 (session_meta + user) — must skip the user
	// entry and return exactly the remaining 3 (tool_use, tool_result,
	// assistant).
	resumeOffset := int64(len(lines[0]) + 1 + len(lines[1]) + 1)

	got, newOffset, err := readAmpFromOffset(path, resumeOffset)
	if err != nil {
		t.Fatalf("readAmpFromOffset(mid): %v", err)
	}
	if len(got) == 0 || len(got) >= len(full) {
		t.Fatalf("resume point skipped nothing (or returned everything): got %d of %d entries — offset is being ignored", len(got), len(full))
	}
	want := full[len(full)-len(got):]
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("entry %d is not the tail of the full read: got %+v, want %+v", i, got[i], want[i])
		}
	}
	if newOffset <= resumeOffset {
		t.Errorf("newOffset %d should advance past resumeOffset %d", newOffset, resumeOffset)
	}
}

// TestResumeExact_PartialTrailingLineNotConsumed proves the bug the switch
// to adapterruntime.TailJSONL fixed: the private seek-and-scan loop this
// replaced advanced newOffset to the file's current size even when the
// final line was a partial write-in-progress (no trailing newline yet),
// so the rest of that turn was silently dropped once the agent finished
// writing it. TailJSONL must instead stop at the last complete newline and
// leave the partial line unconsumed for the next read.
func TestResumeExact_PartialTrailingLineNotConsumed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "thread-1.jsonl")

	complete := `{"type":"user","timestamp":"2024-01-15T10:00:00Z","content":"first"}` + "\n"
	partial := `{"type":"assistant","timestamp":"2024-01-15T10:00:01Z","content":"in-fli` // no closing brace, no newline — mid-write
	if err := os.WriteFile(path, []byte(complete+partial), 0o644); err != nil {
		t.Fatal(err)
	}

	entries, newOffset, err := readAmpFromOffset(path, 0)
	if err != nil {
		t.Fatalf("readAmpFromOffset: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1 (only the complete line)", len(entries))
	}
	if newOffset != int64(len(complete)) {
		t.Fatalf("newOffset = %d, want %d (offset of the complete line only — the partial trailing write must stay unconsumed)", newOffset, len(complete))
	}

	// simulate the agent finishing the write
	full := `{"type":"assistant","timestamp":"2024-01-15T10:00:01Z","content":"in-flight, now complete"}` + "\n"
	if err := os.WriteFile(path, []byte(complete+full), 0o644); err != nil {
		t.Fatal(err)
	}

	entries, _, err = readAmpFromOffset(path, newOffset)
	if err != nil {
		t.Fatalf("readAmpFromOffset resume: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries after the write completed, want 1 (the previously-partial turn must not be lost)", len(entries))
	}
	if entries[0].Content != "in-flight, now complete" {
		t.Errorf("content = %q, want the completed turn's content", entries[0].Content)
	}
}
