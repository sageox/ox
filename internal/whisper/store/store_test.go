package store

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func tempDB(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "whisper.db")
}

func mustOpen(t *testing.T) *Store {
	t.Helper()
	s, err := Open(tempDB(t))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func makeEntry(id, topic string, importance Importance, createdAt time.Time) WhisperEntry {
	return WhisperEntry{
		ID:         id,
		Scope:      "ledger",
		Type:       WhisperTrigger,
		Source:     "test",
		Topic:      topic,
		Content:    "test content for " + id,
		Importance: importance,
		CreatedAt:  createdAt,
	}
}

func TestOpenClose(t *testing.T) {
	s := mustOpen(t)
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	// double close is safe
	if err := s.Close(); err != nil {
		t.Fatalf("double close: %v", err)
	}
}

func TestAddAndGetWhispers(t *testing.T) {
	s := mustOpen(t)
	now := time.Now()

	entries := []WhisperEntry{
		makeEntry("w1", "lint", ImportanceNormal, now.Add(-2*time.Minute)),
		makeEntry("w2", "build", ImportanceCritical, now.Add(-1*time.Minute)),
		makeEntry("w3", "discovery", ImportanceAmbient, now),
	}

	if err := s.Add(entries...); err != nil {
		t.Fatalf("add: %v", err)
	}

	// first query: all entries (no prior cursor)
	got, err := s.GetWhispers("agent-1", AttentionAll, nil)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3, got %d", len(got))
	}

	// critical should come first (importance ordering)
	if got[0].ID != "w2" {
		t.Errorf("expected critical first, got %s (importance=%s)", got[0].ID, got[0].Importance)
	}
}

func TestCursorPersistence(t *testing.T) {
	dbPath := tempDB(t)

	// open, add, query to set cursor
	s1, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	now := time.Now()
	s1.Add(makeEntry("w1", "lint", ImportanceNormal, now.Add(-1*time.Minute)))
	s1.GetWhispers("agent-1", AttentionAll, nil)
	s1.Close()

	// reopen, add new entry, query — should only get new entry
	s2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()

	s2.Add(makeEntry("w2", "build", ImportanceNormal, time.Now()))
	got, err := s2.GetWhispers("agent-1", AttentionAll, nil)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 new entry, got %d", len(got))
	}
	if got[0].ID != "w2" {
		t.Errorf("expected w2, got %s", got[0].ID)
	}
}

func TestAttentionFiltering(t *testing.T) {
	s := mustOpen(t)
	now := time.Now()

	s.Add(
		makeEntry("crit", "lint", ImportanceCritical, now.Add(-2*time.Second)),
		makeEntry("norm", "lint", ImportanceNormal, now.Add(-1*time.Second)),
		makeEntry("ambi", "lint", ImportanceAmbient, now),
	)

	tests := []struct {
		name      string
		attention Attention
		wantIDs   []string
	}{
		{"focused", AttentionFocused, []string{"crit"}},
		{"normal", AttentionNormal, []string{"crit", "norm"}},
		{"all", AttentionAll, []string{"crit", "norm", "ambi"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := s.GetWhispers("agent-"+tt.name, tt.attention, nil)
			if err != nil {
				t.Fatalf("get: %v", err)
			}
			if len(got) != len(tt.wantIDs) {
				t.Fatalf("expected %d entries, got %d", len(tt.wantIDs), len(got))
			}
			for i, id := range tt.wantIDs {
				if got[i].ID != id {
					t.Errorf("entry %d: expected %s, got %s", i, id, got[i].ID)
				}
			}
		})
	}
}

func TestTopicFiltering(t *testing.T) {
	s := mustOpen(t)
	now := time.Now()

	s.Add(
		makeEntry("w1", "lint", ImportanceNormal, now.Add(-2*time.Second)),
		makeEntry("w2", "build", ImportanceNormal, now.Add(-1*time.Second)),
		makeEntry("w3", "test", ImportanceNormal, now),
	)

	got, err := s.GetWhispers("agent-1", AttentionAll, []string{"lint", "build"})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2, got %d", len(got))
	}

	// nil topics = all topics
	got2, err := s.GetWhispers("agent-2", AttentionAll, nil)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got2) != 3 {
		t.Fatalf("expected 3, got %d", len(got2))
	}
}

