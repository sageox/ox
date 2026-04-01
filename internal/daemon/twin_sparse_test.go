//go:build slow

package daemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sageox/ox/internal/gitutil"
	"github.com/sageox/ox/internal/ledger"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- A. Sparse reconfig during active push (concurrent safety) ---

// TestSparseReconfig_DuringPush_NoCorruption verifies that running
// ConfigureSparseCheckout concurrently with a push does not corrupt
// the git index or cause either operation to fail.
// Failure prevented: daemon's ~60s sparse reconfig timer fires while
// session finalize is pushing, causing index.lock contention or
// checkout of files mid-push that breaks the commit.
func TestSparseReconfig_DuringPush_NoCorruption(t *testing.T) {
	g := getSharedGitea(t)
	cloneURL := g.createRepo(t, "twin-sparse-concurrent")

	// seed with ledger-like structure
	pushMultipleFiles(t, cloneURL, map[string]string{
		".sageox/config.json":      `{"version":1}`,
		".sync/status.json":        `{"ok":true}`,
		"sessions/old/summary.md":  "old session\n",
		"data/github/issues.json":  `{"count":1}`,
	})

	cloneDir := filepath.Join(t.TempDir(), "sparse-push")
	g.cloneRepo(t, cloneURL, cloneDir)

	// init sparse checkout (cone mode, like ledger)
	cmd := exec.Command("git", "-C", cloneDir, "sparse-checkout", "init", "--cone")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "sparse-checkout init: %s", string(out))

	cmd = exec.Command("git", "-C", cloneDir, "sparse-checkout", "set",
		".sageox", ".sync", "sessions", "data")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "sparse-checkout set: %s", string(out))

	// make a commit to push
	twinCommitFile(t, cloneDir, "sessions/new/summary.md", "new session\n", "add new session")

	opts := gitutil.PushOpts{
		MaxRetries: 3,
		OpTimeout:  30 * time.Second,
	}

	ctx := context.Background()
	var wg sync.WaitGroup
	var pushErr, sparseErr error

	// run push and sparse reconfig concurrently
	wg.Add(2)
	go func() {
		defer wg.Done()
		pushErr = gitutil.PushWithRetry(ctx, cloneDir, opts)
	}()
	go func() {
		defer wg.Done()
		sparseErr = ledger.ConfigureSparseCheckout(cloneDir)
	}()
	wg.Wait()

	// at least one should succeed; neither should corrupt the repo
	if pushErr != nil && sparseErr != nil {
		t.Fatalf("both push and sparse reconfig failed: push=%v sparse=%v", pushErr, sparseErr)
	}

	// verify the repo is still valid
	cmd = exec.Command("git", "-C", cloneDir, "status")
	out, err = cmd.CombinedOutput()
	assert.NoError(t, err, "git status should work after concurrent ops: %s", string(out))

	// no index.lock left behind
	_, err = os.Stat(filepath.Join(cloneDir, ".git", "index.lock"))
	assert.True(t, os.IsNotExist(err), "index.lock should not exist after operations complete")
}

// --- B. Sparse reconfig preserves dirty files ---

// TestSparseReconfig_PreservesDirtyFiles verifies that
// ConfigureSparseCheckout does not delete uncommitted files in
// directories that are outside the new sparse cone.
// Failure prevented: daemon sparse reconfig deletes staged files
// from data/linear/ or data/custom/ that the CLI is about to commit.
func TestSparseReconfig_PreservesDirtyFiles(t *testing.T) {
	g := getSharedGitea(t)
	cloneURL := g.createRepo(t, "twin-sparse-dirty")

	pushMultipleFiles(t, cloneURL, map[string]string{
		".sageox/config.json":     `{"version":1}`,
		".sync/status.json":       `{"ok":true}`,
		"sessions/s1/summary.md":  "session 1\n",
		"data/github/issues.json": `{"count":0}`,
		"data/linear/tasks.json":  `{"count":0}`,
	})

	cloneDir := filepath.Join(t.TempDir(), "sparse-dirty")
	g.cloneRepo(t, cloneURL, cloneDir)

	// init cone-mode sparse checkout
	cmd := exec.Command("git", "-C", cloneDir, "sparse-checkout", "init", "--cone")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "sparse-checkout init: %s", string(out))

	cmd = exec.Command("git", "-C", cloneDir, "sparse-checkout", "set",
		".sageox", ".sync", "sessions", "data")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "sparse-checkout set: %s", string(out))

	// stage a modification to data/linear/ (simulates CLI import)
	require.NoError(t, os.WriteFile(
		filepath.Join(cloneDir, "data/linear/tasks.json"),
		[]byte(`{"count":5,"updated":true}`), 0o644))

	cmd = exec.Command("git", "-C", cloneDir, "add", "--sparse", "data/linear/tasks.json")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "git add: %s", string(out))

	// run sparse reconfig (this is what the daemon does every ~60s)
	err = ledger.ConfigureSparseCheckout(cloneDir)
	require.NoError(t, err, "ConfigureSparseCheckout should succeed")

	// the staged file must still exist on disk
	content, err := os.ReadFile(filepath.Join(cloneDir, "data/linear/tasks.json"))
	require.NoError(t, err, "staged file should survive sparse reconfig")
	assert.Contains(t, string(content), "updated", "staged file content should be preserved")

	// and it should still be staged
	cmd = exec.Command("git", "-C", cloneDir, "diff", "--cached", "--name-only")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err)
	assert.Contains(t, string(out), "data/linear/tasks.json", "file should still be staged")
}

