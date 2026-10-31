package index

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/sageox/ox/internal/codedb/store"
	"github.com/sageox/ox/internal/githubmirror"
	"github.com/stretchr/testify/require"
)

func openLocalGitHubStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.OpenSQLOnly(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	return s
}

// Destination changes must retain searchable comments and PR-to-commit links.
func TestGitHubDestinationItemsKeepContentAndRelations(t *testing.T) {
	ctx := context.Background()
	s := openLocalGitHubStore(t)
	at := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	line := 42
	pr := githubmirror.Item{
		Kind: githubmirror.KindPullRequest, Number: 7, State: githubmirror.StateMerged,
		Title: "scanned PR", Body: "scanned description", Author: githubmirror.Author{Login: "fixture-author"},
		URL: "https://github.com/acme/api/pull/7", Labels: []string{"migration", "needs,review"},
		CreatedAt: at.Add(-time.Hour), UpdatedAt: at, MergedAt: &at, ClosedAt: &at,
		MergeCommit: "initial-merge", Commits: []githubmirror.Commit{{SHA: "initial-commit"}},
		Comments: []githubmirror.Comment{{Author: githubmirror.Author{Login: "fixture-reviewer"}, Body: "keep review", Path: "main.go", Line: &line, CreatedAt: at}},
	}
	issue := githubmirror.Item{
		Kind: githubmirror.KindIssue, Number: 8, State: githubmirror.StateClosed,
		Title: "scanned issue", Body: "issue description", Author: githubmirror.Author{Login: "fixture-author"},
		URL: "https://github.com/acme/api/issues/8", Labels: []string{"migration"},
		CreatedAt: at.Add(-time.Hour), UpdatedAt: at, ClosedAt: &at,
		Comments: []githubmirror.Comment{{Author: githubmirror.Author{Login: "fixture-reviewer"}, Body: "keep issue comment", CreatedAt: at}},
	}
	n, err := IndexRepoGitHubItems(ctx, s, "repo_selected:digest", []githubmirror.Item{pr, issue})
	require.NoError(t, err)
	require.Equal(t, 2, n)
	before := readPRRow(t, s, 7)
	require.Equal(t, "scanned PR", before.title)
	require.Equal(t, "scanned description", before.body)
	require.Equal(t, "fixture-author", before.author)
	require.Equal(t, at.Unix(), before.merged.Int64)
	require.Equal(t, at.Add(-time.Hour).Unix(), before.created.Int64)
	require.Equal(t, at.Unix(), before.updated.Int64)
	require.JSONEq(t, `["migration","needs,review"]`, before.labels.String)
	require.Equal(t, "github-repo:repo_selected:digest", before.source.String)
	var path string
	var gotLine, closed int64
	require.NoError(t, s.QueryRow(`SELECT c.path, c.line, p.closed_at FROM pr_comments c JOIN pull_requests p ON p.id=c.pr_id WHERE p.number=7`).Scan(&path, &gotLine, &closed))
	require.Equal(t, "main.go", path)
	require.EqualValues(t, line, gotLine)
	require.Equal(t, at.Unix(), closed)

	localPR := pr
	localPR.Title, localPR.Body = "unscanned replacement", "do not replace scanned content"
	localPR.URL = "https://github.com/ACME/API/pull/7"
	localPR.Comments = nil
	localPR.MergeCommit, localPR.Commits = "actual-merge", []githubmirror.Commit{{SHA: "commit-one"}, {SHA: "commit-two"}}
	localIssue := issue
	localIssue.Title, localIssue.Body, localIssue.Comments = "unscanned issue", "do not replace issue", nil
	n, err = IndexLocalGitHubItems(ctx, s, []githubmirror.Item{localPR, localIssue})
	require.NoError(t, err)
	require.Equal(t, 2, n)
	after := readPRRow(t, s, 7)
	before.merge = sql.NullString{String: "actual-merge", Valid: true}
	require.Equal(t, before, after, "a local fetch may enrich the same URL's commit metadata only")
	require.Equal(t, []string{"commit-one", "commit-two"}, prCommitSHAs(t, s, 7))
	require.Equal(t, []string{"fixture-reviewer: keep review"}, prCommentLines(t, s, 7))
	title, body, state, labels, source := readIssueRow(t, s, 8)
	require.Equal(t, "scanned issue", title)
	require.Equal(t, "issue description", body)
	require.Equal(t, githubmirror.StateClosed, state)
	require.JSONEq(t, `["migration"]`, labels.String)
	require.Equal(t, "github-repo:repo_selected:digest", source.String)
	require.Equal(t, []string{"fixture-reviewer: keep issue comment"}, issueCommentLines(t, s, 8))

	pr.Title, pr.MergeCommit, pr.Commits = "new scanned content", "", nil
	n, err = IndexRepoGitHubItems(ctx, s, "repo_selected:next-digest", []githubmirror.Item{pr})
	require.NoError(t, err)
	require.Equal(t, 1, n)
	require.Equal(t, "new scanned content", readPRRow(t, s, 7).title)
	require.Equal(t, "actual-merge", readPRRow(t, s, 7).merge.String)
	require.Equal(t, []string{"commit-one", "commit-two"}, prCommitSHAs(t, s, 7), "later scanned content must retain local commit enrichment")
}

