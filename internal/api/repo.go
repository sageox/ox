package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/useragent"
	"github.com/sageox/ox/internal/logger"
	"github.com/sageox/ox/internal/version"
)

const (
	repoInitPath      = "/api/v1/repo/init"
	repoDoctorPath    = "/api/v1/repo/%s/doctor"    // %s = repo_id
	repoUninstallPath = "/api/v1/repo/%s/uninstall" // %s = repo_id
)

// RepoInitRequest represents the POST /api/v1/repo/init request
type RepoInitRequest struct {
	RepoID           string           `json:"repo_id"`                      // Required: prefixed UUIDv7
	Type             string           `json:"type"`                         // Required: "git" or "svn"
	InitAt           string           `json:"init_at"`                      // Required: RFC3339 timestamp
	Name             string           `json:"name,omitempty"`               // Optional: display name (e.g. "sageox/ox")
	Teams            []string         `json:"teams,omitempty"`              // Optional: team IDs to associate repo with
	RepoSalt         string           `json:"repo_salt,omitempty"`          // Optional: initial commit hash
	RepoRemoteHashes []string         `json:"repo_remote_hashes,omitempty"` // Optional: salted hashes
	Fingerprint      *RepoFingerprint `json:"fingerprint,omitempty"`        // Optional: repo identity fingerprint
	Identities       any              `json:"identities,omitempty"`         // Optional: resolved user identities (identity.ResolvedIdentities)
	IsPublic         bool             `json:"is_public,omitempty"`          // Optional: prevents fork merging
	CreatedByEmail   string           `json:"created_by_email,omitempty"`   // Optional: git user email (backward compat)
	CreatedByName    string           `json:"created_by_name,omitempty"`    // Optional: git user name (backward compat)
}

// RepoFingerprint holds repository identity fingerprint data for detecting
// identical or related repositories across teams. Enables the API to suggest
// team merges when multiple teams are working on the same codebase.
type RepoFingerprint struct {
	// FirstCommit is the hash of the initial commit (same as repo_salt).
	FirstCommit string `json:"first_commit"`

	// MonthlyCheckpoints maps "YYYY-MM" to the first commit hash of that month.
	MonthlyCheckpoints map[string]string `json:"monthly_checkpoints"`

	// AncestrySamples contains commit hashes at power-of-2 intervals.
	AncestrySamples []string `json:"ancestry_samples"`

	// RemoteHashes contains salted SHA256 hashes of normalized remote URLs.
	RemoteHashes []string `json:"remote_hashes,omitempty"`
}

// RepoInitResponse represents the POST /api/v1/repo/init response
type RepoInitResponse struct {
	RepoID     string `json:"repo_id"`
	TeamID     string `json:"team_id"`
	WebBaseURL string `json:"web_base_url,omitempty"` // web dashboard base URL (for enterprise endpoints)
}

// RepoUninstallRequest represents the POST /api/v1/repo/{repo_id}/uninstall request
type RepoUninstallRequest struct {
	RepoSalt string `json:"repo_salt"` // first commit hash for authentication
}

// DoctorIssue represents a single diagnostic issue from the cloud
// Cloud doctor detects things the local CLI cannot:
// - Pending merge conflicts (same repo registered twice) - requires cross-repo knowledge
// - Team invites pending acceptance - lives in cloud DB
// - Guidance updates available - version comparison server-side
// - Billing/quota warnings - enterprise only
// - Team-wide health (X repos need updates) - aggregate view
type DoctorIssue struct {
	Type        string `json:"type"`                   // e.g., "merge_pending", "team_invite_pending"
	Severity    string `json:"severity"`               // "error", "warning", "info"
	Title       string `json:"title"`                  // short display title
	Description string `json:"description"`            // detailed explanation, supports Markdown
	ActionURL   string `json:"action_url,omitempty"`   // URL to resolve the issue
	ActionLabel string `json:"action_label,omitempty"` // button text, e.g., "Resolve merge"
}

// DoctorResponse represents the GET /api/v1/repo/{repo_id}/doctor response
type DoctorResponse struct {
	Issues    []DoctorIssue `json:"issues"`
	CheckedAt string        `json:"checked_at"` // RFC3339 timestamp
}

// RepoClient handles API communication with the SageOx repo endpoints
type RepoClient struct {
	baseURL    string
	httpClient *http.Client
	authToken  string
	version    string
}

