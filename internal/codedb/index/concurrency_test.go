package index

// Concurrency and race condition tests for the codedb indexer.
//
// These tests verify that:
//   - Concurrent IndexLocalRepo calls on the same Store do not corrupt data
//   - Search queries can run safely while indexing is in progress
//   - BuildDirtyIndex and GCDirtyIndexes are safe when called concurrently
//
// Run with the race detector to surface data races:
//
//	go test -race ./internal/codedb/index/ -run TestConcurrent -v

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"github.com/sageox/ox/internal/codedb/search"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// gitExec runs a git command in dir and fails the test on error.
func gitExec(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), // safe: git subprocess needs parent env for git config; isolated via cmd.Dir=tmpdir
		"GIT_AUTHOR_NAME=test",
		"GIT_AUTHOR_EMAIL=test@sageox.ai",
		"GIT_COMMITTER_NAME=test",
		"GIT_COMMITTER_EMAIL=test@sageox.ai",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %s", args, out)
	}
}

// TestConcurrentIndexLocalRepo verifies that two goroutines calling IndexLocalRepo
// on the same Store simultaneously do not panic or corrupt state. SQLite is in
// WAL mode so reads and the serialized write path are safe; this test validates
// the Bleve layer and SQL transaction serialization under contention.
func TestConcurrentIndexLocalRepo(t *testing.T) {
	t.Parallel()

	const goroutines = 3
	dir, _ := initGitRepo(t, 5)
	s := openTestStore(t)

	var wg sync.WaitGroup
	errs := make([]error, goroutines)

	// Release all goroutines at the same moment to maximize contention.
	start := make(chan struct{})
	for i := range goroutines {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			errs[idx] = IndexLocalRepo(context.Background(), s, dir, IndexOptions{})
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		assert.NoError(t, err, "goroutine %d: IndexLocalRepo failed", i)
	}

	// Store must be intact: all commits must be queryable after concurrent indexing.
	rows, err := s.Query("SELECT COUNT(*) FROM commits")
	require.NoError(t, err)
	defer rows.Close()
	var count int
	require.True(t, rows.Next())
	require.NoError(t, rows.Scan(&count))
	assert.Equal(t, 5, count, "all commits must be present after concurrent indexing")
}

// TestSearchDuringIndexing verifies that search queries return without error
// while a long IndexLocalRepo is running on the same Store. SQLite WAL mode
// allows concurrent readers, and this test confirms no deadlock or crash.
func TestSearchDuringIndexing(t *testing.T) {
	t.Parallel()

	dir, _ := initGitRepo(t, 20) // larger repo → longer index time → more read overlap
	s := openTestStore(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// search worker: fires queries until indexing is done
	var searchErr error
	var searchWg sync.WaitGroup
	searchWg.Add(1)
	go func() {
		defer searchWg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			default:
				q, parseErr := search.ParseQuery("type:commit refactor")
				if parseErr != nil {
					searchErr = fmt.Errorf("ParseQuery: %w", parseErr)
					return
				}
				if _, execErr := search.Execute(ctx, s, q); execErr != nil && ctx.Err() == nil {
					searchErr = fmt.Errorf("search.Execute: %w", execErr)
					return
				}
			}
		}
	}()

	indexErr := IndexLocalRepo(context.Background(), s, dir, IndexOptions{})
	cancel() // stop search worker
	searchWg.Wait()

	require.NoError(t, indexErr)
	assert.NoError(t, searchErr)
}

// TestGCDirtyIndexesDuringBuild verifies that GCDirtyIndexes running concurrently
// with a single BuildDirtyIndex call does not cause panics or manifest corruption.
// The realistic race: GC scans the dirty dir while BuildDirtyIndex is mid-swap
// (tmp dir exists, manifest not yet written). GC must not remove a partially-built
// overlay, and the final manifest must be intact after both complete.
func TestGCDirtyIndexesDuringBuild(t *testing.T) {
	t.Parallel()

	dir, _ := initGitRepo(t, 3)
	s := openTestStore(t)

	require.NoError(t, IndexLocalRepo(context.Background(), s, dir, IndexOptions{}))

	// Create a dirty file to index.
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "dirty.go"),
		[]byte("package main\nvar DirtyConcurrent = true\n"),
		0o644,
	))

	dirtyPath := DirtyIndexPath(s.Root, dir)
	codedbDir := filepath.Dir(filepath.Dir(dirtyPath)) // store root

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var gcErr error
	var gcWg sync.WaitGroup
	gcWg.Add(1)
	go func() {
		defer gcWg.Done()
		// Run GC in a tight loop while the build is in progress.
		for {
			select {
			case <-ctx.Done():
				return
			default:
				if _, err := GCDirtyIndexes(codedbDir); err != nil && ctx.Err() == nil {
					gcErr = err
					return
				}
			}
		}
	}()

	_, buildErr := BuildDirtyIndex(context.Background(), dir, dirtyPath, IndexOptions{})
	cancel()
	gcWg.Wait()

	assert.NoError(t, buildErr)
	assert.NoError(t, gcErr)

	// Manifest must exist and point to the correct absolute path.
	manifestData, err := os.ReadFile(dirtyPath + ".manifest")
	require.NoError(t, err, "manifest must exist after build")
	assert.Equal(t, dir, string(manifestData), "manifest must contain abs worktree path")
}

// TestIncrementalIndexDoesNotDuplicateCommits verifies that calling IndexLocalRepo
// a second time after new commits are added produces no duplicate rows — this is
// the sequential precondition for safe incremental updates.
func TestIncrementalIndexDoesNotDuplicateCommits(t *testing.T) {
	t.Parallel()

	dir, _ := initGitRepo(t, 3)
	s := openTestStore(t)

	require.NoError(t, IndexLocalRepo(context.Background(), s, dir, IndexOptions{}))

	// Add a new commit.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "new.go"), []byte("package main\n"), 0o644))
	gitExec(t, dir, "add", "new.go")
	gitExec(t, dir, "commit", "-m", "commit 4")

	require.NoError(t, IndexLocalRepo(context.Background(), s, dir, IndexOptions{}))

	rows, err := s.Query("SELECT COUNT(*) FROM commits")
	require.NoError(t, err)
	defer rows.Close()
	var count int
	require.True(t, rows.Next())
	require.NoError(t, rows.Scan(&count))
	assert.Equal(t, 4, count, "incremental index must not duplicate commits")
}
