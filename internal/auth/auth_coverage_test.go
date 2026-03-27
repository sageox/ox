package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- makeRequest: server error paths and circuit breaker ---

func TestMakeRequest_ServerError500(t *testing.T) {
	t.Parallel()

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "internal server error"})
	}))
	defer mockServer.Close()

	resp, err := makeRequest(context.Background(), "GET", mockServer.URL, "test-token", nil)
	require.NoError(t, err, "500 errors should not return Go errors")

	assert.Equal(t, http.StatusInternalServerError, resp.StatusCode)
	assert.False(t, resp.Ok())
}

func TestMakeRequest_ServerError503(t *testing.T) {
	t.Parallel()

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer mockServer.Close()

	resp, err := makeRequest(context.Background(), "GET", mockServer.URL, "test-token", nil)
	require.NoError(t, err)
	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
}

func TestMakeRequest_404Response(t *testing.T) {
	t.Parallel()

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"error": "not found"})
	}))
	defer mockServer.Close()

	resp, err := makeRequest(context.Background(), "GET", mockServer.URL, "test-token", nil)
	require.NoError(t, err)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	// 4xx errors should be parsed as JSON when content-type is right
	assert.NotNil(t, resp.Data)
}

// --- AuthClient.AuthenticatedRequest with expired token and successful refresh ---

func TestAuthClient_AuthenticatedRequest_ExpiredToken_RefreshSuccess(t *testing.T) {
	t.Parallel()

	// mock API server
	mockAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}))
	defer mockAPI.Close()

	// mock refresh server
	mockRefresh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/oauth2/token":
			json.NewEncoder(w).Encode(map[string]interface{}{
				"access_token":  "refreshed-opaque",
				"refresh_token": "new-refresh",
				"token_type":    "Bearer",
				"expires_in":    3600,
			})
		case "/api/v1/cli/auth/token":
			json.NewEncoder(w).Encode(map[string]interface{}{
				"access_token": "refreshed-jwt",
				"token_type":   "Bearer",
				"expires_in":   900,
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer mockRefresh.Close()

	client := NewTestClient(t).WithEndpoint(mockRefresh.URL)

	// save expired token (within 5-minute buffer = needs proactive refresh)
	token := createTestToken(2 * time.Minute)
	token.AccessToken = "about-to-expire"
	require.NoError(t, client.SaveToken(token))

	// request should proactively refresh then succeed
	resp, err := client.AuthenticatedRequest(context.Background(), "GET", mockAPI.URL+"/test", nil)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// token should be updated
	savedToken, err := client.GetToken()
	require.NoError(t, err)
	assert.Equal(t, "refreshed-jwt", savedToken.AccessToken)
}

// --- AuthClient.AuthenticatedRequest with JSON body ---

func TestAuthClient_AuthenticatedRequest_WithBody(t *testing.T) {
	t.Parallel()

	var receivedBody map[string]interface{}

	mockAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&receivedBody)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "created"})
	}))
	defer mockAPI.Close()

	client := NewTestClient(t).WithEndpoint(mockAPI.URL)

	token := createTestToken(1 * time.Hour)
	require.NoError(t, client.SaveToken(token))

	body := map[string]interface{}{
		"name":  "test-item",
		"count": 42,
	}
	resp, err := client.AuthenticatedRequest(context.Background(), "POST", mockAPI.URL+"/items", body)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "test-item", receivedBody["name"])
}

// --- refreshTokenForEndpoint: server returns various error codes ---

func TestRefreshTokenForEndpoint_500Error(t *testing.T) {
	t.Parallel()

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{
			"error": "internal_error",
		})
	}))
	defer mockServer.Close()

	client := NewTestClient(t).WithEndpoint(mockServer.URL)

	token := &StoredToken{
		AccessToken:  "old-token",
		RefreshToken: "valid-refresh",
		ExpiresAt:    time.Now().Add(-time.Hour),
	}

	_, err := client.refreshToken(token)
	require.Error(t, err)

	refreshErr, ok := err.(*TokenRefreshError)
	require.True(t, ok)
	assert.Contains(t, refreshErr.Message, "HTTP 500")
}

func TestRefreshTokenForEndpoint_InvalidJSON(t *testing.T) {
	t.Parallel()

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("not valid json"))
	}))
	defer mockServer.Close()

	client := NewTestClient(t).WithEndpoint(mockServer.URL)

	token := &StoredToken{
		AccessToken:  "old",
		RefreshToken: "refresh",
		ExpiresAt:    time.Now().Add(-time.Hour),
	}

	_, err := client.refreshToken(token)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid JSON")
}

