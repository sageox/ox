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

func TestPlainOpenTolerant_RepoWithExtensions(t *testing.T) {
	t.Parallel()
	dir, _ := initGitRepo(t, 1)

	// Append an extension that go-git doesn't recognize
	gitConfig := filepath.Join(dir, ".git", "config")
	f, err := os.OpenFile(gitConfig, os.O_APPEND|os.O_WRONLY, 0o644)
	require.NoError(t, err)
	_, err = f.WriteString("\n[extensions]\n\tobjectformat = sha1\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())

	repo, err := plainOpenTolerant(dir)
	require.NoError(t, err)
	assert.NotNil(t, repo)
}

func TestPlainOpenTolerant_FormatV1WithUnknownExtension(t *testing.T) {
	t.Parallel()
	dir, _ := initGitRepo(t, 1)

	// Directly edit .git/config to set repositoryformatversion = 1 with extension.
	// We write the file directly because `git config` may reject format version changes.
	gitConfig := filepath.Join(dir, ".git", "config")
	content, err := os.ReadFile(gitConfig)
	require.NoError(t, err)

	// Replace repositoryformatversion = 0 with 1 and add extensions
	newContent := string(content)
	newContent = newContent + "\n[extensions]\n\tobjectformat = sha256\n"
	// Also bump format version in [core] section
	newContent = replaceFormatVersion(newContent, "1")
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

func TestPlainOpenTolerant_WorktreeWithExtensions(t *testing.T) {
	t.Parallel()
	mainDir, _ := initGitRepo(t, 1)

	worktreeDir := createLinkedWorktree(t, mainDir, "ext-test")

	// Add extensions to the main repo's .git/config
	gitConfig := filepath.Join(mainDir, ".git", "config")
	f, err := os.OpenFile(gitConfig, os.O_APPEND|os.O_WRONLY, 0o644)
	require.NoError(t, err)
	_, err = f.WriteString("\n[extensions]\n\tobjectformat = sha1\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())

	// The worktree's .git is a file pointing to the main repo.
	// plainOpenTolerant resolves this via resolveGitDir (existing function
	// in indexer.go) which follows commondir to the main repo. Since the
	// plan calls plainOpenTolerant on the resolved main repo path, test
	// that opening the main repo path works with extensions.
	repoOpenPath, _ := resolveGitDir(worktreeDir)
	repo, err := plainOpenTolerant(repoOpenPath)
	require.NoError(t, err)
	assert.NotNil(t, repo)
}
