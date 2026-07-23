package plan

// lifecycle_test.go covers AppendPlanEvent (see lifecycle.go): the single
// engine behind the ox plan lifecycle verbs (approve/work/realize/abandon/
// supersede) and the review server's /approve reroute.

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

// seedCreated writes a single `created` event to a fresh plan dir — the
// minimum on-disk state every lifecycle verb requires (AppendPlanEvent
// errors on an empty log) — and returns the dir + minted PlanID.
func seedCreated(t *testing.T) (dir, planID string) {
	t.Helper()
	dir = t.TempDir()
	planID = "pln_test0000000000000001"
	ev := Event{PlanID: planID, Kind: EventCreated, Status: PlanStatusDraft, Timestamp: time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC)}
	if err := AppendEvent(context.Background(), dir, ev); err != nil {
		t.Fatalf("seed created event: %v", err)
	}
	return dir, planID
}

// writeTestMeta writes a minimal meta.json directly (bypassing Save, which
// requires a configured ledger) so dual-write tests can seed a starting
// Status without pulling in the full ledger-resolution machinery.
func writeTestMeta(t *testing.T, dir string, m Meta) {
	t.Helper()
	if err := MutatePlanMeta(context.Background(), dir, func(*Meta) (*Meta, error) {
		return &m, nil
	}); err != nil {
		t.Fatalf("seed meta.json: %v", err)
	}
}

func TestAppendPlanEvent_RejectsNonLifecycleKind(t *testing.T) {
	t.Parallel()
	dir, _ := seedCreated(t)
	for _, kind := range []EventKind{EventCreated, EventRevised, EventVisibilitySet} {
		t.Run(string(kind), func(t *testing.T) {
			changed, err := AppendPlanEvent(context.Background(), dir, kind, PlanEventFields{})
			if err == nil {
				t.Fatalf("want error for kind %q, got changed=%v", kind, changed)
			}
			if changed {
				t.Error("changed must be false on error")
			}
		})
	}
	events, err := LoadEvents(dir)
	if err != nil {
		t.Fatalf("LoadEvents: %v", err)
	}
	if len(events) != 1 {
		t.Errorf("rejected kinds must not append: got %d events, want 1 (the seed)", len(events))
	}
}

func TestAppendPlanEvent_RequiresExistingPlan(t *testing.T) {
	t.Parallel()
	dir := t.TempDir() // no events.jsonl at all
	changed, err := AppendPlanEvent(context.Background(), dir, EventApproved, PlanEventFields{})
	if err == nil {
		t.Fatal("want error for a plan with no recorded history")
	}
	if changed {
		t.Error("changed must be false on error")
	}
}