func TestRefreshTokenForEndpoint_MissingAccessToken(t *testing.T) {
	t.Parallel()

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"token_type": "Bearer",
			"expires_in": 3600,
			// no access_token!
		})
	}))
	defer mockServer.Close()

	client := NewTestClient(t).WithEndpoint(mockServer.URL)

	token := &StoredToken{
		AccessToken:  "old",
		RefreshToken: "refresh",
		ExpiresAt:    time.Now().Add(-time.Hour),
	}

	_, err := client.refreshToken(token)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing required field")
}

func TestRefreshTokenForEndpoint_SessionTokenFallback(t *testing.T) {
	t.Parallel()

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/oauth2/token":
			json.NewEncoder(w).Encode(map[string]interface{}{
				"access_token":  "new-opaque",
				"session_token": "new-session",
				"token_type":    "Bearer",
				"expires_in":    3600,
			})
		case "/api/v1/cli/auth/token":
			json.NewEncoder(w).Encode(map[string]interface{}{
				"access_token": "new-jwt",
				"expires_in":   900,
			})
		}
	}))
	defer mockServer.Close()

	client := NewTestClient(t).WithEndpoint(mockServer.URL)

	// token with session_token but no refresh_token (Better Auth)
	token := &StoredToken{
		AccessToken:  "old",
		RefreshToken: "",
		SessionToken: "old-session",
		ExpiresAt:    time.Now().Add(-time.Hour),
		UserInfo:     UserInfo{UserID: "user-1", Email: "test@example.com"},
	}

	newToken, err := client.refreshToken(token)
	require.NoError(t, err)
	assert.Equal(t, "new-jwt", newToken.AccessToken)
	assert.Equal(t, "new-session", newToken.SessionToken)
	// user info should be preserved
	assert.Equal(t, "user-1", newToken.UserInfo.UserID)
}

// --- loadAuthStore: edge cases ---

func TestLoadAuthStore_CorruptJSON(t *testing.T) {
	setupTestDir(t)

	authPath, err := GetAuthFilePath()
	require.NoError(t, err)
	configDir := filepath.Dir(authPath)
	require.NoError(t, os.MkdirAll(configDir, 0700))
	require.NoError(t, os.WriteFile(authPath, []byte("not json {{{"), 0600))

	store, err := loadAuthStore()
	require.Error(t, err)
	assert.Nil(t, store)
}

func TestLoadAuthStore_EmptyFile(t *testing.T) {
	setupTestDir(t)

	authPath, err := GetAuthFilePath()
	require.NoError(t, err)
	configDir := filepath.Dir(authPath)
	require.NoError(t, os.MkdirAll(configDir, 0700))
	require.NoError(t, os.WriteFile(authPath, []byte(""), 0600))

	store, err := loadAuthStore()
	require.Error(t, err)
	assert.Nil(t, store)
}

func TestLoadAuthStore_NonexistentFile(t *testing.T) {
	setupTestDir(t)

	store, err := loadAuthStore()
	require.NoError(t, err)
	assert.NotNil(t, store)
	assert.NotNil(t, store.Tokens)
	assert.Empty(t, store.Tokens)
}

// --- saveAuthStore: round-trip ---

func TestSaveAndLoadAuthStore_RoundTrip(t *testing.T) {
	setupTestDir(t)

	token := &StoredToken{
		AccessToken:  "round-trip-token",
		RefreshToken: "round-trip-refresh",
		ExpiresAt:    time.Now().Add(time.Hour).Truncate(time.Millisecond),
		TokenType:    "Bearer",
		UserInfo:     UserInfo{UserID: "u1", Email: "rt@example.com"},
	}

	require.NoError(t, SaveToken(token))

	loaded, err := GetToken()
	require.NoError(t, err)
	require.NotNil(t, loaded)
	assert.Equal(t, "round-trip-token", loaded.AccessToken)
	assert.Equal(t, "round-trip-refresh", loaded.RefreshToken)
	assert.Equal(t, "u1", loaded.UserInfo.UserID)
}

// --- RequireAuth: authenticated path with expired token ---

func TestRequireAuth_AuthRequired_ExpiredToken(t *testing.T) {
	t.Setenv("FEATURE_AUTH", "true")
	setupTestDir(t)

	// save expired token without refresh token
	token := &StoredToken{
		AccessToken: "expired",
		ExpiresAt:   time.Now().Add(-time.Hour),
	}
	require.NoError(t, SaveToken(token))

	err := RequireAuth()
	// expired token with no refresh = not authenticated
	require.Error(t, err)
	assert.Contains(t, err.Error(), "authentication required")
}

