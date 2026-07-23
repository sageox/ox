package plan

// store_events_test.go covers Save()'s event-log dual-write (Phase 1:
// additive alongside meta.json — see events.go and html_meta.go). These tests
// exercise Save() end-to-end (not events.go's primitives in isolation, which
// events_test.go already covers): the pln_ mint-vs-reuse decision, the
// provenance mapping onto the appended event, that meta.json keeps writing
// exactly as before, and that a corrupt events.jsonl can never block a save.

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sageox/ox/internal/planid"
)

// sampleProvenance returns a fully-populated Provenance so event-mapping
// assertions can check every field the event's provenance mapping carries
// across from meta.Provenance.
func sampleProvenance() *Provenance {
	return &Provenance{
		SessionName:    "2026-07-01-person-a-Oxab12",
		SessionID:      "ses_test0001",
		AgentID:        "Oxab12",
		RepoID:         "repo-1",
		AgentType:      "claude-code",
		Model:          "claude-sonnet-5",
		AuthorName:     "Person A",
		SessionOutcome: SessionOutcomeActive,
	}
}

// TestSave_FirstSaveWritesCreatedEvent verifies the first save of a fresh plan
// directory mints a pln_ id, writes a single `created` event carrying the
// resolved provenance and a draft status, and leaves Topic unset — the
// title's source of truth is plan.html's <head> (see html_meta.go), not the
// event log.
func TestSave_FirstSaveWritesCreatedEvent(t *testing.T) {
	ledger := t.TempDir()
	withLedger(t, ledger)

	in := Input{Raw: "# Event log foundation\n\nbody"}
	meta := Meta{
		Topic:      "Event log foundation",
		CreatedAt:  time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC),
		Provenance: sampleProvenance(),
	}

	dir, err := Save("/g", in, sampleResult(), nil, meta)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	events, err := LoadEvents(dir)
	if err != nil {
		t.Fatalf("LoadEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("want 1 event after first save, got %d: %+v", len(events), events)
	}
	ev := events[0]

	if !planid.IsPlanID(ev.PlanID) {
		t.Errorf("PlanID = %q, not a valid pln_ id", ev.PlanID)
	}
	if ev.Kind != EventCreated {
		t.Errorf("Kind = %q, want %q", ev.Kind, EventCreated)
	}
	if ev.Status != PlanStatusDraft {
		t.Errorf("Status = %q, want %q", ev.Status, PlanStatusDraft)
	}
	if ev.Topic != "" {
		t.Errorf("Topic = %q, want empty (title lives in plan.html, not the event log)", ev.Topic)
	}
	if ev.SessionID != "ses_test0001" || ev.SessionName != "2026-07-01-person-a-Oxab12" {
		t.Errorf("session fields = (%q,%q), want provenance's", ev.SessionID, ev.SessionName)
	}
	if ev.AgentID != "Oxab12" {
		t.Errorf("AgentID = %q, want Oxab12", ev.AgentID)
	}
	if ev.AgentEnv != "claude-code" {
		t.Errorf("AgentEnv = %q, want claude-code (from Provenance.AgentType)", ev.AgentEnv)
	}
	if ev.Model != "claude-sonnet-5" {
		t.Errorf("Model = %q, want claude-sonnet-5", ev.Model)
	}
	if ev.Author != "Person A" {
		t.Errorf("Author = %q, want Person A (from Provenance.AuthorName)", ev.Author)
	}
}