func TestDedupByID(t *testing.T) {
	s := mustOpen(t)
	now := time.Now()

	e := makeEntry("dup-1", "lint", ImportanceNormal, now)
	s.Add(e)
	s.Add(e) // duplicate

	got, err := s.GetWhispers("agent-1", AttentionAll, nil)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 (deduped), got %d", len(got))
	}
}

func TestRelayedTracking(t *testing.T) {
	s := mustOpen(t)

	ok, err := s.IsRelayed("murmur-1", "ledger")
	if err != nil {
		t.Fatalf("is relayed: %v", err)
	}
	if ok {
		t.Fatal("expected not relayed")
	}

	if err := s.MarkRelayed("murmur-1", "ledger"); err != nil {
		t.Fatalf("mark relayed: %v", err)
	}

	ok, err = s.IsRelayed("murmur-1", "ledger")
	if err != nil {
		t.Fatalf("is relayed: %v", err)
	}
	if !ok {
		t.Fatal("expected relayed")
	}

	// same murmur ID, different scope — not relayed
	ok, err = s.IsRelayed("murmur-1", "team")
	if err != nil {
		t.Fatalf("is relayed team: %v", err)
	}
	if ok {
		t.Fatal("expected not relayed in team scope")
	}
}

func TestRelayedPersistsAcrossReopen(t *testing.T) {
	dbPath := tempDB(t)

	s1, _ := Open(dbPath)
	s1.MarkRelayed("murmur-1", "ledger")
	s1.Close()

	s2, _ := Open(dbPath)
	defer s2.Close()

	ok, _ := s2.IsRelayed("murmur-1", "ledger")
	if !ok {
		t.Fatal("expected relayed after reopen")
	}
}

func TestPrune(t *testing.T) {
	s := mustOpen(t)

	old := time.Now().Add(-48 * time.Hour)
	recent := time.Now().Add(-1 * time.Hour)

	s.Add(
		makeEntry("old-1", "lint", ImportanceNormal, old),
		makeEntry("old-2", "build", ImportanceNormal, old),
		makeEntry("new-1", "lint", ImportanceNormal, recent),
	)
	s.MarkRelayed("relay-old", "ledger")
	// manually backdate the relayed entry
	s.db.Exec("UPDATE relayed_murmurs SET relayed_at = ? WHERE murmur_id = ?",
		old.Format(time.RFC3339Nano), "relay-old")

	result, err := s.Prune(24 * time.Hour)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}

	if result.WhispersDeleted != 2 {
		t.Errorf("expected 2 whispers deleted, got %d", result.WhispersDeleted)
	}
	if result.RelayedDeleted != 1 {
		t.Errorf("expected 1 relayed deleted, got %d", result.RelayedDeleted)
	}

	// new entry should survive
	got, _ := s.GetWhispers("agent-1", AttentionAll, nil)
	if len(got) != 1 {
		t.Fatalf("expected 1 surviving entry, got %d", len(got))
	}
}

func TestScopeCoexistence(t *testing.T) {
	s := mustOpen(t)
	now := time.Now()

	s.Add(
		WhisperEntry{
			ID: "ledger-1", Scope: "ledger", Type: WhisperTrigger,
			Source: "test", Topic: "lint", Content: "repo lint",
			Importance: ImportanceNormal, CreatedAt: now.Add(-1 * time.Second),
		},
		WhisperEntry{
			ID: "team-1", Scope: "team", Type: WhisperTrigger,
			Source: "test", Topic: "architecture", Content: "team arch",
			Importance: ImportanceNormal, CreatedAt: now,
		},
	)

	// both scopes queryable
	got, _ := s.GetWhispers("agent-1", AttentionAll, nil)
	if len(got) != 2 {
		t.Fatalf("expected 2, got %d", len(got))
	}
}

func TestMetadataRoundTrip(t *testing.T) {
	s := mustOpen(t)
	now := time.Now()

	e := makeEntry("meta-1", "lint", ImportanceNormal, now)
	e.Metadata = map[string]string{
		"files":  "internal/auth.go",
		"branch": "ryan/auth-refactor",
		"pr":     "287",
	}

	s.Add(e)

	got, _ := s.GetWhispers("agent-1", AttentionAll, nil)
	if len(got) != 1 {
		t.Fatalf("expected 1, got %d", len(got))
	}
	if got[0].Metadata["files"] != "internal/auth.go" {
		t.Errorf("metadata files: %v", got[0].Metadata)
	}
	if got[0].Metadata["pr"] != "287" {
		t.Errorf("metadata pr: %v", got[0].Metadata)
	}
}

