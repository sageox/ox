package session

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/internal/session/adapters"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFinalizeInactiveCursorCaptureKeepsActiveRecordingOpen(t *testing.T) {
	now := time.Now().UTC()
	cutoff := now.Add(-24 * time.Hour)
	old := cutoff.Add(-time.Hour)
	for _, tc := range []struct {
		name         string
		startedAt    time.Time
		lastHookAt   *time.Time
		stopped      bool
		explicitStop bool
		wantActive   bool
	}{
		{name: "fresh start", startedAt: now, wantActive: true},
		{name: "fresh hook on old recording", startedAt: old, lastHookAt: &now, wantActive: true},
		{name: "exact cutoff stays active", startedAt: cutoff, wantActive: true},
		{name: "missing timestamps uses fresh marker", wantActive: true},
		{name: "inactive start", startedAt: old},
		{name: "inactive hook", startedAt: now, lastHookAt: &old},
		{name: "persisted stop overrides activity", startedAt: now, lastHookAt: &now, stopped: true},
		{name: "explicit stop overrides activity", startedAt: now, lastHookAt: &now, explicitStop: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"role":"user"}` + "\n" + `{"type":"turn_ended","status":"success"}` + "\n"
			f := newCursorCaptureFixture(t, "", body, false)
			require.NoError(t, UpdateRecordingStateForAgent(f.projectRoot, f.state.AgentID, func(state *RecordingState) {
				state.StartedAt = tc.startedAt
				state.LastHookAt = tc.lastHookAt
				if tc.stopped {
					state.StoppedAt = &now
				}
			}))
			if tc.explicitStop {
				require.NoError(t, MarkExplicitStop(f.projectRoot, f.state.AgentID))
			}
			reader := &fixedCursorIncrementalReader{next: int64(len(body)), entries: []adapters.RawEntry{{Role: "user", Content: "capture me when stopped"}}}
			var finalState *RecordingState
			err := fileutil.WithFileLock(context.Background(), f.rawPath, func() error {
				var err error
				finalState, err = FinalizeInactiveCursorCapture(context.Background(), f.projectRoot, f.sessionPath, f.homeDir, reader, cutoff)
				return err
			})
			if tc.wantActive {
				require.ErrorIs(t, err, ErrCursorRecordingActive)
				assert.Nil(t, finalState)
				assert.Zero(t, reader.calls, "active recovery must not mutate capture or settle a journal")
				assert.NoFileExists(t, f.rawPath)
				assert.FileExists(t, recordingStatePath(f.sessionPath))
				return
			}
			require.NoError(t, err)
			require.NotNil(t, finalState)
			assert.Equal(t, 1, finalState.EntryCount)
			raw, err := os.ReadFile(f.rawPath)
			require.NoError(t, err)
			assert.Contains(t, string(raw), `"cursor_finalized":true`)
		})
	}
}