// --- IsExpired edge cases ---

func TestStoredToken_IsExpired(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		expiresAt     time.Time
		bufferSeconds int
		wantExpired   bool
	}{
		{
			name:          "valid token outside buffer",
			expiresAt:     time.Now().Add(10 * time.Minute),
			bufferSeconds: 300,
			wantExpired:   false,
		},
		{
			name:          "token within buffer",
			expiresAt:     time.Now().Add(2 * time.Minute),
			bufferSeconds: 300,
			wantExpired:   true,
		},
		{
			name:          "already expired",
			expiresAt:     time.Now().Add(-1 * time.Minute),
			bufferSeconds: 0,
			wantExpired:   true,
		},
		{
			name:          "zero buffer, valid token",
			expiresAt:     time.Now().Add(1 * time.Minute),
			bufferSeconds: 0,
			wantExpired:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			token := &StoredToken{ExpiresAt: tt.expiresAt}
			assert.Equal(t, tt.wantExpired, token.IsExpired(tt.bufferSeconds))
		})
	}
}

// --- AuthClient: storage operations with custom endpoint ---

func TestAuthClient_MultiEndpoint(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	client1 := NewAuthClientWithDir(tmpDir).WithEndpoint("https://endpoint-1.example.com")
	client2 := NewAuthClientWithDir(tmpDir).WithEndpoint("https://endpoint-2.example.com")

	// save tokens for different endpoints
	token1 := &StoredToken{AccessToken: "token-ep1", ExpiresAt: time.Now().Add(time.Hour)}
	token2 := &StoredToken{AccessToken: "token-ep2", ExpiresAt: time.Now().Add(time.Hour)}
	require.NoError(t, client1.SaveToken(token1))
	require.NoError(t, client2.SaveToken(token2))

	// each client should retrieve its own token
	got1, err := client1.GetToken()
	require.NoError(t, err)
	require.NotNil(t, got1)
	assert.Equal(t, "token-ep1", got1.AccessToken)

	got2, err := client2.GetToken()
	require.NoError(t, err)
	require.NotNil(t, got2)
	assert.Equal(t, "token-ep2", got2.AccessToken)
}

// --- Package-level refreshTokenForEndpoint: exercises the global code path ---

func TestPackageLevel_RefreshTokenForEndpoint_Success(t *testing.T) {
	setupTestDir(t)

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/oauth2/token":
			json.NewEncoder(w).Encode(map[string]interface{}{
				"access_token":  "pkg-refreshed-opaque",
				"refresh_token": "pkg-new-refresh",
				"token_type":    "Bearer",
				"expires_in":    3600,
			})
		case "/api/v1/cli/auth/token":
			json.NewEncoder(w).Encode(map[string]interface{}{
				"access_token": "pkg-refreshed-jwt",
				"token_type":   "Bearer",
				"expires_in":   900,
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer mockServer.Close()

	token := &StoredToken{
		AccessToken:  "old-pkg",
		RefreshToken: "valid-refresh",
		ExpiresAt:    time.Now().Add(-time.Hour),
		UserInfo:     UserInfo{UserID: "user-pkg", Email: "pkg@example.com"},
	}

	newToken, err := refreshTokenForEndpoint(token, mockServer.URL)
	require.NoError(t, err)
	assert.Equal(t, "pkg-refreshed-jwt", newToken.AccessToken)
	assert.Equal(t, "user-pkg", newToken.UserInfo.UserID)
}

func TestPackageLevel_RefreshTokenForEndpoint_NoRefreshToken(t *testing.T) {
	setupTestDir(t)

	token := &StoredToken{
		AccessToken:  "tok",
		RefreshToken: "",
		SessionToken: "",
	}

	_, err := refreshTokenForEndpoint(token, "https://example.com")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no refresh token available")
}

func TestPackageLevel_RefreshTokenForEndpoint_ServerError(t *testing.T) {
	setupTestDir(t)

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{
			"error":             "invalid_grant",
			"error_description": "refresh token expired",
		})
	}))
	defer mockServer.Close()

	token := &StoredToken{
		AccessToken:  "tok",
		RefreshToken: "expired-refresh",
		ExpiresAt:    time.Now().Add(-time.Hour),
	}

	_, err := refreshTokenForEndpoint(token, mockServer.URL)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "re-authentication required")
}

func TestPackageLevel_RefreshTokenForEndpoint_NetworkError(t *testing.T) {
	setupTestDir(t)

	token := &StoredToken{
		AccessToken:  "tok",
		RefreshToken: "refresh",
		ExpiresAt:    time.Now().Add(-time.Hour),
	}

	_, err := refreshTokenForEndpoint(token, "http://invalid-host-does-not-exist:99999")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "network error")
}

