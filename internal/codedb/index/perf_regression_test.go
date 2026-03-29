package index

// Regression tests for the Pass 2/3 codedb indexing optimizations.
// Each test targets a specific code path changed during optimization and
// verifies correct behavior — not just "no panic".
//
// Tests here answer: "what real-world failure does this prevent?"

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/stretchr/testify/require"
)

// ── commit_parents correctness ─────────────────────────────────────────────
//
// commitIDCache must correctly populate parent links during indexing.
// Before optimization: SELECT id per parent (always works).
// After: cache lookup; fallback to SELECT for cross-run parents.

func TestParentLinks_LinearHistory(t *testing.T) {
	t.Parallel()
	dir, _ := initGitRepo(t, 4)
	s := openTestStore(t)

	require.NoError(t, IndexLocalRepo(context.Background(), s, dir, IndexOptions{}))

	// 4 commits, root has no parent → 3 parent links.
	var totalLinks int
	require.NoError(t, s.QueryRow("SELECT COUNT(*) FROM commit_parents").Scan(&totalLinks))
	if totalLinks != 3 {
		t.Errorf("expected 3 parent links for 4-commit linear history, got %d", totalLinks)
	}

	// Every parent_id must reference an existing commit.
	var dangling int
	require.NoError(t, s.QueryRow(`
		SELECT COUNT(*) FROM commit_parents cp
		WHERE NOT EXISTS (SELECT 1 FROM commits c WHERE c.id = cp.parent_id)
	`).Scan(&dangling))
	if dangling > 0 {
		t.Errorf("found %d dangling parent links (parent_id not in commits)", dangling)
	}
}

func TestParentLinks_IncrementalRun(t *testing.T) {
	t.Parallel()
	// Regression: cross-run parent links use the DB fallback path.
	// The parent commit is in DB but not in the new run's commitIDCache.
	dir, _ := initGitRepo(t, 2)
	s := openTestStore(t)

	// First run: index 2 commits.
	require.NoError(t, IndexLocalRepo(context.Background(), s, dir, IndexOptions{}))

	var linksAfterFirst int
	require.NoError(t, s.QueryRow("SELECT COUNT(*) FROM commit_parents").Scan(&linksAfterFirst))
	if linksAfterFirst != 1 {
		t.Fatalf("expected 1 parent link after first run, got %d", linksAfterFirst)
	}

	// Add a third commit and re-index. Commit 3's parent (commit 2) is in DB
	// but not in commitIDCache of the new run — exercises the fallback SELECT path.
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), // safe: git subprocess needs full env for PATH/SSH
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@sageox.ai",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@sageox.ai",
		)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "new.go"), []byte("package main\n"), 0o644))
	run("add", "new.go")
	run("commit", "-m", "commit 3")

	require.NoError(t, IndexLocalRepo(context.Background(), s, dir, IndexOptions{}))

	var linksAfterSecond int
	require.NoError(t, s.QueryRow("SELECT COUNT(*) FROM commit_parents").Scan(&linksAfterSecond))
	if linksAfterSecond != 2 {
		t.Errorf("expected 2 parent links after incremental run, got %d", linksAfterSecond)
	}

	// Verify the new link is not dangling.
	var dangling int
	require.NoError(t, s.QueryRow(`
		SELECT COUNT(*) FROM commit_parents cp
		WHERE NOT EXISTS (SELECT 1 FROM commits c WHERE c.id = cp.parent_id)
	`).Scan(&dangling))
	if dangling > 0 {
		t.Errorf("found %d dangling parent links after incremental run", dangling)
	}
}

// ── ensureBlob content return ──────────────────────────────────────────────
//
// ensureBlob must return content for new blobs so callers (generateDiffText)
// avoid re-reading the same git object. If this is broken, diff text generation
// silently falls back to reading blobs twice per file.

