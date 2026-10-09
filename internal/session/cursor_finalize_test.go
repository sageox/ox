package session

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/internal/session/adapters"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func finalizeCursorFixture(t *testing.T, f *cursorCaptureFixture, reader adapters.IncrementalReader) (*RecordingState, error) {
	t.Helper()
	var state *RecordingState
	err := fileutil.WithFileLock(context.Background(), f.rawPath, func() error {
		var err error
		state, err = FinalizeCursorCapture(context.Background(), f.projectRoot, f.sessionPath, f.homeDir, reader)
		return err
	})
	return state, err
}

func TestFinalizeCursorCapture_PendingAndIncompleteKeepCaptureRetryable(t *testing.T) {
	t.Run("missing native export", func(t *testing.T) {
		f := newCursorCaptureFixture(t, "", "", true)
		_, err := finalizeCursorFixture(t, f, &fixedCursorIncrementalReader{})
		require.ErrorIs(t, err, ErrCursorSourcePending)
		state := loadCursorCaptureState(t, f)
		assert.Empty(t, state.SessionFile)
		assert.Zero(t, state.SourceOffset)
		assert.Zero(t, state.EntryCount)
		assert.NoFileExists(t, f.rawPath)
	})

	t.Run("unterminated final turn", func(t *testing.T) {
		source := `{"role":"user"}` + "\n"
		f := newCursorCaptureFixture(t, "", source, false)
		reader := &fixedCursorIncrementalReader{
			entries: []adapters.RawEntry{{Role: "user", Content: "captured before terminal"}},
			next:    int64(len(source)),
		}
		_, err := finalizeCursorFixture(t, f, reader)
		require.ErrorIs(t, err, ErrCursorFinalDrainPending)
		state := loadCursorCaptureState(t, f)
		assert.Equal(t, int64(len(source)), state.SourceOffset)
		assert.Equal(t, 1, state.EntryCount)
		assert.Equal(t, 1, rawEntryCount(t, f.rawPath), "partial final drain remains durable for retry")
	})
}

func TestFinalizeCursorCapture_LateTerminalFinalizesOrderedZeroTimeRows(t *testing.T) {
	initial := `{"role":"user"}` + "\n"
	f := newCursorCaptureFixture(t, "", initial, false)
	reader := &fixedCursorIncrementalReader{
		entries: []adapters.RawEntry{{Role: "user", Content: "first prompt"}},
		next:    int64(len(initial)),
	}
	_, err := finalizeCursorFixture(t, f, reader)
	require.ErrorIs(t, err, ErrCursorFinalDrainPending)

	complete := initial + `{"type":"turn_ended","status":"success"}` + "\n"
	require.NoError(t, os.WriteFile(f.sourcePath, []byte(complete), 0o600))
	reader.entries = nil
	reader.next = int64(len(complete))
	state, err := finalizeCursorFixture(t, f, reader)
	require.NoError(t, err)
	require.NotNil(t, state)
	assert.Equal(t, 1, state.EntryCount)

	data, err := os.ReadFile(f.rawPath)
	require.NoError(t, err)
	header, finalized, err := cursorCaptureHeader(f.rawPath)
	require.NoError(t, err)
	require.True(t, finalized)
	require.NotNil(t, header)
	assert.Contains(t, string(data), `"cursor_finalized":true`)
	assert.Contains(t, string(data), `"ts":"0001-01-01T00:00:00Z"`)
	assert.Contains(t, string(data), "first prompt")

	late := complete + `{"role":"assistant","content":"must not replay"}` + "\n" + `{"type":"turn_ended","status":"success"}` + "\n"
	require.NoError(t, os.WriteFile(f.sourcePath, []byte(late), 0o600))
	before := append([]byte(nil), data...)
	_, err = drainCursorFixture(t, f, reader, false)
	require.ErrorIs(t, err, ErrNotRecording)
	after, readErr := os.ReadFile(f.rawPath)
	require.NoError(t, readErr)
	assert.Equal(t, before, after)
}