func TestPrincipalFields(t *testing.T) {
	s := mustOpen(t)
	now := time.Now()

	e := makeEntry("principal-1", "lint", ImportanceNormal, now)
	e.AgentID = "Oxa7b3"
	e.PrincipalID = "user@example.com"
	e.PrincipalType = "human"
	e.TeamID = "team-123"

	s.Add(e)

	got, _ := s.GetWhispers("agent-1", AttentionAll, nil)
	if len(got) != 1 {
		t.Fatalf("expected 1, got %d", len(got))
	}
	if got[0].AgentID != "Oxa7b3" {
		t.Errorf("agent_id: %s", got[0].AgentID)
	}
	if got[0].PrincipalID != "user@example.com" {
		t.Errorf("principal_id: %s", got[0].PrincipalID)
	}
	if got[0].PrincipalType != "human" {
		t.Errorf("principal_type: %s", got[0].PrincipalType)
	}
	if got[0].TeamID != "team-123" {
		t.Errorf("team_id: %s", got[0].TeamID)
	}
}

func TestEmptyStore(t *testing.T) {
	s := mustOpen(t)

	got, err := s.GetWhispers("agent-1", AttentionAll, nil)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil slice")
	}
	if len(got) != 0 {
		t.Fatalf("expected empty, got %d", len(got))
	}
}

func TestRemoveCursor(t *testing.T) {
	s := mustOpen(t)
	now := time.Now()

	s.Add(makeEntry("w1", "lint", ImportanceNormal, now))
	s.GetWhispers("agent-1", AttentionAll, nil) // sets cursor

	if err := s.RemoveCursor("agent-1"); err != nil {
		t.Fatalf("remove cursor: %v", err)
	}

	// after cursor removal, should get all entries again
	got, _ := s.GetWhispers("agent-1", AttentionAll, nil)
	if len(got) != 1 {
		t.Fatalf("expected 1 after cursor reset, got %d", len(got))
	}
}

func TestCorruptionRecovery(t *testing.T) {
	dbPath := tempDB(t)

	// create valid DB
	s1, err := Open(dbPath)
	if err != nil {
		t.Fatalf("initial open: %v", err)
	}
	s1.Add(makeEntry("w1", "lint", ImportanceNormal, time.Now()))
	s1.Close()

	// corrupt it
	if err := os.WriteFile(dbPath, []byte("corrupted data"), 0o644); err != nil {
		t.Fatalf("corrupt: %v", err)
	}

	// open should auto-recover
	s2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("recovery open: %v", err)
	}
	defer s2.Close()

	// should work (empty but functional)
	got, err := s2.GetWhispers("agent-1", AttentionAll, nil)
	if err != nil {
		t.Fatalf("get after recovery: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty after recovery, got %d", len(got))
	}

	// can add new entries
	if err := s2.Add(makeEntry("w2", "build", ImportanceNormal, time.Now())); err != nil {
		t.Fatalf("add after recovery: %v", err)
	}
}

func TestDeleteRecovery(t *testing.T) {
	dbPath := tempDB(t)

	// create valid DB then delete it
	s1, _ := Open(dbPath)
	s1.Close()
	os.Remove(dbPath)

	// open should create fresh
	s2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open after delete: %v", err)
	}
	defer s2.Close()

	if err := s2.Add(makeEntry("w1", "lint", ImportanceNormal, time.Now())); err != nil {
		t.Fatalf("add after delete recovery: %v", err)
	}
}

func TestWALSHMCleanup(t *testing.T) {
	dbPath := tempDB(t)

	// create stale WAL/SHM files alongside corrupt DB
	os.WriteFile(dbPath, []byte("corrupt"), 0o644)
	os.WriteFile(dbPath+"-wal", []byte("stale wal"), 0o644)
	os.WriteFile(dbPath+"-shm", []byte("stale shm"), 0o644)

	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	// should have recovered — old WAL/SHM cleaned up
	if err := s.Add(makeEntry("w1", "lint", ImportanceNormal, time.Now())); err != nil {
		t.Fatalf("add: %v", err)
	}
}

