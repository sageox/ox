package auth

import (
	"context"
	"encoding/json"
	"errors"
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

// tokenResponse represents the OAuth2 token endpoint response format.
// SessionToken is a Better Auth fallback for refresh_token.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	SessionToken string `json:"session_token"`
	ExpiresIn    int    `json:"expires_in"`
	TokenType    string `json:"token_type"`
	Scope        string `json:"scope"`
}

// effectiveRefreshToken returns the token to use for subsequent refresh grant requests.
//
// Better Auth compatibility: some server configurations return session_token instead
// of (or in addition to) a standard OAuth2 refresh_token. The session token is a
// long-lived credential that the server accepts as a refresh_token — i.e. sending it
// as grant_type=refresh_token is correct behavior, not a workaround.
// See StoredToken.EffectiveRefreshToken for the stored-credential counterpart.
func (t *tokenResponse) effectiveRefreshToken() string {
	if t.RefreshToken != "" {
		return t.RefreshToken
	}
	if t.SessionToken != "" {
		slog.Debug("server returned session_token instead of refresh_token (Better Auth mode), using as refresh credential")
		return t.SessionToken
	}
	return ""
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

// ErrLoggedOutDuringRefresh marks a token refresh whose stored login was
// removed, normally by `ox logout`, while the refresh waited on the server.
// The refreshed token is discarded so a finished logout stays finished, and
// revoked if the server rotated its refresh token. It comes wrapped in a
// TokenRefreshError.
var ErrLoggedOutDuringRefresh = errors.New("logged out during token refresh")

// refreshResult turns what saveTokenIfUnchanged left stored for ep into the
// refresh's answer, which is always the login on disk, as a fresh read would
// return it:
//   - refreshed was saved: refreshed.
//   - the login was removed: ErrLoggedOutDuringRefresh, which callers treat
//     like any failed refresh (re-authentication required). The caller has
//     already revoked a rotated refresh token (revokeDiscardedRefresh).
//   - the login changed (a concurrent refresh or `ox login` saved first): the
//     stored login, which is newer than the one this refresh started from.
//     Failing instead would break a command whose refresh merely lost a race
//     with the daemon's. When two processes refresh one login, the first to
//     save now wins; the blind save let the last one win.
func refreshResult(ep string, stored, refreshed *StoredToken) (*StoredToken, error) {
	if stored == nil {
		return nil, &TokenRefreshError{
			Message: "token refresh failed, re-authentication required",
			Err:     ErrLoggedOutDuringRefresh,
		}
	}
	if stored != refreshed {
		slog.Info("auth: login changed during token refresh, using the stored login", "endpoint", ep)
	}
	return stored, nil
}

// revokeDiscardedRefresh asks the server at baseURL to revoke the refreshed
// login a refresh discards because `ox logout` removed its login while the
// grant was on the server. sent is the refresh token the grant used, which the
// logout revoked. A server that rotates refresh tokens had already issued a
// new one, which would stay live until it expires with no local copy left to
// revoke. A refresh token the server kept, such as a Better Auth session
// token, is the one the logout revoked: revoking it again adds nothing, and
// could sign out that session if a login this refresh did not find on disk
// still uses it. Best effort: the refresh fails as logged out either way.
func revokeDiscardedRefresh(ctx context.Context, baseURL, sent string, discarded *StoredToken) {
	if discarded.RefreshToken == "" || discarded.RefreshToken == sent {
		return
	}
	status := revokeOnServerAt(ctx, baseURL, discarded)
	slog.Info("auth: asked the server to revoke the token refreshed during logout",
		"endpoint", endpoint.NormalizeEndpoint(baseURL), "confirmed", status == RevocationSuccess)
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

	// env-sourced tokens (SAGEOX_TOKEN) have no refresh credential —
	// the server returning 401 is the source of truth for invalidation.
	// Mirror EnsureValidTokenForEndpoint so callers on the non-endpoint
	// path do not attempt to refresh env tokens.
	if isEnvToken(endpoint.Get(), token) {
		return token, nil
	}

	// check if token needs refresh (expired or will expire within buffer)
	if token.IsExpired(bufferSeconds) {
		return refreshToken(token)
	}

	return token, nil
}

// EnsureValidTokenForEndpoint ensures we have a valid token for a specific endpoint,
// refreshing proactively if the token expires within bufferSeconds.
// Use this instead of GetTokenForEndpoint when the token will be used for API requests.
func EnsureValidTokenForEndpoint(ep string, bufferSeconds int) (*StoredToken, error) {
	return EnsureValidTokenForEndpointContext(context.Background(), ep, bufferSeconds)
}

// EnsureValidTokenForEndpointContext refreshes an endpoint's token while honoring
// the caller's cancellation and deadline through OAuth and JWT exchange.
func EnsureValidTokenForEndpointContext(ctx context.Context, ep string, bufferSeconds int) (*StoredToken, error) {
	token, err := GetTokenForEndpoint(ep)
	if err != nil {
		return nil, err
	}

	if token == nil {
		return nil, nil
	}

	// env-sourced tokens (SAGEOX_TOKEN) have no refresh credential —
	// the server returning 401 is the source of truth for invalidation.
	if isEnvToken(ep, token) {
		return token, nil
	}

	if token.IsExpired(bufferSeconds) {
		return refreshTokenForEndpointContext(ctx, token, ep)
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

// Handle401ErrorForEndpoint is Handle401Error scoped to a specific endpoint.
// Handle401Error hardcodes endpoint.Get() (the global default), which is wrong
// for callers bound to a per-project endpoint that can differ from it — e.g.
// queryTeamContext, which resolves ep via endpoint.GetForProject(projectRoot).
func Handle401ErrorForEndpoint(token *StoredToken, ep string) (*StoredToken, error) {
	return refreshTokenForEndpoint(token, ep)
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
	return refreshTokenForEndpoint(token, endpoint.Get())
}

// refreshTokenForEndpoint performs token refresh against a specific endpoint.
func refreshTokenForEndpoint(token *StoredToken, ep string) (*StoredToken, error) {
	return refreshTokenForEndpointContext(context.Background(), token, ep)
}

func refreshTokenForEndpointContext(ctx context.Context, token *StoredToken, ep string) (*StoredToken, error) {
	baseURL := ep
	tokenURL := baseURL + TokenEndpoint

	// use refresh_token with session_token fallback (Better Auth compatibility)
	rt := token.EffectiveRefreshToken()
	if rt == "" {
		return nil, &TokenRefreshError{
			Message: "no refresh token available, re-authentication required",
		}
	}

	// prepare form data for token refresh
	data := url.Values{}
	data.Set("grant_type", "refresh_token")
	data.Set("client_id", ClientID)
	data.Set("refresh_token", rt)

	// create request
	req, err := useragent.NewRequest(ctx, "POST", tokenURL, strings.NewReader(data.Encode()))
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

	// refresh_token may be rotated or stay the same; fall back to session_token
	refreshToken := token.RefreshToken
	if rt := responseData.effectiveRefreshToken(); rt != "" {
		refreshToken = rt
	}

	// preserve session_token from response, falling back to stored value
	sessionToken := token.SessionToken
	if responseData.SessionToken != "" {
		sessionToken = responseData.SessionToken
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
	jwtToken, err := exchangeForJWT(ctx, httpClient, baseURL, responseData.AccessToken)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

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

	// fallback: if OAuth2 response omitted refresh_token, prefer JWT exchange refresh_token when present
	if responseData.RefreshToken == "" && jwtToken != nil && jwtToken.RefreshToken != "" {
		refreshToken = jwtToken.RefreshToken
	}

	// calculate expiry time
	expiresAt := time.Now().UTC().Add(time.Duration(expiresIn) * time.Second)

	// create new token preserving user_info
	newToken := &StoredToken{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		SessionToken: sessionToken,
		ExpiresAt:    expiresAt,
		TokenType:    tokenType,
		Scope:        scope,
		UserInfo:     token.UserInfo, // preserve user info from original token
	}

	// save new token for the endpoint we refreshed against, unless its login
	// was removed or replaced while we waited on the server
	stored, err := saveTokenForEndpointIfUnchanged(baseURL, token, newToken)
	if err != nil {
		return nil, &TokenRefreshError{
			Message: "failed to save refreshed token",
			Err:     err,
		}
	}
	if stored == nil {
		revokeDiscardedRefresh(ctx, baseURL, rt, newToken)
	}

	return refreshResult(endpoint.NormalizeEndpoint(baseURL), stored, newToken)
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

	// env-sourced tokens (SAGEOX_TOKEN) have no refresh credential —
	// the server returning 401 is the source of truth for invalidation.
	if isEnvToken(c.Endpoint(), token) {
		return token, nil
	}

	// check if token needs refresh (expired or will expire within buffer)
	if token.IsExpired(bufferSeconds) {
		return c.refreshToken(token)
	}

	return token, nil
}

// EnsureValidTokenForEndpoint ensures we have a valid token for a specific endpoint,
// refreshing proactively if the token expires within bufferSeconds.
func (c *AuthClient) EnsureValidTokenForEndpoint(ep string, bufferSeconds int) (*StoredToken, error) {
	token, err := c.GetTokenForEndpoint(ep)
	if err != nil {
		return nil, err
	}

	if token == nil {
		return nil, nil
	}

	// env-sourced tokens (SAGEOX_TOKEN) have no refresh credential —
	// the server returning 401 is the source of truth for invalidation.
	if isEnvToken(ep, token) {
		return token, nil
	}

	if token.IsExpired(bufferSeconds) {
		// use endpoint-specific refresh by creating a temporary client
		epClient := c.WithEndpoint(ep)
		return epClient.refreshToken(token)
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

	// use refresh_token with session_token fallback (Better Auth compatibility)
	rt := token.EffectiveRefreshToken()
	if rt == "" {
		return nil, &TokenRefreshError{
			Message: "no refresh token available, re-authentication required",
		}
	}

	// prepare form data for token refresh
	data := url.Values{}
	data.Set("grant_type", "refresh_token")
	data.Set("client_id", ClientID)
	data.Set("refresh_token", rt)

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

	// refresh_token may be rotated or stay the same; fall back to session_token
	refreshTokenStr := token.RefreshToken
	if rt := responseData.effectiveRefreshToken(); rt != "" {
		refreshTokenStr = rt
	}

	// preserve session_token from response, falling back to stored value
	sessionTokenStr := token.SessionToken
	if responseData.SessionToken != "" {
		sessionTokenStr = responseData.SessionToken
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
	jwtToken, err := exchangeForJWT(context.Background(), httpClient, baseURL, responseData.AccessToken)

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

	// fallback: if OAuth2 response omitted refresh_token, prefer JWT exchange refresh_token when present
	if responseData.RefreshToken == "" && jwtToken != nil && jwtToken.RefreshToken != "" {
		refreshTokenStr = jwtToken.RefreshToken
	}

	// calculate expiry time
	expiresAt := time.Now().UTC().Add(time.Duration(expiresIn) * time.Second)

	// create new token preserving user_info
	newToken := &StoredToken{
		AccessToken:  accessToken,
		RefreshToken: refreshTokenStr,
		SessionToken: sessionTokenStr,
		ExpiresAt:    expiresAt,
		TokenType:    tokenType,
		Scope:        scope,
		UserInfo:     token.UserInfo, // preserve user info from original token
	}

	// save new token using client's storage, unless its login was removed or
	// replaced while we waited on the server
	ep := endpoint.NormalizeEndpoint(baseURL)
	stored, err := c.saveTokenIfUnchanged(ep, token, newToken)
	if err != nil {
		return nil, &TokenRefreshError{
			Message: "failed to save refreshed token",
			Err:     err,
		}
	}
	if stored == nil {
		revokeDiscardedRefresh(context.Background(), baseURL, rt, newToken)
	}

	return refreshResult(ep, stored, newToken)
}
