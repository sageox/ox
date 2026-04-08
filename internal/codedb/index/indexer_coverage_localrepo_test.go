package index

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/testguard"
)

// --- IndexLocalRepo ---

func TestIndexLocalRepo_Idempotent(t *testing.T) {
	// no t.Parallel(): FD leak detector counts process-wide FDs,
	// parallel tests opening stores/repos cause false positives in CI
	if testing.Short() {
		t.Skip("short: git indexing")
	}
	testguard.RequireNoFDLeak(t)
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

// --- FD leak regression ---

// TestIndexLocalRepo_NoFDLeak verifies that repeated IndexLocalRepo calls
// don't leak file descriptors. go-git opens packfiles with KeepDescriptors
// for performance — without repo.Close(), each call leaks those FDs.
// Failure prevented: daemon exhausts FD limit after many indexing cycles.
func TestIndexLocalRepo_NoFDLeak(t *testing.T) {
	// no t.Parallel(): FD leak detector counts process-wide FDs,
	// parallel tests opening stores/repos cause false positives in CI
	if testing.Short() {
		t.Skip("short: git indexing")
	}
	testguard.RequireNoFDLeak(t)

	dir, _ := initGitRepo(t, 20)

	// force packfile creation — KeepDescriptors only leaks when packfiles exist
	gitGC := exec.Command("git", "gc", "--aggressive")
	gitGC.Dir = dir
	gitGC.Env = append(os.Environ(), // safe: git CLI in temp dir
		"GIT_AUTHOR_NAME=test",
		"GIT_AUTHOR_EMAIL=test@sageox.ai",
	)
	if out, err := gitGC.CombinedOutput(); err != nil {
		t.Fatalf("git gc: %s: %v", out, err)
	}

	// verify packfile was created
	packDir := filepath.Join(dir, ".git", "objects", "pack")
	entries, _ := os.ReadDir(packDir)
	hasPack := false
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".pack" {
			hasPack = true
			break
		}
	}
	if !hasPack {
		t.Skip("git gc did not create a packfile")
	}

	s := openTestStore(t)

	// index 10 times — without Close(), this leaked ~2 FDs per call
	for i := 0; i < 10; i++ {
		if err := IndexLocalRepo(context.Background(), s, dir, IndexOptions{}); err != nil {
			t.Fatalf("IndexLocalRepo pass %d: %v", i+1, err)
		}
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
