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

// TestSave_ReturnedKindIsImmuneToLaterLogMutation pins the property that makes
// returning the kind safer than re-reading it: once Save hands the value back,
// nothing that happens to events.jsonl afterwards can change what this save
// reports. A lifecycle verb lands an `approved` event immediately after the
// save — the window the caller's read used to sit in, since it happened after
// Save released its flock — and the captured kind must still say `revised`.
//
// Red-first: move the kind derivation to AFTER this AppendEvent (i.e. re-read
// `events[len-1].Kind` where the caller used to) and the value becomes
// "approved" — reporting a plan as freshly approved every time someone edits it.
//
// HONEST LIMIT, stated rather than implied: the other divergence — Save's event
// append failing, leaving the log's last entry stale — is NOT covered by a test.
// appendEventLocked goes through fileutil.AtomicWriteBytes (temp file + rename),
// so an append failure cannot be induced from outside without adding a fault
// seam to production code for one advisory notification. That case is closed
// STRUCTURALLY instead: the caller no longer performs a second read, so there is
// no second value that can disagree. A bug with no way to be constructed needs
// no test — but the reason has to be written down, not assumed.
func TestSave_ReturnedKindIsImmuneToLaterLogMutation(t *testing.T) {
	withLedger(t, t.TempDir())

	meta := Meta{Topic: "Revised then approved", CreatedAt: time.Date(2026, 8, 10, 9, 0, 0, 0, time.UTC)}

	dir, _, err := Save("/g", Input{Raw: "# Revised then approved\n"}, sampleResult(), nil, meta)
	if err != nil {
		t.Fatalf("first Save: %v", err)
	}
	_, kind, err := Save("/g", Input{Raw: "# Revised then approved\n\nv2"}, sampleResult(), nil, meta)
	if err != nil {
		t.Fatalf("second Save: %v", err)
	}

	events, err := LoadEvents(dir)
	if err != nil || len(events) == 0 {
		t.Fatalf("LoadEvents: err=%v n=%d", err, len(events))
	}
	// The concurrent lifecycle verb lands in the caller's old read window.
	if err := AppendEvent(context.Background(), dir, Event{
		PlanID: events[0].PlanID, Kind: EventApproved, Status: PlanStatusApproved,
	}); err != nil {
		t.Fatalf("AppendEvent approved: %v", err)
	}

	if kind != EventRevised {
		t.Errorf("captured kind = %q, want %q", kind, EventRevised)
	}
	// Fixture self-check: if the log's last entry still matched the returned
	// kind, this test would prove nothing about divergence at all.
	after, err := LoadEvents(dir)
	if err != nil {
		t.Fatalf("LoadEvents after append: %v", err)
	}
	if last := after[len(after)-1].Kind; last == kind {
		t.Fatalf("fixture did not create a divergence: log's last kind is still %q — the test cannot distinguish the two approaches", last)
	}
}
