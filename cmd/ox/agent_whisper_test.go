package main

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	whisperstore "github.com/sageox/ox/internal/whisper/store"
)

func TestFormatWhispers_ReturnValue(t *testing.T) {


	tests := []struct {
		name    string
		entries []whisperstore.WhisperEntry
		want    bool
	}{
		{
			name:    "nil entries returns false",
			entries: nil,
			want:    false,
		},
		{
			name:    "empty slice returns false",
			entries: []whisperstore.WhisperEntry{},
			want:    false,
		},
		{
			name: "single entry returns true",
			entries: []whisperstore.WhisperEntry{
				{ID: "1", Topic: "test", Content: "hello", Importance: whisperstore.ImportanceNormal, Source: "test"},
			},
			want: true,
		},
		{
			name: "multiple entries returns true",
			entries: []whisperstore.WhisperEntry{
				{ID: "1", Topic: "a", Content: "first", Importance: whisperstore.ImportanceNormal, Source: "test"},
				{ID: "2", Topic: "b", Content: "second", Importance: whisperstore.ImportanceCritical, Source: "murmur"},
				{ID: "3", Topic: "c", Content: "third", Importance: whisperstore.ImportanceAmbient, Source: "activity-summary"},
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			got := formatWhispers(&buf, tt.entries)
			if got != tt.want {
				t.Errorf("formatWhispers() = %v, want %v", got, tt.want)
			}
			if !tt.want && buf.Len() > 0 {
				t.Errorf("expected no output when returning false, got: %q", buf.String())
			}
			if tt.want && buf.Len() == 0 {
				t.Error("expected output when returning true, got nothing")
			}
		})
	}
}

// xmlEntry mirrors the XML structure emitted by formatWhispers for round-trip parsing.
type xmlEntry struct {
	Importance string `xml:"importance,attr"`
	Topic      string `xml:"topic,attr"`
	Source     string `xml:"source,attr"`
	Agent      string `xml:"agent,attr,omitempty"`
	Content    string `xml:",chardata"`
}

type xmlMurmurTopic struct {
	Topic   string `xml:"topic,attr"`
	Content string `xml:",chardata"`
}

type xmlSystemReminder struct {
	XMLName       xml.Name         `xml:"system-reminder"`
	MurmurContext string           `xml:"murmur-context,omitempty"`
	MurmurTopics  []xmlMurmurTopic `xml:"murmur-topic"`
	Entries       []xmlEntry       `xml:"entry"`
}

func TestFormatWhispers_XMLRoundTrip(t *testing.T) {


	entries := []whisperstore.WhisperEntry{
		{ID: "1", Topic: "lint", Content: "fix rule X", Importance: whisperstore.ImportanceNormal, Source: "lint-runner"},
		{ID: "2", Topic: "architecture", Content: "API v3 breaking change", Importance: whisperstore.ImportanceCritical, Source: "murmur"},
		{ID: "3", Topic: "ci", Content: "pipeline green", Importance: whisperstore.ImportanceAmbient, Source: "activity-summary"},
	}

	var buf bytes.Buffer
	wrote := formatWhispers(&buf, entries)
	if !wrote {
		t.Fatal("expected formatWhispers to return true")
	}

	// parse the XML back
	var parsed xmlSystemReminder
	if err := xml.Unmarshal(buf.Bytes(), &parsed); err != nil {
		t.Fatalf("failed to parse XML output: %v\nraw output:\n%s", err, buf.String())
	}

	if len(parsed.Entries) != len(entries) {
		t.Fatalf("parsed %d entries, want %d", len(parsed.Entries), len(entries))
	}

	for i, want := range entries {
		got := parsed.Entries[i]
		if got.Content != want.Content {
			t.Errorf("entry[%d] content = %q, want %q", i, got.Content, want.Content)
		}
		if got.Topic != want.Topic {
			t.Errorf("entry[%d] topic = %q, want %q", i, got.Topic, want.Topic)
		}
		if got.Source != want.Source {
			t.Errorf("entry[%d] source = %q, want %q", i, got.Source, want.Source)
		}
		if got.Importance != string(want.Importance) {
			t.Errorf("entry[%d] importance = %q, want %q", i, got.Importance, string(want.Importance))
		}
	}
}

