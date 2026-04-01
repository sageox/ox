//go:build slow

package daemon

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/gitutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- A. Rebase state guards ---

// TestSync_StaleRebase_SkipsPull verifies that a repo stuck in rebase state
// causes pullManagedRepo to skip the pull rather than crashing or hanging.
// Failure prevented: daemon hangs or corrupts a repo that was mid-rebase from
// a previous interrupted pull.
func TestSync_StaleRebase_SkipsPull(t *testing.T) {
	if testing.Short() {
		t.Skip("short: Gitea digital twin")
	}

	g := getSharedGitea(t)
	cloneURL := g.createRepo(t, "adv-stale-rebase")

	cloneDir := filepath.Join(t.TempDir(), "local")
	g.cloneRepo(t, cloneURL, cloneDir)

	// simulate a stuck rebase by creating the rebase-merge directory with a head-name file
	rebaseMergeDir := filepath.Join(cloneDir, ".git", "rebase-merge")
	require.NoError(t, os.MkdirAll(rebaseMergeDir, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(rebaseMergeDir, "head-name"),
		[]byte("refs/heads/main"),
		0o644,
	))

	// push a commit from a separate clone so there IS something to pull
	g.pushFromTempClone(t, cloneURL, "new-file.txt", "should not appear")

	isolateCredentials(t)
	projectDir := setupProjectWithConfig(t, "")
	s := newTestScheduler(projectDir)

	opts := ManagedRepoPullOpts{
		RepoPath:     cloneDir,
		RepoName:     "test",
		ProjectRoot:  projectDir,
		SyncInterval: time.Second,
		MinFetchAge:  0,
		Logger:       slog.Default(),
	}

	result := s.pullManagedRepo(context.Background(), opts)

	// pull should be skipped due to rebase state
	assert.True(t, result.Skipped, "pull should be skipped when rebase is in progress")
	assert.NoError(t, result.Err, "skipping due to rebase should not produce an error")

	// the rebase-merge dir should still exist (not cleaned up by the daemon)
	_, err := os.Stat(rebaseMergeDir)
	assert.NoError(t, err, "rebase-merge directory should still exist after skipped pull")

	// the remote file should NOT have been pulled
	_, err = os.Stat(filepath.Join(cloneDir, "new-file.txt"))
	assert.True(t, os.IsNotExist(err), "remote file should not exist when pull was skipped")
}

// --- B. Force-push divergence ---

// TestSync_ForcePush_DivergenceDetected verifies that when a force-push rewrites
// remote history, pullManagedRepo detects divergence and leaves the repo in a
// usable state (not stuck in rebase).
// Failure prevented: daemon leaves repo permanently bricked after force-push divergence.
func TestSync_ForcePush_DivergenceDetected(t *testing.T) {
	if testing.Short() {
		t.Skip("short: Gitea digital twin")
	}

	g := getSharedGitea(t)
	cloneURL := g.createRepo(t, "adv-forcepush-diverge")

	localDir := filepath.Join(t.TempDir(), "local")
	g.cloneRepo(t, cloneURL, localDir)

	// make a local commit (file A) and push it to establish shared history
	require.NoError(t, os.WriteFile(filepath.Join(localDir, "file-a.txt"), []byte("local-a"), 0o644))
	runGit(t, localDir, "add", "file-a.txt")
	runGit(t, localDir, "commit", "-m", "add file A")
	runGit(t, localDir, "push")

	// clone to a second location, force-push divergent history
	secondDir := filepath.Join(t.TempDir(), "second")
	g.cloneRepo(t, cloneURL, secondDir)
	runGit(t, secondDir, "reset", "--hard", "HEAD~1")
	require.NoError(t, os.WriteFile(filepath.Join(secondDir, "file-b.txt"), []byte("rogue-b"), 0o644))
	runGit(t, secondDir, "add", "file-b.txt")
	runGit(t, secondDir, "commit", "-m", "rogue force push with file B")
	runGit(t, secondDir, "push", "--force")

	// make another local commit (file C) that is NOT pushed — creates true divergence
	require.NoError(t, os.WriteFile(filepath.Join(localDir, "file-c.txt"), []byte("local-c"), 0o644))
	runGit(t, localDir, "add", "file-c.txt")
	runGit(t, localDir, "commit", "-m", "add file C locally")

	isolateCredentials(t)
	projectDir := setupProjectWithConfig(t, "")
	s := newTestScheduler(projectDir)

	opts := ManagedRepoPullOpts{
		RepoPath:         localDir,
		RepoName:         "test",
		ProjectRoot:      projectDir,
		SyncInterval:     time.Second,
		MinFetchAge:      0,
		Logger:           slog.Default(),
		DetectDivergence: true,
	}

	result := s.pullManagedRepo(context.Background(), opts)

	// regardless of whether pull succeeded or failed, the repo must not be stuck in rebase
	assert.False(t, gitutil.IsRebaseInProgress(localDir),
		"repo should not be stuck in rebase state after divergence handling")

	// repo must be structurally valid
	revParseCmd := exec.Command("git", "-C", localDir, "rev-parse", "--git-dir")
	revParseOut, revParseErr := revParseCmd.CombinedOutput()
	assert.NoError(t, revParseErr, "git rev-parse should succeed (repo valid): %s", string(revParseOut))

	// divergence should have been detected
	if result.Err == nil {
		// rebase succeeded — divergence was resolved
		assert.True(t, result.Diverged, "divergence should have been detected")
	}
}

