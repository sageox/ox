package codedb

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sageox/ox/internal/codedb/index"
)

// buildDirtyForWorktree builds a dirty index for worktreePath, writing the index
// into the codedb data directory. Returns the Bleve index path and worktree hash ID.
func buildDirtyForWorktree(t *testing.T, db *DB, worktreePath string) (blevePath, id string) {
	t.Helper()
	dirtyPath := index.DirtyIndexPath(db.Store().Root, worktreePath)
	n, err := index.BuildDirtyIndex(context.Background(), worktreePath, dirtyPath, index.IndexOptions{})
	require.NoError(t, err)
	require.Greater(t, n, 0, "expected dirty files to be indexed in %s", worktreePath)
	// the ID used by AttachAllDirtyIndexes is the directory basename
	return dirtyPath, filepath.Base(dirtyPath)
}

// makeWorktreeDir creates a fresh git repo in a temp directory with committed
// content and an additional dirty file containing token.
func makeWorktreeDir(t *testing.T, token string) string {
	t.Helper()
	dir := initTestRepo(t)
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "dirty_"+token+".go"),
		[]byte("package main\n// "+token+"\nfunc F_"+token+"() {}\n"),
		0o644,
	))
	return dir
}

// --- A. Multi-overlay search correctness ---

// TestSearch_MultipleWorktrees_AllDirtyFilesSearchable verifies that dirty files
// from three independent worktrees are all discoverable via a single Search call.
// Failure prevented: multi-overlay merge drops results from some worktrees.
func TestSearch_MultipleWorktrees_AllDirtyFilesSearchable(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git indexing")
	}

	tokens := []string{"alpha_wt_7k9m", "bravo_wt_3p2q", "charlie_wt_8x4n"}

	// baseline repo — index committed content
	baseRepo := initTestRepo(t)
	dataDir := filepath.Join(t.TempDir(), "codedb")
	require.NoError(t, os.MkdirAll(dataDir, 0o755))

	db, err := Open(dataDir)
	require.NoError(t, err)
	defer db.Close()

	require.NoError(t, db.IndexLocalRepo(context.Background(), baseRepo, index.IndexOptions{}))

	// create 3 worktree-like repos, each with a unique dirty file
	for _, tok := range tokens {
		wtDir := makeWorktreeDir(t, tok)
		blevePath, id := buildDirtyForWorktree(t, db, wtDir)
		require.NoError(t, db.AttachDirtyIndexByID(id, blevePath))
	}
	assert.Equal(t, 3, db.DirtyOverlayCount())

	// each token should be searchable
	for _, tok := range tokens {
		results, sErr := db.Search(context.Background(), tok)
		require.NoError(t, sErr)
		require.NotEmpty(t, results, "expected search hit for token %s", tok)
	}
}

// TestSearch_MultipleWorktrees_CommittedAndDirtyMerged verifies that committed
// content remains searchable when dirty overlays are attached, and that dirty
// content from each overlay is also returned.
// Failure prevented: overlays hide committed results or vice versa.
func TestSearch_MultipleWorktrees_CommittedAndDirtyMerged(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git indexing")
	}

	dataDir := filepath.Join(t.TempDir(), "codedb")
	require.NoError(t, os.MkdirAll(dataDir, 0o755))

	db, err := Open(dataDir)
	require.NoError(t, err)
	defer db.Close()

	// index committed content from the base repo
	baseRepo := initTestRepo(t)
	require.NoError(t, db.IndexLocalRepo(context.Background(), baseRepo, index.IndexOptions{}))

	// attach 2 dirty overlays with unique tokens
	tokA, tokB := "merge_delta_5r8w", "merge_echo_2v6j"
	for _, tok := range []string{tokA, tokB} {
		wtDir := makeWorktreeDir(t, tok)
		blevePath, id := buildDirtyForWorktree(t, db, wtDir)
		require.NoError(t, db.AttachDirtyIndexByID(id, blevePath))
	}
	// committed content still reachable
	results, err := db.Search(context.Background(), "committed_sentinel_comment")
	require.NoError(t, err)
	assert.NotEmpty(t, results, "committed content should remain searchable with overlays attached")

	// dirty content from each overlay reachable
	for _, tok := range []string{tokA, tokB} {
		results, sErr := db.Search(context.Background(), tok)
		require.NoError(t, sErr)
		require.NotEmpty(t, results, "dirty content %s should be searchable", tok)
	}
}

