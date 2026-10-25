package ledger

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"testing"
	"time"
)

// windowFetcher is a GitHubFetcher that behaves like the real API where it
// matters to the sync cursor: listings honor Since (updated_at >= since), and
// comment fetches can be made to fail for chosen item numbers.
type windowFetcher struct {
	prs      []FetchedPR
	issues   []FetchedIssue
	comments map[int][]FetchedComment

	failReviewComments map[int]bool // ListPRComments fails for these numbers
	failIssueComments  map[int]bool // ListIssueComments fails for these numbers

	prSinces    []time.Time
	issueSinces []time.Time
}

var errCommentsUnavailable = errors.New("502 bad gateway")

func (f *windowFetcher) ListPullRequests(_ context.Context, _, _ string, opts ListPRsOptions) ([]FetchedPR, *FetchRateLimit, error) {
	f.prSinces = append(f.prSinces, opts.Since)
	var out []FetchedPR
	for _, pr := range f.prs {
		if !pr.UpdatedAt.Before(opts.Since) {
			out = append(out, pr)
		}
	}
	return out, nil, nil
}

func (f *windowFetcher) ListIssues(_ context.Context, _, _ string, opts ListIssuesOptions) ([]FetchedIssue, *FetchRateLimit, error) {
	f.issueSinces = append(f.issueSinces, opts.Since)
	var out []FetchedIssue
	for _, is := range f.issues {
		if !is.UpdatedAt.Before(opts.Since) {
			out = append(out, is)
		}
	}
	return out, nil, nil
}

func (f *windowFetcher) ListPRComments(_ context.Context, _, _ string, number int) ([]FetchedComment, error) {
	if f.failReviewComments[number] {
		return nil, errCommentsUnavailable
	}
	return nil, nil
}

func (f *windowFetcher) ListIssueComments(_ context.Context, _, _ string, number int) ([]FetchedComment, error) {
	if f.failIssueComments[number] {
		return nil, errCommentsUnavailable
	}
	return f.comments[number], nil
}

func (f *windowFetcher) ListPRCommits(context.Context, string, string, int) ([]FetchedPRCommit, error) {
	return nil, nil
}

// prSnapshots returns every snapshot on disk for PR number.
func prSnapshots(t *testing.T, ledgerPath string, number int) []PRFile {
	t.Helper()
	return readSnapshots[PRFile](t, ledgerPath, "pr", number)
}

func issueSnapshots(t *testing.T, ledgerPath string, number int) []IssueFile {
	t.Helper()
	return readSnapshots[IssueFile](t, ledgerPath, "issue", number)
}

func readSnapshots[T any](t *testing.T, ledgerPath, dataType string, number int) []T {
	t.Helper()
	paths, err := ListGitHubDataFiles(ledgerPath, dataType)
	if err != nil {
		t.Fatal(err)
	}
	var out []T
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		var stub struct {
			Number int `json:"number"`
		}
		if err := json.Unmarshal(data, &stub); err != nil {
			t.Fatal(err)
		}
		if stub.Number != number {
			continue
		}
		var v T
		if err := json.Unmarshal(data, &v); err != nil {
			t.Fatal(err)
		}
		out = append(out, v)
	}
	return out
}

// --- A. A failed comment fetch never marks an item synced ---
//
// Failure prevented: a transient 5xx/rate-limit on the comments endpoint wrote
// a comment-less snapshot, recorded the item as known, and advanced the cursor
// past it. The item was then skipped on every later sync until someone touched
// it on GitHub again, so the issue/PR reader showed it without its discussion.

