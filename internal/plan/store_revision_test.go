package plan

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
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

// TestSave_SameDayClosedPlanGetsFreshDir verifies a save whose dated dir name
// is already held by a CLOSED plan steps around it instead of writing into it.
// Failure prevented: supersede a plan, save its replacement the same day under
// the same slug, and the "replacement" overwrites the closed plan's page,
// reuses its pln_ id, and is logged as that plan's revision.
func TestSave_SameDayClosedPlanGetsFreshDir(t *testing.T) {
	day := time.Date(2026, 9, 23, 9, 0, 0, 0, time.UTC)
	tests := []struct {
		name  string
		close EventKind
		meta  func() Meta // the replacement save
	}{
		{
			name:  "explicit slug after supersede",
			close: EventSuperseded,
			meta:  func() Meta { return Meta{Topic: "Revision target", Slug: "dup", CreatedAt: day, Primary: PrimaryHTML} },
		},
		{
			name:  "derived slug after abandon",
			close: EventAbandoned,
			meta:  func() Meta { return Meta{Topic: "dup", CreatedAt: day, SourcePlanPath: "/src/other.html"} },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ledger := t.TempDir()
			withLedger(t, ledger)
			closed := saveWithSlug(t, "dup", day, "closed body")
			if _, err := AppendPlanEvent(context.Background(), closed, tt.close, PlanEventFields{Reason: "replaced", SupersededBy: "replacement-plan"}); err != nil {
				t.Fatal(err)
			}
			closedEvents, err := LoadEvents(closed)
			if err != nil {
				t.Fatal(err)
			}

			dir, _, err := Save("/fake/git/root", Input{Raw: "replacement body"}, Result{}, nil, tt.meta())
			if err != nil {
				t.Fatalf("Save: %v", err)
			}
			if dir == closed {
				t.Fatalf("replacement wrote into the closed plan's dir %s", filepath.Base(closed))
			}
			if got, want := filepath.Base(dir), "2026-09-23-dup-2"; got != want {
				t.Errorf("dir = %s, want %s", got, want)
			}
			body, err := os.ReadFile(filepath.Join(closed, planMDFile))
			if err != nil || string(body) != "closed body" {
				t.Errorf("closed plan.md = %q (err %v), want it untouched", body, err)
			}
			after, err := LoadEvents(closed)
			if err != nil || len(after) != len(closedEvents) {
				t.Errorf("closed plan gained events: %d -> %d", len(closedEvents), len(after))
			}
			fresh, err := LoadEvents(dir)
			if err != nil || len(fresh) != 1 || fresh[0].Kind != EventCreated {
				t.Fatalf("replacement events = %+v, want one created", fresh)
			}
			if fresh[0].PlanID == closedEvents[0].PlanID {
				t.Error("replacement reused the closed plan's pln_ id")
			}

			// The next save of the same page revises the replacement, never
			// the closed plan its dir name was stepped around.
			again, _, err := Save("/fake/git/root", Input{Raw: "replacement v2"}, Result{}, nil, tt.meta())
			if err != nil {
				t.Fatalf("re-save: %v", err)
			}
			if again != dir {
				t.Errorf("re-save landed in %s, want the replacement %s", filepath.Base(again), filepath.Base(dir))
			}
		})
	}
}

