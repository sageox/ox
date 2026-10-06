package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
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
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/gitserver"
	"github.com/sageox/ox/internal/lfs"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Failure prevented: a revoked team token sends CI to a personal login flow
// that cannot replace the token or repair the Git credential it minted.
func TestTeamTokenAuthFailuresNameRotation(t *testing.T) {
	for _, tc := range []struct {
		name, token, remedy, expiryMessage string
		expiresIn                          time.Duration
	}{
		{"revoked team token", validTeamToken, "Rotate or re-mint", "expiring in", 30 * time.Minute},
		{"malformed team token", "oxt_truncated", "Rotate or re-mint", "expired", -30 * time.Minute},
		{"missing bearer", "", "ox login", "expired", -30 * time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				w.WriteHeader(http.StatusUnauthorized)
			}))
			t.Cleanup(srv.Close)
			setupAuthRenderEnv(t, srv.URL, tc.token)
			t.Setenv("XDG_DATA_HOME", t.TempDir())
			root := createInitializedProjectWithConfig(t, nil)
			hostedTestGit(t, root, "init")
			t.Chdir(root)
			cached := gitserver.GitCredentials{
				Token: "old-pat", ExpiresAt: time.Now().Add(tc.expiresIn),
				BearerTokenHash: gitserver.BearerTokenFingerprint(validTeamToken),
			}
			require.NoError(t, gitserver.SaveCredentialsForEndpoint(srv.URL, cached))
			authCheck := checkAuthentication()
			assert.False(t, authCheck.passed)
			refresh := refreshGitCredentials("expired")
			assert.True(t, refresh.warning, "failed refresh must not report repaired credentials")
			freshness := checkGitCredentialsFreshness(false)
			assert.True(t, freshness.warning)
			assert.Contains(t, freshness.message, tc.expiryMessage)
			localCfg := &config.LocalConfig{}
			missing := fixMissingRepos(root, localCfg)
			assert.False(t, missing.passed, "unusable bearer must not configure repos")
			missingPath := filepath.Join(root, "missing-ledger")
			paths := fixRepoPathIssues(root, localCfg, []repoPathIssue{{repoType: "ledger", path: missingPath, issue: "missing"}})
			assert.True(t, paths.warning)
			assert.NoDirExists(t, missingPath, "failed authentication must not start a clone")
			assert.Equal(t, &config.LocalConfig{}, localCfg)
			for _, guidance := range []string{
				authCheck.detail, refresh.detail, freshness.detail, paths.detail,
				statusExitError(false, false, nil).Error(), hydrateHint(fmt.Errorf("HTTP 401")).Error(),
			} {
				assert.Contains(t, guidance, tc.remedy)
				if tc.token != "" {
					assert.NotContains(t, guidance, "ox login")
				}
			}
			if tc.token != validTeamToken {
				assert.Zero(t, requests.Load(), "missing or malformed bearers must fail before contacting the API")
			}
			preserved, err := gitserver.LoadCredentialsForEndpoint(srv.URL)
			require.NoError(t, err)
			require.NotNil(t, preserved)
			assert.Equal(t, cached.Token, preserved.Token)
			assert.Equal(t, cached.BearerTokenHash, preserved.BearerTokenHash)
		})
	}
}

