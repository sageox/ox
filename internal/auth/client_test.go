package auth

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sageox/ox/internal/useragent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAuthenticatedRequest_NoToken(t *testing.T) {
	t.Parallel()

	client := NewAuthClientWithDir(t.TempDir())

	// no token saved, should return AuthenticationError
	_, err := client.AuthenticatedRequest(context.Background(), "GET", "https://example.com/api/test", nil)

	require.Error(t, err, "want AuthenticationError when not logged in")

	authErr, ok := err.(*AuthenticationError)
	require.True(t, ok, "error type = %T, want *AuthenticationError", err)

	assert.Contains(t, authErr.Message, "not logged in")
}

func TestAuthenticatedRequest_Success(t *testing.T) {
	t.Parallel()

	// create mock API server that expects Bearer token
	var receivedAuthHeader string
	var receivedUserAgent string
	mockAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuthHeader = r.Header.Get("Authorization")
		receivedUserAgent = r.Header.Get("User-Agent")

		w.Header().Set("Content-Type", "application/json")
		response := map[string]interface{}{
			"status": "success",
			"data":   "test data",
		}
		json.NewEncoder(w).Encode(response)
	}))
	defer mockAPI.Close()

	client := NewAuthClientWithDir(t.TempDir()).WithEndpoint(mockAPI.URL)

	// save valid token
	token := createTestToken(1 * time.Hour)
	token.AccessToken = "valid-access-token"
	require.NoError(t, client.SaveToken(token))

	useragent.ResetForTesting()
	t.Cleanup(useragent.ResetForTesting)
	expectedUserAgent := useragent.String()

	// make authenticated request
	resp, err := client.AuthenticatedRequest(context.Background(), "GET", mockAPI.URL+"/test", nil)
	require.NoError(t, err)
	require.NotNil(t, resp, "AuthenticatedRequest() returned nil response")

	// verify Authorization header was set correctly
	expectedAuth := "Bearer valid-access-token"
	assert.Equal(t, expectedAuth, receivedAuthHeader)

	// verify User-Agent was set
	assert.Equal(t, expectedUserAgent, receivedUserAgent)

	// verify response
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.True(t, resp.Ok(), "Ok() = false, want true for 200 response")

	// verify response data was parsed
	assert.NotNil(t, resp.Data, "Data = nil, want parsed JSON")
}

func TestAuthenticatedRequest_401Retry(t *testing.T) {
	t.Parallel()

	// track request count
	requestCount := 0
	var firstToken string
	var secondToken string

	// mock API that returns 401 on first request, 200 on second
	mockAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		authHeader := r.Header.Get("Authorization")

		if requestCount == 1 {
			firstToken = authHeader
			// first request: return 401
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]string{
				"error": "token expired",
			})
		} else {
			secondToken = authHeader
			// second request: return success
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{
				"status": "success",
			})
		}
	}))
	defer mockAPI.Close()

	// create mock refresh server that handles both OAuth2 refresh and JWT exchange
	mockRefresh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch r.URL.Path {
		case "/oauth2/token":
			// verify it's a refresh request
			require.NoError(t, r.ParseForm())
			assert.Equal(t, "refresh_token", r.Form.Get("grant_type"))

			// return new opaque token
			response := map[string]interface{}{
				"access_token":  "refreshed-opaque-token",
				"refresh_token": "new-refresh-token",
				"token_type":    "Bearer",
				"expires_in":    3600,
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
			t.Errorf("unexpected endpoint on refresh server: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer mockRefresh.Close()

	// create client with isolated storage and mock refresh endpoint
	client := NewTestClient(t).WithEndpoint(mockRefresh.URL)

	// save token that will trigger 401
	token := createTestToken(1 * time.Hour)
	token.AccessToken = "old-access-token"
	require.NoError(t, client.SaveToken(token))

	// make request (should retry after 401)
	resp, err := client.AuthenticatedRequest(context.Background(), "GET", mockAPI.URL+"/test", nil)
	require.NoError(t, err)

	// verify request was retried
	assert.Equal(t, 2, requestCount, "want 2 (original + retry)")

	// verify first request used old token
	assert.Contains(t, firstToken, "old-access-token")

	// verify second request used refreshed JWT token
	assert.Contains(t, secondToken, "refreshed-jwt-token")

	// verify final response is successful
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// verify token was saved with refreshed JWT value
	savedToken, err := client.GetToken()
	require.NoError(t, err)
	assert.Equal(t, "refreshed-jwt-token", savedToken.AccessToken)
}

func TestAuthenticatedRequest_401RefreshFails(t *testing.T) {
	t.Parallel()

	// mock refresh server that returns error
	mockRefresh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		response := map[string]interface{}{
			"error":             "invalid_grant",
			"error_description": "refresh token expired",
		}
		json.NewEncoder(w).Encode(response)
	}))
	defer mockRefresh.Close()

	client := NewAuthClientWithDir(t.TempDir()).WithEndpoint(mockRefresh.URL)

	// save token
	token := createTestToken(1 * time.Hour)
	require.NoError(t, client.SaveToken(token))

	// attempt refresh (simulates what happens on 401 response)
	_, err := client.Handle401Error(token)
	require.Error(t, err, "want TokenRefreshError when refresh fails")

	refreshErr, ok := err.(*TokenRefreshError)
	require.True(t, ok, "error type = %T, want *TokenRefreshError", err)

	assert.Contains(t, refreshErr.Message, "re-authentication required")
}

