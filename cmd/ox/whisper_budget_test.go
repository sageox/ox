package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	whisperstore "github.com/sageox/ox/internal/whisper/store"
)

// --- A. Basic pass-through and per-agent limits ---

func TestCapMurmurWhispers_NonMurmurPassThrough(t *testing.T) {
	entries := []whisperstore.WhisperEntry{
		{ID: "1", Source: "activity-summary", Content: "CI green"},
		{ID: "2", Source: "team-context", Content: "SOUL.md updated"},
	}

	result := capMurmurWhispers(entries)
	if len(result) != 2 {
		t.Errorf("non-murmur entries should pass through unchanged, got %d", len(result))
	}
}

func TestCapMurmurWhispers_LimitsPerAgent(t *testing.T) {
	now := time.Now()
	entries := []whisperstore.WhisperEntry{
		{ID: "1", Source: "murmur", AgentID: "OxA", Content: "msg1", CreatedAt: now},
		{ID: "2", Source: "murmur", AgentID: "OxA", Content: "msg2", CreatedAt: now.Add(-1 * time.Minute)},
		{ID: "3", Source: "murmur", AgentID: "OxA", Content: "msg3", CreatedAt: now.Add(-2 * time.Minute)},
	}

	result := capMurmurWhispers(entries)

	murmurCount := 0
	for _, e := range result {
		if e.Source == "murmur" {
			murmurCount++
		}
	}
	// per-agent cap: only the most recent murmur from each agent
	if murmurCount != 1 {
		t.Errorf("expected 1 murmur per agent, got %d", murmurCount)
	}
}

func TestCapMurmurWhispers_MultipleAgents(t *testing.T) {
	now := time.Now()
	entries := []whisperstore.WhisperEntry{
		{ID: "1", Source: "murmur", AgentID: "OxA", Content: "from A", CreatedAt: now},
		{ID: "2", Source: "murmur", AgentID: "OxB", Content: "from B", CreatedAt: now.Add(-1 * time.Minute)},
		{ID: "3", Source: "murmur", AgentID: "OxC", Content: "from C", CreatedAt: now.Add(-2 * time.Minute)},
	}

	result := capMurmurWhispers(entries)

	murmurCount := 0
	for _, e := range result {
		if e.Source == "murmur" {
			murmurCount++
		}
	}
	if murmurCount != 3 {
		t.Errorf("expected 1 murmur per agent (3 agents), got %d", murmurCount)
	}
}

// --- B. Token budget ---

func TestCapMurmurWhispers_TokenBudget(t *testing.T) {
	now := time.Now()
	// create many agents with content that exceeds the budget
	var entries []whisperstore.WhisperEntry
	for i := 0; i < 20; i++ {
		entries = append(entries, whisperstore.WhisperEntry{
			ID:        fmt.Sprintf("m-%d", i),
			Source:    "murmur",
			AgentID:   fmt.Sprintf("Ox%d", i),
			Content:   strings.Repeat("x", 500),
			CreatedAt: now.Add(-time.Duration(i) * time.Minute),
		})
	}

	result := capMurmurWhispers(entries)

	totalBytes := 0
	murmurCount := 0
	for _, e := range result {
		if e.Source == "murmur" {
			totalBytes += len(e.Content)
			murmurCount++
		}
	}
	// should be capped, not all 20
	if murmurCount >= 20 {
		t.Error("token budget should limit number of murmurs")
	}
	if murmurCount == 0 {
		t.Error("at least one murmur should survive budget capping")
	}
}

func TestCapMurmurWhispers_AlwaysKeepsAtLeastOne(t *testing.T) {
	now := time.Now()
	// single entry larger than any reasonable budget
	entries := []whisperstore.WhisperEntry{
		{ID: "big", Source: "murmur", AgentID: "OxA", Content: strings.Repeat("x", 10000), CreatedAt: now},
	}

	result := capMurmurWhispers(entries)
	found := false
	for _, e := range result {
		if e.ID == "big" {
			found = true
		}
	}
	if !found {
		t.Error("at-least-one guarantee: single oversized entry must be kept")
	}
}

