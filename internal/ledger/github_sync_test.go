package ledger

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// mockFetcher implements GitHubFetcher for testing.
type mockFetcher struct {
	prs          []FetchedPR
	issues       []FetchedIssue
	comments     map[int][]FetchedComment  // keyed by issue/PR number
	prCommits    map[int][]FetchedPRCommit // keyed by PR number
	prRL         *FetchRateLimit
	issueRL      *FetchRateLimit
	err          error
	prCommitsErr error // independent error for ListPRCommits
}

func (m *mockFetcher) ListPullRequests(_ context.Context, _, _ string, _ ListPRsOptions) ([]FetchedPR, *FetchRateLimit, error) {
	return m.prs, m.prRL, m.err
}

func (m *mockFetcher) ListIssues(_ context.Context, _, _ string, _ ListIssuesOptions) ([]FetchedIssue, *FetchRateLimit, error) {
	return m.issues, m.issueRL, m.err
}

func (m *mockFetcher) ListPRComments(_ context.Context, _, _ string, number int) ([]FetchedComment, error) {
	return m.comments[number], m.err
}

func (m *mockFetcher) ListIssueComments(_ context.Context, _, _ string, number int) ([]FetchedComment, error) {
	return m.comments[number], m.err
}

func (m *mockFetcher) ListPRCommits(_ context.Context, _, _ string, number int) ([]FetchedPRCommit, error) {
	if m.prCommitsErr != nil {
		return nil, m.prCommitsErr
	}
	if m.prCommits != nil {
		return m.prCommits[number], m.err
	}
	return nil, m.err
}

func TestSyncPRs_NewPRs(t *testing.T) {
	ledgerPath := t.TempDir()
	logger := slog.Default()

	now := time.Now().UTC().Truncate(time.Second)
	fetcher := &mockFetcher{
		prs: []FetchedPR{
			{
				Number:    100,
				Title:     "Add feature X",
				Body:      "description",
				State:     "open",
				Author:    "alice",
				Labels:    []string{"enhancement"},
				CreatedAt: now,
				UpdatedAt: now,
				HTMLURL:   "https://github.com/org/repo/pull/100",
			},
			{
				Number:    101,
				Title:     "Fix bug Y",
				State:     "merged",
				Author:    "bob",
				CreatedAt: now,
				UpdatedAt: now,
				MergedAt:  &now,
				MergeSHA:  "abc123",
				HTMLURL:   "https://github.com/org/repo/pull/101",
			},
		},
		comments: map[int][]FetchedComment{
			100: {{Author: "carol", Body: "LGTM", CreatedAt: now}},
			101: {{Author: "dave", Body: "Looks good", Path: "main.go", CreatedAt: now}},
		},
	}

	result, err := SyncPRs(context.Background(), fetcher, ledgerPath, "org", "repo", 30, logger)
	if err != nil {
		t.Fatalf("SyncPRs: %v", err)
	}

	if result.PRTotal != 2 {
		t.Errorf("expected 2 PRs, got %d", result.PRTotal)
	}
	if result.PRCreated != 2 {
		t.Errorf("expected 2 created, got %d", result.PRCreated)
	}
	if result.PRUpdated != 0 {
		t.Errorf("expected 0 updated, got %d", result.PRUpdated)
	}

	// verify files were written
	files, err := ListGitHubDataFiles(ledgerPath, "pr")
	if err != nil {
		t.Fatalf("ListGitHubDataFiles: %v", err)
	}
	if len(files) != 2 {
		t.Errorf("expected 2 PR files, got %d", len(files))
	}

	// verify sync state was persisted
	state, err := ReadGitHubTypeSyncState(ledgerPath, "pr")
	if err != nil {
		t.Fatalf("ReadGitHubTypeSyncState: %v", err)
	}
	if state.Count != 2 {
		t.Errorf("expected count 2, got %d", state.Count)
	}
	if len(state.KnownItems) != 2 {
		t.Errorf("expected 2 known items, got %d", len(state.KnownItems))
	}
	if state.KnownItems[100].State != "open" {
		t.Errorf("expected PR 100 state 'open', got %q", state.KnownItems[100].State)
	}
	if state.KnownItems[101].State != "merged" {
		t.Errorf("expected PR 101 state 'merged', got %q", state.KnownItems[101].State)
	}
}

