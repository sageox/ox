//go:build !short

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
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
		name         string
		modeAtStart  string
		modeAtStatus string
		// attributionOff turns session attribution off in the repo whose
		// recording status reports.
		attributionOff bool
		// runFromOtherRepo runs status from another repo, with the opposite
		// attribution, while OX_PROJECT_ROOT selects the recording's repo.
		runFromOtherRepo bool
		want             string
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
			name:             "attribution on, run from a repo with it off",
			modeAtStart:      config.SessionPublishingAuto,
			modeAtStatus:     config.SessionPublishingAuto,
			runFromOtherRepo: true,
			want:             "pending",
		},
		{
			name:             "attribution off, run from a repo with it on",
			modeAtStart:      config.SessionPublishingAuto,
			modeAtStatus:     config.SessionPublishingAuto,
			attributionOff:   true,
			runFromOtherRepo: true,
			want:             "disabled",
		},
		{
			// Negative control: without it, a status that always says
			// "disabled" would pass every disabled case above.
			name:         "auto throughout, attribution on",
			modeAtStart:  config.SessionPublishingAuto,
			modeAtStatus: config.SessionPublishingAuto,
			want:         "pending",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			projectRoot := pauseResumeProject(t) // sets OX_PROJECT_ROOT
			if tc.attributionOff {
				setSessionAttributionOff(t, projectRoot)
			}
			if tc.runFromOtherRepo {
				t.Chdir(otherAttributionRepo(t, !tc.attributionOff))
			} else {
				t.Chdir(projectRoot)
			}

			const agentID = "OxDrSt"
			startDraftStatusRecording(t, projectRoot, agentID, tc.modeAtStart)

			t.Setenv(config.EnvSessionPublishing, tc.modeAtStatus)
			t.Setenv("SAGEOX_AGENT_ID", agentID)
			out := runSessionStatusJSON(t)
			assert.Equal(t, tc.want, out["draft_state"])
		})
	}
}

// TestRunSessionStatus_DraftStatePerRecording covers the multi-recording
// rows, which share one repo policy: each row must still apply its own
// recording's saved publishing mode.
//
// Failure prevented: with two AI coworkers recording, the one held on this
// machine reports "pending" (or the other one reports "disabled") because the
// rows were decided from the repo setting alone.
func TestRunSessionStatus_DraftStatePerRecording(t *testing.T) {
	projectRoot := pauseResumeProject(t)
	t.Chdir(projectRoot)

	startDraftStatusRecording(t, projectRoot, "OxDrHeld", config.SessionPublishingManual)
	startDraftStatusRecording(t, projectRoot, "OxDrAuto", config.SessionPublishingAuto)

	t.Setenv(config.EnvSessionPublishing, config.SessionPublishingAuto)
	t.Setenv("SAGEOX_AGENT_ID", "")
	out := runSessionStatusJSON(t)

	rows, ok := out["sessions"].([]any)
	require.True(t, ok, "two recordings must produce the multi-row shape: %v", out)
	byAgent := map[string]any{}
	for _, r := range rows {
		row := r.(map[string]any)
		byAgent[row["agent_id"].(string)] = row["draft_state"]
	}
	assert.Equal(t, "disabled", byAgent["OxDrHeld"], "the recording held on this machine")
	assert.Equal(t, "pending", byAgent["OxDrAuto"], "the auto recording beside it")
}

// startDraftStatusRecording starts a real recording under mode and checks
// that the recording saved it.
func startDraftStatusRecording(t *testing.T, projectRoot, agentID, mode string) {
	t.Helper()
	registerTestInstance(t, projectRoot, agentID)
	t.Setenv(config.EnvSessionPublishing, mode)
	state, err := session.StartRecording(projectRoot, session.StartRecordingOptions{
		AgentID:     agentID,
		AdapterName: "claude-code",
		Username:    "testuser",
	})
	require.NoError(t, err)
	require.Equal(t, mode, state.PublishingMode, "the recording must save the mode it started under")
}

// runSessionStatusJSON runs `ox session status --json` and decodes its output.
func runSessionStatusJSON(t *testing.T) map[string]any {
	t.Helper()
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
	return out
}

func setSessionAttributionOff(t *testing.T, projectRoot string) {
	t.Helper()
	projCfg, err := config.LoadProjectConfig(projectRoot)
	require.NoError(t, err)
	off := ""
	projCfg.Attribution = &config.Attribution{Session: &off}
	require.NoError(t, config.SaveProjectConfig(projectRoot, projCfg))
}

// otherAttributionRepo is a second initialized repo, with session attribution
// off when attributionOff is set, for running a command from outside the
// selected project.
func otherAttributionRepo(t *testing.T, attributionOff bool) string {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".sageox"), 0755))
	cfg := `{"config_version":"2","repo_id":"other-repo","endpoint":"http://test.sageox.local"}`
	if attributionOff {
		cfg = `{"config_version":"2","repo_id":"other-repo","endpoint":"http://test.sageox.local","attribution":{"session":""}}`
	}
	require.NoError(t, os.WriteFile(filepath.Join(root, ".sageox", "config.json"), []byte(cfg), 0644))
	return root
}