// NewRepoClient creates a new repo API client using the global default endpoint.
//
// CAUTION: This should RARELY be used. It uses endpoint.Get() which ignores
// project config, so it will use the wrong endpoint for repos configured with
// non-default endpoints (e.g., enterprise or test environments).
//
// Use NewRepoClientForProject(gitRoot) instead for operations within a repo context.
// Use NewRepoClientWithEndpoint(endpoint) when you have the endpoint explicitly.
func NewRepoClient() *RepoClient {
	return &RepoClient{
		baseURL:    endpoint.Get(),
		httpClient: &http.Client{Timeout: 10 * time.Second},
		version:    version.Version,
	}
}

// NewRepoClientForProject creates a new repo API client using the endpoint from project config.
// This is the recommended way to create a client for repo-bound operations.
// It checks: SAGEOX_ENDPOINT env var > project config > default endpoint.
func NewRepoClientForProject(gitRoot string) *RepoClient {
	return &RepoClient{
		baseURL:    endpoint.GetForProject(gitRoot),
		httpClient: &http.Client{Timeout: 10 * time.Second},
		version:    version.Version,
	}
}

// NewRepoClientWithEndpoint creates a new repo API client with a specific endpoint.
// Use this when you already have the endpoint URL (e.g., from auth flow or config).
func NewRepoClientWithEndpoint(baseURL string) *RepoClient {
	return &RepoClient{
		baseURL:    baseURL,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		version:    version.Version,
	}
}

// WithAuthToken sets the auth token for authenticated requests
func (c *RepoClient) WithAuthToken(token string) *RepoClient {
	c.authToken = token
	return c
}

// Endpoint returns the base URL this client is configured for
func (c *RepoClient) Endpoint() string {
	return c.baseURL
}

// RegisterRepo calls POST /api/v1/repo/init
// Returns (response, error) - error is nil if call succeeds (even for 4xx/5xx)
// Gracefully handles 404 (endpoint not yet deployed) by returning nil, nil
func (c *RepoClient) RegisterRepo(req *RepoInitRequest) (*RepoInitResponse, error) {
	reqURL := strings.TrimSuffix(c.baseURL, "/") + repoInitPath

	// marshal request body
	bodyBytes, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	logger.LogHTTPRequest("POST", reqURL)
	logger.LogHTTPRequestBody(string(bodyBytes))
	start := time.Now()

	// create HTTP request
	httpReq, err := http.NewRequest("POST", reqURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// set headers
	httpReq.Header.Set("Content-Type", "application/json")
	useragent.SetHeaders(httpReq.Header)
	if c.authToken != "" {
		httpReq.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.authToken))
	}

	// execute request
	resp, err := c.httpClient.Do(httpReq)
	duration := time.Since(start)

	if err != nil {
		logger.LogHTTPError("POST", reqURL, err, duration)
		return nil, fmt.Errorf("network error: %w", err)
	}
	defer resp.Body.Close()

	logger.LogHTTPResponse("POST", reqURL, resp.StatusCode, duration)

	// check for version deprecation signals
	if CheckVersionResponse(resp) {
		return nil, ErrVersionUnsupported
	}

	// handle X-SageOx-Merge header for repo/team merges
	// this is best-effort - don't fail the request if redirect handling fails
	if redirectInfo := ParseRedirectHeader(resp.Header); redirectInfo != nil {
		if projectRoot := config.FindProjectRoot(); projectRoot != "" {
			_ = HandleRedirect(projectRoot, redirectInfo)
		}
	}

	// handle 404 gracefully - endpoint not yet deployed
	if resp.StatusCode == http.StatusNotFound {
		io.Copy(io.Discard, resp.Body) // drain body for connection reuse
		return nil, nil
	}

	// read response body for error handling
	bodyBytes, err = io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	// handle non-2xx responses
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		errMsg := strings.TrimSpace(string(bodyBytes))
		if errMsg == "" {
			return nil, fmt.Errorf("HTTP %d from %s", resp.StatusCode, reqURL)
		}
		return nil, fmt.Errorf("HTTP %d from %s: %s", resp.StatusCode, reqURL, errMsg)
	}

	// log the raw response for debugging
	logger.LogHTTPResponseBody(string(bodyBytes))

	// decode successful response
	var initResp RepoInitResponse
	if err := json.Unmarshal(bodyBytes, &initResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &initResp, nil
}

