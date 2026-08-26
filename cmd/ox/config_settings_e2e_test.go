package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/ledger"
	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This file is the end-to-end, customer-facing proof that an `ox config`
// setting a coworker changes actually changes what they experience downstream —
// not that a resolver returns a value in isolation, but that driving the real
// `ox config set` command changes the real command output the customer sees.
// It is the executable mirror of tests/acceptance/features/config/*.feature.
//
// The lens: `ox config get` / resolver unit tests already prove the value round-
// trips. What was untested is the JOIN — set the real setting, then run the real
// consuming flow, and observe the difference the customer would. That join is
// where "I turned it off but it still happened" bugs live.

// initedProjectForConfigE2E builds a real initialized project (git repo +
// .sageox) and isolates user-level config so the host's real ~/.sageox cannot
// bleed into the resolved value. cwd is set to the project so the real commands
// resolve it the way they do for a coworker standing in their repo.
func initedProjectForConfigE2E(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("short: real git + config file writes")
	}
	if _, err := os.Stat("/dev/null"); err != nil {
		t.Skip("needs a real filesystem")
	}
	root := t.TempDir()
	runGit(t, root, "init")
	sageox := filepath.Join(root, ".sageox")
	require.NoError(t, os.MkdirAll(sageox, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(sageox, "config.json"),
		[]byte(`{"config_version":"2","repo_id":"repo_config_e2e"}`), 0644))

	// Isolate user config: OX_USER_CONFIG points at a path that does not exist,
	// so LoadUserConfig contributes nothing and the resolved value is exactly
	// (repo config) over (default) — no host bleed-through.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("OX_USER_CONFIG", filepath.Join(home, "user-config.yaml"))
	t.Setenv("SAGEOX_AGENT_ID", "") // human commit path: attribute unconditionally
	t.Chdir(root)
	return root
}

// oxConfigSetRepo drives the REAL `ox config set <key> <value> --repo` command
// (flag parsing, attribution case-preservation, file write) — not SetConfigValue
// in isolation.
func oxConfigSetRepo(t *testing.T, key, value string) {
	t.Helper()
	require.NoError(t, configSetCmd.Flags().Set("repo", "true"))
	t.Cleanup(func() { _ = configSetCmd.Flags().Set("repo", "false") })
	require.NoError(t, runConfigSet(configSetCmd, []string{key, value}))
	require.NoError(t, configSetCmd.Flags().Set("repo", "false"))
}

// TestConfigE2E_AttributionCommitControlsTheCommitTrailer proves the customer
// promise: what a coworker sets for attribution.commit is exactly what lands in
// their real commit message — including turning it off entirely.
//
// The observable is the commit message the prepare-commit-msg hook produces —
// the artifact a teammate reads in `git log`. Failure prevented: a coworker who
// disabled attribution still ships the SageOx trailer (or a custom trailer is
// silently ignored). Red-first: hardcode the hook to the default attribution
// (ignore config) and the "off" and "custom" assertions fail.
func TestConfigE2E_AttributionCommitControlsTheCommitTrailer(t *testing.T) {
	initedProjectForConfigE2E(t)

	writeMsg := func() string {
		f := filepath.Join(t.TempDir(), "COMMIT_EDITMSG")
		require.NoError(t, os.WriteFile(f, []byte("feat: ship a thing\n"), 0644))
		return f
	}
	runCommitMsgHook := func(msgFile string) string {
		origFile, origSource := hooksCommitMsgFile, hooksCommitMsgSource
		hooksCommitMsgFile = msgFile
		hooksCommitMsgSource = ""
		t.Cleanup(func() { hooksCommitMsgFile = origFile; hooksCommitMsgSource = origSource })
		require.NoError(t, runHooksCommitMsg(hooksCommitMsgCmd, nil))
		out, err := os.ReadFile(msgFile)
		require.NoError(t, err)
		return string(out)
	}

	// Given the default config: an ox-guided commit carries the SageOx trailer.
	assert.Contains(t, runCommitMsgHook(writeMsg()),
		"Co-Authored-By: SageOx <ox@sageox.ai>",
		"with default config, a commit carries the SageOx co-author trailer")

	// When the coworker disables attribution via the real command...
	oxConfigSetRepo(t, "attribution.commit", "")
	// Then the trailer disappears from the real commit message entirely.
	assert.NotContains(t, runCommitMsgHook(writeMsg()), "Co-Authored-By",
		`ox config set attribution.commit "" removes the trailer from real commits`)

	// When the coworker sets a custom attribution via the real command...
	oxConfigSetRepo(t, "attribution.commit", "Co-Authored-By: Devon <devon@example.com>")
	// Then exactly that trailer lands, replacing (not stacking) the default.
	got := runCommitMsgHook(writeMsg())
	assert.Contains(t, got, "Co-Authored-By: Devon <devon@example.com>",
		"a custom attribution.commit is exactly what lands in the commit message")
	assert.NotContains(t, got, "SageOx <ox@sageox.ai>",
		"the custom value replaces the default trailer, it does not stack on top")
}

