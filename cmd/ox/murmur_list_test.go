package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/ledger"
)

func TestParseDuration(t *testing.T) {
	tests := []struct {
		input string
		want  time.Duration
		err   bool
	}{
		{"30m", 30 * time.Minute, false},
		{"2h", 2 * time.Hour, false},
		{"1d", 24 * time.Hour, false},
		{"7d", 7 * 24 * time.Hour, false},
		{"500ms", 500 * time.Millisecond, false},
		{"0d", 0, true},
		{"bad", 0, true},
		{"d", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := parseDuration(tt.input)
			if tt.err {
				if err == nil {
					t.Errorf("parseDuration(%q) expected error", tt.input)
				}
				return
			}
			if err != nil {
				t.Errorf("parseDuration(%q) error: %v", tt.input, err)
				return
			}
			if got != tt.want {
				t.Errorf("parseDuration(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestMurmurListJSONOutput(t *testing.T) {
	// set up a temp ledger with murmur files
	tmpDir := t.TempDir()
	now := time.Now().UTC()

	murmurs := []ledger.MurmurFile{
		{
			SchemaVersion: "1",
			ID:            "test-id-1",
			Timestamp:     now.Add(-5 * time.Minute),
			AgentID:       "OxAbc1",
			Topic:         "wip",
			Importance:    "normal",
			Content:       "refactoring auth middleware",
			Scope:         "ledger",
		},
		{
			SchemaVersion: "1",
			ID:            "test-id-2",
			Timestamp:     now.Add(-2 * time.Minute),
			AgentID:       "OxDef2",
			Topic:         "conflict",
			Importance:    "critical",
			Content:       "modifying shared database schema",
			Scope:         "ledger",
		},
	}

	for _, m := range murmurs {
		_, err := ledger.WriteMurmur(tmpDir, m)
		if err != nil {
			t.Fatalf("write murmur: %v", err)
		}
	}

	// use windowHours=2 to handle hour-boundary flakiness (e.g., test runs at HH:02,
	// -5min murmur lands in previous hour's directory partition)
	read, err := ledger.ReadMurmursInWindow(tmpDir, 2)
	if err != nil {
		t.Fatalf("read murmurs: %v", err)
	}
	if len(read) != 2 {
		t.Fatalf("expected 2 murmurs, got %d", len(read))
	}
}

func TestMurmurListFiltering(t *testing.T) {
	tmpDir := t.TempDir()
	now := time.Now().UTC()

	murmurs := []ledger.MurmurFile{
		{SchemaVersion: "1", ID: "m1", Timestamp: now.Add(-1 * time.Minute), AgentID: "OxA", Topic: "lint", Importance: "normal", Content: "fixing lint"},
		{SchemaVersion: "1", ID: "m2", Timestamp: now.Add(-2 * time.Minute), AgentID: "OxB", Topic: "architecture", Importance: "critical", Content: "redesigning API"},
		{SchemaVersion: "1", ID: "m3", Timestamp: now.Add(-3 * time.Minute), AgentID: "OxA", Topic: "lint", Importance: "ambient", Content: "more lint fixes"},
	}

	for _, m := range murmurs {
		if _, err := ledger.WriteMurmur(tmpDir, m); err != nil {
			t.Fatalf("write murmur: %v", err)
		}
	}

	// use windowHours=2 to handle hour-boundary flakiness
	all, err := ledger.ReadMurmursInWindow(tmpDir, 2)
	if err != nil {
		t.Fatalf("ReadMurmursInWindow failed: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("expected 3 murmurs, got %d", len(all))
	}

	// simulate topic filter
	var lintOnly []ledger.MurmurFile
	for _, m := range all {
		if m.Topic == "lint" {
			lintOnly = append(lintOnly, m)
		}
	}
	if len(lintOnly) != 2 {
		t.Errorf("topic filter: expected 2 lint murmurs, got %d", len(lintOnly))
	}

	// simulate agent filter
	var agentA []ledger.MurmurFile
	for _, m := range all {
		if m.AgentID == "OxA" {
			agentA = append(agentA, m)
		}
	}
	if len(agentA) != 2 {
		t.Errorf("agent filter: expected 2 from OxA, got %d", len(agentA))
	}
}

func TestMurmurListEntryJSON(t *testing.T) {
	entry := murmurListEntry{
		ID:         "test-id",
		Timestamp:  "2026-03-27T14:30:00Z",
		AgentID:    "OxTest",
		Topic:      "wip",
		Importance: "normal",
		Content:    "working on auth",
		Scope:      "ledger",
	}

	data, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded murmurListEntry
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if decoded.ID != entry.ID {
		t.Errorf("ID: got %q, want %q", decoded.ID, entry.ID)
	}
	if decoded.AgentID != entry.AgentID {
		t.Errorf("AgentID: got %q, want %q", decoded.AgentID, entry.AgentID)
	}
	if decoded.Topic != entry.Topic {
		t.Errorf("Topic: got %q, want %q", decoded.Topic, entry.Topic)
	}
}

func TestMurmurListOutputJSON(t *testing.T) {
	output := murmurListOutput{
		Murmurs: []murmurListEntry{
			{ID: "1", Topic: "lint", Content: "fixing"},
		},
		Total:  1,
		Window: "12h",
	}

	data, err := json.Marshal(output)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded murmurListOutput
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if decoded.Total != 1 {
		t.Errorf("Total: got %d, want 1", decoded.Total)
	}
	if decoded.Window != "12h" {
		t.Errorf("Window: got %q, want %q", decoded.Window, "12h")
	}
}

func TestMurmurFileRoundTrip(t *testing.T) {
	tmpDir := t.TempDir()
	now := time.Now().UTC()

	original := ledger.MurmurFile{
		SchemaVersion: "1",
		ID:            "roundtrip-id",
		Timestamp:     now,
		AgentID:       "OxRound",
		Topic:         "test",
		Importance:    "normal",
		Content:       "testing round trip",
		Scope:         "ledger",
	}

	relPath, err := ledger.WriteMurmur(tmpDir, original)
	if err != nil {
		t.Fatalf("write: %v", err)
	}

	// verify file exists
	fullPath := filepath.Join(tmpDir, relPath)
	if _, err := os.Stat(fullPath); err != nil {
		t.Fatalf("file not found: %v", err)
	}

	// read back
	read, err := ledger.ReadMurmursInWindow(tmpDir, 1)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(read) != 1 {
		t.Fatalf("expected 1 murmur, got %d", len(read))
	}

	if read[0].ID != original.ID {
		t.Errorf("ID mismatch: got %q, want %q", read[0].ID, original.ID)
	}
	if read[0].Content != original.Content {
		t.Errorf("Content mismatch: got %q, want %q", read[0].Content, original.Content)
	}
}
