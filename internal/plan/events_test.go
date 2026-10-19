package plan

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"
)

// TestAppendEvent_LoadEvents_RoundTrip verifies a single appended event comes
// back byte-for-byte (modulo time.Time's == landmine, handled separately).
func TestAppendEvent_LoadEvents_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	t0 := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	want := Event{
		Timestamp:   t0,
		PlanID:      "pln_abc",
		Kind:        EventCreated,
		SessionName: "recording-2026-07-01",
		AgentID:     "Ox1a2b",
		AgentEnv:    "claude",
		Model:       "claude-sonnet-5",
		Author:      "ryan",
		Status:      PlanStatusDraft,
		Topic:       "event log foundation",
	}
	if err := AppendEvent(context.Background(), dir, want); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}

	got, err := LoadEvents(dir)
	if err != nil {
		t.Fatalf("LoadEvents: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 event, got %d: %+v", len(got), got)
	}

	// time.Time's == compares monotonic reading + Location pointer, not just
	// the instant, so it's the wrong tool for a JSON-round-tripped value —
	// compare the instant via Equal, then the rest of the struct via ==.
	if !got[0].Timestamp.Equal(want.Timestamp) {
		t.Errorf("Timestamp = %v, want %v", got[0].Timestamp, want.Timestamp)
	}
	gotRest, wantRest := got[0], want
	gotRest.Timestamp, wantRest.Timestamp = time.Time{}, time.Time{}
	if gotRest != wantRest {
		t.Errorf("event (minus ts) = %+v, want %+v", gotRest, wantRest)
	}
}

// TestAppendEvent_MultipleAppendsPreserveOrder verifies sequential appends
// land in call order — Fold's CreatedAt/UpdatedAt derivation depends on it.
func TestAppendEvent_MultipleAppendsPreserveOrder(t *testing.T) {
	dir := t.TempDir()
	t0 := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	kinds := []EventKind{EventCreated, EventRevised, EventApproved, EventWorked, EventRealized}
	for i, k := range kinds {
		ev := Event{Timestamp: t0.Add(time.Duration(i) * time.Minute), PlanID: "pln_order", Kind: k}
		if err := AppendEvent(context.Background(), dir, ev); err != nil {
			t.Fatalf("append %d (%s): %v", i, k, err)
		}
	}

	got, err := LoadEvents(dir)
	if err != nil {
		t.Fatalf("LoadEvents: %v", err)
	}
	if len(got) != len(kinds) {
		t.Fatalf("want %d events, got %d", len(kinds), len(got))
	}
	for i, k := range kinds {
		if got[i].Kind != k {
			t.Errorf("event %d kind = %q, want %q (append order not preserved)", i, got[i].Kind, k)
		}
	}
}