func TestPackageLevel_RefreshTokenForEndpoint_MissingAccessToken(t *testing.T) {
	setupTestDir(t)

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"token_type": "Bearer",
			"expires_in": 3600,
		})
	}))
	defer mockServer.Close()

	token := &StoredToken{
		AccessToken:  "tok",
		RefreshToken: "refresh",
		ExpiresAt:    time.Now().Add(-time.Hour),
	}

	_, err := refreshTokenForEndpoint(token, mockServer.URL)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing required field")
}

func TestPackageLevel_RefreshTokenForEndpoint_JWTExchangeFails(t *testing.T) {
	setupTestDir(t)

	requestCount := 0
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/oauth2/token":
			json.NewEncoder(w).Encode(map[string]interface{}{
				"access_token":  "opaque-token-fallback",
				"refresh_token": "new-refresh",
				"token_type":    "Bearer",
				"expires_in":    3600,
			})
		case "/api/v1/cli/auth/token":
			// JWT exchange fails
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer mockServer.Close()

	token := &StoredToken{
		AccessToken:  "old",
		RefreshToken: "refresh",
		ExpiresAt:    time.Now().Add(-time.Hour),
		UserInfo:     UserInfo{UserID: "u1"},
	}

	newToken, err := refreshTokenForEndpoint(token, mockServer.URL)
	require.NoError(t, err)
	// should fall back to opaque token when JWT exchange fails
	assert.Equal(t, "opaque-token-fallback", newToken.AccessToken)
}

// --- AuthClient: remove token for specific endpoint ---

func TestAuthClient_RemoveToken_OnlyAffectsOwnEndpoint(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	client1 := NewAuthClientWithDir(tmpDir).WithEndpoint("https://ep-a.example.com")
	client2 := NewAuthClientWithDir(tmpDir).WithEndpoint("https://ep-b.example.com")

	token := &StoredToken{AccessToken: "tok", ExpiresAt: time.Now().Add(time.Hour)}
	require.NoError(t, client1.SaveToken(token))
	require.NoError(t, client2.SaveToken(token))

	require.NoError(t, client1.RemoveToken())

	// client1 token removed
	got1, err := client1.GetToken()
	require.NoError(t, err)
	assert.Nil(t, got1)

	// client2 token still present
	got2, err := client2.GetToken()
	require.NoError(t, err)
	assert.NotNil(t, got2)
}

// --- makeRequest: context deadline exceeded ---

func TestMakeRequest_ContextDeadlineExceeded(t *testing.T) {
	t.Parallel()

	// server that takes too long
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(5 * time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	defer mockServer.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := makeRequest(ctx, "GET", mockServer.URL, "test-token", nil)
	require.Error(t, err)

	apiErr, ok := err.(*APIError)
	require.True(t, ok)
	assert.Contains(t, apiErr.Message, "network error")
}

// --- EnsureValidTokenForEndpoint: additional edge cases ---

func TestEnsureValidTokenForEndpoint_FreshTokenNoBigBuffer(t *testing.T) {
	setupTestDir(t)

	ep := "https://fresh-ep.example.com"
	token := &StoredToken{
		AccessToken:  "fresh-token",
		RefreshToken: "refresh",
		ExpiresAt:    time.Now().Add(2 * time.Hour),
	}
	require.NoError(t, SaveTokenForEndpoint(ep, token))

	// large buffer but token still valid
	got, err := EnsureValidTokenForEndpoint(ep, 3600)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "fresh-token", got.AccessToken)
}

// --- AuthClient.EnsureValidTokenForEndpoint ---

func TestAuthClient_EnsureValidTokenForEndpoint_Valid(t *testing.T) {
	t.Parallel()

	client := NewTestClient(t)

	ep := "https://ep-test.example.com"
	token := &StoredToken{
		AccessToken:  "valid-ep-token",
		RefreshToken: "refresh",
		ExpiresAt:    time.Now().Add(time.Hour),
	}
	require.NoError(t, client.SaveTokenForEndpoint(ep, token))

	got, err := client.EnsureValidTokenForEndpoint(ep, 300)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "valid-ep-token", got.AccessToken)
}

func TestAuthClient_EnsureValidTokenForEndpoint_NoToken(t *testing.T) {
	t.Parallel()

	client := NewTestClient(t)

	got, err := client.EnsureValidTokenForEndpoint("https://no-ep.example.com", 300)
	require.NoError(t, err)
	assert.Nil(t, got)
}
