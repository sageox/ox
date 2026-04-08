package auth

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupPackageLevelTest isolates package-level auth functions by pointing
// XDG_CONFIG_HOME at a temp dir and pre-creating the sageox config directory
// so that auth file lock operations succeed.
func setupPackageLevelTest(t *testing.T) {
	t.Helper()
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)
	t.Setenv("SAGEOX_ENDPOINT", "https://test.example.com")
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, "sageox"), 0700))
}

// --- Feature flag tests ---

func TestIsAuthRequired(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  bool
	}{
		{"empty", "", false},
		{"true", "true", true},
		{"TRUE", "TRUE", true},
		{"True", "True", true},
		{"1", "1", true},
		{"yes", "yes", true},
		{"YES", "YES", true},
		{"false", "false", false},
		{"0", "0", false},
		{"no", "no", false},
		{"random", "random", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("FEATURE_AUTH", tt.value)
			got := IsAuthRequired()
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestIsCloudEnabled(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  bool
	}{
		{"empty", "", false},
		{"true", "true", true},
		{"TRUE", "TRUE", true},
		{"1", "1", true},
		{"yes", "yes", true},
		{"false", "false", false},
		{"0", "0", false},
		{"no", "no", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("FEATURE_CLOUD", tt.value)
			got := IsCloudEnabled()
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestIsPostMVPEnabled(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  bool
	}{
		{"empty", "", false},
		{"true", "true", true},
		{"TRUE", "TRUE", true},
		{"1", "1", true},
		{"yes", "yes", true},
		{"false", "false", false},
		{"0", "0", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("FEATURE_POST_MVP", tt.value)
			got := IsPostMVPEnabled()
			assert.Equal(t, tt.want, got)
		})
	}
}

// --- RequireAuth middleware tests ---

func TestRequireAuth_AuthNotRequired(t *testing.T) {
	t.Setenv("FEATURE_AUTH", "false")

	err := RequireAuth()
	assert.NoError(t, err, "should pass when auth not required")
}

func TestRequireAuth_AuthRequired_NotAuthenticated(t *testing.T) {
	t.Setenv("FEATURE_AUTH", "true")
	setupPackageLevelTest(t)

	err := RequireAuth()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "authentication required")
}

func TestRequireAuth_AuthRequired_ValidToken(t *testing.T) {
	t.Setenv("FEATURE_AUTH", "true")
	setupPackageLevelTest(t)

	token := &StoredToken{
		AccessToken:  "valid-token",
		RefreshToken: "refresh",
		ExpiresAt:    time.Now().Add(time.Hour),
		TokenType:    "Bearer",
	}
	require.NoError(t, SaveToken(token))

	err := RequireAuth()
	assert.NoError(t, err)
}

// --- OAuth error types ---

func TestDeviceAuthPendingError(t *testing.T) {
	t.Parallel()

	err := &DeviceAuthPendingError{}
	assert.Equal(t, "authorization_pending", err.Error())
}

func TestDeviceAuthSlowDownError(t *testing.T) {
	t.Parallel()

	err := &DeviceAuthSlowDownError{}
	assert.Equal(t, "slow_down", err.Error())
}

func TestDeviceAuthError(t *testing.T) {
	t.Parallel()

	err := &DeviceAuthError{Message: "authorization was denied"}
	assert.Equal(t, "authorization was denied", err.Error())
}

func TestLoginResult_Fields(t *testing.T) {
	t.Parallel()

	result := &LoginResult{
		Success: true,
		UserInfo: &UserInfo{
			UserID: "user-abc",
			Email:  "test@example.com",
			Name:   "Test Person",
		},
	}

	assert.True(t, result.Success)
	assert.Equal(t, "user-abc", result.UserInfo.UserID)

	failResult := &LoginResult{
		Success: false,
		Error:   "device code expired",
	}
	assert.False(t, failResult.Success)
	assert.Equal(t, "device code expired", failResult.Error)
}

// --- TokenResponse.effectiveRefreshToken (device_flow.go) ---

func TestTokenResponse_effectiveRefreshToken(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		refreshToken string
		sessionToken string
		want         string
	}{
		{
			name:         "prefer refresh_token when both present",
			refreshToken: "refresh-tok",
			sessionToken: "session-tok",
			want:         "refresh-tok",
		},
		{
			name:         "fallback to session_token when refresh empty",
			refreshToken: "",
			sessionToken: "session-tok",
			want:         "session-tok",
		},
		{
			name:         "empty when both empty",
			refreshToken: "",
			sessionToken: "",
			want:         "",
		},
		{
			name:         "refresh_token only",
			refreshToken: "refresh-tok",
			sessionToken: "",
			want:         "refresh-tok",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			resp := &TokenResponse{
				RefreshToken: tt.refreshToken,
				SessionToken: tt.sessionToken,
			}
			got := resp.effectiveRefreshToken()
			assert.Equal(t, tt.want, got)
		})
	}
}

