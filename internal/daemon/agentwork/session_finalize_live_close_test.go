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

// Failure prevented: an AI coworker died mid-recording, and the daemon's stale
// or agent-exit finalize published the recording to the Ledger but never
// closed its live stream, so SageOx showed the session live for 72 h. A
// session the finalize keeps local (held in any cache location, or manual
// publishing) gets no close: the live sweep's stopHeld deletes a held stream.
func TestDetect_EndedRecordingRequestsItsLiveClose(t *testing.T) {
	stale := time.Now().Add(-25 * time.Hour)
	otherRecording := "ses_01934f3e-6b2a-7c3d-8e4f-5a6b7c8d9e99"
	entryPoints := []struct {
		name   string
		detect func(t *testing.T, h *SessionFinalizeHandler, ledgerPath string)
	}{
		{"stale recording scan", func(t *testing.T, h *SessionFinalizeHandler, ledgerPath string) {
			detectCacheOnly(t, h, ledgerPath)
		}},
		{"orphaned recording of an exited agent", func(_ *testing.T, h *SessionFinalizeHandler, ledgerPath string) {
			h.DetectOrphanedForAgent(ledgerPath, "OxHeLd", 0)
		}},
	}
	cases := []struct {
		name          string
		recordedMode  string
		projectMode   string
		recordingID   string
		heldElsewhere bool // a hold for the same session name in another cache location
		wantRequested bool
	}{
		{"auto", "auto", "auto", liveTestSessionID, false, true},
		{"recorded manual is held", "manual", "auto", liveTestSessionID, false, false},
		{"project now manual", "auto", "manual", liveTestSessionID, false, false},
		{"held in another cache location", "auto", "auto", liveTestSessionID, true, false},
		{"another recording's stream", "auto", "auto", otherRecording, false, false},
	}
	for _, ep := range entryPoints {
		for _, tc := range cases {
			t.Run(ep.name+"/"+tc.name, func(t *testing.T) {
				f := newHeldFixture(t)
				f.transcript(t)
				prepareLiveStream(t, f)
				rec, err := json.Marshal(map[string]any{"agent_id": "OxHeLd", "session_id": tc.recordingID,
					"started_at": stale.Format(time.RFC3339), "stopped_at": stale.Add(time.Hour).Format(time.RFC3339),
					"publishing_mode": tc.recordedMode})
				require.NoError(t, err)
				recPath := filepath.Join(f.cacheDir, recordingMarker)
				require.NoError(t, os.WriteFile(recPath, rec, 0o644))
				if tc.heldElsewhere {
					dirs := session.HeldSessionDirs(f.ledgerPath)
					require.Greater(t, len(dirs), 1, "an XDG cache location is scanned too")
					hold(t, filepath.Join(dirs[1], f.name))
				}
				h := NewSessionFinalizeHandlerForTest(slog.New(slog.DiscardHandler))
				h.SetProjectRoot(projectWithPublishing(t, tc.projectMode))

				ep.detect(t, h, f.ledgerPath)

				assert.NoFileExists(t, recPath, "the ended recording is still reclaimed")
				st, err := livesession.ReadState(f.cacheDir)
				require.NoError(t, err)
				assert.Equal(t, tc.wantRequested, st.CloseRequested, "live close requested")
			})
		}
	}
}

// prepareLiveStream leaves in f.cacheDir what a streamed recording does: the
// stream state of liveTestSessionID and a spool holding its first prompt.
func prepareLiveStream(t *testing.T, f cacheFixture) {
	t.Helper()
	cfg := livesession.Config{Endpoint: "https://live.test.sageox.ai", DataDir: t.TempDir()}
	_, err := livesession.Prepare(t.Context(), cfg, f.cacheDir, livesession.OpenParams{SessionID: liveTestSessionID, SessionName: f.name})
	require.NoError(t, err)
	_, err = livesession.Spool(t.Context(), f.cacheDir, []byte(`{"type":"user","content":"hi"}`+"\n"), livesession.SpoolOptions{})
	require.NoError(t, err)
	st, err := livesession.ReadState(f.cacheDir)
	require.NoError(t, err)
	require.False(t, st.CloseRequested, "precondition: nothing asked for the close yet")
}
