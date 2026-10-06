package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
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

// Failure prevented: Git keeps a rejected PAT or releases cached credentials after bearer revocation.
func TestGitCredentialHelper_RotationAndRejectedPAT(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer oxt_rotated_1lKvCA" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(api.ReposResponse{Token: fmt.Sprintf("replacement-pat-%d", count), ExpiresAt: time.Now().Add(24 * time.Hour)})
	}))
	t.Cleanup(srv.Close)
	setupAuthRenderEnv(t, srv.URL, validTeamToken)
	require.NoError(t, gitserver.SaveCredentialsForEndpoint(srv.URL, gitserver.GitCredentials{Token: "old-pat", ExpiresAt: time.Now().Add(24 * time.Hour), BearerTokenHash: gitserver.BearerTokenFingerprint(validTeamToken)}))
	request := "protocol=http\nhost=" + strings.TrimPrefix(srv.URL, "http://") + "\n\n"
	for _, step := range []struct {
		operation, bearer, password string
		calls                       int32
	}{
		{"get", validTeamToken, "old-pat", 0},
		{"get", "oxt_rotated_1lKvCA", "replacement-pat-1", 1},
		{"erase", "oxt_rotated_1lKvCA", "", 2},
		{"get", "oxt_rotated_1lKvCA", "replacement-pat-2", 2},
		{"get", validTeamToken, "", 3},
		{"get", "", "replacement-pat-2", 3},
	} {
		t.Setenv("SAGEOX_TOKEN", step.bearer)
		var out bytes.Buffer
		cmd := &cobra.Command{}
		cmd.SetIn(strings.NewReader(request))
		cmd.SetOut(&out)
		require.NoError(t, runGitCredentialHelper(cmd, []string{step.operation}))
		if step.password == "" {
			require.Empty(t, out.String())
		} else {
			require.Equal(t, "username=oauth2\npassword="+step.password+"\n\n", out.String())
		}
		require.Equal(t, step.calls, calls.Load())
	}
	creds, err := gitserver.LoadCredentialsForEndpoint(srv.URL)
	require.NoError(t, err)
	require.Equal(t, "replacement-pat-2", creds.Token)
}

// Failure prevented: canceling the CLI leaves a constructor or rejected-PAT refresh running.
func TestLFSCLICancellationDuringBearerRotation(t *testing.T) {
	if testing.Short() {
		t.Skip("real Git repos")
	}
	for _, operation := range []string{"fetch", "hydrate", "upload", "migrate"} {
		t.Run(operation, func(t *testing.T) {
			payload := []byte("{\"type\":\"user\",\"content\":\"hello\",\"ts\":\"2026-03-11T10:00:00Z\",\"seq\":1}\n")
			oid := fmt.Sprintf("%x", sha256.Sum256(payload))
			started, release := make(chan struct{}, 1), make(chan struct{})
			var batches atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v1/cli/repos" {
					started <- struct{}{}
					select {
					case <-r.Context().Done():
					case <-release:
					}
				} else {
					batches.Add(1)
				}
				w.WriteHeader(http.StatusUnauthorized)
			}))
			t.Cleanup(func() { close(release); srv.Close() })
			setupAuthRenderEnv(t, srv.URL, validTeamToken)
			root := createInitializedProjectWithConfig(t, nil)
			hostedTestGit(t, root, "init")
			t.Chdir(root)
			ledger := filepath.Join(root, "ledger")
			hostedTestGit(t, root, "init", ledger)
			hostedTestGit(t, ledger, "remote", "add", "origin", srv.URL+"/ledger.git")
			require.NoError(t, config.SaveLocalConfig(root, &config.LocalConfig{Ledger: &config.LedgerConfig{Path: ledger}}))
			dir := filepath.Join(ledger, "sessions", "fixture")
			raw := writeTestPointerFile(t, dir, "raw.jsonl", oid, int64(len(payload)))
			if operation == "upload" {
				require.NoError(t, os.WriteFile(raw, payload, 0o600))
			}
			before, err := os.ReadFile(raw)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(dir, "summary.md"), payload, 0o600))
			meta := lfs.NewSessionMeta("fixture", "test", "Oxctx", "claude-code", time.Now()).Build()
			if operation != "upload" {
				meta.Files = map[string]lfs.FileRef{"raw.jsonl": {OID: "sha256:" + oid, Size: int64(len(payload))}}
			}
			require.NoError(t, lfs.WriteSessionMetaOnly(dir, meta))
			bearer, wantBatches := validTeamToken, int32(1)
			if operation == "fetch" {
				bearer, wantBatches = "previous-bearer", 0
			}
			cached := gitserver.GitCredentials{Token: "old-pat", Username: "git-user", ExpiresAt: time.Now().Add(24 * time.Hour), BearerTokenHash: gitserver.BearerTokenFingerprint(bearer)}
			require.NoError(t, gitserver.SaveCredentialsForEndpoint(srv.URL, cached))
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			cmd := &cobra.Command{}
			cmd.SetContext(ctx)
			cmd.SetOut(io.Discard)
			done := make(chan error, 1)
			go func() {
				switch operation {
				case "fetch":
					done <- runFetch(cmd, []string{raw})
				case "hydrate":
					done <- sessionHydrateCmd.RunE(cmd, []string{"fixture"})
				case "upload":
					done <- sessionUploadCmd.RunE(cmd, []string{"fixture"})
				default:
					done <- migrateSessionToLFS(ctx, root, ledger, dir, "fixture")
				}
			}()
			select {
			case <-started:
			case <-time.After(2 * time.Second):
				t.Fatal("credential refresh did not start")
			}
			cancel()
			select {
			case err := <-done:
				require.ErrorIs(t, err, context.Canceled)
			case <-time.After(time.Second):
				t.Fatal("refresh ignored cancellation")
			}
			assert.Equal(t, wantBatches, batches.Load())
			stored, err := gitserver.LoadCredentialsForEndpoint(srv.URL)
			require.NoError(t, err)
			assert.Equal(t, cached.Token, stored.Token)
			assert.Equal(t, cached.BearerTokenHash, stored.BearerTokenHash)
			after, err := os.ReadFile(raw)
			require.NoError(t, err)
			assert.Equal(t, before, after)
			assert.NoFileExists(t, filepath.Join(ledger, ".sageox", "cache", "sessions", "fixture", "raw.jsonl"))
		})
	}
}

