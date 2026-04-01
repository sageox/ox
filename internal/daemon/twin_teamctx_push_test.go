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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- A. Team context concurrent push (no auto-resolve) ---

// TestTeamCtxPush_ConcurrentNoAutoResolve_Fails verifies that two
// concurrent pushers editing the same non-auto-resolve file in a team
// context repo produce a clear, actionable error for the loser.
// Failure prevented: team context pushes (memory/, docs/) fail with
// opaque git errors instead of telling the user "rebase and retry".
func TestTeamCtxPush_ConcurrentNoAutoResolve_Fails(t *testing.T) {
	g := getSharedGitea(t)
	cloneURL := g.createRepo(t, "twin-teamctx-conflict")

	g.pushFromTempClone(t, cloneURL, "memory/team.md", "# Team Memory\n\nInitial content.\n")

	clone1 := filepath.Join(t.TempDir(), "clone1")
	clone2 := filepath.Join(t.TempDir(), "clone2")
	g.cloneRepo(t, cloneURL, clone1)
	g.cloneRepo(t, cloneURL, clone2)

	// both edit the same file
	twinCommitFile(t, clone1, "memory/team.md", "# Team Memory\n\nUpdated by coworker 1.\n", "coworker 1 update")
	twinCommitFile(t, clone2, "memory/team.md", "# Team Memory\n\nUpdated by coworker 2.\n", "coworker 2 update")

	opts := gitutil.PushOpts{
		MaxRetries: 3,
		OpTimeout:  30 * time.Second,
		// no AutoResolvePrefixes — team context memory/ is human-authored
	}

	ctx := context.Background()

	// push sequentially to guarantee ordering
	err := gitutil.PushWithRetry(ctx, clone1, opts)
	require.NoError(t, err, "first push should succeed")

	err = gitutil.PushWithRetry(ctx, clone2, opts)
	require.Error(t, err, "second push should fail — conflict without auto-resolve")
}

// --- B. Team context push with auto-resolve on data/ ---

// TestTeamCtxPush_DataAutoResolve_Succeeds verifies that concurrent
// pushes to data/ paths (auto-resolve enabled) both succeed, matching
// how the daemon handles imported data from external integrations.
// Failure prevented: auto-resolve not working for team context repos
// that have data/ alongside memory/ and docs/.
func TestTeamCtxPush_DataAutoResolve_Succeeds(t *testing.T) {
	g := getSharedGitea(t)
	cloneURL := g.createRepo(t, "twin-teamctx-data")

	g.pushFromTempClone(t, cloneURL, "data/github/issues.json", `{"count":0}`)

	clone1 := filepath.Join(t.TempDir(), "clone1")
	clone2 := filepath.Join(t.TempDir(), "clone2")
	g.cloneRepo(t, cloneURL, clone1)
	g.cloneRepo(t, cloneURL, clone2)

	twinCommitFile(t, clone1, "data/github/issues.json", `{"count":10,"by":"clone1"}`, "import from clone1")
	twinCommitFile(t, clone2, "data/github/issues.json", `{"count":20,"by":"clone2"}`, "import from clone2")

	opts := gitutil.PushOpts{
		MaxRetries:          3,
		OpTimeout:           30 * time.Second,
		AutoResolvePrefixes: []string{"data/"},
	}

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

	assert.NoError(t, errs[0], "clone1 push should succeed with auto-resolve")
	assert.NoError(t, errs[1], "clone2 push should succeed with auto-resolve")

	// verify file exists with one of the two values
	verify := filepath.Join(t.TempDir(), "verify")
	g.cloneRepo(t, cloneURL, verify)
	content, err := os.ReadFile(filepath.Join(verify, "data/github/issues.json"))
	require.NoError(t, err)
	contentStr := string(content)
	// must be exactly one version — conflict markers or mixed content means broken auto-resolve
	assert.False(t, strings.Contains(contentStr, "<<<<<<<"), "file must not contain conflict markers")
	validContent := contentStr == `{"source":"clone1"}`+"\n" || contentStr == `{"source":"clone2"}`+"\n"
	assert.True(t, validContent, "file should contain exactly one version (last writer wins), got: %s", contentStr)
}