func TestConcurrentAccess(t *testing.T) {
	s := mustOpen(t)
	now := time.Now()

	// add some entries
	for i := range 50 {
		s.Add(makeEntry(
			fmt.Sprintf("w%d", i), "lint", ImportanceNormal,
			now.Add(time.Duration(i)*time.Millisecond),
		))
	}

	var wg sync.WaitGroup
	for i := range 10 {
		wg.Add(1)
		go func(agentNum int) {
			defer wg.Done()
			agentID := fmt.Sprintf("agent-%d", agentNum)
			for range 5 {
				s.GetWhispers(agentID, AttentionAll, nil)
			}
		}(i)
	}

	// concurrent writes
	for i := range 10 {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			s.Add(makeEntry(
				fmt.Sprintf("w%d", n+50), "build", ImportanceCritical,
				time.Now(),
			))
		}(i)
	}

	wg.Wait()
}

func TestEnforceMaxSize(t *testing.T) {
	s := mustOpen(t)

	// add enough entries to make a non-trivial DB
	for i := range 200 {
		s.Add(makeEntry(
			fmt.Sprintf("w%d", i), "lint", ImportanceNormal,
			time.Now().Add(-time.Duration(i)*time.Minute),
		))
	}

	// enforce a very low max size (1 byte) to force cleanup
	if err := s.EnforceMaxSize(1); err != nil {
		t.Fatalf("enforce: %v", err)
	}

	// should still be functional
	got, err := s.GetWhispers("agent-1", AttentionAll, nil)
	if err != nil {
		t.Fatalf("get after enforce: %v", err)
	}
	// all or some entries pruned
	_ = got
}

func TestImportanceOrdering(t *testing.T) {
	s := mustOpen(t)
	now := time.Now()

	// add in wrong order (ambient first, critical last)
	s.Add(
		makeEntry("ambi", "lint", ImportanceAmbient, now.Add(-3*time.Second)),
		makeEntry("norm", "lint", ImportanceNormal, now.Add(-2*time.Second)),
		makeEntry("crit", "lint", ImportanceCritical, now.Add(-1*time.Second)),
	)

	got, _ := s.GetWhispers("agent-1", AttentionAll, nil)
	if len(got) != 3 {
		t.Fatalf("expected 3, got %d", len(got))
	}
	// critical first regardless of creation time
	if got[0].Importance != ImportanceCritical {
		t.Errorf("expected critical first, got %s", got[0].Importance)
	}
	if got[1].Importance != ImportanceNormal {
		t.Errorf("expected normal second, got %s", got[1].Importance)
	}
	if got[2].Importance != ImportanceAmbient {
		t.Errorf("expected ambient third, got %s", got[2].Importance)
	}
}

func TestAddEmpty(t *testing.T) {
	s := mustOpen(t)
	// should be a no-op, not an error
	if err := s.Add(); err != nil {
		t.Fatalf("add empty: %v", err)
	}
}

func TestCheckIntegrity(t *testing.T) {
	s := mustOpen(t)
	if err := s.CheckIntegrity(); err != nil {
		t.Fatalf("integrity: %v", err)
	}
}

func TestMetadataJSON(t *testing.T) {
	s := mustOpen(t)
	now := time.Now()

	// entry without metadata
	e1 := makeEntry("no-meta", "lint", ImportanceNormal, now)
	s.Add(e1)

	got, _ := s.GetWhispers("agent-1", AttentionAll, nil)
	if len(got) != 1 {
		t.Fatalf("expected 1, got %d", len(got))
	}
	if got[0].Metadata != nil {
		t.Errorf("expected nil metadata, got %v", got[0].Metadata)
	}
}

func TestMultiDaemonTeamDB(t *testing.T) {
	// simulate two daemons accessing the same team whisper DB
	dbPath := tempDB(t)

	s1, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open s1: %v", err)
	}
	defer s1.Close()

	s2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open s2: %v", err)
	}
	defer s2.Close()

	now := time.Now()

	// both write
	if err := s1.Add(makeEntry("from-daemon-1", "lint", ImportanceNormal, now)); err != nil {
		t.Fatalf("s1 add: %v", err)
	}
	if err := s2.Add(makeEntry("from-daemon-2", "build", ImportanceNormal, now.Add(time.Millisecond))); err != nil {
		t.Fatalf("s2 add: %v", err)
	}

	// both read
	got1, _ := s1.GetWhispers("agent-a", AttentionAll, nil)
	got2, _ := s2.GetWhispers("agent-b", AttentionAll, nil)

	if len(got1) != 2 {
		t.Errorf("s1 expected 2, got %d", len(got1))
	}
	if len(got2) != 2 {
		t.Errorf("s2 expected 2, got %d", len(got2))
	}
}

