//go:build slow

package daemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGC_FullCycle_Ledger_RealRemote verifies the complete blue-green GC reclone
// against the Gitea digital twin, ensuring committed content, dirty state,
// untracked files, and gitignored cache all survive the reclone.
func TestGC_FullCycle_Ledger_RealRemote(t *testing.T) {
	if testing.Short() {
		t.Skip("short: Gitea digital twin")
	}

	g := getSharedGitea(t)
	cloneURL := g.createRepo(t, "ledger-gc")

	cloneDir := filepath.Join(t.TempDir(), "ledger")
	g.cloneRepo(t, cloneURL, cloneDir)

	// add ledger structure: sessions/ and .sync/
	require.NoError(t, os.MkdirAll(filepath.Join(cloneDir, "sessions"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cloneDir, "sessions", ".gitkeep"), []byte(""), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(cloneDir, ".sync"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cloneDir, ".sync", ".gitkeep"), []byte(""), 0o644))

	cmd := exec.Command("git", "-C", cloneDir, "add", "sessions/.gitkeep", ".sync/.gitkeep")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git add failed: %s", string(out))

	cmd = exec.Command("git", "-C", cloneDir, "commit", "-m", "add ledger structure")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "git commit failed: %s", string(out))

	cmd = exec.Command("git", "-C", cloneDir, "push")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "git push failed: %s", string(out))

	// gitignored cache file (survives GC via carry-over)
	require.NoError(t, os.MkdirAll(filepath.Join(cloneDir, ".sageox", "cache", "codedb"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cloneDir, ".sageox", "cache", "codedb", "index.db"), []byte("precious"), 0o644))

	// dirty tracked file (uncommitted modification)
	require.NoError(t, os.WriteFile(filepath.Join(cloneDir, "README.md"), []byte("dirty"), 0o644))

	// untracked file
	require.NoError(t, os.WriteFile(filepath.Join(cloneDir, "local-only.txt"), []byte("untracked"), 0o644))

	// committed but unpushed file
	require.NoError(t, os.WriteFile(filepath.Join(cloneDir, "committed-unpushed.txt"), []byte("committed-unpushed"), 0o644))
	cmd = exec.Command("git", "-C", cloneDir, "add", "committed-unpushed.txt")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "git add failed: %s", string(out))
	cmd = exec.Command("git", "-C", cloneDir, "commit", "-m", "add committed-unpushed")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "git commit failed: %s", string(out))
	// intentionally NOT pushed

	// set up scheduler
	isolateCredentials(t)
	projectDir := setupProjectWithConfig(t, "")
	s := newTestScheduler(projectDir)

	ws := WorkspaceState{
		ID:       "ledger-test",
		Type:     WorkspaceTypeLedger,
		Path:     cloneDir,
		CloneURL: cloneURL,
		Exists:   true,
	}

	ctx := context.Background()
	result := s.runBlueGreenGC(ctx, ws)
	assert.Equal(t, gcSuccess, result, "GC reclone should succeed")

	// verify directory and git repo exist
	_, err = os.Stat(filepath.Join(cloneDir, ".git", "HEAD"))
	require.NoError(t, err, ".git/HEAD should exist after GC")

	// verify committed structure survived
	_, err = os.Stat(filepath.Join(cloneDir, "sessions", ".gitkeep"))
	require.NoError(t, err, "sessions/.gitkeep should exist after GC")

	// verify gitignored cache preserved
	content, err := os.ReadFile(filepath.Join(cloneDir, ".sageox", "cache", "codedb", "index.db"))
	require.NoError(t, err, ".sageox/cache/codedb/index.db should survive GC")
	assert.Equal(t, "precious", string(content))

	// verify dirty tracked file restored
	content, err = os.ReadFile(filepath.Join(cloneDir, "README.md"))
	require.NoError(t, err, "README.md should exist after GC")
	assert.Equal(t, "dirty", string(content))

	// verify untracked file restored
	content, err = os.ReadFile(filepath.Join(cloneDir, "local-only.txt"))
	require.NoError(t, err, "local-only.txt should exist after GC")
	assert.Equal(t, "untracked", string(content))

	// verify committed-unpushed file (pushed before reclone, then fetched)
	_, err = os.Stat(filepath.Join(cloneDir, "committed-unpushed.txt"))
	require.NoError(t, err, "committed-unpushed.txt should exist after GC")

	// verify temp dirs cleaned up
	_, err = os.Stat(cloneDir + ".old")
	assert.True(t, os.IsNotExist(err), ".old dir should not exist after GC")
	_, err = os.Stat(cloneDir + ".new")
	assert.True(t, os.IsNotExist(err), ".new dir should not exist after GC")
}

