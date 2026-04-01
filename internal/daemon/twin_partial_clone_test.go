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

// --- A. On-demand blob fetch in partial clone ---

// TestPartialClone_OnDemandFetch_OutsideSparse verifies that accessing
// a file outside the sparse checkout in a --filter=blob:none clone
// triggers an on-demand fetch from the promisor remote.
// Failure prevented: TwoPhaseClone or CloneWithSparseCheckout creating
// clones where cat-file / show fails on non-materialized blobs,
// breaking tools that read arbitrary paths.
func TestPartialClone_OnDemandFetch_OutsideSparse(t *testing.T) {
	g := getSharedGitea(t)
	cloneURL := g.createRepo(t, "twin-partial-ondemand")

	// push files in multiple directories
	pushMultipleFiles(t, cloneURL, map[string]string{
		"docs/readme.md":        "# Docs\n",
		"src/main.go":           "package main\n",
		"assets/large-file.bin": strings.Repeat("x", 1024),
	})

	// clone with partial filter + sparse
	cloneDir := filepath.Join(t.TempDir(), "partial-clone")
	cmd := exec.Command("git", "clone",
		"--filter=blob:none",
		"--sparse",
		"--no-checkout",
		cloneURL, cloneDir)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "partial clone: %s", string(out))

	gitConfig(t, cloneDir)

	// sparse checkout only docs/
	cmd = exec.Command("git", "-C", cloneDir, "sparse-checkout", "set", "docs")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "sparse-checkout set: %s", string(out))

	cmd = exec.Command("git", "-C", cloneDir, "checkout", "HEAD")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "checkout: %s", string(out))

	// docs/ should be materialized
	require.FileExists(t, filepath.Join(cloneDir, "docs/readme.md"))

	// src/ and assets/ should NOT be on disk (outside sparse)
	_, err = os.Stat(filepath.Join(cloneDir, "src/main.go"))
	require.True(t, os.IsNotExist(err), "src/ should not be materialized")

	// but git show should still work via on-demand fetch
	cmd = exec.Command("git", "-C", cloneDir, "show", "HEAD:src/main.go")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "git show should fetch blob on demand: %s", string(out))
	assert.Contains(t, string(out), "package main", "should get file content via on-demand fetch")

	// git show for assets/ too
	cmd = exec.Command("git", "-C", cloneDir, "show", "HEAD:assets/large-file.bin")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "git show on assets should work: %s", string(out))
	assert.Len(t, strings.TrimSpace(string(out)), 1024, "should get full content of large file")
}

// --- B. Git log works without fetching blobs ---

// TestPartialClone_LogWithoutBlobFetch verifies that git log and
// diff --stat work in a partial clone without triggering blob downloads.
// Failure prevented: daemon sync checks or status commands triggering
// expensive blob downloads on every invocation.
func TestPartialClone_LogWithoutBlobFetch(t *testing.T) {
	g := getSharedGitea(t)
	cloneURL := g.createRepo(t, "twin-partial-log")

	pushMultipleFiles(t, cloneURL, map[string]string{
		"docs/readme.md": "# Docs\n",
		"src/main.go":    "package main\n",
	})

	// add another commit
	g.pushFromTempClone(t, cloneURL, "docs/changelog.md", "# Changelog\n\n- v1.0\n")

	// partial clone, sparse to docs/ only
	cloneDir := filepath.Join(t.TempDir(), "partial-log")
	cmd := exec.Command("git", "clone",
		"--filter=blob:none",
		"--sparse",
		cloneURL, cloneDir)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "clone: %s", string(out))

	cmd = exec.Command("git", "-C", cloneDir, "sparse-checkout", "set", "docs")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "sparse set: %s", string(out))

	// git log should work without fetching blobs
	cmd = exec.Command("git", "-C", cloneDir, "log", "--oneline")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "git log: %s", string(out))
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	assert.GreaterOrEqual(t, len(lines), 2, "should see at least 2 commits")

	// git diff --stat between commits should work
	cmd = exec.Command("git", "-C", cloneDir, "diff", "--stat", "HEAD~1", "HEAD")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "git diff --stat: %s", string(out))
	assert.NotEmpty(t, string(out), "diff --stat should show changes")
}

