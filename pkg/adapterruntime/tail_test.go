package adapterruntime

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/pkg/adapterprotocol"
)

// testParse models what a real adapter's line parser does: understand some
// record types, ignore the rest.
func testParse(line []byte) ([]adapterprotocol.RawEntry, error) {
	var rec struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(line, &rec); err != nil {
		return nil, err
	}
	if rec.Type != "message" {
		return nil, errors.New("not a message")
	}
	return []adapterprotocol.RawEntry{UserEntry(time.Unix(0, 0).UTC(), rec.Text)}, nil
}

func writeFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func appendTo(t *testing.T, path, content string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.WriteString(content); err != nil {
		t.Fatal(err)
	}
}

func line(text string) string {
	return `{"type":"message","text":"` + text + `"}` + "\n"
}

func texts(entries []adapterprotocol.RawEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Content)
	}
	return out
}

func TestTailJSONL_ReadsEveryRecordFromTheStart(t *testing.T) {
	path := writeFile(t, line("one")+line("two")+line("three"))

	entries, offset, err := TailJSONL(path, 0, testParse)
	if err != nil {
		t.Fatal(err)
	}
	if got := texts(entries); strings.Join(got, ",") != "one,two,three" {
		t.Errorf("got %v", got)
	}
	info, _ := os.Stat(path)
	if offset != info.Size() {
		t.Errorf("offset %d, want the full file size %d", offset, info.Size())
	}
}

func TestTailJSONL_ResumeReturnsOnlyWhatIsNew(t *testing.T) {
	path := writeFile(t, line("one")+line("two"))

	_, offset, err := TailJSONL(path, 0, testParse)
	if err != nil {
		t.Fatal(err)
	}

	appendTo(t, path, line("three"))

	entries, _, err := TailJSONL(path, offset, testParse)
	if err != nil {
		t.Fatal(err)
	}
	if got := texts(entries); strings.Join(got, ",") != "three" {
		t.Errorf("resume returned %v, want just the new record — anything else double-counts or drops turns", got)
	}
}

// TestTailJSONL_DoesNotConsumeAPartialLine is the defect every per-adapter copy
// carried: the offset jumped to the file size even when the agent was midway
// through writing the last record, so the rest of that turn was skipped
// permanently once it landed.
func TestTailJSONL_DoesNotConsumeAPartialLine(t *testing.T) {
	complete := line("one")
	partial := `{"type":"message","text":"tw`
	path := writeFile(t, complete+partial)

	entries, offset, err := TailJSONL(path, 0, testParse)
	if err != nil {
		t.Fatal(err)
	}
	if got := texts(entries); strings.Join(got, ",") != "one" {
		t.Fatalf("got %v, want only the complete record", got)
	}
	if offset != int64(len(complete)) {
		t.Fatalf("offset %d consumed the partial line; want %d so the record is re-read once complete",
			offset, len(complete))
	}

	// the agent finishes the record
	appendTo(t, path, `o"}`+"\n")

	entries, _, err = TailJSONL(path, offset, testParse)
	if err != nil {
		t.Fatal(err)
	}
	if got := texts(entries); strings.Join(got, ",") != "two" {
		t.Errorf("after completion got %v, want the finished record — this turn was lost", got)
	}
}

func TestTailJSONL_SkipsUnparseableRecordsWithoutAbandoningTheRead(t *testing.T) {
	path := writeFile(t, line("one")+`{"type":"telemetry"}`+"\n"+"not json at all\n"+line("two"))

	entries, _, err := TailJSONL(path, 0, testParse)
	if err != nil {
		t.Fatalf("one unknown record aborted the whole read: %v", err)
	}
	if got := texts(entries); strings.Join(got, ",") != "one,two" {
		t.Errorf("got %v, want both messages", got)
	}
}

func TestTailJSONL_ReadsRecordsLargerThanTheBufferSize(t *testing.T) {
	// a tool result carrying a whole file is routine and blew the default
	// 64 KiB scanner limit
	big := strings.Repeat("x", 300*1024)
	path := writeFile(t, line(big))

	entries, _, err := TailJSONL(path, 0, testParse)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	if len(entries[0].Content) != len(big) {
		t.Errorf("record truncated: got %d bytes, want %d", len(entries[0].Content), len(big))
	}
}

// TestTailJSONL_RestartsWhenTheFileShrank covers a transcript that was replaced
// or truncated. Resuming at a stale offset would land mid-record in unrelated
// content.
func TestTailJSONL_RestartsWhenTheFileShrank(t *testing.T) {
	path := writeFile(t, line("one")+line("two")+line("three"))
	_, offset, err := TailJSONL(path, 0, testParse)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, []byte(line("fresh")), 0o600); err != nil {
		t.Fatal(err)
	}

	entries, _, err := TailJSONL(path, offset, testParse)
	if err != nil {
		t.Fatal(err)
	}
	if got := texts(entries); strings.Join(got, ",") != "fresh" {
		t.Errorf("got %v, want the replacement transcript read from the start", got)
	}
}

func TestTailJSONL_ReadingAtEndReturnsNothing(t *testing.T) {
	path := writeFile(t, line("one"))
	_, offset, err := TailJSONL(path, 0, testParse)
	if err != nil {
		t.Fatal(err)
	}

	entries, newOffset, err := TailJSONL(path, offset, testParse)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("got %d entries at the end offset — a live poll would re-emit them forever", len(entries))
	}
	if newOffset != offset {
		t.Errorf("offset moved from %d to %d with nothing read", offset, newOffset)
	}
}

func TestTailJSONL_HandlesCRLF(t *testing.T) {
	path := writeFile(t, `{"type":"message","text":"one"}`+"\r\n")

	entries, _, err := TailJSONL(path, 0, testParse)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Content != "one" {
		t.Errorf("got %v, want one record with a stripped carriage return", texts(entries))
	}
}

func TestTailJSONL_ReportsAMissingFile(t *testing.T) {
	if _, _, err := TailJSONL(filepath.Join(t.TempDir(), "nope.jsonl"), 0, testParse); err == nil {
		t.Error("a missing transcript returned no error — the caller cannot tell it from an idle session")
	}
}

func TestTailJSONL_RequiresAParser(t *testing.T) {
	path := writeFile(t, line("one"))
	if _, _, err := TailJSONL(path, 0, nil); err == nil {
		t.Error("a nil parser silently read nothing")
	}
}

func TestReadFromOffsetJSONL_ReturnsTheProtocolShape(t *testing.T) {
	path := writeFile(t, line("one")+line("two"))

	res, err := ReadFromOffsetJSONL(adapterprotocol.ReadFromOffsetParams{SessionFile: path}, testParse)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Entries) != 2 {
		t.Errorf("got %d entries, want 2", len(res.Entries))
	}
	info, _ := os.Stat(path)
	if res.NewOffset != info.Size() {
		t.Errorf("NewOffset %d, want %d", res.NewOffset, info.Size())
	}
}
