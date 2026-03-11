package codedb

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sageox/ox/internal/codedb/index"
)

// initTestRepo creates a git repo with committed Go files.
func initTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), // safe: git CLI in temp dir needs inherited PATH
			"GIT_AUTHOR_NAME=test",
			"GIT_AUTHOR_EMAIL=test@test.com",
			"GIT_COMMITTER_NAME=test",
			"GIT_COMMITTER_EMAIL=test@test.com",
		)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}

	run("init", "-b", "main")

	require.NoError(t, os.WriteFile(filepath.Join(dir, "committed.go"),
		[]byte("package main\n// committed_sentinel_comment\nfunc CommittedFunc() {}\n"), 0o644))
	run("add", "committed.go")
	run("commit", "-m", "initial commit")

	return dir
}

// buildAndAttachDirty builds the on-disk dirty index and attaches it via the DB facade.
func buildAndAttachDirty(t *testing.T, db *DB, repoDir string) int {
	t.Helper()
	n, err := db.BuildDirtyIndex(context.Background(), repoDir, index.IndexOptions{})
	require.NoError(t, err)
	if n > 0 {
		require.NoError(t, db.AttachDirtyIndex(repoDir))
		t.Cleanup(func() { db.DetachDirtyOverlay() })
	}
	return n
}

func TestSearch_DirtyFileAppearsInResults(t *testing.T) {
	t.Parallel()
	repoDir := initTestRepo(t)
	dataDir := filepath.Join(t.TempDir(), "codedb")
	require.NoError(t, os.MkdirAll(dataDir, 0o755))

	db, err := Open(dataDir)
	require.NoError(t, err)
	defer db.Close()

	require.NoError(t, db.IndexLocalRepo(context.Background(), repoDir, index.IndexOptions{}))

	// write a dirty file with unique content
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "dirty.go"),
		[]byte("package main\n// xyzzy_unique_dirty_token\nfunc DirtySearch() {}\n"), 0o644))

	n := buildAndAttachDirty(t, db, repoDir)
	assert.Equal(t, 1, n)

	// search for the dirty-only content
	results, err := db.Search(context.Background(), "xyzzy_unique_dirty_token")
	require.NoError(t, err)
	require.NotEmpty(t, results, "dirty file content should appear in search results")
	assert.Equal(t, "dirty.go", results[0].FilePath)
}

func TestSearch_DirtyOnlyResult(t *testing.T) {
	t.Parallel()
	repoDir := initTestRepo(t)
	dataDir := filepath.Join(t.TempDir(), "codedb")
	require.NoError(t, os.MkdirAll(dataDir, 0o755))

	db, err := Open(dataDir)
	require.NoError(t, err)
	defer db.Close()

	require.NoError(t, db.IndexLocalRepo(context.Background(), repoDir, index.IndexOptions{}))

	// write dirty content that has no match in committed files
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "only_dirty.go"),
		[]byte("package main\nfunc AbsolutelyNowhere_qwfp() {}\n"), 0o644))

	buildAndAttachDirty(t, db, repoDir)

	results, err := db.Search(context.Background(), "AbsolutelyNowhere_qwfp")
	require.NoError(t, err)
	require.Len(t, results, 1, "only the dirty file should match")
	assert.Equal(t, "only_dirty.go", results[0].FilePath)
}

func TestSearch_CommittedResultsWithoutOverlay(t *testing.T) {
	t.Parallel()
	repoDir := initTestRepo(t)
	dataDir := filepath.Join(t.TempDir(), "codedb")
	require.NoError(t, os.MkdirAll(dataDir, 0o755))

	db, err := Open(dataDir)
	require.NoError(t, err)
	defer db.Close()

	require.NoError(t, db.IndexLocalRepo(context.Background(), repoDir, index.IndexOptions{}))

	// search WITHOUT attaching overlay — should still work for committed content
	results, err := db.Search(context.Background(), "committed_sentinel_comment")
	require.NoError(t, err)
	require.NotEmpty(t, results, "committed content should be searchable without overlay")
}

func TestSearch_DetachRemovesDirtyResults(t *testing.T) {
	t.Parallel()
	repoDir := initTestRepo(t)
	dataDir := filepath.Join(t.TempDir(), "codedb")
	require.NoError(t, os.MkdirAll(dataDir, 0o755))

	db, err := Open(dataDir)
	require.NoError(t, err)
	defer db.Close()

	require.NoError(t, db.IndexLocalRepo(context.Background(), repoDir, index.IndexOptions{}))

	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "ephemeral.go"),
		[]byte("package main\nfunc EphemeralGhost_ztmk() {}\n"), 0o644))

	// build dirty index and attach
	buildAndAttachDirty(t, db, repoDir)

	results, err := db.Search(context.Background(), "EphemeralGhost_ztmk")
	require.NoError(t, err)
	require.NotEmpty(t, results, "dirty result should exist before detach")

	// detach overlay
	db.DetachDirtyOverlay()

	// search again — dirty result should be gone
	results, err = db.Search(context.Background(), "EphemeralGhost_ztmk")
	require.NoError(t, err)
	assert.Empty(t, results, "dirty results should disappear after detach")
}

func TestSearch_DirtyResultHasCorrectLanguage(t *testing.T) {
	t.Parallel()
	repoDir := initTestRepo(t)
	dataDir := filepath.Join(t.TempDir(), "codedb")
	require.NoError(t, os.MkdirAll(dataDir, 0o755))

	db, err := Open(dataDir)
	require.NoError(t, err)
	defer db.Close()

	require.NoError(t, db.IndexLocalRepo(context.Background(), repoDir, index.IndexOptions{}))

	// create dirty files in different languages
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "dirty.py"),
		[]byte("# language_detect_sentinel_py\ndef dirty_python(): pass\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "dirty.ts"),
		[]byte("// language_detect_sentinel_ts\nfunction dirtyTs(): void {}\n"), 0o644))

	buildAndAttachDirty(t, db, repoDir)

	// verify Python detection
	results, err := db.Search(context.Background(), "language_detect_sentinel_py")
	require.NoError(t, err)
	require.NotEmpty(t, results)
	assert.Equal(t, "python", results[0].Language)

	// verify TypeScript detection
	results, err = db.Search(context.Background(), "language_detect_sentinel_ts")
	require.NoError(t, err)
	require.NotEmpty(t, results)
	assert.Equal(t, "typescript", results[0].Language)
}
