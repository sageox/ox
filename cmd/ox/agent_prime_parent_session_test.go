package main

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/sageox/ox/internal/ledger"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/sessionid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The decision prime makes about an inherited SAGEOX_AGENT_ID. Only a new
// native session in a different live agent process is a launched child; every
// case that cannot tell the processes apart keeps reusing the identity, which
// /clear and the CLAUDE.md repeat prime depend on.
func TestLaunchedByAnotherAgent(t *testing.T) {
	projectRoot, repoID := setupTestProject(t)
	createActiveRecordingWithPID(t, projectRoot, repoID, "OxParent", os.Getppid())
	createDeadRecording(t, projectRoot, repoID, "OxGone")
	states, err := session.LoadAllRecordingStates(projectRoot)
	require.NoError(t, err)
	parentPID, childPID := os.Getppid(), os.Getpid()

	cases := []struct {
		name            string
		envID, nativeID string
		agentPID        int
		want            bool
	}{
		{"a new native session in another agent process is a child", "OxParent", "child-session", childPID, true},
		{"the same agent process after /clear keeps its identity", "OxParent", "cleared-session", parentPID, false},
		{"a repeat prime with no native session id keeps its identity", "OxParent", "", childPID, false},
		{"an unknown agent process keeps its identity", "OxParent", "child-session", 0, false},
		{"a stale variable from an ended session names nobody", "OxGone", "child-session", childPID, false},
		{"no inherited variable", "", "child-session", childPID, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, launchedByAnotherAgent(states, tc.envID, tc.nativeID, tc.agentPID))
		})
	}
}

// Failure prevented: the hook's safety-net recording start passed no parent,
// so a child recording it started had no link to send.
func TestHookRecordingStartRecordsTheInheritedParent(t *testing.T) {
	projectRoot, repoID := setupTestProject(t)
	enableRecordingWithoutLedgerClone(t)
	createActiveRecordingWithPID(t, projectRoot, repoID, "OxParent", os.Getppid())
	parentID := sessionid.GenerateSessionID()
	require.NoError(t, session.UpdateRecordingStateForAgent(projectRoot, "OxParent", func(s *session.RecordingState) { s.SessionID = parentID }))
	t.Setenv("SAGEOX_AGENT_ID", "OxParent")
	t.Setenv("OX_PARENT_PID", strconv.Itoa(os.Getpid()))

	startSessionRecordingIfConfigured(&HookContext{
		ProjectRoot: projectRoot, AgentType: "claude-code",
		Marker: &SessionMarker{AgentID: "OxChild", AgentSessionID: "child-session"},
	})

	child, err := session.LoadRecordingStateForAgent(projectRoot, "OxChild")
	require.NoError(t, err)
	require.NotNil(t, child, "harness: the hook must start the child's recording")
	assert.Equal(t, "OxParent", child.ParentAgentID)
	assert.Equal(t, parentID, child.ParentSessionID, "the parent's session id must be captured when the parent is chosen")
}

// enableRecordingWithoutLedgerClone turns auto-recording on, with a Ledger
// directory that counts as cloned, so a recording starts with no network.
func enableRecordingWithoutLedgerClone(t *testing.T) {
	t.Helper()
	t.Setenv("SAGEOX_DAEMON", "false")
	t.Setenv("OX_SESSION_RECORDING", "auto")
	ledgerPath, err := ledger.DefaultPath()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(ledgerPath, ".git"), 0o700))
}

// Failure prevented: a child launched while its parent was paused skipped
// recording once, but its id was already in its env file, so the next prime
// found no parent and started an unsuspended recording, eligible for sharing,
// without the parent ever being resumed.
func TestChildOfAPausedParentStaysPausedWhenPrimedAgain(t *testing.T) {
	projectRoot, repoID := setupTestProject(t)
	enableRecordingWithoutLedgerClone(t)
	createActiveRecordingWithPID(t, projectRoot, repoID, "OxParent", os.Getppid())
	require.NoError(t, session.MarkExplicitPause(projectRoot, "OxParent", 1))

	first := startSessionRecording(projectRoot, "OxChild", "claude-code", "OxParent", "", "child-session")
	require.NotNil(t, first)
	require.False(t, first.Recording, "harness: a paused parent's child skips recording")

	// the re-prime: the child's own id is in the environment now, so no parent
	again := startSessionRecording(projectRoot, "OxChild", "claude-code", "", "", "child-session")
	require.NotNil(t, again)
	require.True(t, again.Recording, "harness: the re-prime starts the child's recording")
	child, err := session.LoadRecordingStateForAgent(projectRoot, "OxChild")
	require.NoError(t, err)
	require.NotNil(t, child)
	assert.True(t, child.IsSuspended(), "a re-primed child of a paused parent recorded unsuspended")
}

// The ids a recording's stream opens with. Failure prevented: the parent was
// looked up again when the stream was prepared, after it had ended or /clear
// had replaced its recording, so the child sent no parent or the wrong one.
func TestLiveSessionLinks(t *testing.T) {
	projectRoot, repoID := setupTestProject(t)
	parentID, replacedID, earlierID, ownID := sessionid.GenerateSessionID(), sessionid.GenerateSessionID(), sessionid.GenerateSessionID(), sessionid.GenerateSessionID()
	// the parent agent's recording after a /clear: a new ses_ id
	require.NoError(t, session.SaveRecordingState(projectRoot, &session.RecordingState{
		AgentID: "OxParent", SessionID: replacedID, AdapterName: "claude-code", StartedAt: time.Now(), ParentPID: os.Getpid(),
		SessionPath: filepath.Join(session.GetContextPath(repoID), "sessions", "2026-04-01T10-00-user-OxParent"),
	}))
	child := func(parentAgent, parentSession, continued string) *session.RecordingState {
		return &session.RecordingState{AgentID: "OxChild", SessionID: ownID, WorkspacePath: projectRoot,
			ParentAgentID: parentAgent, ParentSessionID: parentSession, ContinuedFromSessionID: continued}
	}

	t.Run("a child sends the parent session captured when it was chosen", func(t *testing.T) {
		parent, continued := liveSessionLinks(child("OxParent", parentID, earlierID))
		assert.Equal(t, parentID, parent, "the parent's replacement recording was sent instead")
		assert.Equal(t, earlierID, continued)
	})
	t.Run("a parent that has since ended is still named", func(t *testing.T) {
		parent, _ := liveSessionLinks(child("OxEnded", parentID, ""))
		assert.Equal(t, parentID, parent)
	})
	t.Run("ids that are not ses_ ids, or name the recording itself, are left out", func(t *testing.T) {
		parent, continued := liveSessionLinks(child("OxParent", ownID, "2026-03-04T13-41-person-a-Oxabc1"))
		assert.Empty(t, parent, "a recording is never its own parent")
		assert.Empty(t, continued)
	})
}