// --- StoredToken.EffectiveRefreshToken ---

func TestStoredToken_EffectiveRefreshToken(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		refreshToken string
		sessionToken string
		want         string
	}{
		{
			name:         "prefer refresh_token",
			refreshToken: "refresh",
			sessionToken: "session",
			want:         "refresh",
		},
		{
			name:         "fallback to session_token",
			refreshToken: "",
			sessionToken: "session",
			want:         "session",
		},
		{
			name:         "empty when both empty",
			refreshToken: "",
			sessionToken: "",
			want:         "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			token := &StoredToken{
				RefreshToken: tt.refreshToken,
				SessionToken: tt.sessionToken,
			}
			got := token.EffectiveRefreshToken()
			assert.Equal(t, tt.want, got)
		})
	}
}

// --- Package-level ListEndpoints ---

func TestListEndpoints(t *testing.T) {
	setupPackageLevelTest(t)

	// initially empty
	endpoints, err := ListEndpoints()
	require.NoError(t, err)
	assert.Empty(t, endpoints)

	// add tokens for two endpoints
	token1 := &StoredToken{AccessToken: "tok1", ExpiresAt: time.Now().Add(time.Hour)}
	require.NoError(t, SaveTokenForEndpoint("https://endpoint-a.example.com", token1))

	token2 := &StoredToken{AccessToken: "tok2", ExpiresAt: time.Now().Add(time.Hour)}
	require.NoError(t, SaveTokenForEndpoint("https://endpoint-b.example.com", token2))

	endpoints, err = ListEndpoints()
	require.NoError(t, err)
	assert.Len(t, endpoints, 2)
	assert.Contains(t, endpoints, "https://endpoint-a.example.com")
	assert.Contains(t, endpoints, "https://endpoint-b.example.com")
}

func TestListEndpoints_NormalizesKeys(t *testing.T) {
	setupPackageLevelTest(t)

	token := &StoredToken{AccessToken: "tok", ExpiresAt: time.Now().Add(time.Hour)}
	require.NoError(t, SaveTokenForEndpoint("https://api.sageox.ai", token))

	endpoints, err := ListEndpoints()
	require.NoError(t, err)
	assert.Len(t, endpoints, 1)
	assert.Contains(t, endpoints, "https://sageox.ai")
}

// --- Package-level RemoveToken / RemoveTokenForEndpoint ---