// TestSync_ForcePush_NoConflict_RebasesCleanly verifies that a force-push that
// doesn't conflict with local changes rebases cleanly, preserving both remote
// and local files.
// Failure prevented: non-conflicting force-push causes unnecessary pull failures.
func TestSync_ForcePush_NoConflict_RebasesCleanly(t *testing.T) {
	if testing.Short() {
		t.Skip("short: Gitea digital twin")
	}

	g := getSharedGitea(t)
	cloneURL := g.createRepo(t, "adv-forcepush-clean")

	localDir := filepath.Join(t.TempDir(), "local")
	g.cloneRepo(t, cloneURL, localDir)

	// make a local commit (file A) and push
	require.NoError(t, os.WriteFile(filepath.Join(localDir, "file-a.txt"), []byte("original-a"), 0o644))
	runGit(t, localDir, "add", "file-a.txt")
	runGit(t, localDir, "commit", "-m", "add file A")
	runGit(t, localDir, "push")

	// second clone: force-push new history that adds file B (no overlap with file C)
	secondDir := filepath.Join(t.TempDir(), "second")
	g.cloneRepo(t, cloneURL, secondDir)
	runGit(t, secondDir, "reset", "--hard", "HEAD~1")
	require.NoError(t, os.WriteFile(filepath.Join(secondDir, "file-b.txt"), []byte("force-pushed-b"), 0o644))
	runGit(t, secondDir, "add", "file-b.txt")
	runGit(t, secondDir, "commit", "-m", "force push with file B")
	runGit(t, secondDir, "push", "--force")

	// local: make an unpushed commit adding file C (no conflict with file B)
	require.NoError(t, os.WriteFile(filepath.Join(localDir, "file-c.txt"), []byte("local-c"), 0o644))
	runGit(t, localDir, "add", "file-c.txt")
	runGit(t, localDir, "commit", "-m", "add file C locally")

	isolateCredentials(t)
	projectDir := setupProjectWithConfig(t, "")
	s := newTestScheduler(projectDir)

	opts := ManagedRepoPullOpts{
		RepoPath:         localDir,
		RepoName:         "test",
		ProjectRoot:      projectDir,
		SyncInterval:     time.Second,
		MinFetchAge:      0,
		Logger:           slog.Default(),
		DetectDivergence: true,
	}

	result := s.pullManagedRepo(context.Background(), opts)

	// non-conflicting force-push should rebase cleanly
	assert.NoError(t, result.Err, "non-conflicting force-push should rebase cleanly")

	// file B from force-pushed remote should exist
	contentB, err := os.ReadFile(filepath.Join(localDir, "file-b.txt"))
	require.NoError(t, err, "file-b.txt should exist after rebase")
	assert.Equal(t, "force-pushed-b", string(contentB))

	// file C from local unpushed commit should survive rebase
	contentC, err := os.ReadFile(filepath.Join(localDir, "file-c.txt"))
	require.NoError(t, err, "file-c.txt should exist after rebase")
	assert.Equal(t, "local-c", string(contentC))

	// repo should not be in rebase state
	assert.False(t, gitutil.IsRebaseInProgress(localDir),
		"repo should not be stuck in rebase after clean force-push rebase")

	// repo should be structurally valid
	revParseCmd := exec.Command("git", "-C", localDir, "rev-parse", "--git-dir")
	_, err = revParseCmd.CombinedOutput()
	assert.NoError(t, err, "git rev-parse should succeed (repo valid)")
}