func TestEnsureBlob_ContentForNewBlob(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	repo, err := git.PlainInit(dir, false)
	require.NoError(t, err)

	s := openTestStore(t)
	// upsertRepo to get a valid repoID.
	repoID, err := upsertRepo(context.Background(), s, "test-repo", dir)
	require.NoError(t, err)

	tx, err := s.BeginTx(context.Background(), nil)
	require.NoError(t, err)
	defer tx.Rollback()

	st := &indexState{
		tx:             tx,
		store:          s,
		repo:           repo,
		repoID:         repoID,
		codeBatch:      s.CodeIndex.NewBatch(),
		diffBatch:      s.DiffIndex.NewBatch(),
		knownCommits:   make(map[string]bool),
		treeCache:      make(map[plumbing.Hash]map[string]plumbing.Hash),
		treeOrder:      nil,
		treeCacheLimit: 128,
		blobIDCache:    make(map[string]int64),
		commitIDCache:  make(map[string]int64),
		report:         func(string) {},
	}

	content := "package main\nfunc Hello() {}\n"
	blobHash := writeBlobToRepo(t, repo, content)

	_, gotContent, indexed, err := st.ensureBlob(blobHash, "hello.go")
	require.NoError(t, err)
	if !indexed {
		t.Error("expected new blob to be marked as indexed in Bleve")
	}
	if gotContent != content {
		t.Errorf("ensureBlob content = %q, want %q", gotContent, content)
	}
}

func TestEnsureBlob_NoContentOnCacheHit(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	repo, err := git.PlainInit(dir, false)
	require.NoError(t, err)

	s := openTestStore(t)
	repoID, err := upsertRepo(context.Background(), s, "test-repo-2", dir)
	require.NoError(t, err)

	tx, err := s.BeginTx(context.Background(), nil)
	require.NoError(t, err)
	defer tx.Rollback()

	st := &indexState{
		tx:             tx,
		store:          s,
		repo:           repo,
		repoID:         repoID,
		codeBatch:      s.CodeIndex.NewBatch(),
		diffBatch:      s.DiffIndex.NewBatch(),
		knownCommits:   make(map[string]bool),
		treeCache:      make(map[plumbing.Hash]map[string]plumbing.Hash),
		treeOrder:      nil,
		treeCacheLimit: 128,
		blobIDCache:    make(map[string]int64),
		commitIDCache:  make(map[string]int64),
		report:         func(string) {},
	}

	content := "package p\nvar X = 1\n"
	blobHash := writeBlobToRepo(t, repo, content)

	// First call: new blob → content returned, marked indexed.
	id1, text1, indexed1, err := st.ensureBlob(blobHash, "p.go")
	require.NoError(t, err)
	if text1 == "" {
		t.Error("first call: expected content for new blob")
	}
	if !indexed1 {
		t.Error("first call: expected indexed=true for new blob")
	}

	// Second call: in-memory cache hit → same ID, empty content, not re-indexed.
	id2, text2, indexed2, err := st.ensureBlob(blobHash, "p.go")
	require.NoError(t, err)
	if id1 != id2 {
		t.Errorf("cache hit should return same DB ID: first=%d second=%d", id1, id2)
	}
	if text2 != "" {
		t.Errorf("cache hit should return empty content, got %q", text2)
	}
	if indexed2 {
		t.Error("cache hit should not re-index in Bleve (indexed2=true)")
	}
}

// ── generateDiffText pre-read content ─────────────────────────────────────
//
// Passing pre-read content to generateDiffText must produce identical output
// to the fallback path that reads blobs directly from git. If this is broken,
// diff index content is silently incorrect.

func TestGenerateDiffText_PreReadMatchesFallback(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	repo, err := git.PlainInit(dir, false)
	require.NoError(t, err)

	wt, err := repo.Worktree()
	require.NoError(t, err)
	sig := &object.Signature{Name: "t", Email: "test@sageox.ai", When: time.Now()}

	content1 := strings.Repeat("line of code\n", 50)
	content2 := strings.Repeat("modified line\n", 50)

	require.NoError(t, os.WriteFile(filepath.Join(dir, "f.go"), []byte(content1), 0o644))
	_, err = wt.Add("f.go")
	require.NoError(t, err)
	c1Hash, err := wt.Commit("add f.go", &git.CommitOptions{Author: sig})
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(filepath.Join(dir, "f.go"), []byte(content2), 0o644))
	_, err = wt.Add("f.go")
	require.NoError(t, err)
	c2Hash, err := wt.Commit("modify f.go", &git.CommitOptions{Author: sig})
	require.NoError(t, err)

	c1, err := repo.CommitObject(c1Hash)
	require.NoError(t, err)
	c2, err := repo.CommitObject(c2Hash)
	require.NoError(t, err)
	t1, _ := c1.Tree()
	t2, _ := c2.Tree()

	var oldOID, newOID plumbing.Hash
	_ = t1.Files().ForEach(func(f *object.File) error {
		if f.Name == "f.go" {
			oldOID = f.Hash
		}
		return nil
	})
	_ = t2.Files().ForEach(func(f *object.File) error {
		if f.Name == "f.go" {
			newOID = f.Hash
		}
		return nil
	})

	// Fallback: reads from git.
	fallback := generateDiffText(repo, "f.go", oldOID, newOID, true, true, "", "")
	if fallback == "" {
		t.Fatal("fallback diff text should not be empty")
	}

	// Pre-read: caller provides content directly (optimization path).
	preRead := generateDiffText(repo, "f.go", oldOID, newOID, true, true, content1, content2)

	if fallback != preRead {
		t.Errorf("pre-read produces different diff than fallback\ngot:  %q\nwant: %q", preRead, fallback)
	}
}