func TestPackageLevelRemoveToken(t *testing.T) {
	t.Parallel()

	client := NewTestClient(t)

	token := &StoredToken{AccessToken: "tok", ExpiresAt: time.Now().Add(time.Hour)}
	require.NoError(t, client.SaveToken(token))

	got, err := client.GetToken()
	require.NoError(t, err)
	require.NotNil(t, got)

	require.NoError(t, client.RemoveToken())

	got, err = client.GetToken()
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestPackageLevelRemoveTokenForEndpoint(t *testing.T) {
	t.Parallel()

	client := NewTestClient(t)

	ep := "https://remove-test.example.com"
	token := &StoredToken{AccessToken: "tok", ExpiresAt: time.Now().Add(time.Hour)}
	require.NoError(t, client.SaveTokenForEndpoint(ep, token))

	got, err := client.GetTokenForEndpoint(ep)
	require.NoError(t, err)
	require.NotNil(t, got)

	require.NoError(t, client.RemoveTokenForEndpoint(ep))

	got, err = client.GetTokenForEndpoint(ep)
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestPackageLevelRemoveTokenForEndpoint_NoStore(t *testing.T) {
	t.Parallel()

	client := NewTestClient(t)

	// removing from empty store should not error
	err := client.RemoveTokenForEndpoint("https://nonexistent.example.com")
	assert.NoError(t, err)
}

// --- GetUserID with explicit endpoint ---

func TestGetUserID_WithExplicitEndpoint(t *testing.T) {
	setupPackageLevelTest(t)

	ep := "https://userid-test.example.com"
	token := &StoredToken{
		AccessToken: "tok",
		ExpiresAt:   time.Now().Add(time.Hour),
		UserInfo:    UserInfo{UserID: "user-xyz"},
	}
	require.NoError(t, SaveTokenForEndpoint(ep, token))

	uid := GetUserID(ep)
	assert.Equal(t, "user-xyz", uid)
}

func TestGetUserID_EndpointNoToken(t *testing.T) {
	setupPackageLevelTest(t)

	uid := GetUserID("https://no-token.example.com")
	assert.Equal(t, "", uid)
}

// --- GetLoggedInEndpoints edge cases ---

func TestGetLoggedInEndpoints_NilToken(t *testing.T) {
	setupPackageLevelTest(t)

	// write a store with a nil token value
	authPath, err := GetAuthFilePath()
	require.NoError(t, err)
	configDir := filepath.Dir(authPath)
	require.NoError(t, os.MkdirAll(configDir, 0700))

	store := AuthStore{
		Tokens: map[string]*StoredToken{
			"https://nil-token.example.com": nil,
			"https://valid.example.com": {
				AccessToken: "valid",
				ExpiresAt:   time.Now().Add(time.Hour),
			},
		},
	}
	data, err := json.Marshal(store)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(authPath, data, 0600))

	endpoints := GetLoggedInEndpoints()
	assert.Len(t, endpoints, 1)
	assert.Contains(t, endpoints, "https://valid.example.com")
}

func TestGetLoggedInEndpoints_AllExpired(t *testing.T) {
	setupPackageLevelTest(t)

	token := &StoredToken{
		AccessToken: "expired",
		ExpiresAt:   time.Now().Add(-time.Hour),
	}
	require.NoError(t, SaveTokenForEndpoint("https://expired.example.com", token))

	endpoints := GetLoggedInEndpoints()
	assert.Empty(t, endpoints)
}

// --- NormalizeTokenKeys edge cases ---

func TestNormalizeTokenKeys_NilTokenValue(t *testing.T) {
	t.Parallel()

	// prefixed key with nil token value should not panic
	store := &AuthStore{
		Tokens: map[string]*StoredToken{
			"https://api.sageox.ai": nil,
		},
	}

	normalizeTokenKeys(store)

	// nil token gets moved to normalized key
	assert.Len(t, store.Tokens, 1)
	_, exists := store.Tokens["https://sageox.ai"]
	assert.True(t, exists)
}

func TestNormalizeTokenKeys_CollisionNilVsExisting(t *testing.T) {
	t.Parallel()

	existingToken := &StoredToken{
		AccessToken: "existing",
		ExpiresAt:   time.Now().Add(time.Hour),
	}

	store := &AuthStore{
		Tokens: map[string]*StoredToken{
			"https://sageox.ai":     existingToken,
			"https://api.sageox.ai": nil, // nil collides with existing
		},
	}

	normalizeTokenKeys(store)

	assert.Len(t, store.Tokens, 1)
	retrieved := store.Tokens["https://sageox.ai"]
	require.NotNil(t, retrieved)
	assert.Equal(t, "existing", retrieved.AccessToken, "existing non-nil token should win over nil")
}

// --- tokenResponse.effectiveRefreshToken (refresh.go) ---

func TestRefreshTokenResponse_effectiveRefreshToken(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		refreshToken string
		sessionToken string
		want         string
	}{
		{
			name:         "prefer refresh_token",
			refreshToken: "refresh",
			sessionToken: "session",
			want:         "refresh",
		},
		{
			name:         "fallback to session_token",
			refreshToken: "",
			sessionToken: "session",
			want:         "session",
		},
		{
			name:         "empty when both empty",
			refreshToken: "",
			sessionToken: "",
			want:         "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			resp := &tokenResponse{
				RefreshToken: tt.refreshToken,
				SessionToken: tt.sessionToken,
			}
			got := resp.effectiveRefreshToken()
			assert.Equal(t, tt.want, got)
		})
	}
}

// --- AuthClient legacy migration with empty endpoint ---

func TestAuthClient_LegacyMigration_EmptyEndpoint(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	// create client with empty endpoint to test fallback to endpoint.Get()
	client := &AuthClient{
		configDir: tmpDir,
		endpoint:  "",
	}

	// write legacy single-token format directly to the auth file
	legacyToken := StoredToken{
		AccessToken: "legacy-token",
		ExpiresAt:   time.Now().Add(time.Hour),
		TokenType:   "Bearer",
	}
	data, err := json.Marshal(legacyToken)
	require.NoError(t, err)

	authPath, err := client.GetAuthFilePath()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(authPath), 0700))
	require.NoError(t, os.WriteFile(authPath, data, 0600))

	// verify via public API that the legacy token is accessible
	got, err := client.GetToken()
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "legacy-token", got.AccessToken)
}

// --- AuthClient.GetToken / SaveToken / RemoveToken with empty endpoint ---

func TestAuthClient_TokenOps_EmptyEndpoint(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	client := &AuthClient{
		configDir: tmpDir,
		endpoint:  "",
	}

	// save with empty endpoint (should use default)
	token := &StoredToken{
		AccessToken: "default-ep-token",
		ExpiresAt:   time.Now().Add(time.Hour),
	}
	require.NoError(t, client.SaveToken(token))

	got, err := client.GetToken()
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "default-ep-token", got.AccessToken)

	// remove
	require.NoError(t, client.RemoveToken())

	got, err = client.GetToken()
	require.NoError(t, err)
	assert.Nil(t, got)
}

