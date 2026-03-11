package github

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/sageox/ox/internal/logger"
	"github.com/sageox/ox/internal/useragent"
)

// Client is a minimal GitHub REST API client for fetching PR data.
type Client struct {
	token      string
	httpClient *http.Client
	baseURL    string
}

// NewClient creates a GitHub API client with the given personal access token.
func NewClient(token string) *Client {
	return &Client{
		token: token,
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
		},
		baseURL: "https://api.github.com",
	}
}

// ListPullRequests fetches pull requests for a repository with pagination.
// When opts.Since is set, pagination stops when PRs older than Since are
// encountered (requires Sort="updated", Direction="desc" to work correctly).
func (c *Client) ListPullRequests(ctx context.Context, owner, repo string, opts ListPRsOptions) ([]PullRequest, *RateLimit, error) {
	opts = applyDefaults(opts)

	var allPRs []PullRequest
	var lastRL *RateLimit
	page := opts.Page

	for {
		path := fmt.Sprintf("/repos/%s/%s/pulls?state=%s&sort=%s&direction=%s&per_page=%d&page=%d",
			owner, repo, opts.State, opts.Sort, opts.Direction, opts.PerPage, page)

		var prs []PullRequest
		rl, err := c.doRequest(ctx, "GET", path, &prs)
		if err != nil {
			return allPRs, rl, err
		}
		lastRL = rl

		if len(prs) == 0 {
			break
		}

		// when Since is set, filter out PRs updated before the cutoff and stop
		if !opts.Since.IsZero() {
			for _, pr := range prs {
				if pr.UpdatedAt.Before(opts.Since) {
					return allPRs, lastRL, nil
				}
				allPRs = append(allPRs, pr)
			}
		} else {
			allPRs = append(allPRs, prs...)
		}

		// fewer results than requested means last page
		if len(prs) < opts.PerPage {
			break
		}

		page++
	}

	return allPRs, lastRL, nil
}

// ListPRComments fetches all review comments (file-level) on a pull request.
func (c *Client) ListPRComments(ctx context.Context, owner, repo string, number int) ([]Comment, error) {
	return c.paginateComments(ctx, fmt.Sprintf("/repos/%s/%s/pulls/%d/comments", owner, repo, number))
}

// ListIssueComments fetches all general discussion comments on a pull request.
func (c *Client) ListIssueComments(ctx context.Context, owner, repo string, number int) ([]Comment, error) {
	return c.paginateComments(ctx, fmt.Sprintf("/repos/%s/%s/issues/%d/comments", owner, repo, number))
}

// paginateComments fetches all comments from a paginated endpoint.
func (c *Client) paginateComments(ctx context.Context, basePath string) ([]Comment, error) {
	var all []Comment
	page := 1

	for {
		path := fmt.Sprintf("%s?per_page=100&page=%d", basePath, page)

		var comments []Comment
		if _, err := c.doRequest(ctx, "GET", path, &comments); err != nil {
			return all, err
		}

		if len(comments) == 0 {
			break
		}

		all = append(all, comments...)

		if len(comments) < 100 {
			break
		}

		page++
	}

	return all, nil
}

// doRequest executes an authenticated GitHub API request and decodes the JSON
// response into result. Returns parsed rate limit headers and any error.
func (c *Client) doRequest(ctx context.Context, method, path string, result interface{}) (*RateLimit, error) {
	url := c.baseURL + path

	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", useragent.String())

	logger.LogHTTPRequest(method, url)
	start := time.Now()

	resp, err := c.httpClient.Do(req)
	duration := time.Since(start)
	if err != nil {
		logger.LogHTTPError(method, url, err, duration)
		return nil, fmt.Errorf("github api request failed: %w", err)
	}
	defer resp.Body.Close()

	logger.LogHTTPResponse(method, url, resp.StatusCode, duration)

	rl := parseRateLimit(resp.Header)
	if rl != nil && rl.Remaining < 100 {
		logger.Warn("github rate limit low", "remaining", rl.Remaining, "limit", rl.Limit, "reset", rl.Reset.Format(time.RFC3339))
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return rl, fmt.Errorf("github api status %d: %s", resp.StatusCode, string(body))
	}

	if err := json.NewDecoder(resp.Body).Decode(result); err != nil {
		return rl, fmt.Errorf("decode github response: %w", err)
	}

	return rl, nil
}

// parseRateLimit extracts rate limit information from GitHub response headers.
func parseRateLimit(h http.Header) *RateLimit {
	remaining := h.Get("X-RateLimit-Remaining")
	limit := h.Get("X-RateLimit-Limit")
	reset := h.Get("X-RateLimit-Reset")

	if remaining == "" && limit == "" {
		return nil
	}

	rl := &RateLimit{}

	if v, err := strconv.Atoi(remaining); err == nil {
		rl.Remaining = v
	}
	if v, err := strconv.Atoi(limit); err == nil {
		rl.Limit = v
	}
	if v, err := strconv.ParseInt(reset, 10, 64); err == nil {
		rl.Reset = time.Unix(v, 0)
	}

	return rl
}

// applyDefaults fills in zero-value fields with sensible defaults.
func applyDefaults(opts ListPRsOptions) ListPRsOptions {
	if opts.State == "" {
		opts.State = "all"
	}
	if opts.Sort == "" {
		opts.Sort = "updated"
	}
	if opts.Direction == "" {
		opts.Direction = "desc"
	}
	if opts.PerPage <= 0 || opts.PerPage > 100 {
		opts.PerPage = 100
	}
	if opts.Page <= 0 {
		opts.Page = 1
	}
	return opts
}
