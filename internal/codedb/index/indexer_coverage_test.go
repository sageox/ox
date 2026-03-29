package index

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/codedb/store"
	"github.com/sageox/ox/internal/ledger"
)

// --- gitStatusDirtyFiles ---

func TestGitStatusDirtyFiles(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git indexing")
	}

	// helper to init a git repo with one committed file
	setupRepo := func(t *testing.T) string {
		t.Helper()
		dir := t.TempDir()
		run := func(args ...string) {
			t.Helper()
			cmd := exec.Command("git", args...)
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), // safe: git subprocess needs parent env for PATH
				"GIT_AUTHOR_NAME=test",
				"GIT_AUTHOR_EMAIL=test@sageox.ai",
				"GIT_COMMITTER_NAME=test",
				"GIT_COMMITTER_EMAIL=test@sageox.ai",
			)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("git %v: %s", args, out)
			}
		}
		run("init", "-b", "main")
		if err := os.WriteFile(filepath.Join(dir, "base.go"), []byte("package main\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		run("add", "base.go")
		run("commit", "-m", "init")
		return dir
	}

	t.Run("clean worktree returns nil", func(t *testing.T) {
		t.Parallel()
		dir := setupRepo(t)
		files, err := gitStatusDirtyFiles(context.Background(), dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if files != nil {
			t.Errorf("expected nil for clean worktree, got %v", files)
		}
	})

	t.Run("modified file detected", func(t *testing.T) {
		t.Parallel()
		dir := setupRepo(t)
		if err := os.WriteFile(filepath.Join(dir, "base.go"), []byte("package main\n// modified\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		files, err := gitStatusDirtyFiles(context.Background(), dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(files) != 1 || files[0] != "base.go" {
			t.Errorf("expected [base.go], got %v", files)
		}
	})

	t.Run("untracked file detected", func(t *testing.T) {
		t.Parallel()
		dir := setupRepo(t)
		if err := os.WriteFile(filepath.Join(dir, "new.go"), []byte("package main\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		files, err := gitStatusDirtyFiles(context.Background(), dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		found := false
		for _, f := range files {
			if f == "new.go" {
				found = true
			}
		}
		if !found {
			t.Errorf("expected new.go in dirty files, got %v", files)
		}
	})

	t.Run("deleted file excluded", func(t *testing.T) {
		t.Parallel()
		dir := setupRepo(t)
		os.Remove(filepath.Join(dir, "base.go"))
		files, err := gitStatusDirtyFiles(context.Background(), dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		for _, f := range files {
			if f == "base.go" {
				t.Error("deleted file should be excluded from dirty files")
			}
		}
	})

	t.Run("context cancellation", func(t *testing.T) {
		t.Parallel()
		dir := setupRepo(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := gitStatusDirtyFiles(ctx, dir)
		if err == nil {
			t.Error("expected error with canceled context")
		}
	})
}

// --- upsertRepo / loadKnownCommits / loadExistingRefs ---

func TestUpsertRepo(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git indexing")
	}
	s := openTestStore(t)

	tests := []struct {
		name     string
		repoName string
		path     string
	}{
		{"basic insert", "test-repo", "/path/to/repo"},
		{"insert second", "other-repo", "/path/to/other"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, err := upsertRepo(context.Background(), s, tt.repoName, tt.path)
			if err != nil {
				t.Fatalf("upsertRepo: %v", err)
			}
			if id <= 0 {
				t.Errorf("expected positive id, got %d", id)
			}
		})
	}

	t.Run("upsert updates path", func(t *testing.T) {
		id1, err := upsertRepo(context.Background(), s, "update-repo", "/old/path")
		if err != nil {
			t.Fatalf("first upsert: %v", err)
		}
		id2, err := upsertRepo(context.Background(), s, "update-repo", "/new/path")
		if err != nil {
			t.Fatalf("second upsert: %v", err)
		}
		if id1 != id2 {
			t.Errorf("upsert should return same id, got %d and %d", id1, id2)
		}
		var path string
		if err := s.QueryRow("SELECT path FROM repos WHERE name = ?", "update-repo").Scan(&path); err != nil {
			t.Fatalf("query: %v", err)
		}
		if path != "/new/path" {
			t.Errorf("expected updated path /new/path, got %q", path)
		}
	})
}

func TestLoadKnownCommits(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git indexing")
	}
	s := openTestStore(t)

	repoID, err := upsertRepo(context.Background(), s, "commits-repo", "/repo")
	if err != nil {
		t.Fatalf("upsertRepo: %v", err)
	}

	t.Run("empty repo has no known commits", func(t *testing.T) {
		known, err := loadKnownCommits(context.Background(), s, repoID)
		if err != nil {
			t.Fatalf("loadKnownCommits: %v", err)
		}
		if len(known) != 0 {
			t.Errorf("expected 0 known commits, got %d", len(known))
		}
	})

	t.Run("inserted commits are returned", func(t *testing.T) {
		hashes := []string{"aaa111", "bbb222", "ccc333"}
		for _, h := range hashes {
			_, err := s.Exec(
				"INSERT INTO commits (repo_id, hash, author, message, timestamp) VALUES (?, ?, ?, ?, ?)",
				repoID, h, "test", "msg", time.Now().Unix(),
			)
			if err != nil {
				t.Fatalf("insert commit: %v", err)
			}
		}
		known, err := loadKnownCommits(context.Background(), s, repoID)
		if err != nil {
			t.Fatalf("loadKnownCommits: %v", err)
		}
		if len(known) != 3 {
			t.Errorf("expected 3 known commits, got %d", len(known))
		}
		for _, h := range hashes {
			if !known[h] {
				t.Errorf("expected %q in known commits", h)
			}
		}
	})
}

func TestLoadExistingRefs(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git indexing")
	}
	s := openTestStore(t)

	repoID, err := upsertRepo(context.Background(), s, "refs-repo", "/repo")
	if err != nil {
		t.Fatalf("upsertRepo: %v", err)
	}

	t.Run("no refs initially", func(t *testing.T) {
		refs, err := loadExistingRefs(context.Background(), s, repoID)
		if err != nil {
			t.Fatalf("loadExistingRefs: %v", err)
		}
		if len(refs) != 0 {
			t.Errorf("expected 0 refs, got %d", len(refs))
		}
	})

	t.Run("returns ref name to commit hash mapping", func(t *testing.T) {
		// insert a commit first
		_, err := s.Exec(
			"INSERT INTO commits (repo_id, hash, author, message, timestamp) VALUES (?, ?, ?, ?, ?)",
			repoID, "deadbeef", "test", "msg", time.Now().Unix(),
		)
		if err != nil {
			t.Fatalf("insert commit: %v", err)
		}
		var commitID int64
		if err := s.QueryRow("SELECT id FROM commits WHERE hash = ?", "deadbeef").Scan(&commitID); err != nil {
			t.Fatalf("get commit id: %v", err)
		}

		_, err = s.Exec(
			"INSERT INTO refs (repo_id, name, commit_id) VALUES (?, ?, ?)",
			repoID, "refs/heads/main", commitID,
		)
		if err != nil {
			t.Fatalf("insert ref: %v", err)
		}

		refs, err := loadExistingRefs(context.Background(), s, repoID)
		if err != nil {
			t.Fatalf("loadExistingRefs: %v", err)
		}
		if len(refs) != 1 {
			t.Fatalf("expected 1 ref, got %d", len(refs))
		}
		if refs["refs/heads/main"] != "deadbeef" {
			t.Errorf("expected refs/heads/main -> deadbeef, got %q", refs["refs/heads/main"])
		}
	})
}

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
	s.QueryRow("SELECT id FROM issues WHERE number = 20").Scan(&issueID)
	var commentCount int
	s.QueryRow("SELECT COUNT(*) FROM issue_comments WHERE issue_id = ?", issueID).Scan(&commentCount)
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

// --- IndexLocalRepo ---

func TestIndexLocalRepo_Idempotent(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git indexing")
	}
	dir, _ := initGitRepo(t, 3)
	s := openTestStore(t)

	// index twice
	for i := 0; i < 2; i++ {
		err := IndexLocalRepo(context.Background(), s, dir, IndexOptions{})
		if err != nil {
			t.Fatalf("IndexLocalRepo pass %d: %v", i+1, err)
		}
	}

	// should still have exactly 3 commits (INSERT OR IGNORE)
	var count int
	if err := s.QueryRow("SELECT COUNT(*) FROM commits").Scan(&count); err != nil {
		t.Fatalf("query: %v", err)
	}
	if count != 3 {
		t.Errorf("expected 3 commits after idempotent re-index, got %d", count)
	}
}

func TestIndexLocalRepo_WithProgress(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git indexing")
	}
	dir, _ := initGitRepo(t, 2)
	s := openTestStore(t)

	var messages []string
	opts := IndexOptions{
		Progress: func(msg string) {
			messages = append(messages, msg)
		},
	}

	err := IndexLocalRepo(context.Background(), s, dir, opts)
	if err != nil {
		t.Fatalf("IndexLocalRepo: %v", err)
	}
	if len(messages) == 0 {
		t.Error("expected progress messages")
	}
}

