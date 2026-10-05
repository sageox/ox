package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/gitserver"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Git must follow bearer rotation, refresh rejected PATs, and withhold them after revocation.
func TestGitCredentialHelper_RotationAndRejectedPAT(t *testing.T) {
	prevDir := gitserver.TestSetConfigDirOverride(t.TempDir())
	prevFile := gitserver.TestSetForceFileStorage(true)
	t.Cleanup(func() {
		gitserver.TestSetConfigDirOverride(prevDir)
		gitserver.TestSetForceFileStorage(prevFile)
	})
	var calls atomic.Int32
	var revoked atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := calls.Add(1)
		if revoked.Load() {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		assert.Equal(t, "/api/v1/cli/repos", r.URL.Path)
		assert.Equal(t, "Bearer oxt_rotated_1lKvCA", r.Header.Get("Authorization"))
		_ = json.NewEncoder(w).Encode(api.ReposResponse{
			Token: fmt.Sprintf("replacement-pat-%d", count), ExpiresAt: time.Now().Add(24 * time.Hour),
		})
	}))
	t.Cleanup(server.Close)
	t.Setenv("SAGEOX_ENDPOINT", server.URL)
	t.Setenv("SAGEOX_TOKEN", "oxt_test_1ljPfr")
	require.NoError(t, gitserver.SaveCredentialsForEndpoint(server.URL, gitserver.GitCredentials{
		Token: "old-pat", ExpiresAt: time.Now().Add(24 * time.Hour),
		BearerTokenHash: gitserver.BearerTokenFingerprint("oxt_test_1ljPfr"),
	}))
	serverURL, err := url.Parse(server.URL)
	require.NoError(t, err)
	request := fmt.Sprintf("protocol=%s\nhost=%s\n\n", serverURL.Scheme, serverURL.Host)
	var out bytes.Buffer
	require.NoError(t, helperGet(strings.NewReader(request), &out))
	require.Equal(t, "username=oauth2\npassword=old-pat\n\n", out.String())
	require.Zero(t, calls.Load())

	t.Setenv("SAGEOX_TOKEN", "oxt_rotated_1lKvCA")
	out.Reset()
	require.NoError(t, helperGet(strings.NewReader(request), &out))
	require.Equal(t, "username=oauth2\npassword=replacement-pat-1\n\n", out.String())
	require.EqualValues(t, 1, calls.Load())

	out.Reset()
	cmd := &cobra.Command{Use: "git-credential-helper"}
	cmd.SetIn(strings.NewReader(request))
	cmd.SetOut(&out)
	require.NoError(t, runGitCredentialHelper(cmd, []string{"erase"}))
	require.Empty(t, out.String(), "erase must never emit credentials")
	require.EqualValues(t, 2, calls.Load(), "erase must force replacement of a fresh but rejected PAT")
	require.NoError(t, helperGet(strings.NewReader(request), &out))
	require.Equal(t, "username=oauth2\npassword=replacement-pat-2\n\n", out.String())
	require.EqualValues(t, 2, calls.Load(), "the replacement should be served from cache")
	// A rotation to a revoked bearer must not release the previous PAT.
	revoked.Store(true)
	t.Setenv("SAGEOX_TOKEN", "oxt_test_1ljPfr")
	for _, operation := range []string{"get", "erase"} {
		out.Reset()
		cmd.SetIn(strings.NewReader(request))
		require.NoError(t, runGitCredentialHelper(cmd, []string{operation}))
		require.Empty(t, out.String())
	}
	creds, err := gitserver.LoadCredentialsForEndpoint(server.URL)
	require.NoError(t, err)
	require.NotNil(t, creds)
	require.Equal(t, "replacement-pat-2", creds.Token, "a failed refresh preserves the cache")
}
