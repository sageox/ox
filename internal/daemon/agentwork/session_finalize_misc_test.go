package agentwork

import (
	"bufio"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/session"
)

func TestConvertStoredEntries(t *testing.T) {
	stored := []map[string]any{
		{"type": "user", "content": "hello"},
		{"type": "assistant", "content": "hi there"},
		{"type": "tool", "content": "", "tool_name": "Read", "tool_input": "/tmp/foo"},
	}

	entries := convertStoredEntries(stored)
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}
	if string(entries[0].Type) != "user" {
		t.Errorf("entry 0 type: want 'user', got %q", entries[0].Type)
	}
	if entries[0].Content != "hello" {
		t.Errorf("entry 0 content: want 'hello', got %q", entries[0].Content)
	}
	if entries[2].ToolName != "Read" {
		t.Errorf("entry 2 tool_name: want 'Read', got %q", entries[2].ToolName)
	}
}

func TestRecoverRawFromSessionFile(t *testing.T) {
	logger := slog.Default()

	t.Run("recovers_from_valid_session_file", func(t *testing.T) {
		sessionDir := t.TempDir()
		recPath := filepath.Join(sessionDir, recordingMarker)
		rawPath := filepath.Join(sessionDir, artifactRaw)

		// create source JSONL file
		sourceFile := filepath.Join(t.TempDir(), "session.jsonl")
		startedAt := time.Now().Add(-2 * time.Hour).Truncate(time.Second)
		writeClaudeCodeJSONL(t, sourceFile, []time.Time{
			startedAt.Add(1 * time.Minute),
			startedAt.Add(5 * time.Minute),
		})

		writeRecordingState(t, recPath, session.RecordingState{
			AgentID:     "OxRCVR",
			AdapterName: "claude-code",
			SessionFile: sourceFile,
			StartedAt:   startedAt,
		})

		result := recoverRawFromSessionFile(logger, recPath, sessionDir, rawPath)
		if !result {
			t.Fatal("expected recovery to succeed")
		}

		// raw.jsonl should exist
		if _, err := os.Stat(rawPath); err != nil {
			t.Fatalf("raw.jsonl not created: %v", err)
		}

		// should contain entries (2 timestamps * 2 entries each = 4 entries)
		count := countRawJSONLEntries(t, rawPath)
		if count != 4 {
			t.Errorf("expected 4 entries in raw.jsonl, got %d", count)
		}

		// verify header has recovered=true
		f, _ := os.Open(rawPath)
		defer f.Close()
		scanner := bufio.NewScanner(f)
		if scanner.Scan() {
			var header map[string]any
			if err := json.Unmarshal(scanner.Bytes(), &header); err == nil {
				meta, _ := header["_meta"].(map[string]any)
				if meta == nil {
					t.Error("header missing _meta")
				} else if recovered, _ := meta["recovered"].(bool); !recovered {
					t.Error("header _meta should have recovered=true")
				}
			}
		}
	})

	t.Run("empty_session_file", func(t *testing.T) {
		sessionDir := t.TempDir()
		recPath := filepath.Join(sessionDir, recordingMarker)
		rawPath := filepath.Join(sessionDir, artifactRaw)

		sourceFile := filepath.Join(t.TempDir(), "empty.jsonl")
		if err := os.WriteFile(sourceFile, nil, 0644); err != nil {
			t.Fatal(err)
		}

		writeRecordingState(t, recPath, session.RecordingState{
			AgentID:     "OxEMPT",
			AdapterName: "claude-code",
			SessionFile: sourceFile,
			StartedAt:   time.Now().Add(-2 * time.Hour),
		})

		if recoverRawFromSessionFile(logger, recPath, sessionDir, rawPath) {
			t.Error("expected recovery to fail for empty session file")
		}
	})

	t.Run("missing_session_file", func(t *testing.T) {
		sessionDir := t.TempDir()
		recPath := filepath.Join(sessionDir, recordingMarker)
		rawPath := filepath.Join(sessionDir, artifactRaw)

		writeRecordingState(t, recPath, session.RecordingState{
			AgentID:     "OxMISS",
			AdapterName: "claude-code",
			SessionFile: "/nonexistent/path/session.jsonl",
			StartedAt:   time.Now().Add(-2 * time.Hour),
		})

		if recoverRawFromSessionFile(logger, recPath, sessionDir, rawPath) {
			t.Error("expected recovery to fail for missing session file")
		}
	})

	t.Run("no_session_file_in_state", func(t *testing.T) {
		sessionDir := t.TempDir()
		recPath := filepath.Join(sessionDir, recordingMarker)
		rawPath := filepath.Join(sessionDir, artifactRaw)

		writeRecordingState(t, recPath, session.RecordingState{
			AgentID:     "OxNOSF",
			AdapterName: "claude-code",
			StartedAt:   time.Now().Add(-2 * time.Hour),
		})

		if recoverRawFromSessionFile(logger, recPath, sessionDir, rawPath) {
			t.Error("expected recovery to fail when no session file in state")
		}
	})

	t.Run("invalid_recording_json", func(t *testing.T) {
		sessionDir := t.TempDir()
		recPath := filepath.Join(sessionDir, recordingMarker)
		rawPath := filepath.Join(sessionDir, artifactRaw)

		if err := os.WriteFile(recPath, []byte("{not valid json!!!"), 0644); err != nil {
			t.Fatal(err)
		}

		if recoverRawFromSessionFile(logger, recPath, sessionDir, rawPath) {
			t.Error("expected recovery to fail for invalid recording JSON")
		}
	})

	t.Run("unknown_adapter", func(t *testing.T) {
		sessionDir := t.TempDir()
		recPath := filepath.Join(sessionDir, recordingMarker)
		rawPath := filepath.Join(sessionDir, artifactRaw)

		sourceFile := filepath.Join(t.TempDir(), "session.jsonl")
		writeClaudeCodeJSONL(t, sourceFile, []time.Time{time.Now()})

		writeRecordingState(t, recPath, session.RecordingState{
			AgentID:     "OxUNKN",
			AdapterName: "nonexistent",
			SessionFile: sourceFile,
			StartedAt:   time.Now().Add(-2 * time.Hour),
		})

		if recoverRawFromSessionFile(logger, recPath, sessionDir, rawPath) {
			t.Error("expected recovery to fail for unknown adapter")
		}
	})

	t.Run("filters_entries_by_start_time", func(t *testing.T) {
		sessionDir := t.TempDir()
		recPath := filepath.Join(sessionDir, recordingMarker)
		rawPath := filepath.Join(sessionDir, artifactRaw)

		sourceFile := filepath.Join(t.TempDir(), "session.jsonl")
		startedAt := time.Date(2026, 1, 10, 10, 0, 0, 0, time.UTC)
		writeClaudeCodeJSONL(t, sourceFile, []time.Time{
			startedAt.Add(-10 * time.Minute), // before start — should be filtered
			startedAt.Add(-5 * time.Minute),  // before start — should be filtered
			startedAt.Add(1 * time.Minute),   // after start — should be kept
			startedAt.Add(5 * time.Minute),   // after start — should be kept
		})

		writeRecordingState(t, recPath, session.RecordingState{
			AgentID:     "OxFLTR",
			AdapterName: "claude-code",
			SessionFile: sourceFile,
			StartedAt:   startedAt,
		})

		result := recoverRawFromSessionFile(logger, recPath, sessionDir, rawPath)
		if !result {
			t.Fatal("expected recovery to succeed")
		}

		// 2 timestamps after start * 2 entries each = 4 entries
		count := countRawJSONLEntries(t, rawPath)
		if count != 4 {
			t.Errorf("expected 4 entries after filtering, got %d", count)
		}
	})

	t.Run("no_entries_after_start_time", func(t *testing.T) {
		sessionDir := t.TempDir()
		recPath := filepath.Join(sessionDir, recordingMarker)
		rawPath := filepath.Join(sessionDir, artifactRaw)

		sourceFile := filepath.Join(t.TempDir(), "session.jsonl")
		startedAt := time.Date(2026, 1, 10, 12, 0, 0, 0, time.UTC)
		writeClaudeCodeJSONL(t, sourceFile, []time.Time{
			startedAt.Add(-10 * time.Minute), // all before start
			startedAt.Add(-5 * time.Minute),
		})

		writeRecordingState(t, recPath, session.RecordingState{
			AgentID:     "OxNOAF",
			AdapterName: "claude-code",
			SessionFile: sourceFile,
			StartedAt:   startedAt,
		})

		if recoverRawFromSessionFile(logger, recPath, sessionDir, rawPath) {
			t.Error("expected recovery to fail when all entries are before start time")
		}
	})
}

