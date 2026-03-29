package index

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/codedb/store"
	"github.com/sageox/ox/internal/ledger"
)

func TestIndexGitHubData_PRWithCommits(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git indexing")
	}

	// set up CodeDB store
	storeDir := t.TempDir()
	s, err := store.Open(storeDir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	// set up ledger with a PR that has commits
	ledgerPath := t.TempDir()
	now := time.Now().UTC().Truncate(time.Second)
	pr := &ledger.PRFile{
		Number:      42,
		Title:       "Add feature X",
		Body:        "description",
		Author:      "alice",
		State:       "merged",
		Labels:      []string{"enhancement"},
		CreatedAt:   now,
		MergedAt:    &now,
		UpdatedAt:   now,
		MergeCommit: "merge123",
		URL:         "https://github.com/org/repo/pull/42",
		Comments: []ledger.PRComment{
			{Author: "bob", Body: "LGTM", CreatedAt: now},
		},
		Commits: []ledger.PRCommit{
			{SHA: "aaa111", Author: "alice", Date: now.Add(-2 * time.Hour), Msg: "first commit"},
			{SHA: "bbb222", Author: "alice", Date: now.Add(-1 * time.Hour), Msg: "second commit"},
			{SHA: "ccc333", Author: "bob", Date: now, Msg: "review fix"},
		},
	}
	if err := ledger.WriteGitHubPR(ledgerPath, pr); err != nil {
		t.Fatalf("write PR: %v", err)
	}

	// index
	stats, err := IndexGitHubData(context.Background(), s, ledgerPath, nil)
	if err != nil {
		t.Fatalf("IndexGitHubData: %v", err)
	}
	if stats.PRsIndexed != 1 {
		t.Errorf("expected 1 PR indexed, got %d", stats.PRsIndexed)
	}

	// verify PR was indexed
	var prID int64
	var title string
	err = s.QueryRow("SELECT id, title FROM pull_requests WHERE number = 42").Scan(&prID, &title)
	if err != nil {
		t.Fatalf("query PR: %v", err)
	}
	if title != "Add feature X" {
		t.Errorf("expected title 'Add feature X', got %q", title)
	}

	// verify commits were indexed
	rows, err := s.Query("SELECT sha FROM pr_commits WHERE pr_id = ? ORDER BY rowid", prID)
	if err != nil {
		t.Fatalf("query commits: %v", err)
	}
	defer rows.Close()

	var shas []string
	for rows.Next() {
		var sha string
		if err := rows.Scan(&sha); err != nil {
			t.Fatalf("scan commit: %v", err)
		}
		shas = append(shas, sha)
	}

	if len(shas) != 3 {
		t.Fatalf("expected 3 commits, got %d", len(shas))
	}
	if shas[0] != "aaa111" {
		t.Errorf("expected first commit SHA 'aaa111', got %q", shas[0])
	}
	if shas[2] != "ccc333" {
		t.Errorf("expected third commit SHA 'ccc333', got %q", shas[2])
	}

	// verify comments were also indexed
	var commentCount int
	if err := s.QueryRow("SELECT COUNT(*) FROM pr_comments WHERE pr_id = ?", prID).Scan(&commentCount); err != nil {
		t.Fatalf("query comment count: %v", err)
	}
	if commentCount != 1 {
		t.Errorf("expected 1 comment, got %d", commentCount)
	}
}

