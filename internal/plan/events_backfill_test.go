package plan

// events_backfill_test.go covers BackfillSessionID (see events.go): the
// stop-time session_id backfill for events.jsonl lines, mirroring
// ReconcileSessionOutcome's meta.json Provenance backfill (store.go) but
// applied to the append-only event log.

import (
	"context"
	"testing"
)

func TestBackfillSessionID_SetsEmptySessionIDsForMatchingSessionName(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ctx := context.Background()

	seed := []Event{
		{PlanID: "pln_x", Kind: EventCreated, SessionName: "sess-a"},
		{PlanID: "pln_x", Kind: EventWorked, SessionName: "sess-a"},
	}
	for _, ev := range seed {
		if err := AppendEvent(ctx, dir, ev); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	updated, err := BackfillSessionID(ctx, dir, "sess-a", "ses_abc123")
	if err != nil {
		t.Fatalf("BackfillSessionID: %v", err)
	}
	if updated != 2 {
		t.Fatalf("updated = %d, want 2", updated)
	}

	got, err := LoadEvents(dir)
	if err != nil {
		t.Fatalf("LoadEvents: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 events, got %d", len(got))
	}
	for i, ev := range got {
		if ev.SessionID != "ses_abc123" {
			t.Errorf("event %d SessionID = %q, want ses_abc123", i, ev.SessionID)
		}
		if ev.SessionName != "sess-a" {
			t.Errorf("event %d SessionName = %q, corrupted by backfill", i, ev.SessionName)
		}
	}
}

// TestBackfillSessionID_SecondCallIsNoOp is the idempotency requirement: a
// re-run after a successful backfill must update nothing.
func TestBackfillSessionID_SecondCallIsNoOp(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ctx := context.Background()
	if err := AppendEvent(ctx, dir, Event{PlanID: "pln_x", Kind: EventCreated, SessionName: "sess-a"}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if updated, err := BackfillSessionID(ctx, dir, "sess-a", "ses_abc123"); err != nil || updated != 1 {
		t.Fatalf("first backfill: updated=%d err=%v, want 1/nil", updated, err)
	}
	updated, err := BackfillSessionID(ctx, dir, "sess-a", "ses_abc123")
	if err != nil {
		t.Fatalf("second backfill: %v", err)
	}
	if updated != 0 {
		t.Errorf("second backfill updated = %d, want 0 (idempotent no-op)", updated)
	}

	// raw file must not have grown a duplicate line.
	events, err := LoadEvents(dir)
	if err != nil {
		t.Fatalf("LoadEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("want 1 event still, got %d: %+v", len(events), events)
	}
}

// TestBackfillSessionID_OtherSessionsUntouched verifies the SessionName match
// is exact — a different session's empty session_id must not be filled in by
// a backfill call for a different session.
func TestBackfillSessionID_OtherSessionsUntouched(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ctx := context.Background()
	if err := AppendEvent(ctx, dir, Event{PlanID: "pln_x", Kind: EventCreated, SessionName: "sess-a"}); err != nil {
		t.Fatalf("seed sess-a: %v", err)
	}
	if err := AppendEvent(ctx, dir, Event{PlanID: "pln_x", Kind: EventWorked, SessionName: "sess-b"}); err != nil {
		t.Fatalf("seed sess-b: %v", err)
	}

	updated, err := BackfillSessionID(ctx, dir, "sess-a", "ses_abc123")
	if err != nil {
		t.Fatalf("BackfillSessionID: %v", err)
	}
	if updated != 1 {
		t.Fatalf("updated = %d, want 1 (only sess-a's line)", updated)
	}

	got, err := LoadEvents(dir)
	if err != nil {
		t.Fatalf("LoadEvents: %v", err)
	}
	if got[0].SessionName != "sess-a" || got[0].SessionID != "ses_abc123" {
		t.Errorf("sess-a event = %+v, want SessionID backfilled", got[0])
	}
	if got[1].SessionName != "sess-b" || got[1].SessionID != "" {
		t.Errorf("sess-b event = %+v, want untouched (empty SessionID)", got[1])
	}
}

// TestBackfillSessionID_AlreadySetIsNotOverwritten verifies the guard is
// SessionID == "" specifically — a line already carrying a (possibly
// different) session_id is never clobbered.
func TestBackfillSessionID_AlreadySetIsNotOverwritten(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ctx := context.Background()
	if err := AppendEvent(ctx, dir, Event{PlanID: "pln_x", Kind: EventCreated, SessionName: "sess-a", SessionID: "ses_already"}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	updated, err := BackfillSessionID(ctx, dir, "sess-a", "ses_new")
	if err != nil {
		t.Fatalf("BackfillSessionID: %v", err)
	}
	if updated != 0 {
		t.Errorf("updated = %d, want 0 (must not overwrite an existing session_id)", updated)
	}
	got, err := LoadEvents(dir)
	if err != nil {
		t.Fatalf("LoadEvents: %v", err)
	}
	if got[0].SessionID != "ses_already" {
		t.Errorf("SessionID = %q, want ses_already preserved", got[0].SessionID)
	}
}

// TestBackfillSessionID_EmptyInputsAreNoOps verifies both guard clauses (no
// sessionName to key off of, no sessionID to write) short-circuit before
// touching the file.
func TestBackfillSessionID_EmptyInputsAreNoOps(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ctx := context.Background()
	if err := AppendEvent(ctx, dir, Event{PlanID: "pln_x", Kind: EventCreated, SessionName: "sess-a"}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if updated, err := BackfillSessionID(ctx, dir, "", "ses_x"); err != nil || updated != 0 {
		t.Errorf("empty sessionName: updated=%d err=%v, want 0/nil", updated, err)
	}
	if updated, err := BackfillSessionID(ctx, dir, "sess-a", ""); err != nil || updated != 0 {
		t.Errorf("empty sessionID: updated=%d err=%v, want 0/nil", updated, err)
	}

	got, err := LoadEvents(dir)
	if err != nil {
		t.Fatalf("LoadEvents: %v", err)
	}
	if got[0].SessionID != "" {
		t.Errorf("SessionID = %q, want still empty (no-op)", got[0].SessionID)
	}
}

// TestBackfillSessionID_MissingEventsFileIsNoOp verifies a plan with no
// events.jsonl at all (e.g. a legacy plan) is not an error.
func TestBackfillSessionID_MissingEventsFileIsNoOp(t *testing.T) {
	t.Parallel()
	dir := t.TempDir() // no events.jsonl
	updated, err := BackfillSessionID(context.Background(), dir, "sess-a", "ses_x")
	if err != nil {
		t.Fatalf("BackfillSessionID on missing file: %v", err)
	}
	if updated != 0 {
		t.Errorf("updated = %d, want 0", updated)
	}
}