func TestFormatWhispers_SpecialCharacters(t *testing.T) {


	entries := []whisperstore.WhisperEntry{
		{
			ID:         "1",
			Topic:      "html-content",
			Content:    `value < 10 && value > 0`,
			Importance: whisperstore.ImportanceNormal,
			Source:     "test",
		},
		{
			ID:         "2",
			Topic:      "quotes",
			Content:    `she said "hello" & 'goodbye'`,
			Importance: whisperstore.ImportanceCritical,
			Source:     "murmur",
		},
		{
			ID:         "3",
			Topic:      "xml-like",
			Content:    `<div class="foo">bar & baz</div>`,
			Importance: whisperstore.ImportanceAmbient,
			Source:     "test",
		},
	}

	var buf bytes.Buffer
	wrote := formatWhispers(&buf, entries)
	if !wrote {
		t.Fatal("expected formatWhispers to return true")
	}

	// XML round-trip: parse back and verify content survives escaping
	var parsed xmlSystemReminder
	if err := xml.Unmarshal(buf.Bytes(), &parsed); err != nil {
		t.Fatalf("failed to parse XML output: %v\nraw output:\n%s", err, buf.String())
	}

	if len(parsed.Entries) != len(entries) {
		t.Fatalf("parsed %d entries, want %d", len(parsed.Entries), len(entries))
	}

	for i, want := range entries {
		got := parsed.Entries[i]
		if got.Content != want.Content {
			t.Errorf("entry[%d] content = %q, want %q", i, got.Content, want.Content)
		}
		if got.Topic != want.Topic {
			t.Errorf("entry[%d] topic = %q, want %q", i, got.Topic, want.Topic)
		}
		if got.Source != want.Source {
			t.Errorf("entry[%d] source = %q, want %q", i, got.Source, want.Source)
		}
		if got.Importance != string(want.Importance) {
			t.Errorf("entry[%d] importance = %q, want %q", i, got.Importance, string(want.Importance))
		}
	}
}

func TestCapMurmurWhispers_NonMurmurPassThrough(t *testing.T) {
	entries := []whisperstore.WhisperEntry{
		{ID: "1", Source: "auto-murmur", Topic: "murmur-nudge", Content: "nudge content"},
		{ID: "2", Source: "activity-summary", Topic: "activity", Content: "3 coworkers active"},
	}

	result := capMurmurWhispers(entries)
	if len(result) != 2 {
		t.Errorf("expected 2 entries (non-murmur pass through), got %d", len(result))
	}
}

func TestCapMurmurWhispers_LimitsPerAgent(t *testing.T) {
	entries := []whisperstore.WhisperEntry{
		{ID: "1", Source: "murmur", AgentID: "OxA", Content: "murmur 1", CreatedAt: time.Now()},
		{ID: "2", Source: "murmur", AgentID: "OxA", Content: "murmur 2", CreatedAt: time.Now().Add(-1 * time.Minute)},
		{ID: "3", Source: "murmur", AgentID: "OxA", Content: "murmur 3", CreatedAt: time.Now().Add(-2 * time.Minute)},
		{ID: "4", Source: "murmur", AgentID: "OxA", Content: "murmur 4", CreatedAt: time.Now().Add(-3 * time.Minute)},
	}

	result := capMurmurWhispers(entries)

	var murmurs []whisperstore.WhisperEntry
	for _, e := range result {
		if e.Source == "murmur" {
			murmurs = append(murmurs, e)
		}
	}

	if len(murmurs) != maxMurmurWhispersPerAgent {
		t.Errorf("expected %d murmurs per agent, got %d", maxMurmurWhispersPerAgent, len(murmurs))
	}
	if murmurs[0].ID != "1" {
		t.Errorf("expected newest murmur kept, got ID %s", murmurs[0].ID)
	}
}

func TestCapMurmurWhispers_MultipleAgents(t *testing.T) {
	entries := []whisperstore.WhisperEntry{
		{ID: "a1", Source: "murmur", AgentID: "OxA", Content: "A working on auth", CreatedAt: time.Now()},
		{ID: "b1", Source: "murmur", AgentID: "OxB", Content: "B working on API", CreatedAt: time.Now().Add(-1 * time.Minute)},
		{ID: "c1", Source: "murmur", AgentID: "OxC", Content: "C fixing tests", CreatedAt: time.Now().Add(-2 * time.Minute)},
	}

	result := capMurmurWhispers(entries)

	var murmurs []whisperstore.WhisperEntry
	for _, e := range result {
		if e.Source == "murmur" {
			murmurs = append(murmurs, e)
		}
	}

	// 1 per agent, 3 agents = 3
	if len(murmurs) != 3 {
		t.Errorf("expected 3 murmurs (1 per agent), got %d", len(murmurs))
	}
}

