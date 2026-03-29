package daemon

import (
	"context"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	whisperstore "github.com/sageox/ox/internal/whisper/store"
)

// sendHeartbeat is a test helper that JSON-encodes a HeartbeatPayload and sends it.
func sendHeartbeat(t *testing.T, hb *HeartbeatHandler, payload HeartbeatPayload) {
	t.Helper()
	data, err := json.Marshal(payload)
	require.NoError(t, err)
	hb.Handle("test-caller", data)
}

// openTestWhisperStore creates a temporary whisper store for testing.
func openTestWhisperStore(t *testing.T) *whisperstore.Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "whisper.db")
	store, err := whisperstore.Open(dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })
	return store
}

func TestMurmurNudgeSource_Name(t *testing.T) {
	store := openTestWhisperStore(t)
	src := NewMurmurNudgeSource(store, NewHeartbeatHandler(slog.Default()), 15*time.Minute, "")
	assert.Equal(t, "murmur-nudge", src.Name())
}

func TestMurmurNudgeSource_Interval(t *testing.T) {
	store := openTestWhisperStore(t)
	src := NewMurmurNudgeSource(store, NewHeartbeatHandler(slog.Default()), 15*time.Minute, "")
	assert.Equal(t, 1*time.Minute, src.Interval())
}

func TestMurmurNudgeSource_MinimumInterval(t *testing.T) {
	// interval below 10 minutes should be clamped
	store := openTestWhisperStore(t)
	src := NewMurmurNudgeSource(store, NewHeartbeatHandler(slog.Default()), 5*time.Minute, "")
	assert.Equal(t, 10*time.Minute, src.interval)
}

func TestMurmurNudgeSource_NilDeps(t *testing.T) {
	src := &MurmurNudgeSource{}
	entries := src.Produce(context.Background())
	assert.Empty(t, entries)
}

func TestMurmurNudgeSource_NoActiveAgents(t *testing.T) {
	projectRoot := initMurmuringProject(t)
	store := openTestWhisperStore(t)
	hb := NewHeartbeatHandler(slog.Default())
	src := NewMurmurNudgeSource(store, hb, 15*time.Minute, projectRoot)

	entries := src.Produce(context.Background())
	assert.Empty(t, entries)
}

func TestMurmurNudgeSource_ProducesNudge(t *testing.T) {
	projectRoot := initMurmuringProject(t)
	store := openTestWhisperStore(t)
	hb := NewHeartbeatHandler(slog.Default())
	src := NewMurmurNudgeSource(store, hb, 15*time.Minute, projectRoot)

	// send enough heartbeats to trigger early nudge (2 heartbeats threshold)
	sendHeartbeat(t, hb, HeartbeatPayload{AgentID: "OxTest1", Timestamp: time.Now()})
	sendHeartbeat(t, hb, HeartbeatPayload{AgentID: "OxTest1", Timestamp: time.Now()})

	entries := src.Produce(context.Background())
	require.Len(t, entries, 1)

	e := entries[0]
	assert.Equal(t, "ledger", e.Scope)
	assert.Equal(t, "murmur-nudge", e.Topic)
	assert.Equal(t, "auto-murmur", e.Source)
	assert.Contains(t, e.Content, "ACTION REQUIRED")
	assert.Contains(t, e.Content, "ox murmur")
	assert.Equal(t, "OxTest1", e.AgentID)
	assert.NotEmpty(t, e.ID)
}

func TestMurmurNudgeSource_SkipsAfterMurmur(t *testing.T) {
	projectRoot := initMurmuringProject(t)
	store := openTestWhisperStore(t)
	hb := NewHeartbeatHandler(slog.Default())
	src := NewMurmurNudgeSource(store, hb, 15*time.Minute, projectRoot)

	sendHeartbeat(t, hb, HeartbeatPayload{AgentID: "OxTest2", Timestamp: time.Now()})
	sendHeartbeat(t, hb, HeartbeatPayload{AgentID: "OxTest2", Timestamp: time.Now()})

	entries := src.Produce(context.Background())
	require.Len(t, entries, 1)

	// simulate: agent murmurs (relay stores it in whisper db with source="murmur")
	err := store.Add(whisperstore.WhisperEntry{
		ID:        "murmur-test-1",
		Scope:     "ledger",
		Source:    "murmur",
		Topic:     "wip",
		Content:   "working on auth",
		CreatedAt: time.Now(),
		AgentID:   "OxTest2",
	})
	require.NoError(t, err)

	// store the first nudge so cooldown check works
	err = store.Add(entries[0])
	require.NoError(t, err)

	entries = src.Produce(context.Background())
	assert.Empty(t, entries, "should skip: agent murmured recently")
}

func TestMurmurNudgeSource_NudgeCooldown(t *testing.T) {
	projectRoot := initMurmuringProject(t)
	store := openTestWhisperStore(t)
	hb := NewHeartbeatHandler(slog.Default())
	src := NewMurmurNudgeSource(store, hb, 15*time.Minute, projectRoot)

	sendHeartbeat(t, hb, HeartbeatPayload{AgentID: "OxTest3", Timestamp: time.Now()})
	sendHeartbeat(t, hb, HeartbeatPayload{AgentID: "OxTest3", Timestamp: time.Now()})

	// first nudge
	entries := src.Produce(context.Background())
	require.Len(t, entries, 1)

	// store the nudge in the DB (simulates what the scheduler does)
	err := store.Add(entries[0])
	require.NoError(t, err)

	// second produce immediately — should be suppressed by cooldown
	entries = src.Produce(context.Background())
	assert.Empty(t, entries, "should be suppressed by nudge cooldown")
}

func TestMurmurNudgeSource_SurvivesDaemonRestart(t *testing.T) {
	// This test verifies the key improvement: nudge state persists in whisper.db
	// so a "restarted" source (new instance, same store) respects prior nudges.
	projectRoot := initMurmuringProject(t)
	store := openTestWhisperStore(t)
	hb := NewHeartbeatHandler(slog.Default())

	// first "daemon lifetime" — produce a nudge
	src1 := NewMurmurNudgeSource(store, hb, 15*time.Minute, projectRoot)
	sendHeartbeat(t, hb, HeartbeatPayload{AgentID: "OxRestart", Timestamp: time.Now()})
	sendHeartbeat(t, hb, HeartbeatPayload{AgentID: "OxRestart", Timestamp: time.Now()})

	entries := src1.Produce(context.Background())
	require.Len(t, entries, 1)
	err := store.Add(entries[0])
	require.NoError(t, err)

	// "restart" — new source instance, same store, same heartbeat handler
	src2 := NewMurmurNudgeSource(store, hb, 15*time.Minute, projectRoot)

	// should NOT re-nudge because store has recent nudge
	entries = src2.Produce(context.Background())
	assert.Empty(t, entries, "restarted source should see prior nudge in store")
}

func TestHeartbeatHandler_AgentHeartbeatCallback(t *testing.T) {
	hb := NewHeartbeatHandler(slog.Default())

	var received []string
	hb.SetAgentHeartbeatCallback(func(agentID string) {
		received = append(received, agentID)
	})

	sendHeartbeat(t, hb, HeartbeatPayload{
		AgentID:   "OxCb1",
		Timestamp: time.Now(),
	})
	sendHeartbeat(t, hb, HeartbeatPayload{
		AgentID:   "OxCb2",
		Timestamp: time.Now(),
	})
	// non-agent heartbeat should not trigger callback
	sendHeartbeat(t, hb, HeartbeatPayload{
		Timestamp: time.Now(),
	})

	assert.Equal(t, []string{"OxCb1", "OxCb2"}, received)
}
