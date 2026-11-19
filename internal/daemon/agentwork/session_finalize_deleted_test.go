package agentwork

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/livesession"
	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// deletedOnServer leaves the stream state a live stream writes when SageOx
// answered that the session was deleted, without the hold marker that should
// accompany it: the case where writing the marker failed (C3). The stream
// state is then the only record left that the session must stay local.
func deletedOnServer(t *testing.T, dir string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o755))
	st, err := json.Marshal(map[string]any{
		"conversation_id": "cnv_01934f3e-6b2a-7c3d-8e4f-5a6b7c8d9e0f", "layer_id": "clyr_01934f3e-6b2a-7c3d-8e4f-5a6b7c8d9e10",
		"endpoint": "https://live.test.sageox.ai", "status": "deleted", "deleted_on_server": true,
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "stream-state.json"), st, 0o600))
	require.True(t, livesession.WasDeletedOnServer(dir), "precondition: the stream state says deleted on server")
	require.False(t, session.IsHeld(dir), "precondition: no hold marker was written")
}

// Failure prevented: a session someone deleted on SageOx while it streamed, on
// a machine where the hold marker could not be written, was recovered as an
// orphan and finalized: summarized and published to the Ledger anyway.
func TestDetectOrphanedForAgent_ServerDeletedSessionStaysLocal(t *testing.T) {
	stale := time.Now().Add(-25 * time.Hour)
	f := newHeldFixture(t)
	f.transcript(t)
	rec, err := json.Marshal(map[string]any{"agent_id": "OxHeLd", "started_at": stale.Format(time.RFC3339),
		"stopped_at": stale.Add(time.Hour).Format(time.RFC3339), "publishing_mode": "auto"})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(f.cacheDir, recordingMarker), rec, 0o644))
	h := NewSessionFinalizeHandlerForTest(slog.New(slog.DiscardHandler))
	h.SetProjectRoot(projectWithPublishing(t, "auto"))
	require.Len(t, h.DetectOrphanedForAgent(f.ledgerPath, "OxHeLd", 0), 1, "control: the same orphan is finalized when nobody deleted it")

	require.NoError(t, os.WriteFile(filepath.Join(f.cacheDir, recordingMarker), rec, 0o644))
	deletedOnServer(t, f.cacheDir)
	items := h.DetectOrphanedForAgent(f.ledgerPath, "OxHeLd", 0)
	assert.Empty(t, items, "a session deleted on SageOx is never finalized")
	assert.FileExists(t, filepath.Join(f.cacheDir, artifactRaw), "the transcript stays local")
}

// Failure prevented: an item already queued (or sent over IPC) for a session
// later deleted on SageOx was summarized and staged into the Ledger, because
// sessionHeldLocally asked only for the hold marker. It must also hold when the
// payload points at the Ledger copy, which never carries the stream state.
func TestFinalizeEntryPoints_DropServerDeletedItem(t *testing.T) {
	f := newHeldFixture(t)
	f.transcript(t)
	h := NewSessionFinalizeHandlerForTest(slog.New(slog.DiscardHandler))
	items := detectCacheOnly(t, h, f.ledgerPath)
	require.Len(t, items, 1, "precondition: undeleted local work is queued")

	deletedOnServer(t, f.cacheDir)

	req, err := h.BuildPrompt(items[0])
	require.NoError(t, err)
	assert.True(t, req.SkipLLM, "no LLM run for a session deleted on SageOx")
	require.NoError(t, h.ProcessResult(items[0], &RunResult{}))
	assert.NoDirExists(t, filepath.Join(f.ledgerPath, "sessions", f.name), "nothing is staged into the Ledger")
	assert.FileExists(t, filepath.Join(f.cacheDir, artifactRaw), "the transcript stays local")

	ledgerCopy := &SessionFinalizePayload{SessionDir: filepath.Join(f.ledgerPath, "sessions", f.name), LedgerPath: f.ledgerPath}
	assert.True(t, sessionHeldLocally(ledgerCopy), "found by name from the Ledger copy too")
}
