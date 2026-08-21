package format

import (
	"strings"
	"testing"
	"time"
)

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return ts
}

func TestLoadProjectedDistillationFinalizedFixture(t *testing.T) {
	p, err := LoadProjectedDistillation(fixture(t, "2026-08-15-01-00-finalized"))
	if err != nil {
		t.Fatalf("LoadProjectedDistillation: %v", err)
	}
	if p == nil {
		t.Fatal("want projection, got nil")
	}

	// Finalize marker promotes draft → finalized.
	if p.Status != EpisodeStatusFinalized {
		t.Errorf("Status = %q, want finalized", p.Status)
	}

	// TTL recomputed: extracted 02:00 + 1h + 2×30m = 04:00 (header ignored).
	if want := mustTime(t, "2026-08-15T04:00:00Z"); !p.TTLExpiresAt.Equal(want) {
		t.Errorf("TTLExpiresAt = %v, want %v", p.TTLExpiresAt, want)
	}

	// Atom text edit applied.
	if p.Atoms[0].Text != "Edited text." {
		t.Errorf("edited atom text = %q", p.Atoms[0].Text)
	}
	// Topic takeaway edit lands on Summary.
	if p.Topics[0].Summary != "Edited takeaway." {
		t.Errorf("topic summary = %q", p.Topics[0].Summary)
	}
	// Rejected atom is tombstoned at the edit time.
	if p.Atoms[1].ValidTo == nil || !p.Atoms[1].ValidTo.Equal(mustTime(t, "2026-08-15T03:02:00Z")) {
		t.Errorf("rejected atom ValidTo = %v", p.Atoms[1].ValidTo)
	}
	// Added atom appended with ValidFrom stamped from the edit.
	if len(p.Atoms) != 3 {
		t.Fatalf("atoms = %d, want 3 (base 2 + added)", len(p.Atoms))
	}
	added := p.Atoms[2]
	if added.ID != "at_019ff500-0000-7000-8000-00000000at03" || added.Text != "Added atom." {
		t.Errorf("added atom = %+v", added)
	}
	if added.ValidFrom == nil || !added.ValidFrom.Equal(mustTime(t, "2026-08-15T03:04:00Z")) {
		t.Errorf("added atom ValidFrom = %v", added.ValidFrom)
	}

	current, superseded := p.AtomCounts()
	if current != 2 || superseded != 1 {
		t.Errorf("counts = %d/%d, want 2/1", current, superseded)
	}
	if got := CurrentAtoms(p.Atoms); len(got) != 2 {
		t.Errorf("CurrentAtoms = %d, want 2", len(got))
	}
	if got := SupersededAtoms(p.Atoms); len(got) != 1 || got[0].ID != p.Atoms[1].ID {
		t.Errorf("SupersededAtoms = %+v", got)
	}

	// Sidecar defects aggregated: unknown edit action + malformed ttl line.
	var sawEditInvalid, sawTTLInvalid bool
	for _, inv := range p.Invalid {
		if strings.Contains(inv.Reason, "unknown edit action") {
			sawEditInvalid = true
		}
		if strings.Contains(inv.Path, TTLExtendsFileName) {
			sawTTLInvalid = true
		}
	}
	if !sawEditInvalid || !sawTTLInvalid {
		t.Errorf("aggregated Invalid missing sidecar defects: %+v", p.Invalid)
	}
}

func TestLoadProjectedDistillationDraftFixture(t *testing.T) {
	p, err := LoadProjectedDistillation(fixture(t, "2026-08-11-22-32-full"))
	if err != nil {
		t.Fatalf("LoadProjectedDistillation: %v", err)
	}
	// No sidecars at all: status stays draft, TTL = extracted + 1h.
	if p.Status != EpisodeStatusDraft {
		t.Errorf("Status = %q, want draft", p.Status)
	}
	if want := mustTime(t, "2026-08-18T03:55:27Z"); !p.TTLExpiresAt.Equal(want) {
		t.Errorf("TTLExpiresAt = %v, want %v", p.TTLExpiresAt, want)
	}
	// Base tombstone counted as superseded even with no edits.
	current, superseded := p.AtomCounts()
	if current != 2 || superseded != 1 {
		t.Errorf("counts = %d/%d, want 2/1", current, superseded)
	}
}