// TestSearch_MultipleWorktrees_DetachOne_OthersStillWork verifies that detaching
// a single overlay removes only its content while leaving others intact.
// Failure prevented: detaching one overlay breaks or removes other overlays.
func TestSearch_MultipleWorktrees_DetachOne_OthersStillWork(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git indexing")
	}

	tokens := []string{"detach_foxtrot_4h7k", "detach_golf_9m3p", "detach_hotel_1n5s"}

	dataDir := filepath.Join(t.TempDir(), "codedb")
	require.NoError(t, os.MkdirAll(dataDir, 0o755))

	db, err := Open(dataDir)
	require.NoError(t, err)
	defer db.Close()

	baseRepo := initTestRepo(t)
	require.NoError(t, db.IndexLocalRepo(context.Background(), baseRepo, index.IndexOptions{}))

	// attach 3 overlays, remember their IDs
	ids := make([]string, 3)
	for i, tok := range tokens {
		wtDir := makeWorktreeDir(t, tok)
		blevePath, id := buildDirtyForWorktree(t, db, wtDir)
		require.NoError(t, db.AttachDirtyIndexByID(id, blevePath))
		ids[i] = id
	}
	t.Cleanup(func() { db.DetachDirtyOverlay() })

	require.Equal(t, 3, db.DirtyOverlayCount())

	// detach overlay 2 (index 1)
	db.DetachDirtyIndexByID(ids[1])
	assert.Equal(t, 2, db.DirtyOverlayCount())

	// overlay 1 content — found
	results, err := db.Search(context.Background(), tokens[0])
	require.NoError(t, err)
	assert.NotEmpty(t, results, "overlay 1 content should still be found after detaching overlay 2")

	// overlay 2 content — NOT found
	results, err = db.Search(context.Background(), tokens[1])
	require.NoError(t, err)
	assert.Empty(t, results, "detached overlay 2 content should not appear in results")

	// overlay 3 content — found
	results, err = db.Search(context.Background(), tokens[2])
	require.NoError(t, err)
	assert.NotEmpty(t, results, "overlay 3 content should still be found after detaching overlay 2")
}

// TestSearch_AttachAllDirtyIndexes_DiscoversMultipleWorktrees verifies that
// AttachAllDirtyIndexes discovers and attaches all dirty overlays that have
// manifest files on disk.
// Failure prevented: auto-discovery misses overlays or silently fails.
func TestSearch_AttachAllDirtyIndexes_DiscoversMultipleWorktrees(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git indexing")
	}

	tokens := []string{"discover_india_6t2w", "discover_juliet_8y4z", "discover_kilo_3a9b"}

	dataDir := filepath.Join(t.TempDir(), "codedb")
	require.NoError(t, os.MkdirAll(dataDir, 0o755))

	db, err := Open(dataDir)
	require.NoError(t, err)
	defer db.Close()

	baseRepo := initTestRepo(t)
	require.NoError(t, db.IndexLocalRepo(context.Background(), baseRepo, index.IndexOptions{}))

	// build dirty indexes on disk (creates manifest files) but do NOT attach yet
	for _, tok := range tokens {
		wtDir := makeWorktreeDir(t, tok)
		_, _ = buildDirtyForWorktree(t, db, wtDir)
	}

	// auto-discover and attach all
	n := db.AttachAllDirtyIndexes()
	t.Cleanup(func() { db.DetachDirtyOverlay() })

	assert.Equal(t, 3, n, "AttachAllDirtyIndexes should discover 3 overlays")
	assert.Equal(t, 3, db.DirtyOverlayCount())

	// content from each worktree should be searchable
	for _, tok := range tokens {
		results, sErr := db.Search(context.Background(), tok)
		require.NoError(t, sErr)
		assert.NotEmpty(t, results, "auto-discovered overlay content %s should be searchable", tok)
	}
}