// An interrupted batch reports only committed items and never indexes the tail.
func TestGitHubDestinationItemsReportPartialBatches(t *testing.T) {
	for _, source := range []string{"local", "repo"} {
		t.Run(source, func(t *testing.T) {
			s := openLocalGitHubStore(t)
			items := []githubmirror.Item{
				{Kind: githubmirror.KindIssue, Number: 1, Title: "committed", State: "open"},
				{Kind: "unsupported", Number: 2},
				{Kind: githubmirror.KindPullRequest, Number: 3, Title: "must not run", State: "open"},
			}
			var n int
			var err error
			if source == "local" {
				n, err = IndexLocalGitHubItems(context.Background(), s, items)
			} else {
				n, err = IndexRepoGitHubItems(context.Background(), s, "repo_selected", items)
			}
			require.ErrorContains(t, err, `unknown GitHub item kind "unsupported"`)
			require.Equal(t, 1, n)
			title, _, _, _, _ := readIssueRow(t, s, 1)
			require.Equal(t, "committed", title)
			require.Zero(t, countRows(t, s, "SELECT count(*) FROM pull_requests"))
		})
	}
}

// Failed enrichment must roll back both the merge SHA and the replaced commit list.
func TestGitHubLocalMetadataFailureRollsBack(t *testing.T) {
	for _, tc := range []struct{ name, trigger string }{
		{"merge update", `CREATE TRIGGER reject_metadata BEFORE UPDATE OF merge_commit ON pull_requests BEGIN SELECT RAISE(ABORT, 'fixture metadata failure'); END`},
		{"commit delete", `CREATE TRIGGER reject_metadata BEFORE DELETE ON pr_commits BEGIN SELECT RAISE(ABORT, 'fixture metadata failure'); END`},
		{"second commit insert", `CREATE TRIGGER reject_metadata BEFORE INSERT ON pr_commits WHEN NEW.sha='reject-commit' BEGIN SELECT RAISE(ABORT, 'fixture metadata failure'); END`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := openLocalGitHubStore(t)
			ctx := context.Background()
			old := githubmirror.Item{Kind: githubmirror.KindPullRequest, Number: 7, Title: "scanned content", State: "merged", URL: "https://github.com/acme/api/pull/7", MergeCommit: "old-merge", Commits: []githubmirror.Commit{{SHA: "old-one"}, {SHA: "old-two"}}}
			_, err := IndexRepoGitHubItems(ctx, s, "repo_selected", []githubmirror.Item{old})
			require.NoError(t, err)
			before := readPRRow(t, s, 7)
			_, err = s.Exec(tc.trigger)
			require.NoError(t, err)
			incoming := old
			incoming.Title, incoming.MergeCommit = "unscanned content", "new-merge"
			incoming.Commits = []githubmirror.Commit{{SHA: "new-first"}, {SHA: "reject-commit"}}
			n, err := IndexLocalGitHubItems(ctx, s, []githubmirror.Item{{Kind: githubmirror.KindIssue, Number: 8, Title: "first item", State: "open"}, incoming})
			require.ErrorContains(t, err, "fixture metadata failure")
			require.Equal(t, 1, n, "the failed PR is not counted; the preceding issue committed")
			require.Equal(t, before, readPRRow(t, s, 7))
			require.Equal(t, []string{"old-one", "old-two"}, prCommitSHAs(t, s, 7))
			title, _, _, _, _ := readIssueRow(t, s, 8)
			require.Equal(t, "first item", title)
		})
	}
}