// TestSave_ConcurrentFirstSaveMintsSinglePlanID is the regression test for the
// planID-minting race: concurrent Save calls landing in the same fresh plan
// directory (same Topic/CreatedAt -> same dated-slug dir) must fold to a
// single `created` event and a single pln_ id, never one per goroutine that
// raced the unlocked "is this the first save" read. Fails without the fix
// (LoadEvents/firstSave/planID resolved outside the events.jsonl flock, so
// two goroutines can both observe "no events yet" before either appends) and
// passes with it (the whole resolve-then-append sequence is one critical
// section under events.jsonl's flock).
func TestSave_ConcurrentFirstSaveMintsSinglePlanID(t *testing.T) {
	ledger := t.TempDir()
	withLedger(t, ledger)

	const n = 8
	in := Input{Raw: "# Concurrent First Save\n\nbody"}
	meta := Meta{
		Topic:     "Concurrent First Save",
		CreatedAt: time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC),
	}

	var wg sync.WaitGroup
	dirs := make([]string, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			dirs[i], errs[i] = Save("/g", in, sampleResult(), nil, meta)
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("Save[%d]: %v", i, err)
		}
	}
	for i := 1; i < n; i++ {
		if dirs[i] != dirs[0] {
			t.Fatalf("Save calls landed in different dirs: %q vs %q", dirs[0], dirs[i])
		}
	}

	events, err := LoadEvents(dirs[0])
	if err != nil {
		t.Fatalf("LoadEvents: %v", err)
	}

	planIDs := map[string]bool{}
	createdCount := 0
	for _, ev := range events {
		planIDs[ev.PlanID] = true
		if ev.Kind == EventCreated {
			createdCount++
		}
	}
	if len(planIDs) != 1 {
		t.Errorf("events carry %d distinct plan ids after %d concurrent first-saves, want 1: %v", len(planIDs), n, planIDs)
	}
	if createdCount != 1 {
		t.Errorf("got %d `created` events after %d concurrent first-saves, want exactly 1", createdCount, n)
	}
}

// TestSave_SecondSaveAppendsRevisedEventSamePlanID verifies a re-save on the
// same dated-slug directory appends a `revised` event (no status) that reuses
// the SAME pln_ minted on the first save — the id is stable for the plan's
// life.
func TestSave_SecondSaveAppendsRevisedEventSamePlanID(t *testing.T) {
	ledger := t.TempDir()
	withLedger(t, ledger)

	created := time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC)
	in := Input{Raw: "# Stable plan id\n\nv1"}
	meta := Meta{Topic: "Stable plan id", CreatedAt: created, Provenance: sampleProvenance()}

	dir, err := Save("/g", in, sampleResult(), nil, meta)
	if err != nil {
		t.Fatalf("Save v1: %v", err)
	}

	// Second save: same topic/slug/day, so Save() computes the same
	// dated-slug directory (the real "agent revises the plan" path).
	if _, err := Save("/g", Input{Raw: "# Stable plan id\n\nv2"}, sampleResult(), nil, meta); err != nil {
		t.Fatalf("Save v2: %v", err)
	}

	events, err := LoadEvents(dir)
	if err != nil {
		t.Fatalf("LoadEvents: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("want 2 events after two saves, got %d: %+v", len(events), events)
	}
	if events[0].Kind != EventCreated {
		t.Errorf("events[0].Kind = %q, want created", events[0].Kind)
	}
	if events[1].Kind != EventRevised {
		t.Errorf("events[1].Kind = %q, want revised", events[1].Kind)
	}
	if events[1].Status != "" {
		t.Errorf("events[1].Status = %q, want empty (only the created event carries draft status)", events[1].Status)
	}
	if events[0].PlanID == "" {
		t.Fatal("first event's PlanID is empty")
	}
	if events[1].PlanID != events[0].PlanID {
		t.Errorf("PlanID drifted across saves: %q != %q, want stable", events[1].PlanID, events[0].PlanID)
	}
}

// TestSave_MetaJSONStillWrittenAlongsideEvents pins Phase 1's dual-write
// contract directly: meta.json continues to be written exactly as before, and
// both it and events.jsonl exist and agree on status after a save.
func TestSave_MetaJSONStillWrittenAlongsideEvents(t *testing.T) {
	ledger := t.TempDir()
	withLedger(t, ledger)

	meta := Meta{Topic: "Dual write check", CreatedAt: time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC), Provenance: sampleProvenance()}
	dir, err := Save("/g", Input{Raw: "# Dual write check\n"}, sampleResult(), nil, meta)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "meta.json")); err != nil {
		t.Errorf("meta.json missing after save: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "events.jsonl")); err != nil {
		t.Errorf("events.jsonl missing after save: %v", err)
	}

	gotMeta, err := ReadPlanMeta("/g", "dual-write-check")
	if err != nil {
		t.Fatalf("ReadPlanMeta: %v", err)
	}
	events, err := LoadEvents(dir)
	if err != nil {
		t.Fatalf("LoadEvents: %v", err)
	}
	folded := Fold(events)
	if gotMeta.Status != folded.Status {
		t.Errorf("meta.json status %q disagrees with folded events status %q", gotMeta.Status, folded.Status)
	}
}

