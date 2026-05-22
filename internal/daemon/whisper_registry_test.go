package daemon

import (
	"path/filepath"
	"testing"
	"time"

	whisperstore "github.com/sageox/ox/internal/whisper/store"
)

func openTestStore(t *testing.T) *whisperstore.Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "whisper.db")
	s, err := whisperstore.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestWhisperRegistryAddAndGet(t *testing.T) {
	ledger := openTestStore(t)
	team := openTestStore(t)

	r := NewWhisperRegistry(ledger, nil)
	r.AddTeamStore("team-1", team)
	defer r.Close()

	now := time.Now()

	// add to ledger scope
	err := r.Add("ledger", whisperstore.WhisperEntry{
		ID: "l1", Scope: "ledger", Type: whisperstore.WhisperTrigger,
		Source: "test", Topic: "lint", Content: "ledger lint",
		Importance: whisperstore.ImportanceNormal, CreatedAt: now,
	})
	if err != nil {
		t.Fatalf("add ledger: %v", err)
	}

	// add to team scope
	err = r.Add("team", whisperstore.WhisperEntry{
		ID: "t1", Scope: "team", Type: whisperstore.WhisperTrigger,
		Source: "test", Topic: "architecture", Content: "team arch",
		Importance: whisperstore.ImportanceCritical, CreatedAt: now.Add(time.Millisecond),
	})
	if err != nil {
		t.Fatalf("add team: %v", err)
	}

	// get all whispers — should merge from both stores
	got, err := r.GetWhispers("agent-1", whisperstore.AttentionAll, nil)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2, got %d", len(got))
	}
}

func TestWhisperRegistryPrune(t *testing.T) {
	ledger := openTestStore(t)
	r := NewWhisperRegistry(ledger, nil)
	defer r.Close()

	old := time.Now().Add(-48 * time.Hour)
	r.Add("ledger", whisperstore.WhisperEntry{
		ID: "old", Scope: "ledger", Type: whisperstore.WhisperTrigger,
		Source: "test", Topic: "lint", Content: "old",
		Importance: whisperstore.ImportanceNormal, CreatedAt: old,
	})

	r.Prune(24 * time.Hour)

	got, _ := r.GetWhispers("agent-1", whisperstore.AttentionAll, nil)
	if len(got) != 0 {
		t.Fatalf("expected 0 after prune, got %d", len(got))
	}
}

func TestWhisperRegistryRemoveCursor(t *testing.T) {
	ledger := openTestStore(t)
	r := NewWhisperRegistry(ledger, nil)
	defer r.Close()

	now := time.Now()
	r.Add("ledger", whisperstore.WhisperEntry{
		ID: "w1", Scope: "ledger", Type: whisperstore.WhisperTrigger,
		Source: "test", Topic: "lint", Content: "test",
		Importance: whisperstore.ImportanceNormal, CreatedAt: now,
	})

	// set cursor
	r.GetWhispers("agent-1", whisperstore.AttentionAll, nil)

	// remove cursor
	r.RemoveCursor("agent-1")

	// should get entries again
	got, _ := r.GetWhispers("agent-1", whisperstore.AttentionAll, nil)
	if len(got) != 1 {
		t.Fatalf("expected 1 after cursor reset, got %d", len(got))
	}
}

func TestWhisperRegistryHasTeamStore(t *testing.T) {
	registry := NewWhisperRegistry(nil, nil)

	if registry.HasTeamStore("team-1") {
		t.Error("expected HasTeamStore to return false for unregistered team")
	}

	// nil store is accepted by AddTeamStore — sufficient for map lookup testing
	registry.AddTeamStore("team-1", nil)

	if !registry.HasTeamStore("team-1") {
		t.Error("expected HasTeamStore to return true after AddTeamStore")
	}

	if registry.HasTeamStore("team-2") {
		t.Error("expected HasTeamStore to return false for different team")
	}
}

