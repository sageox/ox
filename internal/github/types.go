package github

import "time"

// PullRequest represents a GitHub pull request from the REST API.
type PullRequest struct {
	Number    int        `json:"number"`
	Title     string     `json:"title"`
	Body      string     `json:"body"`
	State     string     `json:"state"` // open, closed
	User      GitHubUser `json:"user"`
	Labels    []Label    `json:"labels"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	MergedAt  *time.Time `json:"merged_at"`
	MergeSHA  string     `json:"merge_commit_sha"`
	HTMLURL   string     `json:"html_url"`
	Draft     bool       `json:"draft"`
}

// GitHubUser is a minimal GitHub user reference.
type GitHubUser struct {
	Login string `json:"login"`
}

// Label is a GitHub issue/PR label.
type Label struct {
	Name string `json:"name"`
}

// Comment represents either a PR review comment or an issue comment.
// Path and Line are only populated for review comments (file-level).
type Comment struct {
	ID        int64      `json:"id"`
	User      GitHubUser `json:"user"`
	Body      string     `json:"body"`
	Path      string     `json:"path,omitempty"`
	Line      *int       `json:"line,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
}

// Issue represents a GitHub issue from the REST API.
// Note: GitHub's API returns PRs as issues too — filter by checking
// whether the "pull_request" field is present (excluded in our struct).
type Issue struct {
	Number    int        `json:"number"`
	Title     string     `json:"title"`
	Body      string     `json:"body"`
	State     string     `json:"state"` // open, closed
	User      GitHubUser `json:"user"`
	Labels    []Label    `json:"labels"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	ClosedAt  *time.Time `json:"closed_at"`
	HTMLURL   string     `json:"html_url"`
	// PullRequest is non-nil when this "issue" is actually a PR.
	// Used to filter out PRs from issue listings.
	PullRequest *struct{} `json:"pull_request,omitempty"`
}

// ListIssuesOptions controls pagination and filtering for ListIssues.
type ListIssuesOptions struct {
	State     string    // "all", "open", "closed" (default: "all")
	Sort      string    // "updated" (default)
	Direction string    // "desc" (default)
	Since     time.Time // stop pagination when issues are older than this
	PerPage   int       // max 100 (default: 100)
	Page      int       // starting page (default: 1)
}

// RateLimit captures GitHub API rate limit state from response headers.
type RateLimit struct {
	Remaining int
	Limit     int
	Reset     time.Time
}

// ListPRsOptions controls pagination and filtering for ListPullRequests.
type ListPRsOptions struct {
	State     string    // "all", "open", "closed" (default: "all")
	Sort      string    // "updated" (default)
	Direction string    // "desc" (default)
	Since     time.Time // stop pagination when PRs are older than this
	PerPage   int       // max 100 (default: 100)
	Page      int       // starting page (default: 1)
}