func TestLoadProjectedDistillationAbsence(t *testing.T) {
	p, err := LoadProjectedDistillation(fixture(t, "2026-08-12-01-00-legacy"))
	if p != nil || err != nil {
		t.Fatalf("got %v, %v; want nil, nil", p, err)
	}
}

func TestProjectTTL(t *testing.T) {
	extracted := mustTime(t, "2026-08-15T02:00:00Z")
	base := &Distillation{Episode: Episode{
		ID: "ep_x", Status: EpisodeStatusDraft,
		Provenance: Provenance{ExtractedAt: extracted},
	}}
	tests := []struct {
		name    string
		extends int
		want    string
	}{
		{"no extends is base 1h", 0, "2026-08-15T03:00:00Z"},
		{"two extends add an hour", 2, "2026-08-15T04:00:00Z"},
		{"nine extends hit the 4h cap", 9, "2026-08-15T06:00:00Z"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			extends := make([]TTLExtendRecord, tt.extends)
			p := Project(base, nil, nil, extends)
			if want := mustTime(t, tt.want); !p.TTLExpiresAt.Equal(want) {
				t.Errorf("TTLExpiresAt = %v, want %v", p.TTLExpiresAt, want)
			}
		})
	}
}

func TestProjectEdgeCases(t *testing.T) {
	t.Run("nil distillation projects to nil", func(t *testing.T) {
		if p := Project(nil, nil, nil, nil); p != nil {
			t.Fatalf("got %+v", p)
		}
	})
	t.Run("skipped episode header preserved", func(t *testing.T) {
		p, err := LoadProjectedDistillation(fixture(t, "2026-08-16-01-00-skipped"))
		if err != nil {
			t.Fatal(err)
		}
		if p.Status != EpisodeStatusSkipped || p.SkippedReason != "cluster_exhausted_v2" {
			t.Errorf("projected skipped episode = %+v", p)
		}
	})
	t.Run("finalize does not promote a skipped episode", func(t *testing.T) {
		d := &Distillation{Episode: Episode{ID: "ep_x", Status: EpisodeStatusSkipped}}
		p := Project(d, nil, []FinalizeRecord{{At: time.Now()}}, nil)
		if p.Status != EpisodeStatusSkipped {
			t.Errorf("Status = %q, want skipped", p.Status)
		}
	})
	t.Run("zero extracted_at leaves TTL zero", func(t *testing.T) {
		d := &Distillation{Episode: Episode{ID: "ep_x", Status: EpisodeStatusDraft}}
		p := Project(d, nil, nil, nil)
		if !p.TTLExpiresAt.IsZero() {
			t.Errorf("TTLExpiresAt = %v, want zero", p.TTLExpiresAt)
		}
	})
	t.Run("stale header TTL is ignored", func(t *testing.T) {
		header := mustTime(t, "2030-01-01T00:00:00Z")
		extracted := mustTime(t, "2026-08-15T02:00:00Z")
		d := &Distillation{Episode: Episode{
			ID: "ep_x", Status: EpisodeStatusDraft, TTLExpiresAt: &header,
			Provenance: Provenance{ExtractedAt: extracted},
		}}
		p := Project(d, nil, nil, nil)
		if want := extracted.Add(time.Hour); !p.TTLExpiresAt.Equal(want) {
			t.Errorf("TTLExpiresAt = %v, want %v (header must be ignored)", p.TTLExpiresAt, want)
		}
	})
	t.Run("edits on unknown targets and fields are ignored", func(t *testing.T) {
		d := &Distillation{
			Episode: Episode{ID: "ep_x", Status: EpisodeStatusDraft},
			Topics:  []Topic{{ID: "tp_1", Title: "T", Summary: "S"}},
			Atoms:   []Atom{{ID: "at_1", Text: "text"}},
		}
		edits := []EditRecord{
			{Action: EditActionEdit, AtomID: "at_missing", Field: "text", Value: "x", At: time.Now()},
			{Action: EditActionEdit, TopicID: "tp_missing", Field: "title", Value: "x", At: time.Now()},
			{Action: EditActionEdit, AtomID: "at_1", Field: "unknown_field", Value: "x", At: time.Now()},
			{Action: EditActionEdit, TopicID: "tp_1", Field: "unknown_field", Value: "x", At: time.Now()},
			{Action: EditActionReject, AtomID: "at_missing", At: time.Now()},
			{Action: EditActionAdd, At: time.Now()},                          // add without embedded atom
			{Action: EditActionAdd, Atom: &Atom{ID: "at_1"}, At: time.Now()}, // duplicate id ignored
		}
		p := Project(d, edits, nil, nil)
		if p.Atoms[0].Text != "text" || p.Topics[0].Title != "T" || p.Topics[0].Summary != "S" {
			t.Errorf("state mutated by ignorable edits: %+v", p)
		}
		if len(p.Atoms) != 1 {
			t.Errorf("atoms = %d, want 1", len(p.Atoms))
		}
	})
	t.Run("reject of already-tombstoned atom keeps original valid_to", func(t *testing.T) {
		orig := mustTime(t, "2026-08-15T02:30:00Z")
		d := &Distillation{
			Episode: Episode{ID: "ep_x", Status: EpisodeStatusDraft},
			Atoms:   []Atom{{ID: "at_1", ValidTo: &orig}},
		}
		p := Project(d, []EditRecord{{Action: EditActionReject, AtomID: "at_1", At: mustTime(t, "2026-08-15T05:00:00Z")}}, nil, nil)
		if !p.Atoms[0].ValidTo.Equal(orig) {
			t.Errorf("ValidTo = %v, want original %v", p.Atoms[0].ValidTo, orig)
		}
	})
	t.Run("edits and signal/kind fields apply", func(t *testing.T) {
		d := &Distillation{
			Episode: Episode{ID: "ep_x", Status: EpisodeStatusDraft},
			Topics:  []Topic{{ID: "tp_1", Title: "T", Summary: "S"}},
			Atoms:   []Atom{{ID: "at_1", Kind: "learning", Signal: "low"}},
		}
		edits := []EditRecord{
			{Action: EditActionEdit, AtomID: "at_1", Field: "kind", Value: "decision"},
			{Action: EditActionEdit, AtomID: "at_1", Field: "signal", Value: "high"},
			{Action: EditActionEdit, TopicID: "tp_1", Field: "title", Value: "New title"},
			{Action: EditActionEdit, TopicID: "tp_1", Field: "summary", Value: "New summary"},
		}
		p := Project(d, edits, nil, nil)
		if p.Atoms[0].Kind != "decision" || p.Atoms[0].Signal != "high" {
			t.Errorf("atom = %+v", p.Atoms[0])
		}
		if p.Topics[0].Title != "New title" || p.Topics[0].Summary != "New summary" {
			t.Errorf("topic = %+v", p.Topics[0])
		}
	})
	t.Run("base distillation is not mutated", func(t *testing.T) {
		d := &Distillation{
			Episode: Episode{ID: "ep_x", Status: EpisodeStatusDraft},
			Atoms:   []Atom{{ID: "at_1", Text: "orig"}},
		}
		Project(d, []EditRecord{{Action: EditActionEdit, AtomID: "at_1", Field: "text", Value: "new", At: time.Now()}}, nil, nil)
		if d.Atoms[0].Text != "orig" {
			t.Errorf("base mutated: %q", d.Atoms[0].Text)
		}
	})
}