func TestWhisperRegistryUnknownScope(t *testing.T) {
	ledger := openTestStore(t)
	r := NewWhisperRegistry(ledger, nil)
	defer r.Close()

	err := r.Add("unknown", whisperstore.WhisperEntry{
		ID: "x", Scope: "unknown", Type: whisperstore.WhisperTrigger,
		Source: "test", Topic: "lint", Content: "test",
		Importance: whisperstore.ImportanceNormal, CreatedAt: time.Now(),
	})
	if err == nil {
		t.Fatal("expected error for unknown scope")
	}
}

func TestWhisperRegistryReopenLedgerStore(t *testing.T) {
	ledger := openTestStore(t)
	r := NewWhisperRegistry(ledger, nil)
	defer r.Close()

	// add a whisper to the original store
	err := r.Add("ledger", whisperstore.WhisperEntry{
		ID:         "before-reopen",
		Scope:      "ledger",
		Type:       whisperstore.WhisperTimeBased,
		Source:     "test",
		Topic:      "test",
		Content:    "original",
		Importance: whisperstore.ImportanceNormal,
		CreatedAt:  time.Now(),
	})
	if err != nil {
		t.Fatalf("add before reopen: %v", err)
	}

	// reopen to a new DB path (simulates GC reclone)
	newDBPath := filepath.Join(t.TempDir(), "whisper-new.db")
	if err := r.ReopenLedgerStore(newDBPath); err != nil {
		t.Fatalf("reopen: %v", err)
	}

	// add a whisper to the new store
	err = r.Add("ledger", whisperstore.WhisperEntry{
		ID:         "after-reopen",
		Scope:      "ledger",
		Type:       whisperstore.WhisperTimeBased,
		Source:     "test",
		Topic:      "test",
		Content:    "new",
		Importance: whisperstore.ImportanceNormal,
		CreatedAt:  time.Now(),
	})
	if err != nil {
		t.Fatalf("add after reopen: %v", err)
	}

	// verify new store works — should only have the post-reopen entry
	entries, _, err := r.GetWhispersPage("", time.Time{}, 50)
	if err != nil {
		t.Fatalf("get whispers: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry in new store, got %d", len(entries))
	}
	if entries[0].ID != "after-reopen" {
		t.Errorf("expected after-reopen entry, got %s", entries[0].ID)
	}
}

func TestWhisperRegistryUnknownScopeRelayHandling(t *testing.T) {
	ledger := openTestStore(t)
	r := NewWhisperRegistry(ledger, nil)
	defer r.Close()

	// IsRelayed should return false for an unknown scope (nil store)
	relayed, err := r.IsRelayed("murmur-unknown", "bogus-scope")
	if err != nil {
		t.Fatalf("IsRelayed with unknown scope should not error, got: %v", err)
	}
	if relayed {
		t.Error("IsRelayed should return false for unknown scope")
	}

	// MarkRelayed should return nil for an unknown scope (nil store)
	err = r.MarkRelayed("murmur-unknown", "bogus-scope")
	if err != nil {
		t.Fatalf("MarkRelayed with unknown scope should not error, got: %v", err)
	}
}

// --- Idle-close behavior ---
//
// Failure prevented: team whisper SQLite stores accumulating FDs across long
// daemon uptimes when whisper traffic is sparse (every-few-minutes workload).

