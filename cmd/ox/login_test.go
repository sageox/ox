//go:build !short

package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sageox/ox/internal/auth"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLoginProceedsWhenTokenRefreshFails verifies that `ox login` does not
// abort when the stored token is expired and the refresh endpoint returns a
// non-200 status. This is a regression test for the bug where
// IsAuthenticatedForEndpoint returned a fatal error that blocked login
// entirely.
func TestLoginProceedsWhenTokenRefreshFails(t *testing.T) {
	// Mock server: return 404 on /oauth2/token (refresh) and 200 on
	// /api/auth/device/code (device flow) so we can detect that login
	// proceeded past the refresh failure.
	var deviceCodeRequested atomic.Bool
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth2/token":
			// Simulate the 404 that triggered the original bug
			w.WriteHeader(http.StatusNotFound)
		case "/api/auth/device/code":
			deviceCodeRequested.Store(true)
			// Return a valid device code response so the flow proceeds
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{
				"device_code": "test-device-code",
				"user_code": "TEST-CODE",
				"verification_uri": "https://example.com/device",
				"verification_uri_complete": "https://example.com/device?code=TEST-CODE",
				"expires_in": 900,
				"interval": 5
			}`))
		case "/api/v1/device/token":
			// Poll endpoint — return "authorization_pending" to let the
			// flow terminate with a context timeout rather than spinning
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error": "authorization_pending"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer mockServer.Close()

	// Isolate auth storage so we don't touch the real config
	tempDir := t.TempDir()
	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("HOME", tempDir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tempDir, ".config"))

	// Disable browser opening during test
	t.Setenv("SKIP_BROWSER", "1")

	// Seed an expired token for the mock endpoint
	expiredToken := &auth.StoredToken{
		AccessToken:  "expired-access-token",
		RefreshToken: "expired-refresh-token",
		ExpiresAt:    time.Now().Add(-1 * time.Hour),
		TokenType:    "Bearer",
		Scope:        "user:profile sageox:write",
		UserInfo: auth.UserInfo{
			UserID: "user-123",
			Email:  "test@example.com",
			Name:   "Test User",
		},
	}
	require.NoError(t, auth.SaveTokenForEndpoint(mockServer.URL, expiredToken))

	// Verify the token is stored and expired
	token, err := auth.GetTokenForEndpoint(mockServer.URL)
	require.NoError(t, err)
	require.NotNil(t, token)
	assert.True(t, token.IsExpired(0), "token should be expired")

	// Force non-interactive mode so login doesn't try bubbletea
	t.Setenv("CI", "true")

	// Suppress stdout from login flow (device code URL, etc.)
	oldStdout := os.Stdout
	devNull, openErr := os.Open(os.DevNull)
	require.NoError(t, openErr)
	os.Stdout = devNull
	t.Cleanup(func() {
		os.Stdout = oldStdout
		_ = devNull.Close()
	})

	// Run the login flow — this should NOT return the refresh error.
	// Use a short-lived context so the device flow poll times out quickly.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := &cobra.Command{}
	cmd.SetContext(ctx)
	err = runLoginFlow(cmd, mockServer.URL)

	// The flow should have proceeded past the refresh failure and
	// reached the device code request. It will eventually fail because
	// the mock doesn't complete the device flow, but the error should
	// NOT be the original "failed to check authentication status" error.
	if err != nil {
		assert.False(t,
			strings.Contains(err.Error(), "failed to check authentication status"),
			"login should not fail with auth check error; got: %s", err.Error())
		assert.False(t,
			strings.Contains(err.Error(), "token refresh failed"),
			"login should not surface token refresh failure; got: %s", err.Error())
	}

	// The device code endpoint should have been hit, proving login
	// proceeded past the failed refresh
	assert.True(t, deviceCodeRequested.Load(),
		"login should have proceeded to device code request after refresh failure")
}
