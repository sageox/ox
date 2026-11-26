package lfs

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/errkind"
	"github.com/sageox/ox/internal/gitserver"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewClient(t *testing.T) {
	c := NewClient("https://git.sageox.io/sageox/ledger.git", "", "test-token")
	assert.Equal(t, "https://git.sageox.io/sageox/ledger.git/info/lfs/objects/batch", c.batchURL)
	assert.Equal(t, "Basic b2F1dGgyOnRlc3QtdG9rZW4=", c.authHeader)
	assert.Equal(t, 2*time.Minute, c.httpClient.Timeout, "batch API timeout should be 2 minutes")

}

func TestNewClient_TrailingSlash(t *testing.T) {
	c := NewClient("https://git.sageox.io/sageox/ledger.git/", "oauth2", "test-token")
	assert.Equal(t, "https://git.sageox.io/sageox/ledger.git/info/lfs/objects/batch", c.batchURL)
}

func TestBatchUpload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "application/vnd.git-lfs+json", r.Header.Get("Content-Type"))
		assert.Contains(t, r.Header.Get("Authorization"), "Basic ")

		var req batchRequest
		err := json.NewDecoder(r.Body).Decode(&req)
		require.NoError(t, err)
		assert.Equal(t, "upload", req.Operation)
		assert.Len(t, req.Objects, 1)
		assert.Equal(t, "abc123", req.Objects[0].OID)

		resp := BatchResponse{
			Transfer: "basic",
			Objects: []BatchResponseObject{
				{
					OID:  "abc123",
					Size: 100,
					Actions: &Actions{
						Upload: &Action{
							Href: "https://storage.example.com/upload/abc123",
							Header: map[string]string{
								"Content-Type": "application/octet-stream",
							},
						},
					},
				},
			},
		}
		w.Header().Set("Content-Type", "application/vnd.git-lfs+json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	c := &Client{
		batchURL:   server.URL,
		httpClient: server.Client(),
		authHeader: "Basic dGVzdDp0b2tlbg==",
	}

	objects := []BatchObject{{OID: "abc123", Size: 100}}
	resp, err := c.BatchUpload(objects)
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Len(t, resp.Objects, 1)
	assert.Equal(t, "abc123", resp.Objects[0].OID)
	assert.NotNil(t, resp.Objects[0].Actions)
	assert.NotNil(t, resp.Objects[0].Actions.Upload)
}

func TestBatchDownload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req batchRequest
		err := json.NewDecoder(r.Body).Decode(&req)
		require.NoError(t, err)
		assert.Equal(t, "download", req.Operation)

		resp := BatchResponse{
			Transfer: "basic",
			Objects: []BatchResponseObject{
				{
					OID:  "def456",
					Size: 200,
					Actions: &Actions{
						Download: &Action{
							Href: "https://storage.example.com/download/def456",
						},
					},
				},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	c := &Client{
		batchURL:   server.URL,
		httpClient: server.Client(),
		authHeader: "Basic dGVzdDp0b2tlbg==",
	}

	objects := []BatchObject{{OID: "def456", Size: 200}}
	resp, err := c.BatchDownload(objects)
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Len(t, resp.Objects, 1)
	assert.NotNil(t, resp.Objects[0].Actions.Download)
}

func TestBatch_ServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal error"))
	}))
	defer server.Close()

	c := &Client{
		batchURL:   server.URL,
		httpClient: server.Client(),
		authHeader: "Basic dGVzdDp0b2tlbg==",
	}

	_, err := c.BatchUpload([]BatchObject{{OID: "abc", Size: 10}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "HTTP 500")
}

