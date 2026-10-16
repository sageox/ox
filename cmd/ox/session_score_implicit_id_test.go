package main

import (
	"testing"
	"time"

	"github.com/sageox/ox/internal/session"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRunSessionScore_ResolvesPrimedAgentWithoutEnv pins the contract prime
// relies on when it tells every AI coworker to run `ox session score`: the
// command must find the primed session without SAGEOX_AGENT_ID. Only Claude
// Code's SessionStart hook can export that variable into later tool calls, so
// Codex, and Claude Code when prime ran outside the hook, reach this command
// with it empty and recover their identity from prime's native-session marker.
func TestRunSessionScore_ResolvesPrimedAgentWithoutEnv(t *testing.T) {
	tests := []struct {
		name            string
		agentID         string
		adapter         string
		nativeSessionID string
		nativeEnv       string
	}{
		{
			name:            "codex",
			agentID:         "OxCdxS",
			adapter:         "codex",
			nativeSessionID: "codex-native-session-score",
			nativeEnv:       "CODEX_THREAD_ID",
		},
		{
			name:            "claude code primed outside the hook",
			agentID:         "OxClaS",
			adapter:         "claude",
			nativeSessionID: "claude-native-session-score",
			nativeEnv:       "CLAUDE_CODE_SESSION_ID",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// scores live under paths.CacheDir(); keep them out of the real cache
			t.Setenv("OX_XDG_DISABLE", "")
			t.Setenv("XDG_CACHE_HOME", t.TempDir())

			projectRoot := pauseResumeProject(t)
			isolateSessionMarkerDir(t)
			registerTestInstance(t, projectRoot, tt.agentID)
			_, err := session.StartRecording(projectRoot, session.StartRecordingOptions{
				AgentID:     tt.agentID,
				AdapterName: tt.adapter,
				Username:    "testuser",
			})
			require.NoError(t, err)
			require.NoError(t, WriteSessionMarker(&SessionMarker{
				AgentID:        tt.agentID,
				AgentSessionID: tt.nativeSessionID,
				PrimedAt:       time.Now(),
			}))

			// clear competing agent signals first, then present only this host's
			isolateAgentDetection(t)
			t.Setenv("SAGEOX_AGENT_ID", "")
			t.Setenv("AGENT_ENV", tt.adapter)
			if tt.adapter == "claude" {
				t.Setenv("CLAUDECODE", "1")
			}
			t.Setenv(tt.nativeEnv, tt.nativeSessionID)

			cmd := &cobra.Command{}
			cmd.Flags().String("score", "", "")
			cmd.Flags().String("reason", "", "")
			require.NoError(t, cmd.Flags().Set("score", "moderate"))
			require.NoError(t, cmd.Flags().Set("reason", "team context guided the fix"))

			require.NoError(t, runSessionScore(cmd, nil),
				"session score must resolve the primed AI coworker without SAGEOX_AGENT_ID")

			sf, err := session.ReadSageoxScore(tt.agentID)
			require.NoError(t, err)
			require.NotNil(t, sf, "score must be recorded for the primed AI coworker")
			assert.Equal(t, session.ScoreCategory("moderate"), sf.Category)
		})
	}
}
