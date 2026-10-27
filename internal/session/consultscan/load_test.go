package consultscan

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// rawFixture is a raw.jsonl body with every line kind the loader must frame:
// header, _meta header, entries, a malformed line, a blank line, a footer.
var rawFixture = []string{
	`{"type":"header","metadata":{"version":"1.0"}}`,
	`{"_meta":{"schema_version":"1"}}`,
	`{"type":"user","content":"hi"}`,
	`{"type":"assistant","content":"Ox7f2a"}`,
	`garbage`,
	``,
	`{"type":"tool","tool_name":"Read","tool_input":{"file_path":"/x/team/MEMORY.md"}}`,
	`{"type":"footer","stopped_at":"x"}`,
	`{"type":"assistant","content":"after footer"}`,
}

// TestLoadRawEntriesFrom_ChunkedMatchesOneShot proves the incremental loader
// numbers entries exactly as a one-shot load does, however the file is split
// across calls — including a call that lands mid-line while the line is still
// being written. Failure prevented: a live tail of raw.jsonl assigns a
// different seq to a turn than the finalize-time scan, so a `used` event is
// judged before (or after) the turn its item was surfaced on.
func TestLoadRawEntriesFrom_ChunkedMatchesOneShot(t *testing.T) {
	dir := t.TempDir()
	full := filepath.Join(dir, "full.jsonl")
	body := joinLines(rawFixture)
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	want, err := LoadRawEntries(full)
	if err != nil {
		t.Fatal(err)
	}
	if len(want) != 5 {
		t.Fatalf("one-shot got %d entries, want 5: %+v", len(want), want)
	}

	// Grow a second file a few bytes at a time and tail it after every write.
	growing := filepath.Join(dir, "growing.jsonl")
	if err := os.WriteFile(growing, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	var got []Entry
	var off int64
	idx := 0
	for n := 0; n < len(body); {
		step := min(7, len(body)-n)
		appendBytes(t, growing, body[n:n+step])
		n += step
		entries, nextOff, nextIdx, err := LoadRawEntriesFrom(growing, off, idx)
		if err != nil {
			t.Fatalf("at byte %d: %v", n, err)
		}
		if nextIdx != idx+len(entries) {
			t.Fatalf("nextIndex = %d, want %d", nextIdx, idx+len(entries))
		}
		got = append(got, entries...)
		off, idx = nextOff, nextIdx
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("chunked entries differ from one-shot:\n got %+v\nwant %+v", got, want)
	}
	if off != int64(len(body)) || idx != len(want) {
		t.Errorf("final offset/index = %d/%d, want %d/%d", off, idx, len(body), len(want))
	}

	// Nothing new: same position back, no entries.
	entries, nextOff, nextIdx, err := LoadRawEntriesFrom(growing, off, idx)
	if err != nil || len(entries) != 0 || nextOff != off || nextIdx != idx {
		t.Errorf("idle tail = %+v, %d, %d, %v; want nothing at %d/%d", entries, nextOff, nextIdx, err, off, idx)
	}
}

// TestLoadRawEntriesFrom_TrailingLine pins the half-written-line rule: an
// unterminated last line is left for the next call unless it is already a
// complete JSON record.
func TestLoadRawEntriesFrom_TrailingLine(t *testing.T) {
	dir := t.TempDir()
	raw := filepath.Join(dir, "raw.jsonl")
	first := `{"type":"user","content":"hi"}` + "\n"
	partial := `{"type":"assistant","con`
	if err := os.WriteFile(raw, []byte(first+partial), 0o644); err != nil {
		t.Fatal(err)
	}
	entries, off, idx, err := LoadRawEntriesFrom(raw, 0, 0)
	if err != nil || len(entries) != 1 || off != int64(len(first)) || idx != 1 {
		t.Fatalf("got %+v, %d, %d, %v; want the one complete line", entries, off, idx, err)
	}

	appendBytes(t, raw, `tent":"done"}`) // complete JSON, still no newline
	entries, off, idx, err = LoadRawEntriesFrom(raw, off, idx)
	if err != nil || len(entries) != 1 || entries[0].Content != "done" || idx != 2 {
		t.Fatalf("got %+v, %d, %d, %v; want the now-complete record", entries, off, idx, err)
	}
	if fi, _ := os.Stat(raw); off != fi.Size() {
		t.Errorf("offset = %d, want %d (end of file)", off, fi.Size())
	}
}

// TestLoadRawEntriesFrom_Errors covers the fail-open and error paths.
func TestLoadRawEntriesFrom_Errors(t *testing.T) {
	dir := t.TempDir()

	entries, off, idx, err := LoadRawEntriesFrom(filepath.Join(dir, "nope.jsonl"), 12, 3)
	if err != nil || entries != nil || off != 12 || idx != 3 {
		t.Errorf("missing file = %+v, %d, %d, %v; want nil at 12/3, no error", entries, off, idx, err)
	}

	raw := filepath.Join(dir, "raw.jsonl")
	if err := os.WriteFile(raw, []byte(`{"type":"user"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	entries, off, idx, err = LoadRawEntriesFrom(raw, 1000, 4)
	if err != nil || len(entries) != 0 || off != 1000 || idx != 4 {
		t.Errorf("offset past EOF = %+v, %d, %d, %v; want nothing at 1000/4", entries, off, idx, err)
	}

	if _, _, _, err := LoadRawEntriesFrom(raw, -1, 0); err == nil {
		t.Error("negative offset: want error")
	}
	if _, _, _, err := LoadRawEntriesFrom(dir, 0, 0); err == nil {
		t.Error("directory: want read error")
	}
	if _, err := LoadRawEntries(dir); err == nil {
		t.Error("LoadRawEntries on a directory: want read error")
	}
}

func appendBytes(t *testing.T, path, s string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(s); err != nil {
		t.Fatal(err)
	}
}