func TestSyncPRs_IncrementalUpdate(t *testing.T) {
	ledgerPath := t.TempDir()
	logger := slog.Default()

	now := time.Now().UTC().Truncate(time.Second)

	// first sync: create PR 100 as open
	fetcher := &mockFetcher{
		prs: []FetchedPR{{
			Number:    100,
			Title:     "Feature",
			State:     "open",
			Author:    "alice",
			CreatedAt: now,
			UpdatedAt: now,
		}},
		comments: map[int][]FetchedComment{},
	}

	result1, err := SyncPRs(context.Background(), fetcher, ledgerPath, "org", "repo", 30, logger)
	if err != nil {
		t.Fatalf("first sync: %v", err)
	}
	if result1.PRCreated != 1 {
		t.Errorf("first sync: expected 1 created, got %d", result1.PRCreated)
	}

	// second sync: PR 100 now merged (state transition)
	merged := now.Add(time.Hour)
	fetcher2 := &mockFetcher{
		prs: []FetchedPR{{
			Number:    100,
			Title:     "Feature",
			State:     "closed",
			Author:    "alice",
			CreatedAt: now,
			UpdatedAt: merged,
			MergedAt:  &merged,
			MergeSHA:  "def456",
		}},
		comments: map[int][]FetchedComment{
			100: {{Author: "bob", Body: "merged!", CreatedAt: merged}},
		},
	}

	result2, err := SyncPRs(context.Background(), fetcher2, ledgerPath, "org", "repo", 30, logger)
	if err != nil {
		t.Fatalf("second sync: %v", err)
	}
	if result2.PRUpdated != 1 {
		t.Errorf("second sync: expected 1 updated, got %d", result2.PRUpdated)
	}
	if result2.PRCreated != 0 {
		t.Errorf("second sync: expected 0 created, got %d", result2.PRCreated)
	}

	// verify state was updated
	state, err := ReadGitHubTypeSyncState(ledgerPath, "pr")
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	if state.KnownItems[100].State != "merged" {
		t.Errorf("expected state 'merged', got %q", state.KnownItems[100].State)
	}
}

func TestSyncIssues_NewIssues(t *testing.T) {
	ledgerPath := t.TempDir()
	logger := slog.Default()

	now := time.Now().UTC().Truncate(time.Second)
	fetcher := &mockFetcher{
		issues: []FetchedIssue{
			{
				Number:    50,
				Title:     "Bug report",
				Body:      "something is broken",
				State:     "open",
				Author:    "alice",
				Labels:    []string{"bug"},
				CreatedAt: now,
				UpdatedAt: now,
				HTMLURL:   "https://github.com/org/repo/issues/50",
			},
		},
		comments: map[int][]FetchedComment{
			50: {{Author: "bob", Body: "confirmed", CreatedAt: now}},
		},
	}

	result, err := SyncIssues(context.Background(), fetcher, ledgerPath, "org", "repo", 30, logger)
	if err != nil {
		t.Fatalf("SyncIssues: %v", err)
	}

	if result.IssueTotal != 1 {
		t.Errorf("expected 1 issue, got %d", result.IssueTotal)
	}
	if result.IssueCreated != 1 {
		t.Errorf("expected 1 created, got %d", result.IssueCreated)
	}

	files, err := ListGitHubDataFiles(ledgerPath, "issue")
	if err != nil {
		t.Fatalf("ListGitHubDataFiles: %v", err)
	}
	if len(files) != 1 {
		t.Errorf("expected 1 issue file, got %d", len(files))
	}
}

func TestSyncPRs_MergedPRGetsCommits(t *testing.T) {
	ledgerPath := t.TempDir()
	logger := slog.Default()

	now := time.Now().UTC().Truncate(time.Second)
	fetcher := &mockFetcher{
		prs: []FetchedPR{
			{
				Number:    200,
				Title:     "Merged PR with commits",
				State:     "closed",
				Author:    "alice",
				CreatedAt: now,
				UpdatedAt: now,
				MergedAt:  &now,
				MergeSHA:  "merge123",
				HTMLURL:   "https://github.com/org/repo/pull/200",
			},
		},
		comments: map[int][]FetchedComment{},
		prCommits: map[int][]FetchedPRCommit{
			200: {
				{SHA: "aaa111", Author: "alice", Date: now.Add(-2 * time.Hour), Msg: "first commit"},
				{SHA: "bbb222", Author: "alice", Date: now.Add(-1 * time.Hour), Msg: "second commit"},
			},
		},
	}

	result, err := SyncPRs(context.Background(), fetcher, ledgerPath, "org", "repo", 30, logger)
	if err != nil {
		t.Fatalf("SyncPRs: %v", err)
	}

	if result.PRCreated != 1 {
		t.Errorf("expected 1 created, got %d", result.PRCreated)
	}

	// verify the PR file contains commits
	files, err := ListGitHubDataFiles(ledgerPath, "pr")
	if err != nil {
		t.Fatalf("ListGitHubDataFiles: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 PR file, got %d", len(files))
	}

	data, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatalf("read PR file: %v", err)
	}

	var pr PRFile
	if err := json.Unmarshal(data, &pr); err != nil {
		t.Fatalf("unmarshal PR: %v", err)
	}

	if len(pr.Commits) != 2 {
		t.Errorf("expected 2 commits, got %d", len(pr.Commits))
	}
	if pr.Commits[0].SHA != "aaa111" {
		t.Errorf("expected first commit SHA 'aaa111', got %q", pr.Commits[0].SHA)
	}
	if pr.Commits[1].Msg != "second commit" {
		t.Errorf("expected second commit message 'second commit', got %q", pr.Commits[1].Msg)
	}
}

