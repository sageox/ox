package index

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func replaceFormatVersion(content, version string) string {
	return strings.Replace(content,
		"repositoryformatversion = 0",
		"repositoryformatversion = "+version, 1)
}

func TestPlainOpenTolerant_NormalRepo(t *testing.T) {
	t.Parallel()
	dir, _ := initGitRepo(t, 1)

	repo, err := plainOpenTolerant(dir)
	require.NoError(t, err)
	assert.NotNil(t, repo)
}

// TestPlainOpenTolerant_RepoWithExtensions verifies that repos with extensions
// and repositoryformatversion=1 open correctly. go-git v6 handles known
// extensions natively (objectformat, worktreeconfig).
func TestPlainOpenTolerant_RepoWithExtensions(t *testing.T) {
	t.Parallel()
	dir, _ := initGitRepo(t, 1)

	gitConfig := filepath.Join(dir, ".git", "config")
	content, err := os.ReadFile(gitConfig)
	require.NoError(t, err)

	// extensions require repositoryformatversion=1
	newContent := replaceFormatVersion(string(content), "1")
	newContent += "\n[extensions]\n\tobjectformat = sha1\n"
	require.NoError(t, os.WriteFile(gitConfig, []byte(newContent), 0o644))

	repo, err := plainOpenTolerant(dir)
	require.NoError(t, err)
	assert.NotNil(t, repo)
}

func TestPlainOpenTolerant_FormatV1WithUnknownExtension(t *testing.T) {
	t.Parallel()
	dir, _ := initGitRepo(t, 1)

	gitConfig := filepath.Join(dir, ".git", "config")
	content, err := os.ReadFile(gitConfig)
	require.NoError(t, err)

	newContent := replaceFormatVersion(string(content), "1")
	newContent += "\n[extensions]\n\tobjectformat = sha256\n"
	require.NoError(t, os.WriteFile(gitConfig, []byte(newContent), 0o644))

	repo, err := plainOpenTolerant(dir)
	require.NoError(t, err)
	assert.NotNil(t, repo)
}

func TestPlainOpenTolerant_NonRepoPath(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	repo, err := plainOpenTolerant(dir)
	assert.Error(t, err)
	assert.Nil(t, repo)
}

// TestPlainOpenTolerant_WorktreeWithExtensions verifies that linked worktrees
// whose main repo has extensions can be opened. The worktree's .git is a file
// pointing to the main repo, so extensions are read from the main repo's config.
func TestPlainOpenTolerant_WorktreeWithExtensions(t *testing.T) {
	t.Parallel()
	mainDir, _ := initGitRepo(t, 1)

	worktreeDir := createLinkedWorktree(t, mainDir, "ext-test")

	// extensions require repositoryformatversion=1
	gitConfig := filepath.Join(mainDir, ".git", "config")
	content, err := os.ReadFile(gitConfig)
	require.NoError(t, err)

	newContent := replaceFormatVersion(string(content), "1")
	newContent += "\n[extensions]\n\tobjectformat = sha1\n"
	require.NoError(t, os.WriteFile(gitConfig, []byte(newContent), 0o644))

	repoOpenPath, _ := resolveGitDir(worktreeDir)
	repo, err := plainOpenTolerant(repoOpenPath)
	require.NoError(t, err)
	assert.NotNil(t, repo)
}
