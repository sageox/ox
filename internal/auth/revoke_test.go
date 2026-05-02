package auth

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ======
// RevokeToken Tests - Logout path
// Called by: ox logout
// ======

func TestRevokeOnServer_Success(t *testing.T) {
	t.Parallel()

	var receivedToken string
	var receivedHint string
	var receivedClientID string

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// verify POST method
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "application/x-www-form-urlencoded", r.Header.Get("Content-Type"))

		// parse form data
		err := r.ParseForm()
		require.NoError(t, err)

		receivedToken = r.FormValue("token")
		receivedHint = r.FormValue("token_type_hint")
		receivedClientID = r.FormValue("client_id")

		// RFC 7009 says return 200 for successful revocation
		w.WriteHeader(http.StatusOK)
	}))
	defer mockServer.Close()

	client := &AuthClient{
		endpoint: mockServer.URL,
	}

	token := &StoredToken{
		AccessToken:  "access-token-123",
		RefreshToken: "refresh-token-456",
	}

	success := client.revokeOnServer(token)

	assert.True(t, success)
	// should prefer revoking refresh token
	assert.Equal(t, "refresh-token-456", receivedToken)
	assert.Equal(t, "refresh_token", receivedHint)
	assert.Equal(t, ClientID, receivedClientID)
}

func TestRevokeOnServer_FallsBackToAccessToken(t *testing.T) {
	t.Parallel()

	var receivedToken string
	var receivedHint string

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		err := r.ParseForm()
		require.NoError(t, err)

		receivedToken = r.FormValue("token")
		receivedHint = r.FormValue("token_type_hint")

		w.WriteHeader(http.StatusOK)
	}))
	defer mockServer.Close()

	client := &AuthClient{
		endpoint: mockServer.URL,
	}

	// token with no refresh token
	token := &StoredToken{
		AccessToken: "access-token-only",
		// RefreshToken is empty
	}

	success := client.revokeOnServer(token)

	assert.True(t, success)
	assert.Equal(t, "access-token-only", receivedToken)
	assert.Equal(t, "access_token", receivedHint)
}

func TestRevokeOnServer_HTTP400_ReturnsFalse(t *testing.T) {
	t.Parallel()

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"invalid_token"}`))
	}))
	defer mockServer.Close()

	client := &AuthClient{
		endpoint: mockServer.URL,
	}

	token := &StoredToken{
		AccessToken:  "invalid-token",
		RefreshToken: "invalid-refresh",
	}

	success := client.revokeOnServer(token)

	assert.False(t, success, "400 should return false")
}

func TestRevokeOnServer_HTTP401_ReturnsFalse(t *testing.T) {
	t.Parallel()

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer mockServer.Close()

	client := &AuthClient{
		endpoint: mockServer.URL,
	}

	token := &StoredToken{
		AccessToken: "some-token",
	}

	success := client.revokeOnServer(token)

	assert.False(t, success)
}

func TestRevokeOnServer_HTTP500_ReturnsFalse(t *testing.T) {
	t.Parallel()

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"server error"}`))
	}))
	defer mockServer.Close()

	client := &AuthClient{
		endpoint: mockServer.URL,
	}

	token := &StoredToken{
		AccessToken:  "some-token",
		RefreshToken: "some-refresh",
	}

	success := client.revokeOnServer(token)

	assert.False(t, success)
}

func TestRevokeOnServer_NetworkError_ReturnsFalse(t *testing.T) {
	t.Parallel()

	client := &AuthClient{
		endpoint: "http://localhost:99999", // invalid port
	}

	token := &StoredToken{
		AccessToken:  "some-token",
		RefreshToken: "some-refresh",
	}

	success := client.revokeOnServer(token)

	assert.False(t, success, "network error should return false")
}

func TestRevokeOnServer_HandlesTrailingSlash(t *testing.T) {
	t.Parallel()

	var receivedPath string

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer mockServer.Close()

	// endpoint with trailing slash
	client := &AuthClient{
		endpoint: mockServer.URL + "/",
	}

	token := &StoredToken{
		AccessToken: "token",
	}

	_ = client.revokeOnServer(token)

	// should not have double slashes
	assert.Equal(t, RevokeEndpoint, receivedPath)
}

