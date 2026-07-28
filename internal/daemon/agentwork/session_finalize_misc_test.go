package agentwork

import (
	"bufio"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/adapters"
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

// mockReadAdapter is a test adapter that reads simple JSONL files
// with {"role":"...", "content":"...", "timestamp":"..."} lines.
type mockReadAdapter struct{}

func (m *mockReadAdapter) Name() string { return "test-mock" }
func (m *mockReadAdapter) Detect() bool { return false }
func (m *mockReadAdapter) FindSessionFile(_ adapters.SessionLookup) (string, error) {
	return "", adapters.ErrSessionNotFound
}
func (m *mockReadAdapter) ReadMetadata(_ string) (*adapters.SessionMetadata, error) {
	return nil, nil
}
func (m *mockReadAdapter) Watch(_ context.Context, _ string) (<-chan adapters.RawEntry, error) {
	return nil, adapters.ErrWatchNotSupported
}

func (m *mockReadAdapter) Read(sessionPath string) ([]adapters.RawEntry, error) {
	f, err := os.Open(sessionPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var entries []adapters.RawEntry
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var raw struct {
			Role      string `json:"role"`
			Content   string `json:"content"`
			Timestamp string `json:"timestamp"`
		}
		if err := json.Unmarshal(line, &raw); err != nil {
			continue
		}
		ts, _ := time.Parse(time.RFC3339, raw.Timestamp)
		entries = append(entries, adapters.RawEntry{
			Role:      raw.Role,
			Content:   raw.Content,
			Timestamp: ts,
		})
	}
	return entries, scanner.Err()
}

// writeSimpleJSONL creates a simple JSONL source file with entries at the
// given timestamps. Each timestamp gets a user+assistant pair.
func writeSimpleJSONL(t *testing.T, path string, timestamps []time.Time) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	enc := json.NewEncoder(f)
	for i, ts := range timestamps {
		if err := enc.Encode(map[string]any{
			"role":      "user",
			"content":   "question " + string(rune('a'+i)),
			"timestamp": ts.Format(time.RFC3339),
		}); err != nil {
			t.Fatal(err)
		}
		if err := enc.Encode(map[string]any{
			"role":      "assistant",
			"content":   "answer " + string(rune('a'+i)),
			"timestamp": ts.Add(2 * time.Second).Format(time.RFC3339),
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRecoverRawFromSessionFile(t *testing.T) {
	// register a mock adapter for test recovery
	adapters.Register(&mockReadAdapter{})
	t.Cleanup(func() { adapters.Unregister("test-mock") })

	logger := slog.Default()

	t.Run("recovers_from_valid_session_file", func(t *testing.T) {
		sessionDir := t.TempDir()
		recPath := filepath.Join(sessionDir, recordingMarker)
		rawPath := filepath.Join(sessionDir, artifactRaw)

		// create source JSONL file
		sourceFile := filepath.Join(t.TempDir(), "session.jsonl")
		startedAt := time.Now().Add(-2 * time.Hour).Truncate(time.Second)
		writeSimpleJSONL(t, sourceFile, []time.Time{
			startedAt.Add(1 * time.Minute),
			startedAt.Add(5 * time.Minute),
		})

		writeRecordingState(t, recPath, session.RecordingState{
			AgentID:     "OxRCVR",
			AdapterName: "test-mock",
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
			AdapterName: "test-mock",
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
			AdapterName: "test-mock",
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
			AdapterName: "test-mock",
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
		writeSimpleJSONL(t, sourceFile, []time.Time{time.Now()})

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
		writeSimpleJSONL(t, sourceFile, []time.Time{
			startedAt.Add(-10 * time.Minute), // before start — should be filtered
			startedAt.Add(-5 * time.Minute),  // before start — should be filtered
			startedAt.Add(1 * time.Minute),   // after start — should be kept
			startedAt.Add(5 * time.Minute),   // after start — should be kept
		})

		writeRecordingState(t, recPath, session.RecordingState{
			AgentID:     "OxFLTR",
			AdapterName: "test-mock",
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

	// TestRecoverRawFromSessionFile carries_forward_session_id_from_state
	// guards the ox-5n8e root cause: .recording.json's SessionID (minted at
	// StartRecording, post rollout) is the crash-safe carrier every later
	// finalize path reads back out of the raw.jsonl header via
	// ReadHeaderSessionID/ParseStoreMeta. If the reconstructed header drops
	// it, writeMetaAndUploadLFS sees an empty headerID and — when no
	// meta.json exists yet either — mints a brand new SessionID, rotating
	// away from an identity that may already be circulated (commit
	// trailers, PR bodies) or cached server-side. Two independent
	// finalize attempts for the SAME session (e.g. two developer clones
	// that both pull the orphaned raw.jsonl before either pushes a
	// meta.json) would then each mint a different ID for identical content
	// — exactly the two-session_id-values-for-one-session bug observed in
	// production.
	t.Run("carries_forward_session_id_from_state", func(t *testing.T) {
		sessionDir := t.TempDir()
		recPath := filepath.Join(sessionDir, recordingMarker)
		rawPath := filepath.Join(sessionDir, artifactRaw)

		sourceFile := filepath.Join(t.TempDir(), "session.jsonl")
		startedAt := time.Now().Add(-2 * time.Hour).Truncate(time.Second)
		writeSimpleJSONL(t, sourceFile, []time.Time{startedAt.Add(1 * time.Minute)})

		const durableID = "ses_01890a5d-ac96-774b-bcce-b302099a8057"
		writeRecordingState(t, recPath, session.RecordingState{
			AgentID:     "OxSID1",
			AdapterName: "test-mock",
			SessionFile: sourceFile,
			StartedAt:   startedAt,
			SessionID:   durableID,
		})

		if !recoverRawFromSessionFile(logger, recPath, sessionDir, rawPath) {
			t.Fatal("expected recovery to succeed")
		}

		got := session.ReadHeaderSessionID(rawPath)
		if got != durableID {
			t.Errorf("reconstructed header lost the durable SessionID: want %q, got %q", durableID, got)
		}
	})

	t.Run("no_session_id_in_state_yields_no_header_id", func(t *testing.T) {
		// legacy recording (predates SessionID-at-birth minting): state has
		// no SessionID, so the reconstructed header correctly carries none
		// — nothing to preserve, and this must not error or invent one.
		sessionDir := t.TempDir()
		recPath := filepath.Join(sessionDir, recordingMarker)
		rawPath := filepath.Join(sessionDir, artifactRaw)

		sourceFile := filepath.Join(t.TempDir(), "session.jsonl")
		startedAt := time.Now().Add(-2 * time.Hour).Truncate(time.Second)
		writeSimpleJSONL(t, sourceFile, []time.Time{startedAt.Add(1 * time.Minute)})

		writeRecordingState(t, recPath, session.RecordingState{
			AgentID:     "OxSID0",
			AdapterName: "test-mock",
			SessionFile: sourceFile,
			StartedAt:   startedAt,
			// SessionID intentionally empty
		})

		if !recoverRawFromSessionFile(logger, recPath, sessionDir, rawPath) {
			t.Fatal("expected recovery to succeed")
		}

		if got := session.ReadHeaderSessionID(rawPath); got != "" {
			t.Errorf("expected no header SessionID for a legacy state with none, got %q", got)
		}
	})

	t.Run("no_entries_after_start_time", func(t *testing.T) {
		sessionDir := t.TempDir()
		recPath := filepath.Join(sessionDir, recordingMarker)
		rawPath := filepath.Join(sessionDir, artifactRaw)

		sourceFile := filepath.Join(t.TempDir(), "session.jsonl")
		startedAt := time.Date(2026, 1, 10, 12, 0, 0, 0, time.UTC)
		writeSimpleJSONL(t, sourceFile, []time.Time{
			startedAt.Add(-10 * time.Minute), // all before start
			startedAt.Add(-5 * time.Minute),
		})

		writeRecordingState(t, recPath, session.RecordingState{
			AgentID:     "OxNOAF",
			AdapterName: "test-mock",
			SessionFile: sourceFile,
			StartedAt:   startedAt,
		})

		if recoverRawFromSessionFile(logger, recPath, sessionDir, rawPath) {
			t.Error("expected recovery to fail when all entries are before start time")
		}
	})
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