// TestSyncPRs_CommentFetchFailureRetriesNextSync covers both comment endpoints
// a PR depends on, for a PR first seen in the failing sync and for a known PR
// whose update failed.
func TestSyncPRs_CommentFetchFailureRetriesNextSync(t *testing.T) {
	cases := []struct {
		name           string
		failReview     bool
		failIssue      bool
		previouslySeen bool
	}{
		{name: "new PR, review comments fail", failReview: true},
		{name: "new PR, conversation comments fail", failIssue: true},
		{name: "known PR updated, conversation comments fail", failIssue: true, previouslySeen: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ledgerPath := t.TempDir()
			logger := slog.Default()
			now := time.Now().UTC().Truncate(time.Second)
			created := now.Add(-48 * time.Hour)
			// after the first sync's cursor, as a real update would be
			updated := now.Add(time.Minute)
			comment := FetchedComment{Author: "carol", Body: "this breaks on ARM", CreatedAt: updated}

			f := &windowFetcher{comments: map[int][]FetchedComment{7: {comment}}}

			if tc.previouslySeen {
				// an earlier, fully-synced version of the PR
				f.prs = []FetchedPR{{Number: 7, Title: "v1", State: "open", CreatedAt: created, UpdatedAt: created}}
				f.comments[7] = nil
				if _, err := SyncPRs(context.Background(), f, ledgerPath, "o", "r", 30, logger); err != nil {
					t.Fatal(err)
				}
				f.comments[7] = []FetchedComment{comment}
			}

			f.prs = []FetchedPR{{Number: 7, Title: "v2", State: "open", CreatedAt: created, UpdatedAt: updated}}
			f.failReviewComments = map[int]bool{7: tc.failReview}
			f.failIssueComments = map[int]bool{7: tc.failIssue}
			if _, err := SyncPRs(context.Background(), f, ledgerPath, "o", "r", 30, logger); err != nil {
				t.Fatal(err)
			}
			for _, snap := range prSnapshots(t, ledgerPath, 7) {
				if snap.UpdatedAt.Equal(updated) {
					t.Fatalf("failed sync wrote a snapshot of the update without its comments: %+v", snap)
				}
			}

			// GitHub recovers; nothing changed upstream since.
			f.failReviewComments, f.failIssueComments = nil, nil
			if _, err := SyncPRs(context.Background(), f, ledgerPath, "o", "r", 30, logger); err != nil {
				t.Fatal(err)
			}

			pr, err := ReadGitHubPR(ledgerPath, 7, created)
			if err != nil {
				t.Fatalf("PR 7 never synced after GitHub recovered: %v", err)
			}
			if pr.Title != "v2" || len(pr.Comments) != 1 || pr.Comments[0].Body != comment.Body {
				t.Fatalf("PR 7 after recovery = title %q, comments %+v; want v2 with carol's comment", pr.Title, pr.Comments)
			}
		})
	}
}

// TestSyncIssues_CommentFetchFailureRetriesNextSync is the issue twin.
func TestSyncIssues_CommentFetchFailureRetriesNextSync(t *testing.T) {
	ledgerPath := t.TempDir()
	logger := slog.Default()
	now := time.Now().UTC().Truncate(time.Second)
	created := now.Add(-48 * time.Hour)
	updated := now.Add(-time.Hour)
	comment := FetchedComment{Author: "dana", Body: "repro attached", CreatedAt: updated}

	f := &windowFetcher{
		issues:            []FetchedIssue{{Number: 9, Title: "crash on open", State: "open", CreatedAt: created, UpdatedAt: updated}},
		comments:          map[int][]FetchedComment{9: {comment}},
		failIssueComments: map[int]bool{9: true},
	}
	if _, err := SyncIssues(context.Background(), f, ledgerPath, "o", "r", 30, logger); err != nil {
		t.Fatal(err)
	}
	if snaps := issueSnapshots(t, ledgerPath, 9); len(snaps) != 0 {
		t.Fatalf("failed sync wrote %d comment-less snapshot(s) of issue 9", len(snaps))
	}

	f.failIssueComments = nil
	if _, err := SyncIssues(context.Background(), f, ledgerPath, "o", "r", 30, logger); err != nil {
		t.Fatal(err)
	}
	issue := readIssueFile(t, ledgerPath, 9, created)
	if len(issue.Comments) != 1 || issue.Comments[0].Body != comment.Body {
		t.Fatalf("issue 9 after recovery has comments %+v; want dana's comment", issue.Comments)
	}
}

// --- B. A wider --days window applies after the first sync ---
//
// Failure prevented: once a cursor existed, since = max(now-days, last_sync),
// so `ox index github --days 90` after a 30-day sync fetched nothing older
// than the last sync. The only way to widen the window was --full, which
// throws away the sync state and re-fetches every item's comments.