// TestBatch_RejectsPlaintextHTTP confirms doBatch refuses to send
// Authorization to a non-loopback http URL. Without this guard, a
// misconfigured or attacker-rewritten remote (http://example.com/...)
// would leak the user's PAT in the clear on the very first request,
// before the per-action trusted-host stamping has any chance to run.
func TestBatch_RejectsPlaintextHTTP(t *testing.T) {
	c := &Client{
		batchURL:   "http://example.com/sageox/ledger.git/info/lfs/objects/batch",
		httpClient: &http.Client{Timeout: 5 * time.Second},
		authHeader: "Basic dGVzdDp0b2tlbg==",
	}
	_, err := c.BatchUpload([]BatchObject{{OID: "abc", Size: 10}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "plaintext http")
}

// TestBatch_AllowsLoopbackHTTP confirms the guard does not break
// httptest-based tests, which always use 127.0.0.1.
func TestBatch_AllowsLoopbackHTTP(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(BatchResponse{Transfer: "basic"})
	}))
	defer server.Close()

	c := &Client{
		batchURL:   server.URL, // http://127.0.0.1:NNNN — must be permitted
		httpClient: server.Client(),
		authHeader: "Basic dGVzdDp0b2tlbg==",
	}
	_, err := c.BatchUpload([]BatchObject{{OID: "abc", Size: 10}})
	require.NoError(t, err)
}

func TestBatch_ObjectError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := BatchResponse{
			Objects: []BatchResponseObject{
				{
					OID:  "abc123",
					Size: 100,
					Error: &ObjectError{
						Code:    404,
						Message: "object not found",
					},
				},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	c := &Client{
		batchURL:   server.URL,
		httpClient: server.Client(),
		authHeader: "Basic dGVzdDp0b2tlbg==",
	}

	resp, err := c.BatchDownload([]BatchObject{{OID: "abc123", Size: 100}})
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.NotNil(t, resp.Objects[0].Error)
	assert.Equal(t, 404, resp.Objects[0].Error.Code)
}

// TestBatch_HTTPFailureKind files a rejected batch request for usage
// telemetry.
// Failure prevented: an LFS upload or download refused for its credentials
// counted as an unexplained failure instead of an auth one.
func TestBatch_HTTPFailureKind(t *testing.T) {
	tests := []struct {
		status int
		kind   errkind.Kind
	}{
		{http.StatusUnauthorized, errkind.Auth},
		{http.StatusForbidden, errkind.Auth},
		{http.StatusInternalServerError, errkind.Other},
	}
	for _, tt := range tests {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tt.status)
			_, _ = w.Write([]byte("refused for user@example.com"))
		}))
		c := &Client{batchURL: server.URL, httpClient: server.Client(), authHeader: "Basic dGVzdDp0b2tlbg=="}
		_, err := c.BatchDownload([]BatchObject{{OID: "abc", Size: 10}})
		server.Close()
		require.Error(t, err)
		assert.Equal(t, tt.kind, errkind.Of(err), "HTTP %d", tt.status)
		assert.Equal(t, fmt.Sprintf("LFS batch API HTTP %d", tt.status), errkind.DetailOf(err))
		assert.Contains(t, err.Error(), "refused for user@example.com", "the message a person sees is unchanged")
	}
}

