package session

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Hook startup counts only the completed prefix, never the current exported prompt.
func TestInitializeCursorTurnBoundaryKeepsStartupPromptAndQueuedGenerations(t *testing.T) {
	old := `{"role":"user"}` + "\n" + `{"type":"turn_ended","status":"success"}` + "\n"
	current := `{"role":"user"}` + "\n"
	for _, generation := range []string{"", "generation-one"} {
		t.Run("initial_generation_"+generation, func(t *testing.T) {
			f := newCursorCaptureFixture(t, old, current, false)
			require.NoError(t, InitializeCursorTurnBoundary(f.state, f.sourcePath, generation))
			require.Equal(t, 2, f.state.CursorExpectedTurns)
			require.NoError(t, SaveRecordingState(f.projectRoot, f.state))
			for _, next := range []string{"generation-one", "generation-two", "generation-one"} {
				require.NoError(t, RecordCursorPrompt(context.Background(), f.projectRoot, f.state, f.homeDir, next))
			}
			state := loadCursorCaptureState(t, f)
			assert.Equal(t, 3, state.CursorExpectedTurns, "duplicate hooks cannot reserve extra native turns")
			assert.Equal(t, []string{"generation-one", "generation-two"}, state.CursorPromptGenerations)
		})
	}
}

func TestInitializeCursorTurnBoundaryRefusesUnprovenPrefix(t *testing.T) {
	old := `{"type":"turn_ended","status":"success"}` + "\n"
	for _, missing := range []bool{false, true} {
		t.Run(map[bool]string{false: "changed", true: "missing"}[missing], func(t *testing.T) {
			f := newCursorCaptureFixture(t, old, "", false)
			if missing {
				require.NoError(t, os.Remove(f.sourcePath))
			} else {
				require.NoError(t, os.WriteFile(f.sourcePath, []byte(`{"type":"turn_ended","status":"changed"}`+"\n"), 0o600))
			}
			err := InitializeCursorTurnBoundary(f.state, f.sourcePath, "generation")
			if missing {
				require.ErrorIs(t, err, ErrCursorSourcePending)
			} else {
				require.ErrorIs(t, err, ErrCursorSourceChanged)
			}
			assert.Zero(t, f.state.CursorExpectedTurns)
			assert.Empty(t, f.state.CursorPromptGenerations)
		})
	}
}

func TestInitializeCursorTurnBoundaryKeepsPendingFirstTurn(t *testing.T) {
	f := newCursorCaptureFixture(t, "", "", true)
	require.NoError(t, InitializeCursorTurnBoundary(f.state, f.sourcePath, "first"))
	require.NoError(t, SaveRecordingState(f.projectRoot, f.state))
	require.NoError(t, RecordCursorPrompt(context.Background(), f.projectRoot, f.state, f.homeDir, "first"))
	state := loadCursorCaptureState(t, f)
	assert.Equal(t, 1, state.CursorExpectedTurns)
	assert.Equal(t, []string{"first"}, state.CursorPromptGenerations)
}

// A stale CLI snapshot must not pause or submit turns against a replacement recording.
func TestCursorControlsRejectReplacedRecording(t *testing.T) {
	f := newCursorCaptureFixture(t, "", "", true)
	stale := *f.state
	stale.SessionID = "another-recording"
	err := RecordCursorPrompt(context.Background(), f.projectRoot, &stale, f.homeDir, "generation")
	require.ErrorContains(t, err, "identity-conflict")
	updated := false
	err = UpdateCursorRecordingControl(context.Background(), f.projectRoot, &stale, f.homeDir, nil, LifecycleActionPause, func(*RecordingState) {
		updated = true
	})
	require.ErrorContains(t, err, "identity-conflict")
	assert.False(t, updated)
	state := loadCursorCaptureState(t, f)
	assert.Empty(t, state.CursorPromptGenerations)
	assert.Empty(t, state.Lifecycle)
}