func TestRevokeToken_NoToken_ReturnsTrue(t *testing.T) {
	t.Parallel()

	// setup temp directory for token storage
	tmpDir, err := os.MkdirTemp("", "auth-revoke-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	client := NewAuthClientWithDir(tmpDir)

	// no token file exists
	success, err := client.RevokeToken()

	assert.NoError(t, err)
	assert.True(t, success, "no token = success")
}

func TestRevokeToken_RemovesLocalToken(t *testing.T) {
	t.Parallel()

	// setup temp directory for token storage
	tmpDir, err := os.MkdirTemp("", "auth-revoke-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	// create a mock server that returns success
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer mockServer.Close()

	client := NewAuthClientWithDir(tmpDir)
	client.endpoint = mockServer.URL

	// write a test token
	token := &StoredToken{
		AccessToken:  "test-token",
		RefreshToken: "test-refresh",
		ExpiresAt:    time.Now().Add(1 * time.Hour),
	}
	err = client.SaveToken(token)
	require.NoError(t, err)

	// verify token exists
	loadedToken, err := client.GetToken()
	require.NoError(t, err)
	require.NotNil(t, loadedToken, "token should exist before revoke")

	// revoke
	success, err := client.RevokeToken()

	assert.NoError(t, err)
	assert.True(t, success)

	// verify token was removed from store
	loadedToken, err = client.GetToken()
	assert.NoError(t, err)
	assert.Nil(t, loadedToken, "token should be removed after revoke")
}

func TestRevokeToken_RemovesLocalTokenEvenIfServerFails(t *testing.T) {
	t.Parallel()

	// setup temp directory for token storage
	tmpDir, err := os.MkdirTemp("", "auth-revoke-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	// create a mock server that fails
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer mockServer.Close()

	client := NewAuthClientWithDir(tmpDir)
	client.endpoint = mockServer.URL

	// write a test token
	token := &StoredToken{
		AccessToken: "test-token",
	}
	err = client.SaveToken(token)
	require.NoError(t, err)

	// verify token exists before revoke
	loadedToken, err := client.GetToken()
	require.NoError(t, err)
	require.NotNil(t, loadedToken, "token should exist before revoke")

	// revoke - server fails
	success, err := client.RevokeToken()

	// no error, but success=false because server failed
	assert.NoError(t, err)
	assert.False(t, success, "server revocation failed")

	// local token should still be removed from store
	loadedToken, err = client.GetToken()
	assert.NoError(t, err)
	assert.Nil(t, loadedToken, "token should be removed even if server fails")
}

func TestRevokeOnServer_SendsCorrectFormData(t *testing.T) {
	t.Parallel()

	var receivedForm url.Values

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		err := r.ParseForm()
		require.NoError(t, err)
		receivedForm = r.Form
		w.WriteHeader(http.StatusOK)
	}))
	defer mockServer.Close()

	client := &AuthClient{
		endpoint: mockServer.URL,
	}

	token := &StoredToken{
		AccessToken:  "access123",
		RefreshToken: "refresh456",
	}

	success := client.revokeOnServer(token)

	assert.True(t, success)

	// verify all form fields
	assert.Equal(t, "refresh456", receivedForm.Get("token"))
	assert.Equal(t, "refresh_token", receivedForm.Get("token_type_hint"))
	assert.Equal(t, ClientID, receivedForm.Get("client_id"))
}

func TestRevokeOnServer_RFC7009Compliance(t *testing.T) {
	t.Parallel()

	// RFC 7009 says the server SHOULD return 200 even for invalid tokens
	// The client should treat 200 as success regardless of body

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// return 200 even though we might say "token not found" in body
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"message":"token not found"}`))
	}))
	defer mockServer.Close()

	client := &AuthClient{
		endpoint: mockServer.URL,
	}

	token := &StoredToken{
		AccessToken: "already-revoked-token",
	}

	success := client.revokeOnServer(token)

	// 200 = success per RFC 7009
	assert.True(t, success)
}

// ======
// RevokeTokenStatus Tests - Structured revocation status
// ======

func TestRevokeTokenStatus_Success(t *testing.T) {
	t.Parallel()

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer mockServer.Close()

	tmpDir, err := os.MkdirTemp("", "auth-revoke-status-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	client := NewAuthClientWithDir(tmpDir)
	client.endpoint = mockServer.URL

	err = client.SaveToken(&StoredToken{
		AccessToken:  "test",
		RefreshToken: "refresh",
		ExpiresAt:    time.Now().Add(1 * time.Hour),
	})
	require.NoError(t, err)

	status, err := client.RevokeTokenStatus()
	assert.NoError(t, err)
	assert.Equal(t, RevocationSuccess, status)
}

func TestRevokeTokenStatus_NoToken(t *testing.T) {
	t.Parallel()

	tmpDir, err := os.MkdirTemp("", "auth-revoke-status-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	client := NewAuthClientWithDir(tmpDir)
	status, err := client.RevokeTokenStatus()
	assert.NoError(t, err)
	assert.Equal(t, RevocationNoToken, status)
}

func TestRevokeTokenStatus_ServerFailed(t *testing.T) {
	t.Parallel()

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer mockServer.Close()

	tmpDir, err := os.MkdirTemp("", "auth-revoke-status-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	client := NewAuthClientWithDir(tmpDir)
	client.endpoint = mockServer.URL

	err = client.SaveToken(&StoredToken{AccessToken: "test", ExpiresAt: time.Now().Add(1 * time.Hour)})
	require.NoError(t, err)

	status, err := client.RevokeTokenStatus()
	assert.NoError(t, err)
	assert.Equal(t, RevocationServerFailed, status)

	// token should still be removed locally
	tok, err := client.GetToken()
	assert.NoError(t, err)
	assert.Nil(t, tok)
}

func TestRevokeTokenStatus_NetworkError(t *testing.T) {
	t.Parallel()

	tmpDir, err := os.MkdirTemp("", "auth-revoke-status-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	client := NewAuthClientWithDir(tmpDir)
	client.endpoint = "http://localhost:99999"

	err = client.SaveToken(&StoredToken{AccessToken: "test", ExpiresAt: time.Now().Add(1 * time.Hour)})
	require.NoError(t, err)

	status, err := client.RevokeTokenStatus()
	assert.NoError(t, err)
	assert.Equal(t, RevocationNetworkError, status)
}
