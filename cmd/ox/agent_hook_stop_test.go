package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- A. Stop hook behavior ---

// TestStopHook_SetsStoppedAtInRecording verifies that the stop hook sets StoppedAt
// in .recording.json by directly exercising the UpdateRecordingStateForAgent flow.
// The full handleStop function also does IPC which requires a daemon, so we test
// the StoppedAt-setting logic directly.
// Failure prevented: daemon can't detect intentional stop vs crash.
func TestStopHook_SetsStoppedAtInRecording(t *testing.T) {
	cacheDir := t.TempDir()
	projectRoot := t.TempDir()
	repoID := "test-repo-stop"

	// create .sageox/config.json with repo_id
	sageoxDir := filepath.Join(projectRoot, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755))
	configContent := `{"config_version":"2","repo_id":"` + repoID + `"}`
	require.NoError(t, os.WriteFile(filepath.Join(sageoxDir, "config.json"), []byte(configContent), 0644))

	// set up environment for session search paths
	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("HOME", cacheDir)
	t.Setenv("XDG_CACHE_HOME", cacheDir)

	agentID := "OxStopTest"
	sessionsBase := filepath.Join(session.GetContextPath(repoID), "sessions")
	sessionPath := filepath.Join(sessionsBase, "2026-04-01T10-00-user-"+agentID)

	state := &session.RecordingState{
		AgentID:     agentID,
		StartedAt:   time.Now().Add(-10 * time.Minute),
		AdapterName: "claude-code",
		SessionPath: sessionPath,
		OutputFile:  filepath.Join(sessionPath, "raw.jsonl"),
	}
	require.NoError(t, session.SaveRecordingState(projectRoot, state))

	// verify StoppedAt is not set initially
	loaded, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
	require.NoError(t, err)
	require.NotNil(t, loaded)
	assert.Nil(t, loaded.StoppedAt, "StoppedAt should be nil before stop")

	// simulate what handleStop does: set StoppedAt
	now := time.Now()
	require.NoError(t, session.UpdateRecordingStateForAgent(projectRoot, agentID, func(s *session.RecordingState) {
		s.StoppedAt = &now
	}))

	// verify StoppedAt is now set
	loaded, err = session.LoadRecordingStateForAgent(projectRoot, agentID)
	require.NoError(t, err)
	require.NotNil(t, loaded)
	require.NotNil(t, loaded.StoppedAt, "StoppedAt should be set after stop")
	assert.WithinDuration(t, now, *loaded.StoppedAt, time.Second)
}

// TestStopHook_NoRecording_NoError verifies that setting StoppedAt on a
// non-existent recording returns an error (which handleStop logs as debug).
// Failure prevented: stop hook crashes when session wasn't recorded.
func TestStopHook_NoRecording_ReturnsErrNotRecording(t *testing.T) {
	cacheDir := t.TempDir()
	projectRoot := t.TempDir()

	sageoxDir := filepath.Join(projectRoot, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755))
	configContent := `{"config_version":"2","repo_id":"test-repo-none"}`
	require.NoError(t, os.WriteFile(filepath.Join(sageoxDir, "config.json"), []byte(configContent), 0644))

	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("HOME", cacheDir)
	t.Setenv("XDG_CACHE_HOME", cacheDir)

	agentID := "OxNoRecording"
	err := session.UpdateRecordingStateForAgent(projectRoot, agentID, func(s *session.RecordingState) {
		now := time.Now()
		s.StoppedAt = &now
	})
	// ErrNotRecording is expected — handleStop logs it as debug and continues
	require.Error(t, err)
	assert.ErrorIs(t, err, session.ErrNotRecording)
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