// Full snapshot replacement is atomic too, including its comments and commit relations.
func TestGitHubDestinationReplacementFailureKeepsPreviousSnapshot(t *testing.T) {
	for _, kind := range []string{githubmirror.KindPullRequest, githubmirror.KindIssue} {
		t.Run(kind, func(t *testing.T) {
			s := openLocalGitHubStore(t)
			ctx := context.Background()
			old := githubmirror.Item{Kind: kind, Number: 7, Title: "previous snapshot", State: "open", URL: "https://github.com/acme/api/pull/7", MergeCommit: "previous-merge", Commits: []githubmirror.Commit{{SHA: "previous-commit"}}, Comments: []githubmirror.Comment{{Body: "previous comment"}}}
			_, err := IndexLocalGitHubItems(ctx, s, []githubmirror.Item{old})
			require.NoError(t, err)
			table := "pr_comments"
			if kind == githubmirror.KindIssue {
				table = "issue_comments"
			}
			_, err = s.Exec("CREATE TRIGGER reject_comment BEFORE INSERT ON " + table + " BEGIN SELECT RAISE(ABORT, 'fixture comment failure'); END")
			require.NoError(t, err)
			incoming := old
			incoming.Title, incoming.Comments = "new snapshot", []githubmirror.Comment{{Body: "new comment"}}
			n, err := IndexRepoGitHubItems(ctx, s, "repo_selected", []githubmirror.Item{incoming})
			require.ErrorContains(t, err, "fixture comment failure")
			require.Zero(t, n)
			if kind == githubmirror.KindPullRequest {
				require.Equal(t, "previous snapshot", readPRRow(t, s, 7).title)
				require.Equal(t, "github-local", readPRRow(t, s, 7).source.String)
				require.Equal(t, []string{": previous comment"}, prCommentLines(t, s, 7))
				require.Equal(t, []string{"previous-commit"}, prCommitSHAs(t, s, 7))
			} else {
				title, _, _, _, source := readIssueRow(t, s, 7)
				require.Equal(t, "previous snapshot", title)
				require.Equal(t, "github-local", source.String)
				require.Equal(t, []string{": previous comment"}, issueCommentLines(t, s, 7))
			}
		})
	}
}

func TestGitHubDestinationUnavailableStoreIsNotEmptySuccess(t *testing.T) {
	for _, kind := range []string{githubmirror.KindPullRequest, githubmirror.KindIssue} {
		t.Run(kind, func(t *testing.T) {
			s := openLocalGitHubStore(t)
			require.NoError(t, s.Close())
			n, err := IndexLocalGitHubItems(context.Background(), s, []githubmirror.Item{{Kind: kind, Number: 7}})
			require.ErrorContains(t, err, "database is closed")
			require.Zero(t, n)
		})
	}
}

// Shutdown cannot modify existing content or report an unstarted batch as indexed.
func TestGitHubDestinationCancellationPreservesSnapshot(t *testing.T) {
	s := openLocalGitHubStore(t)
	old := githubmirror.Item{Kind: githubmirror.KindPullRequest, Number: 7, Title: "previous", State: "open"}
	_, err := IndexRepoGitHubItems(context.Background(), s, "repo_selected", []githubmirror.Item{old})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	n, err := IndexLocalGitHubItems(ctx, s, []githubmirror.Item{{Kind: githubmirror.KindPullRequest, Number: 7, Title: "replacement"}})
	require.ErrorIs(t, err, context.Canceled)
	require.Zero(t, n)
	require.Equal(t, "previous", readPRRow(t, s, 7).title)
	var id int64
	require.NoError(t, s.QueryRow("SELECT id FROM pull_requests WHERE number=7").Scan(&id))
	require.ErrorIs(t, addLocalCommitMetadata(ctx, s, id, githubmirror.Item{MergeCommit: "uncommitted"}), context.Canceled)
	require.False(t, readPRRow(t, s, 7).merge.Valid)
}