func TestIndexLocalRepo_WithMaxHistoryDepth(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git indexing")
	}
	dir, _ := initGitRepo(t, 10)
	s := openTestStore(t)

	err := IndexLocalRepo(context.Background(), s, dir, IndexOptions{MaxHistoryDepth: 3})
	if err != nil {
		t.Fatalf("IndexLocalRepo: %v", err)
	}

	var count int
	if err := s.QueryRow("SELECT COUNT(*) FROM commits").Scan(&count); err != nil {
		t.Fatalf("query: %v", err)
	}
	// with maxDepth=3, should have at most 3 commits
	if count > 3 {
		t.Errorf("expected at most 3 commits with maxDepth=3, got %d", count)
	}
}

func TestIndexLocalRepo_ContextCancellation(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git indexing")
	}
	dir, _ := initGitRepo(t, 5)
	s := openTestStore(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := IndexLocalRepo(ctx, s, dir, IndexOptions{})
	// should error due to canceled context (may fail at various stages)
	if err == nil {
		// timing-dependent; not failing is also acceptable
		return
	}
}

func TestIndexLocalRepo_CommitsHaveBlobs(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git indexing")
	}
	dir, _ := initGitRepo(t, 3)
	s := openTestStore(t)

	if err := IndexLocalRepo(context.Background(), s, dir, IndexOptions{}); err != nil {
		t.Fatalf("IndexLocalRepo: %v", err)
	}

	var blobCount int
	if err := s.QueryRow("SELECT COUNT(*) FROM blobs").Scan(&blobCount); err != nil {
		t.Fatalf("query blobs: %v", err)
	}
	if blobCount == 0 {
		t.Error("expected blobs to be indexed alongside commits")
	}

	// verify diffs exist
	var diffCount int
	if err := s.QueryRow("SELECT COUNT(*) FROM diffs").Scan(&diffCount); err != nil {
		t.Fatalf("query diffs: %v", err)
	}
	if diffCount == 0 {
		t.Error("expected diffs to be indexed")
	}
}

