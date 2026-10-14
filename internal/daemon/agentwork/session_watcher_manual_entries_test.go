package agentwork

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// User-supplied batches do not advance a native cursor. They must not prevent
// another adapter from starting or recovering its watcher and capturing new rows.
func TestSessionWatcher_ManualEntriesDoNotBlockNativeCapture(t *testing.T) {
	for _, start := range []string{"ipc", "doctor"} {
		for _, prefix := range []string{"", "before recording\n"} {
			name := "zero-offset"
			if prefix != "" {
				name = "recording-boundary"
			}
			t.Run(start+"/"+name, func(t *testing.T) {
				if testing.Short() && prefix == "" {
					t.Skip("short: zero-offset capture waits for the native polling interval")
				}
				mgr := newTestWatcherManager(t)
				t.Cleanup(mgr.StopAll)
				ledgerPath := t.TempDir()
				cachePath := filepath.Join(ledgerPath, ".sageox", "cache", "sessions", "manual-batch")
				require.NoError(t, os.MkdirAll(cachePath, 0o700))
				sourcePath := mgr.codexSessionPath(t, "manual-batch.jsonl")
				source := prefix + "new native message\n"
				require.NoError(t, os.WriteFile(sourcePath, []byte(source), 0o600))
				rawPath := filepath.Join(cachePath, "raw.jsonl")
				writer, err := session.NewRawWriter(rawPath, "")
				require.NoError(t, err)
				require.NoError(t, writer.WriteEntry(&session.SessionEntry{Type: session.EntryTypeUser, Content: "user-added batch"}))
				require.NoError(t, writer.Close())
				state := session.RecordingState{
					SessionPath: cachePath, SessionFile: sourcePath,
					AdapterName: "codex", WatchMode: "tail", ParentPID: os.Getpid(),
					StartOffset: int64(len(prefix)), SourceOffset: int64(len(prefix)), EntryCount: 1,
				}
				recPath := filepath.Join(cachePath, recordingMarker)
				writeRecordingState(t, recPath, state)
				if start == "ipc" {
					require.NoError(t, mgr.StartWatch("manual-batch", sourcePath, "codex", ledgerPath, cachePath))
				} else {
					require.Equal(t, 1, mgr.DetectAndRestart(ledgerPath))
				}
				require.Eventually(t, func() bool {
					data, err := os.ReadFile(recPath)
					if err != nil {
						return false
					}
					var updated session.RecordingState
					return json.Unmarshal(data, &updated) == nil &&
						updated.SourceOffset == int64(len(source)) && updated.EntryCount == 2
				}, 4*time.Second, 10*time.Millisecond, "native capture must advance after a user batch")
				mgr.StopAll()
				raw, err := os.ReadFile(rawPath)
				require.NoError(t, err)
				assert.Contains(t, string(raw), "user-added batch")
				assert.Contains(t, string(raw), "new native message")
				assert.NotContains(t, string(raw), "before recording")
				assert.Equal(t, 2, countRawJSONLEntries(t, rawPath))
			})
		}
	}
}
