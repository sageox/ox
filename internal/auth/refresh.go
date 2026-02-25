package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/logger"
	"github.com/sageox/ox/internal/useragent"
)

const (
	// defaultTimeout for HTTP requests
	defaultTimeout = 30 * time.Second
)

// tokenErrorResponse represents the OAuth2 error response format
type tokenErrorResponse struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

// tokenResponse represents the OAuth2 token endpoint response format
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	TokenType    string `json:"token_type"`
	Scope        string `json:"scope"`
}

// TokenRefreshError represents an error that occurred during token refresh
type TokenRefreshError struct {
	Message string
	Err     error
}

func (e *TokenRefreshError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %v", e.Message, e.Err)
	}
	return e.Message
}

func (e *TokenRefreshError) Unwrap() error {
	return e.Err
}

// EnsureValidToken ensures we have a valid token, refreshing proactively if needed.
//
// Proactive refresh: If token expires within bufferSeconds (default 300 = 5 min),
// refresh it now to avoid expiration during a request.
//
// Returns:
//   - Valid StoredToken, or nil if not authenticated
//
// Raises:
//   - TokenRefreshError if refresh fails
func EnsureValidToken(bufferSeconds int) (*StoredToken, error) {
	token, err := GetToken()
	if err != nil {
		return nil, err
	}

	if token == nil {
		return nil, nil
	}

	// check if token needs refresh (expired or will expire within buffer)
	if token.IsExpired(bufferSeconds) {
		return refreshToken(token)
	}

	return token, nil
}

// Handle401Error handles 401 Unauthorized by attempting token refresh.
//
// Reactive refresh: Called when a request returns 401, attempt refresh
// and return new token for retry.
//
// Args:
//   - token: The current (possibly expired) token
//
// Returns:
//   - New valid StoredToken after refresh
//
// Raises:
//   - TokenRefreshError if refresh fails (user must re-authenticate)
func Handle401Error(token *StoredToken) (*StoredToken, error) {
	return refreshToken(token)
}