func TestSyncPRs_OpenPRDoesNotGetCommits(t *testing.T) {
	ledgerPath := t.TempDir()
	logger := slog.Default()

	now := time.Now().UTC().Truncate(time.Second)
	fetcher := &mockFetcher{
		prs: []FetchedPR{
			{
				Number:    300,
				Title:     "Open PR",
				State:     "open",
				Author:    "bob",
				CreatedAt: now,
				UpdatedAt: now,
			},
		},
		comments: map[int][]FetchedComment{},
		prCommits: map[int][]FetchedPRCommit{
			300: {{SHA: "ccc333", Author: "bob", Date: now, Msg: "wip"}},
		},
	}

	_, err := SyncPRs(context.Background(), fetcher, ledgerPath, "org", "repo", 30, logger)
	if err != nil {
		t.Fatalf("SyncPRs: %v", err)
	}

	files, _ := ListGitHubDataFiles(ledgerPath, "pr")
	data, _ := os.ReadFile(files[0])
	var pr PRFile
	_ = json.Unmarshal(data, &pr)

	if len(pr.Commits) != 0 {
		t.Errorf("open PR should have 0 commits, got %d", len(pr.Commits))
	}
}

func TestSyncPRs_StateTransitionToMergedGetsCommits(t *testing.T) {
	ledgerPath := t.TempDir()
	logger := slog.Default()

	now := time.Now().UTC().Truncate(time.Second)

	// first sync: PR is open
	fetcher1 := &mockFetcher{
		prs: []FetchedPR{{
			Number: 400, Title: "Feature", State: "open", Author: "alice",
			CreatedAt: now, UpdatedAt: now,
		}},
		comments:  map[int][]FetchedComment{},
		prCommits: map[int][]FetchedPRCommit{},
	}
	_, err := SyncPRs(context.Background(), fetcher1, ledgerPath, "org", "repo", 30, logger)
	if err != nil {
		t.Fatalf("first sync: %v", err)
	}

	// second sync: PR merged (state transition)
	merged := now.Add(time.Hour)
	fetcher2 := &mockFetcher{
		prs: []FetchedPR{{
			Number: 400, Title: "Feature", State: "closed", Author: "alice",
			CreatedAt: now, UpdatedAt: merged, MergedAt: &merged, MergeSHA: "xyz789",
		}},
		comments: map[int][]FetchedComment{},
		prCommits: map[int][]FetchedPRCommit{
			400: {{SHA: "ddd444", Author: "alice", Date: now, Msg: "the commit"}},
		},
	}
	_, err = SyncPRs(context.Background(), fetcher2, ledgerPath, "org", "repo", 30, logger)
	if err != nil {
		t.Fatalf("second sync: %v", err)
	}

	// verify commits were fetched on state transition (use ReadGitHubPR for latest version)
	pr, readErr := ReadGitHubPR(ledgerPath, 400, now)
	if readErr != nil {
		t.Fatalf("read PR 400: %v", readErr)
	}

	if len(pr.Commits) != 1 {
		t.Errorf("expected 1 commit after merge transition, got %d", len(pr.Commits))
	}
}

func TestSyncPRs_ReplayedMergedPRPreservesCommits(t *testing.T) {
	ledgerPath := t.TempDir()
	logger := slog.Default()

	now := time.Now().UTC().Truncate(time.Second)

	// first sync: merged PR with commits
	fetcher1 := &mockFetcher{
		prs: []FetchedPR{{
			Number: 450, Title: "Feature", State: "closed", Author: "alice",
			CreatedAt: now, UpdatedAt: now, MergedAt: &now, MergeSHA: "abc123",
		}},
		comments: map[int][]FetchedComment{},
		prCommits: map[int][]FetchedPRCommit{
			450: {
				{SHA: "commit1", Author: "alice", Date: now, Msg: "first"},
				{SHA: "commit2", Author: "alice", Date: now, Msg: "second"},
			},
		},
	}
	_, err := SyncPRs(context.Background(), fetcher1, ledgerPath, "org", "repo", 30, logger)
	if err != nil {
		t.Fatalf("first sync: %v", err)
	}

	// verify commits were stored
	existing, err := ReadGitHubPR(ledgerPath, 450, now)
	if err != nil {
		t.Fatalf("read PR after first sync: %v", err)
	}
	if len(existing.Commits) != 2 {
		t.Fatalf("expected 2 commits after first sync, got %d", len(existing.Commits))
	}

	// second sync: same PR replayed (simulates --full / cursor reset)
	// fetcher should NOT be called for commits since state hasn't changed
	fetcher2 := &mockFetcher{
		prs: []FetchedPR{{
			Number: 450, Title: "Feature", State: "closed", Author: "alice",
			CreatedAt: now, UpdatedAt: now, MergedAt: &now, MergeSHA: "abc123",
		}},
		comments:  map[int][]FetchedComment{},
		prCommits: map[int][]FetchedPRCommit{}, // empty — shouldn't be called
	}
	_, err = SyncPRs(context.Background(), fetcher2, ledgerPath, "org", "repo", 30, logger)
	if err != nil {
		t.Fatalf("second sync: %v", err)
	}

	// verify commits are preserved (not dropped by omitempty)
	replayed, err := ReadGitHubPR(ledgerPath, 450, now)
	if err != nil {
		t.Fatalf("read PR after second sync: %v", err)
	}
	if len(replayed.Commits) != 2 {
		t.Errorf("expected 2 commits preserved after replay, got %d", len(replayed.Commits))
	}
	if replayed.Commits[0].SHA != "commit1" {
		t.Errorf("expected first commit SHA 'commit1', got %q", replayed.Commits[0].SHA)
	}
}

