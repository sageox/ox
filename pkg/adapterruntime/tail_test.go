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

// TestTailJSONL_RestartsWhenTheFileWasReplaced covers the cases a size
// comparison misses: the replacement is LONGER than the stale offset, or
// exactly the same size. Both would otherwise resume inside an unrelated
// record and silently lose the start of the new transcript.
func TestTailJSONL_RestartsWhenTheFileWasReplaced(t *testing.T) {
	tests := []struct {
		name        string
		replacement string
		want        string
	}{
		{
			name:        "replacement is longer than the stale offset",
			replacement: line("fresh-one") + line("fresh-two") + line("fresh-three") + line("fresh-four"),
			want:        "fresh-one,fresh-two,fresh-three,fresh-four",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			original := line("one") + line("two")
			path := writeFile(t, original)

			_, offset, err := TailJSONL(path, 0, testParse)
			if err != nil {
				t.Fatal(err)
			}

			if err := os.WriteFile(path, []byte(tt.replacement), 0o600); err != nil {
				t.Fatal(err)
			}

			entries, _, err := TailJSONL(path, offset, testParse)
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Join(texts(entries), ","); got != tt.want {
				t.Errorf("got %q, want %q — the replacement transcript must be read from the start", got, tt.want)
			}
		})
	}
}

// TestTailJSONL_CannotDetectASameSizeReplacement records a limitation the
// boundary check cannot close, so nobody rediscovers it as a mystery bug.
//
// A byte offset carries no file identity. When a transcript is replaced in
// place by one of exactly the same size, the stale offset still lands on a
// record boundary and still equals the file size, so the read is
// indistinguishable from "already caught up" and the replacement is never read.
//
// Closing this needs identity the offset does not carry — an inode or a content
// hash persisted alongside it. That is a change to what the watcher stores, not
// something this function can decide, so this asserts the CURRENT behavior and
// names the gap rather than pretending it is handled.
func TestTailJSONL_CannotDetectASameSizeReplacement(t *testing.T) {
	original := line("one") + line("two")
	path := writeFile(t, original)

	_, offset, err := TailJSONL(path, 0, testParse)
	if err != nil {
		t.Fatal(err)
	}

	replacement := line("aaa") + line("bbb") // same byte count, different content
	if len(replacement) != len(original) {
		t.Fatalf("test setup is wrong: %d != %d", len(replacement), len(original))
	}
	if err := os.WriteFile(path, []byte(replacement), 0o600); err != nil {
		t.Fatal(err)
	}

	entries, _, err := TailJSONL(path, offset, testParse)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("a same-size replacement is now detected — delete this test and fold the case into TestTailJSONL_RestartsWhenTheFileWasReplaced")
	}
	t.Log("KNOWN GAP: a same-size in-place replacement reads as already-consumed. " +
		"Needs file identity (inode or content hash) persisted with the offset.")
}

// TestTailJSONL_RestartsFromAMidRecordOffset covers a corrupted persisted
// offset pointing into the middle of a record.
func TestTailJSONL_RestartsFromAMidRecordOffset(t *testing.T) {
	path := writeFile(t, line("one")+line("two"))

	entries, _, err := TailJSONL(path, 7, testParse) // deliberately mid-record
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(texts(entries), ","); got != "one,two" {
		t.Errorf("got %q, want the whole file re-read — a mid-record offset is not resumable", got)
	}
}

