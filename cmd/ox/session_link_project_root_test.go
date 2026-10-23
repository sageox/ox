//go:build !short

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/ledger"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/pipeline"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Session-link gates read the project they were handed, never the repo in the
// working directory.
//
// OX_PROJECT_ROOT selects a project that need not contain the working
// directory. A gate that found its project by walking up from the working
// directory read the other repo's attribution.session, so a coworker who turned
// session links off in the selected repo still got them (registration, the stop
// "View at" link, prime's PR directive), and one who left them on lost them.
//
// Every case runs from a second repo whose attribution is the OPPOSITE of the
// selected repo's, in both directions. A second repo with the same setting
// would pass against the old working-directory loader.

// attributionMismatchCases are the two directions every gate below runs.
var attributionMismatchCases = []struct {
	name string
	// selectedOff turns session attribution off in the selected repo; the
	// working-directory repo always has the opposite setting.
	selectedOff bool
}{
	{name: "selected repo on, working directory off", selectedOff: false},
	{name: "selected repo off, working directory on", selectedOff: true},
}

// runFromOppositeRepo selects projectRoot through OX_PROJECT_ROOT and moves the
// working directory into a repo whose session attribution is the opposite of
// projectRoot's.
func runFromOppositeRepo(t *testing.T, projectRoot string, selectedOff bool) {
	t.Helper()
	if selectedOff {
		setSessionAttributionOff(t, projectRoot)
	}
	t.Setenv("OX_PROJECT_ROOT", projectRoot)
	t.Chdir(otherAttributionRepo(t, !selectedOff))
}

// TestNotifySessionStartedAsync_ReadsTheSelectedRepoAttribution covers start
// registration, the gate drafts share.
//
// Failure prevented: registration follows the working-directory repo, so a repo
// with session links off still tells the server about its sessions, and one
// with them on never registers, leaving its /c/ links to 404.
func TestNotifySessionStartedAsync_ReadsTheSelectedRepoAttribution(t *testing.T) {
	for _, tc := range attributionMismatchCases {
		t.Run(tc.name, func(t *testing.T) {
			projectRoot, requestCount := newNotifyManualFixture(t, config.SessionPublishingAuto)
			runFromOppositeRepo(t, projectRoot, tc.selectedOff)
			state := newRegisteredTurnState(t, "OxNotifyRoot")

			notifySessionStartedAsync(projectRoot, state)

			if tc.selectedOff {
				assert.Zero(t, atomic.LoadInt32(requestCount),
					"session attribution is off in the selected repo: nothing may be registered")
				return
			}
			assert.Equal(t, int32(1), atomic.LoadInt32(requestCount),
				"session attribution is on in the selected repo: the session must be registered")
		})
	}
}

// TestSessionStop_ReadsTheSelectedRepoAttribution drives the real
// `ox agent <id> session stop` with OX_PROJECT_ROOT selecting the recording's
// repo.
//
// Failure prevented: stop prints a "View at" /c/ link for a repo whose session
// links are off, or drops it for one whose links are on, because it read the
// working-directory repo's setting.
func TestSessionStop_ReadsTheSelectedRepoAttribution(t *testing.T) {
	for _, tc := range attributionMismatchCases {
		t.Run(tc.name, func(t *testing.T) {
			fx := setupStopLocationTest(t, config.SessionPublishingAuto)
			runFromOppositeRepo(t, fx.projectRoot, tc.selectedOff)

			parsed := runSessionStopJSON(t, fx)

			if tc.selectedOff {
				assert.Empty(t, parsed.SessionURL, "session attribution is off in the selected repo: no /c/ link")
				return
			}
			assert.Contains(t, parsed.SessionURL, fx.apiServer.URL+"/c/",
				"session attribution is on in the selected repo: stop must print the /c/ link")
		})
	}
}

