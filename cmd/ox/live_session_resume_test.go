package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/agentinstance"
	"github.com/sageox/ox/internal/livesession"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/sessionid"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// agentEnds is the SessionEnd hook for the fixture's recording, then what the
// daemon does with the close it requested.
func (f *liveFixture) agentEnds(summary string) {
	f.t.Helper()
	countFinalizeIPC(f.t)
	require.NoError(f.t, finalizeOrHoldOnHookStop(&HookContext{ProjectRoot: f.projectRoot}, f.state, "end"))
	f.daemonClose(summary)
}

func (f *liveFixture) continuing(from string) *session.RecordingState {
	return &session.RecordingState{SessionID: sessionid.GenerateSessionID(), SessionPath: f.dir(), ContinuedFromSessionID: from}
}

func TestLiveAgentExitThenResumeOfTheSameConversationReopens(t *testing.T) {
	if testing.Short() {
		t.Skip("short: a full hook-recorded stop against the fake server")
	}
	f := newLiveFixture(t, liveOpts{streaming: true})
	f.turn("first task")
	st := f.openAndDrain()
	f.agentEnds("# Done\n\nsummary written at the end")
	closed, ok := f.srv.ClosedWith(st.ConversationID)
	require.True(t, ok, "precondition: the daemon's close reached the server")
	assert.Equal(t, "# Done\n\nsummary written at the end", closed.Summary, "every close carries a summary")
	require.NoError(t, os.RemoveAll(f.dir())) // the push-summary prune

	// `claude --continue`: SessionStart(resume) primes the same agent session id,
	// whose marker names the recording that ended
	resumed := f.continuing(f.state.SessionID)
	resumed.SessionPath = filepath.Join(filepath.Dir(f.dir()), "resumed")
	require.NoError(t, os.MkdirAll(resumed.SessionPath, 0o755))
	src := liveReopenSource(f.projectRoot, resumed)
	require.NotNil(t, src, "the next recording of the same agent session must resume")
	_, err := livesession.PrepareReopen(t.Context(), liveConfig(f.projectRoot), resumed.SessionPath, livesession.OpenParams{SessionID: resumed.SessionID}, src)
	require.NoError(t, err)
	_, err = livesession.Spool(t.Context(), resumed.SessionPath, []byte(`{"type":"user","content":"second task"}`+"\n"), livesession.SpoolOptions{})
	require.NoError(t, err)
	_, err = livesession.Open(t.Context(), liveConfig(f.projectRoot), resumed.SessionPath, livesession.OpenParams{SessionID: resumed.SessionID})
	require.NoError(t, err)

	opened, ok := f.srv.OpenedWith(st.ConversationID)
	require.True(t, ok)
	assert.True(t, opened.Reopen, "the resume is an explicit reopen")
}

func TestLiveAnotherAgentSessionIDNeverResumes(t *testing.T) {
	if testing.Short() {
		t.Skip("short: a full hook-recorded stop against the fake server")
	}
	f := newLiveFixture(t, liveOpts{streaming: true})
	f.turn("first task")
	f.openAndDrain()
	f.agentEnds("")
	// the prime marker is keyed by the agent's own session id
	require.NoError(t, WriteSessionMarker(&SessionMarker{AgentID: f.agentID, AgentSessionID: "claude-conversation-a", RecordingSessionID: f.state.SessionID, PrimedAt: time.Now()}))

	same, err := ReadSessionMarker("claude-conversation-a")
	require.NoError(t, err)
	require.NotNil(t, same)
	assert.NotNil(t, liveReopenSource(f.projectRoot, f.continuing(recordingSessionIDFromMarker(same))), "the same agent session resumes")

	// /clear (or any other conversation) is a new agent session id: no marker, no continuation
	other, err := ReadSessionMarker("claude-conversation-b")
	require.NoError(t, err)
	assert.Nil(t, liveReopenSource(f.projectRoot, f.continuing(recordingSessionIDFromMarker(other))), "a different agent session id starts a new conversation")
}

