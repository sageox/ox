package index

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/stretchr/testify/require"

	"github.com/sageox/ox/internal/codedb/store"
)

// ── walkNewCommits benchmark ──────────────────────────────────────────────────

// buildLinearRepo creates a linear git repo with N commits.
// Returns the repo object and the tip hash.
func buildLinearRepo(b *testing.B, n int) (*git.Repository, plumbing.Hash) {
	b.Helper()
	dir := b.TempDir()

	repo, err := git.PlainInit(dir, false)
	require.NoError(b, err)

	wt, err := repo.Worktree()
	require.NoError(b, err)

	sig := &object.Signature{Name: "bench", Email: "b@b.com", When: time.Now()}

	var tip plumbing.Hash
	for i := 0; i < n; i++ {
		fname := fmt.Sprintf("f%d.go", i)
		require.NoError(b, os.WriteFile(filepath.Join(dir, fname), []byte(fmt.Sprintf("package p; var V%d = %d\n", i, i)), 0o644))
		_, err := wt.Add(fname)
		require.NoError(b, err)
		tip, err = wt.Commit(fmt.Sprintf("commit %d", i), &git.CommitOptions{Author: sig})
		require.NoError(b, err)
	}
	return repo, tip
}

// BenchmarkWalkNewCommits_Small measures walkNewCommits with 100 commits.
func BenchmarkWalkNewCommits_Small(b *testing.B) {
	repo, tip := buildLinearRepo(b, 100)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		commits, _ := walkNewCommits(repo, tip, nil, 0)
		if len(commits) != 100 {
			b.Fatalf("expected 100 commits, got %d", len(commits))
		}
	}
}

// BenchmarkWalkNewCommits_Large measures walkNewCommits with 2000 commits.
// The O(n²) slice-shifting queue makes this dramatically slower than Small.
func BenchmarkWalkNewCommits_Large(b *testing.B) {
	repo, tip := buildLinearRepo(b, 2000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		commits, _ := walkNewCommits(repo, tip, nil, 0)
		if len(commits) != 2000 {
			b.Fatalf("expected 2000 commits, got %d", len(commits))
		}
	}
}

// ── generateDiffText benchmark ────────────────────────────────────────────────

// BenchmarkGenerateDiffText measures generateDiffText on a large single-file diff.
// Each call generates up to 200 fmt.Fprintf calls (100 lines old + 100 lines new).
func BenchmarkGenerateDiffText(b *testing.B) {
	// Build a repo with two commits: one adding a 200-line file, one modifying it
	dir := b.TempDir()
	repo, err := git.PlainInit(dir, false)
	require.NoError(b, err)

	wt, err := repo.Worktree()
	require.NoError(b, err)
	sig := &object.Signature{Name: "bench", Email: "b@b.com", When: time.Now()}

	lines := make([]string, 200)
	for i := range lines {
		lines[i] = fmt.Sprintf("func Function%d() { return %d }", i, i)
	}

	fname := "big.go"
	require.NoError(b, os.WriteFile(filepath.Join(dir, fname), []byte(strings.Join(lines, "\n")), 0o644))
	_, err = wt.Add(fname)
	require.NoError(b, err)
	c1, err := wt.Commit("add big file", &git.CommitOptions{Author: sig})
	require.NoError(b, err)

	for i := range lines {
		lines[i] = fmt.Sprintf("func Function%d() { return %d }", i, i*2)
	}
	require.NoError(b, os.WriteFile(filepath.Join(dir, fname), []byte(strings.Join(lines, "\n")), 0o644))
	_, err = wt.Add(fname)
	require.NoError(b, err)
	c2, err := wt.Commit("modify big file", &git.CommitOptions{Author: sig})
	require.NoError(b, err)

	co1, err := repo.CommitObject(c1)
	require.NoError(b, err)
	co2, err := repo.CommitObject(c2)
	require.NoError(b, err)

	// find the blob OIDs by walking the trees
	t1, _ := co1.Tree()
	t2, _ := co2.Tree()
	var oldOID, newOID plumbing.Hash
	_ = t1.Files().ForEach(func(f *object.File) error {
		if f.Name == fname {
			oldOID = f.Hash
		}
		return nil
	})
	_ = t2.Files().ForEach(func(f *object.File) error {
		if f.Name == fname {
			newOID = f.Hash
		}
		return nil
	})

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		result := generateDiffText(repo, fname, oldOID, newOID, true, true, "", "")
		if result == "" {
			b.Fatal("empty diff")
		}
	}
}

// ── full pipeline benchmark ───────────────────────────────────────────────────

