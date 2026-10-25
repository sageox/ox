//go:build !short

package main

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/sageox/ox/internal/agentinstance"
	"github.com/sageox/ox/internal/session"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRunSessionStatus_SurfacesPauseStateInJSON covers the ADR-020 pause block
// in runSessionStatus, which had no test at all.
//
// Failure prevented: a paused recording that reports itself as simply
// "recording". The whole point of surfacing pause state in --json is that an AI
// coworker checks status programmatically and must be able to tell "I am
// recording" from "I am paused and nothing I say is being captured". Without
// these fields the agent keeps working believing its session is being recorded,
// and the guidance naming the resume command never reaches it either.
func TestRunSessionStatus_SurfacesPauseStateInJSON(t *testing.T) {
	projectRoot := pauseResumeProject(t)
	const agentID = "OxStat"
	registerTestInstance(t, projectRoot, agentID)

	_, err := session.StartRecording(projectRoot, session.StartRecordingOptions{
		AgentID:     agentID,
		AdapterName: "claude-code",
		Username:    "testuser",
	})
	require.NoError(t, err)
	require.NoError(t, runAgentSessionPause(&agentinstance.Instance{AgentID: agentID}, nil))

	t.Setenv("SAGEOX_AGENT_ID", agentID)

	// runSessionStatus reads --json off the ROOT command's persistent flags.
	var buf bytes.Buffer
	root := &cobra.Command{}
	root.PersistentFlags().Bool("json", true, "")
	statusCmd := &cobra.Command{}
	statusCmd.Flags().Bool("current", false, "")
	root.AddCommand(statusCmd)
	statusCmd.SetOut(&buf)

	require.NoError(t, runSessionStatus(statusCmd, nil))

	var out map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &out), "status must emit valid JSON: %s", buf.String())

	assert.Equal(t, true, out["suspended"], "a paused recording must report suspended=true")
	assert.NotEmpty(t, out["suspended_at"], "suspended_at must carry the pause timestamp")
	assert.NotEmpty(t, out["suspended_duration"], "suspended_duration must be human-readable")
	if guidance, ok := out["guidance"].(string); ok {
		assert.Contains(t, guidance, "SUSPENDED",
			"guidance must tell the agent its recording is not capturing")
		assert.Contains(t, guidance, "session resume",
			"guidance must name the command that recovers")
	} else {
		t.Error("paused status must carry guidance naming the resume command")
	}
}
