package repotools

import (
	"os/exec"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGitHubOrigin_EveryRemoteFormNamesTheSameRepo — Failure prevented: a
// contributor who cloned over ssh gets no public prime while an https clone
// of the same project does, because only one remote form was parsed.
func TestGitHubOrigin_EveryRemoteFormNamesTheSameRepo(t *testing.T) {
	if !IsInstalled(VCSGit) {
		t.Skip("git not installed")
	}
	for _, remote := range []string{
		"https://github.com/Readest/Readest.git",
		"git@github.com:readest/readest.git",
		"ssh://git@github.com/readest/readest",
		"ssh://git@github.com:22/readest/readest.git",
		"https://github.com/readest/readest/",
	} {
		t.Run(remote, func(t *testing.T) {
			dir := gitRepoWithOrigin(t, remote)
			owner, name, ok := GitHubOrigin(dir)
			require.True(t, ok)
			assert.Equal(t, "readest", owner)
			assert.Equal(t, "readest", name)
		})
	}
}

// TestGitHubOrigin_NotAGitHubRepo — Failure prevented: a GitLab or
// self-hosted remote, a remote with credentials in it, or a repo with no
// origin is sent to SageOx as if it were a GitHub project.
func TestGitHubOrigin_NotAGitHubRepo(t *testing.T) {
	if !IsInstalled(VCSGit) {
		t.Skip("git not installed")
	}
	for _, remote := range []string{
		"git@gitlab.com:readest/readest.git",
		"https://x-access-token:secret@github.com/readest/readest.git",
		"https://github.com/readest",
		"https://github.com/readest/readest/tree/main",
		"/srv/git/readest.git",
	} {
		t.Run(remote, func(t *testing.T) {
			_, _, ok := GitHubOrigin(gitRepoWithOrigin(t, remote))
			assert.False(t, ok)
		})
	}
	t.Run("no origin", func(t *testing.T) {
		dir := t.TempDir()
		runGit(t, dir, "init")
		_, _, ok := GitHubOrigin(dir)
		assert.False(t, ok)
	})
}

func gitRepoWithOrigin(t *testing.T, remote string) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init")
	runGit(t, dir, "remote", "add", "origin", remote)
	return dir
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
}
