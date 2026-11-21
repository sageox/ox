package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/livesession"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/sessionid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Failure prevented: a person exits Claude Code, runs `claude --continue` five
// seconds later and prompts again while the daemon still writes the stopped
// session's summary (up to a minute) before its close. That stream was not
// closed yet, so the lookup refused it and the resumed recording became a
// conversation of its own for good. Here the daemon's summarizer is held back
// deliberately: the hook has requested the close, and the close runs only when
// the test lets it (daemonClose).

// resumeInto starts the recording that continues f's, in a folder of its own,
// through the same start path as a real resume (liveSessionBegin).
func (f *liveFixture) resumeInto(name string) *session.RecordingState {
	f.t.Helper()
	resumed := f.continuing(f.state.SessionID)
	resumed.SessionPath = filepath.Join(filepath.Dir(f.dir()), name)
	resumed.StartedAt = time.Now().UTC()
	require.NoError(f.t, os.MkdirAll(resumed.SessionPath, 0o755))
	header := []byte(`{"type":"header","metadata":{"session_id":"` + resumed.SessionID + `"}}` + "\n")
	require.NoError(f.t, os.WriteFile(filepath.Join(resumed.SessionPath, "raw.jsonl"), header, 0o600))
	liveSessionBegin(f.projectRoot, resumed, header)
	return resumed
}

// endedWhileSummarizing is the SessionEnd hook's part of the end: the close is
// requested, and the daemon starts the summary that holds it back.
func (f *liveFixture) endedWhileSummarizing() (first livesession.State, seed []byte) {
	f.t.Helper()
	f.turn("first task")
	first = f.openAndDrain()
	seed = f.spoolBytes()
	require.NoError(f.t, livesession.RequestClose(f.dir()))
	return first, seed
}

func liveStateOf(t *testing.T, dir string) livesession.State {
	t.Helper()
	st, err := livesession.ReadState(dir)
	require.NoError(t, err)
	return st
}

const resumedPrompt = `{"type":"user","content":"second task"}` + "\n"

func TestLiveQuickResumeWhileTheStoppedSessionSummarizesKeepsItsConversation(t *testing.T) {
	if testing.Short() {
		t.Skip("short: a hook-recorded session against the fake server")
	}
	f := newLiveFixture(t, liveOpts{streaming: true})
	first, seed := f.endedWhileSummarizing()

	resumed := f.resumeInto("resumed")
	waiting := liveStateOf(t, resumed.SessionPath)
	require.NotNil(t, waiting.AwaitReopen, "the resume waits for the close in flight")
	require.NotEqual(t, first.ConversationID, waiting.ConversationID)
	_, err := livesession.Spool(t.Context(), resumed.SessionPath, []byte(resumedPrompt), livesession.SpoolOptions{})
	require.NoError(t, err)
	cfg := liveConfig(f.projectRoot)
	_, err = livesession.Open(t.Context(), cfg, resumed.SessionPath, livesession.OpenParams{SessionID: resumed.SessionID})
	require.NoError(t, err)
	assert.Zero(t, f.srv.CallsTo("PUT", waiting.ConversationID), "nothing opens while it waits")

	f.daemonClose("# First\n\nthe first part")
	st, err := livesession.Open(t.Context(), cfg, resumed.SessionPath, livesession.OpenParams{SessionID: resumed.SessionID})
	require.NoError(t, err)
	require.Equal(t, first.ConversationID, st.ConversationID, "the resume reopens the stopped conversation")
	_, err = livesession.Drain(t.Context(), cfg, resumed.SessionPath, 5*time.Second)
	require.NoError(t, err)
	assert.Equal(t, string(seed)+resumedPrompt, string(f.srv.Stored(t, first.ConversationID, st.LayerID)),
		"the earlier recording first, then the resumed lines, each once")

	liveSessionFinish(f.projectRoot, resumed, func() string { return "# Both\n\nthe whole session" }, 5*time.Second)
	closed, ok := f.srv.ClosedWith(first.ConversationID)
	require.True(t, ok)
	assert.Equal(t, st.LayerID, closed.LayerID)
	assert.Equal(t, "# Both\n\nthe whole session", closed.Summary)
	assert.Zero(t, f.srv.CallsTo("PUT", waiting.ConversationID), "the resume's own conversation never reached SageOx")
}