// GetDoctorIssues calls GET /api/v1/repo/{repo_id}/doctor for cloud diagnostics
// Returns nil, nil if API unavailable (graceful degradation for offline mode)
func (c *RepoClient) GetDoctorIssues(repoID string) (*DoctorResponse, error) {
	reqURL := strings.TrimSuffix(c.baseURL, "/") + fmt.Sprintf(repoDoctorPath, repoID)

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
		// graceful degradation - return nil instead of error for network issues
		return nil, nil
	}
	defer resp.Body.Close()

	logger.LogHTTPResponse("GET", reqURL, resp.StatusCode, duration)

	// handle 404 gracefully - endpoint not yet deployed
	if resp.StatusCode == http.StatusNotFound {
		io.Copy(io.Discard, resp.Body) // drain body for connection reuse
		return nil, nil
	}

	// read response body
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil // graceful degradation
	}

	// handle non-2xx responses silently (graceful degradation)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, nil
	}

	// decode successful response
	var doctorResp DoctorResponse
	if err := json.Unmarshal(bodyBytes, &doctorResp); err != nil {
		return nil, nil // graceful degradation on malformed response
	}

	return &doctorResp, nil
}

// NotifyUninstall calls POST /api/v1/repo/{repo_id}/uninstall to notify server of local uninstall.
// Requires authentication - the server validates the user is a team member with permission
// to trigger uninstallation workflows. Returns errors so callers can provide user feedback.
// The repo_salt (first commit hash) provides additional verification.
func (c *RepoClient) NotifyUninstall(repoID, repoSalt string) error {
	if repoID == "" {
		return fmt.Errorf("repo_id is required")
	}

	reqURL := strings.TrimSuffix(c.baseURL, "/") + fmt.Sprintf(repoUninstallPath, repoID)

	req := &RepoUninstallRequest{
		RepoSalt: repoSalt,
	}

	bodyBytes, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	logger.LogHTTPRequest("POST", reqURL)
	start := time.Now()

	httpReq, err := http.NewRequest("POST", reqURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	useragent.SetHeaders(httpReq.Header)
	if c.authToken != "" {
		httpReq.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.authToken))
	}

	resp, err := c.httpClient.Do(httpReq)
	duration := time.Since(start)

	if err != nil {
		logger.LogHTTPError("POST", reqURL, err, duration)
		return fmt.Errorf("network error: %w", err)
	}
	defer resp.Body.Close()

	logger.LogHTTPResponse("POST", reqURL, resp.StatusCode, duration)

	// drain body for connection reuse (no response parsing needed)
	io.Copy(io.Discard, resp.Body)

	// check for auth/permission errors
	switch resp.StatusCode {
	case http.StatusOK, http.StatusAccepted, http.StatusNoContent:
		return nil
	case http.StatusUnauthorized:
		return fmt.Errorf("authentication required (401)")
	case http.StatusForbidden:
		return fmt.Errorf("not authorized to uninstall this repo (403)")
	case http.StatusNotFound:
		// repo not found in cloud - that's ok, might not have been registered
		return nil
	default:
		if resp.StatusCode >= 400 {
			return fmt.Errorf("server error (%d)", resp.StatusCode)
		}
		return nil
	}
}

// RepoMarkerData holds parsed data from a .repo_* marker file
type RepoMarkerData struct {
	RepoID   string `json:"repo_id"`
	RepoSalt string `json:"repo_salt"`
	Endpoint string `json:"endpoint"`
	// TODO: Remove after 2026-01-31 - legacy field support
	APIEndpoint string `json:"api_endpoint"` // deprecated: use Endpoint
}

// GetEndpoint returns the endpoint, preferring new field over legacy
func (m *RepoMarkerData) GetEndpoint() string {
	if m.Endpoint != "" {
		return m.Endpoint
	}
	return m.APIEndpoint
}

// ReadFirstRepoMarker reads the first .repo_* marker file found in the sageox directory.
// Returns the parsed marker data or nil if no marker found.
// This is useful for getting repo_id and repo_salt before uninstall.
func ReadFirstRepoMarker(sageoxDir string) (*RepoMarkerData, error) {
	entries, err := os.ReadDir(sageoxDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), ".repo_") {
			continue
		}

		markerPath := filepath.Join(sageoxDir, entry.Name())
		data, err := os.ReadFile(markerPath)
		if err != nil {
			continue
		}

		var marker RepoMarkerData
		if err := json.Unmarshal(data, &marker); err != nil {
			continue
		}

		if marker.RepoID != "" {
			return &marker, nil
		}
	}

	return nil, nil
}
