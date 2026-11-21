package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/agentx"
	"github.com/sageox/ox/internal/agentinstance"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/livesession"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/sessionid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Failure prevented: a recording replaced without its stop (or whose stop
// never closed the stream) stayed "Live" on SageOx for good, so one AI
// coworker piled up open streams across /clear boundaries.
func TestLiveNewRecordingByTheSameAICoworkerClosesItsPreviousStream(t *testing.T) {
	if testing.Short() {
		t.Skip("short: three recordings and a close against the fake server")
	}
	f := newLiveFixture(t, liveOpts{streaming: true})
	f.turn("a question")
	old := f.openAndDrain()
	oldDir := f.dir()
	// the recording ends with its stream still open
	require.NoError(t, session.ClearRecordingStateForAgent(f.projectRoot, f.agentID))

	// another AI coworker's open stream in the same project; the usernames
	// only put each recording in its own folder, as a later minute would
	other, err := session.StartRecording(f.projectRoot, session.StartRecordingOptions{AgentID: "OxOther", AdapterName: "claude-code", Username: "person-c"})
	require.NoError(t, err)
	require.NoError(t, writeRawHeader(f.projectRoot, other))
	next, err := session.StartRecording(f.projectRoot, session.StartRecordingOptions{AgentID: f.agentID, AdapterName: "claude-code", Username: "person-b"})
	require.NoError(t, err)
	require.NotEqual(t, oldDir, next.SessionPath, "precondition: the new recording has its own folder")

	require.NoError(t, writeRawHeader(f.projectRoot, next))

	prev, err := livesession.ReadState(oldDir)
	require.NoError(t, err)
	assert.True(t, prev.CloseRequested, "the same AI coworker's previous stream must be closed")
	for name, dir := range map[string]string{"another AI coworker's stream": other.SessionPath, "the new recording's own stream": next.SessionPath} {
		st, err := livesession.ReadState(dir)
		require.NoError(t, err)
		assert.Equal(t, livesession.StatusPending, st.Status, name)
		assert.False(t, st.CloseRequested, "%s must be left alone", name)
	}

	// what the daemon's tick does with a close request
	res, err := livesession.Close(context.Background(), liveConfig(f.projectRoot), oldDir, "", 5*time.Second)
	require.NoError(t, err)
	require.Equal(t, livesession.StatusClosed, res.Status)
	closed, ok := f.srv.ClosedWith(old.ConversationID)
	require.True(t, ok, "the server never received the close")
	assert.Empty(t, closed.Summary, "the normal close, with no summary")
}

// Owner ruling F8: "/compact continues the same live session; /clear starts a
// new session linked to the old one." Failure prevented: /compact stopped the
// recording and closed its stream, splitting one conversation in two.
func TestLiveCompactContinuesTheSessionAndClearEndsIt(t *testing.T) {
	if testing.Short() {
		t.Skip("short: hook-recorded turns and a close against the fake server")
	}
	f := newLiveFixture(t, liveOpts{streaming: true})
	f.turn("before compacting")
	st := f.openAndDrain()
	countFinalizeIPC(t)
	start := func(source string) {
		t.Helper()
		require.NoError(t, handleStart(&HookContext{Phase: phaseStart, AgentType: "claude-code", ProjectRoot: f.projectRoot,
			Input: &agentx.HookInput{Source: source}, Marker: &SessionMarker{AgentID: f.agentID}}))
	}

	start("compact")

	rec, err := session.LoadRecordingStateForAgent(f.projectRoot, f.agentID)
	require.NoError(t, err)
	require.NotNil(t, rec, "/compact must not end the recording")
	assert.Equal(t, f.state.SessionID, rec.SessionID, "/compact must not start a new recording")
	assert.Nil(t, rec.StoppedAt)
	live := f.liveState()
	assert.Equal(t, livesession.StatusStreaming, live.Status)
	assert.False(t, live.CloseRequested, "/compact must not close the live stream")
	_, closed := f.srv.ClosedWith(st.ConversationID)
	require.False(t, closed)

	start("clear")

	assert.True(t, f.liveState().CloseRequested, "/clear ends the session, so its stream is asked to close")
	f.daemonClose("")
	_, closed = f.srv.ClosedWith(st.ConversationID)
	assert.True(t, closed, "and the daemon's close reaches the server")
}