// A revoked PAT triggers one forced refresh; a revoked bearer returns the operator's remedy.
func TestLFSRefreshesRejectedCredentials(t *testing.T) {
	for _, outcome := range []string{"repaired", "bearer rejected", "empty PAT"} {
		t.Run(outcome, func(t *testing.T) {
			prevDir := gitserver.TestSetConfigDirOverride(t.TempDir())
			prevFile := gitserver.TestSetForceFileStorage(true)
			t.Cleanup(func() {
				gitserver.TestSetConfigDirOverride(prevDir)
				gitserver.TestSetForceFileStorage(prevFile)
			})
			var refreshCalls, batchCalls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v1/cli/repos" {
					refreshCalls.Add(1)
					assert.Equal(t, "Bearer oxt_test_1ljPfr", r.Header.Get("Authorization"))
					if outcome == "bearer rejected" {
						w.WriteHeader(http.StatusUnauthorized)
						return
					}
					if outcome == "empty PAT" {
						io.WriteString(w, `{}`)
						return
					}
					json.NewEncoder(w).Encode(api.ReposResponse{Token: "fresh-pat", ExpiresAt: time.Now().Add(24 * time.Hour)})
					return
				}
				batchCalls.Add(1)
				username, token, _ := r.BasicAuth()
				assert.Equal(t, "oauth2", username)
				if token == "old-pat" {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				assert.Equal(t, "fresh-pat", token)
				var request batchRequest
				require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
				assert.Equal(t, "upload", request.Operation, "retry retains the request body")
				assert.Equal(t, []BatchObject{{OID: "abc", Size: 3}}, request.Objects)
				io.WriteString(w, `{"objects":[]}`)
			}))
			defer srv.Close()
			t.Setenv(endpoint.EnvVar, srv.URL)
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			t.Setenv("OX_XDG_DISABLE", "")
			t.Setenv(auth.EnvVarToken, "oxt_test_1ljPfr")
			require.NoError(t, gitserver.SaveCredentialsForEndpoint(srv.URL, gitserver.GitCredentials{
				Token: "old-pat", ExpiresAt: time.Now().Add(24 * time.Hour),
				BearerTokenHash: gitserver.BearerTokenFingerprint("oxt_test_1ljPfr"),
			}))
			client, err := NewClientForEndpoint(context.Background(), srv.URL+"/ledger.git", srv.URL)
			require.NoError(t, err)
			_, err = client.BatchUpload([]BatchObject{{OID: "abc", Size: 3}})
			assert.EqualValues(t, 1, refreshCalls.Load())
			switch outcome {
			case "bearer rejected":
				require.ErrorIs(t, err, api.ErrUnauthorized)
				assert.Contains(t, err.Error(), "rotate or re-mint")
				assert.NotContains(t, err.Error(), "ox login")
				assert.EqualValues(t, 1, batchCalls.Load())
			case "empty PAT":
				require.ErrorContains(t, err, "empty token")
				assert.EqualValues(t, 1, batchCalls.Load(), "an invalid PAT must never be retried")
			case "repaired":
				require.NoError(t, err)
				assert.EqualValues(t, 2, batchCalls.Load())
				// A reused client must also handle logout without dereferencing a missing cache.
				t.Setenv(auth.EnvVarToken, "")
				require.NoError(t, gitserver.RemoveCredentialsForEndpoint(srv.URL))
				_, err = client.BatchUpload(nil)
				require.ErrorContains(t, err, "no git credentials found")
				for _, emptyCache := range []bool{false, true} {
					if emptyCache {
						require.NoError(t, gitserver.SaveCredentialsForEndpoint(srv.URL, gitserver.GitCredentials{}))
					}
					_, err = NewClientForEndpoint(context.Background(), srv.URL+"/ledger.git", srv.URL)
					require.Error(t, err)
					assert.Equal(t, errkind.NotLoggedIn, errkind.Of(err))
				}
				t.Setenv(auth.EnvVarToken, "oxt_invalid")
				_, err = NewClientForEndpoint(context.Background(), srv.URL+"/ledger.git", srv.URL)
				require.ErrorContains(t, err, "SAGEOX_TOKEN")
			}
			if outcome != "repaired" {
				creds, loadErr := gitserver.LoadCredentialsForEndpoint(srv.URL)
				require.NoError(t, loadErr)
				assert.Equal(t, "old-pat", creds.Token, "failed refresh must preserve cached credentials")
			}
		})
	}
}