func TestLiveManualStopIsNeverReopened(t *testing.T) {
	if testing.Short() {
		t.Skip("short: a full hook-recorded stop against the fake server")
	}
	f := newLiveFixture(t, liveOpts{streaming: true})
	f.turn("first task")
	f.openAndDrain()

	markManualLiveStop(f.agentID) // what `ox agent … session stop` does before it closes
	f.stop()
	require.Equal(t, livesession.StatusClosed, f.liveState().Status, "precondition: the stop closed the stream")

	assert.Nil(t, liveReopenSource(f.projectRoot, f.continuing(f.state.SessionID)), "the next prompt must not resume a manual stop")
	require.NoError(t, os.RemoveAll(f.dir()))
	assert.Nil(t, liveReopenSource(f.projectRoot, f.continuing(f.state.SessionID)), "nor from the stash after cache pruning")
}

func TestLiveStartAfterAnyCloseIsANewConversation(t *testing.T) {
	if testing.Short() {
		t.Skip("short: a full hook-recorded stop against the fake server")
	}
	f := newLiveFixture(t, liveOpts{streaming: true})
	f.turn("first task")
	old := f.openAndDrain()
	f.agentEnds("")
	require.NoError(t, session.ClearRecordingStateForAgent(f.projectRoot, f.agentID))

	// `/ox-session-start` records with no continuation: it never names an
	// earlier recording, so the writer prepares an ordinary stream
	next, err := session.StartRecording(f.projectRoot, session.StartRecordingOptions{AgentID: f.agentID, AdapterName: "claude-code", Username: "person-b"})
	require.NoError(t, err)
	require.Empty(t, next.ContinuedFromSessionID)
	require.NoError(t, writeRawHeader(f.projectRoot, next))

	st, err := livesession.ReadState(next.SessionPath)
	require.NoError(t, err)
	assert.NotEqual(t, old.ConversationID, st.ConversationID)
	assert.False(t, st.Reopen)
	assert.Nil(t, liveReopenSource(f.projectRoot, next))
}

func TestLiveHookEndLeavesTheCloseToTheDaemon(t *testing.T) {
	if testing.Short() {
		t.Skip("short: a full hook-recorded stop against the fake server")
	}
	f := newLiveFixture(t, liveOpts{streaming: true})
	f.turn("first task")
	st := f.openAndDrain()
	countFinalizeIPC(t)

	require.NoError(t, finalizeOrHoldOnHookStop(&HookContext{ProjectRoot: f.projectRoot}, f.state, "end"))

	_, closed := f.srv.ClosedWith(st.ConversationID)
	assert.False(t, closed, "the hook has no time for a summary, so it must not send the (final) close itself")
	assert.True(t, f.liveState().CloseRequested)
	_ = context.Background()
}

// The command, not just its helper, marks a person's stop; the SessionEnd
// hook's own stop (--agent-ended) is an automatic end and stays resumable.
func TestLiveStopCommandMarksOnlyAPersonsStop(t *testing.T) {
	if testing.Short() {
		t.Skip("short: a full hook-recorded stop against the fake server")
	}
	for _, tc := range []struct {
		name       string
		agentEnded bool
		wantMarked bool
	}{
		{"person runs session stop", false, true},
		{"SessionEnd hook runs session stop --agent-ended", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newLiveFixture(t, liveOpts{streaming: true})
			f.turn("first task")
			f.openAndDrain()
			cmd := &cobra.Command{}
			cmd.Flags().Bool("agent-ended", tc.agentEnded, "")

			_ = runAgentSessionStopCommand(cmd, &agentinstance.Instance{AgentID: f.agentID, AgentType: "claude-code"})

			st, err := livesession.ReadState(f.dir())
			require.NoError(t, err)
			assert.Equal(t, tc.wantMarked, st.ManualStop)
		})
	}
}
