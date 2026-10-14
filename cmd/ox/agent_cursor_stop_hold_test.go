package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/agentinstance"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Explicit Cursor stop/recover may defer until the native export is complete.
// Persist a one-off manual override before that stop becomes visible to a
// daemon which does not share the calling command's environment.
func TestCursorStopAndRecoverPreservePublishingChoiceWhileDrainPending(t *testing.T) {
	for _, command := range []struct {
		name string
		run  func(*agentinstance.Instance) error
	}{
		{"stop", runAgentSessionStop},
		{"recover", runAgentSessionRecover},
	} {
		for _, tc := range []struct {
			name, startMode, stopMode string
			wantManual                bool
		}{
			{"manual only at stop", config.SessionPublishingAuto, config.SessionPublishingManual, true},
			{"manual at start", config.SessionPublishingManual, config.SessionPublishingAuto, true},
			{"auto throughout", config.SessionPublishingAuto, config.SessionPublishingAuto, false},
		} {
			for _, pendingSource := range []bool{true, false} {
				sourceName := "unfinished turn"
				if pendingSource {
					sourceName = "missing export"
				}
				t.Run(command.name+"/"+tc.name+"/"+sourceName, func(t *testing.T) {
					f := newCursorHostFixture(t)
					t.Setenv("SAGEOX_DAEMON", "false")
					t.Setenv(config.EnvSessionPublishing, tc.startMode)
					state := f.record(t)
					require.Equal(t, tc.startMode, state.PublishingMode)
					wantErr := session.ErrCursorSourcePending
					if !pendingSource {
						f.write(t, cursorHostUser+cursorHostAnswer)
						wantErr = session.ErrCursorFinalDrainPending
					}
					t.Setenv(config.EnvSessionPublishing, tc.stopMode)
					var stopErr error
					captureStdoutForPlanCLI(t, func() { stopErr = command.run(&agentinstance.Instance{AgentID: state.AgentID}) })
					require.ErrorIs(t, stopErr, wantErr)

					// Match the long-running daemon's auto environment after the command
					// exits. Recovery must rely on the marker, not the departed override.
					t.Setenv(config.EnvSessionPublishing, config.SessionPublishingAuto)
					require.Equal(t, config.SessionPublishingAuto, config.GetSessionPublishing(f.root))
					current, err := session.LoadRecordingStateForAgent(f.root, state.AgentID)
					require.NoError(t, err)
					require.NotNil(t, current)
					assert.NotNil(t, current.StoppedAt)
					assert.True(t, current.CursorFinalDrainPending)
					assert.Equal(t, tc.wantManual, current.RecordedManualPublishing(), "deferred daemon recovery must preserve the caller's publishing choice")
					assert.False(t, session.IsHeld(state.SessionPath), "the daemon creates the hold after final drain")
					raw, err := os.ReadFile(filepath.Join(state.SessionPath, "raw.jsonl"))
					require.NoError(t, err)
					assert.NotContains(t, string(raw), `"cursor_finalized":true`)
				})
			}
		}
	}
}
