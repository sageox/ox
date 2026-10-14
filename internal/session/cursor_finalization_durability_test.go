package session

import (
	"errors"
	"os"
	"testing"

	"github.com/sageox/ox/internal/session/adapters"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCursorFinalizationDurability_RetriesDirectorySyncBeforeSuccess(t *testing.T) {
	source := `{"role":"user"}` + "\n" + `{"role":"assistant"}` + "\n" + `{"type":"turn_ended","status":"success"}` + "\n"
	f := newCursorCaptureFixture(t, "", source, false)
	reader := &fixedCursorIncrementalReader{
		next: int64(len(source)), entries: []adapters.RawEntry{
			{Role: "user", Content: "private paused entry"},
			{Role: "assistant", Content: "retained public entry"},
		},
	}
	_, err := drainCursorFixture(t, f, reader, false)
	require.NoError(t, err)
	require.NoError(t, UpdateRecordingStateForAgent(f.projectRoot, f.state.AgentID, func(state *RecordingState) {
		state.Lifecycle = []LifecycleEvent{{Action: LifecycleActionPause, Seq: 0}, {Action: LifecycleActionResume, Seq: 1}}
	}))
	reader.entries = nil

	original := syncRecordingStateDirectory
	t.Cleanup(func() { syncRecordingStateDirectory = original })
	injected := errors.New("injected final raw directory sync failure")
	finalSyncs := 0
	allowSync := false
	syncRecordingStateDirectory = func(path string) error {
		require.Equal(t, f.sessionPath, path)
		_, finalized, err := cursorCaptureHeader(f.rawPath)
		require.NoError(t, err)
		if finalized {
			finalSyncs++
			if !allowSync {
				return injected
			}
		}
		return original(path)
	}

	_, err = finalizeCursorFixture(t, f, reader)
	require.ErrorIs(t, err, injected)
	require.Equal(t, 1, finalSyncs)
	masked, err := os.ReadFile(f.rawPath)
	require.NoError(t, err)
	require.NotContains(t, string(masked), "private paused entry")
	require.Contains(t, string(masked), "retained public entry")

	// The finalized header is already visible after the failed directory sync.
	// A retry must sync the rename without rereading or masking the raw again.
	reader.err = errors.New("a finalized source must not be replayed")
	_, err = finalizeCursorFixture(t, f, reader)
	require.ErrorIs(t, err, injected)
	require.Equal(t, 2, finalSyncs)
	afterRetry, err := os.ReadFile(f.rawPath)
	require.NoError(t, err)
	require.Equal(t, masked, afterRetry)

	allowSync = true
	state, err := finalizeCursorFixture(t, f, reader)
	require.NoError(t, err)
	require.NotNil(t, state)
	assert.Equal(t, 3, finalSyncs)
	assert.Equal(t, f.state.SessionID, state.SessionID)
	afterSuccess, err := os.ReadFile(f.rawPath)
	require.NoError(t, err)
	assert.Equal(t, masked, afterSuccess)
}
