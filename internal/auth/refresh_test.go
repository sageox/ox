package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEnsureValidToken_NoToken(t *testing.T) {
	t.Parallel()

	client := NewAuthClientWithDir(t.TempDir())

	token, err := client.EnsureValidToken(300)
	require.NoError(t, err, "want nil for non-existent token")
	assert.Nil(t, token, "want nil when no token exists")
}

func TestEnsureValidToken_ValidToken(t *testing.T) {
	t.Parallel()

	client := NewAuthClientWithDir(t.TempDir())

	// create token expiring in 1 hour
	originalToken := createTestToken(1 * time.Hour)
	require.NoError(t, client.SaveToken(originalToken))

	// should return token as-is without refresh
	token, err := client.EnsureValidToken(300)
	require.NoError(t, err)
	require.NotNil(t, token, "want valid token")

	// verify token matches original (no refresh occurred)
	assert.Equal(t, originalToken.AccessToken, token.AccessToken)
	assert.Equal(t, originalToken.RefreshToken, token.RefreshToken)
}

func TestEnsureValidToken_ExpiredToken(t *testing.T) {
	t.Parallel()

	// create mock server handling both OAuth2 refresh and JWT exchange
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch r.URL.Path {
		case TokenEndpoint:
			// verify request method and headers
			assert.Equal(t, "POST", r.Method)
			assert.Equal(t, "application/x-www-form-urlencoded", r.Header.Get("Content-Type"))

			// verify request body
			require.NoError(t, r.ParseForm())
			assert.Equal(t, "refresh_token", r.Form.Get("grant_type"))
			assert.Equal(t, ClientID, r.Form.Get("client_id"))
			assert.Equal(t, "test-refresh-token", r.Form.Get("refresh_token"))

			// return successful refresh response
			response := map[string]interface{}{
				"access_token":  "new-opaque-token",
				"refresh_token": "new-refresh-token",
				"token_type":    "Bearer",
				"expires_in":    3600,
				"scope":         "openid profile email",
			}
			json.NewEncoder(w).Encode(response)
		case "/api/v1/cli/auth/token":
			// JWT exchange endpoint
			response := map[string]interface{}{
				"access_token": "new-jwt-token",
				"token_type":   "Bearer",
				"expires_in":   900,
			}
			json.NewEncoder(w).Encode(response)
		default:
			t.Errorf("unexpected endpoint: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer mockServer.Close()

	client := NewAuthClientWithDir(t.TempDir()).WithEndpoint(mockServer.URL)

	// create expired token
	expiredToken := createTestToken(-1 * time.Hour)
	require.NoError(t, client.SaveToken(expiredToken))

	// should trigger refresh
	token, err := client.EnsureValidToken(300)
	require.NoError(t, err)
	require.NotNil(t, token, "want refreshed token")

	// verify token was refreshed and exchanged for JWT
	assert.Equal(t, "new-jwt-token", token.AccessToken)
	assert.Equal(t, "new-refresh-token", token.RefreshToken)

	// verify user info was preserved
	assert.Equal(t, expiredToken.UserInfo.UserID, token.UserInfo.UserID)
}

func TestEnsureValidToken_ExpiringWithinBuffer(t *testing.T) {
	t.Parallel()

	// create mock server handling both OAuth2 refresh and JWT exchange
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch r.URL.Path {
		case TokenEndpoint:
			response := map[string]interface{}{
				"access_token":  "proactive-refresh-opaque",
				"refresh_token": "new-refresh-token",
				"token_type":    "Bearer",
				"expires_in":    3600,
			}
			json.NewEncoder(w).Encode(response)
		case "/api/v1/cli/auth/token":
			response := map[string]interface{}{
				"access_token": "proactive-refresh-jwt",
				"token_type":   "Bearer",
				"expires_in":   900,
			}
			json.NewEncoder(w).Encode(response)
		default:
			t.Errorf("unexpected endpoint: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer mockServer.Close()

	client := NewAuthClientWithDir(t.TempDir()).WithEndpoint(mockServer.URL)

	// create token expiring in 4 minutes (240 seconds)
	// buffer is 300 seconds (5 minutes)
	// should trigger proactive refresh
	almostExpiredToken := createTestToken(4 * time.Minute)
	require.NoError(t, client.SaveToken(almostExpiredToken))

	// should trigger proactive refresh
	token, err := client.EnsureValidToken(300)
	require.NoError(t, err)
	require.NotNil(t, token, "want refreshed token")

	// verify token was refreshed and exchanged for JWT
	assert.Equal(t, "proactive-refresh-jwt", token.AccessToken)
}

func TestRefreshToken_Success(t *testing.T) {
	t.Parallel()

	// create mock server that handles both OAuth2 token refresh and JWT exchange
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch r.URL.Path {
		case TokenEndpoint:
			// OAuth2 token refresh endpoint
			response := map[string]interface{}{
				"access_token":  "refreshed-opaque-token",
				"refresh_token": "refreshed-refresh-token",
				"token_type":    "Bearer",
				"expires_in":    7200,
				"scope":         "user:profile sageox:write",
			}
			json.NewEncoder(w).Encode(response)
		case "/api/v1/cli/auth/token":
			// JWT exchange endpoint
			response := map[string]interface{}{
				"access_token": "refreshed-jwt-token",
				"token_type":   "Bearer",
				"expires_in":   900,
			}
			json.NewEncoder(w).Encode(response)
		default:
			t.Errorf("unexpected endpoint: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer mockServer.Close()

	client := NewAuthClientWithDir(t.TempDir()).WithEndpoint(mockServer.URL)

	// create expired token with user info
	oldToken := createTestToken(-1 * time.Hour)
	oldToken.UserInfo = UserInfo{
		UserID: "user456",
		Email:  "test@example.com",
		Name:   "Test Person",
	}
	require.NoError(t, client.SaveToken(oldToken))

	// trigger refresh via Handle401Error
	newToken, err := client.Handle401Error(oldToken)
	require.NoError(t, err)
	require.NotNil(t, newToken, "want refreshed token")

	// verify new token fields - should be JWT after exchange
	assert.Equal(t, "refreshed-jwt-token", newToken.AccessToken)
	assert.Equal(t, "refreshed-refresh-token", newToken.RefreshToken)
	assert.Equal(t, "Bearer", newToken.TokenType)

	// verify expiry was calculated (should be ~15 min from now after JWT exchange)
	expectedExpiry := time.Now().Add(900 * time.Second)
	timeDiff := newToken.ExpiresAt.Sub(expectedExpiry)
	assert.True(t, timeDiff <= 5*time.Second && timeDiff >= -5*time.Second,
		"ExpiresAt diff = %v, want within 5 seconds of expected", timeDiff)

	// verify user info was preserved
	assert.Equal(t, oldToken.UserInfo.UserID, newToken.UserInfo.UserID)
	assert.Equal(t, oldToken.UserInfo.Email, newToken.UserInfo.Email)
	assert.Equal(t, oldToken.UserInfo.Name, newToken.UserInfo.Name)

	// verify token was saved to disk
	savedToken, err := client.GetToken()
	require.NoError(t, err)
	assert.Equal(t, "refreshed-jwt-token", savedToken.AccessToken)
}

func TestRefreshToken_RefreshTokenNotRotated(t *testing.T) {
	t.Parallel()

	// mock server that doesn't return new refresh_token (some OAuth servers don't rotate)
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := map[string]interface{}{
			"access_token": "new-access-only",
			"token_type":   "Bearer",
			"expires_in":   3600,
			// no refresh_token in response
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer mockServer.Close()

	client := NewAuthClientWithDir(t.TempDir()).WithEndpoint(mockServer.URL)

	oldToken := createTestToken(-1 * time.Hour)
	oldRefreshToken := oldToken.RefreshToken
	require.NoError(t, client.SaveToken(oldToken))

	newToken, err := client.Handle401Error(oldToken)
	require.NoError(t, err)

	// refresh token should be preserved from original
	assert.Equal(t, oldRefreshToken, newToken.RefreshToken, "preserved from original")
}

func TestRefreshToken_InvalidRefreshToken(t *testing.T) {
	t.Parallel()

	// mock server returning 401 unauthorized
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		response := map[string]interface{}{
			"error":             "invalid_grant",
			"error_description": "refresh token expired or revoked",
		}
		json.NewEncoder(w).Encode(response)
	}))
	defer mockServer.Close()

	client := NewAuthClientWithDir(t.TempDir()).WithEndpoint(mockServer.URL)

	token := createTestToken(-1 * time.Hour)
	require.NoError(t, client.SaveToken(token))

	newToken, err := client.Handle401Error(token)
	require.Error(t, err, "want TokenRefreshError")
	assert.Nil(t, newToken, "want nil on error")

	// verify error contains expected message
	assert.Contains(t, err.Error(), "re-authentication required")

	// verify error message contains server error description
	assert.Contains(t, err.Error(), "refresh token expired or revoked")
}

func TestRefreshToken_BadRequest(t *testing.T) {
	t.Parallel()

	// mock server returning 400 bad request
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		response := map[string]interface{}{
			"error":             "invalid_request",
			"error_description": "missing required parameter",
		}
		json.NewEncoder(w).Encode(response)
	}))
	defer mockServer.Close()

	client := NewAuthClientWithDir(t.TempDir()).WithEndpoint(mockServer.URL)

	token := createTestToken(-1 * time.Hour)
	require.NoError(t, client.SaveToken(token))

	newToken, err := client.Handle401Error(token)
	require.Error(t, err, "want TokenRefreshError")
	assert.Nil(t, newToken, "want nil on error")
	assert.Contains(t, err.Error(), "re-authentication required")
}

func TestRefreshToken_ServerError(t *testing.T) {
	t.Parallel()

	// mock server returning 500 internal server error
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		response := map[string]interface{}{
			"error": "server_error",
		}
		json.NewEncoder(w).Encode(response)
	}))
	defer mockServer.Close()

	client := NewAuthClientWithDir(t.TempDir()).WithEndpoint(mockServer.URL)

	token := createTestToken(-1 * time.Hour)
	require.NoError(t, client.SaveToken(token))

	newToken, err := client.Handle401Error(token)
	require.Error(t, err, "want TokenRefreshError")
	assert.Nil(t, newToken, "want nil on error")
	assert.Contains(t, err.Error(), "HTTP 500")
}

