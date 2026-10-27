package consultscan

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/session/contexttrace"
	"github.com/sageox/ox/internal/session/contextuse"
)

func assistant(text string) Entry { return Entry{Type: "assistant", Content: text} }
func read(path string) Entry {
	return Entry{Type: "tool", ToolName: "Read", ToolInput: readInput(path)}
}
func bash(cmd string) Entry { return Entry{Type: "tool", ToolName: "Bash", ToolInput: bashInput(cmd)} }

// TestScanUses_EvidenceByRefType is the core contract: for every evidence kind
// and every ref type, a surfaced ref the agent went back to yields exactly one
// provable `used` event with the right mechanism, evidence and metadata.
// Failure prevented: recap shows "surfaced" context as "used" (or never
// does), so the derived co-author attribution is built on nothing.
func TestScanUses_EvidenceByRefType(t *testing.T) {
	tests := []struct {
		name     string
		surfaced contextuse.SurfacedRef
		entry    Entry
		evidence string
		mech     contexttrace.Mechanism
	}{
		// read
		{"read: prime doc by basename", contextuse.SurfacedRef{Ref: "MEMORY.md", RefType: "doc", Via: contextuse.ViaPrime}, read("/x/team/MEMORY.md"), contexttrace.EvidenceRead, contexttrace.MechanismRetrieval},
		{"read: doc by relative path", contextuse.SurfacedRef{Ref: "agents/rules/postgres.md", RefType: "doc"}, read("/x/team/agents/rules/postgres.md"), contexttrace.EvidenceRead, contexttrace.MechanismRetrieval},
		{"read: doc basename outside any root", contextuse.SurfacedRef{Ref: "agents/rules/postgres.md", RefType: "doc"}, read("/elsewhere/postgres.md"), contexttrace.EvidenceRead, contexttrace.MechanismRetrieval},
		{"read: session dir in ledger", contextuse.SurfacedRef{Ref: "2026-07-01-ryan-Ox1", RefType: "session", Via: contextuse.ViaRecall}, read("/x/ledger/sessions/2026-07-01-ryan-Ox1/summary.md"), contexttrace.EvidenceRead, contexttrace.MechanismRetrieval},
		{"read: plan slug in ledger", contextuse.SurfacedRef{Ref: "2026-07-01-foo", RefType: "plan"}, read("/x/ledger/data/plans/2026-07-01-foo/plan.md"), contexttrace.EvidenceRead, contexttrace.MechanismRetrieval},
		{"read: view tool counts", contextuse.SurfacedRef{Ref: "SOUL.md", RefType: "doc"}, Entry{Type: "tool", ToolName: "view", ToolInput: "/x/team/SOUL.md"}, contexttrace.EvidenceRead, contexttrace.MechanismRetrieval},
		// requery
		{"requery: team-ctx slug", contextuse.SurfacedRef{Ref: "postgres", RefType: "doc"}, bash("ox agent team-ctx postgres"), contexttrace.EvidenceRequery, contexttrace.MechanismRetrieval},
		{"requery: session view", contextuse.SurfacedRef{Ref: "2026-07-01-ryan-Ox1", RefType: "session"}, bash("ox session view 2026-07-01-ryan-Ox1 --json"), contexttrace.EvidenceRequery, contexttrace.MechanismRetrieval},
		{"requery: conversation show", contextuse.SurfacedRef{Ref: "cnv_01a17990", RefType: "discussion"}, bash("ox conversation show cnv_01a17990"), contexttrace.EvidenceRequery, contexttrace.MechanismRetrieval},
		{"requery: plan view", contextuse.SurfacedRef{Ref: "nifty-panda", RefType: "plan"}, bash("cd /repo && ox plan view nifty-panda"), contexttrace.EvidenceRequery, contexttrace.MechanismRetrieval},
		{"requery: plan view quoted", contextuse.SurfacedRef{Ref: "nifty-panda", RefType: "plan"}, bash(`ox plan view "nifty-panda"`), contexttrace.EvidenceRequery, contexttrace.MechanismRetrieval},
		{"requery: conversation show", contextuse.SurfacedRef{Ref: "cnv_7f2a", RefType: "discussion"}, bash("ox conversation show cnv_7f2a"), contexttrace.EvidenceRequery, contexttrace.MechanismRetrieval},
		{"requery: conversation transcript", contextuse.SurfacedRef{Ref: "cnv_7f2a", RefType: "discussion"}, bash("ox conversation transcript cnv_7f2a --json"), contexttrace.EvidenceRequery, contexttrace.MechanismRetrieval},
		{"requery: conversation topics", contextuse.SurfacedRef{Ref: "cnv_7f2a", RefType: "discussion"}, bash("ox conversation topics cnv_7f2a"), contexttrace.EvidenceRequery, contexttrace.MechanismRetrieval},
		{"requery: conversation topic", contextuse.SurfacedRef{Ref: "cnv_7f2a", RefType: "discussion"}, bash("ox conversation topic cnv_7f2a tp_3"), contexttrace.EvidenceRequery, contexttrace.MechanismRetrieval},
		{"requery: conversation walkthrough", contextuse.SurfacedRef{Ref: "cnv_7f2a", RefType: "discussion"}, bash("ox conversation walkthrough cnv_7f2a"), contexttrace.EvidenceRequery, contexttrace.MechanismRetrieval},
		{"requery: walkthrough", contextuse.SurfacedRef{Ref: "cnv_7f2a", RefType: "discussion"}, bash("ox walkthrough cnv_7f2a"), contexttrace.EvidenceRequery, contexttrace.MechanismRetrieval},
		{"requery: plan status", contextuse.SurfacedRef{Ref: "nifty-panda", RefType: "plan"}, bash("ox plan status nifty-panda"), contexttrace.EvidenceRequery, contexttrace.MechanismRetrieval},
		{"requery: session show", contextuse.SurfacedRef{Ref: "2026-07-01-ryan-Ox1", RefType: "session"}, bash("ox session show 2026-07-01-ryan-Ox1"), contexttrace.EvidenceRequery, contexttrace.MechanismRetrieval},
		{"requery: team-ctx with agent id", contextuse.SurfacedRef{Ref: "postgres", RefType: "doc"}, bash("ox agent Oxa1b2 team-ctx postgres"), contexttrace.EvidenceRequery, contexttrace.MechanismRetrieval},
		{"requery: ox binary by path", contextuse.SurfacedRef{Ref: "nifty-panda", RefType: "plan"}, bash("./bin/ox plan view nifty-panda"), contexttrace.EvidenceRequery, contexttrace.MechanismRetrieval},
		// cited
		{"cited: murmur id (short, id-shaped)", contextuse.SurfacedRef{Ref: "Ox7f2a", RefType: "murmur", Title: "auth", URL: "https://sageox.ai/m/Ox7f2a"}, assistant("Following Dana's note (Ox7f2a) on auth."), contexttrace.EvidenceCited, contexttrace.MechanismCitation},
		{"cited: session name", contextuse.SurfacedRef{Ref: "2026-07-01-ryan-Ox1", RefType: "session"}, assistant("SageOx found Ryan's session 2026-07-01-ryan-Ox1 on this."), contexttrace.EvidenceCited, contexttrace.MechanismCitation},
		{"cited: doc path", contextuse.SurfacedRef{Ref: "agents/rules/postgres.md", RefType: "doc"}, assistant("Per agents/rules/postgres.md, use pgx."), contexttrace.EvidenceCited, contexttrace.MechanismCitation},
		{"cited: conversation link", contextuse.SurfacedRef{Ref: "/c/cnv_01a1", RefType: "discussion"}, assistant("See https://sageox.ai/c/cnv_01a1 for the decision."), contexttrace.EvidenceCited, contexttrace.MechanismCitation},
		{"cited: ses_ id at end of text", contextuse.SurfacedRef{Ref: "ses_abc", RefType: "session"}, assistant("building on ses_abc"), contexttrace.EvidenceCited, contexttrace.MechanismCitation},
		{"cited: title phrase", contextuse.SurfacedRef{Ref: "Ox7f2a", RefType: "murmur", Title: "Ox CLI status line design feedback"}, assistant(`Dev and Ryan agreed in "Ox CLI status line design feedback" that whispers are noisy.`), contexttrace.EvidenceCited, contexttrace.MechanismCitation},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ScanUses([]Entry{{Type: "user", Content: "context goes here"}, tt.entry}, []contextuse.SurfacedRef{tt.surfaced}, testRoots())
			if len(got) != 1 {
				t.Fatalf("got %d events, want 1: %+v", len(got), got)
			}
			ev := got[0]
			if ev.Type != contexttrace.EventUsed || ev.Source != contexttrace.SourceOnDemand {
				t.Errorf("type/source = %q/%q, want used/on-demand", ev.Type, ev.Source)
			}
			if ev.Evidence != tt.evidence || ev.Mechanism != tt.mech {
				t.Errorf("evidence/mechanism = %q/%q, want %q/%q", ev.Evidence, ev.Mechanism, tt.evidence, tt.mech)
			}
			if ev.Seq != 1 {
				t.Errorf("seq = %d, want 1 (the entry that used it)", ev.Seq)
			}
			if ev.Ref != tt.surfaced.Ref || ev.RefType != tt.surfaced.RefType || ev.Title != tt.surfaced.Title || ev.URL != tt.surfaced.URL {
				t.Errorf("ref metadata not copied from surfaced ref: %+v", ev)
			}
		})
	}
}

