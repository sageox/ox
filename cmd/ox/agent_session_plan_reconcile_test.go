package main

// agent_session_plan_reconcile_test.go covers reconcileProducedPlansAtStop's
// event-log backfill wiring (see agent_session.go): the CLI-level proof that
// session-stop calls plan.BackfillSessionID (in addition to the pre-existing
// plan.ReconcileSessionOutcome meta.json backfill) so a produced plan's own
// events.jsonl lines get the canonical ses_ id too. BackfillSessionID's own
// semantics (idempotency, session-name scoping) are covered directly in
// internal/plan/events_backfill_test.go; this file only proves the stop-path
// plumbing calls it with the right session name/id.

import (
	"context"
	"testing"
	"time"

	"github.com/sageox/ox/internal/plan"
)

func TestReconcileProducedPlansAtStop_BackfillsEventSessionID(t *testing.T) {
	root := newPlanStatusTestRepo(t)

	meta := plan.Meta{Topic: "Stop-time backfill", CreatedAt: time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC)}
	dir, err := plan.Save(root, plan.Input{Raw: "# Stop-time backfill\n"}, plan.Result{}, nil, meta)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	events, err := plan.LoadEvents(dir)
	if err != nil || len(events) != 1 {
		t.Fatalf("LoadEvents after Save: err=%v n=%d", err, len(events))
	}
	planID := events[0].PlanID

	// Seed a `worked` event carrying only the pre-canonical session_name —
	// the shape a live-recording save produces before the session has
	// stopped and minted its ses_ id.
	if err := plan.AppendEvent(context.Background(), dir, plan.Event{
		PlanID: planID, Kind: plan.EventWorked, SessionName: "2026-07-01-person-a-Oxab12",
	}); err != nil {
		t.Fatalf("seed worked event: %v", err)
	}

	reconcileProducedPlansAtStop(root, []string{"stop-time-backfill"}, "2026-07-01-person-a-Oxab12", "ses_stopbackfill01")

	got, err := plan.LoadEvents(dir)
	if err != nil {
		t.Fatalf("LoadEvents after reconcile: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 events (created+worked), got %d: %+v", len(got), got)
	}
	if got[1].Kind != plan.EventWorked || got[1].SessionID != "ses_stopbackfill01" {
		t.Errorf("worked event = %+v, want SessionID backfilled to ses_stopbackfill01", got[1])
	}
	// the created event carries no session_name, so it must NOT be touched.
	if got[0].SessionID != "" {
		t.Errorf("created event SessionID = %q, want untouched (no session_name to match)", got[0].SessionID)
	}

	// meta.json's Provenance.SessionID also gets backfilled by the
	// pre-existing ReconcileSessionOutcome call — unchanged by this task,
	// re-asserted here to pin the two backfills running side by side.
	gotMeta, err := plan.ReadPlanMeta(root, "stop-time-backfill")
	if err != nil {
		t.Fatalf("ReadPlanMeta: %v", err)
	}
	if gotMeta.Provenance == nil || gotMeta.Provenance.SessionID != "ses_stopbackfill01" {
		t.Errorf("meta.json Provenance = %+v, want SessionID backfilled", gotMeta.Provenance)
	}
}

// TestReconcileProducedPlansAtStop_SecondCallIsNoOp verifies re-running stop
// reconciliation (e.g. a retried stop) does not duplicate events or error.
func TestReconcileProducedPlansAtStop_SecondCallIsNoOp(t *testing.T) {
	root := newPlanStatusTestRepo(t)

	meta := plan.Meta{Topic: "Idempotent stop backfill", CreatedAt: time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC)}
	dir, err := plan.Save(root, plan.Input{Raw: "# Idempotent stop backfill\n"}, plan.Result{}, nil, meta)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	events, err := plan.LoadEvents(dir)
	if err != nil || len(events) != 1 {
		t.Fatalf("LoadEvents after Save: err=%v n=%d", err, len(events))
	}
	if err := plan.AppendEvent(context.Background(), dir, plan.Event{
		PlanID: events[0].PlanID, Kind: plan.EventWorked, SessionName: "2026-07-01-person-a-Oxab12",
	}); err != nil {
		t.Fatalf("seed worked event: %v", err)
	}

	reconcileProducedPlansAtStop(root, []string{"idempotent-stop-backfill"}, "2026-07-01-person-a-Oxab12", "ses_idem01")
	reconcileProducedPlansAtStop(root, []string{"idempotent-stop-backfill"}, "2026-07-01-person-a-Oxab12", "ses_idem01")

	got, err := plan.LoadEvents(dir)
	if err != nil {
		t.Fatalf("LoadEvents: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("second stop must not duplicate events: got %d, want 2: %+v", len(got), got)
	}
}
