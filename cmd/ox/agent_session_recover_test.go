package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/agentinstance"
	"github.com/sageox/ox/internal/lfs"
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

func TestSessionRecoverFromCacheRefusesExistingSessionIDCollision(t *testing.T) {
	projectRoot, ledgerPath := setupLedgerProject(t)
	runGit(t, ledgerPath, "init")
	runGit(t, ledgerPath, "config", "user.email", "test@example.com")
	runGit(t, ledgerPath, "config", "user.name", "Test")
	t.Chdir(projectRoot)
	t.Setenv("OX_PROJECT_ROOT", projectRoot)
	t.Setenv("SAGEOX_ENDPOINT", "https://test-only-no-creds.invalid")

	const (
		sessionName       = "2001-02-03T04-05-historic-OxRcvr"
		recoveredID       = "ses_019d0000-0000-7000-8000-000000000916"
		existingID        = "ses_019d0000-0000-7000-8000-000000000999"
		existingRaw       = "existing finalized raw sentinel\n"
		existingExtraName = "sentinel.txt"
		existingExtra     = "existing finalized sidecar sentinel\n"
	)

	ledgerSessionDir := filepath.Join(ledgerPath, "sessions", sessionName)
	require.NoError(t, os.MkdirAll(ledgerSessionDir, 0o755))
	require.NoError(t, lfs.WriteSessionMetaOnly(ledgerSessionDir, &lfs.SessionMeta{
		SessionName: sessionName,
		SessionID:   existingID,
		AgentID:     "OxOther",
		AgentType:   "claude-code",
		CreatedAt:   time.Date(2001, 2, 3, 4, 5, 0, 0, time.UTC),
		Title:       "existing finalized session",
		EntryCount:  1,
		Files:       map[string]lfs.FileRef{ledgerFileRaw: lfs.NewGitFileRef(int64(len(existingRaw)))},
	}))
	require.NoError(t, os.WriteFile(filepath.Join(ledgerSessionDir, ledgerFileRaw), []byte(existingRaw), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(ledgerSessionDir, existingExtraName), []byte(existingExtra), 0o600))
	beforeMeta, err := os.ReadFile(filepath.Join(ledgerSessionDir, "meta.json"))
	require.NoError(t, err)
	beforeRaw, err := os.ReadFile(filepath.Join(ledgerSessionDir, ledgerFileRaw))
	require.NoError(t, err)
	beforeExtra, err := os.ReadFile(filepath.Join(ledgerSessionDir, existingExtraName))
	require.NoError(t, err)
	runGit(t, ledgerPath, "add", ".")
	runGit(t, ledgerPath, "commit", "--no-verify", "-m", "existing finalized session")
	beforeHead := runGit(t, ledgerPath, "rev-parse", "HEAD")

	cacheRoot := filepath.Join(projectRoot, "sessions")
	sessionPath := filepath.Join(cacheRoot, sessionName)
	require.NoError(t, os.MkdirAll(sessionPath, 0o755))
	recoveredRaw := []byte(`{"type":"header","metadata":{"agent_id":"OxRcvr","session_id":"` + recoveredID + `","created_at":"2001-02-03T04:05:00Z"}}` + "\n" +
		`{"type":"user","content":"please recover the original name"}` + "\n" +
		`{"type":"assistant","content":"recovered"}` + "\n" +
		`{"type":"footer","entry_count":2}` + "\n")
	require.NoError(t, os.WriteFile(filepath.Join(sessionPath, ledgerFileRaw), recoveredRaw, 0o600))
	require.NoError(t, session.SaveRecordingState(projectRoot, &session.RecordingState{
		AgentID:     "OxRcvr",
		AdapterName: "claude-code",
		SessionID:   recoveredID,
		SessionPath: sessionPath,
		StartedAt:   time.Date(2001, 2, 3, 4, 5, 0, 0, time.UTC),
	}))

	var recoverErr error
	out := captureRealStdout(t, func() {
		recoverErr = runAgentSessionRecover(&agentinstance.Instance{AgentID: "OxRcvr"})
	})
	require.Error(t, recoverErr)
	assert.Contains(t, recoverErr.Error(), "refusing to recover session")
	assert.Contains(t, recoverErr.Error(), existingID)
	assert.Contains(t, recoverErr.Error(), recoveredID)
	assert.Empty(t, out, "a refused collision must not emit success JSON")

	afterMeta, err := os.ReadFile(filepath.Join(ledgerSessionDir, "meta.json"))
	require.NoError(t, err)
	afterRaw, err := os.ReadFile(filepath.Join(ledgerSessionDir, ledgerFileRaw))
	require.NoError(t, err)
	afterExtra, err := os.ReadFile(filepath.Join(ledgerSessionDir, existingExtraName))
	require.NoError(t, err)
	assert.Equal(t, beforeMeta, afterMeta)
	assert.Equal(t, beforeRaw, afterRaw)
	assert.Equal(t, beforeExtra, afterExtra)
	assert.Equal(t, beforeHead, runGit(t, ledgerPath, "rev-parse", "HEAD"), "recovery must not commit on collision")
	assert.Empty(t, runGit(t, ledgerPath, "status", "--porcelain", "--", "sessions/"+sessionName),
		"recovery must not stage or mutate the collided ledger directory")
	assert.FileExists(t, filepath.Join(sessionPath, ".recording.json"), "refused recovery must keep state for a safe retry")
}

