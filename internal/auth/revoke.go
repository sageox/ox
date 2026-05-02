package auth

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/logger"
)

// RevokeToken revokes token and removes from local storage.
//
// Best-effort server revocation:
//   - Prefers revoking refresh_token (more secure, invalidates both tokens)
//   - Falls back to access_token if refresh_token missing
//   - Always removes local token regardless of server response
//
// Returns:
//   - true if server revocation succeeded (or no token to revoke)
//   - false if server revocation failed
//   - error if local token removal fails
//
// Local token is always removed regardless of return value (unless removal itself fails).
func RevokeToken() (bool, error) {
	return defaultClient.RevokeToken()
}

// RevokeToken revokes token and removes from local storage for this client.
//
// Best-effort server revocation:
//   - Prefers revoking refresh_token (more secure, invalidates both tokens)
//   - Falls back to access_token if refresh_token missing
//   - Always removes local token regardless of server response
//
// Returns:
//   - true if server revocation succeeded (or no token to revoke)
//   - false if server revocation failed
//   - error if local token removal fails
//
// Local token is always removed regardless of return value (unless removal itself fails).
func (c *AuthClient) RevokeToken() (bool, error) {
	token, err := c.GetToken()
	if err != nil {
		return false, fmt.Errorf("failed to load token: %w", err)
	}

	// no token to revoke, local is already clean
	if token == nil {
		return true, nil
	}

	// best-effort server revocation
	serverSuccess := c.revokeOnServer(token)

	// always remove local token regardless of server response
	if err := c.RemoveToken(); err != nil {
		return serverSuccess, fmt.Errorf("failed to remove local token: %w", err)
	}

	return serverSuccess, nil
}

// revokeOnServer attempts to revoke token on server.
//
// POST to revoke endpoint with:
//   - token: the token to revoke (prefer refresh_token)
//   - token_type_hint: 'refresh_token' or 'access_token'
//   - client_id
//
// Returns true on success (200 status), false on any error.
// Per RFC 7009, revocation endpoint should return 200 even for invalid tokens.
func (c *AuthClient) revokeOnServer(token *StoredToken) bool {
	baseURL := c.endpoint
	if baseURL == "" {
		baseURL = endpoint.Get()
	}
	// strip trailing slash to avoid double slashes in URL
	baseURL = strings.TrimSuffix(baseURL, "/")
	revokeURL := baseURL + RevokeEndpoint

	// prefer revoking refresh_token (invalidates both tokens per OAuth spec)
	tokenToRevoke := token.RefreshToken
	tokenTypeHint := "refresh_token"
	if tokenToRevoke == "" {
		tokenToRevoke = token.AccessToken
		tokenTypeHint = "access_token"
	}

	// prepare form data
	data := url.Values{}
	data.Set("token", tokenToRevoke)
	data.Set("token_type_hint", tokenTypeHint)
	data.Set("client_id", ClientID)

	// create HTTP client with 10 second timeout
	client := &http.Client{
		Timeout: 10 * time.Second,
	}

	logger.LogHTTPRequest("POST", revokeURL)
	start := time.Now()

	// make POST request to revocation endpoint
	resp, err := client.Post(
		revokeURL,
		"application/x-www-form-urlencoded",
		strings.NewReader(data.Encode()),
	)
	duration := time.Since(start)
	if err != nil {
		logger.LogHTTPError("POST", revokeURL, err, duration)
		// network error (DNS, connection refused, timeout, etc.)
		return false
	}
	defer resp.Body.Close()

	logger.LogHTTPResponse("POST", revokeURL, resp.StatusCode, duration)

	// 200 status means success
	// per RFC 7009 section 2.2, server should return 200 even for invalid tokens
	return resp.StatusCode == http.StatusOK
}

// RevocationStatus describes the outcome of a server-side token revocation.
type RevocationStatus int

const (
	// RevocationSuccess means the server confirmed the token is revoked.
	RevocationSuccess RevocationStatus = iota
	// RevocationNoToken means there was no token to revoke (already clean).
	RevocationNoToken
	// RevocationServerFailed means the server did not confirm revocation.
	// The token may still be valid server-side.
	RevocationServerFailed
	// RevocationNetworkError means we couldn't reach the server.
	RevocationNetworkError
)

// RevokeTokenStatus revokes token and removes from local storage,
// returning a structured status instead of a boolean.
func (c *AuthClient) RevokeTokenStatus() (RevocationStatus, error) {
	token, err := c.GetToken()
	if err != nil {
		return RevocationServerFailed, fmt.Errorf("failed to load token: %w", err)
	}

	if token == nil {
		return RevocationNoToken, nil
	}

	status := c.revokeOnServerStatus(token)

	if err := c.RemoveToken(); err != nil {
		return status, fmt.Errorf("failed to remove local token: %w", err)
	}

	return status, nil
}

// revokeOnServerStatus attempts revocation and returns structured status.
func (c *AuthClient) revokeOnServerStatus(token *StoredToken) RevocationStatus {
	baseURL := c.endpoint
	if baseURL == "" {
		baseURL = endpoint.Get()
	}
	baseURL = strings.TrimSuffix(baseURL, "/")
	revokeURL := baseURL + RevokeEndpoint

	tokenToRevoke := token.RefreshToken
	tokenTypeHint := "refresh_token"
	if tokenToRevoke == "" {
		tokenToRevoke = token.AccessToken
		tokenTypeHint = "access_token"
	}

	data := url.Values{}
	data.Set("token", tokenToRevoke)
	data.Set("token_type_hint", tokenTypeHint)
	data.Set("client_id", ClientID)

	client := &http.Client{
		Timeout: 10 * time.Second,
	}

	logger.LogHTTPRequest("POST", revokeURL)
	start := time.Now()

	resp, err := client.Post(
		revokeURL,
		"application/x-www-form-urlencoded",
		strings.NewReader(data.Encode()),
	)
	duration := time.Since(start)
	if err != nil {
		logger.LogHTTPError("POST", revokeURL, err, duration)
		return RevocationNetworkError
	}
	defer resp.Body.Close()

	logger.LogHTTPResponse("POST", revokeURL, resp.StatusCode, duration)

	if resp.StatusCode == http.StatusOK {
		return RevocationSuccess
	}
	return RevocationServerFailed
}
