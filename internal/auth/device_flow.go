package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/logger"
	"github.com/sageox/ox/internal/useragent"
)

// authHTTPTimeout is the timeout for authentication HTTP requests.
// Set to 60 seconds to accommodate slow backend startup (e.g., devcontainer compilation).
const authHTTPTimeout = 60 * time.Second

// DeviceCodeResponse represents the response from the device code endpoint
type DeviceCodeResponse struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

// TokenResponse represents the response from the token endpoint
type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	Scope        string `json:"scope"`
}

// ErrorResponse represents an error response from the API
type ErrorResponse struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

// DeviceCodeRequest represents the request body for device code endpoint
type DeviceCodeRequest struct {
	ClientID string `json:"client_id"`
	Scope    string `json:"scope"`
}

// RequestDeviceCode initiates the device authorization flow
func RequestDeviceCode() (*DeviceCodeResponse, error) {
	apiURL := endpoint.Get()
	endpointURL := apiURL + DeviceCodeEndpoint

	// prepare request body as JSON
	reqBody := DeviceCodeRequest{
		ClientID: ClientID,
		Scope:    strings.Join(DefaultScopes, " "),
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request body: %w", err)
	}

	req, err := http.NewRequest("POST", endpointURL, strings.NewReader(string(jsonBody)))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", useragent.String())

	logger.LogHTTPRequest("POST", endpointURL)
	start := time.Now()

	client := &http.Client{Timeout: authHTTPTimeout}
	resp, err := client.Do(req)
	duration := time.Since(start)
	if err != nil {
		logger.LogHTTPError("POST", endpointURL, err, duration)
		return nil, fmt.Errorf("failed to request device code from %s: %w", endpointURL, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	logger.LogHTTPResponse("POST", endpointURL, resp.StatusCode, duration)

	if resp.StatusCode != http.StatusOK {
		var errResp ErrorResponse
		if err := json.Unmarshal(body, &errResp); err == nil && errResp.Error != "" {
			return nil, fmt.Errorf("device code request to %s failed: %s - %s", endpointURL, errResp.Error, errResp.ErrorDescription)
		}
		return nil, fmt.Errorf("device code request to %s failed with status %d: %s", endpointURL, resp.StatusCode, string(body))
	}

	var deviceCode DeviceCodeResponse
	if err := json.Unmarshal(body, &deviceCode); err != nil {
		return nil, fmt.Errorf("failed to parse device code response: %w", err)
	}

	return &deviceCode, nil
}

// Login polls for the device authorization and completes the login process
func Login(ctx context.Context, deviceCode *DeviceCodeResponse, statusCallback func(string)) error {
	if statusCallback == nil {
		statusCallback = func(string) {} // no-op
	}

	apiURL := endpoint.Get()
	endpointURL := apiURL + DeviceTokenEndpoint

	// calculate polling parameters
	interval := time.Duration(deviceCode.Interval) * time.Second
	if interval < 5*time.Second {
		interval = 5 * time.Second // minimum 5 second interval
	}

	expiresAt := time.Now().Add(time.Duration(deviceCode.ExpiresIn) * time.Second)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	client := &http.Client{Timeout: authHTTPTimeout}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if time.Now().After(expiresAt) {
				return fmt.Errorf("device code expired")
			}

			token, err := pollToken(client, endpointURL, deviceCode.DeviceCode)
			if err != nil {
				// check for specific error types
				if strings.Contains(err.Error(), "authorization_pending") {
					statusCallback("Waiting for authorization...")
					continue
				}
				if strings.Contains(err.Error(), "slow_down") {
					// increase interval as requested by server
					ticker.Reset(interval + 5*time.Second)
					statusCallback("Slowing down polling...")
					continue
				}
				if strings.Contains(err.Error(), "access_denied") {
					return fmt.Errorf("authorization was denied")
				}
				if strings.Contains(err.Error(), "expired_token") {
					return fmt.Errorf("device code expired")
				}
				return err
			}

			// exchange opaque token for JWT
			jwtToken, err := exchangeForJWT(client, apiURL, token.AccessToken)
			if err != nil {
				return fmt.Errorf("failed to exchange token for JWT: %w", err)
			}

			// use JWT as access token, keep original refresh token
			accessToken := jwtToken.AccessToken
			if accessToken == "" {
				// fallback to original if exchange didn't return a new token
				slog.Warn("JWT exchange returned empty access_token, falling back to opaque token",
					"endpoint", apiURL+"/api/v1/cli/auth/token")
				accessToken = token.AccessToken
			}

			// fetch user info using JWT
			userInfo, err := fetchUserInfo(client, apiURL, accessToken)
			if err != nil {
				return fmt.Errorf("failed to fetch user info: %w", err)
			}

			// determine expiration from JWT response or original token
			expiresIn := token.ExpiresIn
			if jwtToken.ExpiresIn > 0 {
				expiresIn = jwtToken.ExpiresIn
			}

			// save JWT token
			storedToken := &StoredToken{
				AccessToken:  accessToken,
				RefreshToken: token.RefreshToken,
				ExpiresAt:    time.Now().Add(time.Duration(expiresIn) * time.Second),
				TokenType:    token.TokenType,
				Scope:        token.Scope,
				UserInfo:     *userInfo,
			}

			if err := SaveToken(storedToken); err != nil {
				return fmt.Errorf("failed to save token: %w", err)
			}

			statusCallback("Successfully authenticated")
			return nil
		}
	}
}