// TestGC_DirtyState_SurvivesReclone verifies that all three forms of dirty state
// (unstaged modification, staged addition, untracked file) survive a blue-green
// GC reclone against the Gitea digital twin.
func TestGC_DirtyState_SurvivesReclone(t *testing.T) {
	if testing.Short() {
		t.Skip("short: Gitea digital twin")
	}

	g := getSharedGitea(t)
	cloneURL := g.createRepo(t, "gc-dirty")

	cloneDir := filepath.Join(t.TempDir(), "repo")
	g.cloneRepo(t, cloneURL, cloneDir)

	// ledger structure required by GC validator
	require.NoError(t, os.MkdirAll(filepath.Join(cloneDir, "sessions"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cloneDir, "sessions", ".gitkeep"), []byte(""), 0o644))
	cmd := exec.Command("git", "-C", cloneDir, "add", "sessions/.gitkeep")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git add failed: %s", string(out))
	cmd = exec.Command("git", "-C", cloneDir, "commit", "-m", "add ledger structure")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "git commit failed: %s", string(out))
	cmd = exec.Command("git", "-C", cloneDir, "push")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "git push failed: %s", string(out))

	// unstaged modification of existing tracked file
	require.NoError(t, os.WriteFile(filepath.Join(cloneDir, "README.md"), []byte("modified-unstaged"), 0o644))

	// staged new file
	require.NoError(t, os.WriteFile(filepath.Join(cloneDir, "staged-new.txt"), []byte("staged-content"), 0o644))
	cmd = exec.Command("git", "-C", cloneDir, "add", "staged-new.txt")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "git add failed: %s", string(out))

	// untracked file
	require.NoError(t, os.WriteFile(filepath.Join(cloneDir, "untracked.txt"), []byte("untracked-content"), 0o644))

	// run GC
	isolateCredentials(t)
	projectDir := setupProjectWithConfig(t, "")
	s := newTestScheduler(projectDir)

	ws := WorkspaceState{
		ID:       "dirty-test",
		Type:     WorkspaceTypeLedger,
		Path:     cloneDir,
		CloneURL: cloneURL,
		Exists:   true,
	}

	result := s.runBlueGreenGC(context.Background(), ws)
	assert.Equal(t, gcSuccess, result, "GC should succeed with dirty state")

	// verify unstaged modification restored
	content, err := os.ReadFile(filepath.Join(cloneDir, "README.md"))
	require.NoError(t, err, "README.md should exist after GC")
	assert.Equal(t, "modified-unstaged", string(content))

	// verify staged file restored
	content, err = os.ReadFile(filepath.Join(cloneDir, "staged-new.txt"))
	require.NoError(t, err, "staged-new.txt should exist after GC")
	assert.Equal(t, "staged-content", string(content))

	// verify untracked file restored
	content, err = os.ReadFile(filepath.Join(cloneDir, "untracked.txt"))
	require.NoError(t, err, "untracked.txt should exist after GC")
	assert.Equal(t, "untracked-content", string(content))
}

// TestGC_CachePreserved_Ledger_RealRemote verifies that multiple cache files
// in .sageox/cache/ survive a blue-green GC reclone byte-for-byte.
func TestGC_CachePreserved_Ledger_RealRemote(t *testing.T) {
	if testing.Short() {
		t.Skip("short: Gitea digital twin")
	}

	g := getSharedGitea(t)
	cloneURL := g.createRepo(t, "gc-cache")

	cloneDir := filepath.Join(t.TempDir(), "ledger")
	g.cloneRepo(t, cloneURL, cloneDir)

	// add sessions/ so it looks like a ledger
	require.NoError(t, os.MkdirAll(filepath.Join(cloneDir, "sessions"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cloneDir, "sessions", ".gitkeep"), []byte(""), 0o644))
	cmd := exec.Command("git", "-C", cloneDir, "add", "sessions/.gitkeep")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git add failed: %s", string(out))
	cmd = exec.Command("git", "-C", cloneDir, "commit", "-m", "add sessions")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "git commit failed: %s", string(out))
	cmd = exec.Command("git", "-C", cloneDir, "push")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "git push failed: %s", string(out))

	// create multiple cache files
	cacheFiles := map[string]string{
		".sageox/cache/codedb/index.db":     "sqlite-data-here",
		".sageox/cache/codedb/bleve.idx":    "bleve-index-bytes",
		".sageox/cache/sync-state.json":     `{"last_sync":"2025-01-01T00:00:00Z"}`,
		".sageox/cache/custom/nested/a.bin": "binary-content-abc",
	}

	for relPath, data := range cacheFiles {
		fullPath := filepath.Join(cloneDir, relPath)
		require.NoError(t, os.MkdirAll(filepath.Dir(fullPath), 0o755))
		require.NoError(t, os.WriteFile(fullPath, []byte(data), 0o644))
	}

	// run GC
	isolateCredentials(t)
	projectDir := setupProjectWithConfig(t, "")
	s := newTestScheduler(projectDir)

	ws := WorkspaceState{
		ID:       "cache-test",
		Type:     WorkspaceTypeLedger,
		Path:     cloneDir,
		CloneURL: cloneURL,
		Exists:   true,
	}

	result := s.runBlueGreenGC(context.Background(), ws)
	assert.Equal(t, gcSuccess, result, "GC should succeed")

	// verify all cache files byte-identical
	for relPath, expected := range cacheFiles {
		fullPath := filepath.Join(cloneDir, relPath)
		content, err := os.ReadFile(fullPath)
		require.NoError(t, err, "%s should survive GC", relPath)
		assert.Equal(t, expected, string(content), "%s should be byte-identical after GC", relPath)
	}
}
