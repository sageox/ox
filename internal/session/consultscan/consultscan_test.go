package consultscan

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/sageox/ox/internal/session/contexttrace"
)

// roots builds a Roots pointing at a fake ledger + team-context under tmp.
func testRoots() Roots {
	return Roots{
		Ledger:      "/x/ledger",
		TeamContext: []string{"/x/team"},
	}
}

func readInput(path string) string {
	b, _ := json.Marshal(map[string]string{"file_path": path})
	return string(b)
}

// TestScan_TagsSageOxReads is the core contract: a Read of a file inside the
// ledger or a team-context repo becomes a turn-anchored consulted event; reads
// elsewhere and non-read tools do not.
// Failure prevented: the deterministic turn-tagger stops recognizing team
// knowledge, silently dropping the provable half of knowledge-flow attribution.
func TestScan_TagsSageOxReads(t *testing.T) {
	tests := []struct {
		name    string
		entry   Entry
		wantTag bool
		refType string
	}{
		{"ledger session read", Entry{ToolName: "Read", ToolInput: readInput("/x/ledger/sessions/2026-07-01-ryan-Ox1/summary.md")}, true, "session"},
		{"ledger plan read", Entry{ToolName: "Read", ToolInput: readInput("/x/ledger/data/plans/2026-07-01-foo/plan.md")}, true, "plan"},
		{"team discussion read", Entry{ToolName: "Read", ToolInput: readInput("/x/team/discussions/2026-06-30-ajit/summary.md")}, true, "discussion"},
		{"team rule read", Entry{ToolName: "Read", ToolInput: readInput("/x/team/agents/rules/postgres.md")}, true, "doc"},
		{"team doc read", Entry{ToolName: "Read", ToolInput: readInput("/x/team/docs/principles.md")}, true, "doc"},
		{"team memory read", Entry{ToolName: "Read", ToolInput: readInput("/x/team/memory/daily/2026-06-30.md")}, true, "kb"},
		{"agent-context read", Entry{ToolName: "Read", ToolInput: readInput("/x/team/agent-context/distilled-discussions.md")}, true, "kb"},
		{"non-sageox file read", Entry{ToolName: "Read", ToolInput: readInput("/home/ryan/project/main.go")}, false, ""},
		{"non-read tool on sageox path", Entry{ToolName: "Bash", ToolInput: readInput("/x/ledger/sessions/a/x.md")}, false, ""},
		{"edit is not a read", Entry{ToolName: "Edit", ToolInput: readInput("/x/team/docs/principles.md")}, false, ""},
		{"bare path input inside root", Entry{ToolName: "Read", ToolInput: "/x/team/docs/glossary.md"}, true, "doc"},
		{"sibling dir must not match (boundary)", Entry{ToolName: "Read", ToolInput: readInput("/x/teamcontext-other/docs/x.md")}, false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Scan([]Entry{tt.entry}, testRoots())
			if !tt.wantTag {
				if len(got) != 0 {
					t.Fatalf("expected no tag, got %+v", got)
				}
				return
			}
			if len(got) != 1 {
				t.Fatalf("expected 1 consulted event, got %d", len(got))
			}
			ev := got[0]
			if ev.Type != contexttrace.EventConsulted {
				t.Errorf("type = %q, want consulted", ev.Type)
			}
			if ev.Mechanism != contexttrace.MechanismRetrieval {
				t.Errorf("mechanism = %q, want retrieval (deterministic grade A)", ev.Mechanism)
			}
			if ev.RefType != tt.refType {
				t.Errorf("ref_type = %q, want %q", ev.RefType, tt.refType)
			}
			if filepath.IsAbs(ev.Ref) {
				t.Errorf("ref %q is absolute — must be repo-relative, never leak a full path", ev.Ref)
			}
		})
	}
}

func bashInput(cmd string) string {
	b, _ := json.Marshal(map[string]string{"command": cmd})
	return string(b)
}

