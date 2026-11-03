package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/flags"
	"github.com/sageox/ox/internal/gitserver"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	projectAPI = "https://api.northwind.example"
	foreignAPI = "https://collector.attacker.example"
)

// projectWithEndpoint is a repository whose committed config names projectAPI.
func projectWithEndpoint(t *testing.T) string {
	t.Helper()
	t.Setenv(endpoint.EnvVar, "")
	t.Setenv("SAGEOX_TOKEN", "")
	root := t.TempDir()
	require.NoError(t, config.SaveProjectConfig(root, &config.ProjectConfig{Endpoint: projectAPI}))
	require.Equal(t, projectAPI, endpoint.GetForProject(root))
	return root
}

func heartbeatWithTokenFrom(t *testing.T, source string) *HeartbeatHandler {
	t.Helper()
	h := NewHeartbeatHandler(testLogger())
	h.Handle("", mustMarshal(HeartbeatPayload{Credentials: &HeartbeatCreds{
		Token: "git-pat", ExpiresAt: time.Now().Add(time.Hour),
		AuthToken: "oauth-token", AuthEndpoint: source,
	}}))
	return h
}

// The kb lister is built for the project's endpoint; a heartbeat token loaded
// for any other origin must not be attached to it.
func TestBuildKBLister_AttachesHeartbeatTokenOnlyForItsOrigin(t *testing.T) {
	for _, tt := range []struct {
		source, want string
	}{{projectAPI, "oauth-token"}, {foreignAPI, ""}, {"", ""}} {
		t.Run("source="+tt.source, func(t *testing.T) {
			s, _ := newRetractScheduler(t)
			s.config.ProjectRoot = projectWithEndpoint(t)
			s.SetAuthTokenGetter(heartbeatWithTokenFrom(t, tt.source).GetAuthToken)
			var gotEndpoint, gotToken string
			s.SetKBBubbleListerFactory(func(ep, token string) KBBubbleLister {
				gotEndpoint, gotToken = ep, token
				return nil
			})

			s.buildKBLister()

			assert.Equal(t, projectAPI, gotEndpoint)
			assert.Equal(t, tt.want, gotToken)
		})
	}
}

func TestProjectGitHubMirrorRelayer_HeartbeatTokenOnlyForProjectOrigin(t *testing.T) {
	noSettings := func() *flags.CLISettingsResponse { return nil }
	for _, tt := range []struct {
		source, want string
	}{{projectAPI, "oauth-token"}, {foreignAPI, ""}} {
		t.Run("source="+tt.source, func(t *testing.T) {
			isolateCredentialsWithDir(t)
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			root := projectWithEndpoint(t)
			r := newProjectGitHubMirrorRelayer(root, noSettings, heartbeatWithTokenFrom(t, tt.source).GetAuthToken, testLogger())

			assert.Equal(t, tt.want, r.deps.AuthToken())
			assert.NotNil(t, r.deps.NewRelay(tt.want))
		})
	}
}

// Each mirror cycle follows the project's current endpoint, and the token and
// relay client of one cycle agree on it.
func TestProjectGitHubMirrorRelayer_FollowsProjectEndpointEditsBetweenCycles(t *testing.T) {
	const movedAPI = "https://api.moved.example"
	noSettings := func() *flags.CLISettingsResponse { return nil }
	isolateCredentialsWithDir(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := projectWithEndpoint(t)
	r := newProjectGitHubMirrorRelayer(root, noSettings, heartbeatWithTokenFrom(t, movedAPI).GetAuthToken, testLogger())

	assert.Empty(t, r.deps.AuthToken(), "token loaded for the moved endpoint must not reach the original one")
	assert.Equal(t, projectAPI, r.deps.NewRelay("").(*api.RepoClient).Endpoint())

	require.NoError(t, config.SaveProjectConfig(root, &config.ProjectConfig{Endpoint: movedAPI}))

	assert.Equal(t, "oauth-token", r.deps.AuthToken())
	assert.Equal(t, movedAPI, r.deps.NewRelay("oauth-token").(*api.RepoClient).Endpoint())
}

// Cold start loads the stored OAuth token for the project endpoint; it must
// stay usable for that endpoint and for nothing else.
func TestInitComponents_ColdStartTokenIsBoundToProjectEndpoint(t *testing.T) {
	onNewMachine(t)
	isolateCredentialsWithDir(t)
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home+"/config")
	t.Setenv("XDG_DATA_HOME", home+"/data")
	t.Setenv("XDG_CACHE_HOME", home+"/cache")
	t.Setenv("OX_USER_CONFIG", home+"/user-config.yaml")
	root := projectWithEndpoint(t)
	require.NoError(t, auth.SaveTokenForEndpoint(projectAPI, &auth.StoredToken{
		AccessToken: "oauth-token", ExpiresAt: time.Now().Add(time.Hour),
	}))
	require.NoError(t, gitserver.SaveCredentialsForEndpoint(projectAPI, gitserver.GitCredentials{
		Token: "git-pat", ServerURL: "https://git.northwind.example", ExpiresAt: time.Now().Add(time.Hour),
	}))

	cfg := DefaultConfig()
	cfg.ProjectRoot = root
	cfg.LedgerPath = t.TempDir()
	d := New(cfg, discardLogger())
	d.ctx, d.cancel = context.WithCancel(context.Background())
	t.Cleanup(func() {
		d.cancel()
		d.releaseGlobalSyncLease()
	})
	d.initComponents()

	assert.Equal(t, "oauth-token", d.heartbeat.GetAuthToken(projectAPI))
	assert.Empty(t, d.heartbeat.GetAuthToken(foreignAPI))
}
