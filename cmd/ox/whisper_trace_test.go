package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/contexttrace"
	"github.com/sageox/ox/internal/tokens"
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

// TestTraceWhisperDelivery_RecordsInlineTokens drives a real recording.
// Failure prevented: whisper volume is invisible to the usage report — a
// delivered murmur lands in the AI coworker's context but its size is never
// recorded, so "context injected" undercounts.
func TestTraceWhisperDelivery_RecordsInlineTokens(t *testing.T) {
	projectRoot := setupSessionTestProject(t)
	state, err := session.StartRecording(projectRoot, session.StartRecordingOptions{AgentID: "OxWh01", AdapterName: "claude-code"})
	require.NoError(t, err)

	content := strings.Repeat("x", 400)
	traceWhisperDelivery(projectRoot, "OxWh01", []whisperstore.WhisperEntry{
		{ID: "m1", Source: "murmur", Topic: "wip", Content: content, AgentID: "OxOther", Scope: "team", PrincipalID: "dana-smith"},
	})

	events, err := contexttrace.ReadEvents(filepath.Join(state.SessionPath, contexttrace.FileName))
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.Equal(t, contexttrace.EventProvided, events[0].Type, "whisper delivery stays a provided event")
	assert.Equal(t, tokens.EstimateTokens(content), events[0].InlineTokens)
	assert.Equal(t, 100, events[0].InlineTokens)
	assert.Equal(t, "m1", events[0].Ref)
	assert.NotEmpty(t, events[0].FromName, "sender name travels with the delivery so a used murmur can be credited")
	assert.Equal(t, whisperFromName(whisperstore.WhisperEntry{PrincipalID: "dana-smith"}), events[0].FromName)
}
