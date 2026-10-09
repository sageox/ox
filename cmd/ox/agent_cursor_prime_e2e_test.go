//go:build !short

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"testing"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/ledger"
	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCursorPrimeReservesIdentityBeforeContextFetch(t *testing.T) {
	env := initializedE2E(t)
	isolateAgentDetection(t)
	isolateSessionMarkerDir(t)
	t.Setenv("AGENT_ENV", "cursor")
	t.Setenv("SAGEOX_AGENT_ID", "")
	oxConfigSetRepo(t, "session_recording", "auto")
	ledgerPath, err := ledger.DefaultPath()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(ledgerPath, 0o755))
	runGit(t, ledgerPath, "init")
	local, err := config.LoadLocalConfig(env.Root)
	require.NoError(t, err)
	local.TeamContexts = []config.TeamContext{{TeamID: env.TeamID, TeamName: "E2E Team", Path: t.TempDir()}}
	require.NoError(t, config.SaveLocalConfig(env.Root, local))
	input, err := json.Marshal(map[string]any{
		"session_id": cursorConversationID, "conversation_id": cursorConversationID,
		"hook_event_name": "sessionStart", "workspace_roots": []string{env.Root},
	})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type observation struct {
		marker *SessionMarker
		state  *session.RecordingState
		err    error
	}
	observed := make(chan observation, 1)
	env.OnRequest(func(r *http.Request) {
		if r.URL.Path == "/api/v1/kb" {
			marker, err := ReadSessionMarker(cursorConversationID)
			var state *session.RecordingState
			if err == nil && marker != nil && marker.AgentID != "" {
				state, err = session.LoadRecordingStateForAgent(env.Root, marker.AgentID)
			}
			select {
			case observed <- observation{marker, state, err}:
			default:
			}
			// Interrupt the slow phase before prime's final marker write.
			cancel()
		}
	})
	cmd := agentPrimeCmd
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	cmd.SetContext(ctx)
	require.NoError(t, cmd.Flags().Set("agent", "cursor"))
	require.NoError(t, cmd.Flags().Set("format", "json"))
	t.Cleanup(func() {
		_ = cmd.Flags().Set("agent", "")
		_ = cmd.Flags().Set("format", "")
		cmd.SetContext(context.Background())
		cmd.SetOut(nil)
		cmd.SetErr(nil)
	})
	withStdin(t, string(input), func() { require.NoError(t, runAgentPrime(cmd, nil)) })
	var first *SessionMarker
	select {
	case result := <-observed:
		require.NoError(t, result.err)
		first = result.marker
		require.NotNil(t, result.state, "recording must already be running at context fetch")
		assert.Equal(t, cursorConversationID, result.state.AgentSessionID)
	default:
		t.Fatal("prime did not reach the context-fetch boundary")
	}
	require.NotNil(t, first)
	require.NotEmpty(t, first.AgentID, "a timeout at context fetch must not lose the assigned identity")
	assert.False(t, first.IsPrimed(), "identity reservation must not claim context delivery")
	require.NotNil(t, first.CursorSourceBoundary)
	env.OnRequest(nil)
	cmd.SetContext(context.Background())
	withStdin(t, string(input), func() { require.NoError(t, runAgentPrime(cmd, nil)) })
	retried, err := ReadSessionMarker(cursorConversationID)
	require.NoError(t, err)
	assert.Equal(t, first.AgentID, retried.AgentID)
	states, err := session.LoadAllRecordingStates(env.Root)
	require.NoError(t, err)
	require.Len(t, states, 1, "retry must reuse the existing native recording")
	assert.Equal(t, first.AgentID, states[0].AgentID)
}