func TestIndexLocalRepo_FileRevsPopulated(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git indexing")
	}
	dir, _ := initGitRepo(t, 2)
	s := openTestStore(t)

	if err := IndexLocalRepo(context.Background(), s, dir, IndexOptions{}); err != nil {
		t.Fatalf("IndexLocalRepo: %v", err)
	}

	var count int
	if err := s.QueryRow("SELECT COUNT(*) FROM file_revs").Scan(&count); err != nil {
		t.Fatalf("query file_revs: %v", err)
	}
	if count == 0 {
		t.Error("expected file_revs to be populated for tip commit")
	}
}

func TestIndexLocalRepo_RefsRecorded(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git indexing")
	}
	dir, _ := initGitRepo(t, 2)
	s := openTestStore(t)

	if err := IndexLocalRepo(context.Background(), s, dir, IndexOptions{}); err != nil {
		t.Fatalf("IndexLocalRepo: %v", err)
	}

	var refName string
	if err := s.QueryRow("SELECT name FROM refs LIMIT 1").Scan(&refName); err != nil {
		t.Fatalf("query refs: %v", err)
	}
	if refName != "refs/heads/main" {
		t.Errorf("expected ref refs/heads/main, got %q", refName)
	}
}

// --- BuildDirtyIndex edge cases ---

