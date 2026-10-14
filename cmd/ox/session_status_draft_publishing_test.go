//go:build !short

package main

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/session"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRunSessionStatus_DraftStateFollowsTheDraftGate drives the real
// `ox session status --json` over a real recording, through every gate that
// stops the Stop hook from publishing a draft.
//
// Failure prevented: status reports draft_state "pending" for a recording the
// hook will never draft, so a coworker asking "why doesn't my /c/ link
// resolve?" waits on a placeholder that cannot arrive. Seen on 0.23.0: a
// recording started with OX_SESSION_PUBLISHING=manual reported "pending" once
// the env var was gone.
func TestRunSessionStatus_DraftStateFollowsTheDraftGate(t *testing.T) {
	tests := []struct {
		name           string
		modeAtStart    string
		modeAtStatus   string
		attributionOff bool
		want           string
	}{
		{
			name:         "started manual, setting now resolves to auto",
			modeAtStart:  config.SessionPublishingManual,
			modeAtStatus: config.SessionPublishingAuto,
			want:         "disabled",
		},
		{
			name:         "started auto, setting now resolves to manual",
			modeAtStart:  config.SessionPublishingAuto,
			modeAtStatus: config.SessionPublishingManual,
			want:         "disabled",
		},
		{
			name:           "session attribution off",
			modeAtStart:    config.SessionPublishingAuto,
			modeAtStatus:   config.SessionPublishingAuto,
			attributionOff: true,
			want:           "disabled",
		},
		{
			// Negative control: without it, a status that always says
			// "disabled" would pass every case above.
			name:         "auto throughout, attribution on",
			modeAtStart:  config.SessionPublishingAuto,
			modeAtStatus: config.SessionPublishingAuto,
			want:         "pending",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			projectRoot := pauseResumeProject(t)
			// Attribution and the project config are found from the working
			// directory, as in a real `ox session status` run from the repo.
			t.Chdir(projectRoot)
			const agentID = "OxDrSt"
			registerTestInstance(t, projectRoot, agentID)

			if tc.attributionOff {
				projCfg, err := config.LoadProjectConfig(projectRoot)
				require.NoError(t, err)
				off := ""
				projCfg.Attribution = &config.Attribution{Session: &off}
				require.NoError(t, config.SaveProjectConfig(projectRoot, projCfg))
			}

			t.Setenv(config.EnvSessionPublishing, tc.modeAtStart)
			state, err := session.StartRecording(projectRoot, session.StartRecordingOptions{
				AgentID:     agentID,
				AdapterName: "claude-code",
				Username:    "testuser",
			})
			require.NoError(t, err)
			require.Equal(t, tc.modeAtStart, state.PublishingMode,
				"the recording must save the mode it started under")

			t.Setenv(config.EnvSessionPublishing, tc.modeAtStatus)
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
			assert.Equal(t, tc.want, out["draft_state"])
		})
	}
}
