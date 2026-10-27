package contexttrace

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestNewWriter_Path(t *testing.T) {
	w := NewWriter("/tmp/session-dir")
	want := filepath.Join("/tmp/session-dir", FileName)
	if w.Path() != want {
		t.Errorf("Path() = %q, want %q", w.Path(), want)
	}
}

func TestAppend_CreatesFile(t *testing.T) {
	dir := t.TempDir()
	w := NewWriter(dir)

	err := w.Append(Event{
		Type:   EventProvided,
		Source: SourceTeamContext,
		Team:   "test-team",
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}

	if _, err := os.Stat(w.Path()); os.IsNotExist(err) {
		t.Fatal("file not created after first Append")
	}
}

func TestAppend_SingleEventRoundTrip(t *testing.T) {
	dir := t.TempDir()
	w := NewWriter(dir)

	original := Event{
		Type:         EventProvided,
		Timestamp:    "2026-03-31T10:00:00Z",
		Source:       SourceTeamContext,
		Team:         "SageOx",
		Doc:          "AGENTS.md",
		InlineTokens: 1500,
	}
	if err := w.Append(original); err != nil {
		t.Fatalf("Append: %v", err)
	}

	events, err := ReadEvents(w.Path())
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}

	got := events[0]
	if got.Type != EventProvided {
		t.Errorf("Type = %q, want %q", got.Type, EventProvided)
	}
	if got.Timestamp != "2026-03-31T10:00:00Z" {
		t.Errorf("Timestamp = %q, want preserved value", got.Timestamp)
	}
	if got.Source != SourceTeamContext {
		t.Errorf("Source = %q, want %q", got.Source, SourceTeamContext)
	}
	if got.Team != "SageOx" {
		t.Errorf("Team = %q, want %q", got.Team, "SageOx")
	}
	if got.Doc != "AGENTS.md" {
		t.Errorf("Doc = %q, want %q", got.Doc, "AGENTS.md")
	}
	if got.InlineTokens != 1500 {
		t.Errorf("InlineTokens = %d, want 1500", got.InlineTokens)
	}
}

func TestAppend_MultipleEvents(t *testing.T) {
	dir := t.TempDir()
	w := NewWriter(dir)

	for i := range 5 {
		err := w.Append(Event{
			Type:      EventProvided,
			Timestamp: "2026-03-31T10:00:00Z",
			Source:    SourceTeamMemory,
			Doc:       "MEMORY.md",
		})
		if err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}

	events, err := ReadEvents(w.Path())
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	if len(events) != 5 {
		t.Fatalf("got %d events, want 5", len(events))
	}
}

func TestAppend_SetsTimestamp(t *testing.T) {
	dir := t.TempDir()
	w := NewWriter(dir)

	// empty timestamp should be auto-filled
	err := w.Append(Event{
		Type:   EventProvided,
		Source: SourceTeamContext,
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}

	events, err := ReadEvents(w.Path())
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if events[0].Timestamp == "" {
		t.Error("expected auto-filled timestamp, got empty")
	}
}

func TestAppend_PreservesTimestamp(t *testing.T) {
	dir := t.TempDir()
	w := NewWriter(dir)

	ts := "2025-01-01T00:00:00Z"
	err := w.Append(Event{
		Type:      EventProvided,
		Timestamp: ts,
		Source:    SourceTeamContext,
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}

	events, err := ReadEvents(w.Path())
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	if events[0].Timestamp != ts {
		t.Errorf("Timestamp = %q, want %q (preserved)", events[0].Timestamp, ts)
	}
}

func TestReadEvents_MissingFile(t *testing.T) {
	events, err := ReadEvents(filepath.Join(t.TempDir(), "nonexistent.jsonl"))
	if err != nil {
		t.Fatalf("expected nil error for missing file, got: %v", err)
	}
	if events != nil {
		t.Errorf("expected nil events for missing file, got %d", len(events))
	}
}

func TestReadEvents_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	if err := os.WriteFile(path, []byte{}, 0644); err != nil {
		t.Fatal(err)
	}

	events, err := ReadEvents(path)
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("expected 0 events from empty file, got %d", len(events))
	}
}

