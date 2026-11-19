package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/daemon"
	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Failure prevented (ox#1931 review): SessionEnd waits for the agent's last
// record before it stops the recording. A restarted agent can replace a
// StopIncomplete recording during that wait (StartRecording clears it and
// mints a new SessionID), and the drain can quarantine it. The stop after the
// wait looked the agent's recording up again, so it stamped, finalized and
// cleared whatever it found: the replacement's later turns were never
// captured. Only the recording loaded before the wait may be stopped.
func TestHandleEndStopsOnlyTheRecordingItLoadedBeforeTheWait(t *testing.T) {
	cases := []struct {
		name string
		// during runs inside the wait. It returns the recording that must
		// come out of SessionEnd untouched, or nil when nothing changed.
		during func(t *testing.T, projectRoot string, loaded *session.RecordingState) *session.RecordingState
	}{
		{"control: unchanged during the wait", nil},
		{"agent restarted into a new folder", func(t *testing.T, projectRoot string, loaded *session.RecordingState) *session.RecordingState {
			next, err := session.StartRecording(projectRoot, session.StartRecordingOptions{AgentID: loaded.AgentID, AdapterName: "claude-code", Username: "person-a"})
			require.NoError(t, err, "a restart replaces a StopIncomplete recording")
			require.NotEqual(t, loaded.SessionPath, next.SessionPath, "precondition: the restart got its own folder")
			return next
		}},
		{"agent restarted into the same folder", func(t *testing.T, projectRoot string, loaded *session.RecordingState) *session.RecordingState {
			// session names are minute-granular, so a restart within the
			// minute reuses the folder: only the SessionID tells them apart
			require.NoError(t, session.ClearRecordingStateAt(loaded.SessionPath, loaded.SessionID))
			next := *loaded
			next.SessionID = "rec-after-restart"
			next.StopIncomplete = false
			next.StartedAt = time.Now()
			require.NoError(t, session.SaveRecordingState(projectRoot, &next))
			return &next
		}},
		{"quarantined by the drain", func(t *testing.T, _ string, loaded *session.RecordingState) *session.RecordingState {
			require.NoError(t, session.SetSourceRejectedAt(loaded.SessionPath, loaded.SessionID, true))
			kept, err := session.ReadRecordingStateFile(loaded.SessionPath)
			require.NoError(t, err)
			return kept
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			projectRoot, repoID := setupTestProject(t)
			t.Setenv("OX_USER_CONFIG", filepath.Join(t.TempDir(), "absent.yaml"))
			t.Setenv("OX_SESSION_PUBLISHING", "auto")
			// where the daemon would not outlive the agent, SessionEnd stops the
			// session itself first; a stop that never launched hands it to the
			// daemon too, so every venue reaches the wait below
			priorStop := runSessionStopAtEnd
			runSessionStopAtEnd = func(string, string) error { return errSessionStopNotLaunched }
			t.Cleanup(func() { runSessionStopAtEnd = priorStop })

			agentID := "OxEndSwap"
			sessionPath := filepath.Join(session.GetContextPath(repoID), "sessions", "2026-04-01T10-00-person-"+agentID)
			loaded := &session.RecordingState{
				AgentID:        agentID,
				SessionID:      "rec-before-wait",
				StartedAt:      time.Now().Add(-10 * time.Minute),
				AdapterName:    "claude-code",
				SessionPath:    sessionPath,
				OutputFile:     filepath.Join(sessionPath, "raw.jsonl"),
				ParentPID:      os.Getpid(),
				StopIncomplete: true, // a stop came back asking for a retry
			}
			require.NoError(t, session.SaveRecordingState(projectRoot, loaded))

			var finalized []string
			origIPC := sendHookFinalizeIPC
			sendHookFinalizeIPC = func(p daemon.SessionFinalizeIPCPayload) error {
				finalized = append(finalized, p.CachePath)
				return nil
			}
			t.Cleanup(func() { sendHookFinalizeIPC = origIPC })

			waited := false
			var kept *session.RecordingState
			origWait := awaitFinalRecordAtEnd
			awaitFinalRecordAtEnd = func(string, string) {
				waited = true
				if tc.during != nil {
					kept = tc.during(t, projectRoot, loaded)
				}
			}
			t.Cleanup(func() { awaitFinalRecordAtEnd = origWait })

			require.NoError(t, handleEnd(&HookContext{Phase: phaseEnd, AgentType: "claude-code", ProjectRoot: projectRoot, Marker: &SessionMarker{AgentID: agentID}}))
			require.True(t, waited, "precondition: SessionEnd waited for the last record")

			if tc.during == nil {
				assert.Equal(t, []string{sessionPath}, finalized, "the loaded recording is handed to the daemon")
				left, err := session.ReadRecordingStateFile(sessionPath)
				require.NoError(t, err)
				assert.Nil(t, left, "the loaded recording is cleared once handed off")
				return
			}

			assert.Empty(t, finalized, "no recording may be handed to the daemon")
			after, err := session.ReadRecordingStateFile(kept.SessionPath)
			require.NoError(t, err)
			require.NotNil(t, after, "the recording that changed during the wait must not be cleared")
			assert.Equal(t, kept.SessionID, after.SessionID)
			assert.Nil(t, after.StoppedAt, "the recording that changed during the wait must not be stopped")
			assert.Equal(t, kept.SourceRejected, after.SourceRejected, "a quarantine must be kept")
		})
	}
}
