package agentwork

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/livesession"
	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	watcherLiveSessionID = "ses_01934f3e-6b2a-7c3d-8e4f-5a6b7c8d9e01"
	otherLiveSessionID   = "ses_01934f3e-6b2a-7c3d-8e4f-5a6b7c8d9e02"
)

// prepareLive gives dir a pending live stream for sessionID, as session start does.
func prepareLive(t *testing.T, dir, sessionID string) {
	t.Helper()
	_, err := livesession.Prepare(context.Background(), livesession.Config{Endpoint: "https://live.test.sageox.ai", DataDir: t.TempDir()}, dir,
		livesession.OpenParams{SessionID: sessionID, SessionName: filepath.Base(dir)})
	require.NoError(t, err)
}

func newAppendFixture(t *testing.T, rec session.RecordingState) (dir, rawPath string, rw *session.RawWriter) {
	t.Helper()
	dir = t.TempDir()
	rec.CommandRedactionVersion = 1
	data, _ := json.Marshal(rec)
	require.NoError(t, os.WriteFile(filepath.Join(dir, recordingMarker), data, 0o600))
	rawPath = filepath.Join(dir, "raw.jsonl")
	require.NoError(t, os.WriteFile(rawPath, []byte(`{"type":"header"}`+"\n"), 0o600))
	rw, err := session.NewRawWriter(rawPath, "")
	require.NoError(t, err)
	t.Cleanup(func() { _ = rw.Close() })
	return dir, rawPath, rw
}

// The tail watcher appends through appendBatch. Live streaming needs the exact
// bytes of each committed batch, spooled only after the append succeeded; a
// failed append must spool nothing.
func TestAppendBatchHandsTheCommittedBytesToTheLiveSpool(t *testing.T) {
	dir, rawPath, rw := newAppendFixture(t, session.RecordingState{SessionID: watcherLiveSessionID})
	prepareLive(t, dir, watcherLiveSessionID)
	m := NewSessionWatcherManager(slog.Default())
	drains := wireLiveStreaming(m)
	aw := &activeWatcher{cachePath: dir, sessionName: "s"}

	require.NoError(t, m.appendBatch(aw, rw, []session.Entry{{Type: session.EntryTypeUser, Content: "hello"}}, 10))
	all, _ := os.ReadFile(rawPath)
	header := len(`{"type":"header"}` + "\n")
	assert.Equal(t, string(all[header:]), spoolOf(t, dir))
	assert.Equal(t, []string{dir}, drains.all())

	require.Error(t, m.appendBatch(aw, rw, []session.Entry{{Type: session.EntryTypeUser, Content: "again"}}, 5), "cursor goes backwards")
	assert.Equal(t, string(all[header:]), spoolOf(t, dir), "a failed append reached the live spool")
	assert.Len(t, drains.all(), 1)
}

// Failure prevented: the watcher handed every committed batch to the live
// spool without asking whose stream it was or whether the session may be
// shared. A recording that reused a folder (names have minute precision) fed
// the previous recording's stream (C4), and a session switched to manual
// publishing kept streaming (C1).
func TestAppendBatchKeepsBatchesOutOfStreamsTheyMustNotReach(t *testing.T) {
	cases := []struct {
		name  string
		rec   session.RecordingState
		setup func(t *testing.T, dir string)
	}{
		{"stream belongs to the previous recording in this folder", session.RecordingState{SessionID: watcherLiveSessionID},
			func(t *testing.T, dir string) { prepareLive(t, dir, otherLiveSessionID) }},
		{"session held on this machine", session.RecordingState{SessionID: watcherLiveSessionID},
			func(t *testing.T, dir string) {
				prepareLive(t, dir, watcherLiveSessionID)
				require.NoError(t, session.WriteHoldMarker(dir, session.HoldManualPublishing, "test"))
			}},
		{"recording switched to manual publishing", session.RecordingState{SessionID: watcherLiveSessionID, PublishingMode: config.SessionPublishingManual},
			func(t *testing.T, dir string) { prepareLive(t, dir, watcherLiveSessionID) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir, _, rw := newAppendFixture(t, tc.rec)
			tc.setup(t, dir)
			m := NewSessionWatcherManager(slog.Default())
			drains := wireLiveStreaming(m)

			require.NoError(t, m.appendBatch(&activeWatcher{cachePath: dir, sessionName: "s"}, rw,
				[]session.Entry{{Type: session.EntryTypeUser, Content: "hello"}}, 10))

			assert.NotContains(t, spoolOf(t, dir), "hello", "the batch reached a live stream it must not")
			assert.NoFileExists(t, filepath.Join(dir, "spool-lost"), "a batch the stream must not carry is not a loss")
			assert.Empty(t, drains.all())
		})
	}
}

// Failure prevented: Cursor recordings capture through DrainCursorSource, not
// appendBatch, so their committed turns never reached the live spool and the
// streamed session held only its header on SageOx (C8).
func TestCursorWatcherHandsCommittedBytesToTheLiveSpool(t *testing.T) {
	installCursorWatcherAdapter(t)
	source := `{"role":"user","content":"first"}` + "\n" +
		`{"role":"assistant","content":"second"}` + "\n" +
		`{"type":"turn_ended","status":"success"}` + "\n"
	f := newCursorWatcherFixture(t, source, false, false)
	f.state.SessionID = watcherLiveSessionID
	writeRecordingState(t, filepath.Join(f.cachePath, recordingMarker), f.state)
	prepareLive(t, f.cachePath, watcherLiveSessionID)
	mgr := newCursorWatcherManager(t, f.homeDir)
	drains := wireLiveStreaming(mgr)
	startCursorWatch(t, mgr, f)

	require.Eventually(t, func() bool { return loadCursorWatcherState(t, f).EntryCount == 2 }, time.Second, 10*time.Millisecond)
	require.Eventually(t, func() bool { return len(drains.all()) > 0 }, time.Second, 10*time.Millisecond)

	raw, err := os.ReadFile(filepath.Join(f.cachePath, artifactRaw))
	require.NoError(t, err)
	for _, dir := range drains.all() {
		assert.Equal(t, f.cachePath, dir)
	}
	assert.Equal(t, string(raw), spoolOf(t, f.cachePath), "every committed Cursor byte, in order")
}