func TestIndexGitHubData_PRWithoutCommits(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git indexing")
	}

	storeDir := t.TempDir()
	s, err := store.Open(storeDir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	ledgerPath := t.TempDir()
	now := time.Now().UTC().Truncate(time.Second)
	pr := &ledger.PRFile{
		Number:    43,
		Title:     "Open PR",
		Author:    "bob",
		State:     "open",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := ledger.WriteGitHubPR(ledgerPath, pr); err != nil {
		t.Fatalf("write PR: %v", err)
	}

	stats, err := IndexGitHubData(context.Background(), s, ledgerPath, nil)
	if err != nil {
		t.Fatalf("IndexGitHubData: %v", err)
	}
	if stats.PRsIndexed != 1 {
		t.Errorf("expected 1 PR indexed, got %d", stats.PRsIndexed)
	}

	// verify no commits indexed
	var prID int64
	if err := s.QueryRow("SELECT id FROM pull_requests WHERE number = 43").Scan(&prID); err != nil {
		t.Fatalf("query PR id: %v", err)
	}
	var count int
	if err := s.QueryRow("SELECT COUNT(*) FROM pr_commits WHERE pr_id = ?", prID).Scan(&count); err != nil {
		t.Fatalf("query commit count: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 commits for open PR, got %d", count)
	}
}

func TestIndexGitHubData_UpsertReplacesCommits(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git indexing")
	}

	storeDir := t.TempDir()
	s, err := store.Open(storeDir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	ledgerPath := t.TempDir()
	now := time.Now().UTC().Truncate(time.Second)

	// first index: PR with 1 commit
	pr := &ledger.PRFile{
		Number: 44, Title: "PR", Author: "alice", State: "merged",
		CreatedAt: now, UpdatedAt: now, MergedAt: &now, MergeCommit: "m1",
		Commits: []ledger.PRCommit{
			{SHA: "old1", Author: "alice", Date: now, Msg: "old"},
		},
	}
	if err := ledger.WriteGitHubPR(ledgerPath, pr); err != nil {
		t.Fatalf("write PR: %v", err)
	}

	if _, err := IndexGitHubData(context.Background(), s, ledgerPath, nil); err != nil {
		t.Fatalf("IndexGitHubData first pass: %v", err)
	}

	// update file (touch it to change mtime)
	pr.Commits = []ledger.PRCommit{
		{SHA: "new1", Author: "alice", Date: now, Msg: "new1"},
		{SHA: "new2", Author: "bob", Date: now, Msg: "new2"},
	}
	// small delay to ensure mtime changes
	time.Sleep(10 * time.Millisecond)
	if err := ledger.WriteGitHubPR(ledgerPath, pr); err != nil {
		t.Fatalf("write updated PR: %v", err)
	}

	// re-index
	if _, err := IndexGitHubData(context.Background(), s, ledgerPath, nil); err != nil {
		t.Fatalf("IndexGitHubData second pass: %v", err)
	}

	// verify commits were replaced
	var prID int64
	if err := s.QueryRow("SELECT id FROM pull_requests WHERE number = 44").Scan(&prID); err != nil {
		t.Fatalf("query PR id: %v", err)
	}
	var count int
	if err := s.QueryRow("SELECT COUNT(*) FROM pr_commits WHERE pr_id = ?", prID).Scan(&count); err != nil {
		t.Fatalf("query commit count: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 commits after upsert, got %d", count)
	}

	var sha string
	if err := s.QueryRow("SELECT sha FROM pr_commits WHERE pr_id = ? ORDER BY rowid LIMIT 1", prID).Scan(&sha); err != nil {
		t.Fatalf("query first commit SHA: %v", err)
	}
	if sha != "new1" {
		t.Errorf("expected SHA 'new1' after upsert, got %q", sha)
	}
}

func TestIndexGitHubData_BackwardCompatOldJSON(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git indexing")
	}

	storeDir := t.TempDir()
	s, err := store.Open(storeDir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	// write old-format JSON without commits field
	ledgerPath := t.TempDir()
	now := time.Now().UTC().Truncate(time.Second)
	dir := filepath.Join(ledgerPath, "data", "github",
		now.Format("2006"), now.Format("01"), now.Format("02"), "pr")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir old-format dir: %v", err)
	}

	oldJSON := `{
		"number": 99,
		"title": "Old PR format",
		"body": "",
		"author": "charlie",
		"state": "merged",
		"created_at": "2026-01-01T00:00:00Z",
		"updated_at": "2026-01-01T00:00:00Z",
		"merge_commit": "old999",
		"url": "https://github.com/org/repo/pull/99"
	}`
	if err := os.WriteFile(filepath.Join(dir, "99.json"), []byte(oldJSON), 0o644); err != nil {
		t.Fatalf("write old-format JSON: %v", err)
	}

	stats, err := IndexGitHubData(context.Background(), s, ledgerPath, nil)
	if err != nil {
		t.Fatalf("IndexGitHubData: %v", err)
	}
	if stats.PRsIndexed != 1 {
		t.Errorf("expected 1 PR indexed, got %d", stats.PRsIndexed)
	}

	// verify PR was indexed correctly with no commits
	var prID int64
	if err := s.QueryRow("SELECT id FROM pull_requests WHERE number = 99").Scan(&prID); err != nil {
		t.Fatalf("query PR id: %v", err)
	}
	var count int
	if err := s.QueryRow("SELECT COUNT(*) FROM pr_commits WHERE pr_id = ?", prID).Scan(&count); err != nil {
		t.Fatalf("query commit count: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 commits from old JSON, got %d", count)
	}
}
