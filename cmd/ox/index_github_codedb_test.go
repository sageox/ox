package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sageox/ox/internal/codedb"
	"github.com/sageox/ox/internal/ledger"
	"github.com/stretchr/testify/require"
)

// stubGitHubFetcher serves one PR and one issue, each with a comment.
type stubGitHubFetcher struct{ at time.Time }

func (f stubGitHubFetcher) ListPullRequests(context.Context, string, string, ledger.ListPRsOptions) ([]ledger.FetchedPR, *ledger.FetchRateLimit, error) {
	return []ledger.FetchedPR{{Number: 41, Title: "Speed up EPUB pagination", State: "open", Author: "eve", CreatedAt: f.at, UpdatedAt: f.at}}, nil, nil
}

func (f stubGitHubFetcher) ListIssues(context.Context, string, string, ledger.ListIssuesOptions) ([]ledger.FetchedIssue, *ledger.FetchRateLimit, error) {
	return []ledger.FetchedIssue{{Number: 42, Title: "Crash when opening PDF", State: "open", Author: "dana", CreatedAt: f.at, UpdatedAt: f.at}}, nil, nil
}

func (f stubGitHubFetcher) ListPRComments(context.Context, string, string, int) ([]ledger.FetchedComment, error) {
	return nil, nil
}

func (f stubGitHubFetcher) ListIssueComments(context.Context, string, string, int) ([]ledger.FetchedComment, error) {
	return []ledger.FetchedComment{{Author: "carol", Body: "same here", CreatedAt: f.at}}, nil
}

func (f stubGitHubFetcher) ListPRCommits(context.Context, string, string, int) ([]ledger.FetchedPRCommit, error) {
	return nil, nil
}

// TestIndexGitHub_IndexesCodeDBWithoutDaemon drives the `ox index github` run
// with the daemon unavailable and asks CodeDB for the synced PR and issue.
//
// Failure prevented: the CLI wrote PRs and issues to the Ledger but never
// indexed them, so `ox code search type:pr` stayed stale until a daemon
// happened to run — despite the command's help promising CodeDB indexing. The
// daemon indexes after every sync; the CLI must too, in-process when it has
// no daemon to ask.
func TestIndexGitHub_IndexesCodeDBWithoutDaemon(t *testing.T) {
	prev := indexGitHubViaDaemon
	indexGitHubViaDaemon = func() error { return errors.New("daemon unavailable") }
	t.Cleanup(func() { indexGitHubViaDaemon = prev })

	ledgerPath := t.TempDir()
	codeDBDir := t.TempDir()
	now := time.Now().UTC().Truncate(time.Second)

	err := syncGitHubIndex(context.Background(), githubIndexRun{
		Fetcher:    stubGitHubFetcher{at: now.Add(-time.Hour)},
		LedgerPath: ledgerPath,
		CodeDBDir:  codeDBDir,
		Owner:      "o",
		Repo:       "r",
		MaxDays:    30,
		SyncPRs:    true,
		SyncIssues: true,
		NoPush:     true,
	})
	require.NoError(t, err)

	db, err := codedb.Open(codeDBDir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	var prTitle, issueTitle string
	require.NoError(t, db.Store().QueryRow("SELECT title FROM pull_requests WHERE number = 41").Scan(&prTitle),
		"synced PR 41 is not in CodeDB")
	require.Equal(t, "Speed up EPUB pagination", prTitle)
	require.NoError(t, db.Store().QueryRow("SELECT title FROM issues WHERE number = 42").Scan(&issueTitle),
		"synced issue 42 is not in CodeDB")
	require.Equal(t, "Crash when opening PDF", issueTitle)
}
