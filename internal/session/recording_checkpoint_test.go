package session

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCheckpointRecordingState_MergesConcurrentLifecycleUpdates(t *testing.T) {
	projectRoot := t.TempDir()
	sessionPath := filepath.Join(t.TempDir(), "session")
	state := &RecordingState{
		AgentID: "OxCheckpoint", SessionPath: sessionPath, EntryCount: 4,
		SourceOffset: 40, Lifecycle: []LifecycleEvent{{Action: LifecycleActionStart, Seq: 0}},
	}
	require.NoError(t, SaveRecordingState(projectRoot, state))

	pausedAt := time.Now().UTC().Truncate(time.Second)
	stoppedAt := pausedAt.Add(time.Second)
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, 3)
	for _, update := range []func() error{
		func() error { return CheckpointRecordingState(projectRoot, sessionPath, 100, 3) },
		func() error {
			return updateRecordingStateAtPath(projectRoot, recordingStatePath(sessionPath), func(s *RecordingState) {
				s.SuspendedAt = &pausedAt
				s.PauseCount++
				s.Lifecycle = append(s.Lifecycle, LifecycleEvent{Action: LifecycleActionPause, At: pausedAt, Seq: s.EntryCount})
			})
		},
		func() error {
			return updateRecordingStateAtPath(projectRoot, recordingStatePath(sessionPath), func(s *RecordingState) {
				s.StoppedAt = &stoppedAt
			})
		},
	} {
		wg.Add(1)
		go func(update func() error) {
			defer wg.Done()
			<-start
			errs <- update()
		}(update)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	data, err := os.ReadFile(recordingStatePath(sessionPath))
	require.NoError(t, err)
	var got RecordingState
	require.NoError(t, json.Unmarshal(data, &got))
	assert.Equal(t, int64(100), got.SourceOffset)
	assert.Equal(t, 7, got.EntryCount)
	assert.Equal(t, &pausedAt, got.SuspendedAt)
	assert.Equal(t, &stoppedAt, got.StoppedAt)
	require.Len(t, got.Lifecycle, 2)
	assert.Equal(t, LifecycleActionPause, got.Lifecycle[1].Action)
}

func TestCheckpointRecordingState_WriteFailureLeavesCheckpointUnacknowledged(t *testing.T) {
	projectRoot := t.TempDir()
	sessionPath := filepath.Join(t.TempDir(), "session")
	state := &RecordingState{SessionPath: sessionPath, SourceOffset: 12, EntryCount: 2}
	require.NoError(t, SaveRecordingState(projectRoot, state))

	original := writeRecordingStateAtomically
	writeRecordingStateAtomically = func(string, any, os.FileMode) error {
		return errors.New("injected checkpoint write failure")
	}
	t.Cleanup(func() { writeRecordingStateAtomically = original })

	err := CheckpointRecordingState(projectRoot, sessionPath, 30, 2)
	require.Error(t, err)

	data, readErr := os.ReadFile(recordingStatePath(sessionPath))
	require.NoError(t, readErr)
	var got RecordingState
	require.NoError(t, json.Unmarshal(data, &got))
	assert.Equal(t, int64(12), got.SourceOffset)
	assert.Equal(t, 2, got.EntryCount)
}

func TestCheckpointRecordingState_StaleOffsetDoesNotDoubleCount(t *testing.T) {
	projectRoot := t.TempDir()
	sessionPath := filepath.Join(t.TempDir(), "session")
	require.NoError(t, SaveRecordingState(projectRoot, &RecordingState{SessionPath: sessionPath}))

	require.NoError(t, CheckpointRecordingState(projectRoot, sessionPath, 10, 1))
	require.NoError(t, CheckpointRecordingState(projectRoot, sessionPath, 10, 4))
	require.NoError(t, CheckpointRecordingState(projectRoot, sessionPath, 9, 4))

	data, err := os.ReadFile(recordingStatePath(sessionPath))
	require.NoError(t, err)
	var got RecordingState
	require.NoError(t, json.Unmarshal(data, &got))
	assert.Equal(t, int64(10), got.SourceOffset)
	assert.Equal(t, 1, got.EntryCount)
}
