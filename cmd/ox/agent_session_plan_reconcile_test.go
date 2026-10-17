package main

// agent_session_plan_reconcile_test.go covers reconcileProducedPlansAtStop
// (see agent_session.go): session stop records the session in each produced
// plan's meta.json and never edits its events.jsonl.

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/plan"
)

// TestReconcileProducedPlansAtStop_LeavesTheEventLogAlone pins the rule that
// keeps union-merged event logs free of duplicate lines: an edit to a line
// that sits next to another clone's append comes out of the merge twice.
func TestReconcileProducedPlansAtStop_LeavesTheEventLogAlone(t *testing.T) {
	root := newPlanStatusTestRepo(t)

	meta := plan.Meta{Topic: "Stop leaves events", CreatedAt: time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC)}
	dir, _, err := plan.Save(root, plan.Input{Raw: "# Stop leaves events\n"}, plan.Result{}, nil, meta)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	events, err := plan.LoadEvents(dir)
	if err != nil || len(events) != 1 {
		t.Fatalf("LoadEvents after Save: err=%v n=%d", err, len(events))
	}
	// the line a recording started by an older ox writes: a name, no id
	if err := plan.AppendEvent(context.Background(), dir, plan.Event{
		PlanID: events[0].PlanID, Kind: plan.EventWorked, SessionName: "2026-07-01-person-a-Oxab12",
	}); err != nil {
		t.Fatalf("seed worked event: %v", err)
	}
	eventsPath := filepath.Join(dir, "events.jsonl")
	before, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatal(err)
	}

	reconcileProducedPlansAtStop(root, []string{"stop-leaves-events"}, "ses_stop01")

	after, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Errorf("session stop edited events.jsonl:\nbefore %s\nafter  %s", before, after)
	}
	gotMeta, err := plan.ReadPlanMeta(root, "stop-leaves-events")
	if err != nil {
		t.Fatalf("ReadPlanMeta: %v", err)
	}
	if gotMeta.Provenance == nil || gotMeta.Provenance.SessionID != "ses_stop01" || gotMeta.Provenance.SessionOutcome != plan.SessionOutcomeStopped {
		t.Errorf("meta.json Provenance = %+v, want SessionID ses_stop01 and outcome stopped", gotMeta.Provenance)
	}
}

// TestReconcileProducedPlansAtStop_ContinuesPastBrokenSlug verifies the
// error-path behavior of the per-slug loop: a slug that can't be resolved
// (plan.Load fails, e.g. it was never saved or its dir is gone) must not
// stop reconciliation for the other slugs in the same stop call — each
// failure is independently debug-logged and skipped via `continue`.
func TestReconcileProducedPlansAtStop_ContinuesPastBrokenSlug(t *testing.T) {
	root := newPlanStatusTestRepo(t)

	meta := plan.Meta{Topic: "Valid Alongside Broken", CreatedAt: time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC)}
	if _, _, err := plan.Save(root, plan.Input{Raw: "# Valid Alongside Broken\n"}, plan.Result{}, nil, meta); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// "does-not-exist" precedes the valid slug so a bug that aborts the loop
	// on the first failure (instead of continuing) would be caught.
	slugs := []string{"does-not-exist", "valid-alongside-broken"}
	reconcileProducedPlansAtStop(root, slugs, "ses_mixedslugs01")

	gotMeta, err := plan.ReadPlanMeta(root, "valid-alongside-broken")
	if err != nil {
		t.Fatalf("ReadPlanMeta: %v", err)
	}
	if gotMeta.Provenance == nil || gotMeta.Provenance.SessionID != "ses_mixedslugs01" {
		t.Fatalf("valid slug must still be reconciled despite a broken slug earlier in the list: got %+v", gotMeta.Provenance)
	}
}
