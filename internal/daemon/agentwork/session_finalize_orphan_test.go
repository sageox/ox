package agentwork

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- A. Immediate orphan detection ---

// TestDetectOrphanedForAgent_FindsMatchingRecording verifies that
// DetectOrphanedForAgent finds and queues sessions for a specific agent.
// Failure prevented: agent death doesn't trigger finalization.
func TestDetectOrphanedForAgent_FindsMatchingRecording(t *testing.T) {
	handler := NewSessionFinalizeHandler(slog.Default())

	ledgerPath := t.TempDir()
	sessionsDir := filepath.Join(ledgerPath, ".sageox", "cache", "sessions")
	sessionDir := filepath.Join(sessionsDir, "2026-04-01T10-00-user-OxTest")
	require.NoError(t, os.MkdirAll(sessionDir, 0755))

	// write .recording.json for agent "test-agent"
	recState := map[string]interface{}{
		"agent_id":   "test-agent",
		"started_at": "2026-04-01T10:00:00Z",
		"parent_pid": 99999,
	}
	recData, _ := json.Marshal(recState)
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, ".recording.json"), recData, 0644))

	// write raw.jsonl with substantive content
	rawContent := `{"_meta":{"schema_version":"1","agent_type":"claude-code"}}
{"type":"user","content":"hello","seq":1}
{"type":"assistant","content":"hi there","seq":2}
`
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, "raw.jsonl"), []byte(rawContent), 0644))

	items := handler.DetectOrphanedForAgent(ledgerPath, "test-agent", 99999)
	require.Len(t, items, 1, "should find one orphaned session for test-agent")
	assert.Equal(t, sessionFinalizeType, items[0].Type)
	assert.Contains(t, items[0].DedupKey, "2026-04-01T10-00-user-OxTest")

	// .recording.json should have been removed
	_, err := os.Stat(filepath.Join(sessionDir, ".recording.json"))
	assert.True(t, os.IsNotExist(err), ".recording.json should be removed after detection")
}

// TestDetectOrphanedForAgent_IgnoresOtherAgents verifies that only
// the specified agent's sessions are queued, not other agents'.
// Failure prevented: cross-agent finalization interference.
func TestDetectOrphanedForAgent_IgnoresOtherAgents(t *testing.T) {
	handler := NewSessionFinalizeHandler(slog.Default())

	ledgerPath := t.TempDir()
	sessionsDir := filepath.Join(ledgerPath, ".sageox", "cache", "sessions")

	rawContent := `{"_meta":{"schema_version":"1","agent_type":"claude-code"}}
{"type":"user","content":"hello","seq":1}
{"type":"assistant","content":"hi","seq":2}
`

	// create session for agent A
	sessionDirA := filepath.Join(sessionsDir, "2026-04-01T10-00-user-OxAgentA")
	require.NoError(t, os.MkdirAll(sessionDirA, 0755))
	recA, _ := json.Marshal(map[string]interface{}{"agent_id": "agent-A"})
	require.NoError(t, os.WriteFile(filepath.Join(sessionDirA, ".recording.json"), recA, 0644))
	require.NoError(t, os.WriteFile(filepath.Join(sessionDirA, "raw.jsonl"), []byte(rawContent), 0644))

	// create session for agent B
	sessionDirB := filepath.Join(sessionsDir, "2026-04-01T11-00-user-OxAgentB")
	require.NoError(t, os.MkdirAll(sessionDirB, 0755))
	recB, _ := json.Marshal(map[string]interface{}{"agent_id": "agent-B"})
	require.NoError(t, os.WriteFile(filepath.Join(sessionDirB, ".recording.json"), recB, 0644))
	require.NoError(t, os.WriteFile(filepath.Join(sessionDirB, "raw.jsonl"), []byte(rawContent), 0644))

	items := handler.DetectOrphanedForAgent(ledgerPath, "agent-A", 0)
	require.Len(t, items, 1, "should only find agent-A's session")
	assert.Contains(t, items[0].DedupKey, "OxAgentA")

	// agent-B's .recording.json should still exist
	_, err := os.Stat(filepath.Join(sessionDirB, ".recording.json"))
	assert.NoError(t, err, "agent-B's .recording.json should not be touched")
}

