package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/sageox/ox/internal/logger"
	"github.com/sageox/ox/internal/useragent"
)

const (
	reposPath      = "/api/v1/cli/repos"
	repoDetailPath = "/api/v1/cli/repos/%s" // %s = repo_id (SageOx UUID)
	teamInfoPath   = "/api/v1/teams/%s"     // %s = team_id
)

// RepoInfo represents a single git repository from the server.
// NOTE: This API only returns team context repos, not ledgers.
// Use GetLedgerStatus() for ledger URLs (project-scoped).
type RepoInfo struct {
	Name   string `json:"name"`              // e.g., "acme-corp-team-context"
	URL    string `json:"url"`               // git clone URL
	Type   string `json:"type"`              // "team-context" (ledgers use separate API)
	TeamID string `json:"team_id,omitempty"` // team_xxx (present for team-context repos)
	Slug   string `json:"slug,omitempty"`    // kebab-case team slug (server-provided)
}

// StableID returns the stable team identifier (team_xxx) for path construction and lookups.
func (r RepoInfo) StableID() string {
	return r.TeamID
}

// TeamMembership represents a team the user belongs to
type TeamMembership struct {
	ID   string `json:"id"`             // team_xxx
	Name string `json:"name"`           // display name
	Slug string `json:"slug,omitempty"` // kebab-case team slug
	Role string `json:"role"`           // "owner", "admin", "member"
}

// ReposResponse represents the GET /api/v1/cli/repos response.
// This API returns team context repos only (user-scoped).
// Ledger URLs are fetched separately via GET /api/v1/repos/{repo_id}/ledger-status (project-scoped).
type ReposResponse struct {
	Token     string              `json:"token"`      // PAT for git operations
	ServerURL string              `json:"server_url"` // GitLab server URL
	Username  string              `json:"username"`   // GitLab username
	ExpiresAt time.Time           `json:"expires_at"` // Token expiration
	Repos     map[string]RepoInfo `json:"repos"`      // Repos indexed by name
	Teams     []TeamMembership    `json:"teams"`      // User's team memberships
}

// TeamMembershipsFromRepos derives team memberships from the repos map.
// Each repo with type "team-context" represents a team the user belongs to.
// Falls back to the Teams array if populated.
func (r *ReposResponse) TeamMembershipsFromRepos() []TeamMembership {
	if len(r.Teams) > 0 {
		return r.Teams
	}
	var teams []TeamMembership
	for _, repo := range r.Repos {
		if repo.Type != "team-context" {
			continue
		}
		teams = append(teams, TeamMembership{
			ID:   repo.StableID(),
			Name: repo.Name,
			Slug: repo.Slug,
		})
	}
	return teams
}

// TeamInfoResponse represents the GET /api/v1/teams/{id} response
type TeamInfoResponse struct {
	ID       string `json:"id"`                  // team ID (team_xxx)
	Name     string `json:"name"`                // display name
	Slug     string `json:"slug,omitempty"`      // URL-friendly name
	RepoURL  string `json:"repo_url,omitempty"`  // team context git repo URL
	GitToken string `json:"git_token,omitempty"` // token for git operations
}

// RepoDetailResponse represents GET /api/v1/cli/repos/{repo_id}.
// Returns repo visibility, user access level, ledger status, and accessible team contexts.
// Works for both members and non-members on public repos.
type RepoDetailResponse struct {
	Visibility   string                  `json:"visibility"`    // "public" or "private"
	AccessLevel  string                  `json:"access_level"`  // "member" or "viewer"
	Ledger       *RepoDetailLedger       `json:"ledger"`        // null if not provisioned
	TeamContexts []RepoDetailTeamContext `json:"team_contexts"` // empty if none accessible
}

// RepoDetailLedger is the ledger section of the repo detail response.
type RepoDetailLedger struct {
	Status  string `json:"status"`            // "ready", "pending", "error"
	RepoURL string `json:"repo_url"`          // git clone URL (empty if not ready)
	Message string `json:"message,omitempty"` // status message for pending/error
}

// RepoDetailTeamContext is a team context in the repo detail response.
type RepoDetailTeamContext struct {
	TeamID      string `json:"team_id"`               // team_xxx
	Name        string `json:"name"`                  // display name
	Slug        string `json:"slug,omitempty"`         // kebab-case team slug
	Visibility  string `json:"visibility"`            // "public" or "private"
	AccessLevel string `json:"access_level"`          // "member" or "viewer"
	RepoURL     string `json:"repo_url"`              // git clone URL
}

// StableID returns the stable team identifier (team_xxx) for path construction and lookups.
func (r RepoDetailTeamContext) StableID() string {
	return r.TeamID
}

// IsReadOnly returns true if the user has viewer (read-only) access.
func (r *RepoDetailResponse) IsReadOnly() bool {
	return r.AccessLevel == "viewer"
}

