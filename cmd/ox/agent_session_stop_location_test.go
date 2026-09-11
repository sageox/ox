package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/sageox/ox/internal/agentinstance"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/session/adapters"
	"github.com/sageox/ox/internal/session/pipeline"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stopLocationTestFixture is a started codex recording, ready to stop, in an
// isolated project bound to a known team. sessionPublishing selects
// config.SessionPublishingAuto or config.SessionPublishingManual so both
// callers of sessionStopLocation's gating can be exercised.
type stopLocationTestFixture struct {
	projectRoot string
	inst        *agentinstance.Instance
	apiServer   *httptest.Server
}

func setupStopLocationTest(t *testing.T, sessionPublishing string) stopLocationTestFixture {
	t.Helper()

	adapters.Register(&testCodexAdapter{})
	t.Cleanup(func() { adapters.Unregister("codex") })

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	t.Cleanup(apiServer.Close)

	t.Setenv("SAGEOX_ENDPOINT", apiServer.URL)
	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SAGEOX_DAEMON", "false")

	projectRoot := createInitializedProjectWithConfig(t, &config.ProjectConfig{
		RepoID:            "test-repo-stop-location",
		Endpoint:          apiServer.URL,
		ProjectID:         "test-project",
		TeamID:            "team_test123",
		TeamName:          "Acme Corp",
		SessionPublishing: sessionPublishing,
	})

	// sessionLinkOutputs' manual-publishing gate resolves the mode via
	// findGitRoot() (git rev-parse --show-toplevel), not via a passed-in
	// root — so this fixture needs a REAL git repo, not just a .sageox/
	// directory, or that gate silently no-ops.
	runGit(t, projectRoot, "init")
	runGit(t, projectRoot, "config", "user.name", "ox-test")
	runGit(t, projectRoot, "config", "user.email", "test@test.sageox.ai")
	runGit(t, projectRoot, "commit", "--allow-empty", "-m", "initial")

	origCwd, err := os.Getwd()
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.Chdir(origCwd) })
	require.NoError(t, os.Chdir(projectRoot))
	cwd, err := os.Getwd()
	require.NoError(t, err)

	require.NoError(t, auth.SaveToken(&auth.StoredToken{
		AccessToken: "test-access-token",
		TokenType:   "Bearer",
		ExpiresAt:   time.Now().Add(2 * time.Hour),
		UserInfo: auth.UserInfo{
			UserID: "test-user-id",
			Email:  "test@example.com",
			Name:   "Test User",
		},
	}))

	// runAgentSessionStart/Stop read output mode from global cfg, which is
	// normally initialized by Cobra PersistentPreRun in CLI execution. Set a
	// default here for the start call below; callers reassign cfg afterward
	// to select the stop-output format they want to assert on.
	oldGlobalCfg := cfg
	cfg = &config.Config{}
	t.Cleanup(func() { cfg = oldGlobalCfg })

	sourcePath := writeCodexSessionFile(t, os.Getenv("HOME"), cwd)
	require.FileExists(t, sourcePath)

	inst := &agentinstance.Instance{
		AgentID:         "OxStopLoc",
		ServerSessionID: "oxsid_test_stop_location",
		CreatedAt:       time.Now(),
		ExpiresAt:       time.Now().Add(24 * time.Hour),
		AgentType:       "codex",
	}
	store, err := getInstanceStore(projectRoot)
	require.NoError(t, err)
	require.NoError(t, store.Add(inst))
	require.NoError(t, runAgentSessionStart(inst, nil))

	appendCodexMessage(t, sourcePath, "user", "hello from the stop-location test")

	return stopLocationTestFixture{projectRoot: projectRoot, inst: inst, apiServer: apiServer}
}

// TestSessionStop_SurfacesTeamAndURL is the red-first proof for fix G
// (contract D8): the real-world failure this closes is a coworker who
// recorded a session successfully, then could not find it on the site
// because the browser was on the wrong team — and nothing the CLI printed
// at stop could have told them. Before this fix, outputSessionStopJSON
// carried no session_url/team_id/team_name/repo_id at all.
//
// SessionPublishing is deliberately "auto" (not "manual") here — manual-mode
// sessions never register with the server, so their session_url is
// correctly suppressed (see TestSessionStop_ManualPublishing_OmitsViewAtURL
// below). This test covers the ordinary, NORMALLY-published session that
// the reported bug actually was: the URL must be populated.
func TestSessionStop_SurfacesTeamAndURL(t *testing.T) {
	fx := setupStopLocationTest(t, config.SessionPublishingAuto)

	// runAgentSessionStart/Stop read output mode from global cfg, which is
	// normally initialized by Cobra PersistentPreRun in CLI execution.
	// Leaving Text/Review false selects the default JSON stop output.
	oldGlobalCfg := cfg
	cfg = &config.Config{}
	t.Cleanup(func() { cfg = oldGlobalCfg })

	stdout := captureRealStdout(t, func() {
		require.NoError(t, runAgentSessionStop(fx.inst))
	})

	var parsed pipeline.StopOutput
	require.NoError(t, json.Unmarshal(stdout, &parsed), "stop output must be valid JSON: %s", stdout)

	assert.Equal(t, "Acme Corp", parsed.TeamName, "team_name must surface so a coworker can tell which team a session landed on")
	assert.Equal(t, "team_test123", parsed.TeamID)
	assert.Equal(t, "test-repo-stop-location", parsed.RepoID)
	assert.NotEmpty(t, parsed.SessionURL, "session_url must be populated when attribution is on and registration isn't pending")
	assert.Contains(t, parsed.SessionURL, fx.apiServer.URL+"/c/", "session_url must be the durable /c/<ses_id> link")
}

// TestSessionStop_ManualPublishing_OmitsViewAtURL proves the coordinator's
// fix-G addendum: session_publishing=manual now suppresses ALL publishing
// (transcript, draft, and the /started notification — see contract D12), so
// a /c/<id> link would 404 because the server never heard of the session.
// sessionLinkOutputs (cmd/ox/session_url.go) already returns "" for
// SessionURL in this mode; this test proves the stop-output text summary
// handles that gracefully — "Recorded to" still prints (team binding is
// known purely from local config, not from registration) but "View at"
// must be omitted entirely, never a bare label or a dead link.
//
// This is a distinct cause from the "registration pending" case D8 already
// covered — manual mode never even attempts registration — so it gets its
// own test rather than being folded into that one.
func TestSessionStop_ManualPublishing_OmitsViewAtURL(t *testing.T) {
	fx := setupStopLocationTest(t, config.SessionPublishingManual)

	oldGlobalCfg := cfg
	cfg = &config.Config{Text: true} // human text output, to check literal printed lines
	t.Cleanup(func() { cfg = oldGlobalCfg })

	stdout := captureRealStdout(t, func() {
		require.NoError(t, runAgentSessionStop(fx.inst))
	})

	text := string(stdout)
	assert.Contains(t, text, "Recorded to", "team binding is known locally and must still be shown")
	assert.Contains(t, text, "Acme Corp", "team name must appear on the Recorded to line")
	assert.NotContains(t, text, "View at", "manual mode never registers with the server, so no URL — dead or otherwise — may be printed")
}