func TestSyncPRs_WiderDaysAfterFirstSyncBackfills(t *testing.T) {
	ledgerPath := t.TempDir()
	logger := slog.Default()
	now := time.Now().UTC().Truncate(time.Second)
	recent := now.Add(-2 * 24 * time.Hour)
	old := now.Add(-60 * 24 * time.Hour)

	f := &windowFetcher{prs: []FetchedPR{
		{Number: 1, Title: "recent", State: "open", CreatedAt: recent, UpdatedAt: recent},
		{Number: 2, Title: "old", State: "closed", CreatedAt: old, UpdatedAt: old},
	}}

	if _, err := SyncPRs(context.Background(), f, ledgerPath, "o", "r", 30, logger); err != nil {
		t.Fatal(err)
	}
	if snaps := prSnapshots(t, ledgerPath, 2); len(snaps) != 0 {
		t.Fatalf("30-day sync wrote PR 2 from 60 days ago")
	}

	if _, err := SyncPRs(context.Background(), f, ledgerPath, "o", "r", 90, logger); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadGitHubPR(ledgerPath, 2, old); err != nil {
		t.Fatalf("--days 90 after a 30-day sync did not fetch PR 2 (since=%v): %v", f.prSinces[len(f.prSinces)-1], err)
	}

	// The widened window is now covered: the next 90-day (and 30-day) run is
	// incremental again instead of re-listing three months every time.
	for _, days := range []int{90, 30} {
		if _, err := SyncPRs(context.Background(), f, ledgerPath, "o", "r", days, logger); err != nil {
			t.Fatal(err)
		}
		since := f.prSinces[len(f.prSinces)-1]
		if since.Before(now.Add(-time.Hour)) {
			t.Fatalf("--days %d after the window was covered listed from %v; want incremental", days, since)
		}
	}
}

func TestSyncIssues_WiderDaysAfterFirstSyncBackfills(t *testing.T) {
	ledgerPath := t.TempDir()
	logger := slog.Default()
	now := time.Now().UTC().Truncate(time.Second)
	old := now.Add(-60 * 24 * time.Hour)

	f := &windowFetcher{issues: []FetchedIssue{{Number: 3, Title: "old", State: "open", CreatedAt: old, UpdatedAt: old}}}
	if _, err := SyncIssues(context.Background(), f, ledgerPath, "o", "r", 30, logger); err != nil {
		t.Fatal(err)
	}
	if _, err := SyncIssues(context.Background(), f, ledgerPath, "o", "r", 90, logger); err != nil {
		t.Fatal(err)
	}
	if snaps := issueSnapshots(t, ledgerPath, 3); len(snaps) != 1 {
		t.Fatalf("--days 90 after a 30-day sync wrote %d snapshot(s) of issue 3; want 1", len(snaps))
	}
}

// cancelOnCommentsFetcher cancels the sync's context from inside a comment
// fetch, as a Ctrl-C or daemon shutdown would, and fails the fetch.
type cancelOnCommentsFetcher struct {
	windowFetcher
	cancel context.CancelFunc
}

func (f *cancelOnCommentsFetcher) ListPRComments(context.Context, string, string, int) ([]FetchedComment, error) {
	f.cancel()
	return nil, context.Canceled
}

func (f *cancelOnCommentsFetcher) ListIssueComments(context.Context, string, string, int) ([]FetchedComment, error) {
	f.cancel()
	return nil, context.Canceled
}

// TestSync_CanceledDuringCommentFetchStopsWithoutSavingState verifies a
// cancellation is not mistaken for a per-item failure: the sync stops with the
// context error and does not persist a cursor, so nothing is skipped later.
func TestSync_CanceledDuringCommentFetchStopsWithoutSavingState(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	at := now.Add(-time.Hour)
	run := map[string]func(context.Context, GitHubFetcher, string) error{
		"pr": func(ctx context.Context, f GitHubFetcher, lp string) error {
			_, err := SyncPRs(ctx, f, lp, "o", "r", 30, slog.Default())
			return err
		},
		"issue": func(ctx context.Context, f GitHubFetcher, lp string) error {
			_, err := SyncIssues(ctx, f, lp, "o", "r", 30, slog.Default())
			return err
		},
	}
	for dataType, sync := range run {
		t.Run(dataType, func(t *testing.T) {
			ledgerPath := t.TempDir()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			f := &cancelOnCommentsFetcher{cancel: cancel, windowFetcher: windowFetcher{
				prs:    []FetchedPR{{Number: 1, State: "open", CreatedAt: at, UpdatedAt: at}},
				issues: []FetchedIssue{{Number: 1, State: "open", CreatedAt: at, UpdatedAt: at}},
			}}
			if err := sync(ctx, f, ledgerPath); !errors.Is(err, context.Canceled) {
				t.Fatalf("sync error = %v, want context.Canceled", err)
			}
			state, err := ReadGitHubTypeSyncState(ledgerPath, dataType)
			if err != nil {
				t.Fatal(err)
			}
			if !state.LastSyncAt.IsZero() {
				t.Fatalf("canceled sync persisted a cursor (%v)", state.LastSyncAt)
			}
		})
	}
}