// DeriveSlug generates a kebab-case slug from a display name.
// Used as fallback when the server doesn't provide a slug.
func DeriveSlug(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	s = strings.ReplaceAll(s, " ", "-")
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		}
	}
	return strings.Trim(b.String(), "-")
}

// GetRepos calls GET /api/v1/cli/repos to fetch user's team context repos.
// This is user-scoped and returns all team contexts the user has access to.
// For ledger URLs, use GetLedgerStatus() which is project-scoped.
// Requires authentication. Returns PAT, repo URLs, and token expiration.
func (c *RepoClient) GetRepos() (*ReposResponse, error) {
	reqURL := strings.TrimSuffix(c.baseURL, "/") + reposPath

	logger.LogHTTPRequest("GET", reqURL)
	start := time.Now()

	httpReq, err := useragent.NewRequest(context.Background(), "GET", reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

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
		if errMsg == "" {
			return nil, fmt.Errorf("HTTP %d from %s", resp.StatusCode, reqURL)
		}
		return nil, fmt.Errorf("HTTP %d from %s: %s", resp.StatusCode, reqURL, errMsg)
	}

	// log the raw response for debugging
	logger.LogHTTPResponseBody(string(bodyBytes))

	// decode successful response
	var reposResp ReposResponse
	if err := json.Unmarshal(bodyBytes, &reposResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &reposResp, nil
}

// GetTeamInfo calls GET /api/v1/teams/{id} to fetch team information
// including the team context repo URL and credentials.
// Requires authentication. Returns nil, nil if team not found (404).
func (c *RepoClient) GetTeamInfo(teamID string) (*TeamInfoResponse, error) {
	reqURL := strings.TrimSuffix(c.baseURL, "/") + fmt.Sprintf(teamInfoPath, teamID)

	logger.LogHTTPRequest("GET", reqURL)
	start := time.Now()

	httpReq, err := useragent.NewRequest(context.Background(), "GET", reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

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

	// handle 404 - team not found
	if resp.StatusCode == http.StatusNotFound {
		io.Copy(io.Discard, resp.Body) // drain body for connection reuse
		return nil, nil
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
			return nil, fmt.Errorf("authentication required: run 'ox login' first")
		}
		if resp.StatusCode == http.StatusForbidden {
			return nil, fmt.Errorf("access denied: you are not a member of this team")
		}
		if errMsg == "" {
			return nil, fmt.Errorf("HTTP %d from %s", resp.StatusCode, reqURL)
		}
		return nil, fmt.Errorf("HTTP %d from %s: %s", resp.StatusCode, reqURL, errMsg)
	}

	// log the raw response for debugging
	logger.LogHTTPResponseBody(string(bodyBytes))

	// decode successful response
	var teamInfo TeamInfoResponse
	if err := json.Unmarshal(bodyBytes, &teamInfo); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &teamInfo, nil
}

// GetRepoDetail calls GET /api/v1/cli/repos/{repo_id} to fetch repo detail.
// Returns visibility, access level, ledger status, and accessible team contexts.
// Works for both members (full access) and non-members on public repos (viewer access).
//
// Returns ErrForbidden if the user is not a member and the repo is private.
// Returns ErrUnauthorized if authentication fails.
// Returns nil, nil if the endpoint returns 404 (server hasn't implemented this endpoint yet).
func (c *RepoClient) GetRepoDetail(repoID string) (*RepoDetailResponse, error) {
	reqURL := strings.TrimSuffix(c.baseURL, "/") + fmt.Sprintf(repoDetailPath, repoID)

	logger.LogHTTPRequest("GET", reqURL)
	start := time.Now()

	httpReq, err := useragent.NewRequest(context.Background(), "GET", reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

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

	// handle 404 - endpoint not yet implemented on server
	if resp.StatusCode == http.StatusNotFound {
		io.Copy(io.Discard, resp.Body) // drain body for connection reuse
		return nil, nil
	}

	// read response body
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	// handle non-2xx responses
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if resp.StatusCode == http.StatusUnauthorized {
			return nil, ErrUnauthorized
		}
		if resp.StatusCode == http.StatusForbidden {
			return nil, ErrForbidden
		}
		errMsg := strings.TrimSpace(string(bodyBytes))
		if errMsg == "" {
			return nil, fmt.Errorf("HTTP %d from %s", resp.StatusCode, reqURL)
		}
		return nil, fmt.Errorf("HTTP %d from %s: %s", resp.StatusCode, reqURL, errMsg)
	}

	// log the raw response for debugging
	logger.LogHTTPResponseBody(string(bodyBytes))

	// decode successful response
	var detail RepoDetailResponse
	if err := json.Unmarshal(bodyBytes, &detail); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &detail, nil
}
