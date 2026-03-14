package ledger

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// GitHubFetcher abstracts the GitHub API client for sync operations.
// *github.Client satisfies this interface. Defined here to avoid
// a circular import between internal/ledger and internal/github.
type GitHubFetcher interface {
	ListPullRequests(ctx context.Context, owner, repo string, opts ListPRsOptions) ([]FetchedPR, *FetchRateLimit, error)
	ListIssues(ctx context.Context, owner, repo string, opts ListIssuesOptions) ([]FetchedIssue, *FetchRateLimit, error)
	ListPRComments(ctx context.Context, owner, repo string, number int) ([]FetchedComment, error)
	ListIssueComments(ctx context.Context, owner, repo string, number int) ([]FetchedComment, error)
}

// FetchedPR is a GitHub pull request as returned by the fetcher.
// Mirrors the fields we need from the GitHub API response.
type FetchedPR struct {
	Number    int
	Title     string
	Body      string
	State     string // "open", "closed"
	Author    string
	Labels    []string
	CreatedAt time.Time
	UpdatedAt time.Time
	MergedAt  *time.Time
	MergeSHA  string
	HTMLURL   string
}

// FetchedIssue is a GitHub issue as returned by the fetcher.
type FetchedIssue struct {
	Number    int
	Title     string
	Body      string
	State     string // "open", "closed"
	Author    string
	Labels    []string
	CreatedAt time.Time
	UpdatedAt time.Time
	ClosedAt  *time.Time
	HTMLURL   string
}

// FetchedComment is a comment from the GitHub API.
type FetchedComment struct {
	Author    string
	Body      string
	Path      string // file path for review comments
	Line      *int   // line number for review comments
	CreatedAt time.Time
}

// FetchRateLimit captures GitHub API rate limit state.
type FetchRateLimit struct {
	Remaining int
	Limit     int
	Reset     time.Time
}

// ListPRsOptions controls pagination and filtering for PR listing.
type ListPRsOptions struct {
	State string
	Since time.Time
}

// ListIssuesOptions controls pagination and filtering for issue listing.
type ListIssuesOptions struct {
	State string
	Since time.Time
}

// SyncResult tracks how many PRs and issues were synced.
type SyncResult struct {
	PRTotal      int
	PRCreated    int
	PRUpdated    int
	IssueTotal   int
	IssueCreated int
	IssueUpdated int
}

// SyncPRs fetches PRs from GitHub and writes them to the ledger.
// Uses cursor-based incremental sync to minimize API calls.
func SyncPRs(ctx context.Context, fetcher GitHubFetcher, ledgerPath, owner, repo string, maxDays int, logger *slog.Logger) (*SyncResult, error) {
	state, err := ReadGitHubTypeSyncState(ledgerPath, "pr")
	if err != nil {
		return nil, fmt.Errorf("read pr sync state: %w", err)
	}

	since := time.Now().AddDate(0, 0, -maxDays)
	if !state.LastSyncAt.IsZero() && state.LastSyncAt.After(since) {
		since = state.LastSyncAt
	}

	logger.Info("fetching PRs from GitHub", "owner", owner, "repo", repo, "since", since.Format(time.RFC3339))

	prs, _, err := fetcher.ListPullRequests(ctx, owner, repo, ListPRsOptions{
		State: "all",
		Since: since,
	})
	if err != nil {
		return nil, fmt.Errorf("list PRs: %w", err)
	}

	result := &SyncResult{}

	for _, pr := range prs {
		if err := ctx.Err(); err != nil {
			return result, err
		}

		// determine state: GitHub uses "open"/"closed", we add "merged"
		prState := pr.State
		if pr.MergedAt != nil {
			prState = "merged"
		}

		// detect state transitions to trigger full comment re-extract
		prevState, known := state.KnownStates[pr.Number]
		stateChanged := known && prevState != prState

		// fetch comments when new or state changed
		var comments []PRComment
		if !known || stateChanged {
			comments = fetchPRComments(ctx, fetcher, owner, repo, pr.Number, logger)
		}

		prFile := &PRFile{
			Number:      pr.Number,
			Title:       pr.Title,
			Body:        pr.Body,
			Author:      pr.Author,
			State:       prState,
			Labels:      pr.Labels,
			CreatedAt:   pr.CreatedAt,
			MergedAt:    pr.MergedAt,
			UpdatedAt:   pr.UpdatedAt,
			MergeCommit: pr.MergeSHA,
			URL:         pr.HTMLURL,
			Comments:    comments,
		}

		if !known {
			result.PRCreated++
		} else {
			result.PRUpdated++
		}

		if err := WriteGitHubPR(ledgerPath, prFile); err != nil {
			return result, fmt.Errorf("write PR %d: %w", pr.Number, err)
		}

		state.KnownStates[pr.Number] = prState
		result.PRTotal++
	}

	// persist updated state
	state.LastSyncAt = time.Now()
	state.Count += result.PRCreated
	if err := WriteGitHubTypeSyncState(ledgerPath, "pr", state); err != nil {
		return result, fmt.Errorf("write pr sync state: %w", err)
	}

	return result, nil
}

