package main

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/agentinstance"
	"github.com/sageox/ox/internal/daemon"
	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/livesession"
	"github.com/sageox/ox/internal/paths"
	"github.com/sageox/ox/internal/runtime"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/sessionid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setPublishing switches the project's session_publishing mid-recording, as a
// coworker editing .sageox/config.json would.
func (f *liveFixture) setPublishing(mode string) {
	f.t.Helper()
	conf := fmt.Sprintf(`{"config_version":"2","repo_id":"repo_live_test","endpoint":%q,"session_publishing":%q}`, f.srv.URL, mode)
	require.NoError(f.t, os.WriteFile(filepath.Join(f.projectRoot, ".sageox", "config.json"), []byte(conf), 0o644))
}

// countFinalizeIPC stubs the hook's daemon finalize request and counts it.
func countFinalizeIPC(t *testing.T) *int {
	t.Helper()
	sent := new(int)
	orig := sendHookFinalizeIPC
	sendHookFinalizeIPC = func(daemon.SessionFinalizeIPCPayload) error { *sent++; return nil }
	t.Cleanup(func() { sendHookFinalizeIPC = orig })
	return sent
}

// Failure prevented (C1): a recording started in auto mode and switched to
// manual before SessionEnd was closed on SageOx, so the "not shared until I
// upload" copy stayed online. SessionEnd's daemon hand-off must delete it.
func TestLiveSessionEndOfAHeldSessionDeletesTheLiveCopy(t *testing.T) {
	f := newLiveFixture(t, liveOpts{streaming: true})
	f.turn("shared while auto")
	st := f.openAndDrain()
	f.setPublishing("manual")
	t.Setenv("SAGEOX_DAEMON", "true")
	t.Setenv("OX_PERSIST_DISK", "1")
	runtime.Reset()
	t.Cleanup(runtime.Reset)
	if !daemonOutlivesSession() {
		t.Skip("this environment runs SessionEnd's upload-before-exit path, covered by the stop tests")
	}
	sent := countFinalizeIPC(t)

	require.NoError(t, handleEnd(&HookContext{Phase: phaseEnd, AgentType: "claude-code", ProjectRoot: f.projectRoot, Marker: &SessionMarker{AgentID: f.agentID}}))

	assert.True(t, f.srv.IsDeleted(st.ConversationID), "manual publishing: the live copy must be deleted, not closed")
	_, closed := f.srv.ClosedWith(st.ConversationID)
	assert.False(t, closed, "a held session must never be closed (published) on SageOx")
	assert.Zero(t, *sent, "a held session is never handed to the daemon's finalize")
	assert.True(t, session.IsHeld(f.dir()))
	assert.Contains(t, string(f.rawBytes()), "shared while auto", "raw.jsonl stays: 'ox session upload' still publishes it")
}

// Failure prevented (C1): /clear and /compact stopped a recording without ever
// finishing its live stream: a held session stayed online, and an auto one was
// never closed.
func TestLiveClearFinishesTheLiveStream(t *testing.T) {
	t.Run("held: deleted", func(t *testing.T) {
		f := newLiveFixture(t, liveOpts{streaming: true})
		f.turn("shared while auto")
		st := f.openAndDrain()
		f.setPublishing("manual")
		countFinalizeIPC(t)

		stopSessionForClear(&HookContext{Phase: phaseStart, AgentType: "claude-code", ProjectRoot: f.projectRoot, Marker: &SessionMarker{AgentID: f.agentID}}, f.agentID)

		assert.True(t, f.srv.IsDeleted(st.ConversationID))
		_, closed := f.srv.ClosedWith(st.ConversationID)
		assert.False(t, closed)
	})
	t.Run("auto: closed", func(t *testing.T) {
		f := newLiveFixture(t, liveOpts{streaming: true})
		f.turn("a question")
		st := f.openAndDrain()
		sent := countFinalizeIPC(t)

		stopSessionForClear(&HookContext{Phase: phaseStart, AgentType: "claude-code", ProjectRoot: f.projectRoot, Marker: &SessionMarker{AgentID: f.agentID}}, f.agentID)

		assert.True(t, f.liveState().CloseRequested, "/clear ends the recording, so its stream is asked to close")
		f.daemonClose("")
		_, closed := f.srv.ClosedWith(st.ConversationID)
		assert.True(t, closed, "and the daemon's close reaches the server")
		assert.Equal(t, 1, *sent, "and the recording still goes to the daemon")
	})
}

