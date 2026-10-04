package agentwork

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/require"
)

// Failure prevented: two watchers sharing .recording.json.tmp publish an inode
// the other writer still owns, allowing trailing bytes after the final rename.
func TestWatcherStatePublishDoesNotReusePendingWriterFile(t *testing.T) {
	for _, count := range []int{10, 1000} {
		t.Run(strconv.Itoa(count), func(t *testing.T) {
			dir := t.TempDir()
			state := &session.RecordingState{AgentID: "test", SessionPath: dir, EntryCount: 9}
			require.NoError(t, session.SaveRecordingState(dir, state))
			short, err := json.MarshalIndent(state, "", "  ")
			require.NoError(t, err)
			// Model another watcher paused after opening the shared temporary
			// file. Resume its write only after this watcher has published.
			pending, err := os.OpenFile(filepath.Join(dir, recordingMarker)+".tmp", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
			require.NoError(t, err)
			t.Cleanup(func() { _ = pending.Close() })
			mgr := newTestWatcherManager(t)
			require.NoError(t, mgr.persistOffset(&activeWatcher{cachePath: dir, sessionName: "test"}, 0, count-9))
			_, err = pending.Write(short)
			require.NoError(t, err)
			require.NoError(t, pending.Close())

			got, err := session.ReadRecordingStateFile(dir)
			require.NoError(t, err, "published state must not share a temporary file with another writer")
			require.Equal(t, count, got.EntryCount)
		})
	}
}