// TestSessionStop_ReadsTheSelectedRepoPublishingMode covers the other half of
// the link gate: the publishing mode it reads must be the selected repo's too.
// The repo turns manual after the recording started, so the recording's saved
// mode (auto) does not decide it and only the setting read is under test.
//
// Failure prevented: stop holds the session for the selected repo's manual
// setting yet prints its dead /c/ link because the gate read the
// working-directory repo's auto, or drops the link of a published session
// because the working-directory repo is manual.
func TestSessionStop_ReadsTheSelectedRepoPublishingMode(t *testing.T) {
	tests := []struct {
		name               string
		selected, workRepo string
		wantURL            bool
	}{
		{"selected repo manual, working directory auto", config.SessionPublishingManual, config.SessionPublishingAuto, false},
		{"selected repo auto, working directory manual", config.SessionPublishingAuto, config.SessionPublishingManual, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(config.EnvSessionPublishing, "") // the env var outranks every config file
			fx := setupStopLocationTest(t, config.SessionPublishingAuto)

			projCfg, err := config.LoadProjectConfig(fx.projectRoot)
			require.NoError(t, err)
			projCfg.SessionPublishing = tc.selected
			require.NoError(t, config.SaveProjectConfig(fx.projectRoot, projCfg))
			t.Setenv("OX_PROJECT_ROOT", fx.projectRoot)
			t.Chdir(otherPublishingRepo(t, tc.workRepo))

			parsed := runSessionStopJSON(t, fx)

			if !tc.wantURL {
				assert.Empty(t, parsed.SessionURL, "the selected repo is manual: the session is held and never registered")
				return
			}
			assert.Contains(t, parsed.SessionURL, fx.apiServer.URL+"/c/",
				"the selected repo is auto: stop must print the /c/ link")
		})
	}
}

// otherPublishingRepo is a second initialized git repo whose session_publishing
// is mode and whose session attribution is on.
func otherPublishingRepo(t *testing.T, mode string) string {
	t.Helper()
	root := t.TempDir()
	gitInitRepo(t, root)
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".sageox"), 0o755))
	cfg := `{"config_version":"2","repo_id":"other-repo","endpoint":"http://test.sageox.local","session_publishing":"` + mode + `"}`
	require.NoError(t, os.WriteFile(filepath.Join(root, ".sageox", "config.json"), []byte(cfg), 0o644))
	return root
}

// TestSessionStop_HeldRecordingPrintsNoLink covers a recording started under
// session_publishing: manual and stopped after the setting resolves to auto.
// Stop holds it on this machine (the saved mode wins) and it was never
// registered, so its /c/ link 404s.
//
// Failure prevented: the stop summary hands the coworker a dead "View at" link
// for a session it just kept local. Red-first: drop the saved-mode half of the
// rule sessionLinkOutputs applies and session_url comes back populated.
func TestSessionStop_HeldRecordingPrintsNoLink(t *testing.T) {
	t.Setenv(config.EnvSessionPublishing, config.SessionPublishingManual)
	fx := setupStopLocationTest(t, config.SessionPublishingAuto)
	state, err := session.LoadRecordingStateForAgent(fx.projectRoot, fx.inst.AgentID)
	require.NoError(t, err)
	require.True(t, state.RecordedManualPublishing(), "fixture: the recording must save the manual mode it started under")

	t.Setenv(config.EnvSessionPublishing, config.SessionPublishingAuto)
	parsed := runSessionStopJSON(t, fx)

	assert.Empty(t, parsed.SessionURL, "a session held on this machine was never registered: no /c/ link")
	assert.NotEmpty(t, parsed.UploadWarning, "fixture: stop must have held the session, not published it")
}

// runSessionStopJSON stops fx's recording and decodes the JSON stop output.
func runSessionStopJSON(t *testing.T, fx stopLocationTestFixture) pipeline.StopOutput {
	t.Helper()
	oldGlobalCfg := cfg
	cfg = &config.Config{} // default JSON stop output
	t.Cleanup(func() { cfg = oldGlobalCfg })

	stdout := captureRealStdout(t, func() {
		require.NoError(t, runAgentSessionStop(fx.inst))
	})
	var parsed pipeline.StopOutput
	require.NoError(t, json.Unmarshal(stdout, &parsed), "stop output must be valid JSON: %s", stdout)
	return parsed
}

// TestRunAgentPrime_ReadsTheSelectedRepoAttribution drives the real
// `ox agent prime --format json` with OX_PROJECT_ROOT selecting the repo.
//
// Failure prevented: prime reports the working-directory repo's attribution to
// the AI coworker and gates the session URL and PR directive on it, so the
// coworker stamps SageOx-Session lines into PRs for a repo that turned them off,
// or never gets them in one that left them on.
func TestRunAgentPrime_ReadsTheSelectedRepoAttribution(t *testing.T) {
	for _, tc := range attributionMismatchCases {
		t.Run(tc.name, func(t *testing.T) {
			env := initializedE2E(t)
			// Prime records only into a provisioned Ledger, and looks for it
			// from the working directory. Both repos get one, the state of a
			// coworker with two initialized repos, so this test does not lean
			// on which repo that lookup picks.
			initLedgerForWorkingDir(t)
			runFromOppositeRepo(t, env.Root, tc.selectedOff)
			initLedgerForWorkingDir(t)

			out := runPrimeJSON(t)

			var parsed struct {
				Attribution config.ResolvedAttribution `json:"attribution"`
				Session     *sessionStatus             `json:"session"`
			}
			require.NoError(t, json.Unmarshal([]byte(out), &parsed), "prime must emit valid JSON: %s", out)
			require.NotNil(t, parsed.Session, "prime must report the session: %s", out)
			require.True(t, parsed.Session.Recording, "fixture: prime must start a recording in the selected repo: %s", out)

			if tc.selectedOff {
				assert.Empty(t, parsed.Attribution.Session, "prime must report the selected repo's attribution")
				assert.Empty(t, parsed.Session.SessionURL, "session attribution is off in the selected repo: no session URL")
				assert.Empty(t, parsed.Session.PRDirective, "session attribution is off in the selected repo: no PR directive")
				return
			}
			assert.NotEmpty(t, parsed.Attribution.Session, "prime must report the selected repo's attribution")
			assert.Contains(t, parsed.Session.SessionURL, "/c/ses_", "session attribution is on in the selected repo: prime must give the session URL")
			assert.Contains(t, parsed.Session.PRDirective, "SageOx-Session: "+parsed.Session.SessionURL)
		})
	}
}