// TestScanUses_Negatives pins the boundaries that keep `used` provable: no
// partial-token matches, no short refs, no short titles, nothing from user/
// system/tool entries, nothing before the ref was surfaced, no near-miss
// commands. Failure prevented: false "used" receipts, which would be worse
// than none — they would inflate the derived attribution with noise.
func TestScanUses_Negatives(t *testing.T) {
	tests := []struct {
		name     string
		surfaced contextuse.SurfacedRef
		entries  []Entry
	}{
		{"cited: ref inside a longer token", contextuse.SurfacedRef{Ref: "OxAbcd", RefType: "murmur"}, []Entry{assistant("see OxAbcdef")}},
		{"cited: ref glued to identifier char", contextuse.SurfacedRef{Ref: "OxAbcd", RefType: "murmur"}, []Entry{assistant("xOxAbcd and OxAbcd_1")}},
		{"cited: short non-id ref", contextuse.SurfacedRef{Ref: "auth", RefType: "query"}, []Entry{assistant("the auth module")}},
		{"cited: title shorter than 12", contextuse.SurfacedRef{Ref: "Ox7f2a", RefType: "murmur", Title: "auth notes"}, []Entry{assistant("see the auth notes")}},
		{"cited: user entry never counts", contextuse.SurfacedRef{Ref: "Ox7f2a", RefType: "murmur"}, []Entry{{Type: "user", Content: "whisper Ox7f2a from Dana"}}},
		{"cited: system entry never counts", contextuse.SurfacedRef{Ref: "Ox7f2a", RefType: "murmur"}, []Entry{{Type: "system", Content: "Ox7f2a"}}},
		{"cited: tool output never counts", contextuse.SurfacedRef{Ref: "Ox7f2a", RefType: "murmur"}, []Entry{{Type: "tool", ToolName: "Bash", Content: "Ox7f2a"}}},
		{"cited: before surfaced seq", contextuse.SurfacedRef{Ref: "Ox7f2a", RefType: "murmur", Seq: 3}, []Entry{assistant("Ox7f2a"), assistant("Ox7f2a")}},
		{"read: before surfaced seq", contextuse.SurfacedRef{Ref: "MEMORY.md", RefType: "doc", Seq: 5}, []Entry{read("/x/team/MEMORY.md")}},
		{"read: different file", contextuse.SurfacedRef{Ref: "MEMORY.md", RefType: "doc"}, []Entry{read("/x/team/SOUL.md")}},
		{"read: other session dir", contextuse.SurfacedRef{Ref: "2026-07-01-ryan-Ox1", RefType: "session"}, []Entry{read("/x/ledger/sessions/2026-07-01-ryan-Ox12/summary.md")}},
		{"read: extensionless ref basename outside ledger", contextuse.SurfacedRef{Ref: "nifty-panda", RefType: "plan"}, []Entry{read("/elsewhere/nifty-panda")}},
		{"requery: different argument", contextuse.SurfacedRef{Ref: "Ox7f2a", RefType: "murmur"}, []Entry{bash("ox session view Ox9999")}},
		{"requery: prefix not at word boundary", contextuse.SurfacedRef{Ref: "nifty-panda", RefType: "plan"}, []Entry{bash("ox plan viewer nifty-panda")}},
		{"requery: before surfaced seq", contextuse.SurfacedRef{Ref: "Ox7f2a", RefType: "murmur", Seq: 2}, []Entry{bash("ox session view Ox7f2a")}},
		{"requery: unrelated ox command", contextuse.SurfacedRef{Ref: "Ox7f2a", RefType: "murmur"}, []Entry{bash("ox murmur list")}},
		{"requery: conversation list is not a re-open", contextuse.SurfacedRef{Ref: "cnv_7f2a", RefType: "discussion"}, []Entry{bash("ox conversation list cnv_7f2a")}},
		{"requery: conversation topics is not topic", contextuse.SurfacedRef{Ref: "cnv_7f2a", RefType: "discussion"}, []Entry{bash("ox conversation topicsx cnv_7f2a")}},
		{"requery: agent subcommand other than team-ctx", contextuse.SurfacedRef{Ref: "postgres", RefType: "doc"}, []Entry{bash("ox agent Oxa1b2 prime postgres")}},
		{"requery: quoted in echo", contextuse.SurfacedRef{Ref: "nifty-panda", RefType: "plan"}, []Entry{bash(`echo "ox plan view nifty-panda"`)}},
		{"requery: not the ox binary", contextuse.SurfacedRef{Ref: "nifty-panda", RefType: "plan"}, []Entry{bash("box plan view nifty-panda")}},
		{"empty surfaced ref", contextuse.SurfacedRef{Ref: "", RefType: "doc", Title: "a title long enough"}, []Entry{read("/x/team/MEMORY.md")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ScanUses(tt.entries, []contextuse.SurfacedRef{tt.surfaced}, testRoots()); len(got) != 0 {
				t.Fatalf("expected no used event, got %+v", got)
			}
		})
	}
	if got := ScanUses(nil, []contextuse.SurfacedRef{{Ref: "Ox7f2a"}}, testRoots()); got != nil {
		t.Errorf("no entries: got %+v", got)
	}
	if got := ScanUses([]Entry{assistant("Ox7f2a")}, nil, testRoots()); got != nil {
		t.Errorf("nothing surfaced: got %+v", got)
	}
}