func TestGenerateDiffText_PartialPreRead(t *testing.T) {
	t.Parallel()
	// Only new content is pre-read; old must be read from git.
	dir := t.TempDir()
	repo, err := git.PlainInit(dir, false)
	require.NoError(t, err)

	wt, err := repo.Worktree()
	require.NoError(t, err)
	sig := &object.Signature{Name: "t", Email: "test@sageox.ai", When: time.Now()}

	require.NoError(t, os.WriteFile(filepath.Join(dir, "g.go"), []byte("package g\n"), 0o644))
	_, err = wt.Add("g.go")
	require.NoError(t, err)
	c1Hash, err := wt.Commit("add", &git.CommitOptions{Author: sig})
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(filepath.Join(dir, "g.go"), []byte("package g\n// changed\n"), 0o644))
	_, err = wt.Add("g.go")
	require.NoError(t, err)
	c2Hash, err := wt.Commit("modify", &git.CommitOptions{Author: sig})
	require.NoError(t, err)

	c1, _ := repo.CommitObject(c1Hash)
	c2, _ := repo.CommitObject(c2Hash)
	tree1, _ := c1.Tree()
	tree2, _ := c2.Tree()
	var oldOID, newOID plumbing.Hash
	_ = tree1.Files().ForEach(func(f *object.File) error { oldOID = f.Hash; return nil })
	_ = tree2.Files().ForEach(func(f *object.File) error { newOID = f.Hash; return nil })

	baseline := generateDiffText(repo, "g.go", oldOID, newOID, true, true, "", "")
	partial := generateDiffText(repo, "g.go", oldOID, newOID, true, true, "", "package g\n// changed\n")

	if baseline != partial {
		t.Errorf("partial pre-read (new only) differs from baseline:\ngot:  %q\nwant: %q", partial, baseline)
	}
}

// ── blob deduplication ─────────────────────────────────────────────────────
//
// Two files with identical content share one blob row in the DB.
// Verifies that blobIDCache deduplicates correctly after the signature change.

func TestIndexLocalRepo_BlobDedup(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), // safe: git subprocess needs full env for PATH/SSH
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@sageox.ai",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@sageox.ai",
		)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
	run("init", "-b", "main")

	// Two files, same content → same blob OID → deduplicated to 1 DB row.
	sameContent := []byte("package main\nfunc Shared() {}\n")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.go"), sameContent, 0o644))
	run("add", "a.go")
	run("commit", "-m", "add a.go")

	require.NoError(t, os.WriteFile(filepath.Join(dir, "b.go"), sameContent, 0o644))
	run("add", "b.go")
	run("commit", "-m", "add b.go with same content")

	s := openTestStore(t)
	require.NoError(t, IndexLocalRepo(context.Background(), s, dir, IndexOptions{}))

	var blobCount int
	require.NoError(t, s.QueryRow("SELECT COUNT(*) FROM blobs").Scan(&blobCount))
	if blobCount != 1 {
		t.Errorf("identical-content files must deduplicate to 1 blob row, got %d", blobCount)
	}
}

// ── tree cache FIFO eviction ───────────────────────────────────────────────
//
// With a small tree cache, FIFO eviction must not corrupt indexed data.
// The wipe-all strategy could evict a parent tree before its child is processed;
// FIFO eviction avoids this for sequential commit processing.

