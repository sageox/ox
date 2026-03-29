package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRequestDeviceCode_Success(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: device flow polling")
	}

	// create mock server that returns valid device code response
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// verify request method and path
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, DeviceCodeEndpoint, r.URL.Path)

		// verify content-type is JSON
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

		// verify accept header
		assert.Equal(t, "application/json", r.Header.Get("Accept"))

		// return mock response
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(DeviceCodeResponse{
			DeviceCode:              "test-device-code",
			UserCode:                "ABCD-1234",
			VerificationURI:         "https://example.com/verify",
			VerificationURIComplete: "https://example.com/verify?code=ABCD-1234",
			ExpiresIn:               300,
			Interval:                5,
		})
	}))
	defer server.Close()

	// create client with test endpoint
	client := NewAuthClientWithDir(t.TempDir()).WithEndpoint(server.URL)

	// test RequestDeviceCode
	resp, err := client.RequestDeviceCode()
	require.NoError(t, err)

	// verify response
	assert.Equal(t, "test-device-code", resp.DeviceCode)
	assert.Equal(t, "ABCD-1234", resp.UserCode)
	assert.Equal(t, "https://example.com/verify", resp.VerificationURI)
	assert.Equal(t, 300, resp.ExpiresIn)
	assert.Equal(t, 5, resp.Interval)
}

func TestRequestDeviceCode_Error(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: device flow polling")
	}

	// create mock server that returns error
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(ErrorResponse{
			Error:            "invalid_request",
			ErrorDescription: "Missing required parameter: client_id",
		})
	}))
	defer server.Close()

	// create client with test endpoint
	client := NewAuthClientWithDir(t.TempDir()).WithEndpoint(server.URL)

	// test RequestDeviceCode
	_, err := client.RequestDeviceCode()
	require.Error(t, err)

	// verify error message contains the error details
	assert.Contains(t, err.Error(), "invalid_request")
}

func TestRequestDeviceCode_InvalidJSON(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: device flow polling")
	}

	// create mock server that returns invalid JSON
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("invalid json"))
	}))
	defer server.Close()

	// create client with test endpoint
	client := NewAuthClientWithDir(t.TempDir()).WithEndpoint(server.URL)

	// test RequestDeviceCode
	_, err := client.RequestDeviceCode()
	require.Error(t, err, "expected error for invalid JSON")
}

func TestLogin_Success(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: device flow polling")
	}

	callCount := 0
	// create mock server that:
	// 1. first call returns authorization_pending
	// 2. second call returns token
	// 3. userinfo endpoint returns user info
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		// handle token endpoint
		if r.URL.Path == DeviceTokenEndpoint {
			callCount++
			if callCount == 1 {
				// first call: authorization pending
				w.WriteHeader(http.StatusBadRequest)
				json.NewEncoder(w).Encode(ErrorResponse{
					Error:            "authorization_pending",
					ErrorDescription: "User has not authorized the device yet",
				})
			} else {
				// second call: success
				json.NewEncoder(w).Encode(TokenResponse{
					AccessToken:  "test-access-token",
					RefreshToken: "test-refresh-token",
					TokenType:    "Bearer",
					ExpiresIn:    3600,
					Scope:        "user:profile sageox:write",
				})
			}
			return
		}

		// handle JWT exchange endpoint
		if r.URL.Path == "/api/v1/cli/auth/token" {
			json.NewEncoder(w).Encode(JWTExchangeResponse{
				AccessToken: "test-jwt-token",
				TokenType:   "Bearer",
				ExpiresIn:   3600,
			})
			return
		}

		// handle userinfo endpoint
		if r.URL.Path == UserInfoEndpoint {
			// verify authorization header
			auth := r.Header.Get("Authorization")
			assert.Equal(t, "Bearer test-jwt-token", auth)

			json.NewEncoder(w).Encode(UserInfo{
				UserID: "user-123",
				Email:  "test@example.com",
				Name:   "Test User",
			})
			return
		}

		// unknown endpoint
		t.Errorf("unexpected path: %s", r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	// create client with temp dir and endpoint
	client := NewAuthClientWithDir(t.TempDir()).WithEndpoint(server.URL)

	// create device code response
	deviceCode := &DeviceCodeResponse{
		DeviceCode:      "test-device-code",
		UserCode:        "ABCD-1234",
		VerificationURI: "https://example.com/verify",
		ExpiresIn:       300,
		Interval:        1, // short interval for testing
	}

	// track status callbacks
	var statuses []string
	statusCallback := func(msg string) {
		statuses = append(statuses, msg)
	}

	// test login with sufficient timeout (needs to wait for polling interval)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	err := client.Login(ctx, deviceCode, statusCallback)
	require.NoError(t, err)

	// verify status callback was invoked
	assert.NotEmpty(t, statuses, "expected status callbacks")

	// verify token was saved
	token, err := client.GetToken()
	require.NoError(t, err)
	require.NotNil(t, token, "expected token to be saved")

	// verify token fields (JWT token is used instead of opaque token)
	assert.Equal(t, "test-jwt-token", token.AccessToken)
	assert.Equal(t, "test-refresh-token", token.RefreshToken)
	assert.Equal(t, "test@example.com", token.UserInfo.Email)
}

