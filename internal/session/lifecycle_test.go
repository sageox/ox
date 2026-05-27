package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestClassifySession_Suspended verifies ADR-020 active-pause is reported
// when the agent is alive and SuspendedAt is set.
func TestClassifySession_Suspended(t *testing.T) {
	alivePID := os.Getpid()
	now := time.Now()

	info := SessionInfo{
		Recording:   true,
		ParentPID:   alivePID,
		CreatedAt:   now.Add(-2 * time.Minute),
		HasRawData:  true,
		EntryCount:  10,
		SuspendedAt: &now,
	}

	got := ClassifySession(info, false)
	assert.Equal(t, StatusSuspended, got)
}

// TestClassifySession_SuspendedDeadAgent verifies that a suspended-then-PID-dead
// session past grace falls through to the orphan path (not Suspended). The
// orphan path finalizes the session with the paused range honored at upload.
func TestClassifySession_SuspendedDeadAgent(t *testing.T) {
	now := time.Now()

	info := SessionInfo{
		Recording:   true,
		ParentPID:   1, // init never matches in practice; isPIDAlive returns true on most systems though
		CreatedAt:   now.Add(-30 * time.Minute),
		HasRawData:  true,
		EntryCount:  10,
		SuspendedAt: &now,
	}

	// Use ParentPID=0 to force the no-PID branch (age-based abandonment).
	info.ParentPID = 0
	got := ClassifySession(info, false)
	assert.Equal(t, StatusOrphan, got, "PID-dead past grace must classify as orphan even when SuspendedAt is set")
}

// TestClassifySession_RecordingUnchanged verifies the non-suspended live path
// still returns StatusRecording (no SuspendedAt means classic recording).
func TestClassifySession_RecordingUnchanged(t *testing.T) {
	info := SessionInfo{
		Recording:  true,
		ParentPID:  os.Getpid(),
		CreatedAt:  time.Now().Add(-1 * time.Minute),
		HasRawData: true,
		EntryCount: 5,
	}
	got := ClassifySession(info, false)
	assert.Equal(t, StatusRecording, got)
}

// TestRecordingState_BackwardCompatibleLoad verifies that .recording.json
// files written without the new ADR-020 fields (Lifecycle, SuspendedAt,
// PauseCount, InheritedPause, InheritedFromSession) still load cleanly.
func TestRecordingState_BackwardCompatibleLoad(t *testing.T) {
	// minimal pre-ADR-020 .recording.json content
	legacyJSON := `{
		"agent_id": "Oxlegacy",
		"started_at": "2026-05-26T12:00:00Z",
		"adapter_name": "claude-code",
		"session_file": "/tmp/source.jsonl",
		"output_file": "/tmp/out.jsonl",
		"session_path": "/tmp/sessions/abc",
		"entry_count": 5,
		"parent_pid": 12345
	}`

	var state RecordingState
	err := json.Unmarshal([]byte(legacyJSON), &state)
	require.NoError(t, err, "legacy .recording.json must unmarshal without error")

	assert.Equal(t, "Oxlegacy", state.AgentID)
	assert.Equal(t, "claude-code", state.AdapterName)
	assert.Equal(t, 5, state.EntryCount)
	assert.Nil(t, state.SuspendedAt, "SuspendedAt must default to nil")
	assert.Empty(t, state.Lifecycle, "Lifecycle must default to empty slice")
	assert.Zero(t, state.PauseCount)
	assert.False(t, state.InheritedPause)
	assert.Empty(t, state.InheritedFromSession)
}

// TestRecordingState_RoundTripWithLifecycle verifies save+load of a state
// carrying the new ADR-020 fields preserves all values.
func TestRecordingState_RoundTripWithLifecycle(t *testing.T) {
	tmp := t.TempDir()
	now := time.Now().UTC().Truncate(time.Second)
	sessionPath := filepath.Join(tmp, "sessions", "Oxa7b3-session")

	state := &RecordingState{
		AgentID:     "Oxa7b3",
		StartedAt:   now,
		AdapterName: "claude-code",
		SessionFile: filepath.Join(tmp, "source.jsonl"),
		OutputFile:  filepath.Join(sessionPath, "raw.jsonl"),
		SessionPath: sessionPath,
		EntryCount:  42,
		SuspendedAt: &now,
		PauseCount:  2,
		Lifecycle: []LifecycleEvent{
			{Action: LifecycleActionStart, At: now, Seq: 0},
			{Action: LifecycleActionPause, At: now.Add(5 * time.Minute), Seq: 18},
			{Action: LifecycleActionResume, At: now.Add(10 * time.Minute), Seq: 18, Reason: "user-resumed"},
			{Action: LifecycleActionPause, At: now.Add(15 * time.Minute), Seq: 42},
		},
		InheritedPause:       true,
		InheritedFromSession: "2026-05-26T11-00-ryan-Oxa7b3",
	}

	err := SaveRecordingState(tmp, state)
	require.NoError(t, err)

	// reload via the canonical loader
	loaded, err := LoadRecordingState(tmp)
	require.NoError(t, err)
	require.NotNil(t, loaded)

	assert.Equal(t, state.AgentID, loaded.AgentID)
	assert.Equal(t, state.PauseCount, loaded.PauseCount)
	require.NotNil(t, loaded.SuspendedAt)
	assert.True(t, loaded.SuspendedAt.Equal(*state.SuspendedAt))
	assert.Equal(t, state.InheritedPause, loaded.InheritedPause)
	assert.Equal(t, state.InheritedFromSession, loaded.InheritedFromSession)

	require.Len(t, loaded.Lifecycle, 4)
	assert.Equal(t, LifecycleActionStart, loaded.Lifecycle[0].Action)
	assert.Equal(t, LifecycleActionPause, loaded.Lifecycle[1].Action)
	assert.Equal(t, 18, loaded.Lifecycle[1].Seq)
	assert.Equal(t, LifecycleActionResume, loaded.Lifecycle[2].Action)
	assert.Equal(t, "user-resumed", loaded.Lifecycle[2].Reason)
}
