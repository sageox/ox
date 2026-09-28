package repotools

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRepoSlug_RemoteFormsAndFallback(t *testing.T) {
	t.Run("directory fallback", func(t *testing.T) {
		repo := filepath.Join(t.TempDir(), "local-only")
		require.NoError(t, os.Mkdir(repo, 0o755))
		require.NoError(t, runRepoSlugGit(repo, "init", "-q"))
		slug, authoritative := RepoSlugFromRemote(repo)
		require.False(t, authoritative)
		require.Empty(t, slug)
		require.Equal(t, "local-only", RepoSlug(repo))
	})

	for _, tc := range []struct {
		name   string
		remote string
	}{
		{name: "https", remote: "https://github.com/acme/widget.git"},
		{name: "ssh", remote: "git@github.com:acme/widget.git"},
		{name: "without suffix", remote: "https://github.com/acme/widget"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			require.NoError(t, runRepoSlugGit(repo, "init", "-q"))
			require.NoError(t, runRepoSlugGit(repo, "remote", "add", "origin", tc.remote))
			slug, authoritative := RepoSlugFromRemote(repo)
			require.True(t, authoritative)
			require.Equal(t, "acme/widget", slug)
			require.Equal(t, slug, RepoSlug(repo))
		})
	}
}

func runRepoSlugGit(dir string, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	return cmd.Run()
}
