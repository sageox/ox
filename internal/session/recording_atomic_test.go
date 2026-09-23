package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
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
			_, err = pending.Write(short)
			require.NoError(t, err)
			_, err = pending.Seek(0, 0)
			require.NoError(t, err)
			state.EntryCount = count
			err = SaveRecordingState(dir, state)
			if runtime.GOOS == "windows" {
				// A Go handle denies delete sharing. A blocked replacement must
				// preserve the old state and allow retry once the handle closes.
				require.Error(t, err)
				prior, readErr := ReadRecordingStateFile(dir)
				require.NoError(t, readErr)
				require.Equal(t, 9, prior.EntryCount)
				require.NoError(t, pending.Close())
				require.NoError(t, SaveRecordingState(dir, state))
			} else {
				require.NoError(t, err)
				_, err = pending.Write(short)
				require.NoError(t, err)
				require.NoError(t, pending.Close())
			}

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

func TestRecordingStateIOErrorsNameFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, recordingFile)
	// A directory in place of the marker fails both reading and replacement,
	// including on Windows and when the test process has elevated privileges.
	require.NoError(t, os.Mkdir(path, 0700))
	_, err := ReadRecordingStateFile(dir)
	require.ErrorContains(t, err, path)
	require.ErrorContains(t, SaveRecordingState(dir, &RecordingState{SessionPath: dir}), path)
}