func TestRefreshToken_NetworkError(t *testing.T) {
	t.Parallel()

	// use invalid URL to trigger network error
	client := NewAuthClientWithDir(t.TempDir()).WithEndpoint("http://invalid-host-that-does-not-exist:99999")

	token := createTestToken(-1 * time.Hour)
	require.NoError(t, client.SaveToken(token))

	newToken, err := client.Handle401Error(token)
	require.Error(t, err, "want TokenRefreshError for network error")
	assert.Nil(t, newToken, "want nil on network error")

	// verify error contains network error message
	assert.Contains(t, err.Error(), "network error")
}

func TestRefreshToken_MissingAccessToken(t *testing.T) {
	t.Parallel()

	// mock server returning response without access_token
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		response := map[string]interface{}{
			"token_type": "Bearer",
			"expires_in": 3600,
			// missing access_token
		}
		json.NewEncoder(w).Encode(response)
	}))
	defer mockServer.Close()

	client := NewAuthClientWithDir(t.TempDir()).WithEndpoint(mockServer.URL)

	token := createTestToken(-1 * time.Hour)
	require.NoError(t, client.SaveToken(token))

	newToken, err := client.Handle401Error(token)
	require.Error(t, err, "want TokenRefreshError for missing access_token")
	assert.Nil(t, newToken, "want nil on error")
	assert.Contains(t, err.Error(), "missing required field 'access_token'")
}