// TestAppendPlanEvent_ApproveAppendsEventAndDualWritesMeta verifies a fresh
// approve appends exactly one approved event carrying the stable PlanID,
// folds to Status=approved, and ALSO updates the pre-existing meta.json
// snapshot (dual-write) — the /approve endpoint and `ox plan approve`
// contract this task's guardrail requires meta.json to keep honoring.
func TestAppendPlanEvent_ApproveAppendsEventAndDualWritesMeta(t *testing.T) {
	t.Parallel()
	dir, planID := seedCreated(t)
	writeTestMeta(t, dir, Meta{Slug: "s", Status: PlanStatusDraft})

	changed, err := AppendPlanEvent(context.Background(), dir, EventApproved, PlanEventFields{Author: "Person A"})
	if err != nil {
		t.Fatalf("AppendPlanEvent: %v", err)
	}
	if !changed {
		t.Fatal("want changed=true on first approve")
	}

	events, err := LoadEvents(dir)
	if err != nil {
		t.Fatalf("LoadEvents: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("want 2 events (created+approved), got %d: %+v", len(events), events)
	}
	if events[1].Kind != EventApproved || events[1].Status != PlanStatusApproved {
		t.Errorf("second event = %+v, want an approved status event", events[1])
	}
	if events[1].PlanID != planID {
		t.Errorf("approved event PlanID = %q, want %q (stable across the log)", events[1].PlanID, planID)
	}
	if folded := Fold(events); folded.Status != PlanStatusApproved {
		t.Errorf("folded status = %q, want approved", folded.Status)
	}

	meta, err := LoadMeta(dir)
	if err != nil {
		t.Fatalf("LoadMeta: %v", err)
	}
	if meta.Status != PlanStatusApproved {
		t.Errorf("meta.json status = %q, want approved (dual-write)", meta.Status)
	}
}

// TestAppendPlanEvent_ApproveIdempotent is the explicit idempotency
// requirement: re-approving an already-approved plan must report
// changed:false and must NOT append a duplicate event.
func TestAppendPlanEvent_ApproveIdempotent(t *testing.T) {
	t.Parallel()
	dir, _ := seedCreated(t)

	if changed, err := AppendPlanEvent(context.Background(), dir, EventApproved, PlanEventFields{}); err != nil || !changed {
		t.Fatalf("first approve: changed=%v err=%v, want true/nil", changed, err)
	}
	changed, err := AppendPlanEvent(context.Background(), dir, EventApproved, PlanEventFields{})
	if err != nil {
		t.Fatalf("second approve: %v", err)
	}
	if changed {
		t.Error("re-approving an already-approved plan must report changed=false")
	}

	events, err := LoadEvents(dir)
	if err != nil {
		t.Fatalf("LoadEvents: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("idempotent re-approve must not duplicate the event: got %d events, want 2", len(events))
	}
}

// TestAppendPlanEvent_WorkDoesNotChangeStatus verifies work is edge-only.
func TestAppendPlanEvent_WorkDoesNotChangeStatus(t *testing.T) {
	t.Parallel()
	dir, _ := seedCreated(t)

	changed, err := AppendPlanEvent(context.Background(), dir, EventWorked, PlanEventFields{SessionName: "sess-a", Author: "Person A"})
	if err != nil {
		t.Fatalf("AppendPlanEvent: %v", err)
	}
	if !changed {
		t.Fatal("want changed=true for a new session's first work event")
	}

	events, err := LoadEvents(dir)
	if err != nil {
		t.Fatalf("LoadEvents: %v", err)
	}
	folded := Fold(events)
	if folded.Status != PlanStatusDraft {
		t.Errorf("work must not change Status: got %q, want draft", folded.Status)
	}
	if len(folded.Sessions) != 1 || folded.Sessions[0].SessionName != "sess-a" {
		t.Fatalf("want 1 session ref for sess-a, got %+v", folded.Sessions)
	}
	if !containsKind(folded.Sessions[0].Kinds, EventWorked) {
		t.Errorf("session Kinds = %v, want to contain worked", folded.Sessions[0].Kinds)
	}
}

// TestAppendPlanEvent_RealizeSetsStatusAndSessionEdgeInOneAppend verifies
// realize is both a status verb and a work verb in a SINGLE appended event
// — no special-case branch.
func TestAppendPlanEvent_RealizeSetsStatusAndSessionEdgeInOneAppend(t *testing.T) {
	t.Parallel()
	dir, _ := seedCreated(t)

	changed, err := AppendPlanEvent(context.Background(), dir, EventRealized, PlanEventFields{
		SessionName: "sess-a",
		Produced:    "https://github.com/sageox/ox/pull/1",
	})
	if err != nil {
		t.Fatalf("AppendPlanEvent: %v", err)
	}
	if !changed {
		t.Fatal("want changed=true")
	}

	events, err := LoadEvents(dir)
	if err != nil {
		t.Fatalf("LoadEvents: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("realize must be ONE appended event (created+realized), got %d: %+v", len(events), events)
	}
	if events[1].Produced != "https://github.com/sageox/ox/pull/1" {
		t.Errorf("Produced = %q, want the passed ref", events[1].Produced)
	}

	folded := Fold(events)
	if folded.Status != PlanStatusRealized {
		t.Errorf("Status = %q, want realized", folded.Status)
	}
	if len(folded.Sessions) != 1 || !containsKind(folded.Sessions[0].Kinds, EventRealized) {
		t.Errorf("want sess-a's Kinds to contain realized from the SAME append, got %+v", folded.Sessions)
	}
}

// TestAppendPlanEvent_AbandonRecordsReasonAndReReasonIsNotANoOp verifies
// --reason is persisted, a byte-identical re-abandon no-ops, but a DIFFERENT
// reason still counts as a semantic change even though Status is unchanged
// — Reason is one of the fields the idempotency check must diff on.
func TestAppendPlanEvent_AbandonRecordsReasonAndReReasonIsNotANoOp(t *testing.T) {
	t.Parallel()
	dir, _ := seedCreated(t)

	if changed, err := AppendPlanEvent(context.Background(), dir, EventAbandoned, PlanEventFields{Reason: "no longer needed"}); err != nil || !changed {
		t.Fatalf("first abandon: changed=%v err=%v, want true/nil", changed, err)
	}

	// Same reason again: Status unchanged AND Reason unchanged → no-op.
	if changed, err := AppendPlanEvent(context.Background(), dir, EventAbandoned, PlanEventFields{Reason: "no longer needed"}); err != nil || changed {
		t.Fatalf("re-abandon with the SAME reason: changed=%v err=%v, want false/nil", changed, err)
	}

	// Different reason: Status stays abandoned, but the reason text changed.
	changed, err := AppendPlanEvent(context.Background(), dir, EventAbandoned, PlanEventFields{Reason: "a better approach emerged"})
	if err != nil {
		t.Fatalf("re-abandon with a different reason: %v", err)
	}
	if !changed {
		t.Error("a changed Reason must count as a semantic change even when Status is unchanged")
	}

	events, err := LoadEvents(dir)
	if err != nil {
		t.Fatalf("LoadEvents: %v", err)
	}
	if len(events) != 3 { // created, abandoned(reason1); the dup was skipped; abandoned(reason2)
		t.Fatalf("want 3 events (dup skipped, distinct reason kept), got %d: %+v", len(events), events)
	}
	if got := events[2].Reason; got != "a better approach emerged" {
		t.Errorf("last event Reason = %q, want the updated text", got)
	}
}

func TestAppendPlanEvent_SupersedeRequiresSupersededBy(t *testing.T) {
	t.Parallel()
	dir, _ := seedCreated(t)

	changed, err := AppendPlanEvent(context.Background(), dir, EventSuperseded, PlanEventFields{})
	if err == nil {
		t.Fatal("want error when SupersededBy is empty")
	}
	if changed {
		t.Error("changed must be false on error")
	}

	events, err := LoadEvents(dir)
	if err != nil {
		t.Fatalf("LoadEvents: %v", err)
	}
	if len(events) != 1 {
		t.Errorf("a rejected supersede must not append: got %d events, want 1 (the seed)", len(events))
	}
}

func TestAppendPlanEvent_SupersedeRecordsSuccessor(t *testing.T) {
	t.Parallel()
	dir, _ := seedCreated(t)

	changed, err := AppendPlanEvent(context.Background(), dir, EventSuperseded, PlanEventFields{SupersededBy: "2026-07-01-successor-plan"})
	if err != nil {
		t.Fatalf("AppendPlanEvent: %v", err)
	}
	if !changed {
		t.Fatal("want changed=true")
	}

	events, err := LoadEvents(dir)
	if err != nil {
		t.Fatalf("LoadEvents: %v", err)
	}
	if len(events) != 2 || events[1].SupersededBy != "2026-07-01-successor-plan" {
		t.Fatalf("want the successor slug recorded on the superseded event, got %+v", events)
	}
	if folded := Fold(events); folded.Status != PlanStatusSuperseded {
		t.Errorf("Status = %q, want superseded", folded.Status)
	}
}

// TestAppendPlanEvent_MissingMetaJSONIsBestEffort verifies the dual-write is
// "only if exists" (mirrors SetStatus's own no-op-on-missing-meta contract)
// and, crucially, that a missing meta.json never fails the primary
// events.jsonl append.
func TestAppendPlanEvent_MissingMetaJSONIsBestEffort(t *testing.T) {
	t.Parallel()
	dir, _ := seedCreated(t) // no meta.json written at all

	changed, err := AppendPlanEvent(context.Background(), dir, EventApproved, PlanEventFields{})
	if err != nil {
		t.Fatalf("a missing meta.json must not fail the event-log append: %v", err)
	}
	if !changed {
		t.Fatal("want changed=true")
	}
	if _, err := LoadMeta(dir); err == nil {
		t.Error("meta.json should still not exist — the dual-write is 'only if exists'")
	}
}

// TestAppendPlanEvent_AnonymousEdgeEventsAlwaysRecorded documents a known
// limitation: an unattributed (no session) work/realize event carries no key
// Fold's Sessions aggregation can dedupe against, so — rather than silently
// dropping the only evidence anything happened — it is always recorded.
func TestAppendPlanEvent_AnonymousEdgeEventsAlwaysRecorded(t *testing.T) {
	t.Parallel()
	dir, _ := seedCreated(t)

	for i := 0; i < 2; i++ {
		changed, err := AppendPlanEvent(context.Background(), dir, EventWorked, PlanEventFields{})
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
		if !changed {
			t.Errorf("call %d: an unattributed work event can't be deduped against history, want changed=true", i)
		}
	}
	events, err := LoadEvents(dir)
	if err != nil {
		t.Fatalf("LoadEvents: %v", err)
	}
	if len(events) != 3 { // created + 2 anonymous worked
		t.Fatalf("want 3 events, got %d: %+v", len(events), events)
	}
}

func TestAppendPlanEvent_DistinctSessionsBothRecorded(t *testing.T) {
	t.Parallel()
	dir, _ := seedCreated(t)

	if changed, err := AppendPlanEvent(context.Background(), dir, EventWorked, PlanEventFields{SessionName: "sess-a"}); err != nil || !changed {
		t.Fatalf("sess-a: changed=%v err=%v", changed, err)
	}
	if changed, err := AppendPlanEvent(context.Background(), dir, EventWorked, PlanEventFields{SessionName: "sess-b"}); err != nil || !changed {
		t.Fatalf("sess-b: changed=%v err=%v", changed, err)
	}

	events, err := LoadEvents(dir)
	if err != nil {
		t.Fatalf("LoadEvents: %v", err)
	}
	folded := Fold(events)
	if len(folded.Sessions) != 2 {
		t.Fatalf("want 2 distinct sessions, got %+v", folded.Sessions)
	}
}

// TestAppendPlanEvent_ConcurrentDistinctSessionsNoLostWrites is the
// concurrency proof behind the "SAME flock AppendEvent uses" design: N
// goroutines each recording a DIFFERENT session's work event must all land
// — the read-decide-write critical section (LoadEvents → candidateChangesState
// → append) has to be atomic under fileutil.WithFileLock, or two goroutines
// racing the read would both decide "new session, must append" against the
// same stale snapshot and one's write would clobber the other's. Run with
// -race to also catch any accidental unlocked access.
func TestAppendPlanEvent_ConcurrentDistinctSessionsNoLostWrites(t *testing.T) {
	dir, _ := seedCreated(t)

	const n = 20
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			changed, err := AppendPlanEvent(context.Background(), dir, EventWorked, PlanEventFields{
				SessionName: fmt.Sprintf("sess-%02d", i),
			})
			if err != nil {
				errs <- err
				return
			}
			if !changed {
				errs <- fmt.Errorf("session %d: want changed=true for a brand-new session", i)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}

	events, err := LoadEvents(dir)
	if err != nil {
		t.Fatalf("LoadEvents: %v", err)
	}
	if len(events) != n+1 { // +1 for the seed `created` event
		t.Fatalf("want %d events (no lost writes), got %d", n+1, len(events))
	}
	folded := Fold(events)
	if len(folded.Sessions) != n {
		t.Fatalf("want %d distinct sessions folded, got %d: %+v", n, len(folded.Sessions), folded.Sessions)
	}
}
