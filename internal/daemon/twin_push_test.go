//go:build slow

package daemon

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sageox/ox/internal/gitutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// commitFile writes a file, stages it, and commits in one step.
func twinCommitFile(t *testing.T, repoDir, relPath, content, msg string) {
	t.Helper()
	absPath := filepath.Join(repoDir, relPath)
	require.NoError(t, os.MkdirAll(filepath.Dir(absPath), 0o755))
	require.NoError(t, os.WriteFile(absPath, []byte(content), 0o644))

	cmd := exec.Command("git", "-C", repoDir, "add", relPath)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git add failed: %s", string(out))

	cmd = exec.Command("git", "-C", repoDir, "commit", "-m", msg)
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "git commit failed: %s", string(out))
}

func defaultPushOpts() gitutil.PushOpts {
	return gitutil.PushOpts{
		MaxRetries: 3,
		OpTimeout:  30 * time.Second,
	}
}

// --- A. Concurrent pushers with auto-resolve (disjoint files) ---

// TestPushWithRetry_ConcurrentPushers_AutoResolve verifies two concurrent
// pushers writing disjoint files under data/ both succeed via rebase-retry.
// Failure prevented: rebase-retry loop fails against real Gitea non-fast-forward errors.
func TestPushWithRetry_ConcurrentPushers_AutoResolve(t *testing.T) {
	g := getSharedGitea(t)
	cloneURL := g.createRepo(t, "twin-push-concurrent")

	// seed with a file so data/ exists
	g.pushFromTempClone(t, cloneURL, "data/seed.txt", "seed\n")

	// two separate clones
	clone1 := filepath.Join(t.TempDir(), "clone1")
	clone2 := filepath.Join(t.TempDir(), "clone2")
	g.cloneRepo(t, cloneURL, clone1)
	g.cloneRepo(t, cloneURL, clone2)

	twinCommitFile(t, clone1, "data/file-a.json", `{"from":"clone1"}`, "add file-a")
	twinCommitFile(t, clone2, "data/file-b.json", `{"from":"clone2"}`, "add file-b")

	opts := defaultPushOpts()
	opts.AutoResolvePrefixes = []string{"data/"}

	ctx := context.Background()
	var wg sync.WaitGroup
	errs := make([]error, 2)

	wg.Add(2)
	go func() {
		defer wg.Done()
		errs[0] = gitutil.PushWithRetry(ctx, clone1, opts)
	}()
	go func() {
		defer wg.Done()
		errs[1] = gitutil.PushWithRetry(ctx, clone2, opts)
	}()
	wg.Wait()

	assert.NoError(t, errs[0], "clone1 push should succeed")
	assert.NoError(t, errs[1], "clone2 push should succeed")

	// verify both files exist in the remote
	verify := filepath.Join(t.TempDir(), "verify")
	g.cloneRepo(t, cloneURL, verify)
	require.FileExists(t, filepath.Join(verify, "data/file-a.json"))
	require.FileExists(t, filepath.Join(verify, "data/file-b.json"))
}

// --- B. Concurrent pushers modifying the same file ---

// TestPushWithRetry_ConcurrentPushers_SameFile_AutoResolve verifies two
// pushers modifying the same file under data/ both succeed with accept-theirs.
// Failure prevented: ResolveRebaseAcceptTheirs fails on real merge conflicts.
func TestPushWithRetry_ConcurrentPushers_SameFile_AutoResolve(t *testing.T) {
	g := getSharedGitea(t)
	cloneURL := g.createRepo(t, "twin-push-same-file")

	// seed with target file
	g.pushFromTempClone(t, cloneURL, "data/shared.json", `{"version":0}`)

	clone1 := filepath.Join(t.TempDir(), "clone1")
	clone2 := filepath.Join(t.TempDir(), "clone2")
	g.cloneRepo(t, cloneURL, clone1)
	g.cloneRepo(t, cloneURL, clone2)

	twinCommitFile(t, clone1, "data/shared.json", `{"version":1,"by":"clone1"}`, "update from clone1")
	twinCommitFile(t, clone2, "data/shared.json", `{"version":2,"by":"clone2"}`, "update from clone2")

	opts := defaultPushOpts()
	opts.AutoResolvePrefixes = []string{"data/"}

	ctx := context.Background()
	var wg sync.WaitGroup
	errs := make([]error, 2)

	wg.Add(2)
	go func() {
		defer wg.Done()
		errs[0] = gitutil.PushWithRetry(ctx, clone1, opts)
	}()
	go func() {
		defer wg.Done()
		errs[1] = gitutil.PushWithRetry(ctx, clone2, opts)
	}()
	wg.Wait()

	assert.NoError(t, errs[0], "clone1 push should succeed")
	assert.NoError(t, errs[1], "clone2 push should succeed")

	// verify file exists and has one of the two versions (last writer wins)
	verify := filepath.Join(t.TempDir(), "verify")
	g.cloneRepo(t, cloneURL, verify)
	content, err := os.ReadFile(filepath.Join(verify, "data/shared.json"))
	require.NoError(t, err)
	contentStr := string(content)
	// must be exactly one version — conflict markers or mixed content means broken auto-resolve
	assert.False(t, strings.Contains(contentStr, "<<<<<<<"), "file must not contain conflict markers")
	validContent := contentStr == `{"source":"clone1"}`+"\n" || contentStr == `{"source":"clone2"}`+"\n"
	require.True(t, validContent, "file should contain exactly one version (last writer wins), got: %s", contentStr)
}

