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

	first := startSessionRecording(projectRoot, "OxChild", "claude-code", inheritedParent{AgentID: "OxParent"}, "", "child-session")
	require.NotNil(t, first)
	require.False(t, first.Recording, "harness: a paused parent's child skips recording")

	// the re-prime: the child's own id is in the environment now, so no parent
	again := startSessionRecording(projectRoot, "OxChild", "claude-code", inheritedParent{}, "", "child-session")
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

// Failure prevented: recording startup read the parent's recording again
// after the parent was chosen, so a parent that /clear replaced in between gave
// the child the replacement's session id.
func TestChildKeepsTheParentSessionChosenWhenTheParentIsReplaced(t *testing.T) {
	projectRoot, repoID := setupTestProject(t)
	enableRecordingWithoutLedgerClone(t)
	createActiveRecordingWithPID(t, projectRoot, repoID, "OxParent", os.Getppid())
	chosenID, replacementID := sessionid.GenerateSessionID(), sessionid.GenerateSessionID()
	require.NoError(t, session.UpdateRecordingStateForAgent(projectRoot, "OxParent", func(s *session.RecordingState) { s.SessionID = chosenID }))
	t.Setenv("SAGEOX_AGENT_ID", "OxParent")
	t.Setenv("OX_PARENT_PID", strconv.Itoa(os.Getpid()))

	parent := inheritedParentOf(projectRoot, "OxChild")
	require.Equal(t, chosenID, parent.SessionID, "harness: the parent is chosen with its session id")
	// /clear replaces the parent's recording before the child's recording starts
	require.NoError(t, session.UpdateRecordingStateForAgent(projectRoot, "OxParent", func(s *session.RecordingState) { s.SessionID = replacementID }))
	status := startSessionRecording(projectRoot, "OxChild", "claude-code", parent, "", "child-session")

	require.NotNil(t, status)
	require.True(t, status.Recording, "harness: the child records")
	child, err := session.LoadRecordingStateForAgent(projectRoot, "OxChild")
	require.NoError(t, err)
	require.NotNil(t, child)
	assert.Equal(t, chosenID, child.ParentSessionID, "the child named the parent's replacement recording")
}

// Failure prevented: a paused parent's skipped child kept its pause in a
// marker but had no recording, so an agent that child launched in turn found
// no parent, got a fresh id and recorded unsuspended, eligible for sharing. A
// Cursor child cannot name its own agent process (Cursor's GUI process is
// shared by every conversation), and its marker named none, so the pause
// stopped there.
func TestAPauseReachesEveryAgentDownTheLaunchChain(t *testing.T) {
	for _, childAgent := range []string{"claude-code", "cursor"} {
		t.Run(childAgent+" child", func(t *testing.T) {
			projectRoot, repoID := setupTestProject(t)
			enableRecordingWithoutLedgerClone(t)
			createActiveRecordingWithPID(t, projectRoot, repoID, "OxParent", os.Getppid())
			require.NoError(t, session.MarkExplicitPause(projectRoot, "OxParent", 1))

			// the child, launched from the paused parent, runs as this process
			t.Setenv("SAGEOX_AGENT_ID", "OxParent")
			t.Setenv("OX_PARENT_PID", strconv.Itoa(os.Getpid()))
			child := startSessionRecordingUnlocked(projectRoot, "OxChild", childAgent, inheritedParentOf(projectRoot, "OxChild"), "", "child-session", nil)
			require.NotNil(t, child)
			require.False(t, child.Recording, "harness: a paused parent's child skips recording")

			// the grandchild, launched from the child's shell, is another agent process
			t.Setenv("SAGEOX_AGENT_ID", "OxChild")
			grandchildPID := os.Getppid()
			if childAgent == "cursor" {
				// the pause a Cursor child holds is its launcher's; the grandchild must be another process
				grandchildPID = os.Getpid()
			}
			t.Setenv("OX_PARENT_PID", strconv.Itoa(grandchildPID))
			grandchild := startSessionRecording(projectRoot, "OxGrandchild", "claude-code", inheritedParentOf(projectRoot, "OxGrandchild"), "", "grandchild-session")

			require.NotNil(t, grandchild)
			assert.False(t, grandchild.Recording, "the grandchild of a paused parent recorded")
			state, err := session.LoadRecordingStateForAgent(projectRoot, "OxGrandchild")
			require.NoError(t, err)
			assert.Nil(t, state)
			assert.True(t, session.InheritedPauseHeld(projectRoot, "OxGrandchild", 0), "the grandchild must hold the pause for agents it launches")
		})
	}
}

