//go:build !short

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/require"
)

func TestCaptureHookEntriesSkipsExplicitStopBeforeFinalState(t *testing.T) {
	root, agentID, _ := setupHandleAfterToolTest(t)
	state, err := session.LoadRecordingStateForAgent(root, agentID)
	require.NoError(t, err)
	require.Nil(t, state.StoppedAt)
	raw := filepath.Join(state.SessionPath, "raw.jsonl")
	// A queued hook sees the same generation and an unstopped recording marker
	// while stop is draining/publishing; the breadcrumb is its only veto.
	require.NoError(t, os.WriteFile(raw, []byte("finalized content\n"), 0600))
	require.NoError(t, session.MarkExplicitStop(root, agentID))
	ctx := &HookContext{Phase: phaseAfterTool, ProjectRoot: root, Marker: &SessionMarker{AgentID: agentID}}
	require.NoError(t, captureHookEntries(ctx, agentID, state.SessionPath, state.SessionID))
	got, err := os.ReadFile(raw)
	require.NoError(t, err)
	require.Equal(t, "finalized content\n", string(got))
	current, err := session.LoadRecordingStateForAgent(root, agentID)
	require.NoError(t, err)
	require.Equal(t, state.SourceOffset, current.SourceOffset)
	require.Equal(t, state.HookInvocations, current.HookInvocations)
	require.True(t, session.HasExplicitStop(root, agentID))
}
