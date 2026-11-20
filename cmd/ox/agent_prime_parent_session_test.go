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

// Failure prevented: a coding agent launched from a recorded session's shell
// (`claude -p`, `codex exec`) inherits that session's SAGEOX_AGENT_ID. Its
// prime took the inherited id as its own, the same reuse a /clear in one agent
// process relies on, so the child never became a session of its own and no
// parent link could ever be sent.
func TestRunAgentPrime_AgentLaunchedFromARecordedSessionRecordsItsParent(t *testing.T) {
	env := initializedE2E(t)
	isolateAgentDetection(t)
	isolateSessionMarkerDir(t)
	t.Setenv("SAGEOX_DAEMON", "false")
	t.Setenv("OX_SESSION_RECORDING", "auto")
	ledgerPath, err := ledger.DefaultPath()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(ledgerPath, ".git"), 0o700))
	parentSession, childSession := uniqueSessionID(t, "parent"), uniqueSessionID(t, "child")

	// two live agent processes: the parent's, then the child it launched
	t.Setenv("OX_PARENT_PID", strconv.Itoa(os.Getppid()))
	primeWithHook(t, parentSession, "startup")
	parentAgent, _ := nativeSessionAgentID(t, parentSession)
	parentState, err := session.LoadRecordingStateForAgent(env.Root, parentAgent)
	require.NoError(t, err)
	require.NotNil(t, parentState, "harness: the parent prime must start a recording")

	t.Setenv("SAGEOX_AGENT_ID", parentAgent)
	t.Setenv("OX_PARENT_PID", strconv.Itoa(os.Getpid()))
	primeWithHook(t, childSession, "startup")

	childAgent, _ := nativeSessionAgentID(t, childSession)
	require.NotEqual(t, parentAgent, childAgent, "the launched agent took over its parent's AI coworker identity")
	childState, err := session.LoadRecordingStateForAgent(env.Root, childAgent)
	require.NoError(t, err)
	require.NotNil(t, childState, "the launched agent must record a session of its own")
	assert.Equal(t, parentAgent, childState.ParentAgentID)
	assert.NotEqual(t, parentState.SessionID, childState.SessionID)
}

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
	t.Setenv("SAGEOX_DAEMON", "false")
	t.Setenv("OX_SESSION_RECORDING", "auto")
	ledgerPath, err := ledger.DefaultPath()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(ledgerPath, ".git"), 0o700))
	createActiveRecordingWithPID(t, projectRoot, repoID, "OxParent", os.Getppid())
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
}

// The ids a recording's stream opens with. Failure prevented: a child stream
// opened with no parent id, or with something that is not a ses_ id.
func TestLiveSessionLinks(t *testing.T) {
	projectRoot, repoID := setupTestProject(t)
	parentID, earlierID, ownID := sessionid.GenerateSessionID(), sessionid.GenerateSessionID(), sessionid.GenerateSessionID()
	require.NoError(t, session.SaveRecordingState(projectRoot, &session.RecordingState{
		AgentID: "OxParent", SessionID: parentID, AdapterName: "claude-code", StartedAt: time.Now(), ParentPID: os.Getpid(),
		SessionPath: filepath.Join(session.GetContextPath(repoID), "sessions", "2026-04-01T10-00-user-OxParent"),
	}))

	t.Run("a child names its parent's recording and the one it continues", func(t *testing.T) {
		parent, continued := liveSessionLinks(projectRoot, &session.RecordingState{
			AgentID: "OxChild", SessionID: ownID, ParentAgentID: "OxParent", ContinuedFromSessionID: earlierID,
		})
		assert.Equal(t, parentID, parent)
		assert.Equal(t, earlierID, continued)
	})
	t.Run("a parent with no live recording names nothing", func(t *testing.T) {
		parent, _ := liveSessionLinks(projectRoot, &session.RecordingState{AgentID: "OxChild", SessionID: ownID, ParentAgentID: "OxEnded"})
		assert.Empty(t, parent)
	})
	t.Run("ids that are not ses_ ids, or name the recording itself, are left out", func(t *testing.T) {
		parent, continued := liveSessionLinks(projectRoot, &session.RecordingState{
			AgentID: "OxParent", SessionID: parentID, ParentAgentID: "OxParent", ContinuedFromSessionID: "2026-03-04T13-41-person-a-Oxabc1",
		})
		assert.Empty(t, parent, "a recording is never its own parent")
		assert.Empty(t, continued)
	})
}