// Failure prevented (C1): after publishing turned manual mid-recording, every
// hook still spooled the new turns and kicked the daemon to send them.
func TestLiveHookStopsSpoolingOnceThePublishingIsManual(t *testing.T) {
	f := newLiveFixture(t, liveOpts{streaming: true})
	f.turn("shared while auto")
	before := f.spoolBytes()
	f.setPublishing("manual")

	f.turn("PRIVATE-AFTER-SWITCH")

	assert.Contains(t, string(f.rawBytes()), "PRIVATE-AFTER-SWITCH", "raw.jsonl keeps recording locally")
	assert.Equal(t, string(before), string(f.spoolBytes()), "nothing captured after the switch may be spooled")
}

// Failure prevented (C1): the person was never told that stop deleted the live
// copy of a session that went manual while it streamed.
func TestLiveStopOfAHeldSessionSaysTheLiveCopyWasDeleted(t *testing.T) {
	f := newLiveFixture(t, liveOpts{streaming: true})
	f.turn("shared while auto")
	st := f.openAndDrain()
	f.setPublishing("manual")

	result := f.stopResult()

	assert.True(t, f.srv.IsDeleted(st.ConversationID))
	assert.True(t, result.Held)
	assert.Contains(t, result.UploadWarning, "live copy")
}

// Failure prevented (C2): when the delete could be neither confirmed nor
// recorded, abort still removed the session folder, so the streamed copy stayed
// on SageOx with nothing left on this machine to retry its delete.
func TestLiveAbortKeepsTheSessionWhenItsDeleteCannotBeRecorded(t *testing.T) {
	f := newLiveFixture(t, liveOpts{streaming: true})
	f.turn("to be discarded")
	st := f.openAndDrain()
	f.srv.SetDown(true)
	// the pending-deletes queue cannot be written
	queue := filepath.Join(paths.DataDir(), "live-pending-deletes.json")
	require.NoError(t, os.MkdirAll(queue, 0o700))
	setForceFlag(t, true)

	err := runAgentSessionAbort(&agentinstance.Instance{AgentID: f.agentID}, agentCmd, nil)

	require.Error(t, err, "abort must say the delete was not recorded")
	assert.DirExists(t, f.dir(), "the folder is the only record of what to delete")
	assert.FileExists(t, filepath.Join(f.dir(), "raw.jsonl"))
	got, rerr := livesession.ReadState(f.dir())
	require.NoError(t, rerr)
	assert.Equal(t, st.ConversationID, got.ConversationID)
	assert.False(t, f.srv.IsDeleted(st.ConversationID))

	// the recording was restored, so its live stream must go on with it
	assert.Equal(t, livesession.StatusStreaming, got.Status, "a failed abort must leave the restored session streaming")
	f.srv.SetDown(false)
	f.turn("AFTER-FAILED-ABORT")
	_, err = livesession.Drain(context.Background(), liveConfig(f.projectRoot), f.dir(), 5*time.Second)
	require.NoError(t, err)
	assert.Contains(t, string(f.srv.Stored(t, st.ConversationID, st.LayerID)), "AFTER-FAILED-ABORT")
}

// restartInSameFolder starts a new recording in f's folder, as StartRecording
// does when the same AI teammate restarts within the minute.
func (f *liveFixture) restartInSameFolder() *session.RecordingState {
	f.t.Helper()
	next := *f.state
	next.SessionID = sessionid.GenerateSessionID()
	next.StartedAt = time.Now().UTC()
	require.NoError(f.t, writeRawHeader(f.projectRoot, &next))
	return &next
}