// TestAppendEvent_ConcurrentAppendsDontCorrupt spawns many goroutines
// appending to the same plan's events.jsonl concurrently — the scenario
// AppendEvent's flock exists for (CLI + daemon + a future reconciler all
// recording events for one plan). Asserts no write is lost, no line is
// corrupted (LoadEvents would fail to parse a torn line), and no line is
// duplicated.
func TestAppendEvent_ConcurrentAppendsDontCorrupt(t *testing.T) {
	dir := t.TempDir()
	const n = 50

	var wg sync.WaitGroup
	wg.Add(n)
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			ev := Event{
				Timestamp:   time.Now().UTC(),
				PlanID:      "pln_concurrent",
				Kind:        EventWorked,
				SessionName: fmt.Sprintf("session-%02d", i),
			}
			if err := AppendEvent(context.Background(), dir, ev); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("AppendEvent under concurrency: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, eventsFile))
	if err != nil {
		t.Fatalf("read raw events file: %v", err)
	}
	if lines := bytes.Count(raw, []byte("\n")); lines != n {
		t.Errorf("raw newline-terminated line count = %d, want %d", lines, n)
	}

	got, err := LoadEvents(dir)
	if err != nil {
		t.Fatalf("LoadEvents: %v (a parse error here means a torn/corrupted line)", err)
	}
	if len(got) != n {
		t.Fatalf("want %d events (one per goroutine), got %d — a write was lost", n, len(got))
	}
	seen := make(map[string]bool, n)
	for _, ev := range got {
		if seen[ev.SessionName] {
			t.Errorf("duplicate session_name %q in log — a write was double-counted", ev.SessionName)
		}
		seen[ev.SessionName] = true
	}
}

// TestAppendEvent_RejectsInvalidInput verifies the two write-time guards
// (empty plan_id, unknown/empty kind) reject before anything touches disk —
// events.jsonl is the single source of truth for lifecycle, so a malformed
// line is worth refusing at the door rather than writing and discovering
// later.
func TestAppendEvent_RejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name string
		ev   Event
	}{
		{"empty plan id", Event{Kind: EventCreated}},
		{"empty kind", Event{PlanID: "pln_x"}},
		{"unknown kind", Event{PlanID: "pln_x", Kind: EventKind("bogus")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := AppendEvent(context.Background(), dir, tt.ev); err == nil {
				t.Errorf("expected an error for %+v, got nil", tt.ev)
			}
			if _, statErr := os.Stat(filepath.Join(dir, eventsFile)); statErr == nil {
				t.Errorf("events file should not exist after a rejected append")
			}
		})
	}
}

// TestAppendEvent_DefaultsTimestampWhenZero verifies AppendEvent stamps
// "now" when the caller doesn't set one, so most call sites can omit it.
func TestAppendEvent_DefaultsTimestampWhenZero(t *testing.T) {
	dir := t.TempDir()
	before := time.Now().UTC()
	if err := AppendEvent(context.Background(), dir, Event{PlanID: "pln_x", Kind: EventCreated}); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	after := time.Now().UTC()

	got, err := LoadEvents(dir)
	if err != nil || len(got) != 1 {
		t.Fatalf("LoadEvents: err=%v events=%+v", err, got)
	}
	if got[0].Timestamp.Before(before) || got[0].Timestamp.After(after) {
		t.Errorf("Timestamp = %v, want within [%v, %v]", got[0].Timestamp, before, after)
	}
}

// TestLoadEvents_MissingFileReturnsNilNil verifies a plan with no recorded
// events (or one saved before events.jsonl existed) is not an error.
func TestLoadEvents_MissingFileReturnsNilNil(t *testing.T) {
	dir := t.TempDir()
	events, err := LoadEvents(dir)
	if err != nil {
		t.Fatalf("LoadEvents on missing file: %v", err)
	}
	if events != nil {
		t.Errorf("events = %+v, want nil", events)
	}
}

// TestLoadEvents_TolerantOfBlankLinesAndTrailingNewline verifies the parser
// skips blank lines (including the trailing newline every AppendEvent write
// leaves) rather than erroring on them.
func TestLoadEvents_TolerantOfBlankLinesAndTrailingNewline(t *testing.T) {
	dir := t.TempDir()
	content := "{\"ts\":\"2026-07-01T12:00:00Z\",\"plan_id\":\"pln_x\",\"event\":\"created\"}\n" +
		"\n" +
		"{\"ts\":\"2026-07-01T12:01:00Z\",\"plan_id\":\"pln_x\",\"event\":\"approved\"}\n" +
		"\n"
	if err := os.WriteFile(filepath.Join(dir, eventsFile), []byte(content), 0o644); err != nil {
		t.Fatalf("seed events file: %v", err)
	}

	got, err := LoadEvents(dir)
	if err != nil {
		t.Fatalf("LoadEvents: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 events (blank lines skipped), got %d: %+v", len(got), got)
	}
	if got[0].Kind != EventCreated || got[1].Kind != EventApproved {
		t.Errorf("unexpected kinds: %+v", got)
	}
}

