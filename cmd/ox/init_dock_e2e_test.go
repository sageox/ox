//go:build !short

package main

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/repotools"
	"github.com/sageox/ox/internal/ui"
)

// TestRunInit_EndsOnTheDockingBox drives `ox init` to its last screen against a
// stub SageOx and snapshots the part a person reads: the dock, the plan prompt,
// then the steps. Stdout is a pipe here, so this is the unframed, art-free form
// that CI, scripts and AI coworkers get.
// Failure prevented: init ending on bold text between separators and saying
// nothing about who can read the sessions it is about to record; a team named
// that registration did not confirm; and the plan prompt drifting from the one
// in the repo-initialized email.
func TestRunInit_EndsOnTheDockingBox(t *testing.T) {
	env := newOxE2E(t)
	withInitFlags(t, env.TeamID)
	initQuiet, initAgentsFlag = false, "claude-code"
	t.Setenv(config.EnvSessionRecording, config.SessionRecordingAuto)
	t.Setenv(config.EnvSessionPublishing, config.SessionPublishingAuto)

	var err error
	output := captureStdoutForPlanCLI(t, func() { err = runInit() })
	require.NoError(t, err)
	text := ansi.Strip(output)

	dock := "SageOx initialized successfully!\n" +
		"  Repo:  " + repotools.GetRepoName(env.Root) + "\n" +
		"  Team:  E2E Team\n" +
		"  Sessions go to E2E Team's Ledger; teammates can read them.\n"
	require.Contains(t, text, dock)
	assert.NotContains(t, text, "══", "the heavy separators are gone")
	assert.NotContains(t, text, "╭", "no frame when stdout is not a terminal")
	assert.NotContains(t, text, "OPTIONAL FOR CLAUDE CODE", "add-ons are offered to a person at a terminal, not to a pipe")

	// the order a person meets things: what is connected, what to try, what to do
	dockAt := strings.Index(text, "SageOx initialized successfully!")
	tryAt := strings.Index(text, "TRY THIS FIRST")
	stepsAt := strings.Index(text, "NEXT STEPS")
	require.True(t, dockAt >= 0 && dockAt < tryAt && tryAt < stepsAt, "dock, then Try this first, then Next Steps:\n%s", text)
	assert.Contains(t, text, "\n  "+ui.InitPlanPrompt+"\n")
	assert.Contains(t, text[stepsAt:], "git commit -m 'SageOx init' && git push",
		"committing the .sageox files stays step 1")
}

// TestRunInit_DockSaysWhatSessionSettingsGive verifies the dock follows the
// repo's own recording and publishing settings instead of asserting a default.
func TestRunInit_DockSaysWhatSessionSettingsGive(t *testing.T) {
	tests := []struct {
		name       string
		recording  string
		publishing string
		want       string
	}{
		{"upload is manual", config.SessionRecordingAuto, config.SessionPublishingManual,
			"Sessions stay on this machine until you upload them to E2E Team's Ledger."},
		{"recording is off", config.SessionRecordingDisabled, config.SessionPublishingAuto,
			"Session recording is off here; nothing goes to the Ledger."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newOxE2E(t)
			withInitFlags(t, env.TeamID)
			initQuiet, initAgentsFlag = false, "claude-code"
			t.Setenv(config.EnvSessionRecording, tt.recording)
			t.Setenv(config.EnvSessionPublishing, tt.publishing)

			var err error
			output := captureStdoutForPlanCLI(t, func() { err = runInit() })
			require.NoError(t, err)

			text := ansi.Strip(output)
			assert.Contains(t, text, tt.want)
			assert.NotContains(t, text, "teammates can read them")
		})
	}
}
