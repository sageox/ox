//go:build slow

package daemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/gitserver"
	"github.com/stretchr/testify/require"
)

// pushMultipleFiles clones a repo, writes all files in one commit, and pushes.
// files is a map of relative path → content.
func pushMultipleFiles(t *testing.T, cloneURL string, files map[string]string) {
	t.Helper()

	tmp := t.TempDir()
	cloneDir := filepath.Join(tmp, "multi-push")

	cmd := exec.Command("git", "clone", cloneURL, cloneDir)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git clone failed: %s", string(out))

	gitConfig(t, cloneDir)

	for relPath, content := range files {
		absPath := filepath.Join(cloneDir, relPath)
		require.NoError(t, os.MkdirAll(filepath.Dir(absPath), 0o755))
		require.NoError(t, os.WriteFile(absPath, []byte(content), 0o644))
	}

	cmd = exec.Command("git", "-C", cloneDir, "add", "-A")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "git add failed: %s", string(out))

	cmd = exec.Command("git", "-C", cloneDir, "commit", "-m", "seed repo with test files")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "git commit failed: %s", string(out))

	cmd = exec.Command("git", "-C", cloneDir, "push")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "git push failed: %s", string(out))
}

// --- A. Sparse checkout with manifest ---

// TestTwoPhaseClone_BasicTeamContext verifies that TwoPhaseClone materializes
// included paths from the manifest while excluding denied paths.
// Failure prevented: manifest deny rules silently ignored, leaking secrets
// into agent-visible sparse checkouts.
func TestTwoPhaseClone_BasicTeamContext(t *testing.T) {
	g := getSharedGitea(t)
	cloneURL := g.createRepo(t, "twin-clone-basic")

	pushMultipleFiles(t, cloneURL, map[string]string{
		".sageox/sync.manifest": "version 1\ninclude docs/\ninclude memory/\ndeny secrets/\n",
		"SOUL.md":               "# Soul\nTeam identity.\n",
		"memory/daily/obs.md":   "observation one\n",
		"docs/guide.md":         "# Guide\nHow to do things.\n",
		"secrets/key.txt":       "super-secret-value\n",
	})

	dest := filepath.Join(t.TempDir(), "clone-basic")
	result, err := gitserver.TwoPhaseClone(context.Background(), cloneURL, dest)
	require.NoError(t, err)

	// root file materialized via /* pattern
	require.FileExists(t, filepath.Join(dest, "SOUL.md"))

	// included directories materialized
	require.FileExists(t, filepath.Join(dest, "memory/daily/obs.md"))
	require.FileExists(t, filepath.Join(dest, "docs/guide.md"))

	// denied path must NOT exist
	_, err = os.Stat(filepath.Join(dest, "secrets/key.txt"))
	require.True(t, os.IsNotExist(err), "denied path secrets/key.txt should not be materialized")

	// manifest itself materialized in phase 1
	require.FileExists(t, filepath.Join(dest, ".sageox/sync.manifest"))

	// result carries parsed manifest
	require.NotNil(t, result.ManifestConfig)
	require.Contains(t, result.ManifestConfig.Includes, "docs/")
	require.Contains(t, result.ManifestConfig.Includes, "memory/")
	require.Contains(t, result.ManifestConfig.Denies, "secrets/")

	// sparse paths include expected entries
	require.Contains(t, result.SparsePaths, ".sageox/")
}

// --- B. Unshallow enables pull --rebase ---

// TestTwoPhaseClone_PullRebaseAfterClone verifies that the unshallow step
// leaves the clone in a state where git pull --rebase --autostash works.
// Failure prevented: shallow clone breaking daemon sync pulls after initial
// TwoPhaseClone, causing silent stale team context.
func TestTwoPhaseClone_PullRebaseAfterClone(t *testing.T) {
	g := getSharedGitea(t)
	cloneURL := g.createRepo(t, "twin-clone-pull")

	pushMultipleFiles(t, cloneURL, map[string]string{
		".sageox/sync.manifest": "version 1\ninclude docs/\n",
		"docs/initial.md":       "initial content\n",
	})

	dest := filepath.Join(t.TempDir(), "clone-pull")
	_, err := gitserver.TwoPhaseClone(context.Background(), cloneURL, dest)
	require.NoError(t, err)

	gitConfig(t, dest)

	// push a new commit from a separate clone
	g.pushFromTempClone(t, cloneURL, "docs/update.md", "new content after clone\n")

	// pull --rebase must succeed (requires unshallowed history)
	cmd := exec.Command("git", "-C", dest, "pull", "--rebase", "--autostash")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git pull --rebase failed after TwoPhaseClone: %s", string(out))

	require.FileExists(t, filepath.Join(dest, "docs/update.md"))
}

// --- C. Graceful fallback without manifest ---

// TestTwoPhaseClone_NoManifest_FallsBack verifies that TwoPhaseClone succeeds
// with default sparse paths when the repo has no .sageox/sync.manifest.
// Failure prevented: NPE or clone failure on repos that lack a manifest,
// blocking first-time setup for new teams.
func TestTwoPhaseClone_NoManifest_FallsBack(t *testing.T) {
	g := getSharedGitea(t)
	cloneURL := g.createRepo(t, "twin-clone-no-manifest")

	pushMultipleFiles(t, cloneURL, map[string]string{
		".sageox/config.json": `{"version": 1}`,
		"SOUL.md":             "# Soul\n",
	})

	dest := filepath.Join(t.TempDir(), "clone-no-manifest")
	result, err := gitserver.TwoPhaseClone(context.Background(), cloneURL, dest)
	require.NoError(t, err)

	require.NotNil(t, result.ManifestConfig)
	require.NotEmpty(t, result.SparsePaths)

	// .sageox/ always materialized
	require.FileExists(t, filepath.Join(dest, ".sageox/config.json"))
}

// --- D. LFS config cleanup ---

// TestTwoPhaseClone_LFSConfigStripped verifies that TwoPhaseClone removes any
// [lfs] section from .git/config that git-lfs may inject during clone.
// Failure prevented: LFS config causing HTTP 403 on push to servers that
// don't expect LFS-aware clients.
func TestTwoPhaseClone_LFSConfigStripped(t *testing.T) {
	g := getSharedGitea(t)
	cloneURL := g.createRepo(t, "twin-clone-lfs-strip")

	pushMultipleFiles(t, cloneURL, map[string]string{
		".sageox/sync.manifest": "version 1\ninclude docs/\n",
		"docs/readme.md":        "content\n",
	})

	dest := filepath.Join(t.TempDir(), "clone-lfs")
	_, err := gitserver.TwoPhaseClone(context.Background(), cloneURL, dest)
	require.NoError(t, err)

	gitConfigPath := filepath.Join(dest, ".git", "config")
	configBytes, err := os.ReadFile(gitConfigPath)
	require.NoError(t, err)

	configStr := strings.ToLower(string(configBytes))
	require.NotContains(t, configStr, "[lfs]",
		".git/config should not contain [lfs] after TwoPhaseClone")
	require.NotContains(t, configStr, "lfs.repositoryformatversion",
		".git/config should not contain lfs.repositoryformatversion after TwoPhaseClone")
}