// Canceling a CLI operation must also cancel the PAT refresh caused by bearer
// rotation, before downloading or replacing any session content.
func TestLFSCLICancellationDuringBearerRotation(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real Git repos and credential refresh requests")
	}
	for _, phase := range []struct {
		name, cachedBearer              string
		initialBatches, recoveryBatches int32
	}{
		{"constructor", "previous-bearer", 0, 1},
		{"after401", validTeamToken, 1, 3},
	} {
		for _, operation := range []string{"fetch", "hydrate", "upload", "migrate"} {
			t.Run(phase.name+"/"+operation, func(t *testing.T) {
				payload := []byte("{\"type\":\"user\",\"content\":\"hello\",\"ts\":\"2026-03-11T10:00:00Z\",\"seq\":1}\n")
				oid := fmt.Sprintf("%x", sha256.Sum256(payload))
				started, release := make(chan struct{}, 1), make(chan struct{})
				var refreshCalls, batchCalls atomic.Int32
				var blockRefresh atomic.Bool
				blockRefresh.Store(true)
				var srv *httptest.Server
				srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case "/api/v1/cli/repos":
						refreshCalls.Add(1)
						assert.Equal(t, "Bearer "+validTeamToken, r.Header.Get("Authorization"))
						if blockRefresh.Load() {
							started <- struct{}{}
							select {
							case <-r.Context().Done():
							case <-release:
								w.WriteHeader(http.StatusUnauthorized)
							}
							return
						}
						_ = json.NewEncoder(w).Encode(api.ReposResponse{
							Token: "fresh-pat", Username: "git-user", ServerURL: srv.URL, ExpiresAt: time.Now().Add(24 * time.Hour),
						})
					case "/ledger.git/info/lfs/objects/batch":
						batchCalls.Add(1)
						username, token, _ := r.BasicAuth()
						assert.Equal(t, "git-user", username)
						if token == "old-pat" {
							w.WriteHeader(http.StatusUnauthorized)
							return
						}
						assert.Equal(t, "fresh-pat", token)
						_ = json.NewEncoder(w).Encode(lfs.BatchResponse{Objects: []lfs.BatchResponseObject{{
							OID: oid, Size: int64(len(payload)), Actions: &lfs.Actions{Download: &lfs.Action{Href: srv.URL + "/object"}},
						}}})
					case "/object":
						_, _ = w.Write(payload)
					default:
						t.Errorf("unexpected request %s", r.URL.Path)
						w.WriteHeader(http.StatusNotFound)
					}
				}))
				t.Cleanup(func() { close(release); srv.Close() })
				setupAuthRenderEnv(t, srv.URL, validTeamToken)
				root := createInitializedProjectWithConfig(t, nil)
				hostedTestGit(t, root, "init")
				t.Chdir(root)
				ledgerPath := filepath.Join(root, "ledger")
				hostedTestGit(t, root, "init", ledgerPath)
				hostedTestGit(t, ledgerPath, "remote", "add", "origin", srv.URL+"/ledger.git")
				require.NoError(t, config.SaveLocalConfig(root, &config.LocalConfig{Ledger: &config.LedgerConfig{Path: ledgerPath}}))
				sessionPath := filepath.Join(ledgerPath, "sessions", "fixture")
				pointerPath := writeTestPointerFile(t, sessionPath, "raw.jsonl", oid, int64(len(payload)))
				if operation == "upload" {
					require.NoError(t, os.WriteFile(pointerPath, payload, 0o600))
				}
				rawBefore, err := os.ReadFile(pointerPath)
				require.NoError(t, err)
				summaryPath := filepath.Join(sessionPath, "summary.md")
				require.NoError(t, os.WriteFile(summaryPath, payload, 0o600))
				meta := lfs.NewSessionMeta("fixture", "test", "Oxctx", "claude-code", time.Now()).Build()
				if operation != "upload" {
					meta.Files = map[string]lfs.FileRef{"raw.jsonl": {OID: "sha256:" + oid, Size: int64(len(payload))}}
				}
				require.NoError(t, lfs.WriteSessionMetaOnly(sessionPath, meta))
				cached := gitserver.GitCredentials{
					Token: "old-pat", Username: "git-user", ServerURL: srv.URL, ExpiresAt: time.Now().Add(24 * time.Hour),
					BearerTokenHash: gitserver.BearerTokenFingerprint(phase.cachedBearer),
				}
				require.NoError(t, gitserver.SaveCredentialsForEndpoint(srv.URL, cached))
				output, stdout, verify := fetchOutputFlag, fetchStdoutFlag, fetchVerifyFlag
				fetchOutputFlag, fetchStdoutFlag, fetchVerifyFlag = "", false, true
				t.Cleanup(func() { fetchOutputFlag, fetchStdoutFlag, fetchVerifyFlag = output, stdout, verify })
				run := func(ctx context.Context) error {
					cmd := &cobra.Command{}
					cmd.SetContext(ctx)
					cmd.SetOut(io.Discard)
					switch operation {
					case "fetch":
						return runFetch(cmd, []string{pointerPath})
					case "hydrate":
						return sessionHydrateCmd.RunE(cmd, []string{"fixture"})
					case "upload":
						return sessionUploadCmd.RunE(cmd, []string{"fixture"})
					default:
						return migrateSessionToLFS(ctx, root, ledgerPath, sessionPath, "fixture")
					}
				}
				ctx, cancel := context.WithCancel(context.Background())
				t.Cleanup(cancel)
				done := make(chan error, 1)
				go func() { done <- run(ctx) }()
				select {
				case <-started:
				case <-time.After(2 * time.Second):
					t.Fatal("CLI did not request credentials for the rotated bearer")
				}
				cancel()
				select {
				case err := <-done:
					require.ErrorIs(t, err, context.Canceled)
				case <-time.After(time.Second):
					t.Fatal("CLI cancellation did not stop credential refresh promptly")
				}
				assert.EqualValues(t, 1, refreshCalls.Load())
				assert.Equal(t, phase.initialBatches, batchCalls.Load(), "canceled refresh must not retry or start an LFS batch")
				preserved, err := gitserver.LoadCredentialsForEndpoint(srv.URL)
				require.NoError(t, err)
				require.NotNil(t, preserved)
				assert.Equal(t, cached.Token, preserved.Token)
				assert.Equal(t, cached.BearerTokenHash, preserved.BearerTokenHash)
				rawAfter, err := os.ReadFile(pointerPath)
				require.NoError(t, err)
				assert.Equal(t, rawBefore, rawAfter, "canceled operation must preserve stubs and original session content")
				summaryAfter, err := os.ReadFile(summaryPath)
				require.NoError(t, err)
				assert.Equal(t, payload, summaryAfter, "canceled migration must preserve original content")
				cachePath := filepath.Join(ledgerPath, ".sageox", "cache", "sessions", "fixture", "raw.jsonl")
				assert.NoFileExists(t, cachePath)
				if operation == "fetch" {
					blockRefresh.Store(false)
					require.NoError(t, run(context.Background()), "a new invocation must refresh and fetch successfully")
					assert.EqualValues(t, 2, refreshCalls.Load())
					assert.Equal(t, phase.recoveryBatches, batchCalls.Load())
					content, err := os.ReadFile(cachePath)
					require.NoError(t, err)
					assert.Equal(t, payload, content)
				}
			})
		}
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
		for _, outcome := range []string{"repaired", "bearer rejected", "replacement rejected", "rotation refresh rejected"} {
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
						if outcome == "rotation refresh rejected" {
							backend.ServeHTTP(w, r)
							return
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
						if outcome == "bearer rejected" || outcome == "rotation refresh rejected" {
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
				cachedBearer := validTeamToken
				if outcome == "rotation refresh rejected" {
					cachedBearer = "oxt_rotated_1lKvCA"
				}
				require.NoError(t, gitserver.SaveCredentialsForEndpoint(apiServer.URL, gitserver.GitCredentials{
					Token: "old-pat", ServerURL: gitServer.URL, ExpiresAt: time.Now().Add(24 * time.Hour),
					BearerTokenHash: gitserver.BearerTokenFingerprint(cachedBearer),
					Repos:           map[string]gitserver.RepoEntry{"team_test": {Name: "ledger", Type: "team-context", URL: gitServer.URL + "/ledger.git"}},
				}))
				_, err := auth.RefreshGitCredentialsForEndpoint(context.Background(), apiServer.URL, false)
				if outcome == "rotation refresh rejected" {
					require.ErrorIs(t, err, api.ErrUnauthorized)
				} else {
					require.NoError(t, err)
					require.Zero(t, refreshCalls.Load(), "the fresh matching cache must initially skip refresh")
				}
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
				if outcome == "rotation refresh rejected" {
					require.Zero(t, oldProbes.Load(), "status must not use an old bearer's still-live PAT")
					assert.Contains(t, guidance, "refresh required")
					assert.Contains(t, guidance, "unverified for the current token")
					assert.NotContains(t, guidance, "✓ valid")
				} else {
					require.EqualValues(t, 1, oldProbes.Load(), "the old PAT must be probed once")
				}
				if outcome == "bearer rejected" || outcome == "rotation refresh rejected" {
					assert.Zero(t, freshProbes.Load(), "a failed refresh must not probe the cached PAT again")
				} else {
					assert.EqualValues(t, 1, freshProbes.Load(), "the replacement PAT must be probed once")
				}
				if outcome != "repaired" && outcome != "rotation refresh rejected" {
					assert.Contains(t, guidance, "Rotate or re-mint")
					assert.NotContains(t, guidance, "ox login")
				}
				if surface == "human" && outcome == "bearer rejected" {
					hostedTestGit(t, root, "init")
					requireSageoxDir(t, root)
					require.NoError(t, config.SaveProjectConfig(root, &config.ProjectConfig{}))
					for _, tc := range []struct {
						name              string
						fix, withoutRepo  bool
						refreshes, probes int32
					}{
						{"check only", false, false, 1, 2},
						{"failed repair", true, false, 2, 3},
						{"missing repo URL", true, true, 3, 3},
					} {
						t.Run("doctor/"+tc.name, func(t *testing.T) {
							cached, err := gitserver.LoadCredentialsForEndpoint(apiServer.URL)
							require.NoError(t, err)
							require.NotNil(t, cached)
							if tc.withoutRepo {
								cached.Repos = nil
								require.NoError(t, gitserver.SaveCredentialsForEndpoint(apiServer.URL, *cached))
							}
							result := checkGitPATLiveness(tc.fix)
							assert.False(t, result.passed)
							assert.False(t, result.skipped, "failed repair must remain an actionable failure")
							assert.Contains(t, result.detail, "Rotate or re-mint")
							assert.NotContains(t, result.detail, "ox login")
							assert.Equal(t, tc.refreshes, refreshCalls.Load(), "check-only must not refresh; repair must force one request")
							assert.Equal(t, tc.probes, oldProbes.Load())
							assert.Zero(t, freshProbes.Load())
							preserved, err := gitserver.LoadCredentialsForEndpoint(apiServer.URL)
							require.NoError(t, err)
							require.NotNil(t, preserved)
							assert.Equal(t, cached.Token, preserved.Token)
							assert.Equal(t, cached.BearerTokenHash, preserved.BearerTokenHash)
						})
					}
				}
			})
		}
	}
}