func TestCapMurmurWhispers_MixedSources(t *testing.T) {
	now := time.Now()
	entries := []whisperstore.WhisperEntry{
		{ID: "non-1", Source: "activity-summary", Content: "CI green"},
		{ID: "m-1", Source: "murmur", AgentID: "OxA", Content: "work", CreatedAt: now},
		{ID: "non-2", Source: "team-context", Content: "update"},
		{ID: "m-2", Source: "murmur", AgentID: "OxB", Content: "more work", CreatedAt: now.Add(-1 * time.Minute)},
	}

	result := capMurmurWhispers(entries)

	var nonMurmur, murmur int
	for _, e := range result {
		if e.Source == "murmur" {
			murmur++
		} else {
			nonMurmur++
		}
	}
	if nonMurmur != 2 {
		t.Errorf("non-murmur entries should pass through, got %d", nonMurmur)
	}
	if murmur != 2 {
		t.Errorf("both murmurs fit in budget, got %d", murmur)
	}
}

// --- C. Deduplication ---

func TestDeduplicateBySourceTopic(t *testing.T) {
	now := time.Now()
	entries := []whisperstore.WhisperEntry{
		{ID: "1", Source: "auto-murmur", Topic: "nudge", Content: "first", CreatedAt: now.Add(-2 * time.Second)},
		{ID: "2", Source: "auto-murmur", Topic: "nudge", Content: "second", CreatedAt: now.Add(-1 * time.Second)},
		{ID: "3", Source: "auto-murmur", Topic: "nudge", Content: "third", CreatedAt: now},
		{ID: "4", Source: "activity-summary", Topic: "activity", Content: "summary", CreatedAt: now},
	}

	result := deduplicateBySourceTopic(entries)

	if len(result) != 2 {
		t.Fatalf("expected 2 deduplicated entries, got %d", len(result))
	}

	// the nudge should be the newest
	for _, e := range result {
		if e.Source == "auto-murmur" && e.ID != "3" {
			t.Errorf("expected newest nudge (ID=3), got ID=%s", e.ID)
		}
	}
}

func TestCapMurmurWhispers_DeduplicatesNudges(t *testing.T) {
	now := time.Now()
	entries := []whisperstore.WhisperEntry{
		{ID: "nudge-1", Source: "auto-murmur", Topic: "murmur-nudge", Content: "tell your team", CreatedAt: now.Add(-2 * time.Second)},
		{ID: "nudge-2", Source: "auto-murmur", Topic: "murmur-nudge", Content: "tell your team", CreatedAt: now.Add(-1 * time.Second)},
		{ID: "nudge-3", Source: "auto-murmur", Topic: "murmur-nudge", Content: "tell your team", CreatedAt: now},
		{ID: "real-1", Source: "murmur", AgentID: "OxA", Topic: "wip", Content: "working", CreatedAt: now},
	}

	result := capMurmurWhispers(entries)

	nudgeCount := 0
	for _, e := range result {
		if e.Source == "auto-murmur" {
			nudgeCount++
		}
	}
	if nudgeCount != 1 {
		t.Errorf("expected 1 deduplicated nudge, got %d", nudgeCount)
	}
}

func TestCapMurmurWhispers_Empty(t *testing.T) {
	result := capMurmurWhispers(nil)
	if result != nil {
		t.Errorf("nil input should return nil, got %v", result)
	}

	result = capMurmurWhispers([]whisperstore.WhisperEntry{})
	if result != nil {
		t.Errorf("empty input should return nil, got %v", result)
	}
}