func TestTreeCache_FIFOEvictionPreservesData(t *testing.T) {
	t.Parallel()
	// 10 commits, TreeCacheLimit=2 → eviction fires on every third lookup.
	dir, _ := initGitRepo(t, 10)
	s := openTestStore(t)

	require.NoError(t, IndexLocalRepo(context.Background(), s, dir, IndexOptions{TreeCacheLimit: 2}))

	var commitCount int
	require.NoError(t, s.QueryRow("SELECT COUNT(*) FROM commits").Scan(&commitCount))
	if commitCount != 10 {
		t.Errorf("expected 10 commits with TreeCacheLimit=2, got %d", commitCount)
	}

	// Commits 2-10 each add a new file → 9 diffs minimum.
	var commitsWithDiffs int
	require.NoError(t, s.QueryRow("SELECT COUNT(DISTINCT commit_id) FROM diffs").Scan(&commitsWithDiffs))
	if commitsWithDiffs < 9 {
		t.Errorf("expected ≥9 commits with diffs (one per non-root commit), got %d", commitsWithDiffs)
	}

	// Parent links must be intact.
	var dangling int
	require.NoError(t, s.QueryRow(`
		SELECT COUNT(*) FROM commit_parents cp
		WHERE NOT EXISTS (SELECT 1 FROM commits c WHERE c.id = cp.parent_id)
	`).Scan(&dangling))
	if dangling > 0 {
		t.Errorf("FIFO eviction with limit=2 caused %d dangling parent links", dangling)
	}
}

func TestTreeCache_MinimalCacheStillCorrect(t *testing.T) {
	t.Parallel()
	// Limit=1: every tree lookup forces an eviction of the previous entry.
	// The implementation must still produce correct output.
	dir, _ := initGitRepo(t, 5)
	s := openTestStore(t)

	require.NoError(t, IndexLocalRepo(context.Background(), s, dir, IndexOptions{TreeCacheLimit: 1}))

	var commitCount int
	require.NoError(t, s.QueryRow("SELECT COUNT(*) FROM commits").Scan(&commitCount))
	if commitCount != 5 {
		t.Errorf("expected 5 commits with TreeCacheLimit=1, got %d", commitCount)
	}

	var dangling int
	require.NoError(t, s.QueryRow(`
		SELECT COUNT(*) FROM commit_parents cp
		WHERE NOT EXISTS (SELECT 1 FROM commits c WHERE c.id = cp.parent_id)
	`).Scan(&dangling))
	if dangling > 0 {
		t.Errorf("TreeCacheLimit=1 caused %d dangling parent links", dangling)
	}
}

// ── LastInsertId fallback (re-index idempotency) ───────────────────────────
//
// When blobs, commits, and diffs already exist (RowsAffected==0 path),
// the fallback SELECT must return correct IDs. If broken, re-indexing would
// either error out or create duplicate rows.

func TestIndexLocalRepo_DoubleIndexPreservesExactCounts(t *testing.T) {
	t.Parallel()
	dir, _ := initGitRepo(t, 4)
	s := openTestStore(t)

	require.NoError(t, IndexLocalRepo(context.Background(), s, dir, IndexOptions{}))

	var commits1, blobs1, diffs1, links1 int
	require.NoError(t, s.QueryRow("SELECT COUNT(*) FROM commits").Scan(&commits1))
	require.NoError(t, s.QueryRow("SELECT COUNT(*) FROM blobs").Scan(&blobs1))
	require.NoError(t, s.QueryRow("SELECT COUNT(*) FROM diffs").Scan(&diffs1))
	require.NoError(t, s.QueryRow("SELECT COUNT(*) FROM commit_parents").Scan(&links1))

	// Re-index: all SQL INSERTs hit the OR IGNORE path → RowsAffected==0.
	require.NoError(t, IndexLocalRepo(context.Background(), s, dir, IndexOptions{}))

	var commits2, blobs2, diffs2, links2 int
	require.NoError(t, s.QueryRow("SELECT COUNT(*) FROM commits").Scan(&commits2))
	require.NoError(t, s.QueryRow("SELECT COUNT(*) FROM blobs").Scan(&blobs2))
	require.NoError(t, s.QueryRow("SELECT COUNT(*) FROM diffs").Scan(&diffs2))
	require.NoError(t, s.QueryRow("SELECT COUNT(*) FROM commit_parents").Scan(&links2))

	if commits2 != commits1 {
		t.Errorf("re-index changed commit count: %d → %d", commits1, commits2)
	}
	if blobs2 != blobs1 {
		t.Errorf("re-index changed blob count: %d → %d", blobs1, blobs2)
	}
	if diffs2 != diffs1 {
		t.Errorf("re-index changed diff count: %d → %d", diffs1, diffs2)
	}
	if links2 != links1 {
		t.Errorf("re-index changed parent link count: %d → %d", links1, links2)
	}
}

