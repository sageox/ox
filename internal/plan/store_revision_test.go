package plan

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// --- Versions, not forks: an explicit slug revises the plan it names ---

// saveWithSlug saves a minimal plan under an explicit slug on a given day.
func saveWithSlug(t *testing.T, slug string, day time.Time, body string) string {
	t.Helper()
	dir, _, err := Save("/fake/git/root", Input{Raw: body}, Result{}, []byte("<html><body>"+body+"</body></html>"),
		Meta{Topic: "Revision target", Slug: slug, CreatedAt: day, Primary: PrimaryHTML})
	if err != nil {
		t.Fatalf("Save(%s): %v", slug, err)
	}
	return dir
}

// TestSave_ExplicitSlugRevisesAcrossDays verifies that re-saving a page whose
// explicit slug names an existing live plan lands in THAT plan's dir with the
// same pln_ id and a `revised` event.
// Failure prevented: a page re-saved the next day forked a second dated dir with
// a second id, splitting its link and review history in two.
func TestSave_ExplicitSlugRevisesAcrossDays(t *testing.T) {
	ledger := t.TempDir()
	withLedger(t, ledger)

	day1 := time.Date(2026, 9, 21, 9, 0, 0, 0, time.UTC)
	day2 := time.Date(2026, 9, 22, 9, 0, 0, 0, time.UTC)
	first := saveWithSlug(t, "recovery-e2e", day1, "v1")
	second := saveWithSlug(t, "recovery-e2e", day2, "v2")

	if first != second {
		t.Fatalf("second save forked a new dir:\n first=%s\nsecond=%s", first, second)
	}
	events, err := LoadEvents(first)
	if err != nil {
		t.Fatalf("LoadEvents: %v", err)
	}
	if len(events) != 2 || events[0].Kind != EventCreated || events[1].Kind != EventRevised {
		t.Fatalf("events = %+v, want [created, revised]", events)
	}
	if events[0].PlanID == "" || events[0].PlanID != events[1].PlanID {
		t.Errorf("plan id changed across revisions: %q -> %q", events[0].PlanID, events[1].PlanID)
	}
	meta, err := LoadMeta(first)
	if err != nil {
		t.Fatalf("LoadMeta: %v", err)
	}
	if !meta.CreatedAt.Equal(day1) {
		t.Errorf("CreatedAt = %v, want original %v", meta.CreatedAt, day1)
	}
	entries, _ := os.ReadDir(filepath.Join(ledger, "data", "plans"))
	if len(entries) != 1 {
		t.Errorf("plans dir has %d entries, want 1", len(entries))
	}
}

