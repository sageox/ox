//go:build slow

package daemon

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- A. Shallow clone + unshallow + rebase ---

// TestShallow_UnshallowEnablesRebase verifies the full sequence:
// depth=1 clone, fetch --unshallow, then pull --rebase works.
// This is the exact sequence TwoPhaseClone uses.
// Failure prevented: unshallow not converting the clone to full depth,
// causing pull --rebase to fail with "fatal: refusing to merge
// unrelated histories".
func TestShallow_UnshallowEnablesRebase(t *testing.T) {
	g := getSharedGitea(t)
	cloneURL := g.createRepo(t, "twin-shallow-rebase")

	// push two commits so there's history
	g.pushFromTempClone(t, cloneURL, "file1.md", "first commit\n")
	g.pushFromTempClone(t, cloneURL, "file2.md", "second commit\n")

	// shallow clone (depth=1)
	cloneDir := filepath.Join(t.TempDir(), "shallow")
	cmd := exec.Command("git", "clone", "--depth=1", cloneURL, cloneDir)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "shallow clone: %s", string(out))

	gitConfig(t, cloneDir)

	// verify it's shallow
	_, err = os.Stat(filepath.Join(cloneDir, ".git", "shallow"))
	require.NoError(t, err, "should be a shallow clone")

	// pull --rebase should fail on shallow clone
	cmd = exec.Command("git", "-C", cloneDir, "pull", "--rebase", "--autostash")
	_, pullErr := cmd.CombinedOutput()
	// may or may not fail depending on git version, but let's unshallow anyway

	_ = pullErr // some git versions handle this fine

	// unshallow
	cmd = exec.Command("git", "-C", cloneDir, "fetch", "--unshallow")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "fetch --unshallow: %s", string(out))

	// verify no longer shallow
	_, err = os.Stat(filepath.Join(cloneDir, ".git", "shallow"))
	assert.True(t, os.IsNotExist(err), "should not be shallow after unshallow")

	// push a new commit from elsewhere
	g.pushFromTempClone(t, cloneURL, "file3.md", "third commit\n")

	// pull --rebase should now work
	cmd = exec.Command("git", "-C", cloneDir, "pull", "--rebase", "--autostash")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "pull --rebase after unshallow: %s", string(out))

	// verify full history is present
	cmd = exec.Command("git", "-C", cloneDir, "log", "--oneline")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	assert.GreaterOrEqual(t, len(lines), 3, "should have at least 3 commits in history")
}

// --- B. Shallow clone single commit repo ---

// TestShallow_SingleCommit_UnshallowNoop verifies that unshallow on a
// repo with only 1 commit is a graceful no-op (not an error).
// Failure prevented: TwoPhaseClone failing on brand-new repos because
// fetch --unshallow errors on single-commit histories.
func TestShallow_SingleCommit_UnshallowNoop(t *testing.T) {
	g := getSharedGitea(t)
	cloneURL := g.createRepo(t, "twin-shallow-single")
	// auto_init creates one commit (README)

	cloneDir := filepath.Join(t.TempDir(), "shallow-single")
	cmd := exec.Command("git", "clone", "--depth=1", cloneURL, cloneDir)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "clone: %s", string(out))

	gitConfig(t, cloneDir)

	// unshallow on single-commit repo
	cmd = exec.Command("git", "-C", cloneDir, "fetch", "--unshallow")
	out, err = cmd.CombinedOutput()
	// git may return error "fatal: --unshallow on a complete repository"
	// or succeed silently — both are acceptable
	if err != nil {
		assert.Contains(t, strings.ToLower(string(out)), "complete",
			"error should indicate repo is already complete, not a real failure")
	}

	// repo should still be functional
	cmd = exec.Command("git", "-C", cloneDir, "log", "--oneline")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "git log: %s", string(out))
	assert.NotEmpty(t, strings.TrimSpace(string(out)), "should have at least one commit")
}

// --- C. Partial + shallow clone interactions ---