func TestRefreshToken_InvalidJSON(t *testing.T) {
	t.Parallel()

	// mock server returning invalid JSON
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("{ invalid json }"))
	}))
	defer mockServer.Close()

	client := NewAuthClientWithDir(t.TempDir()).WithEndpoint(mockServer.URL)

	token := createTestToken(-1 * time.Hour)
	require.NoError(t, client.SaveToken(token))

	newToken, err := client.Handle401Error(token)
	require.Error(t, err, "want TokenRefreshError for invalid JSON")
	assert.Nil(t, newToken, "want nil on error")
	assert.Contains(t, err.Error(), "invalid JSON response")
}

func TestRefreshToken_EmptyAccessToken(t *testing.T) {
	t.Parallel()

	// mock server returning empty access_token
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		response := map[string]interface{}{
			"access_token": "",
			"token_type":   "Bearer",
			"expires_in":   3600,
		}
		json.NewEncoder(w).Encode(response)
	}))
	defer mockServer.Close()

	client := NewAuthClientWithDir(t.TempDir()).WithEndpoint(mockServer.URL)

	token := createTestToken(-1 * time.Hour)
	require.NoError(t, client.SaveToken(token))

	newToken, err := client.Handle401Error(token)
	require.Error(t, err, "want TokenRefreshError for empty access_token")
	assert.Nil(t, newToken, "want nil on error")
	assert.Contains(t, err.Error(), "missing required field 'access_token'")
}

