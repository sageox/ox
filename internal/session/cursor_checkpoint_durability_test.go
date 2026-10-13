package session

import (
	"errors"
	"os"
	"testing"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/session/adapters"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCursorCheckpointDurability_ControlUpdateReportsSyncFailure(t *testing.T) {
	f := newCursorCaptureFixture(t, "", "", false)
	original := syncRecordingStateDirectory
	t.Cleanup(func() { syncRecordingStateDirectory = original })
	injected := errors.New("injected control directory sync failure")
	syncRecordingStateDirectory = func(path string) error {
		require.Equal(t, f.sessionPath, path)
		return injected
	}

	err := UpdateRecordingStateForAgent(f.projectRoot, f.state.AgentID, func(state *RecordingState) {
		state.PublishingMode = config.SessionPublishingManual
	})
	require.ErrorIs(t, err, injected, "stop-time publication choices must not report durable success before sync")
	assert.Equal(t, config.SessionPublishingManual, loadCursorCaptureState(t, f).PublishingMode)

	syncRecordingStateDirectory = original
	require.NoError(t, UpdateRecordingStateForAgent(f.projectRoot, f.state.AgentID, func(state *RecordingState) {
		state.PublishingMode = config.SessionPublishingManual
	}))
}

func TestCursorCheckpointDurability_RetainsJournalUntilDirectorySync(t *testing.T) {
	source := `{"role":"user"}` + "\n" + `{"type":"turn_ended","status":"success"}` + "\n"
	f := newCursorCaptureFixture(t, "", source, false)
	reader := &fixedCursorIncrementalReader{
		next: int64(len(source)), entries: []adapters.RawEntry{{Role: "user", Content: "keep this batch"}},
	}
	original := syncRecordingStateDirectory
	t.Cleanup(func() { syncRecordingStateDirectory = original })
	injected := errors.New("injected directory sync failure")
	checkpointSyncs := 0
	syncRecordingStateDirectory = func(path string) error {
		require.Equal(t, f.sessionPath, path)
		state := loadCursorCaptureState(t, f)
		if state.SourceOffset == 0 {
			return original(path)
		}
		checkpointSyncs++
		// The checkpoint rename has happened, but its journal must still
		// protect the batch until the parent directory sync succeeds.
		require.FileExists(t, f.rawPath+rawAppendJournalSuffix)
		return injected
	}

	result, err := drainCursorFixture(t, f, reader, false)
	require.ErrorIs(t, err, injected)
	require.Nil(t, result, "a failed directory sync must not acknowledge capture")
	require.Equal(t, 1, checkpointSyncs)
	require.FileExists(t, f.rawPath+rawAppendJournalSuffix)
	checkpoint := loadCursorCaptureState(t, f)
	require.Equal(t, int64(len(source)), checkpoint.SourceOffset, "rename is visible despite failed sync")
	require.Equal(t, 1, checkpoint.EntryCount)
	rawBefore, err := os.ReadFile(f.rawPath)
	require.NoError(t, err)

	// A retry sees the new checkpoint. It must sync that rename before
	// settling the acknowledged journal, even with no new source rows.
	reader.entries = nil
	result, err = drainCursorFixture(t, f, reader, false)
	require.ErrorIs(t, err, injected)
	require.Nil(t, result)
	require.Equal(t, 2, checkpointSyncs)
	require.FileExists(t, f.rawPath+rawAppendJournalSuffix)
	rawAfter, err := os.ReadFile(f.rawPath)
	require.NoError(t, err)
	require.Equal(t, rawBefore, rawAfter)

	syncRecordingStateDirectory = func(path string) error {
		require.FileExists(t, f.rawPath+rawAppendJournalSuffix)
		checkpointSyncs++
		return original(path)
	}
	result, err = drainCursorFixture(t, f, reader, false)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Zero(t, result.Entries, "the recovered batch must not be appended twice")
	assert.Equal(t, 3, checkpointSyncs)
	assert.NoFileExists(t, f.rawPath+rawAppendJournalSuffix)
	rawAfter, err = os.ReadFile(f.rawPath)
	require.NoError(t, err)
	assert.Equal(t, rawBefore, rawAfter)
}
