package github

import (
	"context"
	"fmt"
	"time"

	"github.com/sageox/ox/internal/githubmirror"
)

// MirrorFetcher adapts a Client to githubmirror.Fetcher.
//
// Errors keep the Client's sentinels reachable through errors.Is
// (ErrGitHubAuth, ErrGitHubRateLimited, ledger.ErrGitHubNotFound) so the relay
// can back off by cause. A failed call never returns partial data: a truncated
// list that looks like a complete one would make the relay treat items as
// gone.
type MirrorFetcher struct {
	client *Client
}

var _ githubmirror.Fetcher = (*MirrorFetcher)(nil)

// NewMirrorFetcher returns the production githubmirror.Fetcher.
func NewMirrorFetcher(c *Client) *MirrorFetcher { return &MirrorFetcher{client: c} }

func (f *MirrorFetcher) Repo(ctx context.Context, owner, name string) (githubmirror.Repo, error) {
	info, err := f.client.GetRepo(ctx, owner, name)
	if err != nil {
		return githubmirror.Repo{}, fmt.Errorf("get repo %s/%s: %w", owner, name, err)
	}
	return githubmirror.Repo{
		Owner:    info.Owner.Login,
		Name:     info.Name,
		FullName: info.FullName,
		ID:       info.ID,
		Private:  info.Private,
	}, nil
}

// ListPullRequests returns every PR (any state) updated at or after since,
// newest first. A zero since lists the repo's whole history.
func (f *MirrorFetcher) ListPullRequests(ctx context.Context, owner, name string, since time.Time) ([]githubmirror.SourcePR, error) {
	// Since only works with updated-desc ordering: pagination stops at the
	// first item older than the cutoff.
	prs, _, err := f.client.ListPullRequests(ctx, owner, name, ListPRsOptions{
		State:     "all",
		Sort:      "updated",
		Direction: "desc",
		Since:     since,
	})
	if err != nil {
		return nil, fmt.Errorf("list pull requests %s/%s: %w", owner, name, err)
	}

	result := make([]githubmirror.SourcePR, len(prs))
	for i, pr := range prs {
		result[i] = githubmirror.SourcePR{
			Number:    pr.Number,
			Title:     pr.Title,
			Body:      pr.Body,
			State:     pr.State,
			Draft:     pr.Draft,
			Author:    mirrorAuthor(pr.User, pr.AuthorAssociation),
			Labels:    labelNames(pr.Labels),
			CreatedAt: pr.CreatedAt,
			UpdatedAt: pr.UpdatedAt,
			ClosedAt:  pr.ClosedAt,
			MergedAt:  pr.MergedAt,
			HTMLURL:   pr.HTMLURL,
		}
	}
	return result, nil
}

// ListIssues returns issues only. GitHub's issues endpoint also lists pull
// requests; Client.ListIssues drops them (Issue.PullRequest != nil) so a PR is
// never mirrored twice, once as each kind.
func (f *MirrorFetcher) ListIssues(ctx context.Context, owner, name string, since time.Time) ([]githubmirror.SourceIssue, error) {
	issues, _, err := f.client.ListIssues(ctx, owner, name, ListIssuesOptions{
		State:     "all",
		Sort:      "updated",
		Direction: "desc",
		Since:     since,
	})
	if err != nil {
		return nil, fmt.Errorf("list issues %s/%s: %w", owner, name, err)
	}

	result := make([]githubmirror.SourceIssue, len(issues))
	for i, issue := range issues {
		result[i] = githubmirror.SourceIssue{
			Number:    issue.Number,
			Title:     issue.Title,
			Body:      issue.Body,
			State:     issue.State,
			Author:    mirrorAuthor(issue.User, issue.AuthorAssociation),
			Labels:    labelNames(issue.Labels),
			CreatedAt: issue.CreatedAt,
			UpdatedAt: issue.UpdatedAt,
			ClosedAt:  issue.ClosedAt,
			HTMLURL:   issue.HTMLURL,
		}
	}
	return result, nil
}

func (f *MirrorFetcher) ListIssueComments(ctx context.Context, owner, name string, number int) ([]githubmirror.SourceComment, error) {
	comments, err := f.client.ListIssueComments(ctx, owner, name, number)
	if err != nil {
		return nil, fmt.Errorf("list issue comments %s/%s#%d: %w", owner, name, number, err)
	}
	return sourceComments(comments), nil
}

func (f *MirrorFetcher) ListReviewComments(ctx context.Context, owner, name string, number int) ([]githubmirror.SourceComment, error) {
	comments, err := f.client.ListPRComments(ctx, owner, name, number)
	if err != nil {
		return nil, fmt.Errorf("list review comments %s/%s#%d: %w", owner, name, number, err)
	}
	return sourceComments(comments), nil
}

func (f *MirrorFetcher) ListReviews(ctx context.Context, owner, name string, number int) ([]githubmirror.SourceReview, error) {
	reviews, err := f.client.ListPRReviews(ctx, owner, name, number)
	if err != nil {
		return nil, fmt.Errorf("list reviews %s/%s#%d: %w", owner, name, number, err)
	}

	result := make([]githubmirror.SourceReview, len(reviews))
	for i, r := range reviews {
		result[i] = githubmirror.SourceReview{
			ID:          r.ID,
			Author:      mirrorAuthor(r.User, r.AuthorAssociation),
			State:       r.State,
			SubmittedAt: r.SubmittedAt,
		}
	}
	return result, nil
}

func (f *MirrorFetcher) ListPRFiles(ctx context.Context, owner, name string, number, limit int) ([]string, bool, error) {
	paths, truncated, err := f.client.ListPRFiles(ctx, owner, name, number, limit)
	if err != nil {
		return nil, false, fmt.Errorf("list pr files %s/%s#%d: %w", owner, name, number, err)
	}
	return paths, truncated, nil
}

// mirrorAuthor maps a GitHub account to the mirror's Author. The numeric id and
// the account type (not the login) are what trust decisions rest on; a deleted
// account arrives as a null user and maps to the zero Author.
func mirrorAuthor(u GitHubUser, association string) githubmirror.Author {
	return githubmirror.Author{
		Login:       u.Login,
		ID:          u.ID,
		Association: association,
		Type:        u.Type,
	}
}

// labelNames returns label names as a non-nil slice so an unlabeled item
// serializes as [] rather than null.
func labelNames(labels []Label) []string {
	names := make([]string, len(labels))
	for i, l := range labels {
		names[i] = l.Name
	}
	return names
}

func sourceComments(comments []Comment) []githubmirror.SourceComment {
	result := make([]githubmirror.SourceComment, len(comments))
	for i, c := range comments {
		result[i] = githubmirror.SourceComment{
			ID:        c.ID,
			Author:    mirrorAuthor(c.User, c.AuthorAssociation),
			Body:      c.Body,
			CreatedAt: c.CreatedAt,
			UpdatedAt: c.UpdatedAt,
			Path:      c.Path,
			Line:      c.Line,
		}
	}
	return result
}