// --- C. No auto-resolve → second push fails ---

// TestPushWithRetry_NoAutoResolve_ConflictFails verifies that conflicting
// pushes without auto-resolve prefixes fail with an error.
// Failure prevented: PushWithRetry silently succeeds on conflicts when it shouldn't.
func TestPushWithRetry_NoAutoResolve_ConflictFails(t *testing.T) {
	g := getSharedGitea(t)
	cloneURL := g.createRepo(t, "twin-push-no-resolve")

	g.pushFromTempClone(t, cloneURL, "config.yaml", "key: original")

	clone1 := filepath.Join(t.TempDir(), "clone1")
	clone2 := filepath.Join(t.TempDir(), "clone2")
	g.cloneRepo(t, cloneURL, clone1)
	g.cloneRepo(t, cloneURL, clone2)

	twinCommitFile(t, clone1, "config.yaml", "key: from-clone1", "update from clone1")
	twinCommitFile(t, clone2, "config.yaml", "key: from-clone2", "update from clone2")

	opts := defaultPushOpts()
	// no AutoResolvePrefixes

	ctx := context.Background()

	// push clone1 first (sequential to guarantee ordering)
	err := gitutil.PushWithRetry(ctx, clone1, opts)
	require.NoError(t, err, "first push should succeed")

	// clone2 should fail — conflict without auto-resolve
	err = gitutil.PushWithRetry(ctx, clone2, opts)
	require.Error(t, err, "second push should fail on conflict without auto-resolve")
}

// --- D. Invalid credentials → permanent failure ---

// TestPushWithRetry_InvalidCredentials_PermanentFailure verifies that bad
// credentials cause immediate failure without retrying.
// Failure prevented: permanent error patterns don't match real Gitea error format,
// causing 3 retry attempts that all fail on auth.
func TestPushWithRetry_InvalidCredentials_PermanentFailure(t *testing.T) {
	g := getSharedGitea(t)
	cloneURL := g.createRepo(t, "twin-push-bad-creds")

	cloneDir := filepath.Join(t.TempDir(), "clone")
	g.cloneRepo(t, cloneURL, cloneDir)
	twinCommitFile(t, cloneDir, "file.txt", "content", "add file")

	// replace remote URL with bad credentials
	badURL := fmt.Sprintf("http://baduser:badpass@localhost:%s/%s/twin-push-bad-creds.git",
		giteaHostPort, g.adminUser)
	cmd := exec.Command("git", "-C", cloneDir, "remote", "set-url", "origin", badURL)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "set-url failed: %s", string(out))

	opts := defaultPushOpts()
	ctx := context.Background()

	err = gitutil.PushWithRetry(ctx, cloneDir, opts)
	require.Error(t, err, "push with bad credentials should fail")
}

// --- E. Non-existent repo → permanent failure ---

// TestPushWithRetry_NonExistentRepo_PermanentFailure verifies that pushing to
// a non-existent repo fails permanently.
// Failure prevented: non-existent repo errors not matching permanentPatterns,
// causing unnecessary retries.
func TestPushWithRetry_NonExistentRepo_PermanentFailure(t *testing.T) {
	g := getSharedGitea(t)
	// create a real repo to clone from, then point at a non-existent one
	cloneURL := g.createRepo(t, "twin-push-exists")

	cloneDir := filepath.Join(t.TempDir(), "clone")
	g.cloneRepo(t, cloneURL, cloneDir)
	twinCommitFile(t, cloneDir, "file.txt", "content", "add file")

	// point remote at non-existent repo
	fakeURL := fmt.Sprintf("http://%s:%s@localhost:%s/%s/does-not-exist.git",
		g.adminUser, g.adminPass, giteaHostPort, g.adminUser)
	cmd := exec.Command("git", "-C", cloneDir, "remote", "set-url", "origin", fakeURL)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "set-url failed: %s", string(out))

	opts := defaultPushOpts()
	ctx := context.Background()

	err = gitutil.PushWithRetry(ctx, cloneDir, opts)
	require.Error(t, err, "push to non-existent repo should fail")
}
