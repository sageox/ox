//go:build slow

package daemon

import (
	"testing"
	"time"

	"github.com/sageox/ox/internal/ledger"
	whisperstore "github.com/sageox/ox/internal/whisper/store"
)

func TestWhisperPipeline_MurmurToDelivery(t *testing.T) {

	relay, registry, _ := newTestRelay(t)
	baseDir := t.TempDir()

	now := time.Now().UTC()
	writeMurmurAt(t, baseDir, ledger.MurmurFile{
		ID:         "murmur-build-001",
		Timestamp:  now,
		AgentID:    "agent-A",
		Topic:      "build",
		Importance: "critical",
		Content:    "CI is red",
	})

	count := relay.RelayFromPath(baseDir, "ledger")
	if count != 1 {
		t.Fatalf("expected 1 relayed, got %d", count)
	}

	entries, err := registry.GetWhispers("agent-B", whisperstore.AttentionNormal, nil)
	if err != nil {
		t.Fatalf("get whispers: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}

	e := entries[0]
	if e.Content != "CI is red" {
		t.Errorf("content: got %q, want %q", e.Content, "CI is red")
	}
	if e.Topic != "build" {
		t.Errorf("topic: got %q, want %q", e.Topic, "build")
	}
	if e.Importance != whisperstore.ImportanceCritical {
		t.Errorf("importance: got %q, want %q", e.Importance, whisperstore.ImportanceCritical)
	}
	if e.Source != "murmur" {
		t.Errorf("source: got %q, want %q", e.Source, "murmur")
	}
	if e.Type != whisperstore.WhisperTrigger {
		t.Errorf("type: got %q, want %q", e.Type, whisperstore.WhisperTrigger)
	}
}

func TestWhisperPipeline_CursorAdvancement(t *testing.T) {

	relay, registry, _ := newTestRelay(t)
	baseDir := t.TempDir()

	now := time.Now().UTC()
	writeMurmurAt(t, baseDir, ledger.MurmurFile{
		ID:         "murmur-cursor-001",
		Timestamp:  now,
		AgentID:    "remote-agent",
		Topic:      "lint",
		Importance: "normal",
		Content:    "first murmur",
	})

	relay.RelayFromPath(baseDir, "ledger")

	// first query: should get 1 entry
	entries, err := registry.GetWhispers("agent-B", whisperstore.AttentionAll, nil)
	if err != nil {
		t.Fatalf("first query: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("first query: expected 1, got %d", len(entries))
	}

	// second query: cursor advanced, should get 0
	entries, err = registry.GetWhispers("agent-B", whisperstore.AttentionAll, nil)
	if err != nil {
		t.Fatalf("second query: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("second query: expected 0 (cursor advanced), got %d", len(entries))
	}

	// write a new murmur with a later timestamp
	writeMurmurAt(t, baseDir, ledger.MurmurFile{
		ID:         "murmur-cursor-002",
		Timestamp:  now.Add(time.Second),
		AgentID:    "remote-agent",
		Topic:      "lint",
		Importance: "normal",
		Content:    "second murmur",
	})

	relay.RelayFromPath(baseDir, "ledger")

	// third query: should get only the new entry
	entries, err = registry.GetWhispers("agent-B", whisperstore.AttentionAll, nil)
	if err != nil {
		t.Fatalf("third query: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("third query: expected 1 (only new murmur), got %d", len(entries))
	}
	if entries[0].Content != "second murmur" {
		t.Errorf("expected 'second murmur', got %q", entries[0].Content)
	}
}

func TestWhisperPipeline_SelfAuthoredFiltering(t *testing.T) {

	relay, registry, _ := newTestRelay(t)
	baseDir := t.TempDir()

	now := time.Now().UTC()
	writeMurmurAt(t, baseDir, ledger.MurmurFile{
		ID:         "murmur-self-001",
		Timestamp:  now,
		AgentID:    "agent-A",
		Topic:      "status",
		Importance: "normal",
		Content:    "I finished the task",
	})

	relay.SetLocalAgentIDs([]string{"agent-A"})

	count := relay.RelayFromPath(baseDir, "ledger")
	if count != 0 {
		t.Fatalf("expected 0 relayed (self-authored filtered), got %d", count)
	}

	// agent-A queries: should get 0 because the murmur was never stored
	entries, err := registry.GetWhispers("agent-A", whisperstore.AttentionAll, nil)
	if err != nil {
		t.Fatalf("get whispers: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected 0 entries (self-authored never entered store), got %d", len(entries))
	}
}

func TestWhisperPipeline_CrossAgentDelivery(t *testing.T) {

	relay, registry, _ := newTestRelay(t)
	baseDir := t.TempDir()

	now := time.Now().UTC()
	writeMurmurAt(t, baseDir, ledger.MurmurFile{
		ID:         "murmur-cross-001",
		Timestamp:  now,
		AgentID:    "agent-A",
		Topic:      "deploy",
		Importance: "normal",
		Content:    "deployment started",
	})

	relay.RelayFromPath(baseDir, "ledger")

	// agent-B queries: should get 1
	entriesB, err := registry.GetWhispers("agent-B", whisperstore.AttentionAll, nil)
	if err != nil {
		t.Fatalf("agent-B first query: %v", err)
	}
	if len(entriesB) != 1 {
		t.Fatalf("agent-B first query: expected 1, got %d", len(entriesB))
	}

	// agent-C queries: should also get 1 (independent delivery)
	entriesC, err := registry.GetWhispers("agent-C", whisperstore.AttentionAll, nil)
	if err != nil {
		t.Fatalf("agent-C first query: %v", err)
	}
	if len(entriesC) != 1 {
		t.Fatalf("agent-C first query: expected 1, got %d", len(entriesC))
	}

	// both agents received the same entry
	if entriesB[0].ID != entriesC[0].ID {
		t.Errorf("agent-B and agent-C should receive the same entry: B=%s, C=%s", entriesB[0].ID, entriesC[0].ID)
	}
	if entriesB[0].Content != "deployment started" {
		t.Errorf("agent-B content: got %q, want %q", entriesB[0].Content, "deployment started")
	}
	if entriesC[0].Content != "deployment started" {
		t.Errorf("agent-C content: got %q, want %q", entriesC[0].Content, "deployment started")
	}
}

func TestWhisperPipeline_AttentionFiltering(t *testing.T) {

	baseDir := t.TempDir()
	now := time.Now().UTC()

	// write 3 murmurs with different importance levels
	writeMurmurAt(t, baseDir, ledger.MurmurFile{
		ID:         "murmur-attn-critical",
		Timestamp:  now,
		AgentID:    "remote-agent",
		Topic:      "build",
		Importance: "critical",
		Content:    "critical issue",
	})
	writeMurmurAt(t, baseDir, ledger.MurmurFile{
		ID:         "murmur-attn-normal",
		Timestamp:  now.Add(time.Millisecond),
		AgentID:    "remote-agent",
		Topic:      "lint",
		Importance: "normal",
		Content:    "normal issue",
	})
	writeMurmurAt(t, baseDir, ledger.MurmurFile{
		ID:         "murmur-attn-ambient",
		Timestamp:  now.Add(2 * time.Millisecond),
		AgentID:    "remote-agent",
		Topic:      "status",
		Importance: "ambient",
		Content:    "ambient update",
	})

	// test AttentionFocused: only critical
	t.Run("focused", func(t *testing.T) {
		relay, registry, _ := newTestRelay(t)
		relay.RelayFromPath(baseDir, "ledger")

		entries, err := registry.GetWhispers("focused-agent", whisperstore.AttentionFocused, nil)
		if err != nil {
			t.Fatalf("get whispers: %v", err)
		}
		if len(entries) != 1 {
			t.Fatalf("expected 1 (critical only), got %d", len(entries))
		}
		if entries[0].Importance != whisperstore.ImportanceCritical {
			t.Errorf("expected critical, got %s", entries[0].Importance)
		}
	})

	// test AttentionNormal: critical + normal
	t.Run("normal", func(t *testing.T) {
		relay, registry, _ := newTestRelay(t)
		relay.RelayFromPath(baseDir, "ledger")

		entries, err := registry.GetWhispers("normal-agent", whisperstore.AttentionNormal, nil)
		if err != nil {
			t.Fatalf("get whispers: %v", err)
		}
		if len(entries) != 2 {
			t.Fatalf("expected 2 (critical + normal), got %d", len(entries))
		}
	})

	// test AttentionAll: all 3
	t.Run("all", func(t *testing.T) {
		relay, registry, _ := newTestRelay(t)
		relay.RelayFromPath(baseDir, "ledger")

		entries, err := registry.GetWhispers("all-agent", whisperstore.AttentionAll, nil)
		if err != nil {
			t.Fatalf("get whispers: %v", err)
		}
		if len(entries) != 3 {
			t.Fatalf("expected 3 (all), got %d", len(entries))
		}
	})
}

func TestWhisperPipeline_TeamScope(t *testing.T) {

	ledgerStore := openTestStore(t)
	teamStore := openTestStore(t)

	registry := NewWhisperRegistry(ledgerStore, nil)
	registry.AddTeamStore("team-1", teamStore)
	t.Cleanup(func() { registry.Close() })

	relay := NewMurmurRelay(registry, initMurmuringProject(t), nil)

	now := time.Now().UTC()

	// write murmur to "team" path
	teamDir := t.TempDir()
	writeMurmurAt(t, teamDir, ledger.MurmurFile{
		ID:         "murmur-team-001",
		Timestamp:  now,
		AgentID:    "remote-agent",
		Topic:      "architecture",
		Importance: "normal",
		Content:    "team murmur",
	})

	count := relay.RelayFromPath(teamDir, "team")
	if count != 1 {
		t.Fatalf("expected 1 relayed to team, got %d", count)
	}

	entries, err := registry.GetWhispers("querying-agent", whisperstore.AttentionAll, nil)
	if err != nil {
		t.Fatalf("get whispers: %v", err)
	}

	// find the team-scoped entry
	var teamEntry *whisperstore.WhisperEntry
	for i := range entries {
		if entries[i].ID == "murmur-team-001" {
			teamEntry = &entries[i]
			break
		}
	}
	if teamEntry == nil {
		t.Fatal("expected team entry, not found")
	}
	if teamEntry.Scope != "team" {
		t.Errorf("expected scope 'team', got %q", teamEntry.Scope)
	}

	// write murmur to "ledger" path
	ledgerDir := t.TempDir()
	writeMurmurAt(t, ledgerDir, ledger.MurmurFile{
		ID:         "murmur-ledger-001",
		Timestamp:  now.Add(time.Second),
		AgentID:    "remote-agent",
		Topic:      "lint",
		Importance: "normal",
		Content:    "ledger murmur",
	})

	count = relay.RelayFromPath(ledgerDir, "ledger")
	if count != 1 {
		t.Fatalf("expected 1 relayed to ledger, got %d", count)
	}

	// use a different agent ID so cursor doesn't interfere
	entries, err = registry.GetWhispers("querying-agent-2", whisperstore.AttentionAll, nil)
	if err != nil {
		t.Fatalf("get whispers after ledger relay: %v", err)
	}

	// find the ledger-scoped entry
	var ledgerEntry *whisperstore.WhisperEntry
	for i := range entries {
		if entries[i].ID == "murmur-ledger-001" {
			ledgerEntry = &entries[i]
			break
		}
	}
	if ledgerEntry == nil {
		t.Fatal("expected ledger entry, not found")
	}
	if ledgerEntry.Scope != "ledger" {
		t.Errorf("expected scope 'ledger', got %q", ledgerEntry.Scope)
	}
}

func TestWhisperPipeline_MultipleMurmursBatch(t *testing.T) {

	relay, registry, _ := newTestRelay(t)
	baseDir := t.TempDir()

	now := time.Now().UTC()

	// write 5 murmurs from different remote agents with varying importance
	murmurs := []ledger.MurmurFile{
		{ID: "batch-001", Timestamp: now, AgentID: "agent-1", Topic: "build", Importance: "critical", Content: "critical from agent-1"},
		{ID: "batch-002", Timestamp: now.Add(time.Millisecond), AgentID: "agent-2", Topic: "lint", Importance: "normal", Content: "normal from agent-2"},
		{ID: "batch-003", Timestamp: now.Add(2 * time.Millisecond), AgentID: "agent-3", Topic: "test", Importance: "ambient", Content: "ambient from agent-3"},
		{ID: "batch-004", Timestamp: now.Add(3 * time.Millisecond), AgentID: "agent-4", Topic: "deploy", Importance: "critical", Content: "critical from agent-4"},
		{ID: "batch-005", Timestamp: now.Add(4 * time.Millisecond), AgentID: "agent-5", Topic: "review", Importance: "normal", Content: "normal from agent-5"},
	}

	for _, m := range murmurs {
		writeMurmurAt(t, baseDir, m)
	}

	count := relay.RelayFromPath(baseDir, "ledger")
	if count != 5 {
		t.Fatalf("expected 5 relayed, got %d", count)
	}

	entries, err := registry.GetWhispers("batch-reader", whisperstore.AttentionAll, nil)
	if err != nil {
		t.Fatalf("get whispers: %v", err)
	}
	if len(entries) != 5 {
		t.Fatalf("expected 5 entries, got %d", len(entries))
	}

	// verify ordering: critical entries should come before non-critical
	// (the store sorts by importance then time)
	foundCriticalEnd := false
	for i, e := range entries {
		if e.Importance != whisperstore.ImportanceCritical && !foundCriticalEnd {
			foundCriticalEnd = true
			// all entries before this should have been critical
			for j := 0; j < i; j++ {
				if entries[j].Importance != whisperstore.ImportanceCritical {
					t.Errorf("entry %d: expected critical before non-critical, got %s", j, entries[j].Importance)
				}
			}
		}
	}

	// verify all 5 IDs are present
	idSet := make(map[string]bool)
	for _, e := range entries {
		idSet[e.ID] = true
	}
	for _, m := range murmurs {
		if !idSet[m.ID] {
			t.Errorf("missing entry for murmur %s", m.ID)
		}
	}

	// verify each entry has correct source and type
	for i, e := range entries {
		if e.Source != "murmur" {
			t.Errorf("entry %d: source got %q, want %q", i, e.Source, "murmur")
		}
		if e.Type != whisperstore.WhisperTrigger {
			t.Errorf("entry %d: type got %q, want %q", i, e.Type, whisperstore.WhisperTrigger)
		}
	}

	// verify different agent IDs are preserved
	agentSet := make(map[string]bool)
	for _, e := range entries {
		agentSet[e.AgentID] = true
	}
	if len(agentSet) != 5 {
		t.Errorf("expected 5 distinct agent IDs, got %d", len(agentSet))
	}

}
