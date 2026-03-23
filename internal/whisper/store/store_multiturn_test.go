package store

import (
	"fmt"
	"testing"
	"time"
)

// makeTypedEntry creates a WhisperEntry with an explicit type.
func makeTypedEntry(id, topic string, typ WhisperType, importance Importance, createdAt time.Time) WhisperEntry {
	return WhisperEntry{
		ID:         id,
		Scope:      "ledger",
		Type:       typ,
		Source:     "test",
		Topic:      topic,
		Content:    "content for " + id,
		Importance: importance,
		CreatedAt:  createdAt,
	}
}

// tick sleeps 2ms to ensure time.Now() advances past any cursor set by GetWhispers.
// In production, whispers are added asynchronously (daemon sync, murmur relay) so
// there's always a time gap between cursor-set and entry creation.
func tick() { time.Sleep(2 * time.Millisecond) }

func TestMultiTurnWhisperDelivery(t *testing.T) {
	s := mustOpen(t)
	agent := "agent-multi"

	type whisperSpec struct {
		id         string
		topic      string
		importance Importance
	}

	schedule := map[int][]whisperSpec{
		1:  {{id: "t1-w1", topic: "lint", importance: ImportanceNormal}},
		2:  {},
		3:  {{id: "t3-w1", topic: "build", importance: ImportanceCritical}, {id: "t3-w2", topic: "test", importance: ImportanceAmbient}},
		4:  {},
		5:  {},
		6:  {{id: "t6-w1", topic: "deploy", importance: ImportanceNormal}},
		7:  {},
		8:  {{id: "t8-w1", topic: "security", importance: ImportanceCritical}},
		9:  {},
		10: {{id: "t10-w1", topic: "perf", importance: ImportanceAmbient}},
		11: {},
		12: {},
	}

	totalDelivered := 0
	for turn := 1; turn <= 12; turn++ {
		items := schedule[turn]
		if len(items) > 0 {
			tick() // ensure timestamps are after the previous cursor
			ts := time.Now()
			var entries []WhisperEntry
			for i, item := range items {
				entries = append(entries, makeEntry(item.id, item.topic, item.importance,
					ts.Add(time.Duration(i)*time.Microsecond)))
			}
			if err := s.Add(entries...); err != nil {
				t.Fatalf("turn %d add: %v", turn, err)
			}
			tick() // ensure cursor will be after these entries
		}

		got, err := s.GetWhispers(agent, AttentionAll, nil)
		if err != nil {
			t.Fatalf("turn %d get: %v", turn, err)
		}

		expectedNew := len(items)
		if len(got) != expectedNew {
			t.Errorf("turn %d: expected %d new whispers, got %d", turn, expectedNew, len(got))
		}
		totalDelivered += len(got)
	}

	totalAdded := 0
	for _, items := range schedule {
		totalAdded += len(items)
	}
	if totalDelivered != totalAdded {
		t.Errorf("total delivered %d != total added %d", totalDelivered, totalAdded)
	}
}

func TestNoDuplicateWhispers(t *testing.T) {
	s := mustOpen(t)
	agent := "agent-nodup"

	// add 5 whispers
	ts := time.Now()
	for i := range 5 {
		s.Add(makeEntry(fmt.Sprintf("w%d", i), "lint", ImportanceNormal,
			ts.Add(time.Duration(i)*time.Microsecond)))
	}
	tick()

	// first query gets all 5
	got1, err := s.GetWhispers(agent, AttentionAll, nil)
	if err != nil {
		t.Fatalf("first get: %v", err)
	}
	if len(got1) != 5 {
		t.Fatalf("expected 5, got %d", len(got1))
	}

	// second query with no new whispers gets 0
	got2, err := s.GetWhispers(agent, AttentionAll, nil)
	if err != nil {
		t.Fatalf("second get: %v", err)
	}
	if len(got2) != 0 {
		t.Errorf("expected 0 on re-query, got %d", len(got2))
	}

	// add 1 new whisper after cursor
	tick()
	s.Add(makeEntry("w-new", "build", ImportanceCritical, time.Now()))
	tick()

	got3, err := s.GetWhispers(agent, AttentionAll, nil)
	if err != nil {
		t.Fatalf("third get: %v", err)
	}
	if len(got3) != 1 {
		t.Errorf("expected 1 new, got %d", len(got3))
	}
	if len(got3) > 0 && got3[0].ID != "w-new" {
		t.Errorf("expected w-new, got %s", got3[0].ID)
	}
}

