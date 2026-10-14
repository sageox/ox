package session

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/session/adapters"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCursorFinalizePreservesCarrierFooters(t *testing.T) {
	source := "{\"role\":\"user\"}\n{\"type\":\"turn_ended\",\"status\":\"success\"}\n"
	f := newCursorCaptureFixture(t, "", source, false)
	reader := &fixedCursorIncrementalReader{
		entries: []adapters.RawEntry{{Role: "user", Content: "keep this turn"}},
		next:    int64(len(source)),
	}
	_, err := drainCursorFixture(t, f, reader, false)
	require.NoError(t, err)
	firstStop := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	lastStop := firstStop.Add(time.Second)
	require.NoError(t, StampRawCarrier(f.rawPath, CarrierStamp{StoppedAt: firstStop}))
	require.NoError(t, StampRawCarrier(f.rawPath, CarrierStamp{StoppedAt: lastStop}))
	reader.entries = nil
	_, err = finalizeCursorFixture(t, f, reader)
	require.NoError(t, err)
	stored, err := ReadSessionFromPath(f.rawPath)
	require.NoError(t, err)
	require.NotNil(t, stored.Meta.StoppedAt)
	assert.Equal(t, lastStop, *stored.Meta.StoppedAt)
	require.Len(t, stored.Entries, 1)
	assert.Equal(t, "keep this turn", stored.Entries[0]["content"])
}

func TestCursorIdentityBoundUpdateRejectsFinalizedLifecycle(t *testing.T) {
	source := "{\"role\":\"user\"}\n{\"type\":\"turn_ended\",\"status\":\"success\"}\n"
	f := newCursorCaptureFixture(t, "", source, false)
	_, err := finalizeCursorFixture(t, f, &fixedCursorIncrementalReader{
		entries: []adapters.RawEntry{{Role: "user", Content: "final turn"}},
		next:    int64(len(source)),
	})
	require.NoError(t, err)
	err = UpdateRecordingStateAt(f.state.SessionPath, f.state.SessionID, func(state *RecordingState) {
		state.Lifecycle = append(state.Lifecycle, LifecycleEvent{Action: LifecycleActionPause, Seq: state.EntryCount})
	})
	require.ErrorIs(t, err, ErrNotRecording)
	assert.Empty(t, loadCursorCaptureState(t, f).Lifecycle)
}

func TestCursorCaptureRejectsQuarantinedMarkerWithoutMutation(t *testing.T) {
	source := "{\"role\":\"user\"}\n{\"type\":\"turn_ended\",\"status\":\"success\"}\n"

	t.Run("live capture", func(t *testing.T) {
		f := newCursorCaptureFixture(t, "", source, false)
		reader := &fixedCursorIncrementalReader{
			entries: []adapters.RawEntry{{Role: "user", Content: "captured before quarantine"}},
			next:    int64(len(source)),
		}
		_, err := drainCursorFixture(t, f, reader, false)
		require.NoError(t, err)
		require.NoError(t, UpdateRecordingStateAt(f.state.SessionPath, f.state.SessionID, func(state *RecordingState) {
			state.SourceRejected = true
		}))

		markerPath := filepath.Join(f.sessionPath, recordingFile)
		markerFile, err := os.OpenFile(markerPath, os.O_WRONLY|os.O_APPEND, 0)
		require.NoError(t, err)
		_, err = markerFile.WriteString("\n}}")
		require.NoError(t, err)
		require.NoError(t, markerFile.Close())
		markerBefore, err := os.ReadFile(markerPath)
		require.NoError(t, err)
		rawBefore, err := os.ReadFile(f.rawPath)
		require.NoError(t, err)

		reader.err = errors.New("reader must not run for quarantined capture")
		_, err = drainCursorFixture(t, f, reader, false)
		require.ErrorIs(t, err, ErrNotRecording)
		require.ErrorContains(t, err, "held for ownership review")
		assert.Equal(t, 1, reader.calls)
		markerAfter, readErr := os.ReadFile(markerPath)
		require.NoError(t, readErr)
		rawAfter, readErr := os.ReadFile(f.rawPath)
		require.NoError(t, readErr)
		assert.Equal(t, markerBefore, markerAfter)
		assert.Equal(t, rawBefore, rawAfter)
	})

	t.Run("finalized retry", func(t *testing.T) {
		f := newCursorCaptureFixture(t, "", source, false)
		reader := &fixedCursorIncrementalReader{
			entries: []adapters.RawEntry{{Role: "user", Content: "finalized before quarantine"}},
			next:    int64(len(source)),
		}
		_, err := finalizeCursorFixture(t, f, reader)
		require.NoError(t, err)
		require.NoError(t, UpdateRecordingStateAt(f.state.SessionPath, f.state.SessionID, func(state *RecordingState) {
			state.SourceRejected = true
		}))

		markerPath := filepath.Join(f.sessionPath, recordingFile)
		markerFile, err := os.OpenFile(markerPath, os.O_WRONLY|os.O_APPEND, 0)
		require.NoError(t, err)
		_, err = markerFile.WriteString("\n}}")
		require.NoError(t, err)
		require.NoError(t, markerFile.Close())
		markerBefore, err := os.ReadFile(markerPath)
		require.NoError(t, err)
		rawBefore, err := os.ReadFile(f.rawPath)
		require.NoError(t, err)

		reader.err = errors.New("reader must not run for quarantined finalized capture")
		_, err = finalizeCursorFixture(t, f, reader)
		require.ErrorIs(t, err, ErrNotRecording)
		require.ErrorContains(t, err, "held for ownership review")
		assert.Equal(t, 1, reader.calls)
		markerAfter, readErr := os.ReadFile(markerPath)
		require.NoError(t, readErr)
		rawAfter, readErr := os.ReadFile(f.rawPath)
		require.NoError(t, readErr)
		assert.Equal(t, markerBefore, markerAfter)
		assert.Equal(t, rawBefore, rawAfter)
	})
}

func TestCursorCaptureAcceptsRecoverableMarkerTail(t *testing.T) {
	source := "{\"role\":\"user\"}\n{\"type\":\"turn_ended\",\"status\":\"success\"}\n"
	f := newCursorCaptureFixture(t, "", source, false)
	markerPath := filepath.Join(f.sessionPath, recordingFile)
	markerFile, err := os.OpenFile(markerPath, os.O_WRONLY|os.O_APPEND, 0)
	require.NoError(t, err)
	_, err = markerFile.WriteString("\n}}")
	require.NoError(t, err)
	require.NoError(t, markerFile.Close())

	result, err := drainCursorFixture(t, f, &fixedCursorIncrementalReader{
		entries: []adapters.RawEntry{{Role: "user", Content: "captured after stale tail"}},
		next:    int64(len(source)),
	}, false)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 1, result.Entries)

	marker, err := os.ReadFile(markerPath)
	require.NoError(t, err)
	var state RecordingState
	require.NoError(t, json.Unmarshal(marker, &state), "successful checkpoint must replace the recoverable stale tail")
	assert.Equal(t, 1, state.EntryCount)
}