// Failure prevented: status accepts a foreign PAT or fails to refresh a rejected one.
func TestStatusRepairsRejectedPATs(t *testing.T) {
	if testing.Short() || runtime.GOOS == "windows" {
		t.Skip("CGI Git backend")
	}
	for _, surface := range []string{"human", "json"} {
		for _, outcome := range []string{"repaired", "rejected", "rotated"} {
			t.Run(surface+"/"+outcome, func(t *testing.T) {
				root := t.TempDir()
				t.Chdir(root)
				t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
				t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
				hostedTestGit(t, root, "init", "--bare", filepath.Join(root, "ledger.git"))
				backend := &cgi.Handler{Path: filepath.Join(hostedTestGit(t, root, "--exec-path"), "git-http-backend"), Env: []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1"}}
				var refreshes, oldProbes, freshProbes atomic.Int32
				var srv *httptest.Server
				srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case auth.IntrospectEndpoint:
						_, _ = w.Write([]byte(`{"active":true,"principal_kind":"team-service","team":{"team_id":"team_test"}}`))
					case "/api/v1/cli/repos":
						refreshes.Add(1)
						if outcome != "repaired" {
							w.WriteHeader(http.StatusUnauthorized)
							return
						}
						_ = json.NewEncoder(w).Encode(api.ReposResponse{Token: "fresh-pat", ExpiresAt: time.Now().Add(24 * time.Hour), Repos: map[string]api.RepoInfo{"team_test": {Type: "team-context", TeamID: "team_test", URL: srv.URL + "/ledger.git"}}})
					default:
						_, pat, _ := r.BasicAuth()
						if strings.HasSuffix(r.URL.Path, "/info/refs") {
							if pat == "old-pat" {
								oldProbes.Add(1)
							}
							if pat == "fresh-pat" {
								freshProbes.Add(1)
							}
						}
						if (pat == "old-pat" && outcome == "rotated") || pat == "fresh-pat" {
							backend.ServeHTTP(w, r)
							return
						}
						w.Header().Set("WWW-Authenticate", `Basic realm="ledger"`)
						w.WriteHeader(http.StatusUnauthorized)
					}
				}))
				t.Cleanup(srv.Close)
				setupAuthRenderEnv(t, srv.URL, validTeamToken)
				bearer := validTeamToken
				if outcome == "rotated" {
					bearer = "previous-bearer"
				}
				require.NoError(t, gitserver.SaveCredentialsForEndpoint(srv.URL, gitserver.GitCredentials{Token: "old-pat", ExpiresAt: time.Now().Add(24 * time.Hour), BearerTokenHash: gitserver.BearerTokenFingerprint(bearer), Repos: map[string]gitserver.RepoEntry{"team_test": {URL: srv.URL + "/ledger.git"}}}))
				if outcome == "rotated" {
					_, err := auth.RefreshGitCredentialsForEndpoint(context.Background(), srv.URL, false)
					require.ErrorIs(t, err, api.ErrUnauthorized)
				}
				guidance := ""
				if surface == "human" {
					guidance = renderAuthStatus("/tmp/auth.json")
					assert.Equal(t, outcome == "repaired", strings.Contains(guidance, "✓ valid"))
				} else {
					token, err := auth.GetTokenForEndpoint(srv.URL)
					require.NoError(t, err)
					output := buildStatusJSON(true, nil, token, endpoint.NormalizeSlug(srv.URL), "/tmp/auth.json", false, root, root, "", false, nil, root, nil, nil, nil, nil, statusBubblesSummary{})
					require.NotNil(t, output.Auth.GitPATValid)
					assert.Equal(t, outcome == "repaired", *output.Auth.GitPATValid)
					guidance = output.Auth.GitPATReason
				}
				assert.EqualValues(t, 1, refreshes.Load())
				if outcome == "rotated" {
					assert.Zero(t, oldProbes.Load())
					assert.Contains(t, guidance, "unverified")
				} else {
					assert.EqualValues(t, 1, oldProbes.Load())
				}
				if outcome == "repaired" {
					assert.EqualValues(t, 1, freshProbes.Load())
				} else {
					assert.Zero(t, freshProbes.Load())
				}
				if outcome == "rejected" {
					assert.Contains(t, guidance, "Rotate or re-mint")
					assert.NotContains(t, guidance, "ox login")
				}
			})
		}
	}
}
