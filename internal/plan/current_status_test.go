package plan

// current_status_test.go covers CurrentStatus (see store.go): the
// fold-events-or-fall-back-to-meta read helper behind `ox plan list` and
// `ox plan view`'s status display, plus its wiring into PlanInfo.Status via
// List/Load (the actual data both commands consume).

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestCurrentStatus_FoldedWinsOverStaleMeta proves fold wins: a plan whose
// events.jsonl has advanced to realized, but whose meta.json dual-write
// mirror is still draft (a dual-write hiccup, or simply not yet caught up),
// must report the folded (realized) status — never the stale meta.json one.
func TestCurrentStatus_FoldedWinsOverStaleMeta(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ctx := context.Background()

	events := []Event{
		{PlanID: "pln_stale0000000000000001", Kind: EventCreated, Status: PlanStatusDraft, Timestamp: time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC)},
		{PlanID: "pln_stale0000000000000001", Kind: EventRealized, Status: PlanStatusRealized, Timestamp: time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)},
	}
	for _, ev := range events {
		if err := AppendEvent(ctx, dir, ev); err != nil {
			t.Fatalf("seed event: %v", err)
		}
	}
	// meta.json deliberately diverged: still draft, simulating a dual-write
	// that hasn't (or never will) catch up.
	writeTestMeta(t, dir, Meta{Slug: "stale-meta", Status: PlanStatusDraft})

	got := CurrentStatus(dir)
	if got != PlanStatusRealized {
		t.Errorf("CurrentStatus = %q, want realized (folded must win over stale meta.json draft)", got)
	}
}

// TestCurrentStatus_LegacyFallsBackToMetaStatus verifies a plan saved before
// the event log existed (meta.json only, no events.jsonl) still reports its
// meta.json status — the additive fallback this task requires.
func TestCurrentStatus_LegacyFallsBackToMetaStatus(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeTestMeta(t, dir, Meta{Slug: "legacy-plan", Status: PlanStatusApproved})

	got := CurrentStatus(dir)
	if got != PlanStatusApproved {
		t.Errorf("CurrentStatus = %q, want approved (from meta.json fallback)", got)
	}
}

// TestCurrentStatus_NoEventsNoMetaReturnsEmpty verifies an unreadable/empty
// plan dir degrades to "" rather than erroring or panicking — matching how
// callers have always treated an unset meta.Status.
func TestCurrentStatus_NoEventsNoMetaReturnsEmpty(t *testing.T) {
	t.Parallel()
	dir := t.TempDir() // neither events.jsonl nor meta.json
	if got := CurrentStatus(dir); got != "" {
		t.Errorf("CurrentStatus = %q, want empty", got)
	}
}

// TestCurrentStatus_LegacyMetaWithEmptyStatusReturnsEmpty verifies a legacy
// meta.json with no Status field set at all (the pre-lifecycle-feature norm)
// falls back to "", not some invented default.
func TestCurrentStatus_LegacyMetaWithEmptyStatusReturnsEmpty(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeTestMeta(t, dir, Meta{Slug: "no-status"})
	if got := CurrentStatus(dir); got != "" {
		t.Errorf("CurrentStatus = %q, want empty", got)
	}
}

// --- PlanInfo.Status wiring via List/Load (the actual data ox plan
// list/view consume) ---

// TestPlanInfoStatus_FoldedWinsOverStaleMeta exercises the real production
// path: Save() a plan, directly append a realized event via the low-level
// AppendEvent (bypassing AppendPlanEvent's meta.json dual-write, simulating
// a dual-write miss), and verify both List and Load report the folded
// status on PlanInfo.Status — the field `ox plan list`/`view` render.
func TestPlanInfoStatus_FoldedWinsOverStaleMeta(t *testing.T) {
	ledger := t.TempDir()
	withLedger(t, ledger)

	meta := Meta{Topic: "Status divergence", CreatedAt: time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC)}
	dir, err := Save("/g", Input{Raw: "# Status divergence\n"}, sampleResult(), nil, meta)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	// meta.json is draft after Save (the default). Append a realized event
	// directly (not via AppendPlanEvent) so meta.json is left stale.
	events, err := LoadEvents(dir)
	if err != nil || len(events) != 1 {
		t.Fatalf("LoadEvents after Save: err=%v n=%d", err, len(events))
	}
	planID := events[0].PlanID
	if err := AppendEvent(context.Background(), dir, Event{PlanID: planID, Kind: EventRealized, Status: PlanStatusRealized}); err != nil {
		t.Fatalf("append realized: %v", err)
	}

	gotMeta, err := ReadPlanMeta("/g", "status-divergence")
	if err != nil {
		t.Fatalf("ReadPlanMeta: %v", err)
	}
	if gotMeta.Status != PlanStatusDraft {
		t.Fatalf("precondition failed: meta.json Status = %q, want draft (still stale)", gotMeta.Status)
	}

	plans, err := List("/g")
	if err != nil || len(plans) != 1 {
		t.Fatalf("List: err=%v n=%d", err, len(plans))
	}
	if plans[0].Status != PlanStatusRealized {
		t.Errorf("List()[0].Status = %q, want realized (folded wins over stale meta.json draft)", plans[0].Status)
	}

	_, _, info, err := Load("/g", "status-divergence")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if info.Status != PlanStatusRealized {
		t.Errorf("Load info.Status = %q, want realized (folded wins over stale meta.json draft)", info.Status)
	}
}

// TestPlanInfoStatus_LegacyPlanFallsBackToMeta verifies a legacy plan dir
// (meta.json only, written directly — no events.jsonl, no Save()) still
// reports its meta.json status via List/Load's PlanInfo.Status.
func TestPlanInfoStatus_LegacyPlanFallsBackToMeta(t *testing.T) {
	ledger := t.TempDir()
	withLedger(t, ledger)

	dir := filepath.Join(ledger, "data", "plans", "2026-01-01-legacy-view-plan")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := `{"topic":"Legacy view plan","slug":"legacy-view-plan","created_at":"2026-01-01T00:00:00Z","status":"approved"}`
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	// Load also reads plan.md — a real legacy plan dir always has one; only
	// meta.json/events.jsonl are this test's concern.
	if err := os.WriteFile(filepath.Join(dir, "plan.md"), []byte("# Legacy view plan\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	plans, err := List("/g")
	if err != nil || len(plans) != 1 {
		t.Fatalf("List: err=%v n=%d", err, len(plans))
	}
	if plans[0].Status != PlanStatusApproved {
		t.Errorf("List()[0].Status = %q, want approved (meta.json fallback, no events.jsonl)", plans[0].Status)
	}

	_, _, info, err := Load("/g", "legacy-view-plan")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if info.Status != PlanStatusApproved {
		t.Errorf("Load info.Status = %q, want approved (meta.json fallback)", info.Status)
	}
}
