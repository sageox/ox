//go:build !short

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/livesession"
	"github.com/sageox/ox/internal/session"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRunSessionStatus_EdgeBlockedNoticePerRecording: with two recordings,
// both the JSON rows and the text output name the one whose live stream the
// network edge blocks, and only that one.
//
// Failure prevented: the notice was read only for a single recording, so
// starting a second AI teammate hid an existing block warning.
func TestRunSessionStatus_EdgeBlockedNoticePerRecording(t *testing.T) {
	if testing.Short() {
		t.Skip("starts two real recordings: over 500 ms")
	}
	projectRoot := pauseResumeProject(t)
	t.Chdir(projectRoot)
	blocked := startNoticeRecording(t, projectRoot, "OxEdBlk")
	startNoticeRecording(t, projectRoot, "OxEdOk")
	writeEdgeBlockedStream(t, blocked.SessionPath)
	t.Setenv("SAGEOX_AGENT_ID", "")

	out := runSessionStatusJSON(t)
	rows, ok := out["sessions"].([]any)
	require.True(t, ok, "two recordings must produce the multi-row shape: %v", out)
	notices := map[string]any{}
	for _, r := range rows {
		row := r.(map[string]any)
		notices[row["agent_id"].(string)] = row["live_stream_notice"]
	}
	require.Contains(t, notices, "OxEdBlk")
	require.Contains(t, notices, "OxEdOk")
	assert.Contains(t, notices["OxEdBlk"], "blocked by the network edge", "the blocked recording's JSON row")
	assert.Nil(t, notices["OxEdOk"], "the recording beside it has nothing to say")

	text := captureStdoutForPlanCLI(t, func() { require.NoError(t, runSessionStatusText(t)) })
	assert.Equal(t, 1, strings.Count(text, "blocked by the network edge"), "the multi-recording text output names the block once")
}

func startNoticeRecording(t *testing.T, projectRoot, agentID string) *session.RecordingState {
	t.Helper()
	registerTestInstance(t, projectRoot, agentID)
	state, err := session.StartRecording(projectRoot, session.StartRecordingOptions{
		AgentID: agentID, AdapterName: "claude-code", Username: "testuser",
	})
	require.NoError(t, err)
	require.NotEmpty(t, state.SessionPath)
	return state
}

// writeEdgeBlockedStream leaves the stream state a drain writes once the
// network edge started blocking it.
func writeEdgeBlockedStream(t *testing.T, dir string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o700))
	b, err := json.Marshal(livesession.State{
		ConversationID: "cnv_01a1f5e1-6ea2-7635-bbd0-25410e9ed413", Status: livesession.StatusStreaming,
		EdgeBlockedSince: time.Now().Add(-5 * time.Minute).UTC(),
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "stream-state.json"), b, 0o600))
}

// runSessionStatusText runs `ox session status` with text output.
func runSessionStatusText(t *testing.T) error {
	t.Helper()
	root := &cobra.Command{}
	root.PersistentFlags().Bool("json", false, "")
	statusCmd := &cobra.Command{}
	statusCmd.Flags().Bool("current", false, "")
	root.AddCommand(statusCmd)
	statusCmd.SetOut(&bytes.Buffer{})
	return runSessionStatus(statusCmd, nil)
}
