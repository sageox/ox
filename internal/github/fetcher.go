package github

import (
	"context"

	"github.com/sageox/ox/internal/ledger"
)

// Fetcher wraps a Client to satisfy ledger.GitHubFetcher.
type Fetcher struct {
	client *Client
}

// NewFetcher creates a GitHubFetcher from a Client.
func NewFetcher(c *Client) *Fetcher {
	return &Fetcher{client: c}
}

func (f *Fetcher) ListPullRequests(ctx context.Context, owner, repo string, opts ledger.ListPRsOptions) ([]ledger.FetchedPR, *ledger.FetchRateLimit, error) {
	prs, rl, err := f.client.ListPullRequests(ctx, owner, repo, ListPRsOptions{
		State: opts.State,
		Since: opts.Since,
	})
	if err != nil {
		return nil, convertRL(rl), err
	}

	result := make([]ledger.FetchedPR, len(prs))
	for i, pr := range prs {
		var labels []string
		for _, l := range pr.Labels {
			labels = append(labels, l.Name)
		}
		result[i] = ledger.FetchedPR{
			Number:    pr.Number,
			Title:     pr.Title,
			Body:      pr.Body,
			State:     pr.State,
			Author:    pr.User.Login,
			Labels:    labels,
			CreatedAt: pr.CreatedAt,
			UpdatedAt: pr.UpdatedAt,
			MergedAt:  pr.MergedAt,
			MergeSHA:  pr.MergeSHA,
			HTMLURL:   pr.HTMLURL,
		}
	}
	return result, convertRL(rl), nil
}

func (f *Fetcher) ListIssues(ctx context.Context, owner, repo string, opts ledger.ListIssuesOptions) ([]ledger.FetchedIssue, *ledger.FetchRateLimit, error) {
	issues, rl, err := f.client.ListIssues(ctx, owner, repo, ListIssuesOptions{
		State: opts.State,
		Since: opts.Since,
	})
	if err != nil {
		return nil, convertRL(rl), err
	}

	result := make([]ledger.FetchedIssue, len(issues))
	for i, issue := range issues {
		var labels []string
		for _, l := range issue.Labels {
			labels = append(labels, l.Name)
		}
		result[i] = ledger.FetchedIssue{
			Number:    issue.Number,
			Title:     issue.Title,
			Body:      issue.Body,
			State:     issue.State,
			Author:    issue.User.Login,
			Labels:    labels,
			CreatedAt: issue.CreatedAt,
			UpdatedAt: issue.UpdatedAt,
			ClosedAt:  issue.ClosedAt,
			HTMLURL:   issue.HTMLURL,
		}
	}
	return result, convertRL(rl), nil
}

func (f *Fetcher) ListPRComments(ctx context.Context, owner, repo string, number int) ([]ledger.FetchedComment, error) {
	reviewComments, err := f.client.ListPRComments(ctx, owner, repo, number)
	if err != nil {
		return nil, err
	}

	result := make([]ledger.FetchedComment, len(reviewComments))
	for i, c := range reviewComments {
		result[i] = ledger.FetchedComment{
			Author:    c.User.Login,
			Body:      c.Body,
			Path:      c.Path,
			Line:      c.Line,
			CreatedAt: c.CreatedAt,
		}
	}
	return result, nil
}

func (f *Fetcher) ListIssueComments(ctx context.Context, owner, repo string, number int) ([]ledger.FetchedComment, error) {
	comments, err := f.client.ListIssueComments(ctx, owner, repo, number)
	if err != nil {
		return nil, err
	}

	result := make([]ledger.FetchedComment, len(comments))
	for i, c := range comments {
		result[i] = ledger.FetchedComment{
			Author:    c.User.Login,
			Body:      c.Body,
			CreatedAt: c.CreatedAt,
		}
	}
	return result, nil
}

func convertRL(rl *RateLimit) *ledger.FetchRateLimit {
	if rl == nil {
		return nil
	}
	return &ledger.FetchRateLimit{
		Remaining: rl.Remaining,
		Limit:     rl.Limit,
		Reset:     rl.Reset,
	}
}