// TestScanUses_DedupesEarliestPerRefAndEvidence proves one receipt per
// (ref, evidence) at its first turn, while different evidence kinds for the
// same ref are each kept.
func TestScanUses_DedupesEarliestPerRefAndEvidence(t *testing.T) {
	s := []contextuse.SurfacedRef{{Ref: "Ox7f2a", RefType: "murmur", Seq: 1}}
	entries := []Entry{
		{Type: "user", Content: "Ox7f2a"},     // 0: surfacing turn content, ignored
		assistant("first mention Ox7f2a"),     // 1 -> cited seq 1
		assistant("again Ox7f2a"),             // 2 (dup)
		bash("ox session view Ox7f2a"),        // 3 -> requery seq 3
		bash("ox session view Ox7f2a --json"), // 4 (dup)
	}
	got := ScanUses(entries, s, testRoots())
	if len(got) != 2 {
		t.Fatalf("got %d events, want 2: %+v", len(got), got)
	}
	if got[0].Evidence != contexttrace.EvidenceCited || got[0].Seq != 1 || got[0].SurfacedSeq != 1 {
		t.Errorf("first = %+v, want cited@1 surfaced@1", got[0])
	}
	if got[1].Evidence != contexttrace.EvidenceRequery || got[1].Seq != 3 {
		t.Errorf("second = %+v, want requery@3", got[1])
	}
}