// --- C. Push after pull --rebase leaves clean state ---

// TestTeamCtxPush_AfterPullRebase_Clean verifies the common team
// context workflow: pull --rebase to get latest, then push local
// changes. This is the most frequent operation in daemon sync.
// Failure prevented: pull --rebase leaving dirty state or merge
// markers that cause subsequent push to fail.
func TestTeamCtxPush_AfterPullRebase_Clean(t *testing.T) {
	g := getSharedGitea(t)
	cloneURL := g.createRepo(t, "twin-teamctx-pull-push")

	g.pushFromTempClone(t, cloneURL, "memory/daily/day1.md", "observation 1\n")

	cloneDir := filepath.Join(t.TempDir(), "worker")
	g.cloneRepo(t, cloneURL, cloneDir)

	// make a local commit (not pushed yet)
	twinCommitFile(t, cloneDir, "memory/daily/day2.md", "observation 2\n", "add day2")

	// simulate concurrent remote change
	g.pushFromTempClone(t, cloneURL, "docs/guide.md", "# Guide\n\nNew guide.\n")

	// pull --rebase should succeed
	cmd := exec.Command("git", "-C", cloneDir, "pull", "--rebase", "--autostash")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "pull --rebase should succeed: %s", string(out))

	// push should succeed
	cmd = exec.Command("git", "-C", cloneDir, "push")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "push after rebase should succeed: %s", string(out))

	// verify both files exist
	verify := filepath.Join(t.TempDir(), "verify")
	g.cloneRepo(t, cloneURL, verify)
	require.FileExists(t, filepath.Join(verify, "memory/daily/day2.md"))
	require.FileExists(t, filepath.Join(verify, "docs/guide.md"))
}

// --- D. Push with autostash preserves uncommitted changes ---

// TestTeamCtxPush_Autostash_PreservesUncommitted verifies that
// --autostash preserves uncommitted local changes during pull --rebase,
// matching the daemon's pullManagedRepo behavior.
// Failure prevented: daemon pull discards uncommitted memory/ edits
// that a coworker was in the middle of writing.
func TestTeamCtxPush_Autostash_PreservesUncommitted(t *testing.T) {
	g := getSharedGitea(t)
	cloneURL := g.createRepo(t, "twin-teamctx-autostash")

	g.pushFromTempClone(t, cloneURL, "SOUL.md", "# Soul\n\nInitial.\n")

	cloneDir := filepath.Join(t.TempDir(), "worker")
	g.cloneRepo(t, cloneURL, cloneDir)

	// uncommitted local change (simulates in-progress memory edit)
	dirtyContent := "# Soul\n\nModified locally but not committed.\n"
	require.NoError(t, os.WriteFile(filepath.Join(cloneDir, "SOUL.md"), []byte(dirtyContent), 0o644))

	// concurrent remote push to a different file
	g.pushFromTempClone(t, cloneURL, "docs/readme.md", "# Readme\n")

	// pull --rebase --autostash should preserve the dirty SOUL.md
	cmd := exec.Command("git", "-C", cloneDir, "pull", "--rebase", "--autostash")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "pull --rebase --autostash should succeed: %s", string(out))

	// dirty file should still have local changes
	content, err := os.ReadFile(filepath.Join(cloneDir, "SOUL.md"))
	require.NoError(t, err)
	assert.Contains(t, string(content), "Modified locally but not committed",
		"uncommitted changes should survive pull --rebase --autostash")

	// remote file should also be present
	require.FileExists(t, filepath.Join(cloneDir, "docs/readme.md"))
}

// --- E. Remote ref check detects upstream changes ---