// initLedgerForWorkingDir creates a Ledger checkout where the repo in the
// working directory keeps one.
func initLedgerForWorkingDir(t *testing.T) {
	t.Helper()
	ledgerPath, err := ledger.DefaultPath()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(ledgerPath, 0o755))
	gitInitRepo(t, ledgerPath)
}

// runPrimeJSON runs `ox agent prime --agent claude-code --format json`.
func runPrimeJSON(t *testing.T) string {
	t.Helper()
	var buf bytes.Buffer
	cmd := agentPrimeCmd
	cmd.SetOut(&buf)
	cmd.SetErr(&bytes.Buffer{})
	require.NoError(t, cmd.Flags().Set("agent", "claude-code"))
	require.NoError(t, cmd.Flags().Set("format", "json"))
	t.Cleanup(func() {
		_ = cmd.Flags().Set("agent", "")
		_ = cmd.Flags().Set("format", "")
		cmd.SetOut(nil)
		cmd.SetErr(nil)
	})
	require.NoError(t, runAgentPrime(cmd, nil))
	return buf.String()
}

// TestPlanSessionLink_ReadsTheRepoItIsGiven covers the /c/ link stamped into
// rendered plans. `ox plan render` derives its repo from the working directory,
// so the two agree today; this pins that planSessionLink decides for the repo it
// is handed, and does not silently diverge from its own config and recording
// lookups when a caller passes another one.
func TestPlanSessionLink_ReadsTheRepoItIsGiven(t *testing.T) {
	for _, tc := range attributionMismatchCases {
		t.Run(tc.name, func(t *testing.T) {
			root := newPlanCaptureTestRepo(t)
			t.Setenv("SAGEOX_AGENT_ID", "")
			rec := startFakeRecording(t, root, session.RecordingState{
				AgentID:   "Oxplnroot",
				SessionID: "ses_01920000-0000-7000-8000-0000000000e2",
			})
			runFromOppositeRepo(t, root, tc.selectedOff)

			id, _ := planSessionLink(root)

			if tc.selectedOff {
				assert.Empty(t, id, "session attribution is off in the given repo: no link is expected in the render")
				return
			}
			assert.Equal(t, rec.SessionID, id, "session attribution is on in the given repo: the render must link the session")
		})
	}
}

// TestAutoSessionURL_ReadsTheRepoItIsGiven is the PR-header counterpart of the
// plan case above: `ox pr header` also derives its repo from the working
// directory, and autoSessionURL must decide for the repo it is handed.
func TestAutoSessionURL_ReadsTheRepoItIsGiven(t *testing.T) {
	for _, tc := range attributionMismatchCases {
		t.Run(tc.name, func(t *testing.T) {
			root := prHeaderProject(t, true)
			const sesID = "ses_01920000-0000-7000-8000-0000000000e1"
			startFakeRecording(t, root, session.RecordingState{
				SessionPath:                filepath.Join(root, "sessions", "2026-08-20T10-00-devon-Oxprrt1"),
				SessionID:                  sesID,
				LifecycleRegistrationState: "deferred",
			})
			t.Setenv(config.EnvSessionPublishing, config.SessionPublishingAuto)
			runFromOppositeRepo(t, root, tc.selectedOff)

			url, unconfirmed := autoSessionURL(root, false)

			assert.False(t, unconfirmed, "a deferred session is not withheld as unconfirmed")
			if tc.selectedOff {
				assert.Empty(t, url, "session attribution is off in the given repo: nothing to link")
				return
			}
			assert.Equal(t, "https://sageox.ai/c/"+sesID, url, "session attribution is on in the given repo: link the session")
		})
	}
}