// Failure prevented (C4): a recording that reused the previous recording's
// folder found that recording's stream state and kept it. A closed stream
// made every new batch get skipped; an open one took the new recording's
// turns under the old conversation.
func TestLiveRestartInTheSameFolderStartsItsOwnStream(t *testing.T) {
	t.Run("previous stream closed", func(t *testing.T) {
		f := newLiveFixture(t, liveOpts{streaming: true})
		f.turn("first recording")
		f.stop()
		old := f.liveState()
		require.Equal(t, livesession.StatusClosed, old.Status)

		next := f.restartInSameFolder()

		st := f.liveState()
		assert.Equal(t, next.SessionID, st.SessionID, "the new recording owns the folder's stream")
		assert.Equal(t, livesession.StatusPending, st.Status)
		assert.NotEqual(t, old.ConversationID, st.ConversationID)
		assert.NotContains(t, string(f.spoolBytes()), "first recording", "nothing of the old stream is reused")
	})
	t.Run("previous stream unfinished", func(t *testing.T) {
		f := newLiveFixture(t, liveOpts{streaming: true})
		f.turn("first recording")
		old := f.openAndDrain()
		f.turn("UNSENT-TAIL")

		next := f.restartInSameFolder()

		st := f.liveState()
		assert.Equal(t, next.SessionID, st.SessionID)
		assert.NotContains(t, string(f.spoolBytes()), "UNSENT-TAIL")
		kept := filepath.Join(filepath.Dir(f.dir()), ".live-"+old.SessionID)
		prev, err := livesession.ReadState(kept)
		require.NoError(t, err, "the unfinished stream is kept for the daemon to finish")
		assert.Equal(t, old.ConversationID, prev.ConversationID)
		assert.True(t, prev.CloseRequested, "its recording ended, so its close is requested")
		spool, err := os.ReadFile(filepath.Join(kept, "spool.jsonl"))
		require.NoError(t, err)
		assert.Contains(t, string(spool), "UNSENT-TAIL", "none of its bytes are lost")

		_, err = livesession.Close(context.Background(), liveConfig(f.projectRoot), kept, "", 5*time.Second)
		require.NoError(t, err)
		assert.Contains(t, string(f.srv.Stored(t, old.ConversationID, prev.LayerID)), "UNSENT-TAIL")
	})
}

// Failure prevented (C3): someone deleted the session on SageOx after its
// spool was fully sent, then ran /clear. A caught-up stream makes no request
// that would hear of the delete, so /clear handed the session to the daemon,
// which published it to the Ledger.
func TestLiveClearNeverHandsOffASessionDeletedOnSageOx(t *testing.T) {
	f := newLiveFixture(t, liveOpts{streaming: true})
	f.turn("before the delete")
	st := f.openAndDrain()
	f.srv.DeleteSession(st.ConversationID)
	sent := countFinalizeIPC(t)

	stopSessionForClear(&HookContext{Phase: phaseStart, AgentType: "claude-code", ProjectRoot: f.projectRoot, Marker: &SessionMarker{AgentID: f.agentID}}, f.agentID)

	assert.Zero(t, *sent, "the deleted session must not reach the daemon's finalize")
	hold, err := session.ReadHoldMarker(f.dir())
	require.NoError(t, err)
	assert.Equal(t, session.HoldDeletedOnServer, hold.Reason)
}

