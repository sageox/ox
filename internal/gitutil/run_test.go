package gitutil

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	t.Run("git version succeeds", func(t *testing.T) {
		output, err := RunGit(context.Background(), "", "version")
		assert.NoError(t, err)
		assert.Contains(t, output, "git version")
	})

	t.Run("invalid command returns error", func(t *testing.T) {
		_, err := RunGit(context.Background(), "", "not-a-real-command")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "git not-a-real-command")
	})

	t.Run("canceled context returns error", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel() // cancel immediately

		_, err := RunGit(ctx, "", "version")
		assert.Error(t, err)
	})

	t.Run("timeout context returns error", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Nanosecond)
		defer cancel()
		time.Sleep(1 * time.Millisecond) // ensure timeout fires

		_, err := RunGit(ctx, "", "version")
		assert.Error(t, err)
	})

	t.Run("with repo path", func(t *testing.T) {
		repo := t.TempDir()
		cmd := exec.Command("git", "-C", repo, "init", "--quiet")
		require.NoError(t, cmd.Run())

		output, err := RunGit(context.Background(), repo, "status", "--porcelain")
		assert.NoError(t, err)
		// fresh repo has no output
		assert.Empty(t, output)
	})

	t.Run("without repo path omits -C flag", func(t *testing.T) {
		// git version works without -C
		output, err := RunGit(context.Background(), "", "version")
		assert.NoError(t, err)
		assert.Contains(t, output, "git version")
	})

	t.Run("output is auto-sanitized", func(t *testing.T) {
		// create a repo with a remote that has credentials embedded
		repo := t.TempDir()
		cmd := exec.Command("git", "-C", repo, "init", "--quiet")
		require.NoError(t, cmd.Run())

		// set a remote with embedded credentials
		cmd = exec.Command("git", "-C", repo, "remote", "add", "origin",
			"https://oauth2:secret-token@gitlab.com/org/repo.git")
		require.NoError(t, cmd.Run())

		// git remote -v will show the credential URL
		output, err := RunGit(context.Background(), repo, "remote", "-v")
		assert.NoError(t, err)
		assert.NotContains(t, output, "secret-token")
		assert.Contains(t, output, "oauth2:***@")
	})

	t.Run("error output is also sanitized", func(t *testing.T) {
		// create a repo pointing to a nonexistent remote with credentials
		repo := t.TempDir()
		cmd := exec.Command("git", "-C", repo, "init", "--quiet")
		require.NoError(t, cmd.Run())
		cmd = exec.Command("git", "-C", repo, "remote", "add", "origin",
			"https://oauth2:secret-token@nonexistent.example.com/repo.git")
		require.NoError(t, cmd.Run())

		// create a file and commit so we have something to push
		require.NoError(t, os.WriteFile(filepath.Join(repo, "file.txt"), []byte("test"), 0644))
		cmd = exec.Command("git", "-C", repo, "add", "file.txt")
		require.NoError(t, cmd.Run())
		cmd = exec.Command("git", "-C", repo, "commit", "-m", "init", "--no-verify")
		require.NoError(t, cmd.Run())

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		_, err := RunGit(ctx, repo, "push", "--quiet")
		if err != nil {
			// the error message should not contain the token
			assert.NotContains(t, err.Error(), "secret-token")
		}
	})
}