func TestCapMurmurWhispers_TokenBudget(t *testing.T) {
	bigContent := strings.Repeat("x", maxMurmurWhisperTokens*estimatedBytesPerToken)
	now := time.Now()

	entries := []whisperstore.WhisperEntry{
		{ID: "1", Source: "murmur", AgentID: "OxA", Content: bigContent, CreatedAt: now},
		{ID: "2", Source: "murmur", AgentID: "OxB", Content: "small murmur", CreatedAt: now.Add(-1 * time.Minute)},
	}

	result := capMurmurWhispers(entries)

	var murmurs []whisperstore.WhisperEntry
	for _, e := range result {
		if e.Source == "murmur" {
			murmurs = append(murmurs, e)
		}
	}

	// big murmur fills budget alone; with random sampling either one may be
	// picked first, but only one can fit — verify exactly 1 kept
	if len(murmurs) != 1 {
		t.Errorf("expected 1 murmur (budget allows only one), got %d", len(murmurs))
	}
}

func TestCapMurmurWhispers_AlwaysKeepsAtLeastOne(t *testing.T) {
	hugeContent := strings.Repeat("y", maxMurmurWhisperTokens*estimatedBytesPerToken*2)

	entries := []whisperstore.WhisperEntry{
		{ID: "1", Source: "murmur", AgentID: "OxA", Content: hugeContent, CreatedAt: time.Now()},
	}

	result := capMurmurWhispers(entries)
	if len(result) != 1 {
		t.Errorf("expected 1 entry (always keep at least one), got %d", len(result))
	}
}

func TestCapMurmurWhispers_MixedSources(t *testing.T) {
	now := time.Now()
	entries := []whisperstore.WhisperEntry{
		{ID: "nudge", Source: "auto-murmur", Topic: "murmur-nudge", Content: "nudge"},
		{ID: "m1", Source: "murmur", AgentID: "OxA", Content: "working on X", CreatedAt: now},
		{ID: "m2", Source: "murmur", AgentID: "OxA", Content: "still on X", CreatedAt: now.Add(-1 * time.Minute)},
		{ID: "m3", Source: "murmur", AgentID: "OxA", Content: "old murmur", CreatedAt: now.Add(-2 * time.Minute)},
		{ID: "activity", Source: "activity-summary", Content: "2 active"},
	}

	result := capMurmurWhispers(entries)

	// non-murmurs (nudge + activity) + 1 capped murmur = 3
	if len(result) != 3 {
		t.Errorf("expected 3 entries (2 non-murmur + 1 capped murmur), got %d", len(result))
	}

	// non-murmurs come first in result
	if result[0].Source == "murmur" {
		t.Error("expected non-murmur entries first")
	}
}

func TestDeduplicateBySourceTopic(t *testing.T) {
	t.Parallel()
	now := time.Now()

	t.Run("collapses identical nudges to one", func(t *testing.T) {
		entries := make([]whisperstore.WhisperEntry, 20)
		for i := range entries {
			entries[i] = whisperstore.WhisperEntry{
				ID:        fmt.Sprintf("nudge-%d", i),
				Source:    "auto-murmur",
				Topic:     "murmur-nudge",
				Content:   "ACTION REQUIRED: Tell your teammates...",
				CreatedAt: now.Add(-time.Duration(i) * time.Minute),
			}
		}
		result := deduplicateBySourceTopic(entries)
		if len(result) != 1 {
			t.Errorf("expected 1 deduplicated nudge, got %d", len(result))
		}
		if result[0].ID != "nudge-0" {
			t.Errorf("expected newest nudge (nudge-0), got %s", result[0].ID)
		}
	})

	t.Run("preserves distinct source-topic pairs", func(t *testing.T) {
		entries := []whisperstore.WhisperEntry{
			{ID: "1", Source: "auto-murmur", Topic: "murmur-nudge", CreatedAt: now},
			{ID: "2", Source: "activity-summary", Topic: "activity", CreatedAt: now},
			{ID: "3", Source: "auto-murmur", Topic: "other-topic", CreatedAt: now},
		}
		result := deduplicateBySourceTopic(entries)
		if len(result) != 3 {
			t.Errorf("expected 3 distinct entries, got %d", len(result))
		}
	})

	t.Run("empty and single entry", func(t *testing.T) {
		if len(deduplicateBySourceTopic(nil)) != 0 {
			t.Error("nil should return empty")
		}
		single := []whisperstore.WhisperEntry{{ID: "1", Source: "x", Topic: "y"}}
		if len(deduplicateBySourceTopic(single)) != 1 {
			t.Error("single entry should pass through")
		}
	})
}