func TestAPIResponse_Ok(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		statusCode int
		want       bool
	}{
		{"200 OK", http.StatusOK, true},
		{"201 Created", http.StatusCreated, true},
		{"202 Accepted", http.StatusAccepted, true},
		{"204 No Content", http.StatusNoContent, true},
		{"299 (edge of 2xx)", 299, true},
		{"300 Multiple Choices", http.StatusMultipleChoices, false},
		{"400 Bad Request", http.StatusBadRequest, false},
		{"401 Unauthorized", http.StatusUnauthorized, false},
		{"404 Not Found", http.StatusNotFound, false},
		{"500 Internal Server Error", http.StatusInternalServerError, false},
		{"199 (before 2xx)", 199, false},
	}

	for _, tt := range tests {
		tt := tt // capture range variable
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			resp := &APIResponse{
				StatusCode: tt.statusCode,
			}

			got := resp.Ok()
			assert.Equal(t, tt.want, got, "for status %d", tt.statusCode)
		})
	}
}

func TestMakeRequest_JSON(t *testing.T) {
	t.Parallel()

	var receivedContentType string
	var receivedBody string

	// mock server to capture request
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedContentType = r.Header.Get("Content-Type")

		// read body
		body, _ := io.ReadAll(r.Body)
		receivedBody = string(body)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}))
	defer mockServer.Close()

	// prepare test data
	testData := map[string]interface{}{
		"name":  "Test User",
		"email": "test@example.com",
		"count": 42,
	}

	// make request with JSON data
	resp, err := makeRequest(context.Background(), "POST", mockServer.URL, "test-token", testData)
	require.NoError(t, err)

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// verify Content-Type header was set
	assert.Equal(t, "application/json", receivedContentType)

	// verify body is valid JSON
	var parsedBody map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(receivedBody), &parsedBody), "received body is not valid JSON")

	// verify data was serialized correctly
	assert.Equal(t, "Test User", parsedBody["name"])
	assert.Equal(t, "test@example.com", parsedBody["email"])
	assert.Equal(t, float64(42), parsedBody["count"])
}

func TestMakeRequest_NoData(t *testing.T) {
	t.Parallel()

	var receivedContentType string
	var hasBody bool

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedContentType = r.Header.Get("Content-Type")
		hasBody = r.ContentLength > 0

		w.WriteHeader(http.StatusOK)
	}))
	defer mockServer.Close()

	// make request without data
	resp, err := makeRequest(context.Background(), "GET", mockServer.URL, "test-token", nil)
	require.NoError(t, err)

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// verify Content-Type is not set when no data
	assert.Empty(t, receivedContentType, "want empty string when no data")

	// verify no body was sent
	assert.False(t, hasBody, "want no body when data is nil")
}

func TestMakeRequest_UserAgent(t *testing.T) {
	t.Parallel()

	var receivedUserAgent string

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedUserAgent = r.Header.Get("User-Agent")
		w.WriteHeader(http.StatusOK)
	}))
	defer mockServer.Close()

	useragent.ResetForTesting()
	t.Cleanup(useragent.ResetForTesting)
	expectedUserAgent := useragent.String()

	// make request
	_, err := makeRequest(context.Background(), "GET", mockServer.URL, "test-token", nil)
	require.NoError(t, err)

	// verify User-Agent header
	assert.Equal(t, expectedUserAgent, receivedUserAgent)
}

func TestMakeRequest_ResponseParsing(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		contentType string
		body        string
		wantData    bool
		description string
	}{
		{
			name:        "JSON response",
			contentType: "application/json",
			body:        `{"status": "success", "count": 42}`,
			wantData:    true,
			description: "should parse JSON response",
		},
		{
			name:        "JSON with charset",
			contentType: "application/json; charset=utf-8",
			body:        `{"key": "value"}`,
			wantData:    true,
			description: "should parse JSON with charset",
		},
		{
			name:        "plain text response",
			contentType: "text/plain",
			body:        "plain text response",
			wantData:    false,
			description: "should not parse non-JSON",
		},
		{
			name:        "empty response",
			contentType: "application/json",
			body:        "",
			wantData:    false,
			description: "should handle empty JSON response",
		},
		{
			name:        "invalid JSON",
			contentType: "application/json",
			body:        "{ invalid json }",
			wantData:    false,
			description: "should handle invalid JSON gracefully",
		},
	}

	for _, tt := range tests {
		tt := tt // capture range variable
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tt.contentType != "" {
					w.Header().Set("Content-Type", tt.contentType)
				}
				w.Write([]byte(tt.body))
			}))
			defer mockServer.Close()

			resp, err := makeRequest(context.Background(), "GET", mockServer.URL, "test-token", nil)
			require.NoError(t, err)

			if tt.wantData {
				assert.NotNil(t, resp.Data, "want parsed data (%s)", tt.description)
			} else {
				assert.Nil(t, resp.Data, "want nil (%s)", tt.description)
			}
		})
	}
}