func TestTailJSONL_RestartsFromANegativeOffset(t *testing.T) {
	path := writeFile(t, line("one"))

	entries, offset, err := TailJSONL(path, -5, testParse)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(texts(entries), ","); got != "one" {
		t.Errorf("got %q, want the file read from the start", got)
	}
	info, _ := os.Stat(path)
	if offset != info.Size() {
		t.Errorf("offset %d, want %d — a negative offset must not leave the reader permanently short", offset, info.Size())
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

// TestTailJSONLWithStats_ReportsADeadParser covers the failure this whole
// package exists to prevent. A parser that matches nothing the agent writes
// returns zero entries, no error, and an offset advanced to EOF — identical
// from the outside to an idle session. The counts are what make it visible
// without needing a fixture.
func TestTailJSONLWithStats_ReportsADeadParser(t *testing.T) {
	path := writeFile(t, line("one")+line("two")+line("three"))

	deadParser := func([]byte) ([]adapterprotocol.RawEntry, error) {
		return nil, errors.New("no field matched")
	}

	entries, _, stats, err := TailJSONLWithStats(path, 0, deadParser)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected the dead parser to yield nothing, got %d", len(entries))
	}
	if stats.LinesRead != 3 {
		t.Errorf("LinesRead = %d, want 3 — without this the read looks idle", stats.LinesRead)
	}
	if stats.ParseErrors != 3 {
		t.Errorf("ParseErrors = %d, want 3", stats.ParseErrors)
	}
	if !stats.AllLinesFailedToParse() {
		t.Error("AllLinesFailedToParse() = false for a read that understood none of 3 records — this is the signal that would have caught all six broken adapters on their first real run")
	}
}

func TestTailJSONLWithStats_HealthyReadIsNotFlaggedAsDead(t *testing.T) {
	path := writeFile(t, line("one")+`{"type":"telemetry"}`+"\n"+line("two"))

	_, _, stats, err := TailJSONLWithStats(path, 0, testParse)
	if err != nil {
		t.Fatal(err)
	}
	if stats.AllLinesFailedToParse() {
		t.Error("a read that parsed 2 of 3 records was flagged as dead — one unmodeled record type is normal")
	}
	if stats.EntriesParsed != 2 || stats.ParseErrors != 1 {
		t.Errorf("stats = %+v, want 2 parsed and 1 error", stats)
	}
}

// TestTailJSONL_DoesNotConsumeAnOversizedPartialRecord covers the bug where a
// record above the size cap had its io.EOF swallowed, so an unterminated
// oversized record was consumed as if complete and the rest of that turn was
// lost once the agent finished writing it.
func TestTailJSONL_DoesNotConsumeAnOversizedPartialRecord(t *testing.T) {
	complete := line("one")
	// oversized AND unterminated: the agent is still writing it
	partial := `{"type":"message","text":"` + strings.Repeat("x", maxLineBytes+1024)
	path := writeFile(t, complete+partial)

	entries, offset, _, err := TailJSONLWithStats(path, 0, testParse)
	if err != nil {
		t.Fatal(err)
	}
	if got := texts(entries); strings.Join(got, ",") != "one" {
		t.Errorf("got %v, want only the complete record", got)
	}
	if offset != int64(len(complete)) {
		t.Errorf("offset %d consumed an unterminated oversized record; want %d so it is re-read once complete",
			offset, len(complete))
	}
}

// TestTailJSONL_SkipsAnOversizedCompleteRecordWithoutLosingTheNextOne makes
// sure the size cap costs exactly one record, not the rest of the file.
func TestTailJSONL_SkipsAnOversizedCompleteRecordWithoutLosingTheNextOne(t *testing.T) {
	huge := `{"type":"message","text":"` + strings.Repeat("x", maxLineBytes+1024) + `"}` + "\n"
	path := writeFile(t, line("before")+huge+line("after"))

	entries, offset, stats, err := TailJSONLWithStats(path, 0, testParse)
	if err != nil {
		t.Fatal(err)
	}
	if got := texts(entries); strings.Join(got, ",") != "before,after" {
		t.Errorf("got %v — an oversized record must cost only itself", got)
	}
	if stats.ParseErrors != 1 {
		t.Errorf("ParseErrors = %d, want 1 for the skipped oversized record", stats.ParseErrors)
	}
	info, _ := os.Stat(path)
	if offset != info.Size() {
		t.Errorf("offset %d, want the full size %d — the skipped record must still be consumed", offset, info.Size())
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
