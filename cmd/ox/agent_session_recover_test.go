package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/agentinstance"
	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSessionRecoverFromCachePreservesOriginalSessionName(t *testing.T) {
	projectRoot := t.TempDir()
	runGit(t, projectRoot, "init")
	t.Chdir(projectRoot)
	t.Setenv("OX_PROJECT_ROOT", projectRoot)
	t.Setenv("SAGEOX_ENDPOINT", "https://test-only-no-creds.invalid")

	require.NoError(t, os.MkdirAll(filepath.Join(projectRoot, ".sageox"), 0o755))

	const originalName = "2001-02-03T04-05-historic-OxRcvr"
	cacheRoot := filepath.Join(projectRoot, "sessions")
	sessionPath := filepath.Join(cacheRoot, originalName)
	require.NoError(t, os.MkdirAll(sessionPath, 0o755))
	raw := []byte(`{"type":"header","metadata":{"agent_id":"OxRcvr","session_id":"ses_019d0000-0000-7000-8000-000000000916","created_at":"2001-02-03T04:05:00Z"}}` + "\n" +
		`{"type":"user","content":"please recover the original name"}` + "\n" +
		`{"type":"assistant","content":"recovered"}` + "\n" +
		`{"type":"footer","entry_count":2}` + "\n")
	require.NoError(t, os.WriteFile(filepath.Join(sessionPath, ledgerFileRaw), raw, 0o600))

	startedAt := time.Date(2001, 2, 3, 4, 5, 0, 0, time.UTC)
	require.NoError(t, session.SaveRecordingState(projectRoot, &session.RecordingState{
		AgentID:     "OxRcvr",
		AdapterName: "claude-code",
		SessionID:   "ses_019d0000-0000-7000-8000-000000000916",
		SessionPath: sessionPath,
		StartedAt:   startedAt,
	}))

	var recoverErr error
	out := captureRealStdout(t, func() {
		recoverErr = runAgentSessionRecover(&agentinstance.Instance{AgentID: "OxRcvr"})
	})
	require.NoError(t, recoverErr)

	var got sessionRecoverOutput
	require.NoError(t, json.Unmarshal(out, &got))
	assert.Equal(t, originalName, got.SessionName,
		"recovery must report the original cache directory name, not a fresh now()-based identity")
	assert.Equal(t, filepath.Join(sessionPath, ledgerFileRaw), got.RawPath)
	assert.False(t, got.Uploaded, "precondition: no ledger is configured, so the test isolates recovery naming")

	entries, readErr := os.ReadDir(cacheRoot)
	require.NoError(t, readErr)
	require.Len(t, entries, 1, "recovery must not create a second date-based cache identity")
	assert.Equal(t, originalName, entries[0].Name())
	assert.NoFileExists(t, filepath.Join(sessionPath, ".recording.json"), "recovery still clears stale recording state")
}