// --- C. Sparse checkout after clone includes .sageox ---

// TestSparseClone_AlwaysIncludesSageox verifies that
// CloneWithSparseCheckout always includes .sageox/ in the sparse set.
// Failure prevented: .sageox/cache/ (codedb, config) deleted on
// first sparse reconfig after clone.
func TestSparseClone_AlwaysIncludesSageox(t *testing.T) {
	g := getSharedGitea(t)
	cloneURL := g.createRepo(t, "twin-sparse-sageox")

	pushMultipleFiles(t, cloneURL, map[string]string{
		".sageox/config.json": `{"version":1}`,
		".sync/status.json":   `{"ok":true}`,
		"sessions/s1/raw.md":  "content\n",
	})

	cloneDir := filepath.Join(t.TempDir(), "sparse-sageox")
	err := ledger.CloneWithSparseCheckout(cloneDir, cloneURL)
	require.NoError(t, err)

	// .sageox must be materialized
	require.FileExists(t, filepath.Join(cloneDir, ".sageox/config.json"))

	// verify .sageox is in sparse-checkout list
	cmd := exec.Command("git", "-C", cloneDir, "sparse-checkout", "list")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err)
	assert.Contains(t, string(out), ".sageox", "sparse-checkout list should include .sageox")

	// create a cache dir (simulates codedb)
	cacheDir := filepath.Join(cloneDir, ".sageox", "cache", "codedb")
	require.NoError(t, os.MkdirAll(cacheDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cacheDir, "index.db"), []byte("fake db"), 0o644))

	// rerun sparse reconfig — cache must survive
	err = ledger.ConfigureSparseCheckout(cloneDir)
	require.NoError(t, err)

	require.FileExists(t, filepath.Join(cacheDir, "index.db"),
		".sageox/cache/ should survive sparse reconfig")
}

// --- D. Repeated sparse reconfig is idempotent ---

// TestSparseReconfig_Idempotent verifies that calling
// ConfigureSparseCheckout multiple times produces the same result
// and doesn't accumulate duplicate entries or corrupt state.
// Failure prevented: repeated sparse-checkout init --cone deleting
// untracked files in .sageox/cache/ on every invocation.
func TestSparseReconfig_Idempotent(t *testing.T) {
	g := getSharedGitea(t)
	cloneURL := g.createRepo(t, "twin-sparse-idempotent")

	pushMultipleFiles(t, cloneURL, map[string]string{
		".sageox/config.json": `{"version":1}`,
		".sync/status.json":   `{"ok":true}`,
		"sessions/s1/raw.md":  "content\n",
	})

	cloneDir := filepath.Join(t.TempDir(), "sparse-idempotent")
	err := ledger.CloneWithSparseCheckout(cloneDir, cloneURL)
	require.NoError(t, err)

	// place an untracked file in .sageox/cache
	cacheFile := filepath.Join(cloneDir, ".sageox", "cache", "test.db")
	require.NoError(t, os.MkdirAll(filepath.Dir(cacheFile), 0o755))
	require.NoError(t, os.WriteFile(cacheFile, []byte("data"), 0o644))

	// get sparse list after first config
	cmd := exec.Command("git", "-C", cloneDir, "sparse-checkout", "list")
	out1, err := cmd.CombinedOutput()
	require.NoError(t, err)

	// run ConfigureSparseCheckout 3 more times
	for i := 0; i < 3; i++ {
		require.NoError(t, ledger.ConfigureSparseCheckout(cloneDir))
	}

	// sparse list should be the same
	cmd = exec.Command("git", "-C", cloneDir, "sparse-checkout", "list")
	out2, err := cmd.CombinedOutput()
	require.NoError(t, err)

	// normalize for comparison (entries may be ordered differently)
	lines1 := strings.Split(strings.TrimSpace(string(out1)), "\n")
	lines2 := strings.Split(strings.TrimSpace(string(out2)), "\n")
	assert.ElementsMatch(t, lines1, lines2, "sparse list should be stable across reconfigs")

	// cache file must survive all reconfigs
	require.FileExists(t, cacheFile, "cache file should survive repeated sparse reconfigs")
}
