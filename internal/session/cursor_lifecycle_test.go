package session

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/session/adapters"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func cursorPauseUpdate(projectRoot, agentID string) error {
	return UpdateRecordingStateForAgent(projectRoot, agentID, func(state *RecordingState) {
		now := time.Now().UTC()
		state.SuspendedAt = &now
		state.Lifecycle = append(state.Lifecycle, LifecycleEvent{Action: LifecycleActionPause, At: now, Seq: state.EntryCount})
	})
}

func TestCursorLifecycle_RejectsUnacknowledgedRawUntilDrainReconciles(t *testing.T) {
	source := `{"role":"user"}` + "\n" + `{"type":"turn_ended","status":"success"}` + "\n"
	f := newCursorCaptureFixture(t, "", source, false)
	reader := &fixedCursorIncrementalReader{
		entries: []adapters.RawEntry{{Role: "user", Content: "must be acknowledged first"}},
		next:    int64(len(source)),
	}

	original := writeRecordingStateAtomically
	writeRecordingStateAtomically = func(string, any, os.FileMode) error { return errors.New("injected checkpoint failure") }
	t.Cleanup(func() { writeRecordingStateAtomically = original })
	_, err := drainCursorFixture(t, f, reader, false)
	require.ErrorContains(t, err, "checkpoint Cursor capture")
	writeRecordingStateAtomically = original

	before := loadCursorCaptureState(t, f)
	require.Zero(t, before.EntryCount)
	require.ErrorContains(t, cursorPauseUpdate(f.projectRoot, f.state.AgentID), "checkpoint is pending")
	afterRejected := loadCursorCaptureState(t, f)
	assert.Equal(t, before.EntryCount, afterRejected.EntryCount)
	assert.Nil(t, afterRejected.SuspendedAt)
	assert.Empty(t, afterRejected.Lifecycle)

	_, err = drainCursorFixture(t, f, reader, false)
	require.NoError(t, err)
	require.NoError(t, cursorPauseUpdate(f.projectRoot, f.state.AgentID))
	afterRecovered := loadCursorCaptureState(t, f)
	require.NotNil(t, afterRecovered.SuspendedAt)
	require.Len(t, afterRecovered.Lifecycle, 1)
	assert.Equal(t, LifecycleActionPause, afterRecovered.Lifecycle[0].Action)
	assert.Equal(t, 1, afterRecovered.Lifecycle[0].Seq, "pause is sequenced after reconciled capture")
}

func TestCursorLifecycle_RejectsChangesAfterFinalizedRawWithoutStoppedAt(t *testing.T) {
	source := `{"role":"assistant"}` + "\n" + `{"type":"turn_ended","status":"success"}` + "\n"
	f := newCursorCaptureFixture(t, "", source, false)
	reader := &fixedCursorIncrementalReader{
		entries: []adapters.RawEntry{{Role: "assistant", Content: "orphan final"}},
		next:    int64(len(source)),
	}
	_, err := finalizeCursorFixture(t, f, reader)
	require.NoError(t, err)
	state := loadCursorCaptureState(t, f)
	assert.Nil(t, state.StoppedAt, "orphan finalization must be protected by raw marker, not only StoppedAt")

	err = cursorPauseUpdate(f.projectRoot, f.state.AgentID)
	require.ErrorIs(t, err, ErrNotRecording)
	state = loadCursorCaptureState(t, f)
	assert.Nil(t, state.SuspendedAt)
	assert.Empty(t, state.Lifecycle)
}

func TestCursorLifecycle_NonCursorUpdateRemainsUnchanged(t *testing.T) {
	projectRoot := setupRecordingTest(t, t.TempDir())
	source := filepath.Join(t.TempDir(), "source.jsonl")
	require.NoError(t, os.WriteFile(source, []byte("fixture\n"), 0o600))
	state, err := StartRecording(projectRoot, StartRecordingOptions{
		AgentID: "OxNonCursorLifecycle", AdapterName: "codex", SessionFile: source,
		OutputFile: filepath.Join(t.TempDir(), "out.md"), WatchMode: "tail",
	})
	require.NoError(t, err)
	require.NoError(t, cursorPauseUpdate(projectRoot, state.AgentID))
	updated, err := LoadRecordingStateForAgent(projectRoot, state.AgentID)
	require.NoError(t, err)
	require.NotNil(t, updated.SuspendedAt)
	require.Len(t, updated.Lifecycle, 1)
	assert.Equal(t, LifecycleActionPause, updated.Lifecycle[0].Action)
	assert.Zero(t, updated.Lifecycle[0].Seq)
}
