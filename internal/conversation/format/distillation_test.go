package format

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLoadDistillationFullFixture(t *testing.T) {
	d, err := LoadDistillation(fixture(t, "2026-08-11-22-32-full"))
	if err != nil {
		t.Fatalf("LoadDistillation: %v", err)
	}
	if d == nil {
		t.Fatal("want distillation, got nil")
	}
	if d.Episode.Status != EpisodeStatusDraft || d.Episode.ID != "ep_01a012cb-9763-72fc-930a-b74ca843c611" {
		t.Errorf("episode = %+v", d.Episode)
	}
	if len(d.Topics) != 1 || d.Topics[0].ID != "tp_01a012cb-9764-7555-a3f3-ce3377e47d98" {
		t.Errorf("topics = %+v", d.Topics)
	}
	if len(d.Atoms) != 3 {
		t.Fatalf("atoms = %d, want 3", len(d.Atoms))
	}

	// Legacy singular source.uri is tolerated as an unparsed citation.
	legacy := d.Atoms[1]
	if legacy.Source.URI == "" || len(legacy.Source.URIs) != 0 {
		t.Errorf("legacy atom source = %+v", legacy.Source)
	}
	if got := legacy.Source.CitationURIs(); !reflect.DeepEqual(got, []string{legacy.Source.URI}) {
		t.Errorf("CitationURIs = %v", got)
	}
	modern := d.Atoms[0]
	if got := modern.Source.CitationURIs(); !reflect.DeepEqual(got, modern.Source.URIs) {
		t.Errorf("modern CitationURIs = %v", got)
	}
	if got := (AtomSource{}).CitationURIs(); got != nil {
		t.Errorf("empty CitationURIs = %v, want nil", got)
	}

	// Base tombstone decodes with its bi-temporal fields.
	tomb := d.Atoms[2]
	if tomb.ValidTo == nil || tomb.SupersededBy == "" {
		t.Errorf("tombstone atom = %+v", tomb)
	}

	// Unknown-type, malformed, and id-less lines are skipped and surfaced.
	if len(d.Invalid) != 3 {
		t.Fatalf("invalid = %+v, want 3", d.Invalid)
	}
	wantReasons := []string{"unknown record type", "malformed line", "unusable atom line"}
	for i, want := range wantReasons {
		found := false
		for _, inv := range d.Invalid {
			if strings.Contains(inv.Reason, want) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing invalid reason %d ~%q in %+v", i, want, d.Invalid)
		}
	}
}

func TestLoadDistillationAbsence(t *testing.T) {
	d, err := LoadDistillation(fixture(t, "2026-08-12-01-00-legacy"))
	if d != nil || err != nil {
		t.Fatalf("got %v, %v; want nil, nil", d, err)
	}
}

func TestLoadDistillationSkippedEpisode(t *testing.T) {
	d, err := LoadDistillation(fixture(t, "2026-08-16-01-00-skipped"))
	if err != nil {
		t.Fatalf("LoadDistillation: %v", err)
	}
	if d.Episode.Status != EpisodeStatusSkipped || d.Episode.SkippedReason != "cluster_exhausted_v2" {
		t.Errorf("episode = %+v", d.Episode)
	}
	if len(d.Topics) != 0 || len(d.Atoms) != 0 {
		t.Errorf("skipped episode should carry no topics/atoms: %+v", d)
	}
}

func TestLoadDistillationEpisodeStrictness(t *testing.T) {
	tests := []struct {
		name  string
		lines string
	}{
		{"no episode line", `{"type":"topic","id":"tp_x"}`},
		{"episode missing id", `{"type":"episode","status":"draft"}`},
		{"episode missing status", `{"type":"episode","id":"ep_x"}`},
		{"episode type mismatch", `{"type":"episode","id":"ep_x","status":"draft","provenance":"not an object"}`},
		{"multiple episode lines", "{\"type\":\"episode\",\"id\":\"ep_x\",\"status\":\"draft\"}\n{\"type\":\"episode\",\"id\":\"ep_y\",\"status\":\"draft\"}"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			writeDistillation(t, root, tt.lines)
			if _, err := LoadDistillation(root); err == nil {
				t.Fatal("want error")
			}
		})
	}
}

