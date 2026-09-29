package ledger

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
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
	ListPRCommits(ctx context.Context, owner, repo string, number int) ([]FetchedPRCommit, error)
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

// FetchedPRCommit is a commit from a PR's branch, as returned by the fetcher.
type FetchedPRCommit struct {
	SHA    string
	Author string
	Date   time.Time
	Msg    string
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

	// rebuild KnownItems from disk when sync state is cold (first run,
	// cache lost, daemon reclone). Without this, every PR is treated as
	// "unknown" and triggers per-PR API calls for comments and commits —
	// ~3 requests × N PRs — which hangs for minutes on large repos.
	if len(state.KnownItems) == 0 {
		rebuilt, rebuildErr := rebuildSyncStateFromDisk(ledgerPath, "pr", logger)
		if rebuildErr != nil {
			logger.Warn("rebuild pr sync state from disk failed", "error", rebuildErr)
		} else if len(rebuilt.KnownItems) > 0 {
			state = rebuilt
			logger.Info("rebuilt pr sync state from disk", "known", len(state.KnownItems))
		}
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

		// detect state transitions and content updates to trigger re-extract
		prev, known := state.KnownItems[pr.Number]
		stateChanged := known && prev.State != prState
		updatedAtChanged := known && !prev.UpdatedAt.Equal(pr.UpdatedAt)

		// skip only if both state AND updated_at are unchanged —
		// a same-state update (new comments, edits) changes updated_at
		if known && !stateChanged && !updatedAtChanged {
			continue
		}

		// fetch comments for new PRs or state transitions
		comments := fetchPRComments(ctx, fetcher, owner, repo, pr.Number, logger)

		// fetch commits for merged PRs — only when first seen as merged
		// (commit list is immutable once merged, no need to re-fetch)
		var commits []PRCommit
		if prState == "merged" {
			commits = fetchPRCommits(ctx, fetcher, owner, repo, pr.Number, logger)
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
			Commits:     commits,
		}

		if !known {
			result.PRCreated++
		} else {
			result.PRUpdated++
		}

		if err := WriteGitHubPR(ledgerPath, prFile); err != nil {
			return result, fmt.Errorf("write PR %d: %w", pr.Number, err)
		}

		state.KnownItems[pr.Number] = KnownItem{State: prState, UpdatedAt: pr.UpdatedAt}
		result.PRTotal++
	}

	// backfill commits for merged PRs synced before the commits feature
	backfilled, bfErr := BackfillPRCommits(ctx, fetcher, ledgerPath, owner, repo, logger)
	if bfErr != nil {
		logger.Warn("backfill PR commits failed", "error", bfErr)
	} else if backfilled > 0 {
		result.PRUpdated += backfilled
		result.PRTotal += backfilled
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

	// rebuild KnownItems from disk when sync state is cold
	if len(state.KnownItems) == 0 {
		rebuilt, rebuildErr := rebuildSyncStateFromDisk(ledgerPath, "issue", logger)
		if rebuildErr != nil {
			logger.Warn("rebuild issue sync state from disk failed", "error", rebuildErr)
		} else if len(rebuilt.KnownItems) > 0 {
			state = rebuilt
			logger.Info("rebuilt issue sync state from disk", "known", len(state.KnownItems))
		}
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

		prev, known := state.KnownItems[issue.Number]
		stateChanged := known && prev.State != issue.State
		updatedAtChanged := known && !prev.UpdatedAt.Equal(issue.UpdatedAt)

		// skip only if both state AND updated_at are unchanged
		if known && !stateChanged && !updatedAtChanged {
			continue
		}

		var comments []IssueComment
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

		state.KnownItems[issue.Number] = KnownItem{State: issue.State, UpdatedAt: issue.UpdatedAt}
		result.IssueTotal++
	}

	state.LastSyncAt = time.Now()
	state.Count += result.IssueCreated
	if err := WriteGitHubTypeSyncState(ledgerPath, "issue", state); err != nil {
		return result, fmt.Errorf("write issue sync state: %w", err)
	}

	return result, nil
}

// BackfillPRCommits scans existing merged PR JSON files in the ledger and
// fetches commits for those that were synced before the commits feature existed.
// Returns the number of PRs backfilled.
func BackfillPRCommits(ctx context.Context, fetcher GitHubFetcher, ledgerPath, owner, repo string, logger *slog.Logger) (int, error) {
	prFiles, err := ListGitHubDataFiles(ledgerPath, "pr")
	if err != nil {
		return 0, fmt.Errorf("list PR files: %w", err)
	}

	// Deduplicate: when multiple hash-variant files exist for the same PR,
	// keep only the path with the latest updated_at, preferring the variant
	// that still lacks commits when that ties (see below).
	type prPathInfo struct {
		path       string
		updatedAt  time.Time
		hasCommits bool
	}
	bestByNumber := make(map[int]prPathInfo)

	for _, path := range prFiles {
		data, err := os.ReadFile(path)
		if err != nil {
			logger.Warn("read PR file for backfill failed", "path", path, "error", err)
			continue
		}

		var stub struct {
			Number    int               `json:"number"`
			UpdatedAt time.Time         `json:"updated_at"`
			Commits   []json.RawMessage `json:"commits"`
		}
		if err := json.Unmarshal(data, &stub); err != nil {
			logger.Warn("unmarshal PR file for backfill failed", "path", path, "error", err)
			continue
		}

		cand := prPathInfo{path: path, updatedAt: stub.UpdatedAt, hasCommits: len(stub.Commits) > 0}
		prev, exists := bestByNumber[stub.Number]

		// On a tie, prefer the snapshot that still lacks commits. A ledger
		// written before the supersede below existed can hold both variants of
		// one GitHub state, and their updated_at is identical by construction.
		// Picking the enriched one there would make this loop skip the PR (it
		// already has commits) and strand the commit-less duplicate on disk
		// forever — exactly the tie a reader can lose. Picking the commit-less
		// one re-runs the enrichment and lets the supersede clean it up, so an
		// already-broken ledger heals on the next backfill instead of needing
		// the writer to have been fixed before the files were written.
		better := !exists ||
			cand.updatedAt.After(prev.updatedAt) ||
			(cand.updatedAt.Equal(prev.updatedAt) && prev.hasCommits && !cand.hasCommits)
		if better {
			bestByNumber[stub.Number] = cand
		}
	}

	var backfilled int
	for _, info := range bestByNumber {
		if err := ctx.Err(); err != nil {
			return backfilled, err
		}

		data, err := os.ReadFile(info.path)
		if err != nil {
			logger.Warn("read PR file for backfill failed", "path", info.path, "error", err)
			continue
		}

		var pr PRFile
		if err := json.Unmarshal(data, &pr); err != nil {
			logger.Warn("unmarshal PR file for backfill failed", "path", info.path, "error", err)
			continue
		}

		// only backfill merged PRs that have no commits
		if pr.State != "merged" || len(pr.Commits) > 0 {
			continue
		}

		commits := fetchPRCommits(ctx, fetcher, owner, repo, pr.Number, logger)
		if len(commits) == 0 {
			continue
		}

		pr.Commits = commits
		newPath, err := writeGitHubPR(ledgerPath, &pr)
		if err != nil {
			logger.Warn("write backfilled PR failed", "pr", pr.Number, "error", err)
			continue
		}

		// Drop the snapshot we just enriched. Both files describe the SAME
		// GitHub state — backfilling commits does not change the PR upstream,
		// so updated_at is identical in both — and every consumer that has to
		// pick one (findLatestFile here, candidateBeats in
		// internal/codedb/index, the dedup above) orders by updated_at first.
		// With that key tied the choice falls through to mtime, which ties too
		// when both writes land in the same tick, and the winner is then the
		// content hash: a coin flip that can hand a reader the commit-less
		// snapshot. Leaving a strictly-less-complete duplicate of the same
		// state on disk is what makes the tie possible, so remove it.
		//
		// Deliberately NOT fixed by bumping pr.UpdatedAt: that field mirrors
		// GitHub's own updated_at, and RebuildSyncStateFromFiles feeds the max
		// of it into GitHubTypeSyncState.LastSyncAt, which is the `since`
		// cursor for the next incremental fetch. Inflating it would make ox
		// skip PRs updated between GitHub's timestamp and ours.
		// The inequality is an invariant guard, not a reachable case: adding
		// commits always changes the content, so it always changes the hash.
		// It is here because the cost of it ever being wrong is deleting the
		// snapshot we just wrote.
		if newPath != info.path {
			if rmErr := os.Remove(info.path); rmErr != nil && !os.IsNotExist(rmErr) {
				logger.Warn("remove superseded PR snapshot failed", "pr", pr.Number, "path", info.path, "error", rmErr)
			}
		}

		backfilled++
		logger.Info("backfilled PR commits", "pr", pr.Number, "commits", len(commits))
	}

	return backfilled, nil
}

func fetchPRCommits(ctx context.Context, fetcher GitHubFetcher, owner, repo string, number int, logger *slog.Logger) []PRCommit {
	fetched, err := fetcher.ListPRCommits(ctx, owner, repo, number)
	if err != nil {
		logger.Warn("fetch PR commits failed", "pr", number, "error", err)
		return nil
	}
	commits := make([]PRCommit, len(fetched))
	for i, c := range fetched {
		commits[i] = PRCommit(c)
	}
	return commits
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
