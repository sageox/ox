package agentwork

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/livesession"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Failure prevented: an AI coworker died mid-recording, and the daemon's stale
// or agent-exit finalize published the recording to the Ledger but never
// closed its live stream, so SageOx showed the session live for 72 h. A held
// session's stream is left to the live sweep's stopHeld, which deletes it.
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
		wantRequested bool
	}{
		{"auto", "auto", "auto", liveTestSessionID, true},
		{"recorded manual is held", "manual", "auto", liveTestSessionID, false},
		{"project now manual", "auto", "manual", liveTestSessionID, false},
		{"another recording's stream", "auto", "auto", otherRecording, false},
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
