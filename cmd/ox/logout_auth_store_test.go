package main

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/testguard"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCollectLogoutEndpoints_EnvLoginOnStoredEndpointIsOneTarget verifies a
// disk login and a SAGEOX_TOKEN login on the same endpoint are one target.
// Failure prevented: the endpoint was listed twice, so logout revoked it twice
// and a multi-endpoint prompt offered the same endpoint as two choices.
func TestCollectLogoutEndpoints_EnvLoginOnStoredEndpointIsOneTarget(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("OX_XDG_DISABLE", "")
	t.Setenv("SAGEOX_ENDPOINT", "https://sageox.ai")
	t.Setenv(auth.EnvVarToken, "oxp_test_4bDZfN")

	for _, ep := range []string{"https://sageox.ai", "https://test.sageox.ai"} {
		require.NoError(t, auth.SaveTokenForEndpoint(ep, &auth.StoredToken{AccessToken: "test-access", TokenType: "Bearer"}))
	}

	endpoints, listErr := collectLogoutEndpoints()
	require.NoError(t, listErr)
	assert.ElementsMatch(t, []string{"https://sageox.ai", "https://test.sageox.ai"}, endpoints)
}

// TestLogoutOutcome_LocalFailureNeverRewritesServerAnswer covers every server
// answer with and without a local copy left behind.
// Failure prevented: a confirmed revocation reported as failed, or a line that
// says "Logged out" while the local token is still on disk.
func TestLogoutOutcome_LocalFailureNeverRewritesServerAnswer(t *testing.T) {
	const ep = "https://sageox.ai"
	localErr := fmt.Errorf("%w: failed to parse auth file", auth.ErrLocalTokenNotRemoved)
	for _, tt := range []struct {
		name            string
		status          auth.RevocationStatus
		localErr        error
		wantLine        string
		wantUnconfirmed bool
	}{
		{name: "confirmed", status: auth.RevocationSuccess, wantLine: "Logged out from " + ep},
		{name: "nothing to revoke", status: auth.RevocationNoToken, wantLine: "Logged out from " + ep},
		{name: "unreachable", status: auth.RevocationNetworkError, wantLine: "Logged out locally from " + ep + " (could not reach server)", wantUnconfirmed: true},
		{name: "unconfirmed", status: auth.RevocationServerFailed, wantLine: "Logged out locally from " + ep + " (server did not confirm revocation)", wantUnconfirmed: true},
		{name: "confirmed, local copy remains", status: auth.RevocationSuccess, localErr: localErr, wantLine: "Revoked on the server for " + ep + ", but failed to remove local token: failed to parse auth file"},
		{name: "unreachable, local copy remains", status: auth.RevocationNetworkError, localErr: localErr, wantLine: "Could not reach the server for " + ep + ", and failed to remove local token: failed to parse auth file", wantUnconfirmed: true},
		{name: "unconfirmed, local copy remains", status: auth.RevocationServerFailed, localErr: localErr, wantLine: "Server did not confirm revocation for " + ep + ", and failed to remove local token: failed to parse auth file", wantUnconfirmed: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			line, unconfirmed := logoutOutcome(ep, tt.status, tt.localErr)
			assert.Equal(t, tt.wantLine, line)
			assert.Equal(t, tt.wantUnconfirmed, unconfirmed, "server-side session may still be active")
			if tt.localErr != nil {
				assert.NotContains(t, line, "Logged out", "the local token is still on disk")
			}
		})
	}
}

// TestLogout_UnreadableAuthStore drives `ox logout` when auth.json cannot be
// parsed.
// Failure prevented: the storage error hid a usable SAGEOX_TOKEN, so logout
// revoked nothing, or revoked it and then exited 0 as if the store were fine.
// With no usable credential, the error must not read as "not logged in". A
// revocation the server confirmed must not be reported as failed just because
// the corrupt store kept the local copy from being removed.
func TestLogout_UnreadableAuthStore(t *testing.T) {
	for _, tt := range []struct {
		name            string
		envToken        string
		wantRevocations int32
		wantOutput      string
	}{
		{name: "env token is revoked, then the listing error is reported", envToken: "oxp_test_4bDZfN", wantRevocations: 1, wantOutput: "Revoked on the server for"},
		{name: "no usable credential reports the listing error, not a logged-out state"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var revocations atomic.Int32
			server := testguard.SafeMockServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == auth.RevokeEndpoint && r.Method == http.MethodPost {
					revocations.Add(1)
					w.WriteHeader(http.StatusOK)
					return
				}
				http.NotFound(w, r)
			}))

			t.Chdir(t.TempDir()) // logout strips PATs from the current repo's remotes
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			t.Setenv("OX_XDG_DISABLE", "")
			t.Setenv("OX_GIT_CREDENTIALS_FILE", filepath.Join(t.TempDir(), "git-credentials.json"))
			t.Setenv("SAGEOX_ENDPOINT", server.URL)
			t.Setenv(auth.EnvVarToken, tt.envToken)

			authPath, err := auth.GetAuthFilePath()
			require.NoError(t, err)
			require.NoError(t, os.MkdirAll(filepath.Dir(authPath), 0o700))
			require.NoError(t, os.WriteFile(authPath, []byte("not-json"), 0o600))

			prevForce, prevAll, prevEndpoint := logoutForce, logoutAll, logoutEndpoint
			logoutForce, logoutAll, logoutEndpoint = true, false, ""
			t.Cleanup(func() { logoutForce, logoutAll, logoutEndpoint = prevForce, prevAll, prevEndpoint })

			var runErr error
			output := captureStdoutForPlanCLI(t, func() { runErr = logoutCmd.RunE(logoutCmd, nil) })

			require.Error(t, runErr, "an unreadable auth store must fail the logout; output: %s", output)
			assert.Contains(t, runErr.Error(), "failed to list stored authentication endpoints")
			assert.Equal(t, tt.wantRevocations, revocations.Load(), "output: %s", output)
			assert.NotContains(t, output, "not currently logged in", "a storage error must not read as logged out")
			assert.NotContains(t, output, "Failed to logout", "a revocation the server confirmed is not a failed logout")
			assert.NotContains(t, output, "server-side sessions may still be active", "the server confirmed the revocation")
			if tt.wantOutput != "" {
				assert.Contains(t, output, tt.wantOutput)
			}
		})
	}
}