// A missing commit table must fail before replacing the row, rather than dropping old links.
func TestGitHubDestinationCannotReadMetadataKeepsSnapshot(t *testing.T) {
	s := openLocalGitHubStore(t)
	ctx := context.Background()
	old := githubmirror.Item{Kind: githubmirror.KindPullRequest, Number: 7, Title: "previous", State: "merged", URL: "https://github.com/acme/api/pull/7", MergeCommit: "merge", Commits: []githubmirror.Commit{{SHA: "commit"}}}
	_, err := IndexLocalGitHubItems(ctx, s, []githubmirror.Item{old})
	require.NoError(t, err)
	before := readPRRow(t, s, 7)
	_, err = s.Exec("ALTER TABLE pr_commits RENAME TO unavailable_pr_commits")
	require.NoError(t, err)
	incoming := old
	incoming.Title, incoming.MergeCommit, incoming.Commits = "replacement", "", nil
	n, err := IndexRepoGitHubItems(ctx, s, "repo_selected", []githubmirror.Item{incoming})
	require.ErrorContains(t, err, "pr_commits")
	require.Zero(t, n)
	require.Equal(t, before, readPRRow(t, s, 7))
	_, err = s.Exec("ALTER TABLE unavailable_pr_commits RENAME TO pr_commits")
	require.NoError(t, err)
	require.Equal(t, []string{"commit"}, prCommitSHAs(t, s, 7))
}

// Reuse is repo-scoped; merged PRs without both commit relations and a merge SHA must be refetched.
func TestGitHubLocalReuseRequiresCompleteMatchingRows(t *testing.T) {
	s := openLocalGitHubStore(t)
	at := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	items := []githubmirror.Item{
		{Kind: githubmirror.KindPullRequest, Number: 1, State: "merged", URL: "https://github.com/ACME/API/pull/1/", UpdatedAt: at, MergeCommit: "merge", Commits: []githubmirror.Commit{{SHA: "commit"}}},
		{Kind: githubmirror.KindPullRequest, Number: 2, State: "merged", URL: "https://github.com/acme/api/pull/2", UpdatedAt: at, Commits: []githubmirror.Commit{{SHA: "no-merge-sha"}}},
		{Kind: githubmirror.KindPullRequest, Number: 3, State: "merged", URL: "https://github.com/acme/api/pull/3", UpdatedAt: at, MergeCommit: "no-commit-list"},
		{Kind: githubmirror.KindPullRequest, Number: 4, State: "open", URL: "https://github.com/acme/api/pull/4", UpdatedAt: at},
		{Kind: githubmirror.KindPullRequest, Number: 5, State: "open", URL: "https://github.com/fork/api/pull/5", UpdatedAt: at},
		{Kind: githubmirror.KindIssue, Number: 6, State: "open", URL: "https://github.com/ACME/API/issues/6/", UpdatedAt: at},
		{Kind: githubmirror.KindIssue, Number: 7, State: "open", URL: "https://github.com/acme/api/pull/7", UpdatedAt: at},
		{Kind: githubmirror.KindIssue, Number: 8, State: "open", URL: "https://github.com/acme/api/issues/8"},
		{Kind: githubmirror.KindIssue, Number: 9, State: "open", UpdatedAt: at},
	}
	_, err := IndexLocalGitHubItems(context.Background(), s, items)
	require.NoError(t, err)
	known, err := KnownLocalGitHubItems(context.Background(), s, "acme", "api")
	require.NoError(t, err)
	require.Equal(t, map[string]githubmirror.ItemState{
		githubmirror.SourceKey("acme", "api", githubmirror.KindPullRequest, 1): {UpdatedAt: at, CommitMetadata: true},
		githubmirror.SourceKey("acme", "api", githubmirror.KindPullRequest, 4): {UpdatedAt: at, CommitMetadata: true},
		githubmirror.SourceKey("acme", "api", githubmirror.KindIssue, 6):       {UpdatedAt: at},
	}, known)
}

