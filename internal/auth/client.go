package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/logger"
	"github.com/sageox/ox/internal/resilience"
	"github.com/sageox/ox/internal/useragent"
)

// APIResponse represents a response from an authenticated API request
type APIResponse struct {
	StatusCode int
	Data       interface{} // parsed JSON or nil
	Headers    http.Header
}

// Ok returns true if the status code is 2xx
func (r *APIResponse) Ok() bool {
	return r.StatusCode >= 200 && r.StatusCode < 300
}

// AuthenticationError is raised when authentication is required but user is not logged in
type AuthenticationError struct {
	Message string
}

func (e *AuthenticationError) Error() string {
	return e.Message
}

// APIError is raised when API returns an error response
type APIError struct {
	StatusCode int
	Message    string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("API error %d: %s", e.StatusCode, e.Message)
}

// AuthenticatedRequest makes an authenticated HTTP request to the API.
//
// Adds Bearer token to Authorization header. If request returns 401,
// attempts token refresh and retries once.
//
// Args:
//   - ctx: Context for cancellation and timeout
//   - method: HTTP method (GET, POST, PUT, DELETE, etc.)
//   - url: Full URL to request
//   - data: Optional JSON body (will be serialized)
//
// Returns:
//   - APIResponse with status, data, headers
//
// Errors:
//   - AuthenticationError: If not logged in
//   - TokenRefreshError: If token refresh fails
//   - APIError: If API returns error after retry
func AuthenticatedRequest(ctx context.Context, method, url string, data interface{}) (*APIResponse, error) {
	// get valid token (proactive refresh with 5-minute buffer)
	token, err := EnsureValidToken(300)
	if err != nil {
		return nil, err
	}

	if token == nil {
		return nil, &AuthenticationError{
			Message: "not logged in. Run 'ox login' first.",
		}
	}

	// make request with Bearer token
	response, err := makeRequest(ctx, method, url, token.AccessToken, data)
	if err != nil {
		return nil, err
	}

	// handle 401 with retry
	if response.StatusCode == http.StatusUnauthorized {
		newToken, refreshErr := Handle401Error(token)
		if refreshErr != nil {
			return nil, &AuthenticationError{
				Message: "session expired. Run 'ox login' to re-authenticate.",
			}
		}

		// retry with new token
		response, err = makeRequest(ctx, method, url, newToken.AccessToken, data)
		if err != nil {
			return nil, err
		}
	}

	return response, nil
}

// makeRequest makes HTTP request with Bearer token.
//
// Applies throttling, circuit breaker, and HTTP logging for resilience
// and observability.
//
// Args:
//   - ctx: Context for cancellation and timeout
//   - method: HTTP method
//   - url: Full URL
//   - accessToken: OAuth access token
//   - data: Optional JSON body
//
// Returns:
//   - APIResponse with status, data, headers
func makeRequest(ctx context.Context, method, url, accessToken string, data interface{}) (*APIResponse, error) {
	// apply throttling to prevent hammering the API
	resilience.DefaultThrottle().Wait()

	// check circuit breaker (with retryChance=1.0, always attempts)
	circuit := resilience.DefaultCircuit()
	if !circuit.ShouldAttempt() {
		return nil, &APIError{
			StatusCode: 0,
			Message:    "SageOx API unavailable - try again later",
		}
	}

	// prepare request body
	var body io.Reader
	if data != nil {
		jsonData, err := json.Marshal(data)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal request data: %w", err)
		}
		body = bytes.NewReader(jsonData)
	}

	// create request with context for cancellation support
	req, err := useragent.NewRequest(ctx, method, url, body)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// build headers
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", accessToken))

	// add Content-Type if sending JSON data
	if data != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	// log outbound request
	logger.LogHTTPRequest(method, url)
	start := time.Now()

	// make request
	client := &http.Client{
		Timeout: 30 * time.Second,
	}

	resp, err := client.Do(req)
	duration := time.Since(start)

	if err != nil {
		logger.LogHTTPError(method, url, err, duration)
		circuit.RecordFailure()
		return nil, &APIError{
			StatusCode: 0,
			Message:    fmt.Sprintf("network error: %v", err),
		}
	}
	defer resp.Body.Close()

	// check for version deprecation signals
	if api.CheckVersionResponse(resp) {
		return nil, &APIError{
			StatusCode: resp.StatusCode,
			Message:    api.ErrVersionUnsupported.Error(),
		}
	}

	// read response body
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	// log response
	logger.LogHTTPResponse(method, url, resp.StatusCode, duration)

	// log response body for error status codes to aid debugging
	if resp.StatusCode >= 400 {
		logger.LogHTTPResponseBody(string(responseBody))
	}

	// record success/failure in circuit breaker based on status code
	if resp.StatusCode >= 500 {
		circuit.RecordFailure()
	} else {
		circuit.RecordSuccess()
	}

	// parse JSON if content-type is application/json
	var responseData interface{}
	contentType := resp.Header.Get("Content-Type")
	if strings.Contains(strings.ToLower(contentType), "application/json") && len(responseBody) > 0 {
		if err := json.Unmarshal(responseBody, &responseData); err != nil {
			// invalid JSON, leave as nil
			responseData = nil
		}
	}

	return &APIResponse{
		StatusCode: resp.StatusCode,
		Data:       responseData,
		Headers:    resp.Header,
	}, nil
}

// AuthenticatedRequest makes an authenticated HTTP request using this client's token storage.
// This method enables test isolation by using the client's configDir for token storage.
func (c *AuthClient) AuthenticatedRequest(ctx context.Context, method, url string, data interface{}) (*APIResponse, error) {
	// get valid token (proactive refresh with 5-minute buffer)
	token, err := c.EnsureValidToken(300)
	if err != nil {
		return nil, err
	}

	if token == nil {
		return nil, &AuthenticationError{
			Message: "not logged in. Run 'ox login' first.",
		}
	}

	// make request with Bearer token
	response, err := makeRequest(ctx, method, url, token.AccessToken, data)
	if err != nil {
		return nil, err
	}

	// handle 401 with retry
	if response.StatusCode == http.StatusUnauthorized {
		newToken, refreshErr := c.Handle401Error(token)
		if refreshErr != nil {
			return nil, &AuthenticationError{
				Message: "session expired. Run 'ox login' to re-authenticate.",
			}
		}

		// retry with new token
		response, err = makeRequest(ctx, method, url, newToken.AccessToken, data)
		if err != nil {
			return nil, err
		}
	}

	return response, nil
}
