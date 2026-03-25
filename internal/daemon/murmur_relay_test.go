package daemon

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/ledger"
	whisperstore "github.com/sageox/ox/internal/whisper/store"
)

// writeMurmurAt writes a murmur file into the correct hourly partition directory.
func writeMurmurAt(t *testing.T, baseDir string, m ledger.MurmurFile) {
	t.Helper()
	_, err := ledger.WriteMurmur(baseDir, m)
	if err != nil {
		t.Fatalf("write murmur: %v", err)
	}
}

func newTestRelay(t *testing.T) (*MurmurRelay, *WhisperRegistry, *whisperstore.Store) {
	t.Helper()
	store := openTestStore(t)
	registry := NewWhisperRegistry(store, nil)
	relay := NewMurmurRelay(registry, nil)
	t.Cleanup(func() { registry.Close() })
	return relay, registry, store
}

func TestMurmurRelayNewMurmurDetected(t *testing.T) {
	relay, registry, _ := newTestRelay(t)
	baseDir := t.TempDir()

	now := time.Now().UTC()
	writeMurmurAt(t, baseDir, ledger.MurmurFile{
		ID:         "murmur-001",
		Timestamp:  now,
		AgentID:    "remote-agent",
		Topic:      "lint",
		Importance: "normal",
		Content:    "fix the linter warnings",
	})

	count := relay.RelayFromPath(baseDir, "ledger")
	if count != 1 {
		t.Fatalf("expected 1 relayed, got %d", count)
	}

	entries, err := registry.GetWhispers("test-agent", whisperstore.AttentionAll, nil)
	if err != nil {
		t.Fatalf("get whispers: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 whisper entry, got %d", len(entries))
	}
	if entries[0].ID != "murmur-001" {
		t.Errorf("expected ID murmur-001, got %s", entries[0].ID)
	}
	if entries[0].Source != "murmur" {
		t.Errorf("expected source murmur, got %s", entries[0].Source)
	}
}

func TestMurmurRelaySelfAuthoredFiltered(t *testing.T) {
	relay, registry, _ := newTestRelay(t)
	baseDir := t.TempDir()

	now := time.Now().UTC()
	writeMurmurAt(t, baseDir, ledger.MurmurFile{
		ID:         "murmur-self",
		Timestamp:  now,
		AgentID:    "local-agent-1",
		Topic:      "status",
		Importance: "normal",
		Content:    "I finished the task",
	})

	relay.SetLocalAgentIDs([]string{"local-agent-1"})

	count := relay.RelayFromPath(baseDir, "ledger")
	if count != 0 {
		t.Fatalf("expected 0 relayed (self-authored), got %d", count)
	}

	entries, err := registry.GetWhispers("test-agent", whisperstore.AttentionAll, nil)
	if err != nil {
		t.Fatalf("get whispers: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected 0 whisper entries, got %d", len(entries))
	}
}

func TestMurmurRelayEmptyAgentIDRelayed(t *testing.T) {
	relay, registry, _ := newTestRelay(t)
	baseDir := t.TempDir()

	now := time.Now().UTC()
	writeMurmurAt(t, baseDir, ledger.MurmurFile{
		ID:         "murmur-no-agent",
		Timestamp:  now,
		AgentID:    "", // empty AgentID bypasses self-filter
		Topic:      "announcement",
		Importance: "normal",
		Content:    "system-level murmur with no agent",
	})

	// even with local agent IDs set, empty AgentID murmurs should be relayed
	relay.SetLocalAgentIDs([]string{"local-agent-1", "local-agent-2"})

	count := relay.RelayFromPath(baseDir, "ledger")
	if count != 1 {
		t.Fatalf("expected 1 relayed (empty AgentID bypasses self-filter), got %d", count)
	}

	entries, err := registry.GetWhispers("test-agent", whisperstore.AttentionAll, nil)
	if err != nil {
		t.Fatalf("get whispers: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 whisper entry, got %d", len(entries))
	}
	if entries[0].AgentID != "" {
		t.Errorf("expected empty AgentID, got %s", entries[0].AgentID)
	}
}

