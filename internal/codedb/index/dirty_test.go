package index

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sageox/ox/internal/codedb/store"
)

// buildAndAttachDirty builds the on-disk dirty index and attaches it to the store.
func buildAndAttachDirty(t *testing.T, s *store.Store, repoDir string) int {
	t.Helper()
	dirtyPath := DirtyIndexPath(s.Root, repoDir)
	n, err := BuildDirtyIndex(context.Background(), repoDir, dirtyPath, IndexOptions{})
	require.NoError(t, err)
	if n > 0 {
		require.NoError(t, s.AttachDirtyIndex(dirtyPath))
		t.Cleanup(func() { s.DetachDirtyOverlay() })
	}
	return n
}

func TestBuildDirtyIndex_ModifiedFile(t *testing.T) {
	t.Parallel()
	dir, _ := initGitRepo(t, 2)
	dataDir := filepath.Join(t.TempDir(), "codedb")
	require.NoError(t, os.MkdirAll(dataDir, 0o755))

	s, err := store.Open(dataDir)
	require.NoError(t, err)
	defer s.Close()

	require.NoError(t, IndexLocalRepo(context.Background(), s, dir, IndexOptions{}))

	// modify a committed file (making it dirty)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "file1.go"), []byte("package main\nfunc DirtyFunc() { /* unique_dirty_marker */ }\n"), 0o644))

	n := buildAndAttachDirty(t, s, dir)
	assert.Equal(t, 1, n, "only the modified file should be indexed as dirty")
}

func TestBuildDirtyIndex_NewFile(t *testing.T) {
	t.Parallel()
	dir, _ := initGitRepo(t, 1)
	dataDir := filepath.Join(t.TempDir(), "codedb")
	require.NoError(t, os.MkdirAll(dataDir, 0o755))

	s, err := store.Open(dataDir)
	require.NoError(t, err)
	defer s.Close()

	require.NoError(t, IndexLocalRepo(context.Background(), s, dir, IndexOptions{}))

	// add an untracked file
	require.NoError(t, os.WriteFile(filepath.Join(dir, "newfile.go"), []byte("package main\nfunc BrandNewFunc() {}\n"), 0o644))

	n := buildAndAttachDirty(t, s, dir)
	assert.Equal(t, 1, n, "new untracked file should be indexed as dirty")
}

func TestBuildDirtyIndex_CleanWorktree(t *testing.T) {
	t.Parallel()
	dir, _ := initGitRepo(t, 3)
	dataDir := filepath.Join(t.TempDir(), "codedb")
	require.NoError(t, os.MkdirAll(dataDir, 0o755))

	s, err := store.Open(dataDir)
	require.NoError(t, err)
	defer s.Close()

	require.NoError(t, IndexLocalRepo(context.Background(), s, dir, IndexOptions{}))

	dirtyPath := DirtyIndexPath(s.Root, dir)
	n, err := BuildDirtyIndex(context.Background(), dir, dirtyPath, IndexOptions{})
	require.NoError(t, err)
	assert.Equal(t, 0, n, "clean worktree should produce zero dirty docs")

	// no dirty index file should exist for clean worktrees
	_, err = os.Stat(dirtyPath)
	assert.True(t, os.IsNotExist(err), "dirty index should not exist for clean worktree")
}

func TestBuildDirtyIndex_MultipleFiles(t *testing.T) {
	t.Parallel()
	dir, _ := initGitRepo(t, 1)
	dataDir := filepath.Join(t.TempDir(), "codedb")
	require.NoError(t, os.MkdirAll(dataDir, 0o755))

	s, err := store.Open(dataDir)
	require.NoError(t, err)
	defer s.Close()

	require.NoError(t, IndexLocalRepo(context.Background(), s, dir, IndexOptions{}))

	// modify existing + add new files
	require.NoError(t, os.WriteFile(filepath.Join(dir, "file1.go"), []byte("package main\nfunc Modified() {}\n"), 0o644))
	for i := 0; i < 3; i++ {
		fname := fmt.Sprintf("extra%d.go", i)
		require.NoError(t, os.WriteFile(filepath.Join(dir, fname), []byte(fmt.Sprintf("package main\nfunc Extra%d() {}\n", i)), 0o644))
	}

	n := buildAndAttachDirty(t, s, dir)
	assert.Equal(t, 4, n, "1 modified + 3 new = 4 dirty files")
}

func TestDirtyIndexPath_Deterministic(t *testing.T) {
	t.Parallel()
	p1 := DirtyIndexPath("/data/codedb", "/home/user/project")
	p2 := DirtyIndexPath("/data/codedb", "/home/user/project")
	assert.Equal(t, p1, p2, "same inputs should produce same path")

	p3 := DirtyIndexPath("/data/codedb", "/home/user/other-project")
	assert.NotEqual(t, p1, p3, "different worktrees should produce different paths")
}

// TestBuildDirtyIndex_WritesManifest verifies that BuildDirtyIndex writes a
// "<dirtyPath>.manifest" file containing the worktree path. Without it,
// GCDirtyIndexes cannot distinguish live from stale overlays.
func TestBuildDirtyIndex_WritesManifest(t *testing.T) {
	t.Parallel()
	dir, _ := initGitRepo(t, 1)
	dataDir := t.TempDir()

	// create a dirty file so BuildDirtyIndex actually writes the index
	require.NoError(t, os.WriteFile(filepath.Join(dir, "new.go"), []byte("package main\nfunc Fresh() {}\n"), 0o644))

	dirtyPath := DirtyIndexPath(dataDir, dir)
	_, err := BuildDirtyIndex(context.Background(), dir, dirtyPath, IndexOptions{})
	require.NoError(t, err)

	raw, err := os.ReadFile(dirtyPath + ".manifest")
	require.NoError(t, err, "manifest file must be written after a successful dirty index build")
	assert.Equal(t, dir, string(raw), "manifest must contain the worktree path verbatim")
}