// TestMatchesRef covers the shared matcher the commit hook also relies on.
func TestMatchesRef(t *testing.T) {
	tests := []struct {
		name             string
		text, ref, title string
		want             bool
	}{
		{"long ref as token", "fix per 2026-07-01-ryan-Ox1 session", "2026-07-01-ryan-Ox1", "", true},
		{"long ref inside token", "fix per 2026-07-01-ryan-Ox12", "2026-07-01-ryan-Ox1", "", false},
		{"id ref short", "(Ox7f2a)", "Ox7f2a", "", true},
		{"id ref too short to be id-shaped", "Ox7f", "Ox7f", "", false},
		{"pln_ id", "plan pln_x9 done", "pln_x9", "", true},
		{"rec_ id with punctuation", "see rec_42.", "rec_42", "", true},
		{"short plain ref", "auth", "auth", "", false},
		{"title phrase", "closes the Ox CLI status line design feedback thread", "", "Ox CLI status line design feedback", true},
		{"title too short", "auth notes", "", "auth notes", false},
		{"title case-sensitive", "ox cli status line design feedback", "", "Ox CLI status line design feedback", false},
		{"empty text", "", "Ox7f2a", "a long enough title", false},
		{"nothing to match", "hello", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := MatchesRef(tt.text, tt.ref, tt.title); got != tt.want {
				t.Errorf("MatchesRef(%q, %q, %q) = %v, want %v", tt.text, tt.ref, tt.title, got, tt.want)
			}
		})
	}
}