func TestCapMurmurWhispers_DeduplicatesNudges(t *testing.T) {
	now := time.Now()
	// simulate 20 accumulated nudges (the flooding scenario)
	var entries []whisperstore.WhisperEntry
	for i := 0; i < 20; i++ {
		entries = append(entries, whisperstore.WhisperEntry{
			ID:        fmt.Sprintf("nudge-%d", i),
			Source:    "auto-murmur",
			Topic:     "murmur-nudge",
			Content:   "ACTION REQUIRED: Tell your teammates...",
			CreatedAt: now.Add(-time.Duration(i) * time.Minute),
		})
	}
	// add a real murmur to verify it's unaffected
	entries = append(entries, whisperstore.WhisperEntry{
		ID: "murmur-1", Source: "murmur", AgentID: "OxA", Content: "working on auth", CreatedAt: now,
	})

	result := capMurmurWhispers(entries)

	var nudges, murmurs int
	for _, e := range result {
		switch e.Source {
		case "auto-murmur":
			nudges++
		case "murmur":
			murmurs++
		}
	}
	if nudges != 1 {
		t.Errorf("expected 1 deduplicated nudge, got %d", nudges)
	}
	if murmurs != 1 {
		t.Errorf("expected 1 murmur, got %d", murmurs)
	}
}

func TestCapMurmurWhispers_Empty(t *testing.T) {
	result := capMurmurWhispers(nil)
	if len(result) != 0 {
		t.Errorf("expected 0 entries for nil input, got %d", len(result))
	}

	result = capMurmurWhispers([]whisperstore.WhisperEntry{})
	if len(result) != 0 {
		t.Errorf("expected 0 entries for empty input, got %d", len(result))
	}
}

func TestCapMurmurWhispers_AnonymousMurmurs(t *testing.T) {
	// murmurs without agent ID should be grouped together
	now := time.Now()
	entries := []whisperstore.WhisperEntry{
		{ID: "1", Source: "murmur", Content: "anon murmur 1", CreatedAt: now},
		{ID: "2", Source: "murmur", Content: "anon murmur 2", CreatedAt: now.Add(-1 * time.Minute)},
		{ID: "3", Source: "murmur", Content: "anon murmur 3", CreatedAt: now.Add(-2 * time.Minute)},
	}

	result := capMurmurWhispers(entries)

	var murmurs []whisperstore.WhisperEntry
	for _, e := range result {
		if e.Source == "murmur" {
			murmurs = append(murmurs, e)
		}
	}

	if len(murmurs) != maxMurmurWhispersPerAgent {
		t.Errorf("expected %d anonymous murmurs (same group), got %d", maxMurmurWhispersPerAgent, len(murmurs))
	}
}

func TestCapMurmurWhispers_FairnessAcrossAgents(t *testing.T) {
	// with a tight budget, random sampling should give all agents a chance
	// over many iterations. create 5 agents with ~200 byte murmurs each (1000 total).
	// budget is 1024 tokens * 4 bytes = 4096 bytes, so all fit easily.
	// but if we make each murmur ~900 bytes, only ~4 fit and fairness matters.
	content := strings.Repeat("z", 900) // ~225 tokens each
	agents := []string{"OxA", "OxB", "OxC", "OxD", "OxE"}

	var entries []whisperstore.WhisperEntry
	for i, agent := range agents {
		entries = append(entries, whisperstore.WhisperEntry{
			ID:        fmt.Sprintf("m-%s", agent),
			Source:    "murmur",
			AgentID:   agent,
			Content:   content,
			CreatedAt: time.Now().Add(-time.Duration(i) * time.Minute),
		})
	}

	// run many iterations and track which agents appear
	agentSeen := make(map[string]int)
	iterations := 200
	for i := 0; i < iterations; i++ {
		result := capMurmurWhispers(entries)
		for _, e := range result {
			if e.Source == "murmur" {
				agentSeen[e.AgentID]++
			}
		}
	}

	// every agent should appear at least once across 200 iterations
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

	// all fit in budget, should be sorted newest first
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
	// non-murmur whispers are not subject to the 24h filter
	if nonMurmurs != 1 {
		t.Errorf("expected 1 non-murmur (pass through), got %d", nonMurmurs)
	}
}