func TestRefreshToken_OptionalFields(t *testing.T) {
	t.Parallel()

	// mock server with minimal response (only access_token)
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		response := map[string]interface{}{
			"access_token": "minimal-token",
			// all other fields optional
		}
		json.NewEncoder(w).Encode(response)
	}))
	defer mockServer.Close()

	client := NewAuthClientWithDir(t.TempDir()).WithEndpoint(mockServer.URL)

	token := createTestToken(-1 * time.Hour)
	token.TokenType = "Custom"
	token.Scope = "custom:scope"
	require.NoError(t, client.SaveToken(token))

	newToken, err := client.Handle401Error(token)
	require.NoError(t, err)

	// verify defaults are applied
	assert.Equal(t, "minimal-token", newToken.AccessToken)

	// token_type should default to Bearer
	assert.Equal(t, "Bearer", newToken.TokenType, "default")

	// expires_in should default to 3600
	expectedExpiry := time.Now().Add(3600 * time.Second)
	timeDiff := newToken.ExpiresAt.Sub(expectedExpiry)
	assert.True(t, timeDiff <= 5*time.Second && timeDiff >= -5*time.Second,
		"ExpiresAt diff = %v, want within 5 seconds of 1 hour from now", timeDiff)

	// scope should be preserved from original token
	assert.Equal(t, token.Scope, newToken.Scope, "preserved")

	// refresh_token should be preserved from original token
	assert.Equal(t, token.RefreshToken, newToken.RefreshToken, "preserved")
}

func TestHandle401Error(t *testing.T) {
	t.Parallel()

	// create mock server for successful refresh
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := map[string]interface{}{
			"access_token":  "recovered-token",
			"refresh_token": "new-refresh",
			"token_type":    "Bearer",
			"expires_in":    3600,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer mockServer.Close()

	client := NewAuthClientWithDir(t.TempDir()).WithEndpoint(mockServer.URL)

	// simulate 401 response with existing token
	originalToken := createTestToken(1 * time.Hour) // not actually expired, but server rejected it
	require.NoError(t, client.SaveToken(originalToken))

	// handle the 401 error
	newToken, err := client.Handle401Error(originalToken)
	require.NoError(t, err)
	require.NotNil(t, newToken, "want refreshed token")

	// verify token was refreshed
	assert.Equal(t, "recovered-token", newToken.AccessToken)

	// verify user info preserved
	assert.Equal(t, originalToken.UserInfo.UserID, newToken.UserInfo.UserID, "UserInfo not preserved during reactive refresh")
}

func TestRefreshToken_TrailingSlashInURL(t *testing.T) {
	t.Parallel()

	// mock server
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// verify path doesn't have double slashes
		assert.False(t, strings.Contains(r.URL.Path, "//"), "URL path contains double slashes: %v", r.URL.Path)

		response := map[string]interface{}{
			"access_token": "test-token",
			"token_type":   "Bearer",
			"expires_in":   3600,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer mockServer.Close()

	// set API URL with trailing slash
	client := NewAuthClientWithDir(t.TempDir()).WithEndpoint(mockServer.URL + "/")

	token := createTestToken(-1 * time.Hour)
	require.NoError(t, client.SaveToken(token))

	_, err := client.Handle401Error(token)
	require.NoError(t, err)
}

func TestTokenRefreshError_Error(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		err      *TokenRefreshError
		expected string
	}{
		{
			name: "error with wrapped error",
			err: &TokenRefreshError{
				Message: "refresh failed",
				Err:     http.ErrServerClosed,
			},
			expected: "refresh failed: http: Server closed",
		},
		{
			name: "error without wrapped error",
			err: &TokenRefreshError{
				Message: "simple error",
				Err:     nil,
			},
			expected: "simple error",
		},
	}

	for _, tt := range tests {
		tt := tt // capture range variable
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := tt.err.Error()
			assert.Equal(t, tt.expected, got)
		})
	}
}

