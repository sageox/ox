package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sageox/ox/internal/agentinstance"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/livesession"
	"github.com/sageox/ox/internal/paths"
)

// seedReopenStash writes a reopen stash for sessionID where DropReopenStash looks.
func seedReopenStash(t *testing.T, sessionID string) string {
	t.Helper()
	cnv, err := livesession.ConversationID(sessionID)
	require.NoError(t, err)
	dir := filepath.Join(paths.DataDir(), "live-reopen")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	raw, err := json.Marshal(map[string]any{
		"dir":   t.TempDir(),
		"state": map[string]any{"session_id": sessionID, "conversation_id": cnv},
	})
	require.NoError(t, err)
	meta := filepath.Join(dir, cnv+".json")
	require.NoError(t, os.WriteFile(meta, raw, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, cnv+".spool"), []byte("spool"), 0o600))
	return meta
}

// The reopen copy is the person's way back into a session, so a kill that did
// not remove the ledger copy must leave it; a kill that did must remove it.
func TestAbort_ReopenStashDroppedOnlyAfterLedgerRemoval(t *testing.T) {
	f := newDraftLedgerFixture(t)
	t.Chdir(f.projectRoot)
	cfg = &config.Config{}
	setForceFlag(t, true)
	inst := &agentinstance.Instance{AgentID: "OxKill"}

	const failing = "2026-01-01T00-00-testuser-OxKeep"
	seedFinalizedLedgerSessionWithArtifacts(t, f.ledgerPath, failing)
	commitAndPushFinalized(t, f, failing)
	failingMeta := seedReopenStash(t, sessionScopedID(failing))

	// a held index lock makes `git rm` fail with the session dir still in place
	lock := filepath.Join(f.ledgerPath, ".git", "index.lock")
	require.NoError(t, os.WriteFile(lock, nil, 0o600))
	require.Error(t, runAgentSessionAbort(inst, agentCmd, []string{failing}))
	require.DirExists(t, filepath.Join(f.ledgerPath, "sessions", failing))
	require.FileExists(t, failingMeta, "a failed ledger delete must keep the reopen stash")

	require.NoError(t, os.Remove(lock))
	require.NoError(t, runAgentSessionAbort(inst, agentCmd, []string{failing}))
	require.NoFileExists(t, failingMeta, "a completed kill must drop the reopen stash")
}
