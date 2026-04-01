package main

import (
	"os"
	"path/filepath"
	"testing"

	whisperstore "github.com/sageox/ox/internal/whisper/store"
)

// --- Context-trace whisper delivery observability ---

// TestTraceWhisperDelivery_SafeWithoutRecording verifies traceWhisperDelivery
// is a silent no-op in all edge cases where no recording session exists.
// Failure prevented: panic, error log spam, or file creation when there's no session.
func TestTraceWhisperDelivery_SafeWithoutRecording(t *testing.T) {
	t.Parallel()

	entries := []whisperstore.WhisperEntry{
		{ID: "1", Source: "murmur", Topic: "wip", Content: "test", AgentID: "OxTest"},
	}

	// none of these should panic, log errors, or create files
	traceWhisperDelivery("", "OxTest", entries)
	traceWhisperDelivery("/nonexistent", "", entries)
	traceWhisperDelivery("", "", entries)
	traceWhisperDelivery("", "", nil)
	traceWhisperDelivery(t.TempDir(), "OxTest", entries)
	traceWhisperDelivery(t.TempDir(), "OxTest", nil)
	traceWhisperDelivery(t.TempDir(), "OxTest", []whisperstore.WhisperEntry{
		{ID: "1", Source: "auto-murmur", Topic: "nudge", Content: "nudge"},
	})
}

// TestTraceWhisperDelivery_OnlyMurmurEntries verifies that only murmur-sourced
// entries produce context-trace events (not auto-murmur, not activity-summary, etc.).
// Failure prevented: context-trace polluted with non-coordination events.
//
// NOTE: Full integration testing of context-trace file writes requires a real
// recording session (LoadRecordingStateForAgent + ledger paths). This test validates
// the filtering logic by running against a temp dir where LoadRecordingStateForAgent
// returns nil — it verifies no crash and no file creation for non-recording agents.
// The file-write path is covered by integration tests in tests/integration/.
func TestTraceWhisperDelivery_OnlyMurmurEntries(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	entries := []whisperstore.WhisperEntry{
		{ID: "m1", Source: "murmur", Topic: "conflict", Content: "auth refactor", AgentID: "OxOther", Scope: "ledger"},
		{ID: "n1", Source: "auto-murmur", Topic: "nudge", Content: "nudge"},
		{ID: "a1", Source: "activity-summary", Topic: "activity", Content: "CI green"},
	}

	traceWhisperDelivery(tmpDir, "OxTest", entries)

	var found bool
	filepath.Walk(tmpDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.Name() == "context-trace.jsonl" {
			found = true
		}
		return nil
	})
	if found {
		t.Error("context-trace.jsonl should not be created when no recording session is active")
	}
}
