package index

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// TestWalkNewCommitsBFSOrder verifies that walkNewCommits returns commits in
// oldest-first (topological) order: every parent appears before its children.
// This is a regression test for BFS ordering with branching history.
func TestWalkNewCommitsBFSOrder(t *testing.T) {
	tmp := t.TempDir()

	repo, err := git.PlainInit(tmp, false)
	if err != nil {
		t.Fatalf("init: %v", err)
	}

	w, err := repo.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}

	sig := &object.Signature{Name: "test", Email: "t@t.com", When: time.Now()}

	// commit 1 on default branch
	if err := os.WriteFile(filepath.Join(tmp, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Add("a.txt"); err != nil {
		t.Fatal(err)
	}
	c1, err := w.Commit("commit 1", &git.CommitOptions{Author: sig})
	if err != nil {
		t.Fatalf("commit 1: %v", err)
	}

	// commit 2
	if err := os.WriteFile(filepath.Join(tmp, "b.txt"), []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Add("b.txt"); err != nil {
		t.Fatal(err)
	}
	c2, err := w.Commit("commit 2", &git.CommitOptions{Author: sig})
	if err != nil {
		t.Fatalf("commit 2: %v", err)
	}

	// commit 3
	if err := os.WriteFile(filepath.Join(tmp, "c.txt"), []byte("c"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Add("c.txt"); err != nil {
		t.Fatal(err)
	}
	c3, err := w.Commit("commit 3", &git.CommitOptions{Author: sig})
	if err != nil {
		t.Fatalf("commit 3: %v", err)
	}

	known := make(map[string]bool)
	commits, truncated := walkNewCommits(repo, c3, known, 0)
	if truncated {
		t.Error("unexpected depth truncation")
	}

	// build index map for ordering checks
	commitIdx := make(map[plumbing.Hash]int)
	for i, cd := range commits {
		commitIdx[cd.oid] = i
	}

	// verify: for each commit, all parents in the list appear at an earlier index
	for i, cd := range commits {
		for _, pid := range cd.parentIDs {
			if parentIdx, ok := commitIdx[pid]; ok {
				if parentIdx > i {
					t.Errorf("parent %s (idx %d) appears after child %s (idx %d)",
						pid.String()[:8], parentIdx, cd.oid.String()[:8], i)
				}
			}
		}
	}

	// should have all 3 commits
	if len(commits) != 3 {
		t.Errorf("expected 3 commits, got %d", len(commits))
	}

	// c1 must appear before c2 which must appear before c3
	idx1, ok1 := commitIdx[c1]
	idx2, ok2 := commitIdx[c2]
	idx3, ok3 := commitIdx[c3]
	if !ok1 || !ok2 || !ok3 {
		t.Fatal("not all commits found in walk result")
	}
	if idx1 > idx2 || idx2 > idx3 {
		t.Errorf("expected c1 (idx %d) < c2 (idx %d) < c3 (idx %d)", idx1, idx2, idx3)
	}
}

// TestWalkNewCommitsSkipsKnown verifies that already-known commits are excluded.
func TestWalkNewCommitsSkipsKnown(t *testing.T) {
	tmp := t.TempDir()

	repo, err := git.PlainInit(tmp, false)
	if err != nil {
		t.Fatalf("init: %v", err)
	}

	w, err := repo.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}

	sig := &object.Signature{Name: "test", Email: "t@t.com", When: time.Now()}

	if err := os.WriteFile(filepath.Join(tmp, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Add("a.txt"); err != nil {
		t.Fatal(err)
	}
	c1, err := w.Commit("commit 1", &git.CommitOptions{Author: sig})
	if err != nil {
		t.Fatalf("commit 1: %v", err)
	}

	if err := os.WriteFile(filepath.Join(tmp, "b.txt"), []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Add("b.txt"); err != nil {
		t.Fatal(err)
	}
	c2, err := w.Commit("commit 2", &git.CommitOptions{Author: sig})
	if err != nil {
		t.Fatalf("commit 2: %v", err)
	}

	// mark c1 as known — walk from c2 should only return c2
	known := map[string]bool{c1.String(): true}
	commits, _ := walkNewCommits(repo, c2, known, 0)

	if len(commits) != 1 {
		t.Fatalf("expected 1 new commit, got %d", len(commits))
	}
	if commits[0].oid != c2 {
		t.Errorf("expected commit %s, got %s", c2.String()[:8], commits[0].oid.String()[:8])
	}
}

// TestWalkNewCommitsMaxDepth verifies the depth limit is respected.
func TestWalkNewCommitsMaxDepth(t *testing.T) {
	tmp := t.TempDir()

	repo, err := git.PlainInit(tmp, false)
	if err != nil {
		t.Fatalf("init: %v", err)
	}

	w, err := repo.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}

	sig := &object.Signature{Name: "test", Email: "t@t.com", When: time.Now()}

	var lastHash plumbing.Hash
	for i := 0; i < 5; i++ {
		fname := filepath.Join(tmp, fmt.Sprintf("file%d.txt", i))
		if err := os.WriteFile(fname, []byte(fmt.Sprintf("%d", i)), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Add(fmt.Sprintf("file%d.txt", i)); err != nil {
			t.Fatal(err)
		}
		lastHash, err = w.Commit(fmt.Sprintf("commit %d", i+1), &git.CommitOptions{Author: sig})
		if err != nil {
			t.Fatalf("commit %d: %v", i+1, err)
		}
	}

	known := make(map[string]bool)
	commits, truncated := walkNewCommits(repo, lastHash, known, 3)

	if !truncated {
		t.Error("expected depth truncation with maxDepth=3 and 5 commits")
	}
	if len(commits) > 3 {
		t.Errorf("expected at most 3 commits, got %d", len(commits))
	}
}