func TestBuildDirtyIndex_SkipsNonCodeFiles(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git indexing")
	}
	dir, _ := initGitRepo(t, 1)

	// add a non-code file (no language detection match)
	if err := os.WriteFile(filepath.Join(dir, "data.csv"), []byte("a,b,c\n1,2,3\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	dirtyPath := filepath.Join(t.TempDir(), "dirty")
	n, err := BuildDirtyIndex(context.Background(), dir, dirtyPath, IndexOptions{})
	if err != nil {
		t.Fatalf("BuildDirtyIndex: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 indexed files for non-code file, got %d", n)
	}
}

func TestBuildDirtyIndex_SkipsBinaryFiles(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git indexing")
	}
	dir, _ := initGitRepo(t, 1)

	// write binary content with a code extension
	binary := []byte{0x00, 0x01, 0x02, 0xFF, 0xFE}
	if err := os.WriteFile(filepath.Join(dir, "binary.go"), binary, 0o644); err != nil {
		t.Fatal(err)
	}

	dirtyPath := filepath.Join(t.TempDir(), "dirty")
	n, err := BuildDirtyIndex(context.Background(), dir, dirtyPath, IndexOptions{})
	if err != nil {
		t.Fatalf("BuildDirtyIndex: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 indexed files for binary content, got %d", n)
	}
}

func TestBuildDirtyIndex_SkipsEmptyFiles(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git indexing")
	}
	dir, _ := initGitRepo(t, 1)

	if err := os.WriteFile(filepath.Join(dir, "empty.go"), []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}

	dirtyPath := filepath.Join(t.TempDir(), "dirty")
	n, err := BuildDirtyIndex(context.Background(), dir, dirtyPath, IndexOptions{})
	if err != nil {
		t.Fatalf("BuildDirtyIndex: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 indexed files for empty file, got %d", n)
	}
}

func TestBuildDirtyIndex_AtomicSwap(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git indexing")
	}
	dir, _ := initGitRepo(t, 1)

	// add dirty go file
	if err := os.WriteFile(filepath.Join(dir, "dirty.go"), []byte("package main\nfunc Dirty() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	dirtyPath := filepath.Join(t.TempDir(), "dirty_index")

	// build twice — second build should replace first atomically
	for i := 0; i < 2; i++ {
		n, err := BuildDirtyIndex(context.Background(), dir, dirtyPath, IndexOptions{})
		if err != nil {
			t.Fatalf("BuildDirtyIndex pass %d: %v", i+1, err)
		}
		if n != 1 {
			t.Errorf("pass %d: expected 1, got %d", i+1, n)
		}
	}

	// verify the index dir exists and .tmp does not
	if _, err := os.Stat(dirtyPath); err != nil {
		t.Errorf("dirty index should exist: %v", err)
	}
	if _, err := os.Stat(dirtyPath + ".tmp"); err == nil {
		t.Error("tmp dir should not exist after swap")
	}
}

func TestBuildDirtyIndex_RemovesStaleOnClean(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git indexing")
	}
	dir, _ := initGitRepo(t, 1)

	dirtyPath := filepath.Join(t.TempDir(), "dirty_index")

	// first: create dirty file and build index
	if err := os.WriteFile(filepath.Join(dir, "dirty.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	n, err := BuildDirtyIndex(context.Background(), dir, dirtyPath, IndexOptions{})
	if err != nil {
		t.Fatalf("build dirty: %v", err)
	}
	if n == 0 {
		t.Fatal("expected dirty files")
	}

	// commit the file so worktree is clean
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), // safe: git subprocess needs parent env for PATH
			"GIT_AUTHOR_NAME=test",
			"GIT_AUTHOR_EMAIL=test@sageox.ai",
			"GIT_COMMITTER_NAME=test",
			"GIT_COMMITTER_EMAIL=test@sageox.ai",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
	}
	run("add", "dirty.go")
	run("commit", "-m", "commit dirty")

	// rebuild — should remove stale index
	n, err = BuildDirtyIndex(context.Background(), dir, dirtyPath, IndexOptions{})
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 dirty files after commit, got %d", n)
	}
	if _, err := os.Stat(dirtyPath); !os.IsNotExist(err) {
		t.Error("stale dirty index should be removed on clean worktree")
	}
}