func TestIndependentAgentCursors(t *testing.T) {
	s := mustOpen(t)

	// add 3 whispers
	ts := time.Now()
	for i := range 3 {
		s.Add(makeEntry(fmt.Sprintf("shared-%d", i), "lint", ImportanceNormal,
			ts.Add(time.Duration(i)*time.Microsecond)))
	}
	tick()

	// agent-A queries and gets all 3
	gotA, err := s.GetWhispers("agent-A", AttentionAll, nil)
	if err != nil {
		t.Fatalf("agent-A get: %v", err)
	}
	if len(gotA) != 3 {
		t.Fatalf("agent-A expected 3, got %d", len(gotA))
	}

	// agent-B queries independently and also gets all 3
	gotB, err := s.GetWhispers("agent-B", AttentionAll, nil)
	if err != nil {
		t.Fatalf("agent-B get: %v", err)
	}
	if len(gotB) != 3 {
		t.Fatalf("agent-B expected 3, got %d", len(gotB))
	}

	// add 1 more whisper after both cursors
	tick()
	s.Add(makeEntry("new-after", "build", ImportanceNormal, time.Now()))
	tick()

	// agent-A gets only the new one (cursor advanced)
	gotA2, _ := s.GetWhispers("agent-A", AttentionAll, nil)
	if len(gotA2) != 1 {
		t.Errorf("agent-A second query: expected 1, got %d", len(gotA2))
	}

	// agent-B also gets only the new one (independent cursor)
	gotB2, _ := s.GetWhispers("agent-B", AttentionAll, nil)
	if len(gotB2) != 1 {
		t.Errorf("agent-B second query: expected 1, got %d", len(gotB2))
	}

	// agent-C (never queried before) gets all 4
	gotC, _ := s.GetWhispers("agent-C", AttentionAll, nil)
	if len(gotC) != 4 {
		t.Errorf("agent-C (fresh) expected 4, got %d", len(gotC))
	}
}

func TestAllWhisperTypesDelivered(t *testing.T) {
	s := mustOpen(t)
	ts := time.Now()

	entries := []WhisperEntry{
		makeTypedEntry("trigger-1", "murmur", WhisperTrigger, ImportanceCritical, ts),
		makeTypedEntry("structural-1", "lifecycle", WhisperStructural, ImportanceNormal, ts.Add(time.Microsecond)),
		makeTypedEntry("timebased-1", "reminder", WhisperTimeBased, ImportanceAmbient, ts.Add(2*time.Microsecond)),
	}
	if err := s.Add(entries...); err != nil {
		t.Fatalf("add: %v", err)
	}
	tick()

	got, err := s.GetWhispers("agent-types", AttentionAll, nil)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3, got %d", len(got))
	}

	typesSeen := map[WhisperType]bool{}
	for _, e := range got {
		typesSeen[e.Type] = true
	}
	for _, wt := range []WhisperType{WhisperTrigger, WhisperStructural, WhisperTimeBased} {
		if !typesSeen[wt] {
			t.Errorf("whisper type %s not delivered", wt)
		}
	}

	// verify importance ordering: critical first
	if got[0].Type != WhisperTrigger || got[0].Importance != ImportanceCritical {
		t.Errorf("expected critical trigger first, got type=%s importance=%s", got[0].Type, got[0].Importance)
	}
}

func TestCursorSurvivesStoreReopen(t *testing.T) {
	dbPath := tempDB(t)

	// open, add entries, query to set cursor, close
	s1, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open s1: %v", err)
	}
	ts := time.Now()
	s1.Add(
		makeEntry("pre-1", "lint", ImportanceNormal, ts),
		makeEntry("pre-2", "build", ImportanceNormal, ts.Add(time.Microsecond)),
	)
	tick()
	got1, _ := s1.GetWhispers("agent-persist", AttentionAll, nil)
	if len(got1) != 2 {
		t.Fatalf("s1: expected 2, got %d", len(got1))
	}
	s1.Close()

	// reopen, add new entry after the persisted cursor, query
	s2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open s2: %v", err)
	}
	defer s2.Close()

	tick()
	s2.Add(makeEntry("post-1", "deploy", ImportanceCritical, time.Now()))
	tick()

	got2, _ := s2.GetWhispers("agent-persist", AttentionAll, nil)
	if len(got2) != 1 {
		t.Fatalf("s2: expected 1 new entry, got %d", len(got2))
	}
	if got2[0].ID != "post-1" {
		t.Errorf("expected post-1, got %s", got2[0].ID)
	}
}

func TestHighVolumeNoDuplicates(t *testing.T) {
	s := mustOpen(t)
	agent := "agent-volume"

	totalWhispers := 100
	batchSize := 10
	allDelivered := map[string]int{}

	for batch := 0; batch < totalWhispers/batchSize; batch++ {
		tick()
		ts := time.Now()
		var entries []WhisperEntry
		for i := range batchSize {
			idx := batch*batchSize + i
			entries = append(entries, makeEntry(
				fmt.Sprintf("vol-%d", idx), "stream",
				ImportanceNormal,
				ts.Add(time.Duration(i)*time.Microsecond),
			))
		}
		if err := s.Add(entries...); err != nil {
			t.Fatalf("batch %d add: %v", batch, err)
		}
		tick()

		got, err := s.GetWhispers(agent, AttentionAll, nil)
		if err != nil {
			t.Fatalf("batch %d get: %v", batch, err)
		}

		for _, e := range got {
			allDelivered[e.ID]++
		}
	}

	if len(allDelivered) != totalWhispers {
		t.Errorf("expected %d unique whispers, got %d", totalWhispers, len(allDelivered))
	}
	for id, count := range allDelivered {
		if count != 1 {
			t.Errorf("whisper %s delivered %d times (expected 1)", id, count)
		}
	}
}
