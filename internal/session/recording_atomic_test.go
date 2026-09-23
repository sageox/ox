package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

// Failure prevented: overlapping whole-file writes leave the suffix of a longer
// JSON object behind a shorter one, or change bytes an existing reader sees.
func TestRecordingStatePublishIsolatedFromOpenFile(t *testing.T) {
	for _, count := range []int{10, 1000} {
		t.Run(strconv.Itoa(count), func(t *testing.T) {
			dir := t.TempDir()
			state := &RecordingState{AgentID: "test", SessionPath: dir, EntryCount: 9}
			require.NoError(t, SaveRecordingState(dir, state))
			path := filepath.Join(dir, recordingFile)
			short, err := json.MarshalIndent(state, "", "  ")
			require.NoError(t, err)

			// Pause a competing in-place writer immediately after its open with
			// O_TRUNC, exactly the first syscall in os.WriteFile. Holding this
			// descriptor makes the interleaving deterministic, without sleeps.
			pending, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0600)
			require.NoError(t, err)
			t.Cleanup(func() { _ = pending.Close() })
			state.EntryCount = count
			require.NoError(t, SaveRecordingState(dir, state))
			_, err = pending.Write(short)
			require.NoError(t, err)
			require.NoError(t, pending.Close())

			got, err := ReadRecordingStateFile(dir)
			require.NoError(t, err, "publishing a state must detach it from an already-open writer")
			require.Equal(t, count, got.EntryCount)
		})
	}
}

// Failure prevented: an unreadable recording blocks retention without telling
// the coworker which file needs attention. Invalid JSON must remain an error.
func TestRecordingStateParseErrorNamesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, recordingFile)
	require.NoError(t, os.WriteFile(path, []byte(`{"agent_id":"test"}}`), 0600))
	_, err := ReadRecordingStateFile(dir)
	require.ErrorContains(t, err, path)
}