// TestDetectOrphanedForAgent_SkipsAlreadyFinalized verifies that sessions
// with all artifacts already present are not re-queued.
// Failure prevented: redundant finalization of completed sessions.
func TestDetectOrphanedForAgent_SkipsAlreadyFinalized(t *testing.T) {
	handler := NewSessionFinalizeHandler(slog.Default())

	ledgerPath := t.TempDir()
	sessionsDir := filepath.Join(ledgerPath, ".sageox", "cache", "sessions")
	sessionDir := filepath.Join(sessionsDir, "2026-04-01T10-00-user-OxDone")
	require.NoError(t, os.MkdirAll(sessionDir, 0755))

	recState, _ := json.Marshal(map[string]interface{}{"agent_id": "done-agent"})
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, ".recording.json"), recState, 0644))

	rawContent := `{"_meta":{"schema_version":"1","agent_type":"claude-code"}}
{"type":"user","content":"hello","seq":1}
{"type":"assistant","content":"hi","seq":2}
`
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, "raw.jsonl"), []byte(rawContent), 0644))

	// write all required artifacts
	for _, name := range requiredArtifacts {
		require.NoError(t, os.WriteFile(filepath.Join(sessionDir, name), []byte("done"), 0644))
	}

	items := handler.DetectOrphanedForAgent(ledgerPath, "done-agent", 0)
	assert.Empty(t, items, "should not re-queue already finalized session")
}

// TestDetectOrphanedForAgent_EmptyAgentID verifies graceful no-op for empty agent ID.
// Failure prevented: panic or unfiltered scan.
func TestDetectOrphanedForAgent_EmptyAgentID(t *testing.T) {
	handler := NewSessionFinalizeHandler(slog.Default())
	items := handler.DetectOrphanedForAgent(t.TempDir(), "", 0)
	assert.Empty(t, items)
}

// TestDetectOrphanedForAgent_EmptyLedgerPath verifies graceful no-op for empty ledger.
func TestDetectOrphanedForAgent_EmptyLedgerPath(t *testing.T) {
	handler := NewSessionFinalizeHandler(slog.Default())
	items := handler.DetectOrphanedForAgent("", "test-agent", 0)
	assert.Empty(t, items)
}

// --- B. HasPendingWork ---

// TestHasPendingWork_TrueWhenItemsQueued verifies HasPendingWork returns true
// when finalization items are in the queue.
// Failure prevented: daemon exits because it can't see queued work.
func TestHasPendingWork_TrueWhenItemsQueued(t *testing.T) {
	syncSignal := make(chan struct{}, 1)
	runner := NewMockRunner(true)
	loader := func() *config.AgentWorkerConfig {
		return (&config.AgentWorkerConfig{}).WithDefaults()
	}
	mgr := NewManager(runner, slog.Default(), loader, syncSignal, t.TempDir(), "")

	item := &WorkItem{
		Type:     "session-finalize",
		DedupKey: "test-has-pending",
		Priority: 10,
		Payload:  &SessionFinalizePayload{SessionDir: "/tmp/test"},
	}
	mgr.Enqueue(item)

	assert.True(t, mgr.HasPendingWork(), "should report pending work when items are queued")
}

// TestHasPendingWork_FalseWhenEmpty verifies HasPendingWork returns false
// when the queue is empty and nothing is in-flight.
// Failure prevented: daemon stuck alive when nothing to do.
func TestHasPendingWork_FalseWhenEmpty(t *testing.T) {
	syncSignal := make(chan struct{}, 1)
	runner := NewMockRunner(true)
	loader := func() *config.AgentWorkerConfig {
		return (&config.AgentWorkerConfig{}).WithDefaults()
	}
	mgr := NewManager(runner, slog.Default(), loader, syncSignal, t.TempDir(), "")

	assert.False(t, mgr.HasPendingWork(), "should not report pending work when queue is empty")
}