// refreshToken performs token refresh using refresh_token grant.
//
// Makes POST to token endpoint with:
//   - grant_type=refresh_token
//   - client_id
//   - refresh_token
//
// Returns new StoredToken with updated access_token and possibly new refresh_token.
// Preserves user_info from original token.
//
// Args:
//   - token: Current token containing refresh_token
//
// Returns:
//   - New StoredToken with refreshed credentials
//
// Raises:
//   - TokenRefreshError if refresh fails due to network, invalid token, or server error
func refreshToken(token *StoredToken) (*StoredToken, error) {
	baseURL := endpoint.Get()
	tokenURL := baseURL + TokenEndpoint

	// prepare form data for token refresh
	data := url.Values{}
	data.Set("grant_type", "refresh_token")
	data.Set("client_id", ClientID)
	data.Set("refresh_token", token.RefreshToken)

	// create request
	req, err := useragent.NewRequest(context.Background(), "POST", tokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, &TokenRefreshError{
			Message: "failed to create refresh request",
			Err:     err,
		}
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	logger.LogHTTPRequest("POST", tokenURL)
	start := time.Now()

	// make request
	client := &http.Client{
		Timeout: defaultTimeout,
	}

	resp, err := client.Do(req)
	duration := time.Since(start)
	if err != nil {
		logger.LogHTTPError("POST", tokenURL, err, duration)
		return nil, &TokenRefreshError{
			Message: "network error during token refresh",
			Err:     err,
		}
	}
	defer resp.Body.Close()

	// read response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &TokenRefreshError{
			Message: "failed to read refresh response",
			Err:     err,
		}
	}

	logger.LogHTTPResponse("POST", tokenURL, resp.StatusCode, duration)

	// handle HTTP errors
	if resp.StatusCode != http.StatusOK {
		// try to parse error message from response
		var errorData tokenErrorResponse
		errorMsg := fmt.Sprintf("HTTP %d: %s", resp.StatusCode, resp.Status)

		if jsonErr := json.Unmarshal(body, &errorData); jsonErr == nil {
			if errorData.ErrorDescription != "" {
				errorMsg = errorData.ErrorDescription
			} else if errorData.Error != "" {
				errorMsg = errorData.Error
			}
		}

		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusBadRequest {
			return nil, &TokenRefreshError{
				Message: fmt.Sprintf("token refresh failed, re-authentication required: %s", errorMsg),
			}
		}

		return nil, &TokenRefreshError{
			Message: fmt.Sprintf("token refresh failed with HTTP %d: %s", resp.StatusCode, errorMsg),
		}
	}

	// parse response
	var responseData tokenResponse
	if err := json.Unmarshal(body, &responseData); err != nil {
		return nil, &TokenRefreshError{
			Message: "token refresh failed: invalid JSON response from server",
			Err:     err,
		}
	}

	// extract token data from response
	if responseData.AccessToken == "" {
		return nil, &TokenRefreshError{
			Message: "token refresh failed: missing required field 'access_token' in response",
		}
	}

	// refresh_token may be rotated or stay the same
	refreshToken := token.RefreshToken
	if responseData.RefreshToken != "" {
		refreshToken = responseData.RefreshToken
	}

	// extract optional fields with defaults
	tokenType := "Bearer"
	if responseData.TokenType != "" {
		tokenType = responseData.TokenType
	}

	expiresIn := 3600
	if responseData.ExpiresIn > 0 {
		expiresIn = responseData.ExpiresIn
	}

	scope := token.Scope
	if responseData.Scope != "" {
		scope = responseData.Scope
	}

	// exchange opaque token for JWT (same as initial device flow)
	httpClient := &http.Client{Timeout: defaultTimeout}
	jwtToken, err := exchangeForJWT(httpClient, baseURL, responseData.AccessToken)

	accessToken := responseData.AccessToken
	if err != nil {
		slog.Warn("JWT exchange after refresh failed, using opaque token",
			"error", err.Error())
	} else if jwtToken.AccessToken == "" {
		slog.Warn("JWT exchange returned empty access_token, using opaque token",
			"endpoint", baseURL+"/api/v1/cli/auth/token")
	} else {
		accessToken = jwtToken.AccessToken
		// use JWT expiration if available
		if jwtToken.ExpiresIn > 0 {
			expiresIn = jwtToken.ExpiresIn
		}
	}

	// calculate expiry time
	expiresAt := time.Now().UTC().Add(time.Duration(expiresIn) * time.Second)

	// create new token preserving user_info
	newToken := &StoredToken{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ExpiresAt:    expiresAt,
		TokenType:    tokenType,
		Scope:        scope,
		UserInfo:     token.UserInfo, // preserve user info from original token
	}

	// save new token
	if err := SaveToken(newToken); err != nil {
		return nil, &TokenRefreshError{
			Message: "failed to save refreshed token",
			Err:     err,
		}
	}

	return newToken, nil
}

// AuthClient methods for test isolation (enable t.Parallel())

// EnsureValidToken ensures we have a valid token, refreshing proactively if needed.
func (c *AuthClient) EnsureValidToken(bufferSeconds int) (*StoredToken, error) {
	token, err := c.GetToken()
	if err != nil {
		return nil, err
	}

	if token == nil {
		return nil, nil
	}

	// check if token needs refresh (expired or will expire within buffer)
	if token.IsExpired(bufferSeconds) {
		return c.refreshToken(token)
	}

	return token, nil
}

// Handle401Error handles 401 Unauthorized by attempting token refresh.
func (c *AuthClient) Handle401Error(token *StoredToken) (*StoredToken, error) {
	return c.refreshToken(token)
}

