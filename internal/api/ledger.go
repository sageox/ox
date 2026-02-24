package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/sageox/ox/internal/logger"
	"github.com/sageox/ox/internal/useragent"
)

const (
	ledgerStatusPath = "/api/v1/repos/%s/ledger-status" // %s = repo_id (SageOx UUID)
)

// ErrLedgerNotFound is returned when the ledger doesn't exist for the given repo.
var ErrLedgerNotFound = errors.New("ledger not found")

// LedgerStatusResponse represents the GET /api/v1/repos/{repo_id}/ledger-status response.
type LedgerStatusResponse struct {
	Status      string `json:"status"`                 // "ready", "pending", "error"
	RepoURL     string `json:"repo_url"`               // git clone URL (empty if not ready)
	RepoID      int    `json:"repo_id"`                // GitLab project ID (internal use)
	CreatedAt   string `json:"created_at"`             // RFC3339 timestamp
	Message     string `json:"message,omitempty"`      // optional status message
	Visibility  string `json:"visibility,omitempty"`   // "public" or "private" (new field, may be empty on older servers)
	AccessLevel string `json:"access_level,omitempty"` // "member" or "viewer" (new field, may be empty on older servers)
}

// IsReadOnly returns true if the user has viewer (read-only) access.
func (r *LedgerStatusResponse) IsReadOnly() bool {
	return r.AccessLevel == "viewer"
}

// GetLedgerStatus fetches ledger provisioning status from the cloud API.
//
// Response status values:
//   - "ready":   Ledger is provisioned and RepoURL is available for cloning
//   - "pending": Ledger is being provisioned, caller should retry later
//   - "error":   Provisioning failed, check Message for details
//
// The repoID parameter is the SageOx repo identifier (UUID) from project config.
// The returned RepoID is the GitLab project ID (used internally by server).
//
// Returns ErrLedgerNotFound if no ledger exists for this repo.
// Returns ErrUnauthorized if authentication fails.
func (c *RepoClient) GetLedgerStatus(repoID string) (*LedgerStatusResponse, error) {
	reqURL := strings.TrimSuffix(c.baseURL, "/") + fmt.Sprintf(ledgerStatusPath, repoID)

	logger.LogHTTPRequest("GET", reqURL)
	start := time.Now()

	httpReq, err := http.NewRequest("GET", reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	useragent.SetHeaders(httpReq.Header)
	if c.authToken != "" {
		httpReq.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.authToken))
	}

	resp, err := c.httpClient.Do(httpReq)
	duration := time.Since(start)

	if err != nil {
		logger.LogHTTPError("GET", reqURL, err, duration)
		return nil, fmt.Errorf("network error: %w", err)
	}
	defer resp.Body.Close()

	logger.LogHTTPResponse("GET", reqURL, resp.StatusCode, duration)

	// check for version deprecation signals
	if CheckVersionResponse(resp) {
		return nil, ErrVersionUnsupported
	}

	// read response body
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	// handle non-2xx responses
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		errMsg := strings.TrimSpace(string(bodyBytes))
		if resp.StatusCode == http.StatusUnauthorized {
			return nil, ErrUnauthorized
		}
		if resp.StatusCode == http.StatusForbidden {
			return nil, ErrForbidden
		}
		if resp.StatusCode == http.StatusNotFound {
			return nil, fmt.Errorf("%w: repo %s", ErrLedgerNotFound, repoID)
		}
		if errMsg == "" {
			return nil, fmt.Errorf("HTTP %d from %s", resp.StatusCode, reqURL)
		}
		return nil, fmt.Errorf("HTTP %d from %s: %s", resp.StatusCode, reqURL, errMsg)
	}

	// log the raw response for debugging
	logger.LogHTTPResponseBody(string(bodyBytes))

	// decode successful response
	var statusResp LedgerStatusResponse
	if err := json.Unmarshal(bodyBytes, &statusResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &statusResp, nil
}
