package index

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/ledger"
)

// --- IndexGitHubData edge cases ---

func TestIndexGitHubData_EmptyLedgerPath(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git indexing")
	}
	s := openTestStore(t)
	stats, err := IndexGitHubData(context.Background(), s, "", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stats.PRsIndexed != 0 || stats.IssuesIndexed != 0 {
		t.Errorf("expected zero stats for empty ledger path, got prs=%d issues=%d",
			stats.PRsIndexed, stats.IssuesIndexed)
	}
}

func TestIndexGitHubData_IssueIndexing(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git indexing")
	}
	s := openTestStore(t)

	ledgerPath := t.TempDir()
	now := time.Now().UTC().Truncate(time.Second)
	closedAt := now.Add(24 * time.Hour)

	issue := &ledger.IssueFile{
		Number:    10,
		Title:     "Bug report",
		Body:      "Something is broken",
		Author:    "alice",
		State:     "closed",
		Labels:    []string{"bug", "priority-high"},
		CreatedAt: now,
		ClosedAt:  &closedAt,
		UpdatedAt: closedAt,
		URL:       "https://github.com/org/repo/issues/10",
		Comments: []ledger.IssueComment{
			{Author: "bob", Body: "I can reproduce this", CreatedAt: now},
			{Author: "alice", Body: "Fixed in PR #11", CreatedAt: closedAt},
		},
	}
	if err := ledger.WriteGitHubIssue(ledgerPath, issue); err != nil {
		t.Fatalf("write issue: %v", err)
	}

	stats, err := IndexGitHubData(context.Background(), s, ledgerPath, nil)
	if err != nil {
		t.Fatalf("IndexGitHubData: %v", err)
	}
	if stats.IssuesIndexed != 1 {
		t.Errorf("expected 1 issue indexed, got %d", stats.IssuesIndexed)
	}

	// verify issue record
	var title, author, state string
	err = s.QueryRow("SELECT title, author, state FROM issues WHERE number = 10").Scan(&title, &author, &state)
	if err != nil {
		t.Fatalf("query issue: %v", err)
	}
	if title != "Bug report" || author != "alice" || state != "closed" {
		t.Errorf("unexpected issue data: title=%q author=%q state=%q", title, author, state)
	}

	// verify comments
	var commentCount int
	var issueID int64
	if err := s.QueryRow("SELECT id FROM issues WHERE number = 10").Scan(&issueID); err != nil {
		t.Fatalf("get issue id: %v", err)
	}
	if err := s.QueryRow("SELECT COUNT(*) FROM issue_comments WHERE issue_id = ?", issueID).Scan(&commentCount); err != nil {
		t.Fatalf("count comments: %v", err)
	}
	if commentCount != 2 {
		t.Errorf("expected 2 comments, got %d", commentCount)
	}
}

func TestIndexGitHubData_IssueUpsert(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git indexing")
	}
	s := openTestStore(t)

	ledgerPath := t.TempDir()
	now := time.Now().UTC().Truncate(time.Second)

	// index issue initially
	issue := &ledger.IssueFile{
		Number:    20,
		Title:     "Original title",
		Author:    "alice",
		State:     "open",
		CreatedAt: now,
		UpdatedAt: now,
		URL:       "https://github.com/org/repo/issues/20",
		Comments: []ledger.IssueComment{
			{Author: "bob", Body: "comment 1", CreatedAt: now},
		},
	}
	if err := ledger.WriteGitHubIssue(ledgerPath, issue); err != nil {
		t.Fatalf("write issue: %v", err)
	}
	if _, err := IndexGitHubData(context.Background(), s, ledgerPath, nil); err != nil {
		t.Fatalf("first index: %v", err)
	}

	// update and re-index (touch mtime)
	time.Sleep(10 * time.Millisecond)
	issue.Title = "Updated title"
	issue.State = "closed"
	issue.Comments = []ledger.IssueComment{
		{Author: "bob", Body: "comment 1", CreatedAt: now},
		{Author: "alice", Body: "comment 2", CreatedAt: now},
		{Author: "carol", Body: "comment 3", CreatedAt: now},
	}
	if err := ledger.WriteGitHubIssue(ledgerPath, issue); err != nil {
		t.Fatalf("write updated issue: %v", err)
	}
	stats, err := IndexGitHubData(context.Background(), s, ledgerPath, nil)
	if err != nil {
		t.Fatalf("second index: %v", err)
	}
	if stats.IssuesIndexed != 1 {
		t.Errorf("expected 1 issue re-indexed, got %d", stats.IssuesIndexed)
	}

	// verify updated data
	var title, state string
	if err := s.QueryRow("SELECT title, state FROM issues WHERE number = 20").Scan(&title, &state); err != nil {
		t.Fatalf("query: %v", err)
	}
	if title != "Updated title" || state != "closed" {
		t.Errorf("expected updated title/state, got %q/%q", title, state)
	}

	var issueID int64
	if err := s.QueryRow("SELECT id FROM issues WHERE number = 20").Scan(&issueID); err != nil {
		t.Fatalf("get issue id: %v", err)
	}
	var commentCount int
	if err := s.QueryRow("SELECT COUNT(*) FROM issue_comments WHERE issue_id = ?", issueID).Scan(&commentCount); err != nil {
		t.Fatalf("count comments: %v", err)
	}
	if commentCount != 3 {
		t.Errorf("expected 3 comments after upsert, got %d", commentCount)
	}
}