// buildRepoWithFileChanges creates a git repo where each commit modifies
// an existing file (not just adds new ones), producing meaningful diffs.
func buildRepoWithFileChanges(b *testing.B, commits int) string {
	b.Helper()
	dir := b.TempDir()

	repo, err := git.PlainInit(dir, false)
	require.NoError(b, err)

	wt, err := repo.Worktree()
	require.NoError(b, err)
	sig := &object.Signature{Name: "bench", Email: "b@b.com", When: time.Now()}

	// Create 5 files in the first commit.
	const numFiles = 5
	for f := 0; f < numFiles; f++ {
		fname := fmt.Sprintf("file%d.go", f)
		content := fmt.Sprintf("package main\nfunc F%d() { return %d }\n", f, 0)
		require.NoError(b, os.WriteFile(filepath.Join(dir, fname), []byte(content), 0o644))
		_, err := wt.Add(fname)
		require.NoError(b, err)
	}
	_, err = wt.Commit("initial commit", &git.CommitOptions{Author: sig})
	require.NoError(b, err)

	// Each subsequent commit modifies all files (maximum diff surface).
	for i := 1; i < commits; i++ {
		for f := 0; f < numFiles; f++ {
			fname := fmt.Sprintf("file%d.go", f)
			// 50-line file: enough for generateDiffText to produce real output.
			var sb strings.Builder
			fmt.Fprintf(&sb, "package main // commit %d\n", i)
			for line := 0; line < 50; line++ {
				fmt.Fprintf(&sb, "func F%d_%d() { return %d }\n", f, line, i*line)
			}
			require.NoError(b, os.WriteFile(filepath.Join(dir, fname), []byte(sb.String()), 0o644))
			_, err := wt.Add(fname)
			require.NoError(b, err)
		}
		_, err = wt.Commit(fmt.Sprintf("commit %d", i), &git.CommitOptions{Author: sig})
		require.NoError(b, err)
	}
	return dir
}

// BenchmarkIndexLocalRepo_50Commits measures the full indexing pipeline:
// walkNewCommits, getTreeEntries, ensureBlob (SQL+Bleve), generateDiffText,
// insertDiff, insertParentLinks, buildTipFileRevs, and transaction commit.
//
// Run as:
//
//	go test -bench=BenchmarkIndexLocalRepo -benchmem -count=3 ./internal/codedb/index/
//
// Compare before/after by running on the main branch first (go test -bench=...
// 2>&1 | tee /tmp/before.txt) then on ryan/optimization.
func BenchmarkIndexLocalRepo_50Commits(b *testing.B) {
	repoDir := buildRepoWithFileChanges(b, 50)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		s, err := store.Open(b.TempDir())
		require.NoError(b, err)
		b.StartTimer()

		err = IndexLocalRepo(context.Background(), s, repoDir, IndexOptions{})
		if err != nil {
			b.Fatal(err)
		}
		b.StopTimer()
		s.Close()
		b.StartTimer()
	}
}

// BenchmarkIndexLocalRepo_200Commits measures pipeline scaling at 200 commits.
// Each commit modifies 5 files of 50 lines = 250 diffs, 250 blob writes per run.
func BenchmarkIndexLocalRepo_200Commits(b *testing.B) {
	repoDir := buildRepoWithFileChanges(b, 200)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		s, err := store.Open(b.TempDir())
		require.NoError(b, err)
		b.StartTimer()

		err = IndexLocalRepo(context.Background(), s, repoDir, IndexOptions{})
		if err != nil {
			b.Fatal(err)
		}
		b.StopTimer()
		s.Close()
		b.StartTimer()
	}
}

// ── ensureBlob SQL benchmark ──────────────────────────────────────────────────

// openBenchStore opens a store and registers cleanup.
func openBenchStore(b *testing.B) *store.Store {
	b.Helper()
	s, err := store.Open(b.TempDir())
	require.NoError(b, err)
	b.Cleanup(func() { s.Close() })
	return s
}

// BenchmarkEnsureBlob_SQLDoubleQuery measures the INSERT OR IGNORE + SELECT
// round-trip pattern for blob insertion: 2 SQL queries per new blob.
func BenchmarkEnsureBlob_SQLDoubleQuery(b *testing.B) {
	s := openBenchStore(b)
	ctx := context.Background()
	tx, err := s.BeginTx(ctx, nil)
	require.NoError(b, err)
	defer tx.Rollback()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		contentHash := fmt.Sprintf("hash%040d", i)
		lang := "go"

		// Current pattern: INSERT OR IGNORE + SELECT (2 round-trips)
		_, err := tx.Exec("INSERT OR IGNORE INTO blobs (content_hash, language) VALUES (?, ?)", contentHash, lang)
		if err != nil {
			b.Fatal(err)
		}
		var blobID int64
		if err := tx.QueryRow("SELECT id FROM blobs WHERE content_hash = ?", contentHash).Scan(&blobID); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkEnsureBlob_SQLReturning measures the upsert RETURNING pattern
// for blob insertion: 1 SQL query per new blob.
func BenchmarkEnsureBlob_SQLReturning(b *testing.B) {
	s := openBenchStore(b)
	ctx := context.Background()
	tx, err := s.BeginTx(ctx, nil)
	require.NoError(b, err)
	defer tx.Rollback()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		contentHash := fmt.Sprintf("hash%040d", i)
		lang := "go"

		// Proposed pattern: upsert noop RETURNING id (1 round-trip)
		var blobID int64
		if err := tx.QueryRow(
			`INSERT INTO blobs (content_hash, language) VALUES (?, ?)
			 ON CONFLICT(content_hash) DO UPDATE SET content_hash = content_hash
			 RETURNING id`,
			contentHash, lang,
		).Scan(&blobID); err != nil {
			b.Fatal(err)
		}
	}
}