func TestCloseIdleTeamStores_ClosesOnlyIdle(t *testing.T) {
	ledger := openTestStore(t)
	teamA := openTestStore(t)
	teamB := openTestStore(t)
	r := NewWhisperRegistry(ledger, nil)
	defer r.Close()

	clock := time.Now()
	r.SetClock(func() time.Time { return clock })

	r.AddTeamStore("team-a", teamA)
	r.AddTeamStore("team-b", teamB)

	// Advance past threshold, then touch team-b only.
	clock = clock.Add(20 * time.Minute)
	if _, err := r.GetWhispers("agent-1", whisperstore.AttentionAll, nil); err != nil {
		t.Fatalf("GetWhispers: %v", err)
	}
	// Now team-a's lastAccess is stale (set at clock-20min, < cutoff for a
	// 15-minute threshold). team-b was just touched. But the touch happens
	// on every team in GetWhispers, so both are fresh. Override team-a to
	// simulate it being idle, then assert only team-a closes.
	r.mu.Lock()
	r.lastAccess["team-a"] = clock.Add(-30 * time.Minute)
	r.mu.Unlock()

	closed := r.CloseIdleTeamStores(15 * time.Minute)
	if closed != 1 {
		t.Fatalf("expected 1 idle close, got %d", closed)
	}
	if r.HasTeamStore("team-a") {
		t.Error("expected team-a to be closed")
	}
	if !r.HasTeamStore("team-b") {
		t.Error("expected team-b to remain open")
	}
}

func TestCloseIdleTeamStores_NoopBelowThreshold(t *testing.T) {
	ledger := openTestStore(t)
	team := openTestStore(t)
	r := NewWhisperRegistry(ledger, nil)
	defer r.Close()

	clock := time.Now()
	r.SetClock(func() time.Time { return clock })
	r.AddTeamStore("team-a", team)

	// Only 5 minutes pass — under the 15-minute threshold.
	clock = clock.Add(5 * time.Minute)
	closed := r.CloseIdleTeamStores(15 * time.Minute)
	if closed != 0 {
		t.Fatalf("expected 0 closes below threshold, got %d", closed)
	}
	if !r.HasTeamStore("team-a") {
		t.Error("expected team-a to stay open below threshold")
	}
}

func TestCloseIdleTeamStores_AccessBumpsTimestamp(t *testing.T) {
	ledger := openTestStore(t)
	team := openTestStore(t)
	r := NewWhisperRegistry(ledger, nil)
	defer r.Close()

	clock := time.Now()
	r.SetClock(func() time.Time { return clock })
	r.AddTeamStore("team-a", team)

	// 10 minutes pass — under threshold.
	clock = clock.Add(10 * time.Minute)

	// Add a team-scoped entry — should bump lastAccess.
	if err := r.Add("team", whisperstore.WhisperEntry{
		ID: "t-bump", Scope: "team", Type: whisperstore.WhisperTrigger,
		Source: "test", Topic: "lint", Content: "bump",
		Importance: whisperstore.ImportanceNormal, CreatedAt: clock,
	}); err != nil {
		t.Fatalf("Add team: %v", err)
	}

	// 14 more minutes — without the bump this would be 24 min idle. With it, 14.
	clock = clock.Add(14 * time.Minute)
	closed := r.CloseIdleTeamStores(15 * time.Minute)
	if closed != 0 {
		t.Fatalf("Add('team') did not bump lastAccess: %d closed", closed)
	}
}

func TestCloseIdleTeamStores_LedgerNeverClosed(t *testing.T) {
	ledger := openTestStore(t)
	r := NewWhisperRegistry(ledger, nil)
	defer r.Close()

	clock := time.Now().Add(24 * time.Hour)
	r.SetClock(func() time.Time { return clock })

	if closed := r.CloseIdleTeamStores(1 * time.Minute); closed != 0 {
		t.Fatalf("ledger must not be subject to idle-close, got %d", closed)
	}
	if r.LedgerStore() == nil {
		t.Error("ledger store should remain after idle-close pass")
	}
}

func TestTeamIDs(t *testing.T) {
	teamA := openTestStore(t)
	teamB := openTestStore(t)
	r := NewWhisperRegistry(nil, nil)
	r.AddTeamStore("team-a", teamA)
	r.AddTeamStore("team-b", teamB)

	got := r.TeamIDs()
	if len(got) != 2 {
		t.Fatalf("expected 2 team IDs, got %d: %v", len(got), got)
	}

	r.CloseTeamStore("team-a")
	got = r.TeamIDs()
	if len(got) != 1 || got[0] != "team-b" {
		t.Errorf("expected only team-b after close, got %v", got)
	}
}