// TestResolvePlanDir_PrefersSoleLivePlan verifies lookups follow save's rule:
// after supersede-then-resave, the slug names the one live plan.
// Failure prevented: `ox plan status <slug>` / approve / view refused as
// ambiguous right after the standard supersede flow.
func TestResolvePlanDir_PrefersSoleLivePlan(t *testing.T) {
	day1 := time.Date(2026, 9, 21, 9, 0, 0, 0, time.UTC)
	day2 := time.Date(2026, 9, 22, 9, 0, 0, 0, time.UTC)

	tests := []struct {
		name     string
		closeNew bool // also close the newer plan: two closed, zero live
		wantBase string
		wantAmb  bool
	}{
		{name: "one closed + one live resolves to live", wantBase: "2026-09-22-dup"},
		{name: "all closed stays ambiguous", closeNew: true, wantAmb: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ledger := t.TempDir()
			withLedger(t, ledger)
			plansDir := filepath.Join(ledger, "data", "plans")
			old := saveWithSlug(t, "dup", day1, "old")
			if _, err := AppendPlanEvent(context.Background(), old, EventSuperseded, PlanEventFields{Reason: "replaced", SupersededBy: "replacement-plan"}); err != nil {
				t.Fatal(err)
			}
			newer := saveWithSlug(t, "dup", day2, "new")
			if tt.closeNew {
				if _, err := AppendPlanEvent(context.Background(), newer, EventAbandoned, PlanEventFields{Reason: "dropped"}); err != nil {
					t.Fatal(err)
				}
			}

			dir, name, err := resolvePlanDir(plansDir, "dup")
			if tt.wantAmb {
				var amb *AmbiguousSlugError
				if !errors.As(err, &amb) || len(amb.Candidates) != 2 {
					t.Fatalf("err = %v, want AmbiguousSlugError naming both", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolvePlanDir: %v", err)
			}
			if name != tt.wantBase || dir != newer {
				t.Errorf("resolved %s, want %s", name, tt.wantBase)
			}
			// the closed plan stays addressable by its full dir name
			if _, n, err := resolvePlanDir(plansDir, filepath.Base(old)); err != nil || n != filepath.Base(old) {
				t.Errorf("full dir name of the closed plan: %s err=%v", n, err)
			}
		})
	}
}

// TestResolveSaveDir_MatchesSave verifies the CLI's pre-save probe names the
// exact dir Save then writes, and fails the same way Save does without a
// ledger. Failure prevented: the prior-revision snapshot guarding one dir
// while Save overwrites another.
func TestResolveSaveDir_MatchesSave(t *testing.T) {
	day := time.Date(2026, 9, 23, 9, 0, 0, 0, time.UTC)
	ledger := t.TempDir()
	withLedger(t, ledger)
	closed := saveWithSlug(t, "dup", day, "closed")
	if _, err := AppendPlanEvent(context.Background(), closed, EventAbandoned, PlanEventFields{Reason: "dropped"}); err != nil {
		t.Fatal(err)
	}

	meta := Meta{Topic: "Revision target", Slug: "dup", CreatedAt: day}
	probe, err := ResolveSaveDir("/fake/git/root", meta)
	if err != nil {
		t.Fatalf("ResolveSaveDir: %v", err)
	}
	saved, _, err := Save("/fake/git/root", Input{Raw: "x"}, Result{}, nil, meta)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if probe != saved {
		t.Errorf("probe %s != saved %s", filepath.Base(probe), filepath.Base(saved))
	}

	// zero CreatedAt defaults to now rather than the zero date
	today := time.Now().UTC().Format("2006-01-02")
	d, err := ResolveSaveDir("/fake/git/root", Meta{Topic: "Fresh topic"})
	if err != nil {
		t.Fatalf("ResolveSaveDir with zero CreatedAt: %v", err)
	}
	if !strings.HasPrefix(filepath.Base(d), today) {
		t.Errorf("zero CreatedAt resolved to %s, want a %s dir", filepath.Base(d), today)
	}

	withLedger(t, "")
	if _, err := ResolveSaveDir("/fake/git/root", meta); err == nil {
		t.Error("no ledger must be an error, not a guessed dir")
	}
}

// TestAmbiguousSlugError_NamesCandidatesAndFix verifies the refusal tells the
// user which dirs collide and how to pick one.
func TestAmbiguousSlugError_NamesCandidatesAndFix(t *testing.T) {
	err := &AmbiguousSlugError{Slug: "dup", Candidates: []string{"2026-09-21-dup", "2026-09-22-dup"}}
	msg := err.Error()
	for _, want := range []string{`"dup"`, "2 saved plans", "2026-09-21-dup, 2026-09-22-dup", "full directory name"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q missing %q", msg, want)
		}
	}
}

// TestKindsHint_ListsExactlyTheValidKinds verifies every `--kind` hint names
// every accepted kind and nothing else. Failure prevented: a hint listing a
// subset, so a coworker concludes a valid kind (evidence) is unsupported.
func TestKindsHint_ListsExactlyTheValidKinds(t *testing.T) {
	hint := KindsHint()
	if hint != "plan|mockup|review|evidence" {
		t.Errorf("KindsHint = %q", hint)
	}
	for _, k := range strings.Split(hint, "|") {
		if !ValidKind(k) {
			t.Errorf("hint names %q, which ValidKind rejects", k)
		}
	}
	for _, k := range []string{"", " Mockup ", "EVIDENCE"} {
		if !ValidKind(k) {
			t.Errorf("ValidKind(%q) = false, want true", k)
		}
	}
	if ValidKind("design") {
		t.Error("ValidKind accepted an unknown kind")
	}
}
