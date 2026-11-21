package agentwork

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/livesession"
	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// liveGap is a declared loss in the live spool.
type liveGap struct {
	Type   string `json:"type"`
	Reason string `json:"reason"`
	Bytes  int64  `json:"bytes"`
}

// lockStreamState makes the live stream's state unreadable until restore, so
// the stream cannot take a batch committed in the meantime.
func lockStreamState(t *testing.T, dir string) (restore func()) {
	t.Helper()
	path := filepath.Join(dir, "stream-state.json")
	require.NoError(t, os.Chmod(path, 0o200))
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
	return func() { require.NoError(t, os.Chmod(path, 0o600)) }
}

// nextLiveGap makes the stream's next write and returns the first line it
// added: the gap it declared ahead of its own line, if it declared one.
func nextLiveGap(t *testing.T, dir string) liveGap {
	t.Helper()
	before := spoolOf(t, dir)
	_, err := livesession.Spool(context.Background(), dir, []byte(`{"type":"msg","content":"the next write"}`+"\n"), livesession.SpoolOptions{})
	require.NoError(t, err)
	first, _, _ := strings.Cut(spoolOf(t, dir)[len(before):], "\n")
	dec := json.NewDecoder(strings.NewReader(first))
	dec.DisallowUnknownFields()
	var g liveGap
	if err := dec.Decode(&g); err != nil || g.Type != "gap" {
		t.Fatalf("the next write declared no gap for the committed batch; it began with %.160q (%v)", first, err)
	}
	return g
}

// Failure prevented (finding 5): the tail watcher committed a batch to
// raw.jsonl and moved its cursor, then could not hand the batch to the live
// stream, and nothing said so. With no gap line in its place, the close
// claimed a complete stream that was missing a turn. A committed batch the
// stream cannot take is declared: the next write carries a gap of its size.
func TestTailWatcherDeclaresABatchTheStreamCouldNotTake(t *testing.T) {
	skipAsRoot(t)
	dir, rawPath, rw := newAppendFixture(t, session.RecordingState{SessionID: watcherLiveSessionID})
	prepareLive(t, dir, watcherLiveSessionID)
	m := NewSessionWatcherManager(slog.Default())
	drains := wireLiveStreaming(m)
	rawBefore, err := os.ReadFile(rawPath)
	require.NoError(t, err)
	spoolBefore := spoolOf(t, dir)
	restore := lockStreamState(t, dir)

	require.NoError(t, m.appendBatch(&activeWatcher{cachePath: dir, sessionName: "s"}, rw,
		[]session.Entry{{Type: session.EntryTypeUser, Content: "a turn the stream never got"}}, 10))

	restore()
	rawAfter, err := os.ReadFile(rawPath)
	require.NoError(t, err)
	committed := int64(len(rawAfter) - len(rawBefore))
	require.Positive(t, committed, "precondition: the batch was committed to raw.jsonl")
	assert.Equal(t, spoolBefore, spoolOf(t, dir), "precondition: the stream could not take the batch")
	assert.Empty(t, drains.all(), "nothing was spooled, so there is nothing to drain")
	g := nextLiveGap(t, dir)
	assert.Equal(t, "spool_write_failed", g.Reason)
	assert.Equal(t, committed, g.Bytes, "the gap must be the size of the batch the stream never got")
}

// Failure prevented (finding 5): Cursor's capture transaction, which its
// watcher, hook, stop and recovery all share, dropped a committed batch it
// could not hand to the live stream without a gap line in its place.
func TestCursorWatcherDeclaresABatchTheStreamCouldNotTake(t *testing.T) {
	skipAsRoot(t)
	installCursorWatcherAdapter(t)
	source := `{"role":"user","content":"first"}` + "\n" +
		`{"role":"assistant","content":"second"}` + "\n" +
		`{"type":"turn_ended","status":"success"}` + "\n"
	f := newCursorWatcherFixture(t, source, false, false)
	f.state.SessionID = watcherLiveSessionID
	writeRecordingState(t, filepath.Join(f.cachePath, recordingMarker), f.state)
	prepareLive(t, f.cachePath, watcherLiveSessionID)
	rawPath := filepath.Join(f.cachePath, artifactRaw)
	var rawBefore int64
	if info, err := os.Stat(rawPath); err == nil {
		rawBefore = info.Size()
	}
	spoolBefore := spoolOf(t, f.cachePath)
	mgr := newCursorWatcherManager(t, f.homeDir)
	drains := wireLiveStreaming(mgr)
	restore := lockStreamState(t, f.cachePath)
	startCursorWatch(t, mgr, f)

	require.Eventually(t, func() bool { return loadCursorWatcherState(t, f).EntryCount == 2 }, time.Second, 10*time.Millisecond)
	info, err := os.Stat(rawPath)
	require.NoError(t, err)
	mgr.StopAll() // the poll that committed the batch has handed it on
	restore()

	committed := info.Size() - rawBefore
	require.Positive(t, committed, "precondition: the batch was committed to raw.jsonl")
	assert.Equal(t, spoolBefore, spoolOf(t, f.cachePath), "precondition: the stream could not take the batch")
	assert.Empty(t, drains.all(), "nothing was spooled, so there is nothing to drain")
	g := nextLiveGap(t, f.cachePath)
	assert.Equal(t, "spool_write_failed", g.Reason)
	assert.Equal(t, committed, g.Bytes, "the gap must be the size of the batch the stream never got")
}