// refreshToken performs token refresh using refresh_token grant.
func (c *AuthClient) refreshToken(token *StoredToken) (*StoredToken, error) {
	baseURL := c.Endpoint()
	tokenURL := baseURL + TokenEndpoint

	// prepare form data for token refresh
	data := url.Values{}
	data.Set("grant_type", "refresh_token")
	data.Set("client_id", ClientID)
	data.Set("refresh_token", token.RefreshToken)

	// create request
	req, err := useragent.NewRequest(context.Background(), "POST", tokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, &TokenRefreshError{
			Message: "failed to create refresh request",
			Err:     err,
		}
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	logger.LogHTTPRequest("POST", tokenURL)
	start := time.Now()

	// make request
	client := &http.Client{
		Timeout: defaultTimeout,
	}

	resp, err := client.Do(req)
	duration := time.Since(start)
	if err != nil {
		logger.LogHTTPError("POST", tokenURL, err, duration)
		return nil, &TokenRefreshError{
			Message: "network error during token refresh",
			Err:     err,
		}
	}
	defer resp.Body.Close()

	// read response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &TokenRefreshError{
			Message: "failed to read refresh response",
			Err:     err,
		}
	}

	logger.LogHTTPResponse("POST", tokenURL, resp.StatusCode, duration)

	// handle HTTP errors
	if resp.StatusCode != http.StatusOK {
		// try to parse error message from response
		var errorData tokenErrorResponse
		errorMsg := fmt.Sprintf("HTTP %d: %s", resp.StatusCode, resp.Status)

		if jsonErr := json.Unmarshal(body, &errorData); jsonErr == nil {
			if errorData.ErrorDescription != "" {
				errorMsg = errorData.ErrorDescription
			} else if errorData.Error != "" {
				errorMsg = errorData.Error
			}
		}

		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusBadRequest {
			return nil, &TokenRefreshError{
				Message: fmt.Sprintf("token refresh failed, re-authentication required: %s", errorMsg),
			}
		}

		return nil, &TokenRefreshError{
			Message: fmt.Sprintf("token refresh failed with HTTP %d: %s", resp.StatusCode, errorMsg),
		}
	}

	// parse response
	var responseData tokenResponse
	if err := json.Unmarshal(body, &responseData); err != nil {
		return nil, &TokenRefreshError{
			Message: "token refresh failed: invalid JSON response from server",
			Err:     err,
		}
	}

	// extract token data from response
	if responseData.AccessToken == "" {
		return nil, &TokenRefreshError{
			Message: "token refresh failed: missing required field 'access_token' in response",
		}
	}

	// refresh_token may be rotated or stay the same
	refreshTokenStr := token.RefreshToken
	if responseData.RefreshToken != "" {
		refreshTokenStr = responseData.RefreshToken
	}

	// extract optional fields with defaults
	tokenType := "Bearer"
	if responseData.TokenType != "" {
		tokenType = responseData.TokenType
	}

	expiresIn := 3600
	if responseData.ExpiresIn > 0 {
		expiresIn = responseData.ExpiresIn
	}

	scope := token.Scope
	if responseData.Scope != "" {
		scope = responseData.Scope
	}

	// exchange opaque token for JWT (same as initial device flow)
	httpClient := &http.Client{Timeout: defaultTimeout}
	jwtToken, err := exchangeForJWT(httpClient, baseURL, responseData.AccessToken)

	accessToken := responseData.AccessToken
	if err != nil {
		slog.Warn("JWT exchange after refresh failed, using opaque token",
			"error", err.Error())
	} else if jwtToken.AccessToken == "" {
		slog.Warn("JWT exchange returned empty access_token, using opaque token",
			"endpoint", baseURL+"/api/v1/cli/auth/token")
	} else {
		accessToken = jwtToken.AccessToken
		// use JWT expiration if available
		if jwtToken.ExpiresIn > 0 {
			expiresIn = jwtToken.ExpiresIn
		}
	}

	// calculate expiry time
	expiresAt := time.Now().UTC().Add(time.Duration(expiresIn) * time.Second)

	// create new token preserving user_info
	newToken := &StoredToken{
		AccessToken:  accessToken,
		RefreshToken: refreshTokenStr,
		ExpiresAt:    expiresAt,
		TokenType:    tokenType,
		Scope:        scope,
		UserInfo:     token.UserInfo, // preserve user info from original token
	}

	// save new token using client's storage
	if err := c.SaveToken(newToken); err != nil {
		return nil, &TokenRefreshError{
			Message: "failed to save refreshed token",
			Err:     err,
		}
	}

	return newToken, nil
}
