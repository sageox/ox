package main

import (
	"bytes"
	"encoding/xml"
	"fmt"
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
	Content    string `xml:",chardata"`
}

type xmlSystemReminder struct {
	XMLName xml.Name   `xml:"system-reminder"`
	Entries []xmlEntry `xml:"entry"`
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