// TestLoadRawEntries_HeaderOffset proves the seq-frame fix: the raw.jsonl
// header line (and a footer / _meta record) does not occupy an entry index, so
// a consulted/used event's Seq lands in the same EntryCount frame the whisper
// and invoked writers use. Malformed and content-shaped lines still hold
// their slot. Failure prevented: every finalize-time seq off by one, so a
// read on the very turn a whisper landed is judged "before" it.
func TestLoadRawEntries_HeaderOffset(t *testing.T) {
	dir := t.TempDir()
	raw := filepath.Join(dir, "raw.jsonl")
	lines := []string{
		`{"type":"header","metadata":{"version":"1.0"}}`,                   // dropped
		`{"_meta":{"schema_version":"1","agent_id":"OxCURS"}}`,             // dropped (cursor-style header)
		`{"type":"user","content":"hi"}`,                                   // 0
		`{"type":"assistant","content":[{"type":"text","text":"Ox7f2a"}]}`, // 1 (array content kept as raw JSON)
		`garbage`, // 2
		`{"type":"tool","tool_name":"Read","tool_input":{"file_path":"/x/team/MEMORY.md"}}`, // 3 (object tool_input)
		`{"type":"footer","stopped_at":"x"}`,                                                // dropped
	}
	if err := os.WriteFile(raw, []byte(joinLines(lines)), 0o644); err != nil {
		t.Fatal(err)
	}
	entries, err := LoadRawEntries(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 4 {
		t.Fatalf("got %d entries, want 4: %+v", len(entries), entries)
	}
	if entries[0].Type != "user" || entries[0].Content != "hi" {
		t.Errorf("entry 0 = %+v", entries[0])
	}
	if entries[1].Type != "assistant" || entries[1].Content == "" {
		t.Errorf("entry 1 = %+v, want assistant with raw array content", entries[1])
	}
	if entries[2].Type != "" || entries[2].ToolName != "" {
		t.Errorf("entry 2 = %+v, want empty slot for malformed line", entries[2])
	}
	if entries[3].ToolName != "Read" || extractPath(entries[3].ToolInput) != "/x/team/MEMORY.md" {
		t.Errorf("entry 3 = %+v", entries[3])
	}

	got, err := ScanRawFile(raw, testRoots())
	if err != nil || len(got) != 1 || got[0].Seq != 3 {
		t.Fatalf("ScanRawFile = %+v, %v; want one consulted event at seq 3", got, err)
	}
}

// TestTagSessionReads_UsesAndIdempotent drives the finalize-time tagger end to
// end: surfaced refs come from the existing trace, used events are appended
// after the consulted ones, and a second run appends nothing. Failure
// prevented: a daemon re-finalize or CLI retry doubling every receipt.
func TestTagSessionReads_UsesAndIdempotent(t *testing.T) {
	dir := t.TempDir()
	raw := filepath.Join(dir, "raw.jsonl")
	lines := []string{
		`{"type":"header","metadata":{}}`,
		`{"type":"user","content":"prompt"}`,                                                      // 0
		`{"type":"user","content":"<whisper id=\"Ox7f2a\">auth notes</whisper>"}`,                 // 1 (whisper surfaced at seq 1)
		`{"type":"assistant","content":"Per Ox7f2a I will use pgx."}`,                             // 2 -> cited
		`{"type":"tool","tool_name":"Read","tool_input":"{\"file_path\":\"/x/team/MEMORY.md\"}"}`, // 3 -> consulted + read
	}
	if err := os.WriteFile(raw, []byte(joinLines(lines)), 0o644); err != nil {
		t.Fatal(err)
	}
	w := contexttrace.NewWriter(dir)
	pre := []contexttrace.Event{
		{Type: contexttrace.EventProvided, Source: contexttrace.SourceTeamMemory, Doc: "MEMORY.md", Seq: 0},
		{Type: contexttrace.EventProvided, Source: contexttrace.SourceTeamWhisper, Ref: "Ox7f2a", Title: "auth", FromName: "Dana", Seq: 1},
	}
	for _, ev := range pre {
		if err := w.Append(ev); err != nil {
			t.Fatal(err)
		}
	}

	n, err := TagSessionReads(raw, dir, testRoots())
	if err != nil || n != 3 {
		t.Fatalf("first run = %d, %v; want 3 (consulted, cited, read)", n, err)
	}
	events, _ := contexttrace.ReadEvents(w.Path())
	var used []contexttrace.Event
	for _, ev := range events {
		if ev.Type == contexttrace.EventUsed {
			used = append(used, ev)
		}
	}
	if len(used) != 2 {
		t.Fatalf("used events = %+v, want 2", used)
	}
	byEvidence := map[string]contexttrace.Event{}
	for _, ev := range used {
		byEvidence[ev.Evidence] = ev
	}
	if ev := byEvidence[contexttrace.EvidenceCited]; ev.Ref != "Ox7f2a" || ev.Seq != 2 || ev.SurfacedSeq != 1 || ev.Title != "auth" {
		t.Errorf("cited = %+v", ev)
	}
	if ev := byEvidence[contexttrace.EvidenceRead]; ev.Ref != "MEMORY.md" || ev.Seq != 3 || ev.RefType != "doc" {
		t.Errorf("read = %+v", ev)
	}

	n, err = TagSessionReads(raw, dir, testRoots())
	if err != nil || n != 0 {
		t.Fatalf("second run = %d, %v; want 0 (idempotent)", n, err)
	}
	again, _ := contexttrace.ReadEvents(w.Path())
	if len(again) != len(events) {
		t.Errorf("trace grew from %d to %d on re-run", len(events), len(again))
	}

	// Missing raw file: fail-open.
	if n, err := TagSessionReads(filepath.Join(dir, "nope.jsonl"), dir, testRoots()); n != 0 || err != nil {
		t.Errorf("missing raw: got (%d, %v)", n, err)
	}
}
