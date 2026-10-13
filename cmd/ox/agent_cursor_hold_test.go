package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Cursor's sessionEnd keeps its recording for the daemon's final JSONL drain.
// The daemon cannot see a manual-publishing override supplied only to this
// hook, so that choice must survive in the same marker as the stop signal.
func TestCursorHostSessionEndPreservesManualPublishingForRecovery(t *testing.T) {
	for _, tc := range []struct {
		name, startMode, stopMode string
		wantManual                bool
	}{
		{"manual only at stop", config.SessionPublishingAuto, config.SessionPublishingManual, true},
		{"manual at start", config.SessionPublishingManual, config.SessionPublishingAuto, true},
		{"auto throughout", config.SessionPublishingAuto, config.SessionPublishingAuto, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newCursorHostFixture(t)
			t.Setenv("SAGEOX_DAEMON", "false")
			t.Setenv(config.EnvSessionPublishing, tc.startMode)
			state := f.record(t)
			require.Equal(t, tc.startMode, state.PublishingMode)
			rawPath := filepath.Join(state.SessionPath, "raw.jsonl")
			before, err := os.ReadFile(rawPath)
			require.NoError(t, err)

			t.Setenv(config.EnvSessionPublishing, tc.stopMode)
			require.NoError(t, runCursorAgentHook("sessionEnd", f.input(t, "sessionEnd", "g1"), f.root))

			// Match a daemon that outlives the one-off hook environment.
			t.Setenv(config.EnvSessionPublishing, config.SessionPublishingAuto)
			require.Equal(t, config.SessionPublishingAuto, config.GetSessionPublishing(f.root))
			current, err := session.LoadRecordingStateForAgent(f.root, state.AgentID)
			require.NoError(t, err)
			require.NotNil(t, current, "the daemon still owns final drain")
			require.NotNil(t, current.StoppedAt)
			assert.Equal(t, tc.wantManual, current.RecordedManualPublishing(), "recovery must retain the stop-time publishing choice")
			assert.True(t, current.CursorFinalDrainPending)
			assert.False(t, session.IsHeld(state.SessionPath), "hold follows the daemon's final drain, not the native hook")
			after, err := os.ReadFile(rawPath)
			require.NoError(t, err)
			assert.Equal(t, before, after, "pending native export must leave captured bytes untouched")
			assert.Zero(t, f.reader.calls.Load())
		})
	}
}