// TestScan_TagsRetrievalCommands covers the second deterministic signal: running
// an `ox` retrieval command in a turn is a consultation, tagged with the
// knowledge kind and the query subject.
// Failure prevented: research/planning turns that pulled team context via
// `ox query` go untagged, hiding the highest-value influence path.
func TestScan_TagsRetrievalCommands(t *testing.T) {
	tests := []struct {
		name    string
		cmd     string
		refType string
		query   string
	}{
		{"ox query quoted", `ox query "token refresh contract"`, "query", "token refresh contract"},
		{"ox code search", `ox code search "PushWithRetry"`, "code", "PushWithRetry"},
		{"ox team-ctx", `ox agent team-ctx`, "team-context", ""},
		{"ox team-ctx with agent id", `ox agent Oxa1b2 team-ctx`, "team-context", ""},
		{"ox query cut at separator", `ox query auth && echo "done"`, "query", "auth"},
		{"ox query after cd", `cd /repo && ox query "pgx pools"`, "query", "pgx pools"},
		{"agent subcommand other than team-ctx", `ox agent Oxa1b2 prime`, "", ""},
		{"ox decision enrich with flag", `ox decision enrich --topic "session streaming"`, "decision", "session streaming"},
		{"unrelated command", `ls -la /tmp`, "", ""},
		{"non-ox query", `grep query file.go`, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Scan([]Entry{{ToolName: "Bash", ToolInput: bashInput(tt.cmd)}}, testRoots())
			if tt.refType == "" {
				if len(got) != 0 {
					t.Fatalf("expected no tag, got %+v", got)
				}
				return
			}
			if len(got) != 1 {
				t.Fatalf("expected 1 tag, got %d", len(got))
			}
			ev := got[0]
			if ev.Type != contexttrace.EventConsulted || ev.Mechanism != contexttrace.MechanismRetrieval {
				t.Errorf("type/mechanism = %q/%q, want consulted/retrieval", ev.Type, ev.Mechanism)
			}
			if ev.RefType != tt.refType {
				t.Errorf("ref_type = %q, want %q", ev.RefType, tt.refType)
			}
			if ev.Query != tt.query {
				t.Errorf("query = %q, want %q", ev.Query, tt.query)
			}
		})
	}
}

// TestScan_SeqAnchorsToTurn proves each tag carries the turn (entry index) it
// happened on, even with non-read turns interleaved — the anchor recap needs to
// tie a consult to the work in that turn.
func TestScan_SeqAnchorsToTurn(t *testing.T) {
	entries := []Entry{
		{ToolName: "user", ToolInput: ""},                                           // 0
		{ToolName: "Bash", ToolInput: "ls"},                                         // 1
		{ToolName: "Read", ToolInput: readInput("/x/team/docs/principles.md")},      // 2 -> tag seq 2
		{ToolName: "assistant", ToolInput: ""},                                      // 3
		{ToolName: "Read", ToolInput: readInput("/x/ledger/sessions/a/summary.md")}, // 4 -> tag seq 4
	}
	got := Scan(entries, testRoots())
	if len(got) != 2 {
		t.Fatalf("expected 2 tags, got %d", len(got))
	}
	if got[0].Seq != 2 || got[1].Seq != 4 {
		t.Errorf("seqs = %d,%d; want 2,4 (must align with transcript turn order)", got[0].Seq, got[1].Seq)
	}
}