// Failure prevented: the start collected the owed notices before its header
// write retired the reused folder's stream to .live-<id>, so it cleared the
// mark where it no longer was and the notice was said again at every start.
func TestLiveStartInAReusedFolderClearsTheNoticeWhereItMoved(t *testing.T) {
	if testing.Short() {
		t.Skip("short: a full hook-recorded stop against the fake server")
	}
	f := newLiveFixture(t, liveOpts{streaming: true})
	f.turn("before the secret")
	f.openAndDrain()
	markStreamWithheld(t, f.dir())
	countFinalizeIPC(t)
	require.NoError(t, finalizeOrHoldOnHookStop(&HookContext{ProjectRoot: f.projectRoot}, f.state, "clear"))
	require.Equal(t, []string{f.dir()}, f.owedLater(), "precondition: the hook stop left the notice owed")
	// the next recording lands in the same folder (same minute)
	next := &session.RecordingState{SessionID: sessionid.GenerateSessionID(), SessionPath: f.dir(), AgentID: f.agentID,
		AdapterName: "claude-code", StartedAt: time.Now()}

	said := writeStartHeaderAndCollectOwedNotices(f.projectRoot, next, "claude-code")
	confirmLiveSecretWithheldNotices(said)

	require.Len(t, said, 1, "the start must say the notice")
	assert.Equal(t, ".live-"+f.state.SessionID, filepath.Base(said[0]))
	assert.Empty(t, f.owedLater(), "said once: the mark must be cleared where it moved")
}

// closedStdout points os.Stdout at a closed file for the test, so every write
// to it fails.
func closedStdout(t *testing.T) {
	t.Helper()
	r, w, err := os.Pipe()
	require.NoError(t, err)
	require.NoError(t, r.Close())
	require.NoError(t, w.Close())
	orig := os.Stdout
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = orig })
}

// Failure prevented: the text output ignored a failed write to stdout, so the
// owed mark was cleared for a notice nobody saw.
func TestLiveStopTextThatCannotBeWrittenKeepsTheNoticeOwed(t *testing.T) {
	if testing.Short() {
		t.Skip("short: a recording fixture against the fake server")
	}
	f := newLiveFixture(t, liveOpts{})
	require.NoError(t, livesession.OweSecretWithheldNotice(f.dir()))
	result := &agentSessionResult{LiveSecretWithheld: true, LiveNoticeDirs: []string{f.dir()}}
	cfg = &config.Config{Text: true}
	inst := &agentinstance.Instance{AgentID: f.agentID}

	closedStdout(t)
	err := emitSessionStop(f.projectRoot, inst, f.state, "1s", result, nil, map[string]int64{})

	require.Error(t, err, "a stop whose output could not be written must say so")
	assert.True(t, livesession.OwesSecretWithheldNotice(f.dir()), "the notice was not seen, so it stays owed")
}

// The start's text output is one write whose error decides whether the start
// clears the owed marks of the notices in it.
func TestSessionStartTextReportsAFailedWrite(t *testing.T) {
	err := printSessionStartText(failingWriter{}, "OxLive1", "claude-code", "", liveSecretWithheldEarlierWarning, time.Now(), false)
	assert.Error(t, err)
}

// Failure prevented (PR 1971 review): a recording that publishes manually
// returned before the superseded-stream cleanup, so switching an AI coworker
// to manual publishing left its previous automatic stream open.
func TestLiveManualRecordingStillClosesThePreviousStream(t *testing.T) {
	if testing.Short() {
		t.Skip("short: a hook-recorded turn against the fake server")
	}
	f := newLiveFixture(t, liveOpts{streaming: true})
	f.turn("a question")
	f.openAndDrain()
	next := &session.RecordingState{SessionID: sessionid.GenerateSessionID(), AgentID: f.agentID, AdapterName: "claude-code",
		SessionPath: filepath.Join(filepath.Dir(f.dir()), "2099-01-01T00-00-person-a-"+f.agentID), StartedAt: time.Now().Add(time.Minute),
		PublishingMode: config.SessionPublishingManual}

	liveSessionBegin(f.projectRoot, next, nil)

	assert.True(t, f.liveState().CloseRequested, "the AI coworker's previous automatic stream must be closed")
	_, err := livesession.ReadState(next.SessionPath)
	assert.ErrorIs(t, err, livesession.ErrNotStreaming, "a manual recording never streams itself")
}
