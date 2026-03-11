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