// SyncIssues fetches issues from GitHub and writes them to the ledger.
func SyncIssues(ctx context.Context, fetcher GitHubFetcher, ledgerPath, owner, repo string, maxDays int, logger *slog.Logger) (*SyncResult, error) {
	state, err := ReadGitHubTypeSyncState(ledgerPath, "issue")
	if err != nil {
		return nil, fmt.Errorf("read issue sync state: %w", err)
	}

	since := time.Now().AddDate(0, 0, -maxDays)
	if !state.LastSyncAt.IsZero() && state.LastSyncAt.After(since) {
		since = state.LastSyncAt
	}

	logger.Info("fetching issues from GitHub", "owner", owner, "repo", repo, "since", since.Format(time.RFC3339))

	issues, _, err := fetcher.ListIssues(ctx, owner, repo, ListIssuesOptions{
		State: "all",
		Since: since,
	})
	if err != nil {
		return nil, fmt.Errorf("list issues: %w", err)
	}

	result := &SyncResult{}

	for _, issue := range issues {
		if err := ctx.Err(); err != nil {
			return result, err
		}

		prevState, known := state.KnownStates[issue.Number]
		stateChanged := known && prevState != issue.State

		var comments []IssueComment
		if !known || stateChanged {
			fetched, cErr := fetcher.ListIssueComments(ctx, owner, repo, issue.Number)
			if cErr != nil {
				logger.Warn("fetch issue comments failed", "issue", issue.Number, "error", cErr)
			} else {
				for _, c := range fetched {
					comments = append(comments, IssueComment{
						Author:    c.Author,
						Body:      c.Body,
						CreatedAt: c.CreatedAt,
					})
				}
			}
		}

		issueFile := &IssueFile{
			Number:    issue.Number,
			Title:     issue.Title,
			Body:      issue.Body,
			Author:    issue.Author,
			State:     issue.State,
			Labels:    issue.Labels,
			CreatedAt: issue.CreatedAt,
			ClosedAt:  issue.ClosedAt,
			UpdatedAt: issue.UpdatedAt,
			URL:       issue.HTMLURL,
			Comments:  comments,
		}

		if !known {
			result.IssueCreated++
		} else {
			result.IssueUpdated++
		}

		if err := WriteGitHubIssue(ledgerPath, issueFile); err != nil {
			return result, fmt.Errorf("write issue %d: %w", issue.Number, err)
		}

		state.KnownStates[issue.Number] = issue.State
		result.IssueTotal++
	}

	state.LastSyncAt = time.Now()
	state.Count += result.IssueCreated
	if err := WriteGitHubTypeSyncState(ledgerPath, "issue", state); err != nil {
		return result, fmt.Errorf("write issue sync state: %w", err)
	}

	return result, nil
}

func fetchPRComments(ctx context.Context, fetcher GitHubFetcher, owner, repo string, number int, logger *slog.Logger) []PRComment {
	var comments []PRComment

	reviewComments, err := fetcher.ListPRComments(ctx, owner, repo, number)
	if err != nil {
		logger.Warn("fetch review comments failed", "pr", number, "error", err)
	} else {
		for _, c := range reviewComments {
			comments = append(comments, PRComment(c))
		}
	}

	issueComments, err := fetcher.ListIssueComments(ctx, owner, repo, number)
	if err != nil {
		logger.Warn("fetch issue comments failed", "pr", number, "error", err)
	} else {
		for _, c := range issueComments {
			comments = append(comments, PRComment(c))
		}
	}

	return comments
}
