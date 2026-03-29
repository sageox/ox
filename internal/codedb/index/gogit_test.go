package index

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v6/plumbing/object"
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

// TestPlainOpenTolerant_KeepDescriptors verifies that plainOpenTolerant uses
// KeepDescriptors for better performance. The repo must be readable (HEAD,
// tree, and blob objects all accessible) via the KeepDescriptors path.
func TestPlainOpenTolerant_KeepDescriptors(t *testing.T) {
	t.Parallel()
	dir, _ := initGitRepo(t, 3) // 3 commits → packfile after gc

	repo, err := plainOpenTolerant(dir)
	require.NoError(t, err)
	require.NotNil(t, repo)

	// Verify HEAD is resolvable
	head, err := repo.Head()
	require.NoError(t, err, "HEAD must be resolvable via KeepDescriptors path")
	require.False(t, head.Hash().IsZero())

	// Verify commit object is readable
	commit, err := repo.CommitObject(head.Hash())
	require.NoError(t, err, "commit object must be readable via KeepDescriptors path")

	// Verify tree and blob objects are readable — this exercises the packfile path
	tree, err := repo.TreeObject(commit.TreeHash)
	require.NoError(t, err, "tree object must be readable")

	err = tree.Files().ForEach(func(f *object.File) error {
		_, rErr := f.Contents()
		return rErr
	})
	require.NoError(t, err, "blob contents must be readable via KeepDescriptors path")
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
