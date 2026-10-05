package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/gitserver"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Failure prevented: a revoked team token sends CI to a personal login flow
// that cannot replace the token or repair the Git credential it minted.
func TestTeamTokenAuthFailuresNameRotation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)
	setupAuthRenderEnv(t, srv.URL, validTeamToken)

	for _, guidance := range []string{
		checkAuthentication().detail,
		refreshGitCredentials("expired").detail,
		statusExitError(false, false, nil).Error(),
		hydrateHint(fmt.Errorf("HTTP 401")).Error(),
	} {
		assert.Contains(t, guidance, "Rotate or re-mint")
		assert.NotContains(t, guidance, "ox login")
	}
}

// Failure prevented: status tells CI to rotate its bearer after Git rejects a
// locally fresh PAT, instead of forcing an API refresh and probing the new PAT.
func TestStatusRepairsRejectedPATs(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real Git liveness probes and CGI backend")
	}
	if runtime.GOOS == "windows" {
		t.Skip("CGI git-http-backend fixture requires Unix process semantics")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	for _, surface := range []string{"human", "json"} {
		for _, outcome := range []string{"repaired", "bearer rejected", "replacement rejected"} {
			t.Run(surface+"/"+outcome, func(t *testing.T) {
				root := t.TempDir()
				t.Chdir(root)
				t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
				t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
				hostedTestGit(t, root, "init", "--bare", filepath.Join(root, "ledger.git"))
				backend := &cgi.Handler{
					Path: filepath.Join(hostedTestGit(t, root, "--exec-path"), "git-http-backend"),
					Env:  []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1"},
				}
				var oldProbes, freshProbes atomic.Int32
				gitServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_, token, _ := r.BasicAuth()
					switch token {
					case "old-pat":
						if strings.HasSuffix(r.URL.Path, "/info/refs") {
							oldProbes.Add(1)
						}
					case "fresh-pat":
						if strings.HasSuffix(r.URL.Path, "/info/refs") {
							freshProbes.Add(1)
						}
						if outcome == "repaired" {
							backend.ServeHTTP(w, r)
							return
						}
					}
					w.Header().Set("WWW-Authenticate", `Basic realm="ledger"`)
					w.WriteHeader(http.StatusUnauthorized)
				}))
				t.Cleanup(gitServer.Close)
				var refreshCalls atomic.Int32
				apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					switch r.URL.Path {
					case auth.IntrospectEndpoint:
						_, _ = w.Write([]byte(`{"active":true,"principal_kind":"team-service","team":{"team_id":"team_test"}}`))
					case "/api/v1/cli/repos":
						refreshCalls.Add(1)
						assert.Equal(t, "Bearer "+validTeamToken, r.Header.Get("Authorization"))
						if outcome == "bearer rejected" {
							w.WriteHeader(http.StatusUnauthorized)
							return
						}
						_ = json.NewEncoder(w).Encode(api.ReposResponse{
							Token: "fresh-pat", ServerURL: gitServer.URL, ExpiresAt: time.Now().Add(24 * time.Hour),
							Repos: map[string]api.RepoInfo{"ledger": {Name: "ledger", Type: "team-context", TeamID: "team_test", URL: gitServer.URL + "/ledger.git"}},
						})
					default:
						t.Errorf("unexpected API request %s", r.URL.Path)
						w.WriteHeader(http.StatusNotFound)
					}
				}))
				t.Cleanup(apiServer.Close)
				setupAuthRenderEnv(t, apiServer.URL, validTeamToken)
				require.NoError(t, gitserver.SaveCredentialsForEndpoint(apiServer.URL, gitserver.GitCredentials{
					Token: "old-pat", ServerURL: gitServer.URL, ExpiresAt: time.Now().Add(24 * time.Hour),
					BearerTokenHash: gitserver.BearerTokenFingerprint(validTeamToken),
					Repos:           map[string]gitserver.RepoEntry{"team_test": {Name: "ledger", Type: "team-context", URL: gitServer.URL + "/ledger.git"}},
				}))
				_, err := auth.RefreshGitCredentialsForEndpoint(apiServer.URL, false)
				require.NoError(t, err)
				require.Zero(t, refreshCalls.Load(), "the fresh matching cache must initially skip refresh")
				var guidance string
				if surface == "human" {
					guidance = renderAuthStatus("/tmp/auth.json")
					if outcome == "repaired" {
						assert.Contains(t, guidance, "✓ valid")
						assert.NotContains(t, guidance, "PAT rejected")
					}
				} else {
					token, err := auth.GetTokenForEndpoint(apiServer.URL)
					require.NoError(t, err)
					output := buildStatusJSON(true, nil, token, endpoint.NormalizeSlug(apiServer.URL), "/tmp/auth.json", false,
						root, root, "", false, nil, root, nil, nil, nil, nil, statusBubblesSummary{})
					require.NotNil(t, output.Auth.GitPATValid, "JSON must load credentials by the full endpoint, including port")
					assert.Equal(t, outcome == "repaired", *output.Auth.GitPATValid)
					guidance = output.Auth.GitPATReason
				}
				require.EqualValues(t, 1, refreshCalls.Load(), "a Git rejection must force exactly one refresh")
				require.EqualValues(t, 1, oldProbes.Load(), "the old PAT must be probed once")
				if outcome == "bearer rejected" {
					assert.Zero(t, freshProbes.Load(), "a failed refresh must not probe the cached PAT again")
				} else {
					assert.EqualValues(t, 1, freshProbes.Load(), "the replacement PAT must be probed once")
				}
				if outcome != "repaired" {
					assert.Contains(t, guidance, "Rotate or re-mint")
					assert.NotContains(t, guidance, "ox login")
				}
			})
		}
	}
}