// TokenRequest represents the request body for device token endpoint
type TokenRequest struct {
	GrantType  string `json:"grant_type"`
	DeviceCode string `json:"device_code"`
	ClientID   string `json:"client_id"`
}

// pollToken attempts to exchange the device code for an access token
func pollToken(client *http.Client, endpoint, deviceCode string) (*TokenResponse, error) {
	reqBody := TokenRequest{
		GrantType:  "urn:ietf:params:oauth:grant-type:device_code",
		DeviceCode: deviceCode,
		ClientID:   ClientID,
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request body: %w", err)
	}

	req, err := http.NewRequest("POST", endpoint, strings.NewReader(string(jsonBody)))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", useragent.String())

	logger.LogHTTPRequest("POST", endpoint)
	start := time.Now()

	resp, err := client.Do(req)
	duration := time.Since(start)
	if err != nil {
		logger.LogHTTPError("POST", endpoint, err, duration)
		return nil, fmt.Errorf("failed to poll token: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	logger.LogHTTPResponse("POST", endpoint, resp.StatusCode, duration)

	if resp.StatusCode != http.StatusOK {
		var errResp ErrorResponse
		if err := json.Unmarshal(body, &errResp); err == nil && errResp.Error != "" {
			return nil, fmt.Errorf("%s", errResp.Error)
		}
		return nil, fmt.Errorf("token request failed with status %d: %s", resp.StatusCode, string(body))
	}

	var token TokenResponse
	if err := json.Unmarshal(body, &token); err != nil {
		return nil, fmt.Errorf("failed to parse token response: %w", err)
	}

	return &token, nil
}

// JWTExchangeResponse represents the response from JWT exchange endpoint
type JWTExchangeResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
}

// exchangeForJWT exchanges an opaque session token for a JWT
func exchangeForJWT(client *http.Client, apiURL, opaqueToken string) (*JWTExchangeResponse, error) {
	endpoint := strings.TrimSuffix(apiURL, "/") + "/api/v1/cli/auth/token"

	req, err := http.NewRequest("GET", endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+opaqueToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", useragent.String())

	logger.LogHTTPRequest("GET", endpoint)
	start := time.Now()

	resp, err := client.Do(req)
	duration := time.Since(start)
	if err != nil {
		logger.LogHTTPError("GET", endpoint, err, duration)
		return nil, fmt.Errorf("failed to exchange token: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	logger.LogHTTPResponse("GET", endpoint, resp.StatusCode, duration)

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("JWT exchange failed with status %d: %s", resp.StatusCode, string(body))
	}

	var jwtResp JWTExchangeResponse
	if err := json.Unmarshal(body, &jwtResp); err != nil {
		return nil, fmt.Errorf("failed to parse JWT response: %w", err)
	}

	return &jwtResp, nil
}

// fetchUserInfo retrieves user information using the access token
func fetchUserInfo(client *http.Client, apiURL, accessToken string) (*UserInfo, error) {
	endpoint := apiURL + UserInfoEndpoint

	req, err := http.NewRequest("GET", endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", useragent.String())

	logger.LogHTTPRequest("GET", endpoint)
	start := time.Now()

	resp, err := client.Do(req)
	duration := time.Since(start)
	if err != nil {
		logger.LogHTTPError("GET", endpoint, err, duration)
		return nil, fmt.Errorf("failed to fetch user info: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	logger.LogHTTPResponse("GET", endpoint, resp.StatusCode, duration)

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("user info request failed with status %d: %s", resp.StatusCode, string(body))
	}

	var userInfo UserInfo
	if err := json.Unmarshal(body, &userInfo); err != nil {
		return nil, fmt.Errorf("failed to parse user info response: %w", err)
	}

	return &userInfo, nil
}

// AuthClient methods for test isolation (enable t.Parallel())

// RequestDeviceCode initiates the device authorization flow.
func (c *AuthClient) RequestDeviceCode() (*DeviceCodeResponse, error) {
	apiURL := c.Endpoint()
	endpointURL := apiURL + DeviceCodeEndpoint

	// prepare request body as JSON
	reqBody := DeviceCodeRequest{
		ClientID: ClientID,
		Scope:    strings.Join(DefaultScopes, " "),
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request body: %w", err)
	}

	req, err := http.NewRequest("POST", endpointURL, strings.NewReader(string(jsonBody)))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", useragent.String())

	logger.LogHTTPRequest("POST", endpointURL)
	start := time.Now()

	client := &http.Client{Timeout: authHTTPTimeout}
	resp, err := client.Do(req)
	duration := time.Since(start)
	if err != nil {
		logger.LogHTTPError("POST", endpointURL, err, duration)
		return nil, fmt.Errorf("failed to request device code from %s: %w", endpointURL, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	logger.LogHTTPResponse("POST", endpointURL, resp.StatusCode, duration)

	if resp.StatusCode != http.StatusOK {
		var errResp ErrorResponse
		if err := json.Unmarshal(body, &errResp); err == nil && errResp.Error != "" {
			return nil, fmt.Errorf("device code request to %s failed: %s - %s", endpointURL, errResp.Error, errResp.ErrorDescription)
		}
		return nil, fmt.Errorf("device code request to %s failed with status %d: %s", endpointURL, resp.StatusCode, string(body))
	}

	var deviceCode DeviceCodeResponse
	if err := json.Unmarshal(body, &deviceCode); err != nil {
		return nil, fmt.Errorf("failed to parse device code response: %w", err)
	}

	return &deviceCode, nil
}

// Login polls for the device authorization and completes the login process.
func (c *AuthClient) Login(ctx context.Context, deviceCode *DeviceCodeResponse, statusCallback func(string)) error {
	if statusCallback == nil {
		statusCallback = func(string) {} // no-op
	}

	apiURL := c.Endpoint()
	endpointURL := apiURL + DeviceTokenEndpoint

	// calculate polling parameters
	interval := time.Duration(deviceCode.Interval) * time.Second
	if interval < 5*time.Second {
		interval = 5 * time.Second // minimum 5 second interval
	}

	expiresAt := time.Now().Add(time.Duration(deviceCode.ExpiresIn) * time.Second)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	httpClient := &http.Client{Timeout: authHTTPTimeout}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if time.Now().After(expiresAt) {
				return fmt.Errorf("device code expired")
			}

			token, err := pollToken(httpClient, endpointURL, deviceCode.DeviceCode)
			if err != nil {
				// check for specific error types
				if strings.Contains(err.Error(), "authorization_pending") {
					statusCallback("Waiting for authorization...")
					continue
				}
				if strings.Contains(err.Error(), "slow_down") {
					// increase interval as requested by server
					ticker.Reset(interval + 5*time.Second)
					statusCallback("Slowing down polling...")
					continue
				}
				if strings.Contains(err.Error(), "access_denied") {
					return fmt.Errorf("authorization was denied")
				}
				if strings.Contains(err.Error(), "expired_token") {
					return fmt.Errorf("device code expired")
				}
				return err
			}

			// exchange opaque token for JWT
			jwtToken, err := exchangeForJWT(httpClient, apiURL, token.AccessToken)
			if err != nil {
				return fmt.Errorf("failed to exchange token for JWT: %w", err)
			}

			// use JWT as access token, keep original refresh token
			accessToken := jwtToken.AccessToken
			if accessToken == "" {
				// fallback to original if exchange didn't return a new token
				slog.Warn("JWT exchange returned empty access_token, falling back to opaque token",
					"endpoint", apiURL+"/api/v1/cli/auth/token")
				accessToken = token.AccessToken
			}

			// fetch user info using JWT
			userInfo, err := fetchUserInfo(httpClient, apiURL, accessToken)
			if err != nil {
				return fmt.Errorf("failed to fetch user info: %w", err)
			}

			// determine expiration from JWT response or original token
			expiresIn := token.ExpiresIn
			if jwtToken.ExpiresIn > 0 {
				expiresIn = jwtToken.ExpiresIn
			}

			// save JWT token using client's storage
			storedToken := &StoredToken{
				AccessToken:  accessToken,
				RefreshToken: token.RefreshToken,
				ExpiresAt:    time.Now().Add(time.Duration(expiresIn) * time.Second),
				TokenType:    token.TokenType,
				Scope:        token.Scope,
				UserInfo:     *userInfo,
			}

			if err := c.SaveToken(storedToken); err != nil {
				return fmt.Errorf("failed to save token: %w", err)
			}

			statusCallback("Successfully authenticated")
			return nil
		}
	}
}
