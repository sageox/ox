package session

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A manual recording can start before any prompt. Pausing it must not require
// submitting an unwanted paused prompt just to produce a turn_ended row.
func TestCursorResumeEmptyExportRequiresAcknowledgedBoundary(t *testing.T) {
	for _, tc := range []struct {
		name       string
		prepare    func(*testing.T, *cursorCaptureFixture)
		controlErr error
	}{
		{name: "existing empty export"},
		{
			name: "missing export",
			prepare: func(t *testing.T, f *cursorCaptureFixture) {
				require.NoError(t, os.Remove(f.sourcePath))
			},
			controlErr: ErrCursorSourcePending,
		},
		{
			name: "submitted turn pending",
			prepare: func(t *testing.T, f *cursorCaptureFixture) {
				require.NoError(t, RecordCursorPrompt(context.Background(), f.projectRoot, f.state, f.homeDir, "pending-generation"))
			},
			controlErr: ErrCursorFinalDrainPending,
		},
		{
			name: "unread terminal export",
			prepare: func(t *testing.T, f *cursorCaptureFixture) {
				require.NoError(t, os.WriteFile(f.sourcePath, []byte(`{"type":"turn_ended","status":"success"}`+"\n"), 0o600))
			},
			controlErr: ErrCursorFinalDrainPending,
		},
		{
			name: "changed checkpoint hash",
			prepare: func(t *testing.T, f *cursorCaptureFixture) {
				state := loadCursorCaptureState(t, f)
				state.SourcePrefixSHA256 = cursorSourceHash([]byte("a different acknowledged prefix"))
				require.NoError(t, SaveRecordingState(f.projectRoot, state))
			},
			controlErr: ErrCursorSourceChanged,
		},
	} {
		for _, generic := range []bool{false, true} {
			control := "Cursor control"
			if generic {
				control = "generic lifecycle guard"
			}
			t.Run(tc.name+"/"+control, func(t *testing.T) {
				f := newCursorCaptureFixture(t, "", "", false)
				ctx := context.Background()
				reader := &fixedCursorIncrementalReader{}
				require.Zero(t, f.state.CursorExpectedTurns, "manual startup reserves no native turn")
				require.NoError(t, UpdateCursorRecordingControl(ctx, f.projectRoot, f.state, f.homeDir, reader, LifecycleActionPause, func(state *RecordingState) {
					now := time.Now().UTC()
					state.SuspendedAt = &now
					state.Lifecycle = append(state.Lifecycle, LifecycleEvent{Action: LifecycleActionPause, At: now, Seq: state.EntryCount})
				}))
				if tc.prepare != nil {
					tc.prepare(t, f)
				}
				before := loadCursorCaptureState(t, f)
				resume := func(state *RecordingState) {
					state.SuspendedAt = nil
					state.Lifecycle = append(state.Lifecycle, LifecycleEvent{Action: LifecycleActionResume, At: time.Now().UTC(), Seq: state.EntryCount})
				}
				var err error
				if generic {
					err = UpdateRecordingStateForAgent(f.projectRoot, f.state.AgentID, resume)
				} else {
					err = UpdateCursorRecordingControl(ctx, f.projectRoot, f.state, f.homeDir, reader, LifecycleActionResume, resume)
				}
				if tc.controlErr != nil {
					wantErr := tc.controlErr
					if generic {
						wantErr = ErrCursorFinalDrainPending
					}
					require.ErrorIs(t, err, wantErr)
					assert.Equal(t, before, loadCursorCaptureState(t, f), "failed resume preserves the paused recording")
					return
				}
				require.NoError(t, err)
				state := loadCursorCaptureState(t, f)
				assert.Nil(t, state.SuspendedAt)
				assert.Zero(t, state.EntryCount)
				assert.Zero(t, state.SourceOffset)
				assert.Zero(t, state.CursorExpectedTurns)
				assert.Empty(t, state.CursorPromptGenerations)
				require.Len(t, state.Lifecycle, 2)
				assert.Equal(t, LifecycleActionResume, state.Lifecycle[1].Action)
				assert.Zero(t, state.Lifecycle[1].Seq)
			})
		}
	}
}
