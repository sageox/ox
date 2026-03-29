package index

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
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
	if testing.Short() {
		t.Skip("short: git indexing")
	}
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
	if testing.Short() {
		t.Skip("short: git indexing")
	}
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
	if testing.Short() {
		t.Skip("short: git indexing")
	}
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
	if testing.Short() {
		t.Skip("short: git indexing")
	}
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
	if testing.Short() {
		t.Skip("short: git indexing")
	}
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

func TestGCDirtyIndexes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		setup       func(t *testing.T, codedbDir string)
		wantRemoved int
		wantErr     bool
		verify      func(t *testing.T, codedbDir string)
	}{
		{
			name:        "empty_dir",
			setup:       func(t *testing.T, codedbDir string) {},
			wantRemoved: 0,
		},
		{
			name: "keeps_live_overlay",
			setup: func(t *testing.T, codedbDir string) {
				dir, _ := initGitRepo(t, 1)
				require.NoError(t, os.WriteFile(filepath.Join(dir, "live.go"), []byte("package main\nfunc Live() {}\n"), 0o644))
				dp := DirtyIndexPath(codedbDir, dir)
				_, err := BuildDirtyIndex(context.Background(), dir, dp, IndexOptions{})
				require.NoError(t, err)
			},
			wantRemoved: 0,
		},
		{
			name: "removes_stale_overlay",
			setup: func(t *testing.T, codedbDir string) {
				dir, _ := initGitRepo(t, 1)
				require.NoError(t, os.WriteFile(filepath.Join(dir, "stale.go"), []byte("package main\nfunc Stale() {}\n"), 0o644))
				dp := DirtyIndexPath(codedbDir, dir)
				_, err := BuildDirtyIndex(context.Background(), dir, dp, IndexOptions{})
				require.NoError(t, err)
				require.DirExists(t, dp)
				require.FileExists(t, dp+".manifest")
				require.NoError(t, os.RemoveAll(dir))
			},
			wantRemoved: 1,
			verify: func(t *testing.T, codedbDir string) {
				// all overlay dirs and manifests under dirty/ should be gone
				dirtyDir := filepath.Join(codedbDir, "bleve", "dirty")
				entries, err := os.ReadDir(dirtyDir)
				require.NoError(t, err)
				for _, e := range entries {
					if e.IsDir() {
						t.Errorf("stale overlay dir still exists: %s", e.Name())
					}
				}
			},
		},
		{
			name: "ignores_no_manifest",
			setup: func(t *testing.T, codedbDir string) {
				dirtyDir := filepath.Join(codedbDir, "bleve", "dirty")
				require.NoError(t, os.MkdirAll(dirtyDir, 0o755))
				orphanDir := filepath.Join(dirtyDir, "aabbccdd11223344")
				require.NoError(t, os.MkdirAll(orphanDir, 0o755))
			},
			wantRemoved: 0,
			verify: func(t *testing.T, codedbDir string) {
				orphanDir := filepath.Join(codedbDir, "bleve", "dirty", "aabbccdd11223344")
				require.DirExists(t, orphanDir, "orphan dir must survive")
			},
		},
		{
			name: "mixed_stale_and_live",
			setup: func(t *testing.T, codedbDir string) {
				liveDir, _ := initGitRepo(t, 1)
				staleDir1, _ := initGitRepo(t, 1)
				staleDir2, _ := initGitRepo(t, 1)
				for _, dir := range []string{liveDir, staleDir1, staleDir2} {
					require.NoError(t, os.WriteFile(filepath.Join(dir, "f.go"), []byte("package p\nfunc F() {}\n"), 0o644))
					dp := DirtyIndexPath(codedbDir, dir)
					_, err := BuildDirtyIndex(context.Background(), dir, dp, IndexOptions{})
					require.NoError(t, err)
				}
				require.NoError(t, os.RemoveAll(staleDir1))
				require.NoError(t, os.RemoveAll(staleDir2))
			},
			wantRemoved: 2,
		},
		{
			name: "unreadable_dirty_dir",
			setup: func(t *testing.T, codedbDir string) {
				if runtime.GOOS == "windows" {
					t.Skip("chmod not effective on Windows")
				}
				dirtyDir := filepath.Join(codedbDir, "bleve", "dirty")
				require.NoError(t, os.MkdirAll(dirtyDir, 0o755))
				require.NoError(t, os.Chmod(dirtyDir, 0o000))
				t.Cleanup(func() { _ = os.Chmod(dirtyDir, 0o755) })
			},
			wantRemoved: 0,
			wantErr:     true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			codedbDir := t.TempDir()
			tc.setup(t, codedbDir)

			got, err := GCDirtyIndexes(codedbDir)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantRemoved, got)
			if tc.verify != nil {
				tc.verify(t, codedbDir)
			}
		})
	}
}
