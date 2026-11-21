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

// Failure prevented: a folder that never streamed gained a live.lock it has
// no use for, and paid the lock's wait for nothing.
func TestWriteHoldMarker_NonLiveFolderTakesNoLiveLock(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, WriteHoldMarkerFor(dir, HoldManualPublishing, "session_stop", "ses_test"))
	assert.True(t, IsHeld(dir))

	_, err := os.Lstat(filepath.Join(dir, LiveLockFileName))
	assert.ErrorIs(t, err, os.ErrNotExist, "a non-live folder gained a live.lock")
	_, err = os.Lstat(fileutil.LockPath(liveLockTarget(dir)))
	assert.ErrorIs(t, err, os.ErrNotExist, "a non-live folder's live.lock was taken")
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
