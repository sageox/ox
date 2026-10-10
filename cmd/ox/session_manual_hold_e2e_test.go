package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Customer promise (GH #1093, #1019): a session I hold stays on my machine —
// not summarized, not uploaded, not deleted — until I run
// `ox session upload <name>`, which publishes it.
//
// Starts from what a manual stop leaves in the Ledger cache (transcript plus
// .held); that the real stop and the exit hooks write the hold is proven in
// agent_session_manual_publish_test.go and agent_hook_end_test.go. Everything
// after it is real: the daemon's finalize pass with an LLM that would publish
// a summary, doctor's upload retry, the publish command, a bare remote, and
// LFS over HTTP.
func TestHeldSession_StaysLocalUntilExplicitUpload(t *testing.T) {
	f := newDownloadLedgerFixture(t)
	const name = "2026-10-05T10-00-devon-OxDeVn"
	raw := []byte(`{"metadata":{"schema_version":"1","agent_type":"claude-code","username":"devon","agent_id":"OxDeVn"},"type":"header"}
{"type":"user","content":"Refactor the ledger sync retry loop so failed pushes back off exponentially, and keep this session on my machine until I decide to share it.","seq":1}
{"type":"assistant","content":"Understood: the session stays local until you publish it explicitly.","seq":2}
{"entry_count":2}
`)
	cacheDir := filepath.Join(f.ledgerPath, ".sageox", "cache", "sessions", name)
	require.NoError(t, os.MkdirAll(cacheDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cacheDir, ledgerFileRaw), raw, 0o644))
	require.NoError(t, session.WriteHoldMarker(cacheDir, session.HoldManualPublishing, "session_stop"))
	remoteBefore := runGit(t, f.barePath, "rev-parse", "HEAD")

	// --- held: two daemon passes and doctor's retry leave it alone ---
	for pass := 0; pass < 2; pass++ {
		queued, llmRuns := runFinalizePass(t, f.projectRoot, f.ledgerPath)
		require.Zero(t, llmRuns, "a held session must never be summarized")
		require.Zero(t, queued, "a held session must never be queued")
	}
	_ = checkSessionUploadRetry()

	got, err := os.ReadFile(filepath.Join(cacheDir, ledgerFileRaw))
	require.NoError(t, err)
	assert.Equal(t, string(raw), string(got), "the transcript is untouched")
	assert.NoFileExists(t, filepath.Join(cacheDir, "summary.json"))
	assert.NoDirExists(t, filepath.Join(f.ledgerPath, "sessions", name), "nothing reached the shared tree")
	assert.Equal(t, remoteBefore, runGit(t, f.barePath, "rev-parse", "HEAD"), "nothing reached the remote")
	assert.True(t, session.IsHeld(cacheDir))

	// --- explicit publish ---
	require.NoError(t, sessionUploadCmd.RunE(sessionUploadCmd, []string{"OxDeVn"}))

	assert.NotEqual(t, remoteBefore, runGit(t, f.barePath, "rev-parse", "HEAD"), "the publish reached the remote")
	runGit(t, f.ledgerPath, "fetch", "--quiet", f.barePath)
	ledgerDir := filepath.Join(f.ledgerPath, "sessions", name)
	meta, err := lfs.ReadSessionMeta(ledgerDir)
	require.NoError(t, err)
	assert.False(t, meta.IsDraft(), "published, not a placeholder")
	sum := sha256.Sum256(raw)
	require.Contains(t, meta.Files, ledgerFileRaw)
	assert.Equal(t, hex.EncodeToString(sum[:]), meta.Files[ledgerFileRaw].BareOID(), "the remote points at exactly this transcript")
	assert.True(t, lfs.IsPointerFile(filepath.Join(ledgerDir, ledgerFileRaw)), "the shared tree carries a pointer, never the bytes")
	assert.False(t, session.IsHeld(cacheDir), "publishing releases the hold")

	// --- and only now the daemon summarizes it, once ---
	queued, llmRuns := runFinalizePass(t, f.projectRoot, f.ledgerPath)
	assert.Equal(t, 1, queued, "the published session is queued for its summary")
	assert.Equal(t, 1, llmRuns)
}