func TestCapMurmurWhispers_AnonymousMurmurs(t *testing.T) {
	now := time.Now()
	entries := []whisperstore.WhisperEntry{
		{ID: "anon-1", Source: "murmur", Content: "anonymous 1", CreatedAt: now},
		{ID: "anon-2", Source: "murmur", Content: "anonymous 2", CreatedAt: now.Add(-1 * time.Minute)},
	}

	result := capMurmurWhispers(entries)

	murmurs := 0
	for _, e := range result {
		if e.Source == "murmur" {
			murmurs++
		}
	}
	// anonymous murmurs share the same per-agent key, so only 1 kept
	if murmurs != 1 {
		t.Errorf("anonymous murmurs should be capped together, got %d", murmurs)
	}
}

// --- D. Ordering and fairness ---

func TestCapMurmurWhispers_FairnessAcrossAgents(t *testing.T) {
	content := strings.Repeat("y", 2000)
	agents := []string{"OxA", "OxB", "OxC", "OxD", "OxE"}

	var entries []whisperstore.WhisperEntry
	for i, agent := range agents {
		entries = append(entries, whisperstore.WhisperEntry{
			ID: fmt.Sprintf("m-%s", agent), Source: "murmur", AgentID: agent,
			Content: content, CreatedAt: time.Now().Add(-time.Duration(i) * time.Minute),
		})
	}

	agentSeen := make(map[string]int)
	for i := 0; i < 200; i++ {
		for _, e := range capMurmurWhispers(entries) {
			if e.Source == "murmur" {
				agentSeen[e.AgentID]++
			}
		}
	}

	for _, agent := range agents {
		if agentSeen[agent] == 0 {
			t.Errorf("agent %s was never included — random sampling is unfair", agent)
		}
	}
}

func TestCapMurmurWhispers_SortsByTimeNewestFirst(t *testing.T) {
	now := time.Now()
	entries := []whisperstore.WhisperEntry{
		{ID: "old", Source: "murmur", AgentID: "OxA", Content: "old", CreatedAt: now.Add(-10 * time.Minute)},
		{ID: "new", Source: "murmur", AgentID: "OxB", Content: "new", CreatedAt: now},
		{ID: "mid", Source: "murmur", AgentID: "OxC", Content: "mid", CreatedAt: now.Add(-5 * time.Minute)},
	}

	result := capMurmurWhispers(entries)

	var murmurs []whisperstore.WhisperEntry
	for _, e := range result {
		if e.Source == "murmur" {
			murmurs = append(murmurs, e)
		}
	}
	if len(murmurs) != 3 {
		t.Fatalf("expected 3 murmurs, got %d", len(murmurs))
	}
	if murmurs[0].ID != "new" {
		t.Errorf("expected newest first, got %s", murmurs[0].ID)
	}
	if murmurs[2].ID != "old" {
		t.Errorf("expected oldest last, got %s", murmurs[2].ID)
	}
}

func TestCapMurmurWhispers_DropsOlderThan24h(t *testing.T) {
	now := time.Now()
	entries := []whisperstore.WhisperEntry{
		{ID: "recent", Source: "murmur", AgentID: "OxA", Content: "fresh", CreatedAt: now.Add(-1 * time.Hour)},
		{ID: "stale", Source: "murmur", AgentID: "OxB", Content: "old", CreatedAt: now.Add(-25 * time.Hour)},
		{ID: "nudge", Source: "auto-murmur", Content: "nudge", CreatedAt: now.Add(-25 * time.Hour)},
	}

	result := capMurmurWhispers(entries)

	var murmurs, nonMurmurs int
	for _, e := range result {
		if e.Source == "murmur" {
			murmurs++
			if e.ID == "stale" {
				t.Error("stale murmur (>24h) should have been dropped")
			}
		} else {
			nonMurmurs++
		}
	}
	if murmurs != 1 {
		t.Errorf("expected 1 murmur (only recent), got %d", murmurs)
	}
	if nonMurmurs != 1 {
		t.Errorf("expected 1 non-murmur (pass through), got %d", nonMurmurs)
	}
}

// --- E. Critical murmur budget priority ---
// These tests verify that high-value signals survive budget pressure.