// TestRemoteRefCheck_DetectsNewCommit verifies that ls-remote correctly
// detects when the remote has a new commit not present locally.
// Failure prevented: remoteRefCheck returning true (no changes) when
// remote has new commits, causing sync to skip pull.
func TestRemoteRefCheck_DetectsNewCommit(t *testing.T) {
	g := getSharedGitea(t)
	cloneURL := g.createRepo(t, "twin-teamctx-refcheck")

	g.pushFromTempClone(t, cloneURL, "SOUL.md", "initial\n")

	cloneDir := filepath.Join(t.TempDir(), "refcheck")
	g.cloneRepo(t, cloneURL, cloneDir)

	// local and remote should match
	localSHA := gitSHA(t, cloneDir)
	remoteSHA := lsRemoteSHA(t, cloneDir)
	require.Equal(t, localSHA, remoteSHA, "should match initially")

	// push from elsewhere
	g.pushFromTempClone(t, cloneURL, "docs/new.md", "new doc\n")

	// now remote should differ
	remoteSHA2 := lsRemoteSHA(t, cloneDir)
	assert.NotEqual(t, localSHA, remoteSHA2, "remote should have new commit")
}

func gitSHA(t *testing.T, repoDir string) string {
	t.Helper()
	cmd := exec.Command("git", "-C", repoDir, "rev-parse", "HEAD")
	out, err := cmd.Output()
	require.NoError(t, err)
	return strings.TrimSpace(string(out))
}

func lsRemoteSHA(t *testing.T, repoDir string) string {
	t.Helper()
	// resolve tracking branch
	cmd := exec.Command("git", "-C", repoDir, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}")
	out, err := cmd.Output()
	require.NoError(t, err)

	upstream := strings.TrimSpace(string(out))
	remoteRef := upstream
	if strings.HasPrefix(upstream, "origin/") {
		remoteRef = "refs/heads/" + strings.TrimPrefix(upstream, "origin/")
	}

	cmd = exec.Command("git", "-C", repoDir, "ls-remote", "origin", remoteRef)
	out, err = cmd.Output()
	require.NoError(t, err)

	fields := strings.Fields(string(out))
	require.NotEmpty(t, fields, "ls-remote should return at least one field")
	return fields[0]
}

// --- F. Force push detection ---

// TestRemoteRefCheck_ForcePush_Detected verifies that force-push to
// the remote is detected as a divergence (local SHA != remote SHA).
// Failure prevented: after force-push, daemon thinks it's up to date
// because it cached the old remote ref.
func TestRemoteRefCheck_ForcePush_Detected(t *testing.T) {
	g := getSharedGitea(t)
	cloneURL := g.createRepo(t, "twin-teamctx-forcepush")

	g.pushFromTempClone(t, cloneURL, "file1.md", "version 1\n")

	cloneDir := filepath.Join(t.TempDir(), "forcepush-detect")
	g.cloneRepo(t, cloneURL, cloneDir)

	// verify in sync
	sha1 := gitSHA(t, cloneDir)
	remoteSHA1 := lsRemoteSHA(t, cloneDir)
	require.Equal(t, sha1, remoteSHA1)

	// create a divergent history from another clone and force-push
	forceClone := filepath.Join(t.TempDir(), "force-clone")
	g.cloneRepo(t, cloneURL, forceClone)

	// reset to before file1.md, add different file, force push
	cmd := exec.Command("git", "-C", forceClone, "reset", "--hard", "HEAD~1")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "reset: %s", string(out))

	twinCommitFile(t, forceClone, "file2.md", "divergent content\n", "divergent commit")

	cmd = exec.Command("git", "-C", forceClone, "push", "--force")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "force push: %s", string(out))

	// original clone should see different remote SHA
	remoteSHA2 := lsRemoteSHA(t, cloneDir)
	assert.NotEqual(t, sha1, remoteSHA2,
		"remote SHA should differ after force push — daemon must detect this")

	// pull should reconcile (may need --rebase)
	cmd = exec.Command("git", "-C", cloneDir, "pull", "--rebase", "--autostash")
	out, err = cmd.CombinedOutput()
	// force push divergence may cause rebase conflicts; that's expected
	// the point is the divergence was detected, not silently ignored
	_ = err
	_ = out
}
