package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/agentinstance"
	"github.com/sageox/ox/internal/daemon"
	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recoverAfterCrash runs `ox agent <id> session recover` once the crash took
// the native transcript, so recovery publishes from the cache alone.
func (f *liveFixture) recoverAfterCrash() sessionRecoverOutput {
	f.t.Helper()
	require.NoError(f.t, os.Remove(f.sourceFile))
	var err error
	out := captureRealStdout(f.t, func() { err = recoverAgentSession(&agentinstance.Instance{AgentID: f.agentID}, false) })
	require.NoError(f.t, err)
	var got sessionRecoverOutput
	require.NoError(f.t, json.Unmarshal(out, &got))
	return got
}

// Failure prevented: an AI teammate crashed and cache-only recovery published
// its recording, but nothing closed its live stream, so SageOx showed the
// session live until the server's 72 h auto-finish.
func TestLiveRecoverFromCacheClosesTheStream(t *testing.T) {
	f := newLiveFixture(t, liveOpts{streaming: true})
	f.turn("work before the crash")
	st := f.openAndDrain()

	got := f.recoverAfterCrash()

	require.Equal(t, "session recovered from cache", got.Message, "precondition: recovered from the cache")
	require.True(t, f.liveState().CloseRequested, "recovery must ask the daemon to close the stream")
	f.daemonClose("")
	_, closed := f.srv.ClosedWith(st.ConversationID)
	assert.True(t, closed, "the server no longer shows the session live")
}

// Failure prevented: recovering a held session (publishing switched to manual)
// closed its stream with a summary. A held stream's live copy is deleted by the
// daemon's stopHeld instead.
func TestLiveRecoverFromCacheLeavesAHeldStreamToBeDeleted(t *testing.T) {
	f := newLiveFixture(t, liveOpts{streaming: true})
	f.turn("shared while auto")
	st := f.openAndDrain()
	f.setPublishing("manual")

	f.recoverAfterCrash()

	require.True(t, session.IsHeld(f.dir()), "precondition: recovery held the session")
	assert.False(t, f.liveState().CloseRequested, "no close, and so no summary, for a held stream")
	live := daemon.NewLiveSessions(slog.New(slog.NewTextHandler(testWriter{t}, nil)), filepath.Dir(f.dir()), f.srv.URL)
	live.Tick(context.Background())
	live.Wait()
	assert.True(t, f.srv.IsDeleted(st.ConversationID), "the daemon deletes the held session's live copy")
	_, closed := f.srv.ClosedWith(st.ConversationID)
	assert.False(t, closed, "a held session is never closed with a summary")
}
