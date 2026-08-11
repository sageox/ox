package plan

// store_save_kind_test.go pins Save's returned EventKind — the value that
// replaced a caller re-reading events.jsonl to learn what Save had just written.
//
// Failure prevented: the caller reports a lifecycle event to the server that
// disagrees with the save it just performed. Re-deriving by reading the log back
// is not equivalent to being told, because the event append is best-effort: when
// it fails the log's last entry is a STALE event from an earlier lifecycle verb
// (`approved`, `abandoned` — kinds a save never produces), and the read happens
// after Save's flock is released, so a concurrent AppendPlanEvent can land in
// between.

import (
	"context"
	"testing"
	"time"
)

// TestSave_ReturnsCreatedThenRevised is the baseline contract: created on a
// plan's first save, revised on every later one.
func TestSave_ReturnsCreatedThenRevised(t *testing.T) {
	withLedger(t, t.TempDir())

	meta := Meta{Topic: "Kind round trip", CreatedAt: time.Date(2026, 8, 10, 9, 0, 0, 0, time.UTC)}

	dir, kind, err := Save("/g", Input{Raw: "# Kind round trip\n"}, sampleResult(), nil, meta)
	if err != nil {
		t.Fatalf("first Save: %v", err)
	}
	if kind != EventCreated {
		t.Errorf("first save returned %q, want %q", kind, EventCreated)
	}

	_, kind2, err := Save("/g", Input{Raw: "# Kind round trip\n\nv2"}, sampleResult(), nil, meta)
	if err != nil {
		t.Fatalf("second Save: %v", err)
	}
	if kind2 != EventRevised {
		t.Errorf("second save returned %q, want %q", kind2, EventRevised)
	}

	// The returned kind must also match what actually landed in the log on the
	// happy path — being told and reading back only diverge under failure.
	events, err := LoadEvents(dir)
	if err != nil {
		t.Fatalf("LoadEvents: %v", err)
	}
	if got := events[len(events)-1].Kind; got != kind2 {
		t.Errorf("log's last kind %q != returned kind %q", got, kind2)
	}
}

// TestSave_ReturnedKindDescribesThisSaveNotTheLog is the case the old
// read-back could not get right. A lifecycle verb appends `approved`, then the
// plan is saved again: the log's last entry is now `approved`, but this save
// performed a revision and must say so.
//
// Red-first: re-derive the kind with `events[len(events)-1].Kind` after Save and
// this returns "approved" — reporting a plan as freshly approved to the server's
// activity index every time someone edits it.
func TestSave_ReturnedKindDescribesThisSaveNotTheLog(t *testing.T) {
	withLedger(t, t.TempDir())

	meta := Meta{Topic: "Approved then revised", CreatedAt: time.Date(2026, 8, 10, 9, 0, 0, 0, time.UTC)}

	dir, _, err := Save("/g", Input{Raw: "# Approved then revised\n"}, sampleResult(), nil, meta)
	if err != nil {
		t.Fatalf("first Save: %v", err)
	}
	events, err := LoadEvents(dir)
	if err != nil || len(events) == 0 {
		t.Fatalf("LoadEvents: err=%v n=%d", err, len(events))
	}
	if err := AppendEvent(context.Background(), dir, Event{
		PlanID: events[0].PlanID, Kind: EventApproved, Status: PlanStatusApproved,
	}); err != nil {
		t.Fatalf("AppendEvent approved: %v", err)
	}

	_, kind, err := Save("/g", Input{Raw: "# Approved then revised\n\nv2"}, sampleResult(), nil, meta)
	if err != nil {
		t.Fatalf("Save after approve: %v", err)
	}
	if kind != EventRevised {
		t.Errorf("Save returned %q, want %q — the kind must describe THIS save, not the log's last line", kind, EventRevised)
	}
}
