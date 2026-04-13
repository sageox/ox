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

// ValidateTokenServerSide checks if the server accepts the given access token
// by hitting the /oauth2/userinfo endpoint. Returns nil on success, or an error
// describing why the token was rejected.
func ValidateTokenServerSide(ep, accessToken string) error {
	ep = endpoint.NormalizeEndpoint(ep)
	url := strings.TrimSuffix(ep, "/") + UserInfoEndpoint

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := useragent.NewRequest(ctx, "GET", url, nil)
	if err != nil {
		return fmt.Errorf("failed to create validation request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")

	logger.LogHTTPRequest("GET", url)
	start := time.Now()

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	duration := time.Since(start)
	if err != nil {
		logger.LogHTTPError("GET", url, err, duration)
		slog.Debug("server-side token validation failed (network)", "endpoint", ep, "error", err)
		return fmt.Errorf("could not reach %s to validate token: %w", endpoint.NormalizeSlug(ep), err)
	}
	defer resp.Body.Close()

	logger.LogHTTPResponse("GET", url, resp.StatusCode, duration)

	if resp.StatusCode == http.StatusOK {
		return nil
	}

	// read error body for context
	body, _ := io.ReadAll(resp.Body)
	slog.Debug("server-side token validation rejected",
		"endpoint", ep,
		"status", resp.StatusCode,
		"response", string(body),
	)

	var errResp struct {
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	if json.Unmarshal(body, &errResp) == nil && errResp.ErrorDescription != "" {
		return fmt.Errorf("server rejected token: %s", errResp.ErrorDescription)
	}
	if errResp.Error != "" {
		return fmt.Errorf("server rejected token: %s", errResp.Error)
	}
	return fmt.Errorf("server rejected token (HTTP %d)", resp.StatusCode)
}

// --- Server-validated auth checks ---
// These contact the server to verify the token is actually accepted.
// Use for commands that gate real work: init, doctor, status.

// IsAuthenticatedForEndpoint validates that the user has a working token for the
// given endpoint by checking both local validity and server acceptance via
// /oauth2/userinfo.
func IsAuthenticatedForEndpoint(ep string) (bool, error) {
	// local credential check first (fast-fail if no token)
	token, err := EnsureValidTokenForEndpoint(ep, 0)
	if err != nil {
		raw, _ := GetTokenForEndpoint(ep)
		if raw != nil {
			return false, fmt.Errorf("token refresh failed: %w", err)
		}
		return false, nil
	}
	if token == nil {
		return false, nil
	}

	// server-side validation
	if err := ValidateTokenServerSide(ep, token.AccessToken); err != nil {
		slog.Debug("server-side auth validation failed", "endpoint", ep, "error", err)
		return false, err
	}

	return true, nil
}

// IsAuthenticated validates that the user has a working token for the current
// endpoint, including server-side validation.
func IsAuthenticated() (bool, error) {
	return IsAuthenticatedForEndpoint(endpoint.Get())
}

// --- Local-only credential checks ---
// These only check local auth.json state. Use for fast checks where network
// latency is unacceptable: login prompts, daemon hot paths, background polling.

// IsAuthCredentialValidForEndpoint checks if a locally valid (non-expired) token
// exists for a specific endpoint. Does NOT contact the server.
func IsAuthCredentialValidForEndpoint(ep string) (bool, error) {
	token, err := EnsureValidTokenForEndpoint(ep, 0)
	if err != nil {
		raw, _ := GetTokenForEndpoint(ep)
		if raw != nil {
			return false, fmt.Errorf("token refresh failed: %w", err)
		}
		return false, nil
	}

	if token == nil {
		return false, nil
	}

	return true, nil
}

// IsAuthCredentialValid checks if a locally valid (non-expired) token exists for
// the current endpoint. Does NOT contact the server.
func IsAuthCredentialValid() (bool, error) {
	return IsAuthCredentialValidForEndpoint(endpoint.Get())
}