func TestReadEvents_MalformedLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)

	content := `{"type":"provided","ts":"2026-01-01T00:00:00Z","source":"team-context","team":"A"}
this is not json
{"type":"influenced","ts":"2026-01-01T00:00:00Z","source":"team-memory","decision":"used pattern X"}
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	events, err := ReadEvents(path)
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2 (skipping malformed line)", len(events))
	}
	if events[0].Type != EventProvided {
		t.Errorf("events[0].Type = %q, want %q", events[0].Type, EventProvided)
	}
	if events[1].Type != EventInfluenced {
		t.Errorf("events[1].Type = %q, want %q", events[1].Type, EventInfluenced)
	}
}

func TestEvent_AllEventTypes(t *testing.T) {
	dir := t.TempDir()
	w := NewWriter(dir)

	provided := Event{
		Type:         EventProvided,
		Timestamp:    "2026-03-31T10:00:00Z",
		Source:       SourceTeamContext,
		Team:         "SageOx",
		Docs:         []string{"AGENTS.md", "CLAUDE.md"},
		InlineTokens: 2000,
	}
	influenced := Event{
		Type:      EventInfluenced,
		Timestamp: "2026-03-31T10:05:00Z",
		Source:    SourceTeamContext,
		Doc:       "AGENTS.md",
		Section:   "Go Conventions",
		Decision:  "used table-driven tests per team norm",
		PlanStep:  3,
	}
	whisper := Event{
		Type:      EventProvided,
		Timestamp: "2026-03-31T10:10:00Z",
		Source:    SourceProjectWhisper,
		From:      "OxB4k2",
		Topic:     "wip",
	}

	for _, ev := range []Event{provided, influenced, whisper} {
		if err := w.Append(ev); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	events, err := ReadEvents(w.Path())
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("got %d events, want 3", len(events))
	}

	// provided event
	if events[0].Team != "SageOx" || len(events[0].Docs) != 2 {
		t.Errorf("provided event mismatch: team=%q, docs=%v", events[0].Team, events[0].Docs)
	}

	// influenced event
	if events[1].Decision != "used table-driven tests per team norm" || events[1].PlanStep != 3 {
		t.Errorf("influenced event mismatch: decision=%q, plan_step=%d", events[1].Decision, events[1].PlanStep)
	}

	// whisper event
	if events[2].From != "OxB4k2" || events[2].Topic != "wip" {
		t.Errorf("whisper event mismatch: from=%q, topic=%q", events[2].From, events[2].Topic)
	}
}

func TestEvent_JSONOmitEmpty(t *testing.T) {
	ev := Event{
		Type:   EventProvided,
		Source: SourceTeamContext,
	}
	data, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}

	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}

	// these optional fields should be absent
	for _, field := range []string{"team", "doc", "docs", "section", "inline_tokens", "read_on_demand", "from", "topic", "decision", "plan_step"} {
		if _, ok := m[field]; ok {
			t.Errorf("field %q should be omitted when empty, but present in JSON", field)
		}
	}

	// required fields should be present
	for _, field := range []string{"type", "source"} {
		if _, ok := m[field]; !ok {
			t.Errorf("required field %q missing from JSON", field)
		}
	}
}

func TestEvent_AllSourceTypes(t *testing.T) {
	sources := []SourceType{
		SourceTeamContext,
		SourceTeamMemory,
		SourceTeamDocs,
		SourceProjectConfig,
		SourceTeamWhisper,
		SourceProjectWhisper,
		SourceOnDemand,
	}

	for _, src := range sources {
		t.Run(string(src), func(t *testing.T) {
			ev := Event{Type: EventProvided, Source: src}
			data, err := json.Marshal(ev)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var got Event
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if got.Source != src {
				t.Errorf("round-trip: got %q, want %q", got.Source, src)
			}
		})
	}
}

func TestAppend_SequentialSafe(t *testing.T) {
	dir := t.TempDir()
	w := NewWriter(dir)

	n := 100
	for i := range n {
		err := w.Append(Event{
			Type:   EventProvided,
			Source: SourceTeamContext,
			Doc:    "doc.md",
		})
		if err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}

	events, err := ReadEvents(w.Path())
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	if len(events) != n {
		t.Errorf("got %d events, want %d", len(events), n)
	}
}

// TestInvokedEvent_RoundTripAndLegacyBytesUnchanged checks the additive
// usage fields. Failure prevented: an ox-call event loses its volume fields on
// read-back, or the new fields leak into (and change the bytes of) every
// legacy event written to the team Ledger.
func TestInvokedEvent_RoundTripAndLegacyBytesUnchanged(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	w := NewWriter(dir)
	in := Event{
		Type: EventInvoked, Timestamp: "2026-06-01T00:00:00Z", Source: SourceOxCLI,
		Command: "agent prime", Label: "loaded team context", InlineTokens: 2000,
		OutputBytes: 8000, FullBytes: 14000, TokensBySource: map[string]int{"team": 1500, "sageox": 500},
		Deferred: []string{"team-docs"}, Failed: true,
	}
	if err := w.Append(in); err != nil {
		t.Fatal(err)
	}
	got, err := ReadEvents(w.Path())
	if err != nil || len(got) != 1 {
		t.Fatalf("ReadEvents = %v, %v", got, err)
	}
	if !reflect.DeepEqual(got[0], in) {
		t.Errorf("round trip mismatch:\n got %+v\nwant %+v", got[0], in)
	}

	legacy, err := json.Marshal(Event{Type: EventProvided, Timestamp: "2026-06-01T00:00:00Z", Source: SourceTeamDocs, Doc: "a.md"})
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"type":"provided","ts":"2026-06-01T00:00:00Z","source":"team-docs","doc":"a.md"}`
	if string(legacy) != want {
		t.Errorf("legacy event bytes changed:\n got %s\nwant %s", legacy, want)
	}
}