func writeDistillation(t *testing.T, root, content string) {
	t.Helper()
	dir := filepath.Join(root, DistillationDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, DistillationFileName), []byte(content+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadEditsFinalizedFixture(t *testing.T) {
	edits, invalid, err := LoadEdits(fixture(t, "2026-08-15-01-00-finalized"))
	if err != nil {
		t.Fatalf("LoadEdits: %v", err)
	}
	if len(edits) != 5 {
		t.Fatalf("edits = %d, want 5 (unknown action excluded): %+v", len(edits), edits)
	}
	actions := make([]string, len(edits))
	for i, e := range edits {
		actions[i] = e.Action
	}
	want := []string{EditActionEdit, EditActionEdit, EditActionReject, EditActionRedact, EditActionAdd}
	if !reflect.DeepEqual(actions, want) {
		t.Errorf("actions = %v, want %v", actions, want)
	}
	if len(invalid) != 1 || !strings.Contains(invalid[0].Reason, "unknown edit action") {
		t.Errorf("invalid = %+v", invalid)
	}
}

func TestLoadSidecarAbsence(t *testing.T) {
	root := t.TempDir()
	if edits, invalid, err := LoadEdits(root); edits != nil || invalid != nil || err != nil {
		t.Fatalf("LoadEdits empty: %v %v %v", edits, invalid, err)
	}
	if recs, invalid, err := LoadFinalize(root); recs != nil || invalid != nil || err != nil {
		t.Fatalf("LoadFinalize empty: %v %v %v", recs, invalid, err)
	}
	if recs, invalid, err := LoadTTLExtends(root); recs != nil || invalid != nil || err != nil {
		t.Fatalf("LoadTTLExtends empty: %v %v %v", recs, invalid, err)
	}
}

func TestLoadFinalizeWellFormedness(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, DistillationDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := `{"actor":"user"}` + "\n" + `not json` + "\n" + `{"actor":"user","at":"2026-08-15T04:00:00Z"}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, FinalizeFileName), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	recs, invalid, err := LoadFinalize(root)
	if err != nil {
		t.Fatalf("LoadFinalize: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("records = %+v, want exactly the well-formed marker", recs)
	}
	if len(invalid) != 2 {
		t.Fatalf("invalid = %+v, want 2", invalid)
	}
}

func TestLoadTTLExtendsCountsWellFormedLines(t *testing.T) {
	recs, invalid, err := LoadTTLExtends(fixture(t, "2026-08-15-01-00-finalized"))
	if err != nil {
		t.Fatalf("LoadTTLExtends: %v", err)
	}
	if len(recs) != 2 {
		t.Errorf("records = %d, want 2", len(recs))
	}
	if len(invalid) != 1 {
		t.Errorf("invalid = %+v, want the malformed line surfaced", invalid)
	}
}

// writeSidecar stages one distillation sidecar file under root.
func writeSidecar(t *testing.T, root, fileName, content string) {
	t.Helper()
	dir := filepath.Join(root, DistillationDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, fileName), []byte(content+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestLoadEditsRejectsMissingAt verifies a live edit record whose at
// timestamp is null or omitted is surfaced as invalid, never returned for
// folding. Failure prevented: a zero-time reject tombstones its atom at year
// 0001 — the atom silently vanishes from the current view and an invalid
// valid_to is emitted under --include-superseded. Retired redact records
// stay accepted as no-ops: legacy data omits at.
func TestLoadEditsRejectsMissingAt(t *testing.T) {
	tests := []struct {
		name      string
		line      string
		wantEdits int
	}{
		{name: "reject with null at", line: `{"edit_id":"e1","action":"reject","atom_id":"at1","at":null}`},
		{name: "reject with missing at", line: `{"edit_id":"e1","action":"reject","atom_id":"at1"}`},
		{name: "edit with missing at", line: `{"edit_id":"e1","action":"edit","atom_id":"at1","field":"text","value":"x"}`},
		{name: "add with missing at", line: `{"edit_id":"e1","action":"add","atom":{"id":"at9","text":"y"}}`},
		{name: "reject with valid at", line: `{"edit_id":"e1","action":"reject","atom_id":"at1","at":"2026-08-15T03:02:00Z"}`, wantEdits: 1},
		{name: "legacy redact without at is a no-op record", line: `{"edit_id":"e1","action":"redact","atom_id":"at1"}`, wantEdits: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			writeSidecar(t, root, EditsFileName, tt.line)
			edits, invalid, err := LoadEdits(root)
			if err != nil {
				t.Fatalf("LoadEdits: %v", err)
			}
			if len(edits) != tt.wantEdits {
				t.Fatalf("edits = %+v, want %d records", edits, tt.wantEdits)
			}
			wantInvalid := 1 - tt.wantEdits
			if len(invalid) != wantInvalid {
				t.Fatalf("invalid = %+v, want %d records", invalid, wantInvalid)
			}
			if wantInvalid == 1 && !strings.Contains(invalid[0].Reason, "missing at") {
				t.Errorf("invalid reason = %q, want it to name the missing at timestamp", invalid[0].Reason)
			}
		})
	}
}

// TestLoadTTLExtendsRejectsNonObjectLines verifies null and other non-object
// JSON lines in ttl_extends.jsonl are surfaced as invalid and never counted.
// Failure prevented: json.Unmarshal accepts a literal null into a struct
// without error, so a null line silently counted as a +30m TTL extension.
func TestLoadTTLExtendsRejectsNonObjectLines(t *testing.T) {
	root := t.TempDir()
	writeSidecar(t, root, TTLExtendsFileName,
		"null\n"+`{"actor":"user","at":"2026-08-15T02:30:00Z"}`+"\n"+`"extend"`+"\n"+"42\n[]")
	recs, invalid, err := LoadTTLExtends(root)
	if err != nil {
		t.Fatalf("LoadTTLExtends: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("records = %+v, want exactly the one object line", recs)
	}
	if len(invalid) != 4 {
		t.Fatalf("invalid = %+v, want the 4 non-object lines surfaced", invalid)
	}
	for _, inv := range invalid {
		if !strings.Contains(inv.Reason, "not a JSON object") {
			t.Errorf("invalid reason = %q, want it to name the non-object shape", inv.Reason)
		}
	}
}

// TestProjectedTTLIgnoresNullExtendLines proves the projection end of the
// same bug: a ttl_extends.jsonl holding only a null line yields the base
// 1-hour TTL, not 1h30m, and the defect is surfaced on the projected view.
func TestProjectedTTLIgnoresNullExtendLines(t *testing.T) {
	root := t.TempDir()
	writeDistillation(t, root,
		`{"type":"episode","id":"ep1","status":"finalized","provenance":{"extracted_at":"2026-08-15T02:00:00Z"}}`)
	writeSidecar(t, root, TTLExtendsFileName, "null")
	p, err := LoadProjectedDistillation(root)
	if err != nil {
		t.Fatalf("LoadProjectedDistillation: %v", err)
	}
	want := p.ExtractedAt.Add(ttlBase)
	if !p.TTLExpiresAt.Equal(want) {
		t.Fatalf("TTLExpiresAt = %v, want base %v (null line must not extend)", p.TTLExpiresAt, want)
	}
	if len(p.Invalid) != 1 || !strings.Contains(p.Invalid[0].Reason, "not a JSON object") {
		t.Fatalf("Invalid = %+v, want the null line surfaced", p.Invalid)
	}
}