func TestFinalizeCursorCapture_MasksPausedEntriesAndIsIdempotent(t *testing.T) {
	source := `{"role":"user"}` + "\n" +
		`{"role":"assistant"}` + "\n" +
		`{"role":"tool"}` + "\n" +
		`{"type":"turn_ended","status":"success"}` + "\n"
	f := newCursorCaptureFixture(t, "", source, false)
	require.NoError(t, UpdateRecordingStateForAgent(f.projectRoot, f.state.AgentID, func(state *RecordingState) {
		state.Lifecycle = []LifecycleEvent{
			{Action: LifecycleActionPause, Seq: 1},
			{Action: LifecycleActionResume, Seq: 2},
		}
	}))
	reader := &fixedCursorIncrementalReader{
		entries: []adapters.RawEntry{
			{Role: "user", Content: "keep first"},
			{Role: "assistant", Content: "exclude paused"},
			{Role: "tool", ToolName: "read", ToolInput: "keep third"},
		},
		next: int64(len(source)),
	}
	state, err := finalizeCursorFixture(t, f, reader)
	require.NoError(t, err)
	require.NotNil(t, state)
	assert.Equal(t, 3, state.EntryCount)

	data, err := os.ReadFile(f.rawPath)
	require.NoError(t, err)
	assert.Equal(t, 2, rawEntryCount(t, f.rawPath))
	assert.Contains(t, string(data), "keep first")
	assert.NotContains(t, string(data), "exclude paused")
	assert.Contains(t, string(data), "keep third")

	before := append([]byte(nil), data...)
	reader.err = errors.New("reader must not be called after finalized marker")
	require.NoError(t, os.WriteFile(f.sourcePath, append([]byte(source), []byte(`{"role":"assistant","content":"late"}`+"\n")...), 0o600))
	again, err := finalizeCursorFixture(t, f, reader)
	require.NoError(t, err)
	assert.Equal(t, state.EntryCount, again.EntryCount)
	after, readErr := os.ReadFile(f.rawPath)
	require.NoError(t, readErr)
	assert.Equal(t, before, after)
}

func TestFinalizeCursorCapture_RejectsNullCheckpointAfterFinalizedRename(t *testing.T) {
	source := `{"role":"assistant"}` + "\n" + `{"type":"turn_ended","status":"success"}` + "\n"
	f := newCursorCaptureFixture(t, "", source, false)
	reader := &fixedCursorIncrementalReader{
		entries: []adapters.RawEntry{{Role: "assistant", Content: "durable entry"}},
		next:    int64(len(source)),
	}
	_, err := finalizeCursorFixture(t, f, reader)
	require.NoError(t, err)
	before, err := os.ReadFile(f.rawPath)
	require.NoError(t, err)

	// This models a corrupt marker observed after the atomic raw replacement but
	// before its caller has cleared the marker. The finalized shortcut must not
	// report success with a nil state or read/replay the native source.
	require.NoError(t, os.WriteFile(recordingStatePath(f.sessionPath), []byte("null"), 0o600))
	reader.err = errors.New("reader must not run for finalized raw")
	state, err := finalizeCursorFixture(t, f, reader)
	require.Error(t, err)
	assert.Nil(t, state)
	assert.Contains(t, err.Error(), "invalid finalized Cursor recording checkpoint")
	after, readErr := os.ReadFile(f.rawPath)
	require.NoError(t, readErr)
	assert.Equal(t, before, after)
	marker, readErr := os.ReadFile(recordingStatePath(f.sessionPath))
	require.NoError(t, readErr)
	assert.Equal(t, []byte("null"), marker)
}

func rawEntryCount(t *testing.T, rawPath string) int {
	t.Helper()
	data, err := os.ReadFile(rawPath)
	require.NoError(t, err)
	entries, complete, err := completeRawRecords(data)
	require.NoError(t, err)
	require.Equal(t, len(data), complete)
	return len(entries)
}