// oxConfigSetUser drives the REAL `ox config set <key> <value>` command at the
// default (user) level.
func oxConfigSetUser(t *testing.T, key, value string) {
	t.Helper()
	require.NoError(t, configSetCmd.Flags().Set("repo", "false"))
	require.NoError(t, configSetCmd.Flags().Set("team", "false"))
	require.NoError(t, runConfigSet(configSetCmd, []string{key, value}))
}

// TestConfigE2E_SessionRecordingGateControlsRecording proves the master switch:
// what a coworker sets for session_recording decides whether an agent session
// is actually recorded to disk — not just what a resolver returns.
//
// The observable is the recording itself: the per-agent state and the raw.jsonl
// transcript. Failure prevented: a coworker who set recording to disabled still
// has sessions captured (or auto silently records nothing). Red-first: force
// startSessionRecording past the `!IsAuto()` gate and the disabled case records.
func TestConfigE2E_SessionRecordingGateControlsRecording(t *testing.T) {
	f := newDraftLedgerFixture(t)
	t.Chdir(f.projectRoot)
	cfg = &config.Config{}
	const agentID = "OxRecGate"

	// Disabled via the real command: the gate creates no recording.
	oxConfigSetRepo(t, "session_recording", "disabled")
	assert.Nil(t, startSessionRecording(f.projectRoot, agentID, "claude-code", "", ""),
		"session_recording=disabled must not start a recording")
	st, err := session.LoadRecordingStateForAgent(f.projectRoot, agentID)
	require.NoError(t, err)
	assert.Nil(t, st, "no recording state may exist when recording is disabled")

	// Provision a ledger at its default location so the auto path can record —
	// the disabled path short-circuits (at the !IsAuto gate) before this check.
	lp, derr := ledger.DefaultPath()
	require.NoError(t, derr)
	require.NoError(t, os.MkdirAll(filepath.Join(lp, ".git"), 0o755))

	// Auto via the real command: the gate creates a recording with a transcript.
	oxConfigSetRepo(t, "session_recording", "auto")
	status := startSessionRecording(f.projectRoot, agentID, "claude-code", "", "")
	require.NotNil(t, status, "session_recording=auto must start a recording")
	assert.True(t, status.Recording, "auto must actually record")
	st2, err := session.LoadRecordingStateForAgent(f.projectRoot, agentID)
	require.NoError(t, err)
	require.NotNil(t, st2, "auto must persist recording state")
	assert.FileExists(t, filepath.Join(st2.SessionPath, "raw.jsonl"),
		"auto must create the session transcript on disk")
}

// TestConfigE2E_ResumeLinksNewRecordingToPriorSession exercises the durable
// resume handoff: a native coding-agent session marker survives SessionEnd,
// and a later prime uses its prior ses_ identity to link the new recording.
func TestConfigE2E_ResumeLinksNewRecordingToPriorSession(t *testing.T) {
	f := newDraftLedgerFixture(t)
	t.Chdir(f.projectRoot)
	cfg = &config.Config{}
	oxConfigSetRepo(t, "session_recording", "auto")

	const (
		agentID        = "OxResume"
		agentSessionID = "claude-resume-fixture"
	)
	t.Cleanup(func() { _ = DeleteSessionMarker(agentSessionID) })

	firstStatus := startSessionRecording(f.projectRoot, agentID, "claude-code", "", "")
	require.NotNil(t, firstStatus)
	first, err := session.LoadRecordingStateForAgent(f.projectRoot, agentID)
	require.NoError(t, err)
	require.NotNil(t, first)

	marker := &SessionMarker{
		AgentID:            agentID,
		RecordingSessionID: first.SessionID,
		AgentSessionID:     agentSessionID,
		PrimedAt:           time.Now(),
	}
	require.NoError(t, WriteSessionMarker(marker))

	// SessionEnd finalizes the first recording and clears its active state.
	// Removing the cache directory models the daemon's successful upload/prune.
	_, err = session.StopRecording(f.projectRoot, agentID)
	require.NoError(t, err)
	require.NoError(t, os.RemoveAll(first.SessionPath))

	resumedMarker, err := ReadSessionMarker(agentSessionID)
	require.NoError(t, err)
	continuedFrom := recordingSessionIDFromMarker(resumedMarker)
	secondStatus := startSessionRecording(f.projectRoot, agentID, "claude-code", "", continuedFrom)
	require.NotNil(t, secondStatus)
	second, err := session.LoadRecordingStateForAgent(f.projectRoot, agentID)
	require.NoError(t, err)
	require.NotNil(t, second)

	assert.NotEqual(t, first.SessionID, second.SessionID, "each finalized half keeps its own identity")
	assert.Equal(t, first.SessionID, second.ContinuedFromSessionID)

	stored, err := session.ReadSessionFromPath(filepath.Join(second.SessionPath, "raw.jsonl"))
	require.NoError(t, err)
	require.NotNil(t, stored.Meta)
	assert.Equal(t, first.SessionID, stored.Meta.ContinuedFromSessionID,
		"continuation must survive loss of the ephemeral recording state")
}

