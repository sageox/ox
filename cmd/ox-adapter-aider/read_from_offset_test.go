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

// --- E. One-shot dispatch wiring ---

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
	path := filepath.Join(dir, aiderHistoryFile)
	content := "# aider chat started at 2024-01-15 14:30:45\n#### hello\nhi there\n"
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
	if len(result.Entries) == 0 {
		t.Fatalf("expected entries from offset 0, got none: %s", out.String())
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

// --- F. Resume exactness ---
//
// These prove the contract the daemon's catch-up read depends on: reading
// from offset 0 returns everything, reading again from the offset that read
// left off at returns nothing new (no re-emitted turns on a live poll), and
// resuming from a mid-transcript offset returns exactly the tail of a full
// read — never a duplicated or a dropped turn.

func writeAiderFixture(t *testing.T, dir string) (path string, full []adapterprotocol.RawEntry) {
	t.Helper()
	path = filepath.Join(dir, aiderHistoryFile)
	content := `# aider chat started at 2024-01-15 14:30:45
#### First question
First answer
> tool ran
#### Second question
Second answer
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	full, err := readAiderFile(path)
	if err != nil {
		t.Fatalf("readAiderFile: %v", err)
	}
	if len(full) < 4 {
		t.Fatalf("fixture too small to prove a mid-transcript resume point: got %d entries", len(full))
	}
	return path, full
}

// TestResumeExact_FromZeroMatchesFullRead verifies reading from offset 0
// through the offset path returns the same entries as a plain full read.
func TestResumeExact_FromZeroMatchesFullRead(t *testing.T) {
	dir := t.TempDir()
	path, full := writeAiderFixture(t, dir)

	got, _, err := readAiderFromOffset(path, 0)
	if err != nil {
		t.Fatalf("readAiderFromOffset(0): %v", err)
	}
	if len(got) != len(full) {
		t.Fatalf("offset-0 read returned %d entries, full read returned %d", len(got), len(full))
	}
	for i := range full {
		if got[i].Content != full[i].Content || got[i].Role != full[i].Role {
			t.Fatalf("entry %d differs: got %+v, want %+v", i, got[i], full[i])
		}
	}
}

// TestResumeExact_AtEndOffsetReturnsNothingNew verifies that resuming at the
// offset a prior read left off at returns zero entries — the shape a live
// poll loop depends on to avoid re-emitting turns forever.
func TestResumeExact_AtEndOffsetReturnsNothingNew(t *testing.T) {
	dir := t.TempDir()
	path, _ := writeAiderFixture(t, dir)

	_, endOffset, err := readAiderFromOffset(path, 0)
	if err != nil {
		t.Fatalf("readAiderFromOffset(0): %v", err)
	}

	got, newOffset, err := readAiderFromOffset(path, endOffset)
	if err != nil {
		t.Fatalf("readAiderFromOffset(endOffset): %v", err)
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
	path, full := writeAiderFixture(t, dir)

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// resume point: the byte offset right after "First answer\n" — this must
	// skip the first user+assistant pair (2 entries) and return exactly the
	// remaining 2 (tool output + second question/answer collapse to 2 entries
	// under the parser's role-transition-flush model).
	marker := "First answer\n"
	idx := strings.Index(string(raw), marker)
	if idx == -1 {
		t.Fatal("fixture marker not found — test fixture drifted from writeAiderFixture")
	}
	resumeOffset := int64(idx + len(marker))

	got, newOffset, err := readAiderFromOffset(path, resumeOffset)
	if err != nil {
		t.Fatalf("readAiderFromOffset(mid): %v", err)
	}
	if len(got) == 0 || len(got) >= len(full) {
		t.Fatalf("resume point skipped nothing (or returned everything): got %d of %d entries — offset is being ignored", len(got), len(full))
	}
	want := full[len(full)-len(got):]
	for i := range want {
		if got[i].Content != want[i].Content || got[i].Role != want[i].Role {
			t.Fatalf("entry %d is not the tail of the full read: got %+v, want %+v", i, got[i], want[i])
		}
	}
	if newOffset <= resumeOffset {
		t.Errorf("newOffset %d should advance past resumeOffset %d", newOffset, resumeOffset)
	}
}
