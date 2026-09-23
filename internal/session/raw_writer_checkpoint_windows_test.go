package session

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// Windows denies replacing a marker opened without delete sharing. Exercise
// the actual publication failure, raw rollback, and retry after releasing it.
func TestRawCheckpointWindowsSharingViolation(t *testing.T) {
	dir := t.TempDir()
	state := &RecordingState{SessionPath: dir}
	require.NoError(t, SaveRecordingState(dir, state))
	held, err := os.Open(filepath.Join(dir, recordingFile))
	require.NoError(t, err)
	t.Cleanup(func() { _ = held.Close() })
	raw := filepath.Join(dir, "raw.jsonl")
	w, err := NewRawWriter(raw, "")
	require.NoError(t, err)
	t.Cleanup(func() { _ = w.Close() })
	state.SourceOffset, state.EntryCount = 1, 1
	entries := []Entry{{Type: EntryTypeUser, Content: "one batch"}}
	publish := func() error { return SaveRecordingState(dir, state) }
	require.Error(t, w.AppendCheckpointed(entries, publish))
	data, err := os.ReadFile(raw)
	require.NoError(t, err)
	require.Empty(t, data)
	prior, err := ReadRecordingStateFile(dir)
	require.NoError(t, err)
	require.Zero(t, prior.SourceOffset)
	require.NoError(t, held.Close())
	require.NoError(t, w.AppendCheckpointed(entries, publish))
	stored, err := ReadRecordingStateFile(dir)
	require.NoError(t, err)
	require.EqualValues(t, 1, stored.SourceOffset)
	require.Equal(t, 1, stored.EntryCount)
	data, err = os.ReadFile(raw)
	require.NoError(t, err)
	require.Contains(t, string(data), "one batch")
}