// TestLoadEvents_MalformedLineErrors verifies a genuinely corrupt line
// surfaces as an error rather than being silently dropped — events.jsonl is
// written only by AppendEvent (never browser/human-authored like a feedback
// round), so a bad line means corruption worth knowing about.
func TestLoadEvents_MalformedLineErrors(t *testing.T) {
	dir := t.TempDir()
	content := "{\"ts\":\"2026-07-01T12:00:00Z\",\"plan_id\":\"pln_x\",\"event\":\"created\"}\n" +
		"not json at all\n"
	if err := os.WriteFile(filepath.Join(dir, eventsFile), []byte(content), 0o644); err != nil {
		t.Fatalf("seed events file: %v", err)
	}
	if _, err := LoadEvents(dir); err == nil {
		t.Error("expected an error for a malformed line, got nil")
	}
}

// assertFold does field-by-field comparison of a FoldedState, using .Equal
// for time.Time fields (see the == landmine noted in the round-trip test
// above) and slices.Equal for order-sensitive slice fields.
func assertFold(t *testing.T, got, want FoldedState) {
	t.Helper()
	if got.PlanID != want.PlanID {
		t.Errorf("PlanID = %q, want %q", got.PlanID, want.PlanID)
	}
	if got.Topic != want.Topic {
		t.Errorf("Topic = %q, want %q", got.Topic, want.Topic)
	}
	if got.Status != want.Status {
		t.Errorf("Status = %q, want %q", got.Status, want.Status)
	}
	if got.Visibility != want.Visibility {
		t.Errorf("Visibility = %q, want %q", got.Visibility, want.Visibility)
	}
	if !got.CreatedAt.Equal(want.CreatedAt) {
		t.Errorf("CreatedAt = %v, want %v", got.CreatedAt, want.CreatedAt)
	}
	if !got.UpdatedAt.Equal(want.UpdatedAt) {
		t.Errorf("UpdatedAt = %v, want %v", got.UpdatedAt, want.UpdatedAt)
	}
	if !slices.Equal(got.Authors, want.Authors) {
		t.Errorf("Authors = %v, want %v", got.Authors, want.Authors)
	}
	if len(got.Sessions) != len(want.Sessions) {
		t.Fatalf("Sessions count = %d, want %d (got %+v, want %+v)", len(got.Sessions), len(want.Sessions), got.Sessions, want.Sessions)
	}
	for i := range got.Sessions {
		g, w := got.Sessions[i], want.Sessions[i]
		if g.SessionName != w.SessionName || g.SessionID != w.SessionID || g.Author != w.Author {
			t.Errorf("Sessions[%d] identity = %+v, want %+v", i, g, w)
		}
		if !slices.Equal(g.Kinds, w.Kinds) {
			t.Errorf("Sessions[%d].Kinds = %v, want %v", i, g.Kinds, w.Kinds)
		}
		if !g.FirstAt.Equal(w.FirstAt) || !g.LastAt.Equal(w.LastAt) {
			t.Errorf("Sessions[%d] times = [%v,%v], want [%v,%v]", i, g.FirstAt, g.LastAt, w.FirstAt, w.LastAt)
		}
	}
}