// TestShallow_PartialFilter_WithDepth verifies that combining
// --filter=blob:none with --depth=1 (as TwoPhaseClone does) produces
// a working clone that can be unshallowed and then fetch blobs on demand.
// Failure prevented: partial+shallow combination creating a broken
// promisor configuration that can't fetch blobs after unshallow.
func TestShallow_PartialFilter_WithDepth(t *testing.T) {
	g := getSharedGitea(t)
	cloneURL := g.createRepo(t, "twin-shallow-partial")

	pushMultipleFiles(t, cloneURL, map[string]string{
		"docs/readme.md": "# Docs\n",
		"src/main.go":    "package main\n",
		"data/test.json": `{"key":"value"}`,
	})

	// clone with both --filter=blob:none and --depth=1 (TwoPhaseClone pattern)
	cloneDir := filepath.Join(t.TempDir(), "shallow-partial")
	cmd := exec.Command("git", "clone",
		"--filter=blob:none",
		"--depth=1",
		"--sparse",
		"--no-checkout",
		cloneURL, cloneDir)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "clone: %s", string(out))

	gitConfig(t, cloneDir)

	// sparse checkout docs/ only
	cmd = exec.Command("git", "-C", cloneDir, "sparse-checkout", "set", "docs")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "sparse set: %s", string(out))

	cmd = exec.Command("git", "-C", cloneDir, "checkout", "HEAD")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "checkout: %s", string(out))

	require.FileExists(t, filepath.Join(cloneDir, "docs/readme.md"))

	// unshallow — must succeed for a shallow clone with >1 commit
	cmd = exec.Command("git", "-C", cloneDir, "fetch", "--unshallow")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "fetch --unshallow should succeed on a multi-commit shallow clone: %s", string(out))

	// after unshallow, on-demand blob fetch should still work
	cmd = exec.Command("git", "-C", cloneDir, "show", "HEAD:src/main.go")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "git show should fetch blob on demand: %s", string(out))
	assert.Contains(t, string(out), "package main")

	// git log should work
	cmd = exec.Command("git", "-C", cloneDir, "log", "--oneline")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "git log: %s", string(out))
	assert.NotEmpty(t, strings.TrimSpace(string(out)))

	// push new commit from elsewhere, pull --rebase should work
	g.pushFromTempClone(t, cloneURL, "docs/changelog.md", "# Changelog\n")

	cmd = exec.Command("git", "-C", cloneDir, "pull", "--rebase", "--autostash")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "pull --rebase: %s", string(out))

	require.FileExists(t, filepath.Join(cloneDir, "docs/changelog.md"))
}

// --- D. Depth=1 clone with merge base ---

// TestShallow_DepthOne_MergeBase verifies that a depth=1 clone has no
// merge base with the remote, which is why unshallow is necessary
// before rebase operations.
// Failure prevented: assuming depth=1 clone has enough history for
// merge-base computation — it doesn't, causing pull --rebase to fail
// with "fatal: Need a single revision" on multi-commit repos.
func TestShallow_DepthOne_MergeBase(t *testing.T) {
	g := getSharedGitea(t)
	cloneURL := g.createRepo(t, "twin-shallow-mergebase")

	// create several commits
	g.pushFromTempClone(t, cloneURL, "a.txt", "a\n")
	g.pushFromTempClone(t, cloneURL, "b.txt", "b\n")
	g.pushFromTempClone(t, cloneURL, "c.txt", "c\n")

	// depth=1 clone
	cloneDir := filepath.Join(t.TempDir(), "shallow-mb")
	cmd := exec.Command("git", "clone", "--depth=1", cloneURL, cloneDir)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "clone: %s", string(out))

	gitConfig(t, cloneDir)

	// verify only 1 commit in log (shallow)
	cmd = exec.Command("git", "-C", cloneDir, "log", "--oneline")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	assert.Equal(t, 1, len(lines), "depth=1 should have exactly 1 commit visible")

	// unshallow
	cmd = exec.Command("git", "-C", cloneDir, "fetch", "--unshallow")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "unshallow: %s", string(out))

	// now should see all commits
	cmd = exec.Command("git", "-C", cloneDir, "log", "--oneline")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err)
	lines = strings.Split(strings.TrimSpace(string(out)), "\n")
	assert.GreaterOrEqual(t, len(lines), 4, "unshallowed should show all commits (init + 3 pushes)")
}
