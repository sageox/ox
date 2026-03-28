package daemon

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sendHeartbeat is a test helper that JSON-encodes a HeartbeatPayload and sends it.
func sendHeartbeat(t *testing.T, hb *HeartbeatHandler, payload HeartbeatPayload) {
	t.Helper()
	data, err := json.Marshal(payload)
	require.NoError(t, err)
	hb.Handle("test-caller", data)
}

func TestMurmurNudgeSource_Name(t *testing.T) {
	src := NewMurmurNudgeSource(NewMurmurNudgeTracker(), NewHeartbeatHandler(slog.Default()), 15*time.Minute, "")
	assert.Equal(t, "murmur-nudge", src.Name())
}

func TestMurmurNudgeSource_Interval(t *testing.T) {
	src := NewMurmurNudgeSource(NewMurmurNudgeTracker(), NewHeartbeatHandler(slog.Default()), 15*time.Minute, "")
	assert.Equal(t, 1*time.Minute, src.Interval())
}

func TestMurmurNudgeSource_MinimumInterval(t *testing.T) {
	// interval below 10 minutes should be clamped
	src := NewMurmurNudgeSource(NewMurmurNudgeTracker(), NewHeartbeatHandler(slog.Default()), 5*time.Minute, "")
	assert.Equal(t, 10*time.Minute, src.interval)
}

func TestMurmurNudgeSource_NilDeps(t *testing.T) {
	src := &MurmurNudgeSource{}
	entries := src.Produce(context.Background())
	assert.Empty(t, entries)
}

func TestMurmurNudgeSource_NoActiveAgents(t *testing.T) {
	projectRoot := initMurmuringProject(t)
	tracker := NewMurmurNudgeTracker()
	hb := NewHeartbeatHandler(slog.Default())
	src := NewMurmurNudgeSource(tracker, hb, 15*time.Minute, projectRoot)

	entries := src.Produce(context.Background())
	assert.Empty(t, entries)
}

func TestMurmurNudgeSource_ProducesNudge(t *testing.T) {
	projectRoot := initMurmuringProject(t)
	tracker := NewMurmurNudgeTracker()
	hb := NewHeartbeatHandler(slog.Default())
	src := NewMurmurNudgeSource(tracker, hb, 15*time.Minute, projectRoot)

	sendHeartbeat(t, hb, HeartbeatPayload{
		AgentID:   "OxTest1",
		Timestamp: time.Now(),
	})

	// record enough heartbeats to trigger early nudge (2 heartbeats threshold)
	tracker.RecordHeartbeat("OxTest1")
	tracker.RecordHeartbeat("OxTest1")

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
	tracker := NewMurmurNudgeTracker()
	hb := NewHeartbeatHandler(slog.Default())
	src := NewMurmurNudgeSource(tracker, hb, 15*time.Minute, projectRoot)

	sendHeartbeat(t, hb, HeartbeatPayload{
		AgentID:   "OxTest2",
		Timestamp: time.Now(),
	})

	tracker.RecordHeartbeat("OxTest2")
	tracker.RecordHeartbeat("OxTest2")

	entries := src.Produce(context.Background())
	require.Len(t, entries, 1)

	// agent murmurs — suppress further nudges
	tracker.RecordMurmur("OxTest2")

	entries = src.Produce(context.Background())
	assert.Empty(t, entries)
}

func TestMurmurNudgeSource_NudgeCooldown(t *testing.T) {
	projectRoot := initMurmuringProject(t)
	tracker := NewMurmurNudgeTracker()
	hb := NewHeartbeatHandler(slog.Default())
	src := NewMurmurNudgeSource(tracker, hb, 15*time.Minute, projectRoot)

	sendHeartbeat(t, hb, HeartbeatPayload{
		AgentID:   "OxTest3",
		Timestamp: time.Now(),
	})

	tracker.RecordHeartbeat("OxTest3")
	tracker.RecordHeartbeat("OxTest3")

	// first nudge
	entries := src.Produce(context.Background())
	require.Len(t, entries, 1)

	// second produce immediately — should be suppressed by cooldown
	entries = src.Produce(context.Background())
	assert.Empty(t, entries)
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