// Corrupt or inaccessible cache rows cannot tell the fetcher that it has a complete snapshot.
func TestGitHubLocalReuseReadFailuresReturnNoPartialKnowledge(t *testing.T) {
	for _, failure := range []string{"closed", "canceled", "malformed PR timestamp", "malformed issue timestamp", "missing issue table"} {
		t.Run(failure, func(t *testing.T) {
			s := openLocalGitHubStore(t)
			ctx := context.Background()
			at := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
			_, err := IndexLocalGitHubItems(ctx, s, []githubmirror.Item{
				{Kind: githubmirror.KindPullRequest, Number: 1, Title: "valid first row", State: "open", URL: "https://github.com/acme/api/pull/1", UpdatedAt: at},
				{Kind: githubmirror.KindPullRequest, Number: 2, Title: "second PR", State: "open", URL: "https://github.com/acme/api/pull/2", UpdatedAt: at},
				{Kind: githubmirror.KindIssue, Number: 3, Title: "issue", State: "open", URL: "https://github.com/acme/api/issues/3", UpdatedAt: at},
			})
			require.NoError(t, err)
			switch failure {
			case "closed":
				require.NoError(t, s.Close())
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "malformed PR timestamp":
				_, err = s.Exec("UPDATE pull_requests SET updated_at='invalid-time' WHERE number=2")
			case "malformed issue timestamp":
				_, err = s.Exec("UPDATE issues SET updated_at='invalid-time' WHERE number=3")
			case "missing issue table":
				_, err = s.Exec("DROP TABLE issues")
			}
			require.NoError(t, err)
			known, err := KnownLocalGitHubItems(ctx, s, "acme", "api")
			require.Error(t, err)
			if failure == "canceled" {
				require.ErrorIs(t, err, context.Canceled)
			}
			require.Nil(t, known, "partial cache knowledge must not suppress a required refetch")
		})
	}
}

func TestGitHubLocalMissingPRIdentityReturnsError(t *testing.T) {
	s := openLocalGitHubStore(t)
	same, err := sameIndexedPR(s, 999, "https://github.com/acme/api/pull/7")
	require.ErrorIs(t, err, sql.ErrNoRows)
	require.False(t, same, "missing row cannot establish identity")
}

// Failed identity lookup cannot authorize enriching or replacing a same-number PR.
func TestGitHubDestinationCannotReadIdentityKeepsSnapshot(t *testing.T) {
	for _, source := range []string{"local", "repo"} {
		t.Run(source, func(t *testing.T) {
			s := openLocalGitHubStore(t)
			ctx := context.Background()
			old := githubmirror.Item{Kind: githubmirror.KindPullRequest, Number: 7, Title: "previous", State: "merged", URL: "https://github.com/acme/api/pull/7", MergeCommit: "old-merge", Commits: []githubmirror.Commit{{SHA: "old-commit"}}}
			_, err := IndexRepoGitHubItems(ctx, s, "repo_selected", []githubmirror.Item{old})
			require.NoError(t, err)
			before := readPRRow(t, s, 7)
			_, err = s.Exec("ALTER TABLE pull_requests RENAME COLUMN url TO unavailable_url")
			require.NoError(t, err)
			incoming := old
			incoming.Title, incoming.MergeCommit, incoming.Commits = "replacement", "new-merge", []githubmirror.Commit{{SHA: "new-commit"}}
			items := []githubmirror.Item{{Kind: githubmirror.KindIssue, Number: 8, Title: "first item", State: "open"}, incoming}
			var n int
			if source == "local" {
				n, err = IndexLocalGitHubItems(ctx, s, items)
			} else {
				n, err = IndexRepoGitHubItems(ctx, s, "repo_selected", items)
			}
			require.ErrorContains(t, err, "url")
			require.Equal(t, 1, n)
			_, err = s.Exec("ALTER TABLE pull_requests RENAME COLUMN unavailable_url TO url")
			require.NoError(t, err)
			require.Equal(t, before, readPRRow(t, s, 7))
			require.Equal(t, []string{"old-commit"}, prCommitSHAs(t, s, 7))
		})
	}
}