// TestConfigE2E_CloudQueryGateControlsTheNetworkDecision proves the privacy
// default and the opt-in, end to end through the real prompt-path decision.
//
// The observable is PrepareCloudQuery's decision — the canonical chokepoint every
// future cloud query must pass. Failure prevented: ox reaches the network on the
// prompt path when the coworker never opted in, or transmits an un-redacted
// prompt. Red-first: drop the `!ResolveUserPromptSubmitCloudQuery` gate and the
// default-off case opts in.
func TestConfigE2E_CloudQueryGateControlsTheNetworkDecision(t *testing.T) {
	withIsolatedConfig(t)

	// Default (off): the decision never opts in and the redactor never runs.
	def := PrepareCloudQuery(context.Background(), "", "find the login handler")
	assert.False(t, def.ShouldQuery, "default config must make no cloud query")
	assert.Empty(t, def.RedactedPrompt, "the off path must not invoke the redactor")

	// Opt in via the real command, with a local (fake, non-expired) token.
	oxConfigSetUser(t, "hooks.userpromptsubmit.cloud_query", "on")
	writeFakeToken(t, "")
	const secret = "ghp_abcdefghijklmnopqrstuvwxyz0123456789"
	on := PrepareCloudQuery(context.Background(), "", "debug this, token: "+secret)
	assert.True(t, on.ShouldQuery, "cloud_query=on + a valid token must open the gate")
	assert.NotContains(t, on.RedactedPrompt, secret,
		"the prompt must be redacted before anything could be transmitted")
	assert.Contains(t, on.RedactedPrompt, "[REDACTED_GITHUB_TOKEN]",
		"the redacted slug is what a transmit would carry")
}

// TestConfigE2E_PlanHTMLAndOpenControlTheNudge proves that plan.html gates
// whether the plan-exit render recommendation fires, and plan.open shapes what
// it tells the agent to do.
//
// The observable is the stashed nudge file and its text. Failure prevented: a
// coworker who turned plan HTML off still gets render nudges, or plan.open is
// ignored so the agent opens a browser the coworker said never to. Red-first:
// drop the `PlanHTML==Off` gate and the off case still stashes a nudge.
func TestConfigE2E_PlanHTMLAndOpenControlTheNudge(t *testing.T) {
	root := planNudgeProject(t)
	isolatePlanConfigEnv(t)
	t.Chdir(root)
	stubPlanEnrichment(t, func(string) (planJSONResult, bool) {
		var r planJSONResult
		r.Signals.NonTrivial = true // material enough that a render would be recommended
		return r, true
	})
	const agentID = "OxPlanCfg"
	nudgeFile := planNudgePath(root, agentID)
	run := func() { handlePlanExit(planExitCtx(root, "# Big plan\n- do the thing"), agentID) }

	// plan.html=off: no render nudge is stashed at all.
	oxConfigSetUser(t, "plan.html", "off")
	_ = os.Remove(nudgeFile)
	run()
	assert.NoFileExists(t, nudgeFile, "plan.html=off must produce no render nudge")

	// plan.html=recommend + plan.open=always: a nudge is stashed, told to open directly.
	oxConfigSetUser(t, "plan.html", "recommend")
	oxConfigSetUser(t, "plan.open", "always")
	_ = os.Remove(nudgeFile)
	run()
	require.FileExists(t, nudgeFile, "plan.html=recommend must stash a render nudge")
	body, err := os.ReadFile(nudgeFile)
	require.NoError(t, err)
	assert.Contains(t, string(body), "plan.open=always",
		"plan.open=always must shape the nudge to open the render directly")

	// plan.open=never: the nudge is told NOT to open a browser.
	oxConfigSetUser(t, "plan.open", "never")
	_ = os.Remove(nudgeFile)
	run()
	body2, err := os.ReadFile(nudgeFile)
	require.NoError(t, err)
	assert.Contains(t, string(body2), "do NOT prompt to open",
		"plan.open=never must suppress the open directive")
}

// TestConfigE2E_InvalidValueRejected proves the real `ox config set` command
// rejects an out-of-enum value rather than silently accepting it — a coworker
// who fat-fingers a setting gets a clear error, not a quietly broken config.
func TestConfigE2E_InvalidValueRejected(t *testing.T) {
	initedProjectForConfigE2E(t)
	require.NoError(t, configSetCmd.Flags().Set("repo", "false"))
	require.NoError(t, configSetCmd.Flags().Set("team", "false"))
	err := runConfigSet(configSetCmd, []string{"session_recording", "bogus"})
	require.Error(t, err, "an out-of-enum value must be rejected by `ox config set`")
}
