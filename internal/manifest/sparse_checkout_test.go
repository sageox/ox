package manifest

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestComputeSparseSet_DenyDataExcludesData(t *testing.T) {
	cfg := &ManifestConfig{
		Includes: []string{"memory/", ".sageox/"},
		Denies:   []string{"data/"},
	}

	result := ComputeSparseSet(cfg)

	assert.Contains(t, result, "memory/")
	assert.Contains(t, result, ".sageox/")
	assert.NotContains(t, result, "data/")
}

func TestComputeSparseSet_DenyDataBlocksDataSubdirs(t *testing.T) {
	cfg := &ManifestConfig{
		Includes: []string{"data/slack/"},
		Denies:   []string{"data/"},
	}

	result := ComputeSparseSet(cfg)

	assert.Empty(t, result, "deny on parent data/ should block child data/slack/")
}

func TestComputeSparseSet_FallbackConfigExcludesData(t *testing.T) {
	cfg := FallbackConfig()
	result := ComputeSparseSet(cfg)

	for _, path := range result {
		assert.NotEqual(t, "data/", path, "fallback sparse set should not include data/")
		assert.False(t,
			len(path) > 5 && path[:5] == "data/",
			"fallback sparse set should not include data/ subdirectories, got: %s", path,
		)
	}

	// verify known fallback paths are present
	assert.Contains(t, result, "memory/")
	assert.Contains(t, result, ".sageox/")
}

// gitEnv returns environment variables that provide git identity
// so tests don't depend on global git config.
func gitEnv() []string {
	return append(os.Environ(),
		"GIT_AUTHOR_NAME=Test User",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test User",
		"GIT_COMMITTER_EMAIL=test@example.com",
	)
}

// runGit runs a git command in the given directory with test-safe env vars.
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = gitEnv()
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v failed: %s", args, string(out))
}

// initGitRepo creates a git repo at dir with an initial commit containing
// the provided files. fileContents maps relative paths to file content.
func initGitRepo(t *testing.T, dir string, fileContents map[string]string) {
	t.Helper()

	runGit(t, dir, "init")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test User")

	for relPath, content := range fileContents {
		fullPath := filepath.Join(dir, relPath)
		require.NoError(t, os.MkdirAll(filepath.Dir(fullPath), 0o755))
		require.NoError(t, os.WriteFile(fullPath, []byte(content), 0o644))
	}

	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "initial commit")
}

func TestSparseCheckout_DataExcludedFromWorkingTree(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	dir := t.TempDir()

	// create repo with files in data/, memory/, and .sageox/
	initGitRepo(t, dir, map[string]string{
		"data/raw.txt":                  "raw data content",
		"memory/daily/2026-01-01.md":    "daily memory",
		".sageox/config.json":           `{"version": 1}`,
	})

	// compute sparse set from a manifest that denies data/
	cfg := &ManifestConfig{
		Includes: []string{"memory/", ".sageox/"},
		Denies:   []string{"data/"},
	}
	sparseSet := ComputeSparseSet(cfg)
	require.NotEmpty(t, sparseSet)

	// enable sparse checkout
	runGit(t, dir, "sparse-checkout", "init", "--cone")
	runGit(t, dir, append([]string{"sparse-checkout", "set"}, sparseSet...)...)

	// data/raw.txt should NOT exist in the working tree
	_, err := os.Stat(filepath.Join(dir, "data", "raw.txt"))
	assert.True(t, os.IsNotExist(err), "data/raw.txt should not exist in sparse working tree")

	// memory/ files should exist
	_, err = os.Stat(filepath.Join(dir, "memory", "daily", "2026-01-01.md"))
	assert.NoError(t, err, "memory/daily/2026-01-01.md should exist in sparse working tree")

	// .sageox/ files should exist
	_, err = os.Stat(filepath.Join(dir, ".sageox", "config.json"))
	assert.NoError(t, err, ".sageox/config.json should exist in sparse working tree")
}

func TestSparseCheckout_FreshCloneExcludesData(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	// create a bare repo to serve as the remote
	bareDir := t.TempDir()
	runGit(t, bareDir, "init", "--bare")

	// create a working repo, add files, push to bare
	srcDir := t.TempDir()
	initGitRepo(t, srcDir, map[string]string{
		"data/file.txt":     "should be excluded",
		"memory/obs.jsonl":  `{"observation": "test"}`,
		".sageox/soul.md":   "team soul",
	})
	runGit(t, srcDir, "remote", "add", "origin", bareDir)
	runGit(t, srcDir, "push", "origin", "HEAD:main")

	// clone with --sparse from the bare repo
	cloneDir := filepath.Join(t.TempDir(), "clone")
	cmd := exec.Command("git", "clone", "--sparse", "--branch", "main", bareDir, cloneDir)
	cmd.Env = gitEnv()
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git clone --sparse failed: %s", string(out))

	// compute sparse set and apply
	cfg := &ManifestConfig{
		Includes: []string{"memory/", ".sageox/"},
		Denies:   []string{"data/"},
	}
	sparseSet := ComputeSparseSet(cfg)
	require.NotEmpty(t, sparseSet)

	runGit(t, cloneDir, append([]string{"sparse-checkout", "set"}, sparseSet...)...)

	// data/ should not be in the working tree
	_, err = os.Stat(filepath.Join(cloneDir, "data"))
	assert.True(t, os.IsNotExist(err), "data/ directory should not exist in sparse clone")

	// memory/ should be present
	_, err = os.Stat(filepath.Join(cloneDir, "memory", "obs.jsonl"))
	assert.NoError(t, err, "memory/obs.jsonl should exist in sparse clone")
}