// writeClaudeCodeJSONL creates a Claude Code JSONL source file with entries at
// the given timestamps. Each timestamp gets a user+assistant pair.
func writeClaudeCodeJSONL(t *testing.T, path string, timestamps []time.Time) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	enc := json.NewEncoder(f)
	for i, ts := range timestamps {
		// user message
		user := map[string]any{
			"type":      "user",
			"timestamp": ts.Format(time.RFC3339),
			"message": map[string]any{
				"role":    "user",
				"content": "question " + strings.Repeat("x", i),
			},
		}
		if err := enc.Encode(user); err != nil {
			t.Fatal(err)
		}

		// assistant message
		assistant := map[string]any{
			"type":      "assistant",
			"timestamp": ts.Add(2 * time.Second).Format(time.RFC3339),
			"message": map[string]any{
				"role": "assistant",
				"content": []map[string]any{
					{"type": "text", "text": "answer " + strings.Repeat("y", i)},
				},
			},
		}
		if err := enc.Encode(assistant); err != nil {
			t.Fatal(err)
		}
	}
}

// writeRecordingState writes a .recording.json with the given state fields.
func writeRecordingState(t *testing.T, recPath string, state session.RecordingState) {
	t.Helper()
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(recPath, data, 0644); err != nil {
		t.Fatal(err)
	}
}

// countRawJSONLEntries counts non-header lines in a raw.jsonl file.
func countRawJSONLEntries(t *testing.T, rawPath string) int {
	t.Helper()
	f, err := os.Open(rawPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	count := 0
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(line, &m); err != nil {
			continue
		}
		if _, hasMeta := m["_meta"]; hasMeta {
			continue // skip header
		}
		count++
	}
	return count
}