// TestGCDirtyIndexes_EmptyDir verifies GC returns 0 when the dirty dir is absent.
func TestGCDirtyIndexes_EmptyDir(t *testing.T) {
	t.Parallel()
	codedbDir := t.TempDir()
	removed, err := GCDirtyIndexes(codedbDir)
	require.NoError(t, err)
	assert.Equal(t, 0, removed)
}

// TestGCDirtyIndexes_KeepsLiveOverlay verifies GC does NOT remove an overlay
// whose worktree still exists on disk.
func TestGCDirtyIndexes_KeepsLiveOverlay(t *testing.T) {
	t.Parallel()
	dir, _ := initGitRepo(t, 1)
	dataDir := t.TempDir()

	require.NoError(t, os.WriteFile(filepath.Join(dir, "live.go"), []byte("package main\nfunc Live() {}\n"), 0o644))

	dirtyPath := DirtyIndexPath(dataDir, dir)
	_, err := BuildDirtyIndex(context.Background(), dir, dirtyPath, IndexOptions{})
	require.NoError(t, err)

	removed, err := GCDirtyIndexes(dataDir)
	require.NoError(t, err)
	assert.Equal(t, 0, removed, "live worktree overlay must not be removed")

	_, err = os.Stat(dirtyPath)
	require.NoError(t, err, "dirty index dir must still exist")
}

// TestGCDirtyIndexes_RemovesStaleOverlay verifies GC removes an overlay once
// its worktree directory is deleted.
func TestGCDirtyIndexes_RemovesStaleOverlay(t *testing.T) {
	t.Parallel()
	dir, _ := initGitRepo(t, 1)
	dataDir := t.TempDir()

	require.NoError(t, os.WriteFile(filepath.Join(dir, "stale.go"), []byte("package main\nfunc Stale() {}\n"), 0o644))

	dirtyPath := DirtyIndexPath(dataDir, dir)
	_, err := BuildDirtyIndex(context.Background(), dir, dirtyPath, IndexOptions{})
	require.NoError(t, err)

	// overlay + manifest should exist now
	require.DirExists(t, dirtyPath)
	require.FileExists(t, dirtyPath+".manifest")

	// delete the worktree
	require.NoError(t, os.RemoveAll(dir))

	removed, err := GCDirtyIndexes(dataDir)
	require.NoError(t, err)
	assert.Equal(t, 1, removed, "stale overlay must be counted as removed")

	_, err = os.Stat(dirtyPath)
	assert.True(t, os.IsNotExist(err), "dirty index dir must be deleted")
	_, err = os.Stat(dirtyPath + ".manifest")
	assert.True(t, os.IsNotExist(err), "manifest must be deleted along with the overlay")
}

// TestGCDirtyIndexes_IgnoresNoManifest verifies GC leaves overlays alone when
// no manifest is present (written by an older binary that predates this feature).
func TestGCDirtyIndexes_IgnoresNoManifest(t *testing.T) {
	t.Parallel()
	codedbDir := t.TempDir()
	dirtyDir := filepath.Join(codedbDir, "bleve", "dirty")
	require.NoError(t, os.MkdirAll(dirtyDir, 0o755))

	// create a dir that looks like a dirty overlay but has no manifest
	orphanDir := filepath.Join(dirtyDir, "aabbccdd11223344")
	require.NoError(t, os.MkdirAll(orphanDir, 0o755))

	removed, err := GCDirtyIndexes(codedbDir)
	require.NoError(t, err)
	assert.Equal(t, 0, removed, "overlay without manifest must not be touched")
	require.DirExists(t, orphanDir, "orphan dir must survive")
}

// TestGCDirtyIndexes_MixedStaleAndLive verifies GC removes only stale overlays
// when live and stale overlays coexist in the same codedb.
func TestGCDirtyIndexes_MixedStaleAndLive(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()

	// build two live overlays
	liveDir1, _ := initGitRepo(t, 1)
	liveDir2, _ := initGitRepo(t, 1)
	// build two overlays for worktrees we will delete
	staleDir1, _ := initGitRepo(t, 1)
	staleDir2, _ := initGitRepo(t, 1)

	for _, dir := range []string{liveDir1, liveDir2, staleDir1, staleDir2} {
		d := dir // capture loop var
		require.NoError(t, os.WriteFile(filepath.Join(d, "f.go"), []byte("package p\nfunc F() {}\n"), 0o644))
		dp := DirtyIndexPath(dataDir, d)
		_, err := BuildDirtyIndex(context.Background(), d, dp, IndexOptions{})
		require.NoError(t, err)
	}

	// delete the two stale worktrees
	require.NoError(t, os.RemoveAll(staleDir1))
	require.NoError(t, os.RemoveAll(staleDir2))

	removed, err := GCDirtyIndexes(dataDir)
	require.NoError(t, err)
	assert.Equal(t, 2, removed, "exactly the two stale overlays must be removed")

	// live overlays must survive
	assert.DirExists(t, DirtyIndexPath(dataDir, liveDir1))
	assert.DirExists(t, DirtyIndexPath(dataDir, liveDir2))

	// stale overlays must be gone
	_, err1 := os.Stat(DirtyIndexPath(dataDir, staleDir1))
	assert.True(t, os.IsNotExist(err1))
	_, err2 := os.Stat(DirtyIndexPath(dataDir, staleDir2))
	assert.True(t, os.IsNotExist(err2))
}