func TestAppend_UsedEventRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		ev   Event
		keys []string // json keys that must be present
	}{
		{
			name: "cited_commit with metadata",
			ev: Event{
				Type: EventUsed, Timestamp: "2026-10-26T10:00:00Z", Source: SourceTeamWhisper,
				Mechanism: MechanismCitation, Evidence: EvidenceCitedCommit,
				Ref: "Ox7f2a", RefType: "murmur", Seq: 14, SurfacedSeq: 9,
				Commit: "3e1c9d0", Title: "auth: retry budget", URL: "https://sageox.ai/c/cnv_1",
				FromName: "Dana",
			},
			keys: []string{"evidence", "surfaced_seq", "commit", "title", "url", "from_name"},
		},
		{
			name: "judged",
			ev: Event{
				Type: EventUsed, Timestamp: "2026-10-26T10:00:00Z", Source: SourceLocalRecall,
				Mechanism: MechanismJudge, Evidence: EvidenceJudged, Ref: "ses_1", Decision: "followed retry pattern",
			},
			keys: []string{"evidence", "decision"},
		},
		{
			name: "self-reported none",
			ev:   Event{Type: EventUsed, Timestamp: "2026-10-26T10:00:00Z", Mechanism: MechanismSelfReport, Evidence: EvidenceNone},
			keys: []string{"evidence"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := NewWriter(t.TempDir())
			if err := w.Append(tt.ev); err != nil {
				t.Fatalf("Append: %v", err)
			}
			events, err := ReadEvents(w.Path())
			if err != nil || len(events) != 1 {
				t.Fatalf("ReadEvents: %v, n=%d", err, len(events))
			}
			if !reflect.DeepEqual(events[0], tt.ev) {
				t.Errorf("round trip mismatch:\n got %+v\nwant %+v", events[0], tt.ev)
			}
			raw, err := os.ReadFile(w.Path())
			if err != nil {
				t.Fatal(err)
			}
			var m map[string]any
			if err := json.Unmarshal(raw, &m); err != nil {
				t.Fatal(err)
			}
			for _, k := range tt.keys {
				if _, ok := m[k]; !ok {
					t.Errorf("json key %q missing in %s", k, raw)
				}
			}
		})
	}
}

func TestUsedFields_OmittedWhenEmpty(t *testing.T) {
	b, err := json.Marshal(Event{Type: EventProvided, Source: SourceTeamContext})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"evidence", "surfaced_seq", "commit", "title", "url", "from_name"} {
		if _, ok := m[k]; ok {
			t.Errorf("empty field %q should be omitted: %s", k, b)
		}
	}
}

func TestAliases_RoundTripAndLegacyDecode(t *testing.T) {
	tests := []struct {
		name string
		line string
		want []string
	}{
		{name: "legacy event without aliases", line: `{"type":"provided","ts":"t","source":"local-recall","ref":"cnv_x"}`, want: nil},
		{name: "event with aliases", line: `{"type":"provided","ts":"t","source":"local-recall","ref":"pln_x","aliases":["auth-plan","2026-10-01-auth-plan"]}`, want: []string{"auth-plan", "2026-10-01-auth-plan"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var ev Event
			if err := json.Unmarshal([]byte(tt.line), &ev); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if !reflect.DeepEqual(ev.Aliases, tt.want) {
				t.Fatalf("Aliases = %v, want %v", ev.Aliases, tt.want)
			}
			out, err := json.Marshal(ev)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var back Event
			if err := json.Unmarshal(out, &back); err != nil {
				t.Fatalf("re-unmarshal: %v", err)
			}
			if !reflect.DeepEqual(back, ev) {
				t.Fatalf("round trip = %+v, want %+v", back, ev)
			}
			if tt.want == nil && strings.Contains(string(out), "aliases") {
				t.Fatalf("empty aliases must be omitted, got %s", out)
			}
		})
	}
}