// --- AuthClient.IsAuthenticated with empty endpoint ---

func TestAuthClient_IsAuthenticated_EmptyEndpoint(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	client := &AuthClient{
		configDir: tmpDir,
		endpoint:  "",
	}

	// no token - not authenticated
	authed, err := client.IsAuthenticated()
	require.NoError(t, err)
	assert.False(t, authed)

	// save valid token
	token := &StoredToken{
		AccessToken: "tok",
		ExpiresAt:   time.Now().Add(time.Hour),
	}
	require.NoError(t, client.SaveToken(token))

	authed, err = client.IsAuthenticated()
	require.NoError(t, err)
	assert.True(t, authed)
}

// --- Package-level IsAuthenticated / IsAuthenticatedForEndpoint ---

func TestPackageLevel_IsAuthenticated_NoToken(t *testing.T) {
	setupPackageLevelTest(t)

	authed, err := IsAuthenticated()
	require.NoError(t, err)
	assert.False(t, authed)
}

func TestPackageLevel_IsAuthenticatedForEndpoint_NoToken(t *testing.T) {
	setupPackageLevelTest(t)

	authed, err := IsAuthenticatedForEndpoint("https://example.com")
	require.NoError(t, err)
	assert.False(t, authed)
}

func TestPackageLevel_IsAuthenticatedForEndpoint_ValidToken(t *testing.T) {
	setupPackageLevelTest(t)

	ep := "https://auth-check.example.com"
	token := &StoredToken{
		AccessToken: "valid",
		ExpiresAt:   time.Now().Add(time.Hour),
	}
	require.NoError(t, SaveTokenForEndpoint(ep, token))

	authed, err := IsAuthenticatedForEndpoint(ep)
	require.NoError(t, err)
	assert.True(t, authed)
}

func TestPackageLevel_IsAuthenticatedForEndpoint_ExpiredWithRefresh(t *testing.T) {
	setupPackageLevelTest(t)

	ep := "https://expired-auth.example.com"
	token := &StoredToken{
		AccessToken:  "expired",
		RefreshToken: "refresh", // has refresh token, will attempt refresh and fail
		ExpiresAt:    time.Now().Add(-time.Hour),
	}
	require.NoError(t, SaveTokenForEndpoint(ep, token))

	// refresh will fail (no server), so should return error
	authed, err := IsAuthenticatedForEndpoint(ep)
	assert.False(t, authed)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "token refresh failed")
}

// --- Package-level EnsureValidToken / EnsureValidTokenForEndpoint ---

func TestPackageLevel_EnsureValidToken_NoToken(t *testing.T) {
	setupPackageLevelTest(t)

	token, err := EnsureValidToken(300)
	require.NoError(t, err)
	assert.Nil(t, token)
}

func TestPackageLevel_EnsureValidToken_Valid(t *testing.T) {
	setupPackageLevelTest(t)

	saved := &StoredToken{
		AccessToken:  "valid-token",
		RefreshToken: "refresh",
		ExpiresAt:    time.Now().Add(time.Hour),
	}
	require.NoError(t, SaveToken(saved))

	token, err := EnsureValidToken(300)
	require.NoError(t, err)
	require.NotNil(t, token)
	assert.Equal(t, "valid-token", token.AccessToken)
}

func TestPackageLevel_EnsureValidTokenForEndpoint_NoToken(t *testing.T) {
	setupPackageLevelTest(t)

	token, err := EnsureValidTokenForEndpoint("https://example.com", 300)
	require.NoError(t, err)
	assert.Nil(t, token)
}

// --- Package-level RevokeToken ---

func TestPackageLevel_RevokeToken_NoToken(t *testing.T) {
	setupPackageLevelTest(t)

	success, err := RevokeToken()
	assert.NoError(t, err)
	assert.True(t, success, "no token = success")
}

// --- Package-level Handle401Error ---

func TestPackageLevel_Handle401Error_NoRefreshToken(t *testing.T) {
	setupPackageLevelTest(t)

	token := &StoredToken{
		AccessToken:  "tok",
		RefreshToken: "",
		SessionToken: "",
		ExpiresAt:    time.Now().Add(-time.Hour),
	}

	_, err := Handle401Error(token)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no refresh token available")
}

// --- Package-level refreshToken ---

func TestPackageLevel_refreshToken_NoRefreshToken(t *testing.T) {
	setupPackageLevelTest(t)

	token := &StoredToken{
		AccessToken:  "tok",
		RefreshToken: "",
		SessionToken: "",
	}

	_, err := refreshToken(token)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no refresh token available")
}