func TestLogin_Timeout(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: device flow polling")
	}

	// create mock server that always returns authorization_pending
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(ErrorResponse{
			Error:            "authorization_pending",
			ErrorDescription: "User has not authorized the device yet",
		})
	}))
	defer server.Close()

	client := NewAuthClientWithDir(t.TempDir()).WithEndpoint(server.URL)

	// create device code response with very short expiry
	deviceCode := &DeviceCodeResponse{
		DeviceCode:      "test-device-code",
		UserCode:        "ABCD-1234",
		VerificationURI: "https://example.com/verify",
		ExpiresIn:       1, // expires in 1 second
		Interval:        1,
	}

	// test login with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	err := client.Login(ctx, deviceCode, nil)
	require.Error(t, err)

	// verify error message
	errMsg := err.Error()
	assert.True(t, strings.Contains(errMsg, "expired") || strings.Contains(errMsg, "context deadline exceeded"),
		"expected timeout or expiry error, got: %v", err)
}

func TestLogin_AccessDenied(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: device flow polling")
	}

	// create mock server that returns access_denied
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(ErrorResponse{
			Error:            "access_denied",
			ErrorDescription: "User denied authorization",
		})
	}))
	defer server.Close()

	client := NewAuthClientWithDir(t.TempDir()).WithEndpoint(server.URL)

	// create device code response
	deviceCode := &DeviceCodeResponse{
		DeviceCode:      "test-device-code",
		UserCode:        "ABCD-1234",
		VerificationURI: "https://example.com/verify",
		ExpiresIn:       300,
		Interval:        1,
	}

	// test login with sufficient timeout for first poll
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err := client.Login(ctx, deviceCode, nil)
	require.Error(t, err)

	// verify error message
	assert.Contains(t, err.Error(), "denied")
}

func TestLogin_SlowDown(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow test (25s+ delay) in short mode")
	}
	t.Parallel()

	callCount := 0
	// create mock server that returns slow_down then success
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		// handle token endpoint
		if r.URL.Path == DeviceTokenEndpoint {
			callCount++
			switch callCount {
			case 1:
				// first call: slow down
				w.WriteHeader(http.StatusBadRequest)
				json.NewEncoder(w).Encode(ErrorResponse{
					Error:            "slow_down",
					ErrorDescription: "You are polling too quickly",
				})
			case 2:
				// second call: still pending
				w.WriteHeader(http.StatusBadRequest)
				json.NewEncoder(w).Encode(ErrorResponse{
					Error:            "authorization_pending",
					ErrorDescription: "User has not authorized the device yet",
				})
			default:
				// third call: success
				json.NewEncoder(w).Encode(TokenResponse{
					AccessToken:  "test-access-token",
					RefreshToken: "test-refresh-token",
					TokenType:    "Bearer",
					ExpiresIn:    3600,
					Scope:        "user:profile sageox:write",
				})
			}
			return
		}

		// handle JWT exchange endpoint
		if r.URL.Path == "/api/v1/cli/auth/token" {
			json.NewEncoder(w).Encode(JWTExchangeResponse{
				AccessToken: "test-jwt-token",
				TokenType:   "Bearer",
				ExpiresIn:   3600,
			})
			return
		}

		// handle userinfo endpoint
		if r.URL.Path == UserInfoEndpoint {
			json.NewEncoder(w).Encode(UserInfo{
				UserID: "user-123",
				Email:  "test@example.com",
				Name:   "Test User",
			})
			return
		}

		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	client := NewAuthClientWithDir(t.TempDir()).WithEndpoint(server.URL)

	// create device code response
	deviceCode := &DeviceCodeResponse{
		DeviceCode:      "test-device-code",
		UserCode:        "ABCD-1234",
		VerificationURI: "https://example.com/verify",
		ExpiresIn:       300,
		Interval:        1, // short interval for testing
	}

	// track status messages
	var statuses []string
	statusCallback := func(msg string) {
		statuses = append(statuses, msg)
	}

	// test login with sufficient timeout for multiple polls
	// slow_down adds 5 seconds to interval, so need extra time
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	err := client.Login(ctx, deviceCode, statusCallback)
	require.NoError(t, err)

	// verify slow_down message was received
	hasSlowDown := false
	for _, msg := range statuses {
		if strings.Contains(strings.ToLower(msg), "slow") {
			hasSlowDown = true
			break
		}
	}
	assert.True(t, hasSlowDown, "expected slow_down status message")

	// verify we got at least 3 calls (slow_down, pending, success)
	assert.GreaterOrEqual(t, callCount, 3, "expected at least 3 calls to token endpoint")
}