func TestSyncPRs_CommitFetchErrorIsBestEffort(t *testing.T) {
	ledgerPath := t.TempDir()
	logger := slog.Default()

	now := time.Now().UTC().Truncate(time.Second)
	fetcher := &mockFetcher{
		prs: []FetchedPR{{
			Number: 460, Title: "Merged PR", State: "closed", Author: "alice",
			CreatedAt: now, UpdatedAt: now, MergedAt: &now, MergeSHA: "def456",
		}},
		comments:     map[int][]FetchedComment{},
		prCommitsErr: fmt.Errorf("GitHub API rate limited"),
	}

	result, err := SyncPRs(context.Background(), fetcher, ledgerPath, "org", "repo", 30, logger)
	if err != nil {
		t.Fatalf("SyncPRs should succeed despite commit fetch error: %v", err)
	}
	if result.PRCreated != 1 {
		t.Errorf("expected 1 created, got %d", result.PRCreated)
	}

	// PR should be written without commits (best-effort)
	pr, err := ReadGitHubPR(ledgerPath, 460, now)
	if err != nil {
		t.Fatalf("read PR: %v", err)
	}
	if len(pr.Commits) != 0 {
		t.Errorf("expected 0 commits when fetch fails, got %d", len(pr.Commits))
	}
}