// TestFold covers Fold's derivation rules in isolation (pure function, no
// disk IO): status/topic/visibility "latest wins", author/session dedup in
// first-seen order, session Kinds merged, and the empty-log default.
func TestFold(t *testing.T) {
	t0 := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	t1 := t0.Add(1 * time.Hour)
	t2 := t0.Add(2 * time.Hour)
	t3 := t0.Add(3 * time.Hour)

	tests := []struct {
		name   string
		events []Event
		want   FoldedState
	}{
		{
			name:   "empty log folds to draft/private/empty",
			events: nil,
			want:   FoldedState{Status: PlanStatusDraft, Visibility: defaultVisibility},
		},
		{
			name: "created, approved, realized across two sessions",
			events: []Event{
				{Timestamp: t0, PlanID: "pln_1", Kind: EventCreated, Status: PlanStatusDraft, Author: "ryan", Topic: "event log foundation", SessionName: "sess-a"},
				{Timestamp: t1, PlanID: "pln_1", Kind: EventApproved, Status: PlanStatusApproved, Author: "ryan", SessionName: "sess-a"},
				{Timestamp: t2, PlanID: "pln_1", Kind: EventRealized, Status: PlanStatusRealized, Author: "sam", SessionName: "sess-b"},
			},
			want: FoldedState{
				PlanID:     "pln_1",
				Topic:      "event log foundation",
				Status:     PlanStatusRealized,
				Authors:    []string{"ryan", "sam"},
				CreatedAt:  t0,
				UpdatedAt:  t2,
				Visibility: defaultVisibility,
				Sessions: []SessionRef{
					{SessionName: "sess-a", Author: "ryan", Kinds: []EventKind{EventCreated, EventApproved}, FirstAt: t0, LastAt: t1},
					{SessionName: "sess-b", Author: "sam", Kinds: []EventKind{EventRealized}, FirstAt: t2, LastAt: t2},
				},
			},
		},
		{
			name: "revised event's topic overrides created's",
			events: []Event{
				{Timestamp: t0, PlanID: "pln_2", Kind: EventCreated, Status: PlanStatusDraft, Topic: "first draft"},
				{Timestamp: t1, PlanID: "pln_2", Kind: EventRevised, Topic: "revised topic"},
			},
			want: FoldedState{
				PlanID: "pln_2", Topic: "revised topic", Status: PlanStatusDraft,
				CreatedAt: t0, UpdatedAt: t1, Visibility: defaultVisibility,
			},
		},
		{
			name: "repeated author after a different one keeps first-seen order",
			events: []Event{
				{Timestamp: t0, PlanID: "pln_3", Kind: EventCreated, Author: "ryan"},
				{Timestamp: t1, PlanID: "pln_3", Kind: EventWorked, Author: "sam"},
				{Timestamp: t2, PlanID: "pln_3", Kind: EventWorked, Author: "ryan"},
			},
			want: FoldedState{
				PlanID: "pln_3", Status: PlanStatusDraft, Authors: []string{"ryan", "sam"},
				CreatedAt: t0, UpdatedAt: t2, Visibility: defaultVisibility,
			},
		},
		{
			name: "repeated kind within one session merges to a single Kinds entry",
			events: []Event{
				{Timestamp: t0, PlanID: "pln_4", Kind: EventCreated, SessionName: "sess-a"},
				{Timestamp: t1, PlanID: "pln_4", Kind: EventWorked, SessionName: "sess-a"},
				{Timestamp: t2, PlanID: "pln_4", Kind: EventWorked, SessionName: "sess-a"},
			},
			want: FoldedState{
				PlanID: "pln_4", Status: PlanStatusDraft,
				CreatedAt: t0, UpdatedAt: t2, Visibility: defaultVisibility,
				Sessions: []SessionRef{
					{SessionName: "sess-a", Kinds: []EventKind{EventCreated, EventWorked}, FirstAt: t0, LastAt: t2},
				},
			},
		},
		{
			name: "visibility defaults private with no visibility-set event",
			events: []Event{
				{Timestamp: t0, PlanID: "pln_5", Kind: EventCreated},
			},
			want: FoldedState{
				PlanID: "pln_5", Status: PlanStatusDraft,
				CreatedAt: t0, UpdatedAt: t0, Visibility: defaultVisibility,
			},
		},
		{
			name: "visibility-set overrides the default",
			events: []Event{
				{Timestamp: t0, PlanID: "pln_6", Kind: EventCreated},
				{Timestamp: t1, PlanID: "pln_6", Kind: EventVisibilitySet, Visibility: "public"},
			},
			want: FoldedState{
				PlanID: "pln_6", Status: PlanStatusDraft,
				CreatedAt: t0, UpdatedAt: t1, Visibility: "public",
			},
		},
		{
			name: "session_id backfilled on a later event enriches the existing session ref",
			events: []Event{
				{Timestamp: t0, PlanID: "pln_7", Kind: EventCreated, SessionName: "sess-a", Author: "ryan"},
				{Timestamp: t1, PlanID: "pln_7", Kind: EventWorked, SessionName: "sess-a", SessionID: "ses_backfilled"},
			},
			want: FoldedState{
				PlanID: "pln_7", Status: PlanStatusDraft, Authors: []string{"ryan"},
				CreatedAt: t0, UpdatedAt: t1, Visibility: defaultVisibility,
				Sessions: []SessionRef{
					{SessionName: "sess-a", SessionID: "ses_backfilled", Author: "ryan", Kinds: []EventKind{EventCreated, EventWorked}, FirstAt: t0, LastAt: t1},
				},
			},
		},
		{
			name: "event with neither session_name nor session_id is not attributed to any session",
			events: []Event{
				{Timestamp: t0, PlanID: "pln_8", Kind: EventCreated},
				{Timestamp: t3, PlanID: "pln_8", Kind: EventAbandoned, Status: PlanStatusAbandoned},
			},
			want: FoldedState{
				PlanID: "pln_8", Status: PlanStatusAbandoned,
				CreatedAt: t0, UpdatedAt: t3, Visibility: defaultVisibility,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Fold(tt.events)
			assertFold(t, got, tt.want)
		})
	}
}

