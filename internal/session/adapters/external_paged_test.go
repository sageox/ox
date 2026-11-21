package adapters

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/pkg/adapterprotocol"
)

// pagingAdapter is a fake adapter whose read-from-offset answers by offset
// ($5), from a shell case body, and logs every offset it was asked for.
func pagingAdapter(t *testing.T, name, cases string) (*ExternalAdapter, string) {
	t.Helper()
	if testing.Short() {
		t.Skip("short: drives an external adapter subprocess")
	}
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	script := "#!/bin/sh\necho \"$5\" >> '" + calls + "'\ncase \"$5\" in\n" + cases + "\nesac\n"
	bin := filepath.Join(dir, "ox-adapter-paging")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return NewExternalAdapterWithInfo(bin, &adapterprotocol.InfoResponse{Name: name}), calls
}

// transcriptOfSize stands in for the transcript; only its size matters here.
func transcriptOfSize(t *testing.T, size int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(path, []byte(strings.Repeat("\n", size)), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func page(content string, next string) string {
	return `echo '{"entries":[{"timestamp":"2026-04-02T10:30:00Z","role":"user","content":"` + content + `"}],"new_offset":` + next + `}'`
}

func callsMade(t *testing.T, calls string) []string {
	t.Helper()
	b, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Fields(string(b))
}

func contents(entries []RawEntry) string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Content)
	}
	return strings.Join(out, ",")
}

// One ReadFromOffset is what a stop, a recovery and a finalize make, so a
// read the adapter ended early must be read on to the end of the transcript.
func TestExternalAdapter_ReadFromOffsetDrainsEveryPage(t *testing.T) {
	ea, calls := pagingAdapter(t, "claude-code", "  0) "+page("a", "100")+";;\n  100) "+page("b", "200")+";;\n  200) "+page("c", "300")+";;\n  *) exit 1;;")
	entries, next, err := ea.ReadFromOffset(transcriptOfSize(t, 300), 0)
	if err != nil {
		t.Fatal(err)
	}
	if contents(entries) != "a,b,c" || next != 300 {
		t.Fatalf("read %q to %d, want a,b,c to 300", contents(entries), next)
	}
	if got := strings.Join(callsMade(t, calls), ","); got != "0,100,200" {
		t.Fatalf("offsets asked = %s, want one read per page and none past the end", got)
	}
}

// A page that does not move the cursor (only an unterminated record is left)
// ends the read: nothing is lost, and the loop cannot spin on it.
func TestExternalAdapter_ReadFromOffsetStopsWhenTheCursorStops(t *testing.T) {
	ea, calls := pagingAdapter(t, "claude-code", "  0) "+page("a", "100")+";;\n  100) echo '{\"entries\":[],\"new_offset\":100}';;\n  *) exit 1;;")
	entries, next, err := ea.ReadFromOffset(transcriptOfSize(t, 300), 0)
	if err != nil || contents(entries) != "a" || next != 100 {
		t.Fatalf("read %q to %d, %v; want a to 100", contents(entries), next, err)
	}
	if got := strings.Join(callsMade(t, calls), ","); got != "0,100" {
		t.Fatalf("offsets asked = %s, want 0,100", got)
	}
}

// An adapter whose cursor creeps forward is stopped by the page bound, with an
// exact cursor, so the next read resumes where this one ended.
func TestExternalAdapter_ReadFromOffsetIsBoundedInPages(t *testing.T) {
	ea, calls := pagingAdapter(t, "claude-code", `  *) n=$(( $5 + 1 )); echo "{\"entries\":[{\"timestamp\":\"2026-04-02T10:30:00Z\",\"role\":\"user\",\"content\":\"p$5\"}],\"new_offset\":$n}";;`)
	entries, next, err := ea.ReadFromOffset(transcriptOfSize(t, 10*maxReadPages), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != maxReadPages || next != maxReadPages || len(callsMade(t, calls)) != maxReadPages {
		t.Fatalf("entries=%d next=%d calls=%d, want %d of each", len(entries), next, len(callsMade(t, calls)), maxReadPages)
	}
}

// A later page failing fails the read as one read failed: the caller retries
// from its own offset and never commits a cursor past what it was given.
func TestExternalAdapter_ReadFromOffsetLaterPageErrorFailsTheRead(t *testing.T) {
	ea, _ := pagingAdapter(t, "claude-code", "  0) "+page("a", "100")+";;\n  *) echo '{\"error\":\"boom\"}'; exit 1;;")
	entries, next, err := ea.ReadFromOffset(transcriptOfSize(t, 300), 0)
	if err == nil || entries != nil || next != 0 {
		t.Fatalf("entries=%v next=%d err=%v, want no entries, the caller's offset and the error", entries, next, err)
	}
}

// A page that moves the cursor back (the transcript was replaced under the
// read) fails the whole read: reporting the pages before it as a success
// could skip a prefix of the replacement. The caller retries from its offset.
func TestExternalAdapter_ReadFromOffsetFailsWhenTheCursorGoesBack(t *testing.T) {
	ea, _ := pagingAdapter(t, "claude-code", "  0) "+page("a", "100")+";;\n  100) "+page("replaced", "40")+";;\n  *) exit 1;;")
	entries, next, err := ea.ReadFromOffset(transcriptOfSize(t, 300), 0)
	if !errors.Is(err, ErrInvalidResponse) || entries != nil || next != 0 {
		t.Fatalf("entries=%v next=%d err=%v, want no entries, the caller's offset and ErrInvalidResponse", entries, next, err)
	}
}

// Another adapter's cursor need not be a byte offset (opencode counts rows):
// it is read once, as before, whatever the file's size.
func TestExternalAdapter_ReadFromOffsetReadsOtherAdaptersOnce(t *testing.T) {
	ea, calls := pagingAdapter(t, "opencode", "  0) "+page("a", "5")+";;\n  *) "+page("again", "6")+";;")
	entries, next, err := ea.ReadFromOffset(transcriptOfSize(t, 300), 0)
	if err != nil || contents(entries) != "a" || next != 5 {
		t.Fatalf("read %q to %d, %v; want a to 5", contents(entries), next, err)
	}
	if got := len(callsMade(t, calls)); got != 1 {
		t.Fatalf("calls = %d, want 1", got)
	}
}
