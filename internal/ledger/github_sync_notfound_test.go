package ledger

import (
	"context"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type failingPRCommitsFetcher struct {
	mockFetcher
	calls   int
	failure error
}

func (f *failingPRCommitsFetcher) ListPRCommits(ctx context.Context, owner, repo string, number int) ([]FetchedPRCommit, error) {
	f.calls++
	if f.failure != nil {
		return nil, f.failure
	}
	return f.mockFetcher.ListPRCommits(ctx, owner, repo, number)
}

// A transient API failure must be retried, while a wrapped 404 is suppressed
// until restart. Neither failure may erase the existing PR snapshot.
func TestBackfillPRCommits_NotFoundAndTransientFailuresHaveDifferentLifetimes(t *testing.T) {
	ResetPRCommitsNotFound()
	t.Cleanup(ResetPRCommitsNotFound)
	now := time.Now().UTC().Truncate(time.Second)
	ledgerPath := t.TempDir()
	pr := &PRFile{Number: 700, Title: "saved PR", State: "merged", Author: "person-a", CreatedAt: now, UpdatedAt: now, MergedAt: &now}
	require.NoError(t, WriteGitHubPR(ledgerPath, pr))
	fetcher := &failingPRCommitsFetcher{failure: fmt.Errorf("API temporarily unavailable")}
	backfill := func(owner, repo string) int {
		t.Helper()
		count, err := BackfillPRCommits(context.Background(), fetcher, ledgerPath, owner, repo, slog.Default())
		require.NoError(t, err)
		return count
	}
	require.Zero(t, backfill("org", "repo"))
	require.Zero(t, backfill("org", "repo"))
	require.Equal(t, 2, fetcher.calls, "transient failure must remain retryable")
	fetcher.failure = fmt.Errorf("PR commits: %w", ErrGitHubNotFound)
	require.Zero(t, backfill("org", "repo"))
	require.Zero(t, backfill("org", "repo"))
	require.Equal(t, 3, fetcher.calls, "404 must not be requested on each sync cycle")
	saved, err := ReadGitHubPR(ledgerPath, pr.Number, now)
	require.NoError(t, err)
	require.Equal(t, pr.Title, saved.Title)
	require.Empty(t, saved.Commits)
	require.Zero(t, backfill("other-org", "repo"))
	require.Zero(t, backfill("org", "other-repo"))
	require.Equal(t, 5, fetcher.calls, "a missing PR in one repository must not suppress another")
	ResetPRCommitsNotFound()
	fetcher.failure = nil
	fetcher.prCommits = map[int][]FetchedPRCommit{700: {{SHA: "abc123", Author: "person-a", Date: now, Msg: "restored commit"}}}
	require.Equal(t, 1, backfill("org", "repo"), "a restarted process must recover a resource that reappears")
	require.Equal(t, 6, fetcher.calls)
	saved, err = ReadGitHubPR(ledgerPath, pr.Number, now)
	require.NoError(t, err)
	require.Len(t, saved.Commits, 1)
	require.Equal(t, "abc123", saved.Commits[0].SHA)
}
