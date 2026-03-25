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