// --- C. Fetch after partial clone brings new commits ---

// TestPartialClone_FetchBringsNewCommits verifies that git fetch in
// a partial clone correctly brings new commits and tree objects
// without downloading all blobs.
// Failure prevented: fetch in partial clone failing with "missing
// object" errors, breaking daemon sync loop.
func TestPartialClone_FetchBringsNewCommits(t *testing.T) {
	g := getSharedGitea(t)
	cloneURL := g.createRepo(t, "twin-partial-fetch")

	g.pushFromTempClone(t, cloneURL, "docs/v1.md", "version 1\n")

	// partial clone
	cloneDir := filepath.Join(t.TempDir(), "partial-fetch")
	cmd := exec.Command("git", "clone",
		"--filter=blob:none",
		"--sparse",
		cloneURL, cloneDir)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "clone: %s", string(out))

	gitConfig(t, cloneDir)

	cmd = exec.Command("git", "-C", cloneDir, "sparse-checkout", "set", "docs")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "sparse set: %s", string(out))

	sha1 := gitSHA(t, cloneDir)

	// push new commit from elsewhere
	g.pushFromTempClone(t, cloneURL, "docs/v2.md", "version 2\n")

	// fetch should succeed
	cmd = exec.Command("git", "-C", cloneDir, "fetch", "origin")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "fetch: %s", string(out))

	// local HEAD unchanged (haven't merged)
	sha2 := gitSHA(t, cloneDir)
	assert.Equal(t, sha1, sha2, "local HEAD should not change on fetch alone")

	// pull --rebase to integrate
	cmd = exec.Command("git", "-C", cloneDir, "pull", "--rebase", "--autostash")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "pull --rebase: %s", string(out))

	sha3 := gitSHA(t, cloneDir)
	assert.NotEqual(t, sha1, sha3, "HEAD should advance after pull")

	// new file should be present (in sparse set)
	require.FileExists(t, filepath.Join(cloneDir, "docs/v2.md"))
}

// --- D. Sparse redefine adds new directories ---

// TestPartialClone_SparseRedefine_AddsDirectory verifies that expanding
// the sparse set in a partial clone correctly materializes new
// directories from existing commits without re-cloning.
// Failure prevented: ledger ConfigureSparseCheckout expanding the cone
// to include data/linear/ but files not appearing because blobs weren't
// fetched.
func TestPartialClone_SparseRedefine_AddsDirectory(t *testing.T) {
	g := getSharedGitea(t)
	cloneURL := g.createRepo(t, "twin-partial-redefine")

	pushMultipleFiles(t, cloneURL, map[string]string{
		"docs/readme.md":       "# Docs\n",
		"data/github/repo.json": `{"stars":42}`,
		"data/linear/tasks.json": `{"count":5}`,
	})

	// partial clone, sparse to docs/ only
	cloneDir := filepath.Join(t.TempDir(), "partial-redefine")
	cmd := exec.Command("git", "clone",
		"--filter=blob:none",
		"--sparse",
		cloneURL, cloneDir)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "clone: %s", string(out))

	cmd = exec.Command("git", "-C", cloneDir, "sparse-checkout", "set", "docs")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "sparse set: %s", string(out))

	// data/ should not be on disk
	_, err = os.Stat(filepath.Join(cloneDir, "data"))
	require.True(t, os.IsNotExist(err), "data/ should not exist initially")

	// expand sparse set to include data/
	cmd = exec.Command("git", "-C", cloneDir, "sparse-checkout", "set", "docs", "data")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "expand sparse: %s", string(out))

	// data/ files should now be materialized (blobs fetched on demand)
	require.FileExists(t, filepath.Join(cloneDir, "data/github/repo.json"))
	require.FileExists(t, filepath.Join(cloneDir, "data/linear/tasks.json"))

	// verify content is correct
	content, err := os.ReadFile(filepath.Join(cloneDir, "data/github/repo.json"))
	require.NoError(t, err)
	assert.Contains(t, string(content), "42", "should have correct content")
}