// TestSave_ExplicitSlugResolution covers the other resolution outcomes.
// Failure prevented: an ambiguous slug silently writing one plan's page into
// another plan's history, or a deliberately closed plan being reopened.
func TestSave_ExplicitSlugResolution(t *testing.T) {
	day1 := time.Date(2026, 9, 21, 9, 0, 0, 0, time.UTC)
	day2 := time.Date(2026, 9, 22, 9, 0, 0, 0, time.UTC)
	day3 := time.Date(2026, 9, 23, 9, 0, 0, 0, time.UTC)

	tests := []struct {
		name    string
		setup   func(t *testing.T, plansDir string)
		wantErr bool
		wantDir string // base name expected when no error
	}{
		{
			name:    "no prior plan: fresh dated dir",
			setup:   func(t *testing.T, plansDir string) {},
			wantDir: "2026-09-23-dup",
		},
		{
			name: "two live plans share the slug: refuse with candidates",
			setup: func(t *testing.T, plansDir string) {
				saveWithSlug(t, "dup", day1, "a")
				// a second dir with the same slug, as legacy saves produced
				d := filepath.Join(plansDir, "2026-09-22-dup")
				if err := os.MkdirAll(d, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := writeMetaForTest(d, Meta{Topic: "dup", Slug: "dup", CreatedAt: day2}); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: true,
		},
		{
			name: "only match is abandoned: fresh dated dir, never reopened",
			setup: func(t *testing.T, plansDir string) {
				d := saveWithSlug(t, "dup", day1, "a")
				if _, err := AppendPlanEvent(context.Background(), d, EventAbandoned, PlanEventFields{Reason: "dropped"}); err != nil {
					t.Fatal(err)
				}
			},
			wantDir: "2026-09-23-dup",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ledger := t.TempDir()
			withLedger(t, ledger)
			plansDir := filepath.Join(ledger, "data", "plans")
			tt.setup(t, plansDir)

			dir, _, err := Save("/fake/git/root", Input{Raw: "x"}, Result{}, nil, Meta{Topic: "dup", Slug: "dup", CreatedAt: day3})
			if tt.wantErr {
				var amb *AmbiguousSlugError
				if !errors.As(err, &amb) {
					t.Fatalf("err = %v, want AmbiguousSlugError", err)
				}
				if len(amb.Candidates) != 2 {
					t.Errorf("candidates = %v, want both dirs", amb.Candidates)
				}
				if _, statErr := os.Stat(filepath.Join(plansDir, "2026-09-23-dup")); !os.IsNotExist(statErr) {
					t.Error("an ambiguous save must not create a new dir")
				}
				return
			}
			if err != nil {
				t.Fatalf("Save: %v", err)
			}
			if filepath.Base(dir) != tt.wantDir {
				t.Errorf("dir = %s, want %s", filepath.Base(dir), tt.wantDir)
			}
		})
	}
}

// TestResolvePlanDir_AmbiguousSlug verifies slug lookups refuse to guess and
// that the full dated dir name stays an unambiguous escape hatch.
// Failure prevented: `ox plan approve recovery-e2e` approving whichever of two
// same-slug dirs ReadDir happened to list first.
func TestResolvePlanDir_AmbiguousSlug(t *testing.T) {
	plansDir := t.TempDir()
	for _, name := range []string{"2026-09-21-2026-09-21-recovery-e2e", "2026-09-22-2026-09-21-recovery-e2e"} {
		d := filepath.Join(plansDir, name)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := writeMetaForTest(d, Meta{Topic: "recovery", Slug: "2026-09-21-recovery-e2e"}); err != nil {
			t.Fatal(err)
		}
	}

	tests := []struct {
		name    string
		slug    string
		wantErr bool
	}{
		{"shared slug is ambiguous", "2026-09-21-recovery-e2e", true},
		{"full dir name resolves", "2026-09-22-2026-09-21-recovery-e2e", false},
		{"unknown slug is not found", "nope", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := resolvePlanDir(plansDir, tt.slug)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
	_, _, err := resolvePlanDir(plansDir, "2026-09-21-recovery-e2e")
	var amb *AmbiguousSlugError
	if !errors.As(err, &amb) || len(amb.Candidates) != 2 {
		t.Errorf("want AmbiguousSlugError naming both dirs, got %v", err)
	}
}

// TestList_CarriesPlanID verifies `ox plan list --json` can print share links.
// Failure prevented: scripts had to re-read events.jsonl to learn a plan's id.
func TestList_CarriesPlanID(t *testing.T) {
	ledger := t.TempDir()
	withLedger(t, ledger)
	dir := saveWithSlug(t, "listed", time.Date(2026, 9, 21, 9, 0, 0, 0, time.UTC), "v1")
	events, err := LoadEvents(dir)
	if err != nil || len(events) == 0 {
		t.Fatalf("LoadEvents: %v", err)
	}
	infos, err := List("/fake/git/root")
	if err != nil || len(infos) != 1 {
		t.Fatalf("List: %v n=%d", err, len(infos))
	}
	if infos[0].PlanID != events[0].PlanID || infos[0].PlanID == "" {
		t.Errorf("PlanID = %q, want %q", infos[0].PlanID, events[0].PlanID)
	}
	if infos[0].Status != PlanStatusDraft {
		t.Errorf("Status = %q, want draft", infos[0].Status)
	}
}

func writeMetaForTest(dir string, m Meta) error {
	return MutatePlanMeta(context.Background(), dir, func(*Meta) (*Meta, error) { return &m, nil })
}