func TestIndexGitHubData_IncrementalSkip(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git indexing")
	}
	s := openTestStore(t)

	ledgerPath := t.TempDir()
	now := time.Now().UTC().Truncate(time.Second)
	pr := &ledger.PRFile{
		Number: 50, Title: "PR", Author: "alice", State: "open",
		CreatedAt: now, UpdatedAt: now, URL: "https://github.com/org/repo/pull/50",
	}
	if err := ledger.WriteGitHubPR(ledgerPath, pr); err != nil {
		t.Fatalf("write PR: %v", err)
	}

	// first index
	stats1, err := IndexGitHubData(context.Background(), s, ledgerPath, nil)
	if err != nil {
		t.Fatalf("first index: %v", err)
	}
	if stats1.PRsIndexed != 1 {
		t.Errorf("expected 1 PR on first index, got %d", stats1.PRsIndexed)
	}

	// second index without changing file — should be skipped
	stats2, err := IndexGitHubData(context.Background(), s, ledgerPath, nil)
	if err != nil {
		t.Fatalf("second index: %v", err)
	}
	if stats2.PRsIndexed != 0 {
		t.Errorf("expected 0 PRs on unchanged re-index, got %d", stats2.PRsIndexed)
	}
}

func TestIndexGitHubData_ContextCancellation(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git indexing")
	}
	s := openTestStore(t)

	ledgerPath := t.TempDir()
	now := time.Now().UTC().Truncate(time.Second)
	// write multiple PRs to increase chance of hitting cancellation
	for i := 1; i <= 5; i++ {
		pr := &ledger.PRFile{
			Number:    i,
			Title:     fmt.Sprintf("PR %d", i),
			Author:    "alice",
			State:     "open",
			CreatedAt: now.Add(time.Duration(i) * time.Hour),
			UpdatedAt: now,
			URL:       fmt.Sprintf("https://github.com/org/repo/pull/%d", i),
		}
		if err := ledger.WriteGitHubPR(ledgerPath, pr); err != nil {
			t.Fatalf("write PR %d: %v", i, err)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	_, err := IndexGitHubData(ctx, s, ledgerPath, nil)
	if err == nil {
		// context cancellation may or may not trigger depending on timing;
		// the important thing is it doesn't panic
		return
	}
	if err != context.Canceled {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

func TestIndexGitHubData_ProgressCallback(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git indexing")
	}
	s := openTestStore(t)

	ledgerPath := t.TempDir()
	now := time.Now().UTC().Truncate(time.Second)
	pr := &ledger.PRFile{
		Number: 60, Title: "PR", Author: "alice", State: "open",
		CreatedAt: now, UpdatedAt: now, URL: "https://github.com/org/repo/pull/60",
	}
	if err := ledger.WriteGitHubPR(ledgerPath, pr); err != nil {
		t.Fatalf("write PR: %v", err)
	}

	var messages []string
	progress := func(msg string) {
		messages = append(messages, msg)
	}

	_, err := IndexGitHubData(context.Background(), s, ledgerPath, progress)
	if err != nil {
		t.Fatalf("IndexGitHubData: %v", err)
	}
	if len(messages) == 0 {
		t.Error("expected at least one progress message")
	}
}

func TestIndexGitHubData_InvalidJSON(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git indexing")
	}
	s := openTestStore(t)

	ledgerPath := t.TempDir()
	now := time.Now().UTC()
	dir := filepath.Join(ledgerPath, "data", "github",
		now.Format("2006"), now.Format("01"), now.Format("02"), "pr")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "1.json"), []byte("not valid json"), 0o644); err != nil {
		t.Fatal(err)
	}

	// invalid JSON should be skipped (logged warning), not cause an error
	stats, err := IndexGitHubData(context.Background(), s, ledgerPath, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stats.PRsIndexed != 0 {
		t.Errorf("expected 0 PRs indexed for invalid JSON, got %d", stats.PRsIndexed)
	}
}

func TestIndexGitHubData_MixedPRsAndIssues(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git indexing")
	}
	s := openTestStore(t)

	ledgerPath := t.TempDir()
	now := time.Now().UTC().Truncate(time.Second)

	// write 2 PRs
	for i := 1; i <= 2; i++ {
		pr := &ledger.PRFile{
			Number: i, Title: fmt.Sprintf("PR %d", i), Author: "alice", State: "open",
			CreatedAt: now, UpdatedAt: now,
			URL: fmt.Sprintf("https://github.com/org/repo/pull/%d", i),
		}
		if err := ledger.WriteGitHubPR(ledgerPath, pr); err != nil {
			t.Fatalf("write PR %d: %v", i, err)
		}
	}

	// write 3 issues
	for i := 1; i <= 3; i++ {
		issue := &ledger.IssueFile{
			Number: i, Title: fmt.Sprintf("Issue %d", i), Author: "bob", State: "open",
			CreatedAt: now, UpdatedAt: now,
			URL: fmt.Sprintf("https://github.com/org/repo/issues/%d", i),
		}
		if err := ledger.WriteGitHubIssue(ledgerPath, issue); err != nil {
			t.Fatalf("write issue %d: %v", i, err)
		}
	}

	stats, err := IndexGitHubData(context.Background(), s, ledgerPath, nil)
	if err != nil {
		t.Fatalf("IndexGitHubData: %v", err)
	}
	if stats.PRsIndexed != 2 {
		t.Errorf("expected 2 PRs indexed, got %d", stats.PRsIndexed)
	}
	if stats.IssuesIndexed != 3 {
		t.Errorf("expected 3 issues indexed, got %d", stats.IssuesIndexed)
	}
}
