package github

import (
	"context"
	"fmt"
)

// ListIssues fetches issues for a repository with pagination.
// Filters out pull requests (GitHub API returns PRs as issues).
// When opts.Since is set, pagination stops when issues older than Since are
// encountered (requires Sort="updated", Direction="desc" to work correctly).
func (c *Client) ListIssues(ctx context.Context, owner, repo string, opts ListIssuesOptions) ([]Issue, *RateLimit, error) {
	opts = applyIssueDefaults(opts)

	var allIssues []Issue
	var lastRL *RateLimit
	page := opts.Page

	for {
		path := fmt.Sprintf("/repos/%s/%s/issues?state=%s&sort=%s&direction=%s&per_page=%d&page=%d",
			owner, repo, opts.State, opts.Sort, opts.Direction, opts.PerPage, page)

		var issues []Issue
		rl, err := c.doRequest(ctx, "GET", path, &issues)
		if err != nil {
			return allIssues, rl, err
		}
		lastRL = rl

		if len(issues) == 0 {
			break
		}

		if !opts.Since.IsZero() {
			for _, issue := range issues {
				if issue.UpdatedAt.Before(opts.Since) {
					return allIssues, lastRL, nil
				}
				// filter out PRs (GitHub issues API includes them)
				if issue.PullRequest != nil {
					continue
				}
				allIssues = append(allIssues, issue)
			}
		} else {
			for _, issue := range issues {
				if issue.PullRequest != nil {
					continue
				}
				allIssues = append(allIssues, issue)
			}
		}

		if len(issues) < opts.PerPage {
			break
		}

		page++
	}

	return allIssues, lastRL, nil
}

// applyIssueDefaults fills in zero-value fields with sensible defaults.
func applyIssueDefaults(opts ListIssuesOptions) ListIssuesOptions {
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
