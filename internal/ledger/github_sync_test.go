package ledger

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// mockFetcher implements GitHubFetcher for testing.
type mockFetcher struct {
	prs      []FetchedPR
	issues   []FetchedIssue
	comments map[int][]FetchedComment // keyed by issue/PR number
	prRL     *FetchRateLimit
	issueRL  *FetchRateLimit
	err      error
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
	if len(state.KnownStates) != 2 {
		t.Errorf("expected 2 known states, got %d", len(state.KnownStates))
	}
	if state.KnownStates[100] != "open" {
		t.Errorf("expected PR 100 state 'open', got %q", state.KnownStates[100])
	}
	if state.KnownStates[101] != "merged" {
		t.Errorf("expected PR 101 state 'merged', got %q", state.KnownStates[101])
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
	if state.KnownStates[100] != "merged" {
		t.Errorf("expected state 'merged', got %q", state.KnownStates[100])
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