// ── DiffTree correctness ───────────────────────────────────────────────────
//
// DiffTreeContext replaces O(all_files)×2 getTree walks with O(changed_files)
// merkle-trie comparison. These tests verify that the semantic output is
// identical: same file adds, modifies, and deletes recorded in diffs table.

func TestDiffTree_InitialCommitAllFilesIndexed(t *testing.T) {
	t.Parallel()
	// Initial commit has no parent → DiffTree treats all files as inserts.
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), // safe: git subprocess needs full env for PATH/SSH
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@sageox.ai",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@sageox.ai",
		)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
	run("init", "-b", "main")

	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.go"), []byte("package main\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "b.go"), []byte("package main\n// b\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "c.go"), []byte("package main\n// c\n"), 0o644))
	run("add", "a.go", "b.go", "c.go")
	run("commit", "-m", "initial commit")

	s := openTestStore(t)
	require.NoError(t, IndexLocalRepo(context.Background(), s, dir, IndexOptions{}))

	// All 3 files should appear in diffs with no old_blob_id (pure inserts)
	var totalDiffs, insertsWithNoOld int
	require.NoError(t, s.QueryRow("SELECT COUNT(*) FROM diffs").Scan(&totalDiffs))
	require.NoError(t, s.QueryRow("SELECT COUNT(*) FROM diffs WHERE old_blob_id IS NULL AND new_blob_id IS NOT NULL").Scan(&insertsWithNoOld))

	if totalDiffs != 3 {
		t.Errorf("expected 3 diffs for initial commit with 3 files, got %d", totalDiffs)
	}
	if insertsWithNoOld != 3 {
		t.Errorf("expected 3 insert diffs (no old_blob_id), got %d", insertsWithNoOld)
	}
}

func TestDiffTree_ModifyAndDeleteTracked(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), // safe: git subprocess needs full env for PATH/SSH
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@sageox.ai",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@sageox.ai",
		)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
	run("init", "-b", "main")

	// Commit 1: add foo.go and bar.go
	require.NoError(t, os.WriteFile(filepath.Join(dir, "foo.go"), []byte("package main\n// v1\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "bar.go"), []byte("package main\n// bar\n"), 0o644))
	run("add", "foo.go", "bar.go")
	run("commit", "-m", "commit 1")

	// Commit 2: modify foo.go, delete bar.go
	require.NoError(t, os.WriteFile(filepath.Join(dir, "foo.go"), []byte("package main\n// v2\n"), 0o644))
	run("add", "foo.go")
	run("rm", "bar.go")
	run("commit", "-m", "commit 2")

	s := openTestStore(t)
	require.NoError(t, IndexLocalRepo(context.Background(), s, dir, IndexOptions{}))

	// Find commit 2's DB ID (message may have trailing newline)
	var c2ID int64
	require.NoError(t, s.QueryRow("SELECT id FROM commits WHERE message LIKE 'commit 2%'").Scan(&c2ID))

	// foo.go: should be a modify (has both old_blob_id and new_blob_id)
	var fooModified int
	require.NoError(t, s.QueryRow(
		"SELECT COUNT(*) FROM diffs WHERE commit_id = ? AND path = 'foo.go' AND old_blob_id IS NOT NULL AND new_blob_id IS NOT NULL",
		c2ID,
	).Scan(&fooModified))
	if fooModified != 1 {
		t.Errorf("expected foo.go to be modified (both old+new blob) in commit 2, got %d", fooModified)
	}

	// bar.go: should be a delete (has old_blob_id, NULL new_blob_id)
	var barDeleted int
	require.NoError(t, s.QueryRow(
		"SELECT COUNT(*) FROM diffs WHERE commit_id = ? AND path = 'bar.go' AND old_blob_id IS NOT NULL AND new_blob_id IS NULL",
		c2ID,
	).Scan(&barDeleted))
	if barDeleted != 1 {
		t.Errorf("expected bar.go to be deleted (old blob, no new blob) in commit 2, got %d", barDeleted)
	}
}

func TestDiffTree_UnchangedFilesNotDuplicated(t *testing.T) {
	t.Parallel()
	// When only one file changes, DiffTree should produce exactly 1 diff for
	// that commit — not 2 (the unchanged file must be skipped).
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), // safe: git subprocess needs full env for PATH/SSH
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@sageox.ai",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@sageox.ai",
		)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
	run("init", "-b", "main")

	// Commit 1: add a.go and b.go
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.go"), []byte("package main\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "b.go"), []byte("package main\n// b\n"), 0o644))
	run("add", "a.go", "b.go")
	run("commit", "-m", "commit 1")

	// Commit 2: modify only a.go; b.go unchanged
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.go"), []byte("package main\n// modified\n"), 0o644))
	run("add", "a.go")
	run("commit", "-m", "commit 2")

	s := openTestStore(t)
	require.NoError(t, IndexLocalRepo(context.Background(), s, dir, IndexOptions{}))

	var c2ID int64
	require.NoError(t, s.QueryRow("SELECT id FROM commits WHERE message LIKE 'commit 2%'").Scan(&c2ID))

	var diffsForC2 int
	require.NoError(t, s.QueryRow("SELECT COUNT(*) FROM diffs WHERE commit_id = ?", c2ID).Scan(&diffsForC2))
	if diffsForC2 != 1 {
		t.Errorf("commit 2 should have exactly 1 diff (only a.go changed), got %d", diffsForC2)
	}
}

