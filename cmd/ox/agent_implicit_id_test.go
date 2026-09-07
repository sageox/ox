package main

import (
	"os"
	"testing"
	"time"

	"github.com/sageox/ox/internal/session"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRunAgentDispatcher_ResolvesNativeSessionMarkerWithoutEnv exercises the
// production shorthand boundary rather than calling the resolver directly.
// Codex and other non-Claude hosts cannot export SAGEOX_AGENT_ID into their
// parent process, so `ox agent session pause` must recover the primed identity
// from the native-session marker and actually dispatch the requested command.
func TestRunAgentDispatcher_ResolvesNativeSessionMarkerWithoutEnv(t *testing.T) {
	projectRoot := pauseResumeProject(t)
	isolateSessionMarkerDir(t)

	const (
		agentID         = "OxBare"
		nativeSessionID = "codex-native-session-bare-command"
	)
	registerTestInstance(t, projectRoot, agentID)
	_, err := session.StartRecording(projectRoot, session.StartRecordingOptions{
		AgentID:     agentID,
		AdapterName: "codex",
		Username:    "testuser",
	})
	require.NoError(t, err)
	require.NoError(t, WriteSessionMarker(&SessionMarker{
		AgentID:        agentID,
		AgentSessionID: nativeSessionID,
		PrimedAt:       time.Now(),
	}))

	t.Setenv("SAGEOX_AGENT_ID", "")
	t.Setenv("AGENT_ENV", "codex")
	t.Setenv("CODEX_THREAD_ID", nativeSessionID)

	cmd := &cobra.Command{}
	require.NoError(t, runAgentDispatcher(cmd, []string{"session", "pause"}))

	state, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
	require.NoError(t, err)
	require.NotNil(t, state)
	assert.NotNil(t, state.SuspendedAt, "bare session command must dispatch to the marker's AI coworker")
}

// TestFindUnambiguousSessionMarkerByPID_DoesNotGuessAmongAgentIDs pins the
// safety boundary used for agents whose native runtime exposes no session ID.
// Reusing a process across clear/resume boundaries can leave more than one live
// marker; selecting whichever filename sorts first would operate on an
// unrelated session.
func TestFindUnambiguousSessionMarkerByPID_DoesNotGuessAmongAgentIDs(t *testing.T) {
	isolateSessionMarkerDir(t)
	pid := os.Getpid()
	for i, marker := range []*SessionMarker{
		{AgentID: "OxOne1", AgentSessionID: "one", ParentPID: pid},
		{AgentID: "OxTwo2", AgentSessionID: "two", ParentPID: pid},
	} {
		marker.PrimedAt = time.Now().Add(time.Duration(i) * time.Second)
		require.NoError(t, WriteSessionMarker(marker))
	}

	assert.Nil(t, FindUnambiguousSessionMarkerByPID(pid))
}
