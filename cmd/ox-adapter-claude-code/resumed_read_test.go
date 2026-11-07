package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/session/nativeimport"
)

// `ox session import` reads a resumed Claude Code transcript from the offset
// import inspection reports, the first of the file's own records, past the
// copy of the conversation it continues. That offset must be a record boundary
// this adapter reads from, and reading from it must yield exactly the
// session's own turns.
//
// Failure prevented: an import that uploads the parent conversation again
// under the new session's name, or an offset that lands mid-record so the
// adapter drops or garbles the session's first turn.
func TestReadFromInspectedOffsetYieldsOnlyAResumedSessionsOwnTurns(t *testing.T) {
	const (
		own    = "230457d4-c384-4e38-90b5-3289ff44be62"
		parent = "a1969243-7a0d-4f80-bc97-b71beefcb3e6"
	)
	user := func(id, text, at string) string {
		return fmt.Sprintf(`{"type":"user","sessionId":%q,"timestamp":"2026-09-%sZ","cwd":"/work/repo","message":{"role":"user","content":%q}}`, id, at, text)
	}
	reply := func(id, text, at string) string {
		return fmt.Sprintf(`{"type":"assistant","sessionId":%q,"timestamp":"2026-09-%sZ","cwd":"/work/repo","message":{"role":"assistant","content":[{"type":"text","text":%q}]}}`, id, at, text)
	}
	lines := []string{
		user(parent, "The login page loops on Safari, fix it", "10T09:00:00"),
		reply(parent, "Set SameSite=Lax on the state cookie.", "10T09:01:00"),
		`{"type":"file-history-snapshot","messageId":"m1"}`,
		user(own, "Now add a regression test for the loop", "12T09:00:00"),
		reply(own, "Added a test that replays the Safari callback.", "12T09:02:00"),
	}
	path := filepath.Join(t.TempDir(), own+".jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	s, err := nativeimport.InspectClaude(path)
	if err != nil {
		t.Fatal(err)
	}
	whole, _, err := readSessionFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(whole) != 4 {
		t.Fatalf("the whole file holds both conversations, so the offset is what keeps the parent out: got %d entries", len(whole))
	}

	entries, _, err := readFromOffset(path, s.Offset)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range entries {
		got = append(got, e.Role+": "+e.Content)
	}
	want := []string{
		"user: Now add a regression test for the loop",
		"assistant: Added a test that replays the Safari callback.",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("reading from offset %d:\n got %q\nwant %q", s.Offset, got, want)
	}
}