func TestMurmurRelayDedupAcrossCalls(t *testing.T) {
	relay, _, _ := newTestRelay(t)
	baseDir := t.TempDir()

	now := time.Now().UTC()
	writeMurmurAt(t, baseDir, ledger.MurmurFile{
		ID:         "murmur-dup",
		Timestamp:  now,
		AgentID:    "remote-agent",
		Topic:      "build",
		Importance: "critical",
		Content:    "build failed",
	})

	count1 := relay.RelayFromPath(baseDir, "ledger")
	count2 := relay.RelayFromPath(baseDir, "ledger")

	if count1 != 1 {
		t.Fatalf("first relay: expected 1, got %d", count1)
	}
	if count2 != 0 {
		t.Fatalf("second relay: expected 0 (dedup), got %d", count2)
	}
}

func TestMurmurRelayRestartResilience(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "whisper.db")
	baseDir := t.TempDir()

	now := time.Now().UTC()
	writeMurmurAt(t, baseDir, ledger.MurmurFile{
		ID:         "murmur-persist",
		Timestamp:  now,
		AgentID:    "remote-agent",
		Topic:      "deploy",
		Importance: "normal",
		Content:    "deployment started",
	})

	// first "session": relay the murmur, then close store
	{
		store, err := whisperstore.Open(dbPath)
		if err != nil {
			t.Fatalf("open store: %v", err)
		}
		registry := NewWhisperRegistry(store, nil)
		relay := NewMurmurRelay(registry, nil)

		count := relay.RelayFromPath(baseDir, "ledger")
		if count != 1 {
			t.Fatalf("first session: expected 1, got %d", count)
		}

		registry.Close()
	}

	// second "session": reopen and verify dedup state survived
	{
		store, err := whisperstore.Open(dbPath)
		if err != nil {
			t.Fatalf("reopen store: %v", err)
		}
		registry := NewWhisperRegistry(store, nil)
		relay := NewMurmurRelay(registry, nil)

		count := relay.RelayFromPath(baseDir, "ledger")
		if count != 0 {
			t.Fatalf("second session: expected 0 (persisted dedup), got %d", count)
		}

		registry.Close()
	}
}

func TestMurmurRelayInvalidJSONSkipped(t *testing.T) {
	relay, _, _ := newTestRelay(t)
	baseDir := t.TempDir()

	now := time.Now().UTC()
	writeMurmurAt(t, baseDir, ledger.MurmurFile{
		ID:         "murmur-valid",
		Timestamp:  now,
		AgentID:    "remote-agent",
		Topic:      "test",
		Importance: "normal",
		Content:    "valid murmur",
	})

	// write invalid JSON into the same hourly directory
	hourDir := filepath.Join(baseDir, ledger.MurmurDateHourDir(now))
	invalidPath := filepath.Join(hourDir, "not-json.json")
	if err := os.WriteFile(invalidPath, []byte("this is not json {{{"), 0o644); err != nil {
		t.Fatalf("write invalid file: %v", err)
	}

	// should relay the valid one and skip the invalid one without error
	count := relay.RelayFromPath(baseDir, "ledger")
	if count != 1 {
		t.Fatalf("expected 1 relayed (invalid skipped), got %d", count)
	}
}

func TestMurmurRelayEmptyDirectory(t *testing.T) {
	relay, _, _ := newTestRelay(t)
	baseDir := t.TempDir()

	count := relay.RelayFromPath(baseDir, "ledger")
	if count != 0 {
		t.Fatalf("expected 0 for empty dir, got %d", count)
	}
}