func TestMakeRequest_HTTPMethods(t *testing.T) {
	t.Parallel()

	methods := []string{"GET", "POST", "PUT", "DELETE", "PATCH"}

	for _, method := range methods {
		method := method // capture range variable
		t.Run(method, func(t *testing.T) {
			t.Parallel()
			var receivedMethod string

			mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				receivedMethod = r.Method
				w.WriteHeader(http.StatusOK)
			}))
			defer mockServer.Close()

			_, err := makeRequest(context.Background(), method, mockServer.URL, "test-token", nil)
			require.NoError(t, err)

			assert.Equal(t, method, receivedMethod)
		})
	}
}

func TestMakeRequest_NetworkError(t *testing.T) {
	t.Parallel()

	// use invalid URL to trigger network error
	_, err := makeRequest(context.Background(), "GET", "http://invalid-host-does-not-exist:99999/test", "test-token", nil)

	require.Error(t, err, "want network error")

	apiErr, ok := err.(*APIError)
	require.True(t, ok, "error type = %T, want *APIError", err)

	assert.Equal(t, 0, apiErr.StatusCode, "want 0 for network error")
	assert.Contains(t, apiErr.Message, "network error")
}

func TestMakeRequest_InvalidJSON(t *testing.T) {
	t.Parallel()

	// data that cannot be marshaled to JSON
	invalidData := make(chan int) // channels cannot be marshaled to JSON

	_, err := makeRequest(context.Background(), "POST", "http://example.com", "test-token", invalidData)

	require.Error(t, err, "want marshal error for invalid data")
	assert.Contains(t, err.Error(), "failed to marshal request data")
}

func TestAuthenticatedRequest_ProactiveRefresh(t *testing.T) {
	t.Parallel()

	var apiCallCount int

	// mock API server
	mockAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiCallCount++
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}))
	defer mockAPI.Close()

	// mock refresh server
	mockRefresh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		response := map[string]interface{}{
			"access_token":  "proactively-refreshed-token",
			"refresh_token": "new-refresh",
			"token_type":    "Bearer",
			"expires_in":    3600,
		}
		json.NewEncoder(w).Encode(response)
	}))
	defer mockRefresh.Close()

	client := NewAuthClientWithDir(t.TempDir()).WithEndpoint(mockRefresh.URL)

	// create token expiring in 4 minutes (within 5-minute buffer)
	token := createTestToken(4 * time.Minute)
	token.AccessToken = "about-to-expire-token"
	require.NoError(t, client.SaveToken(token))

	// make request (should proactively refresh before making API call)
	resp, err := client.AuthenticatedRequest(context.Background(), "GET", mockAPI.URL+"/test", nil)
	require.NoError(t, err)

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// verify only one API call was made (no retry needed)
	assert.Equal(t, 1, apiCallCount, "proactive refresh should prevent retry")

	// verify token was refreshed
	savedToken, err := client.GetToken()
	require.NoError(t, err)
	assert.Equal(t, "proactively-refreshed-token", savedToken.AccessToken)
}

func TestAuthenticationError_Error(t *testing.T) {
	t.Parallel()

	err := &AuthenticationError{
		Message: "test authentication error",
	}

	expected := "test authentication error"
	assert.Equal(t, expected, err.Error())
}

func TestAPIError_Error(t *testing.T) {
	t.Parallel()

	err := &APIError{
		StatusCode: 404,
		Message:    "resource not found",
	}

	expected := "API error 404: resource not found"
	assert.Equal(t, expected, err.Error())
}

func TestMakeRequest_HeadersPreserved(t *testing.T) {
	t.Parallel()

	var headers http.Header

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headers = r.Header
		w.Header().Set("X-Custom-Header", "test-value")
		w.Header().Set("X-Request-ID", "12345")
		w.WriteHeader(http.StatusOK)
	}))
	defer mockServer.Close()

	resp, err := makeRequest(context.Background(), "GET", mockServer.URL, "test-token", nil)
	require.NoError(t, err)

	// verify request headers were set
	assert.Equal(t, "Bearer test-token", headers.Get("Authorization"))

	// verify response headers are accessible
	assert.Equal(t, "test-value", resp.Headers.Get("X-Custom-Header"))
	assert.Equal(t, "12345", resp.Headers.Get("X-Request-ID"))
}
