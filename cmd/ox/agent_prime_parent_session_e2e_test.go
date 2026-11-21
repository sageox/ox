//go:build !short

package main

import (
	"os"
	"strconv"
	"testing"

	"github.com/sageox/ox/internal/session"
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
	enableRecordingWithoutLedgerClone(t)
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
	assert.Equal(t, parentState.SessionID, childState.ParentSessionID, "the parent's session id was not captured at prime")
	assert.NotEqual(t, parentState.SessionID, childState.SessionID)
}