// TestCapMurmurWhispers_CriticalSurvivesBudgetPressure verifies that a critical
// murmur is never dropped when the budget forces sampling among entries.
// Failure prevented: golden nugget randomly dropped in favor of ambient noise.
func TestCapMurmurWhispers_CriticalSurvivesBudgetPressure(t *testing.T) {
	t.Parallel()

	now := time.Now()
	bigContent := strings.Repeat("x", 3000)

	entries := []whisperstore.WhisperEntry{
		{ID: "critical-signal", Source: "murmur", AgentID: "OxA", Content: bigContent, Importance: whisperstore.ImportanceCritical, CreatedAt: now},
		{ID: "normal-1", Source: "murmur", AgentID: "OxB", Content: bigContent, Importance: whisperstore.ImportanceNormal, CreatedAt: now.Add(-1 * time.Minute)},
		{ID: "normal-2", Source: "murmur", AgentID: "OxC", Content: bigContent, Importance: whisperstore.ImportanceNormal, CreatedAt: now.Add(-2 * time.Minute)},
	}

	for i := 0; i < 50; i++ {
		result := capMurmurWhispers(entries)
		found := false
		for _, e := range result {
			if e.ID == "critical-signal" {
				found = true
			}
		}
		if !found {
			t.Fatal("critical murmur was dropped during budget sampling — critical entries must always survive")
		}
	}
}

// TestCapMurmurWhispers_MultipleCriticalAllSurvive verifies that when multiple
// critical entries exist, ALL are kept even if they exceed the budget.
// Failure prevented: only the first critical entry survives, others get sampled out.
func TestCapMurmurWhispers_MultipleCriticalAllSurvive(t *testing.T) {
	t.Parallel()

	now := time.Now()
	bigContent := strings.Repeat("x", 3000)

	entries := []whisperstore.WhisperEntry{
		{ID: "crit-1", Source: "murmur", AgentID: "OxA", Content: bigContent, Importance: whisperstore.ImportanceCritical, CreatedAt: now},
		{ID: "crit-2", Source: "murmur", AgentID: "OxB", Content: bigContent, Importance: whisperstore.ImportanceCritical, CreatedAt: now.Add(-1 * time.Minute)},
		{ID: "normal", Source: "murmur", AgentID: "OxC", Content: bigContent, Importance: whisperstore.ImportanceNormal, CreatedAt: now.Add(-2 * time.Minute)},
	}

	result := capMurmurWhispers(entries)
	critIDs := make(map[string]bool)
	for _, e := range result {
		if e.Importance == whisperstore.ImportanceCritical {
			critIDs[e.ID] = true
		}
	}
	if !critIDs["crit-1"] || !critIDs["crit-2"] {
		t.Errorf("all critical entries must survive budget pressure, got: %v", critIDs)
	}
}

// TestCapMurmurWhispers_NormalStillSampledFairly verifies that among non-critical
// entries, random sampling still provides fairness across agents over time.
// Failure prevented: critical-first logic accidentally breaks fairness for normal entries.
func TestCapMurmurWhispers_NormalStillSampledFairly(t *testing.T) {
	t.Parallel()

	now := time.Now()
	entries := []whisperstore.WhisperEntry{
		{ID: "crit", Source: "murmur", AgentID: "OxCrit", Content: "short", Importance: whisperstore.ImportanceCritical, CreatedAt: now},
	}
	for i := 0; i < 5; i++ {
		entries = append(entries, whisperstore.WhisperEntry{
			ID: fmt.Sprintf("norm-%d", i), Source: "murmur", AgentID: fmt.Sprintf("Ox%d", i),
			Content: strings.Repeat("y", 2000), Importance: whisperstore.ImportanceNormal,
			CreatedAt: now.Add(-time.Duration(i+1) * time.Minute),
		})
	}

	seen := make(map[string]int)
	for i := 0; i < 200; i++ {
		for _, e := range capMurmurWhispers(entries) {
			if e.Source == "murmur" {
				seen[e.ID]++
			}
		}
	}

	if seen["crit"] != 200 {
		t.Errorf("critical entry should appear in all 200 iterations, appeared %d times", seen["crit"])
	}
	for i := 0; i < 5; i++ {
		id := fmt.Sprintf("norm-%d", i)
		if seen[id] == 0 {
			t.Errorf("normal entry %s never appeared — fairness broken among non-critical entries", id)
		}
	}
}