// Failure prevented (C3): the stop's upload read "deleted on SageOx" once,
// early, and never again before publishing; a delete learned in between went
// to the Ledger. The publication boundary must re-read it before any write.
func TestLiveUploadRefusesASessionDeletedOnSageOx(t *testing.T) {
	f := newLiveFixture(t, liveOpts{streaming: true})
	f.turn("before the delete")
	st := f.openAndDrain()
	f.srv.DeleteSession(st.ConversationID)
	f.turn("after the delete")
	_, err := livesession.Drain(context.Background(), liveConfig(f.projectRoot), f.dir(), 5*time.Second)
	require.NoError(t, err)
	require.True(t, liveDeletedOnServer(f.dir()), "precondition: the stream heard of the delete")
	ledger := filepath.Join(t.TempDir(), "ledger")
	uploaded := false
	effects := sessionUploadEffects{uploadLFS: func(string, string) (map[string]lfs.FileRef, error) { uploaded = true; return nil, nil }}

	err = uploadSessionToLedgerWithEffects(f.projectRoot, &agentSessionResult{RawPath: filepath.Join(f.dir(), "raw.jsonl"), EntryCount: 2}, f.state, ledger, "test", effects)

	require.Error(t, err)
	assert.False(t, uploaded, "nothing may leave the machine")
	assert.NoDirExists(t, filepath.Join(ledger, "sessions", "test"), "nothing may be written into the Ledger")
}

// Failure prevented (C2): a delete SageOx refused for good (a permission
// refusal) was reported as deleted and dropped, so the person never learned
// the streamed copy was still there.
func TestLiveAbortTellsThePersonWhenSageOxRefusesTheDelete(t *testing.T) {
	f := newLiveFixture(t, liveOpts{streaming: true})
	f.turn("to be discarded")
	st := f.openAndDrain()
	f.srv.RefuseDelete(true)
	setForceFlag(t, true)
	var out bytes.Buffer
	agentCmd.SetOut(&out)
	t.Cleanup(func() { agentCmd.SetOut(nil) })

	require.NoError(t, runAgentSessionAbort(&agentinstance.Instance{AgentID: f.agentID}, agentCmd, nil))

	assert.False(t, f.srv.IsDeleted(st.ConversationID))
	assert.Contains(t, out.String(), "delete it on SageOx", "the person must hear the live copy is still on SageOx")
	assert.NoDirExists(t, f.dir(), "the refusal is recorded outside the folder, so local cleanup proceeds")
}

// Failure prevented (C4 and C1): a hold is a statement about one recording,
// but it was read per folder. StartRecording refuses a held folder, yet the
// previous recording's stream can still hear SageOx's delete after that check
// and before the new recording's start retires it, and write its
// deleted_on_server hold into the shared folder. The daemon then read the new
// recording as held and deleted the new stream on SageOx.
func TestLiveRestartAfterTheOldStreamHeardADeleteKeepsTheNewStream(t *testing.T) {
	f := newLiveFixture(t, liveOpts{streaming: true})
	f.turn("first recording")
	old := f.openAndDrain()
	f.srv.DeleteSession(old.ConversationID)
	f.turn("after the delete")
	_, err := livesession.Drain(context.Background(), liveConfig(f.projectRoot), f.dir(), 5*time.Second)
	require.NoError(t, err)
	hold, err := session.ReadHoldMarker(f.dir())
	require.NoError(t, err, "precondition: the old stream held the folder")
	require.Equal(t, session.HoldDeletedOnServer, hold.Reason)

	next := f.restartInSameFolder()
	// the new recording's first prompt, as its capture spools it: a stream
	// opens only once it holds one
	_, err = livesession.Spool(context.Background(), f.dir(), []byte(`{"type":"user","content":"second recording"}`+"\n"), livesession.SpoolOptions{})
	require.NoError(t, err)
	live := daemon.NewLiveSessions(slog.New(slog.NewTextHandler(testWriter{t}, nil)), filepath.Dir(f.dir()), f.srv.URL)
	live.Drain(context.Background(), f.dir())
	live.Wait()

	st := f.liveState()
	require.Equal(t, next.SessionID, st.SessionID)
	assert.Equal(t, livesession.StatusStreaming, st.Status, "the daemon must stream the new recording, not delete it")
	assert.False(t, f.srv.IsDeleted(st.ConversationID))
	assert.False(t, session.IsHeld(f.dir()), "the previous recording's hold must not hold the new one")
}