// --- C. Corruption detection ---

// TestSync_CorruptedGitHead_DetectedAsCorrupt verifies that a repo with a
// corrupted .git/HEAD is detected as corrupt when ValidateIntegrity is enabled,
// preventing the daemon from operating on a broken repo.
// Failure prevented: daemon attempts fetch/pull on a structurally invalid repo,
// producing confusing errors instead of a clear corruption signal.
func TestSync_CorruptedGitHead_DetectedAsCorrupt(t *testing.T) {
	if testing.Short() {
		t.Skip("short: Gitea digital twin")
	}

	g := getSharedGitea(t)
	cloneURL := g.createRepo(t, "adv-corrupt-head")

	cloneDir := filepath.Join(t.TempDir(), "local")
	g.cloneRepo(t, cloneURL, cloneDir)

	// corrupt .git/HEAD with garbage content
	headPath := filepath.Join(cloneDir, ".git", "HEAD")
	require.NoError(t, os.WriteFile(headPath, []byte("garbage-not-a-ref"), 0o644))

	isolateCredentials(t)
	projectDir := setupProjectWithConfig(t, "")
	s := newTestScheduler(projectDir)

	opts := ManagedRepoPullOpts{
		RepoPath:          cloneDir,
		RepoName:          "test",
		ProjectRoot:       projectDir,
		SyncInterval:      time.Second,
		MinFetchAge:       0,
		Logger:            slog.Default(),
		ValidateIntegrity: true,
	}

	result := s.pullManagedRepo(context.Background(), opts)

	// corruption should be detected
	assert.True(t, result.CorruptRepo, "corrupted HEAD should be detected as corrupt repo")

	// pull should not have been attempted (no fetch/pull error)
	assert.NoError(t, result.Err, "no pull should be attempted on a corrupt repo")
	assert.False(t, result.Skipped, "corruption is not a skip — it's a distinct state")
}

// --- D. Lock file guards ---

// TestSync_IndexLockFile_SkipsWithIssue verifies that a fresh index.lock file
// causes the pull to be skipped with an issue report rather than deleting the
// lock or crashing.
// Failure prevented: daemon deletes a lock held by an active git process, or
// hangs waiting for a lock to be released.
func TestSync_IndexLockFile_SkipsWithIssue(t *testing.T) {
	if testing.Short() {
		t.Skip("short: Gitea digital twin")
	}

	g := getSharedGitea(t)
	cloneURL := g.createRepo(t, "adv-index-lock")

	cloneDir := filepath.Join(t.TempDir(), "local")
	g.cloneRepo(t, cloneURL, cloneDir)

	// create a fresh index.lock (simulates an active git process)
	lockPath := filepath.Join(cloneDir, ".git", "index.lock")
	require.NoError(t, os.WriteFile(lockPath, []byte(""), 0o644))

	// push a commit from separate clone so there's something to pull
	g.pushFromTempClone(t, cloneURL, "remote-file.txt", "should not appear")

	isolateCredentials(t)
	projectDir := setupProjectWithConfig(t, "")
	s := newTestScheduler(projectDir)

	opts := ManagedRepoPullOpts{
		RepoPath:     cloneDir,
		RepoName:     "test",
		ProjectRoot:  projectDir,
		SyncInterval: time.Second,
		MinFetchAge:  0,
		Logger:       slog.Default(),
	}

	result := s.pullManagedRepo(context.Background(), opts)

	// pull should be skipped due to fresh lock file
	assert.True(t, result.Skipped, "pull should be skipped when fresh lock file exists")
	assert.NoError(t, result.Err, "skipping due to lock should not produce an error")

	// an issue should be reported
	assert.NotNil(t, result.Issue, "an issue should be reported for lock files")

	// the lock file should still exist (not deleted — it's fresh)
	_, err := os.Stat(lockPath)
	assert.NoError(t, err, "fresh index.lock should not be deleted")

	// the remote file should NOT appear locally (pull was skipped)
	_, err = os.Stat(filepath.Join(cloneDir, "remote-file.txt"))
	assert.True(t, os.IsNotExist(err), "remote file should not exist when pull was skipped")
}