func TestTokenRefreshError_Unwrap(t *testing.T) {
	t.Parallel()

	wrappedErr := http.ErrServerClosed
	err := &TokenRefreshError{
		Message: "test",
		Err:     wrappedErr,
	}

	unwrapped := err.Unwrap()
	assert.Equal(t, wrappedErr, unwrapped)

	// test nil wrapped error
	err2 := &TokenRefreshError{
		Message: "test",
		Err:     nil,
	}

	unwrapped2 := err2.Unwrap()
	assert.Nil(t, unwrapped2)
}

func TestEnsureValidToken_ZeroBuffer(t *testing.T) {
	t.Parallel()

	client := NewAuthClientWithDir(t.TempDir())

	// token expiring in 10 seconds
	token := createTestToken(10 * time.Second)
	require.NoError(t, client.SaveToken(token))

	// with zero buffer, should not refresh
	result, err := client.EnsureValidToken(0)
	require.NoError(t, err)

	// should return original token
	assert.Equal(t, token.AccessToken, result.AccessToken, "expected no refresh")
}

func TestEnsureValidToken_NegativeBuffer(t *testing.T) {
	t.Parallel()

	client := NewAuthClientWithDir(t.TempDir())

	// token expiring in 1 hour
	token := createTestToken(1 * time.Hour)
	require.NoError(t, client.SaveToken(token))

	// negative buffer should be treated as zero
	result, err := client.EnsureValidToken(-100)
	require.NoError(t, err)

	// should return original token
	assert.Equal(t, token.AccessToken, result.AccessToken, "expected no refresh with negative buffer")
}

func TestEnsureValidTokenForEndpoint_NoToken(t *testing.T) {
	t.Parallel()

	client := NewAuthClientWithDir(t.TempDir())

	token, err := client.EnsureValidTokenForEndpoint("https://other.example.com", 300)
	require.NoError(t, err)
	assert.Nil(t, token, "want nil when no token exists for endpoint")
}

func TestEnsureValidTokenForEndpoint_ValidToken(t *testing.T) {
	t.Parallel()

	client := NewAuthClientWithDir(t.TempDir())
	ep := "https://other.example.com"

	// save token for a specific endpoint
	original := createTestToken(1 * time.Hour)
	require.NoError(t, client.SaveTokenForEndpoint(ep, original))

	// should return token as-is without refresh
	token, err := client.EnsureValidTokenForEndpoint(ep, 300)
	require.NoError(t, err)
	require.NotNil(t, token)
	assert.Equal(t, original.AccessToken, token.AccessToken)
}

func TestEnsureValidTokenForEndpoint_ExpiredToken(t *testing.T) {
	t.Parallel()

	// mock server that handles refresh + JWT exchange
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch r.URL.Path {
		case TokenEndpoint:
			require.NoError(t, r.ParseForm())
			assert.Equal(t, "refresh_token", r.Form.Get("grant_type"))
			assert.Equal(t, "test-refresh-token", r.Form.Get("refresh_token"))

			json.NewEncoder(w).Encode(map[string]interface{}{
				"access_token":  "refreshed-opaque",
				"refresh_token": "refreshed-refresh",
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
			t.Errorf("unexpected endpoint: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer mockServer.Close()

	client := NewAuthClientWithDir(t.TempDir())

	// save expired token for the mock server endpoint
	expired := createTestToken(-1 * time.Hour)
	require.NoError(t, client.SaveTokenForEndpoint(mockServer.URL, expired))

	// should refresh using the correct endpoint
	token, err := client.EnsureValidTokenForEndpoint(mockServer.URL, 300)
	require.NoError(t, err)
	require.NotNil(t, token)
	assert.Equal(t, "refreshed-jwt", token.AccessToken)
	assert.Equal(t, "refreshed-refresh", token.RefreshToken)
	assert.Equal(t, expired.UserInfo.UserID, token.UserInfo.UserID)
}
