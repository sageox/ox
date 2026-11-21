package session_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/internal/livesession"
	"github.com/sageox/ox/internal/livesession/livesessiontest"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/sessionid"
	"github.com/stretchr/testify/require"
)

// Failure prevented (ox #2002 review): the hold writer took no lock, so a hold
// placed after a close checked for one was followed by that close's summary
// send. A hold and the send are now mutually exclusive under the folder's
// live.lock: while a close holds it the hold waits, and the close it waited
// for finished before the marker existed. Built from livesession's own stream
// state and lock key, and named through a symlink as a hook names the folder,
// so a drift in the state file name, the lock name or the path resolution
// fails here.
func TestWriteHoldMarker_WaitsForTheLiveLockOfAStreamingFolder(t *testing.T) {
	cfg := livesessiontest.New(t).Config(t)
	sessions := filepath.Join(t.TempDir(), "sessions")
	real := filepath.Join(sessions, "2026-03-04T13-41-person-a-Oxabc1")
	require.NoError(t, os.MkdirAll(real, 0o755))
	st, err := livesession.Prepare(t.Context(), cfg, real, livesession.OpenParams{SessionID: sessionid.GenerateSessionID()})
	require.NoError(t, err)
	link := filepath.Join(t.TempDir(), "sessions-link")
	require.NoError(t, os.Symlink(sessions, link))
	via := filepath.Join(link, filepath.Base(real))

	locked, release, closeDone := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		closeDone <- fileutil.WithFileLockTimeout(context.Background(), livesession.LockTarget(real), 5*time.Second, func() error {
			close(locked)
			<-release
			// a close's last look before it sends its summary
			if session.IsHeld(real) {
				t.Error("the hold landed while the close held live.lock")
			}
			return nil
		})
	}()
	<-locked

	holdDone := make(chan error, 1)
	go func() {
		holdDone <- session.WriteHoldMarkerFor(via, session.HoldManualPublishing, "session_stop", st.SessionID)
	}()
	select {
	case err := <-holdDone:
		t.Fatalf("the hold was written while a close held live.lock (err=%v)", err)
	case <-time.After(300 * time.Millisecond):
	}
	require.False(t, session.IsHeld(real), "the hold landed under a close")

	close(release)
	require.NoError(t, <-closeDone)
	select {
	case err := <-holdDone:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("the hold never followed the close")
	}
	require.True(t, session.IsHeld(real))
	hold, err := session.ReadHoldMarker(real)
	require.NoError(t, err)
	require.Equal(t, st.SessionID, hold.SessionID)
}
