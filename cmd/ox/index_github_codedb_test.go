package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/codedb"
	"github.com/sageox/ox/internal/codedb/store"
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

// failingCommentsFetcher is stubGitHubFetcher with every comment fetch failing.
type failingCommentsFetcher struct{ stubGitHubFetcher }

func (failingCommentsFetcher) ListPRComments(context.Context, string, string, int) ([]ledger.FetchedComment, error) {
	return nil, errors.New("502 bad gateway")
}

func (failingCommentsFetcher) ListIssueComments(context.Context, string, string, int) ([]ledger.FetchedComment, error) {
	return nil, errors.New("502 bad gateway")
}

// TestIndexGitHub_PrefersDaemonIndexPass verifies the in-process fallback does
// not run when the daemon indexed: the daemon owns the shared CodeDB then, and
// a second writer would contend with it.
func TestIndexGitHub_PrefersDaemonIndexPass(t *testing.T) {
	prev := indexGitHubViaDaemon
	indexGitHubViaDaemon = func() error { return nil }
	t.Cleanup(func() { indexGitHubViaDaemon = prev })

	codeDBDir := t.TempDir()
	require.NoError(t, indexGitHubCodeDB(context.Background(), githubIndexRun{LedgerPath: t.TempDir(), CodeDBDir: codeDBDir}))
	require.NoFileExists(t, filepath.Join(codeDBDir, store.MetadataDBFile), "fallback wrote CodeDB although the daemon indexed")
}

// TestIndexGitHub_FallbackFailuresAreReported covers the fallback's refusals:
// no usable directory (the shared index is not built yet) and a directory that
// cannot be opened. Each must be an error the command reports, not a silent
// "indexed".
func TestIndexGitHub_FallbackFailuresAreReported(t *testing.T) {
	prev := indexGitHubViaDaemon
	indexGitHubViaDaemon = func() error { return errors.New("daemon unavailable") }
	t.Cleanup(func() { indexGitHubViaDaemon = prev })

	notADir := filepath.Join(t.TempDir(), "codedb")
	require.NoError(t, os.WriteFile(notADir, []byte("x"), 0o600))

	cases := []struct {
		name, dir, want string
	}{
		{name: "shared index not built", dir: "", want: "ox index code"},
		{name: "unopenable directory", dir: notADir, want: "open codedb"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := indexGitHubCodeDB(context.Background(), githubIndexRun{LedgerPath: t.TempDir(), CodeDBDir: tc.dir})
			require.ErrorContains(t, err, tc.want)
		})
	}
}

// TestIndexGitHub_WarnsAboutSkippedItemsAndUnindexedData verifies a run that
// could not fetch comments, and could not index, still succeeds (the rest of
// the sync is saved) but tells the user both things on stderr.
func TestIndexGitHub_WarnsAboutSkippedItemsAndUnindexedData(t *testing.T) {
	prev := indexGitHubViaDaemon
	indexGitHubViaDaemon = func() error { return errors.New("daemon unavailable") }
	t.Cleanup(func() { indexGitHubViaDaemon = prev })

	now := time.Now().UTC().Truncate(time.Second)
	stderr := captureStderr(t, func() {
		require.NoError(t, syncGitHubIndex(context.Background(), githubIndexRun{
			Fetcher:    failingCommentsFetcher{stubGitHubFetcher{at: now.Add(-time.Hour)}},
			LedgerPath: t.TempDir(),
			Owner:      "o",
			Repo:       "r",
			MaxDays:    30,
			SyncPRs:    true,
			SyncIssues: true,
			NoPush:     true,
		}))
	})
	require.Contains(t, stderr, "2 item(s) skipped because their comments could not be fetched")
	require.Contains(t, stderr, "GitHub data synced but not indexed for search")
}

// TestGitHubFallbackCodeDBDir_IsTheDaemonsDirectory verifies the fallback
// indexes where the daemon does when no Ledger index would be shadowed.
func TestGitHubFallbackCodeDBDir_IsTheDaemonsDirectory(t *testing.T) {
	root := t.TempDir()
	require.Equal(t, resolveCodeDBDir(root), githubFallbackCodeDBDir(root))
}