func TestLogin_ContextCancellation(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: device flow polling")
	}

	// create mock server that always returns authorization_pending
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// add delay to simulate slow server
		time.Sleep(100 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(ErrorResponse{
			Error:            "authorization_pending",
			ErrorDescription: "User has not authorized the device yet",
		})
	}))
	defer server.Close()

	client := NewAuthClientWithDir(t.TempDir()).WithEndpoint(server.URL)

	// create device code response
	deviceCode := &DeviceCodeResponse{
		DeviceCode:      "test-device-code",
		UserCode:        "ABCD-1234",
		VerificationURI: "https://example.com/verify",
		ExpiresIn:       300,
		Interval:        1,
	}

	// create context that we'll cancel
	ctx, cancel := context.WithCancel(context.Background())

	// cancel context after short delay
	go func() {
		time.Sleep(500 * time.Millisecond)
		cancel()
	}()

	// test login
	err := client.Login(ctx, deviceCode, nil)
	require.Error(t, err)

	// verify error is context cancellation
	assert.Equal(t, context.Canceled, err)
}

func TestLogin_ExpiredToken(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: device flow polling")
	}

	// create mock server that returns expired_token
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(ErrorResponse{
			Error:            "expired_token",
			ErrorDescription: "The device code has expired",
		})
	}))
	defer server.Close()

	client := NewAuthClientWithDir(t.TempDir()).WithEndpoint(server.URL)

	// create device code response
	deviceCode := &DeviceCodeResponse{
		DeviceCode:      "test-device-code",
		UserCode:        "ABCD-1234",
		VerificationURI: "https://example.com/verify",
		ExpiresIn:       300,
		Interval:        1,
	}

	// test login with sufficient timeout for first poll
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err := client.Login(ctx, deviceCode, nil)
	require.Error(t, err)

	// verify error message
	assert.Contains(t, err.Error(), "expired")
}

func TestLogin_UserInfoError(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: device flow polling")
	}

	// create mock server where token succeeds but userinfo fails
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		// handle token endpoint
		if r.URL.Path == DeviceTokenEndpoint {
			json.NewEncoder(w).Encode(TokenResponse{
				AccessToken:  "test-access-token",
				RefreshToken: "test-refresh-token",
				TokenType:    "Bearer",
				ExpiresIn:    3600,
				Scope:        "user:profile sageox:write",
			})
			return
		}

		// handle JWT exchange endpoint
		if r.URL.Path == "/api/v1/cli/auth/token" {
			json.NewEncoder(w).Encode(JWTExchangeResponse{
				AccessToken: "test-jwt-token",
				TokenType:   "Bearer",
				ExpiresIn:   3600,
			})
			return
		}

		// handle userinfo endpoint - return error
		if r.URL.Path == UserInfoEndpoint {
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(ErrorResponse{
				Error:            "invalid_token",
				ErrorDescription: "Token is invalid",
			})
			return
		}

		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	client := NewAuthClientWithDir(t.TempDir()).WithEndpoint(server.URL)

	// create device code response
	deviceCode := &DeviceCodeResponse{
		DeviceCode:      "test-device-code",
		UserCode:        "ABCD-1234",
		VerificationURI: "https://example.com/verify",
		ExpiresIn:       300,
		Interval:        1,
	}

	// test login with sufficient timeout for first poll
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err := client.Login(ctx, deviceCode, nil)
	require.Error(t, err)

	// verify error message
	assert.Contains(t, err.Error(), "user info")
}