// --- E. Rebase conflict recovery ---

// TestSync_RebaseConflict_RepoRecoverable verifies that a rebase conflict from
// a pull does not permanently brick the repo — it remains structurally valid
// and can be recovered via rebase --abort.
// Failure prevented: conflicting pull leaves repo in a state where no future
// operations (including daemon recovery) can succeed.
func TestSync_RebaseConflict_RepoRecoverable(t *testing.T) {
	if testing.Short() {
		t.Skip("short: Gitea digital twin")
	}

	g := getSharedGitea(t)
	cloneURL := g.createRepo(t, "adv-rebase-conflict")

	localDir := filepath.Join(t.TempDir(), "local")
	g.cloneRepo(t, cloneURL, localDir)

	// make a local commit modifying README.md (don't push)
	require.NoError(t, os.WriteFile(filepath.Join(localDir, "README.md"), []byte("local-version"), 0o644))
	runGit(t, localDir, "add", "README.md")
	runGit(t, localDir, "commit", "-m", "local README change")

	// push a conflicting change from a separate clone
	g.pushFromTempClone(t, cloneURL, "README.md", "remote-version")

	isolateCredentials(t)
	projectDir := setupProjectWithConfig(t, "")
	s := newTestScheduler(projectDir)

	opts := ManagedRepoPullOpts{
		RepoPath:     localDir,
		RepoName:     "test",
		ProjectRoot:  projectDir,
		SyncInterval: time.Second,
		MinFetchAge:  0,
		Logger:       slog.Default(),
	}

	// first pull — should encounter a rebase conflict
	result1 := s.pullManagedRepo(context.Background(), opts)
	assert.Error(t, result1.Err, "first pull should fail due to rebase conflict")

	// after the failed pull, check what state the repo is in
	rebaseInProgress := gitutil.IsRebaseInProgress(localDir)

	if rebaseInProgress {
		// repo is stuck in rebase — verify second pull skips cleanly
		result2 := s.pullManagedRepo(context.Background(), opts)
		assert.True(t, result2.Skipped, "second pull should be skipped when rebase is in progress")
		assert.NoError(t, result2.Err, "second pull skip should not produce an error")

		// verify manual recovery is possible: rebase --abort should succeed
		abortCmd := exec.Command("git", "-C", localDir, "rebase", "--abort")
		abortOut, abortErr := abortCmd.CombinedOutput()
		assert.NoError(t, abortErr, "rebase --abort should succeed for recovery: %s", string(abortOut))
	}
	// if rebase was already aborted by pullManagedRepo, that's also acceptable

	// in either case, the repo must be structurally valid
	revParseCmd := exec.Command("git", "-C", localDir, "rev-parse", "--git-dir")
	revParseOut, revParseErr := revParseCmd.CombinedOutput()
	assert.NoError(t, revParseErr, "git rev-parse should succeed (repo valid): %s", string(revParseOut))

	// after recovery, the repo should not be in rebase state
	assert.False(t, gitutil.IsRebaseInProgress(localDir),
		"repo should not be in rebase state after recovery")
}

// --- Helpers ---

// runGit executes a git command in the given directory and fails the test on error.
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	fullArgs := append([]string{"-C", dir}, args...)
	cmd := exec.Command("git", fullArgs...)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v failed: %s", args, string(out))
}