// TestSave_StampsPlanHTMLMetaTags verifies the stored plan.html's <head>
// carries the 3 self-identifying meta tags, matching the minted pln_, the
// clean slug (directory name minus the YYYY-MM-DD- prefix), and the plan's
// topic.
func TestSave_StampsPlanHTMLMetaTags(t *testing.T) {
	ledger := t.TempDir()
	withLedger(t, ledger)

	html := []byte(`<!doctype html><html><head><title>x</title></head><body>hi</body></html>`)
	meta := Meta{Topic: "Html Meta Tags", CreatedAt: time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC), Provenance: sampleProvenance()}

	dir, err := Save("/g", Input{Raw: "# Html Meta Tags\n"}, sampleResult(), html, meta)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	events, err := LoadEvents(dir)
	if err != nil || len(events) != 1 {
		t.Fatalf("LoadEvents: err=%v n=%d", err, len(events))
	}
	planID := events[0].PlanID

	stored, err := os.ReadFile(filepath.Join(dir, "plan.html"))
	if err != nil {
		t.Fatalf("read plan.html: %v", err)
	}
	got := string(stored)

	for _, want := range []string{
		`<meta name="sageox:plan" content="` + planID + `">`,
		`<meta name="sageox:plan-slug" content="html-meta-tags">`,
		`<meta name="sageox:plan-title" content="Html Meta Tags">`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("stored plan.html missing %q\ngot: %s", want, got)
		}
	}
}

// TestSave_CorruptEventsLogDoesNotBlockSave verifies the hard guardrail: a
// corrupt/unreadable events.jsonl must never fail Save or lose the meta.json
// write. Save falls back to minting a fresh id and proceeds.
func TestSave_CorruptEventsLogDoesNotBlockSave(t *testing.T) {
	ledger := t.TempDir()
	withLedger(t, ledger)

	created := time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC)
	dir := filepath.Join(ledger, "data", "plans", "2026-07-01-corrupt-log")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte("not json at all\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	meta := Meta{Topic: "Corrupt log", Slug: "corrupt-log", CreatedAt: created}
	if _, err := Save("/g", Input{Raw: "# Corrupt log\n"}, sampleResult(), nil, meta); err != nil {
		t.Fatalf("Save must not fail on a corrupt events.jsonl: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "meta.json")); err != nil {
		t.Errorf("meta.json missing despite corrupt events.jsonl: %v", err)
	}
}

// TestSave_NoProvenanceOmitsEventProvenanceFields verifies a plan saved with
// no resolvePlanProvenance() result (meta.Provenance == nil — e.g. outside a
// primed/recording session) still writes a valid created event, just with
// empty provenance fields, rather than erroring or panicking.
func TestSave_NoProvenanceOmitsEventProvenanceFields(t *testing.T) {
	ledger := t.TempDir()
	withLedger(t, ledger)

	meta := Meta{Topic: "No provenance", CreatedAt: time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC)}
	dir, err := Save("/g", Input{Raw: "# No provenance\n"}, sampleResult(), nil, meta)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	events, err := LoadEvents(dir)
	if err != nil || len(events) != 1 {
		t.Fatalf("LoadEvents: err=%v n=%d", err, len(events))
	}
	ev := events[0]
	if ev.Kind != EventCreated || !planid.IsPlanID(ev.PlanID) {
		t.Errorf("expected a valid created event even with nil provenance, got %+v", ev)
	}
	if ev.SessionID != "" || ev.SessionName != "" || ev.AgentID != "" || ev.AgentEnv != "" || ev.Model != "" || ev.Author != "" {
		t.Errorf("expected empty provenance fields with nil meta.Provenance, got %+v", ev)
	}
}