func TestPollToken_Success(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: device flow polling")
	}

	// create mock server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// verify request
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

		// return success
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(TokenResponse{
			AccessToken:  "test-access-token",
			RefreshToken: "test-refresh-token",
			TokenType:    "Bearer",
			ExpiresIn:    3600,
			Scope:        "user:profile sageox:write",
		})
	}))
	defer server.Close()

	// test pollToken
	client := &http.Client{Timeout: 10 * time.Second}
	token, err := pollToken(client, server.URL, "test-device-code")
	require.NoError(t, err)

	// verify response
	assert.Equal(t, "test-access-token", token.AccessToken)
	assert.Equal(t, "Bearer", token.TokenType)
}

func TestPollToken_AuthorizationPending(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: device flow polling")
	}

	// create mock server that returns authorization_pending
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(ErrorResponse{
			Error:            "authorization_pending",
			ErrorDescription: "User has not authorized the device yet",
		})
	}))
	defer server.Close()

	// test pollToken
	client := &http.Client{Timeout: 10 * time.Second}
	_, err := pollToken(client, server.URL, "test-device-code")
	require.Error(t, err)

	// verify error contains authorization_pending
	assert.Contains(t, err.Error(), "authorization_pending")
}

func TestFetchUserInfo_Success(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: device flow polling")
	}

	// create mock server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// verify authorization header
		auth := r.Header.Get("Authorization")
		assert.Equal(t, "Bearer test-token", auth)

		// return user info
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(UserInfo{
			UserID: "user-123",
			Email:  "test@example.com",
			Name:   "Test User",
		})
	}))
	defer server.Close()

	// test fetchUserInfo
	client := &http.Client{Timeout: 10 * time.Second}
	userInfo, err := fetchUserInfo(client, server.URL, "test-token")
	require.NoError(t, err)

	// verify response
	assert.Equal(t, "user-123", userInfo.UserID)
	assert.Equal(t, "test@example.com", userInfo.Email)
	assert.Equal(t, "Test User", userInfo.Name)
}

func TestFetchUserInfo_Unauthorized(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: device flow polling")
	}

	// create mock server that returns 401
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte("Unauthorized"))
	}))
	defer server.Close()

	// test fetchUserInfo
	client := &http.Client{Timeout: 10 * time.Second}
	_, err := fetchUserInfo(client, server.URL, "invalid-token")
	require.Error(t, err)

	// verify error message contains status code
	assert.Contains(t, err.Error(), "401")
}

func TestExchangeForJWT_ResponseFormats(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: device flow polling")
	}

	tests := []struct {
		name        string
		response    string
		wantToken   string
		wantErr     bool
		errContains string
	}{
		{
			name:      "flat OAuth-style response",
			response:  `{"access_token":"jwt-flat","token_type":"bearer","expires_in":3600}`,
			wantToken: "jwt-flat",
		},
		{
			name:      "enveloped data response from older server",
			response:  `{"success":true,"data":{"access_token":"jwt-envelope","token_type":"bearer","expires_in":3600}}`,
			wantToken: "jwt-envelope",
		},
		{
			name:        "empty access token returns error",
			response:    `{"token_type":"bearer","expires_in":3600}`,
			wantErr:     true,
			errContains: "empty access token",
		},
		{
			name:        "empty data envelope returns error",
			response:    `{"success":true,"data":{"token_type":"bearer"}}`,
			wantErr:     true,
			errContains: "empty access token",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				w.Write([]byte(tt.response))
			}))
			defer server.Close()

			client := &http.Client{Timeout: 5 * time.Second}
			resp, err := exchangeForJWT(client, server.URL, "test-opaque-token")

			if tt.wantErr {
				require.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.wantToken, resp.AccessToken)
		})
	}
}