func TestFormatWhispers_LargeContent(t *testing.T) {


	// 500 bytes — max murmur size
	largeContent := strings.Repeat("x", 500)

	entries := []whisperstore.WhisperEntry{
		{
			ID:         "1",
			Topic:      "large-murmur",
			Content:    largeContent,
			Importance: whisperstore.ImportanceNormal,
			Source:     "murmur",
		},
	}

	var buf bytes.Buffer
	wrote := formatWhispers(&buf, entries)
	if !wrote {
		t.Fatal("expected formatWhispers to return true")
	}

	output := buf.String()

	// verify the full content is present (no truncation)
	if !strings.Contains(output, largeContent) {
		t.Error("large content was truncated")
	}

	// verify XML wrapper still present around large content
	if !strings.Contains(output, "<system-reminder>") {
		t.Error("missing <system-reminder> opening tag")
	}
	if !strings.Contains(output, "</system-reminder>") {
		t.Error("missing </system-reminder> closing tag")
	}
	if !strings.Contains(output, `topic="large-murmur"`) {
		t.Error("missing topic attribute")
	}
}

func TestFilterMurmurReceive(t *testing.T) {
	t.Parallel()

	entries := []whisperstore.WhisperEntry{
		{ID: "1", Source: "murmur", Content: "coworker signal"},
		{ID: "2", Source: "auto-murmur", Content: "nudge to self"},
		{ID: "3", Source: "activity-summary", Content: "activity"},
	}

	t.Run("pass through when enabled", func(t *testing.T) {
		// empty projectRoot => MurmurReceiveEnabled returns true (default auto)
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

func TestMurmurTopicHint(t *testing.T) {
	t.Parallel()

	tests := []struct {
		topic string
		want  bool // true if non-empty hint expected
	}{
		{"wip", true},
		{"conflict", true},
		{"architecture", true},
		{"lint", true},
		{"ci", true},
		{"unknown-topic", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.topic, func(t *testing.T) {
			hint := murmurTopicHint(tt.topic)
			if tt.want && hint == "" {
				t.Errorf("expected non-empty hint for topic %q", tt.topic)
			}
			if !tt.want && hint != "" {
				t.Errorf("expected empty hint for topic %q, got %q", tt.topic, hint)
			}
		})
	}
}

func TestFormatWhispers_MurmurFraming(t *testing.T) {
	t.Parallel()

	t.Run("murmur context appears with murmurs", func(t *testing.T) {
		entries := []whisperstore.WhisperEntry{
			{ID: "1", Source: "murmur", Topic: "wip", Content: "working on auth", Importance: whisperstore.ImportanceNormal, AgentID: "OxA7b3"},
		}
		var buf bytes.Buffer
		formatWhispers(&buf, entries)
		output := buf.String()

		if !strings.Contains(output, "<murmur-context>") {
			t.Error("expected <murmur-context> tag when murmurs present")
		}
		if !strings.Contains(output, "NOT tasks or requests") {
			t.Error("expected murmur context framing text")
		}
	})

	t.Run("murmur context absent without murmurs", func(t *testing.T) {
		entries := []whisperstore.WhisperEntry{
			{ID: "1", Source: "auto-murmur", Topic: "nudge", Content: "nudge content", Importance: whisperstore.ImportanceNormal},
			{ID: "2", Source: "activity-summary", Topic: "activity", Content: "active", Importance: whisperstore.ImportanceNormal},
		}
		var buf bytes.Buffer
		formatWhispers(&buf, entries)
		output := buf.String()

		if strings.Contains(output, "<murmur-context>") {
			t.Error("murmur-context should not appear without murmur entries")
		}
		if strings.Contains(output, "<murmur-topic") {
			t.Error("murmur-topic should not appear without murmur entries")
		}
	})

	t.Run("murmur topic hints for known topics", func(t *testing.T) {
		entries := []whisperstore.WhisperEntry{
			{ID: "1", Source: "murmur", Topic: "wip", Content: "building feature X", Importance: whisperstore.ImportanceNormal, AgentID: "OxA1"},
			{ID: "2", Source: "murmur", Topic: "conflict", Content: "touching auth module", Importance: whisperstore.ImportanceNormal, AgentID: "OxB2"},
		}
		var buf bytes.Buffer
		formatWhispers(&buf, entries)
		output := buf.String()

		if !strings.Contains(output, `<murmur-topic topic="wip">`) {
			t.Error("expected murmur-topic for wip")
		}
		if !strings.Contains(output, `<murmur-topic topic="conflict">`) {
			t.Error("expected murmur-topic for conflict")
		}
	})

	t.Run("no murmur topic hint for unknown topics", func(t *testing.T) {
		entries := []whisperstore.WhisperEntry{
			{ID: "1", Source: "murmur", Topic: "random-unknown", Content: "stuff", Importance: whisperstore.ImportanceNormal},
		}
		var buf bytes.Buffer
		formatWhispers(&buf, entries)
		output := buf.String()

		if !strings.Contains(output, "<murmur-context>") {
			t.Error("expected murmur-context even for unknown topic")
		}
		if strings.Contains(output, "<murmur-topic") {
			t.Error("murmur-topic should not appear for unknown topics")
		}
	})

	t.Run("agent attribute on murmur entries", func(t *testing.T) {
		entries := []whisperstore.WhisperEntry{
			{ID: "1", Source: "murmur", Topic: "wip", Content: "working", Importance: whisperstore.ImportanceNormal, AgentID: "OxA7b3"},
			{ID: "2", Source: "activity-summary", Topic: "activity", Content: "active", Importance: whisperstore.ImportanceNormal, AgentID: "OxB1c2"},
		}
		var buf bytes.Buffer
		formatWhispers(&buf, entries)
		output := buf.String()

		if !strings.Contains(output, `agent="OxA7b3"`) {
			t.Error("expected agent attribute on murmur entry")
		}
		// non-murmur entries should NOT get agent attribute
		if strings.Contains(output, `agent="OxB1c2"`) {
			t.Error("non-murmur entries should not have agent attribute")
		}
	})

	t.Run("no agent attribute when agent ID empty", func(t *testing.T) {
		entries := []whisperstore.WhisperEntry{
			{ID: "1", Source: "murmur", Topic: "wip", Content: "anon murmur", Importance: whisperstore.ImportanceNormal},
		}
		var buf bytes.Buffer
		formatWhispers(&buf, entries)
		output := buf.String()

		if strings.Contains(output, `agent=`) {
			t.Error("should not emit agent attribute when AgentID is empty")
		}
	})

	t.Run("XML round-trip with murmur framing", func(t *testing.T) {
		entries := []whisperstore.WhisperEntry{
			{ID: "1", Source: "murmur", Topic: "wip", Content: "building auth", Importance: whisperstore.ImportanceNormal, AgentID: "OxA7b3"},
			{ID: "2", Source: "auto-murmur", Topic: "nudge", Content: "nudge", Importance: whisperstore.ImportanceNormal},
		}
		var buf bytes.Buffer
		formatWhispers(&buf, entries)

		var parsed xmlSystemReminder
		if err := xml.Unmarshal(buf.Bytes(), &parsed); err != nil {
			t.Fatalf("failed to parse XML: %v\nraw:\n%s", err, buf.String())
		}

		if parsed.MurmurContext == "" {
			t.Error("expected non-empty murmur-context")
		}
		if len(parsed.MurmurTopics) != 1 {
			t.Errorf("expected 1 murmur-topic, got %d", len(parsed.MurmurTopics))
		} else if parsed.MurmurTopics[0].Topic != "wip" {
			t.Errorf("expected murmur-topic for wip, got %q", parsed.MurmurTopics[0].Topic)
		}
		if len(parsed.Entries) != 2 {
			t.Fatalf("expected 2 entries, got %d", len(parsed.Entries))
		}
		if parsed.Entries[0].Agent != "OxA7b3" {
			t.Errorf("expected agent OxA7b3 on murmur entry, got %q", parsed.Entries[0].Agent)
		}
		if parsed.Entries[1].Agent != "" {
			t.Errorf("expected no agent on non-murmur entry, got %q", parsed.Entries[1].Agent)
		}
	})
}