func TestBackfillPRCommits(t *testing.T) {
	ledgerPath := t.TempDir()
	logger := slog.Default()

	now := time.Now().UTC().Truncate(time.Second)

	// write a merged PR without commits (simulating pre-feature data)
	pr := &PRFile{
		Number:      500,
		Title:       "Old merged PR",
		State:       "merged",
		Author:      "alice",
		CreatedAt:   now,
		UpdatedAt:   now,
		MergedAt:    &now,
		MergeCommit: "old123",
	}
	if err := WriteGitHubPR(ledgerPath, pr); err != nil {
		t.Fatalf("write PR: %v", err)
	}

	// also write an open PR (should not be backfilled)
	openPR := &PRFile{
		Number:    501,
		Title:     "Open PR",
		State:     "open",
		Author:    "bob",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := WriteGitHubPR(ledgerPath, openPR); err != nil {
		t.Fatalf("write open PR: %v", err)
	}

	fetcher := &mockFetcher{
		prCommits: map[int][]FetchedPRCommit{
			500: {
				{SHA: "eee555", Author: "alice", Date: now, Msg: "backfilled commit"},
			},
		},
	}

	backfilled, err := BackfillPRCommits(context.Background(), fetcher, ledgerPath, "org", "repo", logger)
	if err != nil {
		t.Fatalf("BackfillPRCommits: %v", err)
	}

	if backfilled != 1 {
		t.Errorf("expected 1 backfilled, got %d", backfilled)
	}

	// verify the PR file now has commits (use ReadGitHubPR to get latest version)
	got500, err := ReadGitHubPR(ledgerPath, 500, now)
	if err != nil {
		t.Fatalf("read PR 500: %v", err)
	}
	if len(got500.Commits) != 1 {
		t.Errorf("expected 1 commit for PR 500, got %d", len(got500.Commits))
	}
	if got500.Commits[0].SHA != "eee555" {
		t.Errorf("expected commit SHA 'eee555', got %q", got500.Commits[0].SHA)
	}

	// verify open PR was not backfilled
	got501, err := ReadGitHubPR(ledgerPath, 501, now)
	if err != nil {
		t.Fatalf("read PR 501: %v", err)
	}
	if len(got501.Commits) != 0 {
		t.Errorf("open PR should not have commits, got %d", len(got501.Commits))
	}
}

func TestBackfillPRCommits_AlreadyHasCommits(t *testing.T) {
	ledgerPath := t.TempDir()
	logger := slog.Default()

	now := time.Now().UTC().Truncate(time.Second)

	// write a merged PR that already has commits
	pr := &PRFile{
		Number:      600,
		Title:       "Already has commits",
		State:       "merged",
		Author:      "alice",
		CreatedAt:   now,
		UpdatedAt:   now,
		MergedAt:    &now,
		MergeCommit: "fff666",
		Commits: []PRCommit{
			{SHA: "existing", Author: "alice", Date: now, Msg: "existing"},
		},
	}
	if err := WriteGitHubPR(ledgerPath, pr); err != nil {
		t.Fatalf("write PR: %v", err)
	}

	fetcher := &mockFetcher{
		prCommits: map[int][]FetchedPRCommit{
			600: {{SHA: "new", Author: "alice", Date: now, Msg: "should not replace"}},
		},
	}

	backfilled, err := BackfillPRCommits(context.Background(), fetcher, ledgerPath, "org", "repo", logger)
	if err != nil {
		t.Fatalf("BackfillPRCommits: %v", err)
	}

	if backfilled != 0 {
		t.Errorf("expected 0 backfilled (already has commits), got %d", backfilled)
	}
}

func TestPRFile_BackwardCompat_NoCommitsField(t *testing.T) {
	// Simulate old JSON without commits field
	oldJSON := `{
		"number": 700,
		"title": "Old PR",
		"body": "",
		"author": "alice",
		"state": "merged",
		"created_at": "2026-01-01T00:00:00Z",
		"updated_at": "2026-01-01T00:00:00Z",
		"merge_commit": "ggg777",
		"url": "https://github.com/org/repo/pull/700"
	}`

	var pr PRFile
	if err := json.Unmarshal([]byte(oldJSON), &pr); err != nil {
		t.Fatalf("unmarshal old JSON: %v", err)
	}

	if pr.Number != 700 {
		t.Errorf("expected number 700, got %d", pr.Number)
	}
	if pr.Commits != nil {
		t.Errorf("expected nil commits from old JSON, got %v", pr.Commits)
	}
}

func TestSyncPRs_ContextCancellation(t *testing.T) {
	ledgerPath := t.TempDir()
	logger := slog.Default()

	now := time.Now().UTC().Truncate(time.Second)
	fetcher := &mockFetcher{
		prs: []FetchedPR{
			{Number: 1, State: "open", Author: "a", CreatedAt: now, UpdatedAt: now},
			{Number: 2, State: "open", Author: "b", CreatedAt: now, UpdatedAt: now},
		},
		comments: map[int][]FetchedComment{},
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	_, err := SyncPRs(ctx, fetcher, ledgerPath, "org", "repo", 30, logger)
	if err == nil {
		t.Fatal("expected context cancellation error")
	}
}

func TestCommitAndPushGitHubData_NothingToCommit(t *testing.T) {
	// create a git repo with nothing to stage
	ledgerPath := t.TempDir()
	initGitRepo(t, ledgerPath)

	result := &SyncResult{PRTotal: 0, IssueTotal: 0}
	pushCalled := false
	pushFn := func(_ context.Context, _ string) error {
		pushCalled = true
		return nil
	}

	// with 0 total items, CommitAndPushGitHubData is not called by the caller
	// but let's test the "nothing to commit" path
	err := CommitAndPushGitHubData(context.Background(), ledgerPath, "org", "repo", result, pushFn)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// push should not be called since there's nothing to commit
	if pushCalled {
		t.Error("push should not be called when nothing to commit")
	}
}

func TestCommitAndPushGitHubData_WithData(t *testing.T) {
	ledgerPath := t.TempDir()
	initGitRepo(t, ledgerPath)

	// write a PR file
	now := time.Now().UTC().Truncate(time.Second)
	pr := &PRFile{
		Number:    42,
		Title:     "Test PR",
		State:     "open",
		Author:    "test",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := WriteGitHubPR(ledgerPath, pr); err != nil {
		t.Fatalf("WriteGitHubPR: %v", err)
	}

	result := &SyncResult{PRTotal: 1, PRCreated: 1}
	pushCalled := false
	pushFn := func(_ context.Context, _ string) error {
		pushCalled = true
		return nil
	}

	err := CommitAndPushGitHubData(context.Background(), ledgerPath, "org", "repo", result, pushFn)
	if err != nil {
		t.Fatalf("CommitAndPushGitHubData: %v", err)
	}
	if !pushCalled {
		t.Error("push should have been called")
	}
}

func TestSyncPRs_RebuildStateFromDisk_SkipsCommentFetch(t *testing.T) {
	ledgerPath := t.TempDir()
	logger := slog.Default()

	now := time.Now().UTC().Truncate(time.Second)

	// simulate daemon having already synced PR files to disk
	for _, pr := range []*PRFile{
		{Number: 10, Title: "PR 10", State: "open", Author: "alice", CreatedAt: now, UpdatedAt: now},
		{Number: 11, Title: "PR 11", State: "merged", Author: "bob", CreatedAt: now, UpdatedAt: now, MergedAt: &now, MergeCommit: "abc",
			Commits: []PRCommit{{SHA: "c1", Author: "bob", Date: now, Msg: "commit"}}},
	} {
		if err := WriteGitHubPR(ledgerPath, pr); err != nil {
			t.Fatalf("write PR %d: %v", pr.Number, err)
		}
	}

	// NO sync state file — simulates cache loss / first CLI run after daemon sync

	// fetcher returns the same PRs (incremental sync finds them)
	// track whether comment fetching is called
	commentCalls := 0
	fetcher := &countingFetcher{
		inner: &mockFetcher{
			prs: []FetchedPR{
				{Number: 10, Title: "PR 10", State: "open", Author: "alice", CreatedAt: now, UpdatedAt: now},
				{Number: 11, Title: "PR 11", State: "closed", Author: "bob", CreatedAt: now, UpdatedAt: now, MergedAt: &now, MergeSHA: "abc"},
			},
			comments: map[int][]FetchedComment{},
		},
		onCommentCall: func() { commentCalls++ },
	}

	result, err := SyncPRs(context.Background(), fetcher, ledgerPath, "org", "repo", 30, logger)
	if err != nil {
		t.Fatalf("SyncPRs: %v", err)
	}

	// both PRs should be treated as "known" (rebuilt from disk) — no comment fetches,
	// no writes, no counts (known + unchanged = skipped entirely)
	if commentCalls > 0 {
		t.Errorf("expected 0 comment API calls (state rebuilt from disk), got %d", commentCalls)
	}
	if result.PRCreated != 0 {
		t.Errorf("expected 0 created (all known from disk), got %d", result.PRCreated)
	}
	if result.PRUpdated != 0 {
		t.Errorf("expected 0 updated (state unchanged = skipped), got %d", result.PRUpdated)
	}
	if result.PRTotal != 0 {
		t.Errorf("expected 0 total (all skipped), got %d", result.PRTotal)
	}

	// verify the persisted sync state was rebuilt correctly
	state, err := ReadGitHubTypeSyncState(ledgerPath, "pr")
	if err != nil {
		t.Fatalf("ReadGitHubTypeSyncState: %v", err)
	}
	if len(state.KnownItems) != 2 {
		t.Errorf("expected 2 known items, got %d", len(state.KnownItems))
	}
	if state.KnownItems[10].State != "open" {
		t.Errorf("expected PR 10 state 'open', got %q", state.KnownItems[10].State)
	}
	if state.KnownItems[11].State != "merged" {
		t.Errorf("expected PR 11 state 'merged', got %q", state.KnownItems[11].State)
	}
	if state.LastSyncAt.IsZero() {
		t.Error("expected non-zero LastSyncAt after rebuild")
	}
}

func TestSyncIssues_RebuildStateFromDisk_SkipsCommentFetch(t *testing.T) {
	ledgerPath := t.TempDir()
	logger := slog.Default()

	now := time.Now().UTC().Truncate(time.Second)

	// simulate daemon having already synced issue files to disk
	if err := WriteGitHubIssue(ledgerPath, &IssueFile{
		Number: 20, Title: "Issue 20", State: "open", Author: "alice", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("write issue: %v", err)
	}

	// NO sync state file

	commentCalls := 0
	fetcher := &countingFetcher{
		inner: &mockFetcher{
			issues: []FetchedIssue{
				{Number: 20, Title: "Issue 20", State: "open", Author: "alice", CreatedAt: now, UpdatedAt: now},
			},
			comments: map[int][]FetchedComment{},
		},
		onCommentCall: func() { commentCalls++ },
	}

	result, err := SyncIssues(context.Background(), fetcher, ledgerPath, "org", "repo", 30, logger)
	if err != nil {
		t.Fatalf("SyncIssues: %v", err)
	}

	if commentCalls > 0 {
		t.Errorf("expected 0 comment API calls (state rebuilt from disk), got %d", commentCalls)
	}
	if result.IssueCreated != 0 {
		t.Errorf("expected 0 created (known from disk), got %d", result.IssueCreated)
	}
	if result.IssueUpdated != 0 {
		t.Errorf("expected 0 updated (state unchanged = skipped), got %d", result.IssueUpdated)
	}
	if result.IssueTotal != 0 {
		t.Errorf("expected 0 total (all skipped), got %d", result.IssueTotal)
	}

	// verify the persisted sync state was rebuilt correctly
	state, err := ReadGitHubTypeSyncState(ledgerPath, "issue")
	if err != nil {
		t.Fatalf("ReadGitHubTypeSyncState: %v", err)
	}
	if len(state.KnownItems) != 1 {
		t.Errorf("expected 1 known item, got %d", len(state.KnownItems))
	}
	if state.KnownItems[20].State != "open" {
		t.Errorf("expected issue 20 state 'open', got %q", state.KnownItems[20].State)
	}
	if state.LastSyncAt.IsZero() {
		t.Error("expected non-zero LastSyncAt after rebuild")
	}
}

func TestSyncPRs_KnownUnchangedDoesNotOverwriteComments(t *testing.T) {
	// Regression: when a PR is known and state hasn't changed, re-sync must
	// NOT overwrite the existing file (which would drop comments).
	ledgerPath := t.TempDir()
	logger := slog.Default()

	now := time.Now().UTC().Truncate(time.Second)

	// first sync: new PR with comments
	fetcher1 := &mockFetcher{
		prs: []FetchedPR{{
			Number: 700, Title: "Feature", State: "open", Author: "alice",
			CreatedAt: now, UpdatedAt: now,
		}},
		comments: map[int][]FetchedComment{
			700: {{Author: "bob", Body: "nice work", CreatedAt: now}},
		},
	}
	_, err := SyncPRs(context.Background(), fetcher1, ledgerPath, "org", "repo", 30, logger)
	if err != nil {
		t.Fatalf("first sync: %v", err)
	}

	// verify comments were stored (fetchPRComments calls both ListPRComments
	// and ListIssueComments, so the same mock comment appears twice)
	pr1, err := ReadGitHubPR(ledgerPath, 700, now)
	if err != nil {
		t.Fatalf("read PR after first sync: %v", err)
	}
	if len(pr1.Comments) != 2 {
		t.Fatalf("expected 2 comments after first sync, got %d", len(pr1.Comments))
	}

	// second sync: same PR, same state — must NOT overwrite
	fetcher2 := &mockFetcher{
		prs: []FetchedPR{{
			Number: 700, Title: "Feature", State: "open", Author: "alice",
			CreatedAt: now, UpdatedAt: now,
		}},
		comments: map[int][]FetchedComment{}, // empty — shouldn't matter since PR is skipped
	}
	result, err := SyncPRs(context.Background(), fetcher2, ledgerPath, "org", "repo", 30, logger)
	if err != nil {
		t.Fatalf("second sync: %v", err)
	}

	// PR was skipped — counters must be zero
	if result.PRTotal != 0 {
		t.Errorf("expected 0 total (skipped), got %d", result.PRTotal)
	}

	// comments must still be present on disk
	pr2, err := ReadGitHubPR(ledgerPath, 700, now)
	if err != nil {
		t.Fatalf("read PR after second sync: %v", err)
	}
	if len(pr2.Comments) != 2 {
		t.Errorf("expected 2 comments preserved after re-sync, got %d", len(pr2.Comments))
	}
	if pr2.Comments[0].Body != "nice work" {
		t.Errorf("expected comment body 'nice work', got %q", pr2.Comments[0].Body)
	}
}

func TestSyncIssues_KnownUnchangedDoesNotOverwriteComments(t *testing.T) {
	// Regression: when an issue is known and state hasn't changed, re-sync must
	// NOT overwrite the existing file (which would drop comments).
	ledgerPath := t.TempDir()
	logger := slog.Default()

	now := time.Now().UTC().Truncate(time.Second)

	// first sync: new issue with comments
	fetcher1 := &mockFetcher{
		issues: []FetchedIssue{{
			Number: 800, Title: "Bug", State: "open", Author: "alice",
			CreatedAt: now, UpdatedAt: now,
		}},
		comments: map[int][]FetchedComment{
			800: {{Author: "carol", Body: "reproduced", CreatedAt: now}},
		},
	}
	_, err := SyncIssues(context.Background(), fetcher1, ledgerPath, "org", "repo", 30, logger)
	if err != nil {
		t.Fatalf("first sync: %v", err)
	}

	// verify comments were stored
	issueFile1 := readIssueFile(t, ledgerPath, 800, now)
	if len(issueFile1.Comments) != 1 {
		t.Fatalf("expected 1 comment after first sync, got %d", len(issueFile1.Comments))
	}

	// second sync: same issue, same state — must NOT overwrite
	fetcher2 := &mockFetcher{
		issues: []FetchedIssue{{
			Number: 800, Title: "Bug", State: "open", Author: "alice",
			CreatedAt: now, UpdatedAt: now,
		}},
		comments: map[int][]FetchedComment{}, // empty — shouldn't matter since issue is skipped
	}
	result, err := SyncIssues(context.Background(), fetcher2, ledgerPath, "org", "repo", 30, logger)
	if err != nil {
		t.Fatalf("second sync: %v", err)
	}

	// issue was skipped — counters must be zero
	if result.IssueTotal != 0 {
		t.Errorf("expected 0 total (skipped), got %d", result.IssueTotal)
	}

	// comments must still be present on disk
	issueFile2 := readIssueFile(t, ledgerPath, 800, now)
	if len(issueFile2.Comments) != 1 {
		t.Errorf("expected 1 comment preserved after re-sync, got %d", len(issueFile2.Comments))
	}
	if issueFile2.Comments[0].Body != "reproduced" {
		t.Errorf("expected comment body 'reproduced', got %q", issueFile2.Comments[0].Body)
	}
}

func TestSyncPRs_ResyncsWhenUpdatedAtChanges(t *testing.T) {
	// Regression: a PR with same state but different updated_at (e.g., new comment
	// added) must trigger a re-sync so comments are re-fetched.
	ledgerPath := t.TempDir()
	logger := slog.Default()

	now := time.Now().UTC().Truncate(time.Second)

	// first sync: PR 900 open
	fetcher1 := &mockFetcher{
		prs: []FetchedPR{{
			Number: 900, Title: "Feature", State: "open", Author: "alice",
			CreatedAt: now, UpdatedAt: now,
		}},
		comments: map[int][]FetchedComment{
			900: {{Author: "bob", Body: "first comment", CreatedAt: now}},
		},
	}
	_, err := SyncPRs(context.Background(), fetcher1, ledgerPath, "org", "repo", 30, logger)
	if err != nil {
		t.Fatalf("first sync: %v", err)
	}

	// second sync: same state (open) but updated_at changed (new comment)
	later := now.Add(time.Hour)
	commentCalls := 0
	fetcher2 := &countingFetcher{
		inner: &mockFetcher{
			prs: []FetchedPR{{
				Number: 900, Title: "Feature", State: "open", Author: "alice",
				CreatedAt: now, UpdatedAt: later, // updated_at changed
			}},
			comments: map[int][]FetchedComment{
				900: {
					{Author: "bob", Body: "first comment", CreatedAt: now},
					{Author: "carol", Body: "new comment", CreatedAt: later},
				},
			},
		},
		onCommentCall: func() { commentCalls++ },
	}

	result, err := SyncPRs(context.Background(), fetcher2, ledgerPath, "org", "repo", 30, logger)
	if err != nil {
		t.Fatalf("second sync: %v", err)
	}

	// PR should be updated (not skipped) because updated_at changed
	if result.PRUpdated != 1 {
		t.Errorf("expected 1 updated, got %d", result.PRUpdated)
	}
	if commentCalls == 0 {
		t.Error("expected comment API calls for updated_at change, got 0")
	}

	// verify the new comments are on disk
	pr, err := ReadGitHubPR(ledgerPath, 900, now)
	if err != nil {
		t.Fatalf("read PR 900: %v", err)
	}
	if len(pr.Comments) < 2 {
		t.Errorf("expected at least 2 comments after re-sync, got %d", len(pr.Comments))
	}

	// verify KnownItems updated_at was updated
	state, err := ReadGitHubTypeSyncState(ledgerPath, "pr")
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	if !state.KnownItems[900].UpdatedAt.Equal(later) {
		t.Errorf("expected KnownItems updated_at to be %v, got %v", later, state.KnownItems[900].UpdatedAt)
	}
}

func TestSyncIssues_ResyncsWhenUpdatedAtChanges(t *testing.T) {
	// Same as PR test: issue with same state but different updated_at gets re-synced.
	ledgerPath := t.TempDir()
	logger := slog.Default()

	now := time.Now().UTC().Truncate(time.Second)

	// first sync
	fetcher1 := &mockFetcher{
		issues: []FetchedIssue{{
			Number: 950, Title: "Bug", State: "open", Author: "alice",
			CreatedAt: now, UpdatedAt: now,
		}},
		comments: map[int][]FetchedComment{
			950: {{Author: "bob", Body: "confirmed", CreatedAt: now}},
		},
	}
	_, err := SyncIssues(context.Background(), fetcher1, ledgerPath, "org", "repo", 30, logger)
	if err != nil {
		t.Fatalf("first sync: %v", err)
	}

	// second sync: updated_at changed
	later := now.Add(time.Hour)
	commentCalls := 0
	fetcher2 := &countingFetcher{
		inner: &mockFetcher{
			issues: []FetchedIssue{{
				Number: 950, Title: "Bug", State: "open", Author: "alice",
				CreatedAt: now, UpdatedAt: later,
			}},
			comments: map[int][]FetchedComment{
				950: {
					{Author: "bob", Body: "confirmed", CreatedAt: now},
					{Author: "carol", Body: "fix incoming", CreatedAt: later},
				},
			},
		},
		onCommentCall: func() { commentCalls++ },
	}

	result, err := SyncIssues(context.Background(), fetcher2, ledgerPath, "org", "repo", 30, logger)
	if err != nil {
		t.Fatalf("second sync: %v", err)
	}

	if result.IssueUpdated != 1 {
		t.Errorf("expected 1 updated, got %d", result.IssueUpdated)
	}
	if commentCalls == 0 {
		t.Error("expected comment API calls for updated_at change, got 0")
	}
}

// countingFetcher wraps a GitHubFetcher to count comment API calls.
type countingFetcher struct {
	inner         GitHubFetcher
	onCommentCall func()
}

func (f *countingFetcher) ListPullRequests(ctx context.Context, owner, repo string, opts ListPRsOptions) ([]FetchedPR, *FetchRateLimit, error) {
	return f.inner.ListPullRequests(ctx, owner, repo, opts)
}
func (f *countingFetcher) ListIssues(ctx context.Context, owner, repo string, opts ListIssuesOptions) ([]FetchedIssue, *FetchRateLimit, error) {
	return f.inner.ListIssues(ctx, owner, repo, opts)
}
func (f *countingFetcher) ListPRComments(ctx context.Context, owner, repo string, number int) ([]FetchedComment, error) {
	if f.onCommentCall != nil {
		f.onCommentCall()
	}
	return f.inner.ListPRComments(ctx, owner, repo, number)
}
func (f *countingFetcher) ListIssueComments(ctx context.Context, owner, repo string, number int) ([]FetchedComment, error) {
	if f.onCommentCall != nil {
		f.onCommentCall()
	}
	return f.inner.ListIssueComments(ctx, owner, repo, number)
}
func (f *countingFetcher) ListPRCommits(ctx context.Context, owner, repo string, number int) ([]FetchedPRCommit, error) {
	return f.inner.ListPRCommits(ctx, owner, repo, number)
}

// initGitRepo creates a minimal git repo for testing.
func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	cmds := [][]string{
		{"git", "-C", dir, "init", "--initial-branch=main"},
		{"git", "-C", dir, "config", "user.name", "test"},
		{"git", "-C", dir, "config", "user.email", "test@test.com"},
	}
	// create initial commit
	readmePath := filepath.Join(dir, "README.md")
	if err := os.WriteFile(readmePath, []byte("# test\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cmds = append(cmds,
		[]string{"git", "-C", dir, "add", "README.md"},
		[]string{"git", "-C", dir, "commit", "-m", "initial"},
	)
	for _, c := range cmds {
		out, err := runCmd(c[0], c[1:]...)
		if err != nil {
			t.Fatalf("%v failed: %s: %v", c, out, err)
		}
	}
}

func runCmd(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// readIssueFile reads an issue JSON file from the ledger by number and creation date.
func readIssueFile(t *testing.T, ledgerPath string, number int, createdAt time.Time) *IssueFile {
	t.Helper()
	dir := DateDir(ledgerPath, createdAt, "issue")
	path, err := findLatestFile(dir, number)
	if err != nil {
		t.Fatalf("read issue %d: %v", number, err)
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("read issue %d: %v", number, readErr)
	}
	var issue IssueFile
	if err := json.Unmarshal(data, &issue); err != nil {
		t.Fatalf("unmarshal issue %d: %v", number, err)
	}
	return &issue
}