// Failure prevented: an endpoint's Git PAT is sent for a repo on another host,
// or refused for the git server the SageOx API named for it.
func TestNewClientForEndpointOnlyTrustsItsGitServer(t *testing.T) {
	prevDir := gitserver.TestSetConfigDirOverride(t.TempDir())
	prevFile := gitserver.TestSetForceFileStorage(true)
	t.Cleanup(func() {
		gitserver.TestSetConfigDirOverride(prevDir)
		gitserver.TestSetForceFileStorage(prevFile)
	})
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv(auth.EnvVarToken, "")
	const ep = "https://sageox.ai"
	require.NoError(t, gitserver.SaveCredentialsForEndpoint(ep, gitserver.GitCredentials{
		Token: "pat", ExpiresAt: time.Now().Add(time.Hour), ServerURL: "http://10.0.0.5:8929",
	}))

	_, err := NewClientForEndpoint(context.Background(), "http://10.0.0.5:8929/team/ledger.git", ep)
	require.NoError(t, err, "the server the API named is the PAT's git server")

	_, err = NewClientForEndpoint(context.Background(), "https://github.com/acme/app.git", ep)
	require.ErrorContains(t, err, "github.com, which is not its git server")
}

// Failure prevented: OAuth or JWT refresh ignores cancellation or saves fallback credentials.
func TestLFSBatchCancelsCredentialRefresh(t *testing.T) {
	for _, stage := range []string{"OAuth", "JWT"} {
		t.Run(stage, func(t *testing.T) {
			prevDir := gitserver.TestSetConfigDirOverride(t.TempDir())
			prevFile := gitserver.TestSetForceFileStorage(true)
			t.Cleanup(func() {
				gitserver.TestSetConfigDirOverride(prevDir)
				gitserver.TestSetForceFileStorage(prevFile)
			})
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			t.Setenv("OX_XDG_DISABLE", "")
			t.Setenv(auth.EnvVarToken, "oxt_test_1ljPfr")
			blockedPath := auth.TokenEndpoint
			if stage == "JWT" {
				blockedPath = "/api/v1/cli/auth/token"
			}
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			var batchCalls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == blockedPath {
					_, _ = io.Copy(io.Discard, r.Body)
					cancel()
					select {
					case <-r.Context().Done():
					case <-time.After(time.Second):
						t.Error("credential refresh ignored caller cancellation")
					}
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				if r.URL.Path == auth.TokenEndpoint {
					_ = json.NewEncoder(w).Encode(map[string]any{
						"access_token": "refreshed-opaque", "expires_in": 3600,
					})
					return
				}
				batchCalls.Add(1)
				w.WriteHeader(http.StatusUnauthorized)
			}))
			t.Cleanup(srv.Close)
			t.Setenv(endpoint.EnvVar, srv.URL)
			require.NoError(t, gitserver.SaveCredentialsForEndpoint(srv.URL, gitserver.GitCredentials{
				Token: "old-pat", ExpiresAt: time.Now().Add(24 * time.Hour),
				BearerTokenHash: gitserver.BearerTokenFingerprint("oxt_test_1ljPfr"),
			}))
			client, err := NewClientForEndpoint(context.Background(), srv.URL+"/ledger.git", srv.URL)
			require.NoError(t, err)
			t.Setenv(auth.EnvVarToken, "")
			require.NoError(t, auth.SaveTokenForEndpoint(srv.URL, &auth.StoredToken{
				AccessToken: "expired-jwt", RefreshToken: "valid-refresh",
				ExpiresAt: time.Now().Add(-time.Hour),
			}))

			_, err = client.BatchUploadContext(ctx, []BatchObject{{OID: "abc", Size: 3}})
			require.ErrorIs(t, err, context.Canceled)
			require.Zero(t, batchCalls.Load())
			creds, err := gitserver.LoadCredentialsForEndpoint(srv.URL)
			require.NoError(t, err)
			require.Equal(t, "old-pat", creds.Token, "cancellation must preserve the PAT cache")
			token, err := auth.GetTokenForEndpoint(srv.URL)
			require.NoError(t, err)
			require.Equal(t, "expired-jwt", token.AccessToken, "canceled JWT exchange must not save an opaque fallback")
		})
	}
}