func TestSessionRecoverFromCacheRefusesLegacyFinalizedSessionCollision(t *testing.T) {
	projectRoot, ledgerPath := setupLedgerProject(t)
	runGit(t, ledgerPath, "init")
	runGit(t, ledgerPath, "config", "user.email", "test@example.com")
	runGit(t, ledgerPath, "config", "user.name", "Test")
	t.Chdir(projectRoot)
	t.Setenv("OX_PROJECT_ROOT", projectRoot)
	t.Setenv("SAGEOX_ENDPOINT", "https://test-only-no-creds.invalid")

	const (
		sessionName = "2001-02-03T04-05-historic-OxRcvr"
		recoveredID = "ses_019d0000-0000-7000-8000-000000000916"
		existingRaw = "existing legacy finalized raw sentinel\n"
	)

	ledgerSessionDir := filepath.Join(ledgerPath, "sessions", sessionName)
	require.NoError(t, os.MkdirAll(ledgerSessionDir, 0o755))
	require.NoError(t, lfs.WriteSessionMetaOnly(ledgerSessionDir, &lfs.SessionMeta{
		SessionName: sessionName,
		AgentID:     "OxLegacy",
		AgentType:   "claude-code",
		CreatedAt:   time.Date(2001, 2, 3, 4, 5, 0, 0, time.UTC),
		Title:       "existing legacy finalized session",
		EntryCount:  1,
		Files:       map[string]lfs.FileRef{ledgerFileRaw: lfs.NewGitFileRef(int64(len(existingRaw)))},
	}))
	require.NoError(t, os.WriteFile(filepath.Join(ledgerSessionDir, ledgerFileRaw), []byte(existingRaw), 0o600))
	beforeMeta, err := os.ReadFile(filepath.Join(ledgerSessionDir, "meta.json"))
	require.NoError(t, err)
	require.NotContains(t, string(beforeMeta), `"session_id"`, "test fixture must represent finalized legacy metadata")
	beforeRaw, err := os.ReadFile(filepath.Join(ledgerSessionDir, ledgerFileRaw))
	require.NoError(t, err)
	runGit(t, ledgerPath, "add", ".")
	runGit(t, ledgerPath, "commit", "--no-verify", "-m", "existing legacy finalized session")
	beforeHead := runGit(t, ledgerPath, "rev-parse", "HEAD")

	cacheRoot := filepath.Join(projectRoot, "sessions")
	sessionPath := filepath.Join(cacheRoot, sessionName)
	require.NoError(t, os.MkdirAll(sessionPath, 0o755))
	recoveredRaw := []byte(`{"type":"header","metadata":{"agent_id":"OxRcvr","session_id":"` + recoveredID + `","created_at":"2001-02-03T04:05:00Z"}}` + "\n" +
		`{"type":"user","content":"please recover the original name"}` + "\n" +
		`{"type":"assistant","content":"recovered"}` + "\n" +
		`{"type":"footer","entry_count":2}` + "\n")
	require.NoError(t, os.WriteFile(filepath.Join(sessionPath, ledgerFileRaw), recoveredRaw, 0o600))
	require.NoError(t, session.SaveRecordingState(projectRoot, &session.RecordingState{
		AgentID:     "OxRcvr",
		AdapterName: "claude-code",
		SessionID:   recoveredID,
		SessionPath: sessionPath,
		StartedAt:   time.Date(2001, 2, 3, 4, 5, 0, 0, time.UTC),
	}))

	var recoverErr error
	out := captureRealStdout(t, func() {
		recoverErr = runAgentSessionRecover(&agentinstance.Instance{AgentID: "OxRcvr"})
	})
	require.Error(t, recoverErr)
	assert.Contains(t, recoverErr.Error(), "refusing to recover session")
	assert.Contains(t, recoverErr.Error(), "finalized legacy metadata without a session ID")
	assert.Empty(t, out, "a refused legacy collision must not emit success JSON")

	afterMeta, err := os.ReadFile(filepath.Join(ledgerSessionDir, "meta.json"))
	require.NoError(t, err)
	afterRaw, err := os.ReadFile(filepath.Join(ledgerSessionDir, ledgerFileRaw))
	require.NoError(t, err)
	assert.Equal(t, beforeMeta, afterMeta)
	assert.Equal(t, beforeRaw, afterRaw)
	assert.Equal(t, beforeHead, runGit(t, ledgerPath, "rev-parse", "HEAD"), "recovery must not commit on legacy collision")
	assert.Empty(t, runGit(t, ledgerPath, "status", "--porcelain", "--", "sessions/"+sessionName),
		"recovery must not stage or mutate the collided legacy ledger directory")
	assert.FileExists(t, filepath.Join(sessionPath, ".recording.json"), "refused recovery must keep state for a safe retry")
}