// Failure prevented: the over-correction. A pause left by a skipped child
// whose agent process has exited must not pause an unrelated later agent that
// inherited the same stale SAGEOX_AGENT_ID.
func TestAnEndedLaunchersPauseReachesNobody(t *testing.T) {
	projectRoot, _ := setupTestProject(t)
	enableRecordingWithoutLedgerClone(t)
	require.NoError(t, session.MarkInheritedPause(projectRoot, "OxEnded", session.PauseAnchor{PID: 999999999, RootAgentID: "OxEndedRoot"}))
	t.Setenv("SAGEOX_AGENT_ID", "OxEnded")
	t.Setenv("OX_PARENT_PID", strconv.Itoa(os.Getpid()))

	status := startSessionRecording(projectRoot, "OxLater", "claude-code", inheritedParentOf(projectRoot, "OxLater"), "", "later-session")

	require.NotNil(t, status)
	require.True(t, status.Recording, "a dead launcher's pause stopped an unrelated agent from recording")
	later, err := session.LoadRecordingStateForAgent(projectRoot, "OxLater")
	require.NoError(t, err)
	require.NotNil(t, later)
	assert.False(t, later.IsSuspended())
}

// Failure prevented: a Cursor recording stores no agent process (ParentPID 0),
// so a paused Cursor parent passed its skipped Cursor child no process to hold
// the pause, and an agent that child launched recorded while the person's
// pause was still in effect. And the reverse: a chain anchored on a running
// parent process stayed paused after the person resumed the root. A pause
// that names its root holds exactly while the root's person-set pause does.
func TestAPausedParentsPauseHoldsDownTheChainUntilUnpaused(t *testing.T) {
	cases := []struct {
		name       string
		parentPID  int
		childAgent string
	}{
		// a Cursor recording names no agent process
		{"cursor parent and child", 0, "cursor"},
		{"claude-code parent and child", os.Getppid(), "claude-code"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			projectRoot, repoID := setupTestProject(t)
			enableRecordingWithoutLedgerClone(t)
			createActiveRecordingWithPID(t, projectRoot, repoID, "OxParent", tc.parentPID)
			require.NoError(t, session.MarkExplicitPause(projectRoot, "OxParent", 1))

			t.Setenv("SAGEOX_AGENT_ID", "OxParent")
			t.Setenv("OX_PARENT_PID", strconv.Itoa(os.Getpid()))
			child := startSessionRecordingUnlocked(projectRoot, "OxChild", tc.childAgent, inheritedParentOf(projectRoot, "OxChild"), "", "child-conversation", nil)
			require.NotNil(t, child)
			require.False(t, child.Recording, "harness: a paused parent's child skips recording")

			launch := func(agentID string) *sessionStatus {
				t.Helper()
				t.Setenv("SAGEOX_AGENT_ID", "OxChild")
				t.Setenv("OX_PARENT_PID", strconv.Itoa(os.Getpid()))
				return startSessionRecording(projectRoot, agentID, "claude-code", inheritedParentOf(projectRoot, agentID), "", agentID+"-session")
			}
			paused := launch("OxGrandchild")
			require.NotNil(t, paused)
			assert.False(t, paused.Recording, "the grandchild of a paused parent recorded")

			// the person resumes the root: the whole chain is released
			require.True(t, session.ClearExplicitPause(projectRoot, "OxParent"))
			resumed := launch("OxLaterGrandchild")
			require.NotNil(t, resumed)
			assert.True(t, resumed.Recording, "a grandchild launched after the root resumed stayed paused")
		})
	}
}

// Failure prevented: /clear removes the root's recording state before the
// next prime recreates it. A chain whose pause required that state read as
// released in the gap, so an agent launched then recorded unsuspended while
// the person's pause and the root's agent process were both still there.
func TestARootsPauseHoldsThroughItsClearHandoff(t *testing.T) {
	projectRoot, repoID := setupTestProject(t)
	enableRecordingWithoutLedgerClone(t)
	createActiveRecordingWithPID(t, projectRoot, repoID, "OxParent", os.Getppid())
	require.NoError(t, session.MarkExplicitPause(projectRoot, "OxParent", 1))

	t.Setenv("SAGEOX_AGENT_ID", "OxParent")
	t.Setenv("OX_PARENT_PID", strconv.Itoa(os.Getpid()))
	child := startSessionRecordingUnlocked(projectRoot, "OxChild", "claude-code", inheritedParentOf(projectRoot, "OxChild"), "", "child-session", nil)
	require.NotNil(t, child)
	require.False(t, child.Recording, "harness: a paused parent's child skips recording")

	// the root runs /clear: its recording state is gone until its next prime;
	// its pause marker and its agent process remain
	require.NoError(t, session.ClearRecordingStateForAgent(projectRoot, "OxParent"))
	t.Setenv("SAGEOX_AGENT_ID", "OxChild")
	grandchild := startSessionRecording(projectRoot, "OxGrandchild", "claude-code", inheritedParentOf(projectRoot, "OxGrandchild"), "", "grandchild-session")

	require.NotNil(t, grandchild)
	assert.False(t, grandchild.Recording, "an agent launched during the root's /clear handoff recorded")
}