// TestScanRawFile drives the raw.jsonl reader end to end, including seq
// alignment across a malformed line and the fail-open missing-file path.
func TestScanRawFile(t *testing.T) {
	dir := t.TempDir()
	raw := filepath.Join(dir, "raw.jsonl")
	lines := []string{
		`{"type":"user","content":"hi"}`, // 0
		`{"type":"tool","tool_name":"Read","tool_input":"{\"file_path\":\"/x/team/docs/principles.md\"}"}`, // 1 -> tag
		`not json at all`, // 2 (malformed, still a turn)
		`{"type":"tool","tool_name":"Read","tool_input":"{\"file_path\":\"/home/x/main.go\"}"}`, // 3 (outside roots)
	}
	if err := os.WriteFile(raw, []byte(joinLines(lines)), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := ScanRawFile(raw, testRoots())
	if err != nil {
		t.Fatalf("ScanRawFile: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 tag, got %d: %+v", len(got), got)
	}
	if got[0].Seq != 1 {
		t.Errorf("seq = %d, want 1 (malformed line must still hold its turn slot)", got[0].Seq)
	}

	// Missing file is fail-open: no events, no error.
	missing, err := ScanRawFile(filepath.Join(dir, "nope.jsonl"), testRoots())
	if err != nil || missing != nil {
		t.Errorf("missing file: got (%v, %v), want (nil, nil)", missing, err)
	}
}

// TestTagSessionReads_WritesTraceEvents proves the finalize-time wiring:
// scanning a raw.jsonl appends turn-anchored consulted events to the session's
// context-trace, readable back by the trace layer recap mines.
func TestTagSessionReads_WritesTraceEvents(t *testing.T) {
	dir := t.TempDir()
	raw := filepath.Join(dir, "raw.jsonl")
	os.WriteFile(raw, []byte(
		`{"tool_name":"Read","tool_input":"{\"file_path\":\"/x/team/docs/principles.md\"}"}`+"\n"+
			`{"tool_name":"Read","tool_input":"{\"file_path\":\"/x/ledger/sessions/a/summary.md\"}"}`+"\n"), 0o644)

	n, err := TagSessionReads(raw, dir, testRoots())
	if err != nil {
		t.Fatalf("TagSessionReads: %v", err)
	}
	if n != 2 {
		t.Fatalf("tagged %d, want 2", n)
	}
	events, err := contexttrace.ReadEvents(filepath.Join(dir, contexttrace.FileName))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("read back %d events, want 2", len(events))
	}
	for _, ev := range events {
		if ev.Type != contexttrace.EventConsulted || ev.Mechanism != contexttrace.MechanismRetrieval {
			t.Errorf("unexpected event: %+v", ev)
		}
	}
}

func joinLines(lines []string) string {
	out := ""
	for _, l := range lines {
		out += l + "\n"
	}
	return out
}

// TestTagSessionReads_UnaffectedByInvokedEvents runs the finalize-time tagger
// into a trace that already holds `invoked` ox-call events. Failure prevented:
// the consult tagger's output changes (or clobbers the ox-call lines) once the
// hook starts writing invoked events into the same file.
func TestTagSessionReads_UnaffectedByInvokedEvents(t *testing.T) {
	raw := `{"tool_name":"Read","tool_input":"{\"file_path\":\"/x/team/docs/principles.md\"}"}` + "\n" +
		`{"tool_name":"Bash","tool_input":"{\"command\":\"ox query \\\"auth\\\"\"}"}` + "\n"

	run := func(preexisting []contexttrace.Event) []contexttrace.Event {
		dir := t.TempDir()
		rawPath := filepath.Join(dir, "raw.jsonl")
		if err := os.WriteFile(rawPath, []byte(raw), 0o644); err != nil {
			t.Fatal(err)
		}
		w := contexttrace.NewWriter(dir)
		for _, ev := range preexisting {
			if err := w.Append(ev); err != nil {
				t.Fatal(err)
			}
		}
		n, err := TagSessionReads(rawPath, dir, testRoots())
		if err != nil || n != 2 {
			t.Fatalf("TagSessionReads = %d, %v; want 2, nil", n, err)
		}
		events, err := contexttrace.ReadEvents(w.Path())
		if err != nil {
			t.Fatal(err)
		}
		return events
	}

	invoked := []contexttrace.Event{
		{Type: contexttrace.EventInvoked, Timestamp: "2026-06-01T00:00:00Z", Source: contexttrace.SourceOxCLI, Command: "query", Label: "searched team memory", RefType: "query", Query: "auth", OutputBytes: 400, InlineTokens: 100, Seq: 1},
	}
	plain := run(nil)
	mixed := run(invoked)
	if len(mixed) != len(invoked)+len(plain) {
		t.Fatalf("mixed trace has %d events, want %d", len(mixed), len(invoked)+len(plain))
	}
	if mixed[0].Type != contexttrace.EventInvoked {
		t.Errorf("pre-existing invoked event was not preserved: %+v", mixed[0])
	}
	for i, ev := range mixed[len(invoked):] {
		want := plain[i]
		ev.Timestamp, want.Timestamp = "", ""
		if !reflect.DeepEqual(ev, want) {
			t.Errorf("consulted event %d changed:\n got %+v\nwant %+v", i, ev, want)
		}
	}
}