func TestLiveStopOfAWaitingResumeResolvesBeforeItSummarizes(t *testing.T) {
	if testing.Short() {
		t.Skip("short: a hook-recorded session against the fake server")
	}
	t.Run("the stopped session closed meanwhile", func(t *testing.T) {
		f := newLiveFixture(t, liveOpts{streaming: true})
		first, seed := f.endedWhileSummarizing()
		resumed := f.resumeInto("resumed")
		_, err := livesession.Spool(t.Context(), resumed.SessionPath, []byte(resumedPrompt), livesession.SpoolOptions{})
		require.NoError(t, err)
		f.daemonClose("")

		summarizedOn := ""
		liveSessionFinish(f.projectRoot, resumed, func() string {
			summarizedOn = liveStateOf(t, resumed.SessionPath).ConversationID
			return "# Restated\n\nthe whole session"
		}, 5*time.Second)

		assert.Equal(t, first.ConversationID, summarizedOn, "the summary is written for the reopened stream")
		closed, ok := f.srv.ClosedWith(first.ConversationID)
		require.True(t, ok)
		assert.Equal(t, "# Restated\n\nthe whole session", closed.Summary)
		assert.Equal(t, string(seed)+resumedPrompt, string(f.srv.Stored(t, first.ConversationID, closed.LayerID)))
	})
	t.Run("the stopped session is still summarizing", func(t *testing.T) {
		f := newLiveFixture(t, liveOpts{streaming: true})
		first, _ := f.endedWhileSummarizing()
		resumed := f.resumeInto("resumed")
		own := liveStateOf(t, resumed.SessionPath).ConversationID
		_, err := livesession.Spool(t.Context(), resumed.SessionPath, []byte(resumedPrompt), livesession.SpoolOptions{})
		require.NoError(t, err)

		// the stop never waits on another session's summary
		liveSessionFinish(f.projectRoot, resumed, func() string { return "# Own\n\nthe resumed part" }, 5*time.Second)
		closed, ok := f.srv.ClosedWith(own)
		require.True(t, ok, "the resume closes as the conversation of its own it already is")
		assert.Equal(t, "# Own\n\nthe resumed part", closed.Summary)

		f.daemonClose("# First\n\nthe first part")
		firstClosed, _ := f.srv.ClosedWith(first.ConversationID)
		assert.Equal(t, first.LayerID, firstClosed.LayerID, "the stopped conversation keeps its one layer")
	})
}

// A recording restarted within the minute reuses the folder, so the closing
// stream is moved to its own folder first (retirePreviousLiveStream): the wait
// must name where it went.
func TestLiveQuickResumeInTheSameFolderWaitsForTheMovedStream(t *testing.T) {
	if testing.Short() {
		t.Skip("short: a hook-recorded session against the fake server")
	}
	f := newLiveFixture(t, liveOpts{streaming: true})
	first, seed := f.endedWhileSummarizing()

	next := *f.state
	next.SessionID = sessionid.GenerateSessionID()
	next.StartedAt = time.Now().UTC()
	next.ContinuedFromSessionID = f.state.SessionID
	require.NoError(t, writeRawHeader(f.projectRoot, &next))

	kept := filepath.Join(filepath.Dir(f.dir()), ".live-"+f.state.SessionID)
	waiting := f.liveState()
	require.Equal(t, next.SessionID, waiting.SessionID, "the new recording owns the folder")
	require.NotNil(t, waiting.AwaitReopen)
	resolvedKept, err := filepath.EvalSymlinks(kept)
	require.NoError(t, err)
	assert.Equal(t, resolvedKept, waiting.AwaitReopen.Dir, "the wait names the moved stream")

	res, err := livesession.Close(context.Background(), liveConfig(f.projectRoot), kept, "# First", 5*time.Second)
	require.NoError(t, err)
	require.True(t, res.Done)
	_, err = livesession.Spool(t.Context(), f.dir(), []byte(resumedPrompt), livesession.SpoolOptions{})
	require.NoError(t, err)
	cfg := liveConfig(f.projectRoot)
	st, err := livesession.Open(t.Context(), cfg, f.dir(), livesession.OpenParams{SessionID: next.SessionID})
	require.NoError(t, err)
	require.Equal(t, first.ConversationID, st.ConversationID)
	_, err = livesession.Drain(t.Context(), cfg, f.dir(), 5*time.Second)
	require.NoError(t, err)
	assert.Equal(t, string(seed)+resumedPrompt, string(f.srv.Stored(t, first.ConversationID, st.LayerID)))
}

// Failure prevented (ox #2002 review): a same-minute resume looked the
// previous stream up while its close was in flight (Pending), the daemon then
// finished the close and stashed it before the retire took the lock, and the
// retire removed the closed stream's files. The second lookup found that ready
// stash, but it names this same reused folder, so it was refused and the
// resume opened a new conversation instead of the stashed one.
func TestLiveSameFolderResumeTakesAStreamThatClosedBeforeTheRetire(t *testing.T) {
	if testing.Short() {
		t.Skip("short: a hook-recorded session against the fake server")
	}
	f := newLiveFixture(t, liveOpts{streaming: true})
	first, _ := f.endedWhileSummarizing()
	pending := &livesession.ReopenSource{Dir: f.dir(), State: first, Pending: true}

	// the daemon's close lands first, then the retire finds it closed
	res, err := livesession.Close(context.Background(), liveConfig(f.projectRoot), f.dir(), "# First", 5*time.Second)
	require.NoError(t, err)
	require.True(t, res.Done)
	require.NoError(t, retirePreviousLiveStream(f.projectRoot, f.dir(), f.state.SessionID))

	// named as the stash names it (resolved), as a hook in a real checkout does
	resolved, err := filepath.EvalSymlinks(f.dir())
	require.NoError(t, err)
	next := *f.state
	next.SessionID = sessionid.GenerateSessionID()
	next.ContinuedFromSessionID = f.state.SessionID
	next.SessionPath = resolved
	pending.Dir = resolved
	src := reopenSourceAfterRetire(f.projectRoot, &next, pending)
	require.NotNil(t, src, "the stashed conversation was refused for naming the reused folder")
	assert.False(t, src.Pending)
	assert.Equal(t, first.ConversationID, src.State.ConversationID)
}
