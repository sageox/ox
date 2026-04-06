package main

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- A. Stop hook drain-only behavior ---

// TestHandleStop_DoesNotSetStoppedAt verifies that handleStop (which fires on
// every response turn) does NOT set StoppedAt on the recording.
// Failure prevented: active recording marked as stopped after first response turn,
// which caused recordings to appear stopped 2-9 seconds after creation.
func TestHandleStop_DoesNotSetStoppedAt(t *testing.T) {
	projectRoot, repoID := setupTestProject(t)

	agentID := "OxStopTest"
	createActiveRecording(t, projectRoot, repoID, agentID)

	// verify StoppedAt is not set initially
	loaded, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
	require.NoError(t, err)
	require.NotNil(t, loaded)
	assert.Nil(t, loaded.StoppedAt, "StoppedAt should be nil before stop")

	// simulate handleStop being called (fires on every response turn)
	ctx := &HookContext{
		Phase:       phaseStop,
		AgentType:   "claude-code",
		ProjectRoot: projectRoot,
		Marker:      &SessionMarker{AgentID: agentID},
	}
	err = handleStop(ctx)
	require.NoError(t, err)

	// verify StoppedAt was NOT set — handleStop is drain-only
	loaded, err = session.LoadRecordingStateForAgent(projectRoot, agentID)
	require.NoError(t, err)
	require.NotNil(t, loaded, "recording should still exist")
	assert.Nil(t, loaded.StoppedAt, "handleStop must not set StoppedAt (fires every turn)")
}

// TestHandleStop_RecordingSurvivesMultipleTurns verifies that multiple
// handleStop calls (simulating multiple response turns) do not degrade
// the recording state.
// Failure prevented: recording state corrupted by repeated stop hook calls.
func TestHandleStop_RecordingSurvivesMultipleTurns(t *testing.T) {
	projectRoot, repoID := setupTestProject(t)

	agentID := "OxMultiTurn"
	createActiveRecording(t, projectRoot, repoID, agentID)

	ctx := &HookContext{
		Phase:       phaseStop,
		AgentType:   "claude-code",
		ProjectRoot: projectRoot,
		Marker:      &SessionMarker{AgentID: agentID},
	}

	// simulate 5 response turns
	for range 5 {
		require.NoError(t, handleStop(ctx))
	}

	// verify recording is still intact
	state, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
	require.NoError(t, err)
	require.NotNil(t, state, "recording should survive multiple stop hook calls")
	assert.Nil(t, state.StoppedAt, "StoppedAt should remain nil across multiple turns")
	assert.Equal(t, agentID, state.AgentID)
}

// TestHandleStop_NoRecording_NoError verifies that handleStop gracefully
// handles the case where no recording exists.
func TestHandleStop_NoRecording_NoError(t *testing.T) {
	ctx := &HookContext{
		Phase:       phaseStop,
		AgentType:   "claude-code",
		ProjectRoot: t.TempDir(),
	}
	err := handleStop(ctx)
	assert.NoError(t, err)
}

// TestStoppedAt_OnlySetByExplicitStop verifies that StoppedAt can still be set
// via UpdateRecordingStateForAgent (as used by the explicit CLI session stop).
// This ensures the explicit stop path still works after handleStop became drain-only.
func TestStoppedAt_OnlySetByExplicitStop(t *testing.T) {
	projectRoot, repoID := setupTestProject(t)

	agentID := "OxExplicitStop"
	createActiveRecording(t, projectRoot, repoID, agentID)

	// simulate explicit session stop setting StoppedAt
	now := time.Now()
	require.NoError(t, session.UpdateRecordingStateForAgent(projectRoot, agentID, func(s *session.RecordingState) {
		s.StoppedAt = &now
	}))

	loaded, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
	require.NoError(t, err)
	require.NotNil(t, loaded)
	require.NotNil(t, loaded.StoppedAt, "explicit stop should set StoppedAt")
	assert.WithinDuration(t, now, *loaded.StoppedAt, time.Second)
}

// --- B. deriveLedgerPath ---

// TestDeriveLedgerPath verifies ledger path extraction from session paths.
func TestDeriveLedgerPath(t *testing.T) {
	tests := []struct {
		name        string
		sessionPath string
		want        string
	}{
		{
			name:        "cache sessions path",
			sessionPath: "/home/user/.local/share/sageox/endpoint/ledgers/repo-id/.sageox/cache/sessions/2026-04-01T10-00-user-OxTest",
			want:        "/home/user/.local/share/sageox/endpoint/ledgers/repo-id",
		},
		{
			name:        "git-tracked sessions path",
			sessionPath: "/home/user/.local/share/sageox/endpoint/ledgers/repo-id/sessions/2026-04-01T10-00-user-OxTest",
			want:        "/home/user/.local/share/sageox/endpoint/ledgers/repo-id",
		},
		{
			name:        "unknown path structure",
			sessionPath: "/tmp/random/path",
			want:        "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := deriveLedgerPath(tt.sessionPath)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestDeriveLedgerPath_RecordingJSON verifies that the session path from
// recording state produces a valid ledger path.
func TestDeriveLedgerPath_RecordingJSON(t *testing.T) {
	ledgerPath := "/data/ledgers/repo123"
	sessionPath := filepath.Join(ledgerPath, ".sageox", "cache", "sessions", "2026-04-01T10-00-user-OxABC")

	state := session.RecordingState{
		AgentID:     "OxABC",
		SessionPath: sessionPath,
	}
	data, err := json.Marshal(state)
	require.NoError(t, err)

	var loaded session.RecordingState
	require.NoError(t, json.Unmarshal(data, &loaded))

	got := deriveLedgerPath(loaded.SessionPath)
	assert.Equal(t, ledgerPath, got)
}