func TestExchangeForJWT_RefreshTokenField(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: device flow polling")
	}

	// verify JWTExchangeResponse captures refresh_token from server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"access_token":  "jwt-token",
			"refresh_token": "jwt-refresh-token",
			"token_type":    "Bearer",
			"expires_in":    900,
		})
	}))
	defer server.Close()

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := exchangeForJWT(client, server.URL, "opaque-token")
	require.NoError(t, err)
	assert.Equal(t, "jwt-token", resp.AccessToken)
	assert.Equal(t, "jwt-refresh-token", resp.RefreshToken)
}

func TestLogin_JWTExchangeRefreshTokenFallback(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: device flow polling")
	}

	// Regression test: when device flow returns no refresh_token,
	// but JWT exchange does, the stored token should use the JWT exchange one.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch r.URL.Path {
		case DeviceTokenEndpoint:
			// device flow returns NO refresh_token
			json.NewEncoder(w).Encode(TokenResponse{
				AccessToken: "opaque-token",
				TokenType:   "Bearer",
				ExpiresIn:   3600,
				// RefreshToken intentionally empty
			})
		case "/api/v1/cli/auth/token":
			// JWT exchange DOES return a refresh_token
			json.NewEncoder(w).Encode(map[string]interface{}{
				"access_token":  "jwt-access",
				"refresh_token": "jwt-refresh-from-exchange",
				"token_type":    "Bearer",
				"expires_in":    900,
			})
		case UserInfoEndpoint:
			json.NewEncoder(w).Encode(UserInfo{
				UserID: "user-123",
				Email:  "test@example.com",
				Name:   "Test User",
			})
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := NewAuthClientWithDir(t.TempDir()).WithEndpoint(server.URL)

	deviceCode := &DeviceCodeResponse{
		DeviceCode:      "test-device-code",
		UserCode:        "ABCD-1234",
		VerificationURI: "https://example.com/verify",
		ExpiresIn:       300,
		Interval:        1,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	err := client.Login(ctx, deviceCode, nil)
	require.NoError(t, err)

	// verify the stored token picked up refresh_token from JWT exchange
	token, err := client.GetToken()
	require.NoError(t, err)
	require.NotNil(t, token)
	assert.Equal(t, "jwt-access", token.AccessToken)
	assert.Equal(t, "jwt-refresh-from-exchange", token.RefreshToken,
		"refresh_token should fall back to JWT exchange response when device flow doesn't provide one")
}

func TestLogin_JWTExchangeRefreshTokenFallback_WithSessionToken(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: device flow polling")
	}

	// Edge case: device flow returns session_token (no refresh_token),
	// JWT exchange returns refresh_token — JWT refresh_token should be preferred.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch r.URL.Path {
		case DeviceTokenEndpoint:
			// device flow returns session_token but NO refresh_token
			json.NewEncoder(w).Encode(map[string]interface{}{
				"access_token":  "opaque-token",
				"session_token": "device-session-token",
				"token_type":    "Bearer",
				"expires_in":    3600,
			})
		case "/api/v1/cli/auth/token":
			json.NewEncoder(w).Encode(map[string]interface{}{
				"access_token":  "jwt-access",
				"refresh_token": "jwt-refresh-from-exchange",
				"token_type":    "Bearer",
				"expires_in":    900,
			})
		case UserInfoEndpoint:
			json.NewEncoder(w).Encode(UserInfo{
				UserID: "user-123",
				Email:  "test@example.com",
				Name:   "Test User",
			})
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := NewAuthClientWithDir(t.TempDir()).WithEndpoint(server.URL)

	deviceCode := &DeviceCodeResponse{
		DeviceCode:      "test-device-code",
		UserCode:        "ABCD-1234",
		VerificationURI: "https://example.com/verify",
		ExpiresIn:       300,
		Interval:        1,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	err := client.Login(ctx, deviceCode, nil)
	require.NoError(t, err)

	token, err := client.GetToken()
	require.NoError(t, err)
	require.NotNil(t, token)
	assert.Equal(t, "jwt-access", token.AccessToken)
	assert.Equal(t, "jwt-refresh-from-exchange", token.RefreshToken,
		"JWT exchange refresh_token should be preferred over session_token fallback")
}