func TestMurmurRelayScopeCorrectlyTagged(t *testing.T) {
	baseDir := t.TempDir()
	now := time.Now().UTC()

	writeMurmurAt(t, baseDir, ledger.MurmurFile{
		ID:         "murmur-scope",
		Timestamp:  now,
		AgentID:    "remote-agent",
		Topic:      "architecture",
		Importance: "normal",
		Content:    "use event sourcing",
	})

	tests := []struct {
		name  string
		scope string
	}{
		{"ledger scope", "ledger"},
		{"team scope", "team"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			relay, registry, _ := newTestRelay(t)

			// team scope requires a registered team store for writes to land
			if tt.scope == "team" {
				teamStore := openTestStore(t)
				registry.AddTeamStore("team-1", teamStore)
			}

			count := relay.RelayFromPath(baseDir, tt.scope)
			if count != 1 {
				t.Fatalf("expected 1 relayed, got %d", count)
			}

			entries, err := registry.GetWhispers("test-agent", whisperstore.AttentionAll, nil)
			if err != nil {
				t.Fatalf("get whispers: %v", err)
			}
			if len(entries) != 1 {
				t.Fatalf("expected 1 entry, got %d", len(entries))
			}
			if entries[0].Scope != tt.scope {
				t.Errorf("expected scope %s, got %s", tt.scope, entries[0].Scope)
			}
		})
	}
}

func TestMurmurRelayMultipleMurmursInWindow(t *testing.T) {
	relay, registry, _ := newTestRelay(t)
	baseDir := t.TempDir()

	now := time.Now().UTC()

	// write murmurs across different hours within the window
	for i, id := range []string{"m1", "m2", "m3"} {
		writeMurmurAt(t, baseDir, ledger.MurmurFile{
			ID:         id,
			Timestamp:  now.Add(-time.Duration(i) * time.Hour),
			AgentID:    "remote-agent",
			Topic:      "test",
			Importance: "normal",
			Content:    "murmur " + id,
		})
	}

	count := relay.RelayFromPath(baseDir, "ledger")
	if count != 3 {
		t.Fatalf("expected 3 relayed, got %d", count)
	}

	entries, err := registry.GetWhispers("test-agent", whisperstore.AttentionAll, nil)
	if err != nil {
		t.Fatalf("get whispers: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 whisper entries, got %d", len(entries))
	}
}

func TestMurmurRelayContentAndMetadataPreserved(t *testing.T) {
	relay, registry, _ := newTestRelay(t)
	baseDir := t.TempDir()

	now := time.Now().UTC()
	writeMurmurAt(t, baseDir, ledger.MurmurFile{
		ID:            "murmur-meta",
		Timestamp:     now,
		AgentID:       "agent-42",
		AgentType:     "claude-code",
		PrincipalID:   "user-99",
		PrincipalType: "human",
		Topic:         "security",
		Importance:    "critical",
		Content:       "rotate the API keys immediately",
		Metadata: map[string]string{
			"source_file": "config.yaml",
			"severity":    "high",
		},
	})

	count := relay.RelayFromPath(baseDir, "ledger")
	if count != 1 {
		t.Fatalf("expected 1 relayed, got %d", count)
	}

	entries, err := registry.GetWhispers("test-agent", whisperstore.AttentionAll, nil)
	if err != nil {
		t.Fatalf("get whispers: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}

	e := entries[0]
	if e.Content != "rotate the API keys immediately" {
		t.Errorf("content mismatch: %s", e.Content)
	}
	if e.Topic != "security" {
		t.Errorf("topic mismatch: %s", e.Topic)
	}
	if e.Importance != whisperstore.ImportanceCritical {
		t.Errorf("importance mismatch: %s", e.Importance)
	}
	if e.AgentID != "agent-42" {
		t.Errorf("agent_id mismatch: %s", e.AgentID)
	}
	if e.PrincipalID != "user-99" {
		t.Errorf("principal_id mismatch: %s", e.PrincipalID)
	}
	if e.PrincipalType != "human" {
		t.Errorf("principal_type mismatch: %s", e.PrincipalType)
	}
	if e.Source != "murmur" {
		t.Errorf("source mismatch: %s", e.Source)
	}
	if e.Type != whisperstore.WhisperTrigger {
		t.Errorf("type mismatch: %s", e.Type)
	}
	if len(e.Metadata) != 2 {
		t.Fatalf("expected 2 metadata entries, got %d", len(e.Metadata))
	}
	if e.Metadata["source_file"] != "config.yaml" {
		t.Errorf("metadata source_file mismatch: %s", e.Metadata["source_file"])
	}
	if e.Metadata["severity"] != "high" {
		t.Errorf("metadata severity mismatch: %s", e.Metadata["severity"])
	}
}
