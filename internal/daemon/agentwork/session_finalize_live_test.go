package agentwork

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/livesession"
	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const liveTestSessionID = "ses_01934f3e-6b2a-7c3d-8e4f-5a6b7c8d9e0f"

// streamLive gives f.cacheDir the files a real live stream leaves there: the
// stream state and spool from livesession itself, plus the stop and abort
// markers.
func streamLive(t *testing.T, f cacheFixture) {
	t.Helper()
	require.NoError(t, os.MkdirAll(f.cacheDir, 0o755))
	ctx := context.Background()
	_, err := livesession.Prepare(ctx, livesession.Config{Endpoint: "https://live.test.sageox.ai"}, f.cacheDir,
		livesession.OpenParams{SessionID: liveTestSessionID, SessionName: f.name})
	require.NoError(t, err)
	_, err = livesession.Spool(ctx, f.cacheDir, []byte(`{"type":"header"}`+"\n"+`{"type":"user","content":"hi"}`+"\n"), livesession.SpoolOptions{})
	require.NoError(t, err)
	for _, marker := range []string{"close-requested", "delete-requested"} {
		require.NoError(t, os.WriteFile(filepath.Join(f.cacheDir, marker), nil, 0o600))
	}
}

// Failure prevented: spool.jsonl (a second full transcript) and the live
// stream's ids, offsets and markers were copied into the Ledger's git tree with
// the session, so every streamed session added transcript-sized git history
// for the whole team (C5).
func TestStageSessionInLedger_LeavesLiveStreamFilesBehind(t *testing.T) {
	f := newHeldFixture(t)
	runGitCmd(t, f.ledgerPath, "init")
	f.transcript(t)
	f.write(t, "summary.md", "summary")
	streamLive(t, f)
	require.FileExists(t, filepath.Join(f.cacheDir, "spool.jsonl"))
	require.FileExists(t, filepath.Join(f.cacheDir, "stream-state.json"))

	payload := &SessionFinalizePayload{SessionDir: f.cacheDir, LedgerPath: f.ledgerPath}
	_, err := NewSessionFinalizeHandler(slog.Default()).stageSessionInLedger(payload)
	require.NoError(t, err)

	dest := filepath.Join(f.ledgerPath, "sessions", f.name)
	assert.FileExists(t, filepath.Join(dest, artifactRaw))
	assert.FileExists(t, filepath.Join(dest, "summary.md"))
	entries, err := os.ReadDir(dest)
	require.NoError(t, err)
	for _, e := range entries {
		assert.False(t, livesession.IsLocalFile(e.Name()), "live stream file staged into the Ledger: %s", e.Name())
	}
	for _, name := range []string{"spool.jsonl", "stream-state.json", "close-requested", "delete-requested"} {
		assert.NoFileExists(t, filepath.Join(dest, name))
	}
}

// Failure prevented: after a successful push the daemon removed the whole
// session cache, including the spool and close request its live tick needed to
// finish a stream the live API had not closed yet. The tick scans only that
// cache, so the stream stayed open on the server for good.
func TestPruneSessionCache_KeepsAnUnfinishedLiveStream(t *testing.T) {
	cases := []struct {
		name     string
		setup    func(t *testing.T, f cacheFixture)
		wantKept bool
	}{
		{"stream still pending its close", streamLive, true},
		{"no live stream", func(*testing.T, cacheFixture) {}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newHeldFixture(t)
			f.transcript(t)
			f.write(t, "summary.md", "summary")
			// delete-requested reads as deleted (final); this case is about a close
			tc.setup(t, f)
			_ = os.Remove(filepath.Join(f.cacheDir, "delete-requested"))

			kept := PruneSessionCache(slog.Default(), f.cacheDir)

			assert.Equal(t, tc.wantKept, kept)
			if !tc.wantKept {
				assert.NoDirExists(t, f.cacheDir, "a cache with no unfinished stream is pruned as before")
				return
			}
			assert.FileExists(t, filepath.Join(f.cacheDir, "spool.jsonl"), "the daemon still needs the spool to close")
			assert.FileExists(t, filepath.Join(f.cacheDir, "close-requested"))
			st, err := livesession.ReadState(f.cacheDir)
			require.NoError(t, err)
			assert.Equal(t, liveTestSessionID, st.SessionID)
			assert.NoFileExists(t, filepath.Join(f.cacheDir, artifactRaw), "the pushed transcript is still pruned")
			assert.NoFileExists(t, filepath.Join(f.cacheDir, "summary.md"))
			assert.Equal(t, session.RawMissing, session.ClassifyRawFile(filepath.Join(f.cacheDir, artifactRaw)),
				"no transcript left behind for Detect to re-queue")
		})
	}
}

// Failure prevented: the daemon's upload removed the whole session cache once
// the session reached the Ledger, taking the spool and close request with it
// while the live API was unavailable. The live tick scans only that cache, so
// the stream stayed open on the server for good.
func TestUploadOnly_KeepsTheLiveStreamItHasNotClosed(t *testing.T) {
	f := newHeldFixture(t)
	runGitCmd(t, f.ledgerPath, "init")
	f.allArtifacts(t)
	streamLive(t, f)
	require.NoError(t, os.Remove(filepath.Join(f.cacheDir, "delete-requested")), "this stream is still owed a close")
	h := NewSessionFinalizeHandlerForTest(slog.Default())
	item := &WorkItem{Type: sessionFinalizeType, Payload: &SessionFinalizePayload{
		SessionDir: f.cacheDir, RawPath: filepath.Join(f.cacheDir, artifactRaw), LedgerPath: f.ledgerPath, UploadOnly: true,
	}}

	require.NoError(t, h.ProcessResult(item, &RunResult{}))

	dest := filepath.Join(f.ledgerPath, "sessions", f.name)
	assert.FileExists(t, filepath.Join(dest, artifactRaw), "the session reached the Ledger")
	assert.NoFileExists(t, filepath.Join(dest, "spool.jsonl"), "the spool never enters the Ledger")
	assert.FileExists(t, filepath.Join(f.cacheDir, "spool.jsonl"), "the live tick still needs the spool")
	assert.FileExists(t, filepath.Join(f.cacheDir, "close-requested"), "and the close request")
	assert.NoFileExists(t, filepath.Join(f.cacheDir, artifactRaw), "the published transcript is pruned")
}
