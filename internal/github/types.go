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
	ClosedAt  *time.Time `json:"closed_at"`
	MergedAt  *time.Time `json:"merged_at"`
	MergeSHA  string     `json:"merge_commit_sha"`
	HTMLURL   string     `json:"html_url"`
	Draft     bool       `json:"draft"`
	// AuthorAssociation is GitHub's relationship of the author to the repo
	// (OWNER, MEMBER, COLLABORATOR, CONTRIBUTOR, NONE, ...). The mirror derives
	// its trust tier from it, never from text.
	AuthorAssociation string `json:"author_association"`
}

// GitHubUser is a minimal GitHub user reference. ID is the numeric account id,
// the only identity the mirror trusts; Type is "User" or "Bot".
type GitHubUser struct {
	Login string `json:"login"`
	ID    int64  `json:"id"`
	Type  string `json:"type"`
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
	UpdatedAt time.Time  `json:"updated_at"`
	// AuthorAssociation: see PullRequest.AuthorAssociation.
	AuthorAssociation string `json:"author_association"`
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
	// AuthorAssociation: see PullRequest.AuthorAssociation.
	AuthorAssociation string `json:"author_association"`
	// PullRequest is non-nil when this "issue" is actually a PR.
	// Used to filter out PRs from issue listings.
	PullRequest *struct{} `json:"pull_request,omitempty"`
}

// Review is one review a person or bot left on a pull request. State is
// APPROVED, CHANGES_REQUESTED, COMMENTED, DISMISSED or PENDING. SubmittedAt is
// the zero time for a PENDING review, which GitHub has not submitted yet.
type Review struct {
	ID                int64      `json:"id"`
	User              GitHubUser `json:"user"`
	State             string     `json:"state"`
	SubmittedAt       time.Time  `json:"submitted_at"`
	AuthorAssociation string     `json:"author_association"`
}

// PRFile is one file changed by a pull request. Only the path is kept; the
// mirror never relays diffs.
type PRFile struct {
	Filename string `json:"filename"`
}

// RepoInfo is the subset of GET /repos/{owner}/{repo} the mirror needs.
// Private is what lets the server enforce the team's private-repo opt-in.
type RepoInfo struct {
	ID       int64      `json:"id"`
	Name     string     `json:"name"`
	FullName string     `json:"full_name"`
	Private  bool       `json:"private"`
	Owner    GitHubUser `json:"owner"`
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

// PRCommit represents a commit from a pull request's commit list.
// Returned by the /repos/{owner}/{repo}/pulls/{number}/commits endpoint.
type PRCommit struct {
	SHA    string    `json:"sha"`
	Author string    // extracted from commit.author.name or author.login
	Date   time.Time // extracted from commit.author.date
	Msg    string    // extracted from commit.message
}

// prCommitJSON is the raw GitHub API response shape for PR commits.
type prCommitJSON struct {
	SHA    string `json:"sha"`
	Commit struct {
		Message string `json:"message"`
		Author  struct {
			Name string    `json:"name"`
			Date time.Time `json:"date"`
		} `json:"author"`
	} `json:"commit"`
	Author *struct {
		Login string `json:"login"`
	} `json:"author"`
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