// TestSearch_AttachAllDirtyIndexes_SkipsDeletedWorktree verifies that
// AttachAllDirtyIndexes gracefully skips worktrees that have been removed from
// disk since their dirty indexes were built.
// Failure prevented: deleted worktree causes crash or stale results.
func TestSearch_AttachAllDirtyIndexes_SkipsDeletedWorktree(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git indexing")
	}

	tokens := []string{"skip_lima_5c7d", "skip_mike_2e8f", "skip_november_9g1h"}

	dataDir := filepath.Join(t.TempDir(), "codedb")
	require.NoError(t, os.MkdirAll(dataDir, 0o755))

	db, err := Open(dataDir)
	require.NoError(t, err)
	defer db.Close()

	baseRepo := initTestRepo(t)
	require.NoError(t, db.IndexLocalRepo(context.Background(), baseRepo, index.IndexOptions{}))

	// build dirty indexes, recording worktree paths
	wtDirs := make([]string, 3)
	for i, tok := range tokens {
		wtDirs[i] = makeWorktreeDir(t, tok)
		_, _ = buildDirtyForWorktree(t, db, wtDirs[i])
	}

	// delete worktree 2 from disk before discovery
	require.NoError(t, os.RemoveAll(wtDirs[1]))

	n := db.AttachAllDirtyIndexes()
	t.Cleanup(func() { db.DetachDirtyOverlay() })

	assert.Equal(t, 2, n, "should attach 2 overlays, skipping the deleted worktree")
	assert.Equal(t, 2, db.DirtyOverlayCount())

	// worktree 1 content — found
	results, err := db.Search(context.Background(), tokens[0])
	require.NoError(t, err)
	assert.NotEmpty(t, results, "worktree 1 content should be searchable")

	// worktree 3 content — found
	results, err = db.Search(context.Background(), tokens[2])
	require.NoError(t, err)
	assert.NotEmpty(t, results, "worktree 3 content should be searchable")
}

// --- B. Deduplication ---

// TestSearch_MultipleWorktrees_SameFileModifiedInTwo verifies that when the same
// file is modified with different content in two worktrees, searches find content
// from both overlays and the baseline without exact duplicates.
// Failure prevented: overlapping dirty files cause duplicate results or hide versions.
func TestSearch_MultipleWorktrees_SameFileModifiedInTwo(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git indexing")
	}

	dataDir := filepath.Join(t.TempDir(), "codedb")
	require.NoError(t, os.MkdirAll(dataDir, 0o755))

	db, err := Open(dataDir)
	require.NoError(t, err)
	defer db.Close()

	// baseline repo with a known file
	baseRepo := initTestRepo(t)
	require.NoError(t, db.IndexLocalRepo(context.Background(), baseRepo, index.IndexOptions{}))

	// worktree 1: modifies committed.go with content B
	tokenB := "dedup_oscar_7j3k"
	wt1 := initTestRepo(t)
	require.NoError(t, os.WriteFile(
		filepath.Join(wt1, "committed.go"),
		[]byte("package main\n// "+tokenB+"\nfunc CommittedFunc() {}\n"),
		0o644,
	))
	blevePath1, id1 := buildDirtyForWorktree(t, db, wt1)
	require.NoError(t, db.AttachDirtyIndexByID(id1, blevePath1))

	// worktree 2: modifies committed.go with content C
	tokenC := "dedup_papa_4m8n"
	wt2 := initTestRepo(t)
	require.NoError(t, os.WriteFile(
		filepath.Join(wt2, "committed.go"),
		[]byte("package main\n// "+tokenC+"\nfunc CommittedFunc() {}\n"),
		0o644,
	))
	blevePath2, id2 := buildDirtyForWorktree(t, db, wt2)
	require.NoError(t, db.AttachDirtyIndexByID(id2, blevePath2))
	t.Cleanup(func() { db.DetachDirtyOverlay() })

	// content B — found via worktree 1 overlay
	results, err := db.Search(context.Background(), tokenB)
	require.NoError(t, err)
	assert.NotEmpty(t, results, "worktree 1 modification should be searchable")

	// content C — found via worktree 2 overlay
	results, err = db.Search(context.Background(), tokenC)
	require.NoError(t, err)
	assert.NotEmpty(t, results, "worktree 2 modification should be searchable")

	// original committed content A — found via baseline
	results, err = db.Search(context.Background(), "committed_sentinel_comment")
	require.NoError(t, err)
	assert.NotEmpty(t, results, "baseline committed content should still be searchable")

	// verify no exact duplicate file paths in results for a broad query
	// (search for "CommittedFunc" which appears in baseline + both overlays)
	results, err = db.Search(context.Background(), "CommittedFunc")
	require.NoError(t, err)
	seen := make(map[string]int)
	for _, r := range results {
		seen[r.FilePath]++
	}
	for path, count := range seen {
		assert.LessOrEqual(t, count, 3, "file %s should appear at most 3 times (baseline + 2 overlays)", path)
	}
}
