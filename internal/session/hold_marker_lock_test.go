package session

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sageox/ox/internal/fileutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// liveStateFileName is livesession's stream state file.
const liveStateFileName = "stream-state.json"

// liveFolder is a session folder with a live stream state, as livesession
// leaves one (its content does not matter to the hold writer).
func liveFolder(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, liveStateFileName), []byte(`{"status":"closing"}`), 0o600))
	return dir
}

// holdLiveLock takes dir's live.lock until the returned release is called, as
// a close in flight does.
func holdLiveLock(t *testing.T, dir string) (release func()) {
	t.Helper()
	locked, done := make(chan struct{}), make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = fileutil.WithFileLockTimeout(context.Background(), liveLockTarget(dir), 5*time.Second, func() error {
			close(locked)
			<-done
			return nil
		})
	}()
	select {
	case <-locked:
	case <-time.After(5 * time.Second):
		t.Fatal("could not take live.lock")
	}
	var once sync.Once
	release = func() { once.Do(func() { close(done); wg.Wait() }) }
	t.Cleanup(release)
	return release
}

func shortenHoldLockWait(t *testing.T, d time.Duration) {
	t.Helper()
	prev := holdLockWait
	holdLockWait = d
	t.Cleanup(func() { holdLockWait = prev })
}

// Failure prevented (ox #2002 review): the writer took live.lock only once
// stream state existed, so Prepare could create it between that check and the
// marker write and a close could pass its IsHeld check in the gap. A folder
// with no stream state yet waits for the lock too, and the lock file lives in
// the shared lock directory, never in the session folder.
func TestWriteHoldMarker_TakesTheLiveLockBeforeAnyStreamExists(t *testing.T) {
	dir := t.TempDir()
	attempted := make(chan struct{})
	prev := holdLockAttempt
	holdLockAttempt = func() { close(attempted) }
	t.Cleanup(func() { holdLockAttempt = prev })
	release := holdLiveLock(t, dir)

	done := make(chan error, 1)
	go func() { done <- WriteHoldMarkerFor(dir, HoldManualPublishing, "session_stop", "ses_test") }()
	select {
	case <-attempted:
	case err := <-done:
		t.Fatalf("the hold finished without asking for live.lock (err=%v)", err)
	case <-time.After(5 * time.Second):
		t.Fatal("the hold never asked for live.lock")
	}
	assert.False(t, IsHeld(dir), "the hold landed while live.lock was held")
	release()
	require.NoError(t, <-done)
	assert.True(t, IsHeld(dir))
	_, err := os.Lstat(filepath.Join(dir, LiveLockFileName))
	assert.ErrorIs(t, err, os.ErrNotExist, "the session folder gained a live.lock file")
}

// Failure prevented: a hold that waited for a close stuck past every budget
// was dropped with the lock timeout, so the session was published after all.
// A hold is never lost: it is written anyway, and that is logged.
func TestWriteHoldMarker_LockTimeoutStillWritesTheHold(t *testing.T) {
	dir := liveFolder(t)
	shortenHoldLockWait(t, 150*time.Millisecond)
	holdLiveLock(t, dir)
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	start := time.Now()
	require.NoError(t, WriteHoldMarkerFor(dir, HoldManualPublishing, "session_stop", "ses_test"))

	assert.GreaterOrEqual(t, time.Since(start), 150*time.Millisecond, "the hold did not wait for live.lock")
	assert.True(t, IsHeld(dir), "the hold was lost to the lock timeout")
	assert.Contains(t, logs.String(), `"msg":"session_hold_without_live_lock"`)
	assert.Equal(t, 1, strings.Count(logs.String(), "\n"), "one single-line log entry")
}

// Failure prevented: code already holding live.lock (a drain hearing of a
// delete) that took it again waited out the whole bound behind itself.
func TestWriteHoldMarkerForLocked_NeverTakesTheLock(t *testing.T) {
	dir := liveFolder(t)
	shortenHoldLockWait(t, 2*time.Second)
	holdLiveLock(t, dir)

	start := time.Now()
	require.NoError(t, WriteHoldMarkerForLocked(dir, HoldDeletedOnServer, "live_stream", "ses_test"))

	assert.Less(t, time.Since(start), time.Second, "the locked variant waited for live.lock")
	hold, err := ReadHoldMarker(dir)
	require.NoError(t, err)
	assert.Equal(t, HoldDeletedOnServer, hold.Reason)
	assert.Equal(t, "ses_test", hold.SessionID)
}

// Failure prevented: re-holding an already held live folder (stop writes the
// hold the stream already wrote) waited for a lock with nothing to order.
func TestWriteHoldMarker_AlreadyHeldNeedsNoLock(t *testing.T) {
	dir := liveFolder(t)
	require.NoError(t, WriteHoldMarkerForLocked(dir, HoldDeletedOnServer, "live_stream", ""))
	shortenHoldLockWait(t, 2*time.Second)
	holdLiveLock(t, dir)

	start := time.Now()
	require.NoError(t, WriteHoldMarker(dir, HoldDeletedOnServer, "session_stop"))

	assert.Less(t, time.Since(start), time.Second, "an existing hold waited for live.lock")
	hold, err := ReadHoldMarker(dir)
	require.NoError(t, err)
	assert.Equal(t, "live_stream", hold.Source, "the first hold survives")
}