// TestCapMurmurWhispers_SingleCriticalExceedsBudget verifies the at-least-one
// guarantee applies to critical entries even when a single entry is oversized.
// Failure prevented: oversized critical entry dropped entirely, agent sees nothing.
func TestCapMurmurWhispers_SingleCriticalExceedsBudget(t *testing.T) {
	t.Parallel()

	now := time.Now()
	hugeContent := strings.Repeat("z", 10000)

	entries := []whisperstore.WhisperEntry{
		{ID: "huge-crit", Source: "murmur", AgentID: "OxA", Content: hugeContent, Importance: whisperstore.ImportanceCritical, CreatedAt: now},
	}

	result := capMurmurWhispers(entries)
	found := false
	for _, e := range result {
		if e.ID == "huge-crit" {
			found = true
		}
	}
	if !found {
		t.Error("single oversized critical entry must still be kept (at-least-one guarantee)")
	}
}

// --- F. Murmur receive filtering ---

func TestFilterMurmurReceive(t *testing.T) {
	t.Parallel()

	entries := []whisperstore.WhisperEntry{
		{ID: "1", Source: "murmur", Content: "coworker signal"},
		{ID: "2", Source: "auto-murmur", Content: "nudge to self"},
		{ID: "3", Source: "activity-summary", Content: "activity"},
	}

	t.Run("pass through when enabled", func(t *testing.T) {
		result := filterMurmurReceive(entries, "")
		if len(result) != 3 {
			t.Errorf("expected all 3 entries when receive enabled, got %d", len(result))
		}
	})

	t.Run("strips murmur when disabled", func(t *testing.T) {
		dir := createMurmurReceiveOffProject(t)
		result := filterMurmurReceive(entries, dir)

		if len(result) != 2 {
			t.Fatalf("expected 2 entries (murmur stripped), got %d", len(result))
		}
		for _, e := range result {
			if e.Source == "murmur" {
				t.Error("murmur entry should have been filtered out")
			}
		}
	})

	t.Run("preserves auto-murmur when disabled", func(t *testing.T) {
		dir := createMurmurReceiveOffProject(t)
		result := filterMurmurReceive(entries, dir)

		found := false
		for _, e := range result {
			if e.Source == "auto-murmur" {
				found = true
			}
		}
		if !found {
			t.Error("auto-murmur (nudge) entry should be preserved when receive is off")
		}
	})

	t.Run("preserves non-murmur when disabled", func(t *testing.T) {
		dir := createMurmurReceiveOffProject(t)
		result := filterMurmurReceive(entries, dir)

		found := false
		for _, e := range result {
			if e.Source == "activity-summary" {
				found = true
			}
		}
		if !found {
			t.Error("non-murmur entry should be preserved when receive is off")
		}
	})

	t.Run("nil entries returns nil", func(t *testing.T) {
		dir := createMurmurReceiveOffProject(t)
		result := filterMurmurReceive(nil, dir)
		if result != nil {
			t.Errorf("expected nil for nil input, got %v", result)
		}
	})
}

// createMurmurReceiveOffProject creates a temp project with murmur_receive=off config.
func createMurmurReceiveOffProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	sageoxDir := filepath.Join(dir, ".sageox")
	if err := os.MkdirAll(sageoxDir, 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(sageoxDir, "config.json")
	if err := os.WriteFile(configPath, []byte(`{"murmur_receive":"off"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}