// TestFold_IsPure verifies calling Fold twice on the same slice yields
// identical results and never mutates the input — a caller re-folding after
// a fresh LoadEvents must not observe drift from a previous fold.
func TestFold_IsPure(t *testing.T) {
	t0 := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	events := []Event{
		{Timestamp: t0, PlanID: "pln_pure", Kind: EventCreated, Author: "ryan", SessionName: "sess-a"},
		{Timestamp: t0.Add(time.Minute), PlanID: "pln_pure", Kind: EventWorked, Author: "ryan", SessionName: "sess-a"},
	}
	snapshot := slices.Clone(events)

	first := Fold(events)
	second := Fold(events)
	assertFold(t, first, second)

	if !slices.Equal(events, snapshot) {
		t.Errorf("Fold mutated its input slice: got %+v, want %+v", events, snapshot)
	}
}

// TestAppendEvent_ThenFold_Integration exercises the full path — append via
// AppendEvent, read back via LoadEvents, fold via Fold — the way a caller
// actually uses this package, rather than only unit-testing Fold in
// isolation.
func TestAppendEvent_ThenFold_Integration(t *testing.T) {
	dir := t.TempDir()
	t0 := time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC)
	ctx := context.Background()

	steps := []Event{
		{Timestamp: t0, PlanID: "pln_int", Kind: EventCreated, Status: PlanStatusDraft, Author: "ryan", Topic: "integration test", SessionName: "sess-int"},
		{Timestamp: t0.Add(time.Hour), PlanID: "pln_int", Kind: EventApproved, Status: PlanStatusApproved, Author: "sam", SessionName: "sess-int"},
		{Timestamp: t0.Add(2 * time.Hour), PlanID: "pln_int", Kind: EventWorked, Produced: "abc1234", Author: "ryan", SessionName: "sess-int"},
		{Timestamp: t0.Add(3 * time.Hour), PlanID: "pln_int", Kind: EventRealized, Status: PlanStatusRealized, Author: "ryan", SessionName: "sess-int"},
	}
	for _, ev := range steps {
		if err := AppendEvent(ctx, dir, ev); err != nil {
			t.Fatalf("AppendEvent(%s): %v", ev.Kind, err)
		}
	}

	events, err := LoadEvents(dir)
	if err != nil {
		t.Fatalf("LoadEvents: %v", err)
	}
	state := Fold(events)

	assertFold(t, state, FoldedState{
		PlanID: "pln_int", Topic: "integration test", Status: PlanStatusRealized,
		Authors: []string{"ryan", "sam"}, CreatedAt: t0, UpdatedAt: t0.Add(3 * time.Hour),
		Visibility: defaultVisibility,
		Sessions: []SessionRef{
			{SessionName: "sess-int", Author: "ryan", Kinds: []EventKind{EventCreated, EventApproved, EventWorked, EventRealized}, FirstAt: t0, LastAt: t0.Add(3 * time.Hour)},
		},
	})
}