// ── checkpoint flushing correctness ───────────────────────────────────────
//
// checkpointFlush commits SQL every checkpointEvery commits, making the index
// incrementally searchable. These tests verify commits survive checkpoints
// and that double-indexing after checkpoints is still idempotent.

func TestCheckpointFlush_DataAccessibleAfterRun(t *testing.T) {
	t.Parallel()
	// Use more than checkpointEvery commits to force at least one checkpoint.
	numCommits := checkpointEvery + 100
	dir, _ := initGitRepo(t, numCommits)
	s := openTestStore(t)

	require.NoError(t, IndexLocalRepo(context.Background(), s, dir, IndexOptions{}))

	var count int
	require.NoError(t, s.QueryRow("SELECT COUNT(*) FROM commits").Scan(&count))
	if count != numCommits {
		t.Errorf("expected %d commits after checkpoint run, got %d", numCommits, count)
	}
}

func TestCheckpointFlush_IdempotentAfterCheckpoints(t *testing.T) {
	t.Parallel()
	// Re-indexing after a checkpoint run must produce the same counts.
	numCommits := checkpointEvery + 50
	dir, _ := initGitRepo(t, numCommits)
	s := openTestStore(t)

	require.NoError(t, IndexLocalRepo(context.Background(), s, dir, IndexOptions{}))

	var commits1, diffs1 int
	require.NoError(t, s.QueryRow("SELECT COUNT(*) FROM commits").Scan(&commits1))
	require.NoError(t, s.QueryRow("SELECT COUNT(*) FROM diffs").Scan(&diffs1))

	// Re-index: all INSERTs hit OR IGNORE → counts must stay identical
	require.NoError(t, IndexLocalRepo(context.Background(), s, dir, IndexOptions{}))

	var commits2, diffs2 int
	require.NoError(t, s.QueryRow("SELECT COUNT(*) FROM commits").Scan(&commits2))
	require.NoError(t, s.QueryRow("SELECT COUNT(*) FROM diffs").Scan(&diffs2))

	if commits2 != commits1 {
		t.Errorf("re-index after checkpoint changed commit count: %d → %d", commits1, commits2)
	}
	if diffs2 != diffs1 {
		t.Errorf("re-index after checkpoint changed diff count: %d → %d", diffs1, diffs2)
	}
}

// ── helper ─────────────────────────────────────────────────────────────────

// writeBlobToRepo stores content as a loose blob in the repo and returns its hash.
func writeBlobToRepo(t *testing.T, repo *git.Repository, content string) plumbing.Hash {
	t.Helper()
	obj := repo.Storer.NewEncodedObject()
	obj.SetType(plumbing.BlobObject)
	w, err := obj.Writer()
	require.NoError(t, err)
	_, err = fmt.Fprint(w, content)
	require.NoError(t, err)
	require.NoError(t, w.Close())
	h, err := repo.Storer.SetEncodedObject(obj)
	require.NoError(t, err)
	return h
}