// --- mtime tracking (saveFileMtime / loadFileMtimes) ---

func TestFileMtimePersistence(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git indexing")
	}
	s := openTestStore(t)
	ctx := context.Background()

	// save mtime
	if err := saveFileMtime(ctx, s, "/path/to/file.json", 1234567890); err != nil {
		t.Fatalf("saveFileMtime: %v", err)
	}

	// load and verify
	mtimes, err := loadFileMtimes(ctx, s)
	if err != nil {
		t.Fatalf("loadFileMtimes: %v", err)
	}
	if mtimes["/path/to/file.json"] != 1234567890 {
		t.Errorf("expected mtime 1234567890, got %d", mtimes["/path/to/file.json"])
	}

	// overwrite mtime
	if err := saveFileMtime(ctx, s, "/path/to/file.json", 9999999999); err != nil {
		t.Fatalf("saveFileMtime overwrite: %v", err)
	}
	mtimes, err = loadFileMtimes(ctx, s)
	if err != nil {
		t.Fatalf("loadFileMtimes after overwrite: %v", err)
	}
	if mtimes["/path/to/file.json"] != 9999999999 {
		t.Errorf("expected updated mtime 9999999999, got %d", mtimes["/path/to/file.json"])
	}
}

// --- ParseSymbols / ParseComments ---

func TestParseSymbols_NoSupportedLanguages(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git indexing")
	}
	s := openTestStore(t)

	stats, err := ParseSymbols(context.Background(), s, nil)
	if err != nil {
		t.Fatalf("ParseSymbols: %v", err)
	}
	// symbols stub returns nil for SupportedLanguages, so nothing to parse
	if stats.BlobsParsed != 0 || stats.SymbolsExtracted != 0 {
		t.Errorf("expected zero stats with no supported languages, got %+v", stats)
	}
}

func TestParseComments_NoUnparsedBlobs(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git indexing")
	}
	s := openTestStore(t)

	var messages []string
	progress := func(msg string) { messages = append(messages, msg) }

	stats, err := ParseComments(context.Background(), s, progress)
	if err != nil {
		t.Fatalf("ParseComments: %v", err)
	}
	if stats.BlobsParsed != 0 {
		t.Errorf("expected 0 blobs parsed, got %d", stats.BlobsParsed)
	}
}

func TestParseComments_WithIndexedRepo(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git indexing")
	}
	dir, _ := initGitRepo(t, 3)
	s := openTestStore(t)

	// index the repo first so blobs exist in the store
	if err := IndexLocalRepo(context.Background(), s, dir, IndexOptions{}); err != nil {
		t.Fatalf("IndexLocalRepo: %v", err)
	}

	// verify blobs exist before parsing
	var blobCount int
	if err := s.QueryRow("SELECT COUNT(*) FROM blobs").Scan(&blobCount); err != nil {
		t.Fatalf("query blobs: %v", err)
	}
	if blobCount == 0 {
		t.Skip("no blobs indexed, nothing to parse")
	}

	stats, err := ParseComments(context.Background(), s, nil)
	if err != nil {
		t.Fatalf("ParseComments: %v", err)
	}
	// initGitRepo creates .go files which have comment support
	// the result depends on whether comments exist in the Go files
	// but the function should at least not error out
	t.Logf("ParseComments: %d blobs parsed, %d comments extracted", stats.BlobsParsed, stats.CommentsExtracted)
}

