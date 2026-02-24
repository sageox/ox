package gitutil

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHasLockFiles(t *testing.T) {
	t.Run("no lock files", func(t *testing.T) {
		gitDir := filepath.Join(t.TempDir(), ".git")
		require.NoError(t, os.MkdirAll(gitDir, 0755))

		assert.Empty(t, HasLockFiles(gitDir))
	})

	t.Run("all lock types present", func(t *testing.T) {
		gitDir := filepath.Join(t.TempDir(), ".git")
		require.NoError(t, os.MkdirAll(gitDir, 0755))

		for _, lock := range knownLockFiles {
			require.NoError(t, os.WriteFile(filepath.Join(gitDir, lock), []byte{}, 0644))
		}

		found := HasLockFiles(gitDir)
		assert.Len(t, found, len(knownLockFiles))
		for _, lock := range knownLockFiles {
			assert.Contains(t, found, lock)
		}
	})

	t.Run("partial locks", func(t *testing.T) {
		gitDir := filepath.Join(t.TempDir(), ".git")
		require.NoError(t, os.MkdirAll(gitDir, 0755))

		// create all, then remove one
		for _, lock := range knownLockFiles {
			require.NoError(t, os.WriteFile(filepath.Join(gitDir, lock), []byte{}, 0644))
		}
		require.NoError(t, os.Remove(filepath.Join(gitDir, "index.lock")))

		found := HasLockFiles(gitDir)
		assert.Len(t, found, len(knownLockFiles)-1)
		assert.NotContains(t, found, "index.lock")
	})

	t.Run("nonexistent directory", func(t *testing.T) {
		found := HasLockFiles("/nonexistent/path/.git")
		assert.Empty(t, found)
	})
}

func TestIsRebaseInProgress(t *testing.T) {
	t.Run("clean repo", func(t *testing.T) {
		repo := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(repo, ".git"), 0755))

		assert.False(t, IsRebaseInProgress(repo))
	})

	t.Run("rebase-merge exists", func(t *testing.T) {
		repo := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(repo, ".git", "rebase-merge"), 0755))

		assert.True(t, IsRebaseInProgress(repo))
	})

	t.Run("rebase-apply exists", func(t *testing.T) {
		repo := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(repo, ".git", "rebase-apply"), 0755))

		assert.True(t, IsRebaseInProgress(repo))
	})

	t.Run("both exist", func(t *testing.T) {
		repo := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(repo, ".git", "rebase-merge"), 0755))
		require.NoError(t, os.MkdirAll(filepath.Join(repo, ".git", "rebase-apply"), 0755))

		assert.True(t, IsRebaseInProgress(repo))
	})
}

func TestIsSafeForGitOps(t *testing.T) {
	t.Run("clean repo", func(t *testing.T) {
		repo := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(repo, ".git"), 0755))

		assert.NoError(t, IsSafeForGitOps(repo))
	})

	t.Run("lock files present", func(t *testing.T) {
		repo := t.TempDir()
		gitDir := filepath.Join(repo, ".git")
		require.NoError(t, os.MkdirAll(gitDir, 0755))
		require.NoError(t, os.WriteFile(filepath.Join(gitDir, "index.lock"), []byte{}, 0644))

		err := IsSafeForGitOps(repo)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "index.lock")
	})

	t.Run("rebase in progress", func(t *testing.T) {
		repo := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(repo, ".git", "rebase-merge"), 0755))

		err := IsSafeForGitOps(repo)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "rebase")
	})

	t.Run("both lock and rebase", func(t *testing.T) {
		repo := t.TempDir()
		gitDir := filepath.Join(repo, ".git")
		require.NoError(t, os.MkdirAll(filepath.Join(gitDir, "rebase-merge"), 0755))
		require.NoError(t, os.WriteFile(filepath.Join(gitDir, "index.lock"), []byte{}, 0644))

		// lock check runs first, so error should mention locks
		err := IsSafeForGitOps(repo)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "lock")
	})
}

func TestFetchHeadAge(t *testing.T) {
	t.Run("no FETCH_HEAD", func(t *testing.T) {
		repo := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(repo, ".git"), 0755))

		age, ok := FetchHeadAge(repo)
		assert.False(t, ok)
		assert.Zero(t, age)
	})

	t.Run("recent FETCH_HEAD", func(t *testing.T) {
		repo := t.TempDir()
		gitDir := filepath.Join(repo, ".git")
		require.NoError(t, os.MkdirAll(gitDir, 0755))
		require.NoError(t, os.WriteFile(filepath.Join(gitDir, "FETCH_HEAD"), []byte("ref"), 0644))

		age, ok := FetchHeadAge(repo)
		assert.True(t, ok)
		assert.Less(t, age, 2*time.Second) // just created
	})

	t.Run("old FETCH_HEAD", func(t *testing.T) {
		repo := t.TempDir()
		gitDir := filepath.Join(repo, ".git")
		require.NoError(t, os.MkdirAll(gitDir, 0755))

		fetchHead := filepath.Join(gitDir, "FETCH_HEAD")
		require.NoError(t, os.WriteFile(fetchHead, []byte("ref"), 0644))
		oldTime := time.Now().Add(-5 * time.Minute)
		require.NoError(t, os.Chtimes(fetchHead, oldTime, oldTime))

		age, ok := FetchHeadAge(repo)
		assert.True(t, ok)
		assert.Greater(t, age, 4*time.Minute)
	})
}