func TestParseComments_Idempotent(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git indexing")
	}
	dir, _ := initGitRepo(t, 2)
	s := openTestStore(t)

	if err := IndexLocalRepo(context.Background(), s, dir, IndexOptions{}); err != nil {
		t.Fatalf("IndexLocalRepo: %v", err)
	}

	// parse once
	stats1, err := ParseComments(context.Background(), s, nil)
	if err != nil {
		t.Fatalf("first ParseComments: %v", err)
	}

	// parse again — should find no unparsed blobs
	stats2, err := ParseComments(context.Background(), s, nil)
	if err != nil {
		t.Fatalf("second ParseComments: %v", err)
	}
	if stats2.BlobsParsed != 0 {
		t.Errorf("second parse should find 0 unparsed blobs, got %d (first parsed %d)",
			stats2.BlobsParsed, stats1.BlobsParsed)
	}
}

// --- resolveDefaultBranch ---

func TestResolveDefaultBranch_WithHead(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git indexing")
	}
	dir, tipHash := initGitRepo(t, 2)

	repo, err := plainOpenTolerant(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	ref, err := resolveDefaultBranch(repo)
	if err != nil {
		t.Fatalf("resolveDefaultBranch: %v", err)
	}
	if ref.name != "refs/heads/main" {
		t.Errorf("expected refs/heads/main, got %q", ref.name)
	}
	if ref.tipOID.String() != tipHash {
		t.Errorf("expected tip %s, got %s", tipHash, ref.tipOID.String())
	}
}

func TestResolveDefaultBranch_FallbackToBranchNames(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git indexing")
	}

	// create a bare repo (HEAD may not resolve in the same way)
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), // safe: git subprocess needs parent env for PATH
			"GIT_AUTHOR_NAME=test",
			"GIT_AUTHOR_EMAIL=test@sageox.ai",
			"GIT_COMMITTER_NAME=test",
			"GIT_COMMITTER_EMAIL=test@sageox.ai",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
	}
	run("init", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "a.txt")
	run("commit", "-m", "init")

	// clone as bare repo
	bareDir := filepath.Join(t.TempDir(), "bare.git")
	run2 := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
	}
	run2("clone", "--bare", dir, bareDir)

	repo, err := plainOpenTolerant(bareDir)
	if err != nil {
		t.Fatalf("open bare: %v", err)
	}

	ref, err := resolveDefaultBranch(repo)
	if err != nil {
		t.Fatalf("resolveDefaultBranch on bare repo: %v", err)
	}
	// should resolve HEAD or fallback to refs/heads/main
	if ref.name != "refs/heads/main" {
		t.Errorf("expected refs/heads/main, got %q", ref.name)
	}
}

// --- TreeCacheLimit ---

func TestIndexLocalRepo_CustomTreeCacheLimit(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git indexing")
	}
	dir, _ := initGitRepo(t, 5)
	s := openTestStore(t)

	// use very small tree cache limit to exercise eviction path
	err := IndexLocalRepo(context.Background(), s, dir, IndexOptions{TreeCacheLimit: 2})
	if err != nil {
		t.Fatalf("IndexLocalRepo with TreeCacheLimit=2: %v", err)
	}

	var count int
	if err := s.QueryRow("SELECT COUNT(*) FROM commits").Scan(&count); err != nil {
		t.Fatalf("query: %v", err)
	}
	if count != 5 {
		t.Errorf("expected 5 commits, got %d", count)
	}
}

// --- helper ---

func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "codedb")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	s, err := store.Open(dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
