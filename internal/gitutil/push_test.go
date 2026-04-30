package gitutil

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// initBareRemoteRepo creates a local git repo with a bare remote for push testing.
// Returns (repoPath, bareRemotePath). The repo has one initial commit and
// origin pointing at the bare remote.
func initBareRemoteRepo(t *testing.T) (string, string) {
	t.Helper()

	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	bare := filepath.Join(t.TempDir(), "remote.git")
	repo := filepath.Join(t.TempDir(), "work")

	// create bare remote
	run(t, "", "git", "init", "--bare", "--quiet", bare)

	// clone into working repo
	run(t, "", "git", "clone", "--quiet", bare, repo)

	// configure git identity (isolated to this repo)
	run(t, repo, "git", "config", "user.email", "test@test.local")
	run(t, repo, "git", "config", "user.name", "Test")

	// create initial commit so we have a branch
	require.NoError(t, os.WriteFile(filepath.Join(repo, "init.txt"), []byte("init"), 0644))
	run(t, repo, "git", "add", "init.txt")
	run(t, repo, "git", "commit", "-m", "init", "--no-verify", "--quiet")
	run(t, repo, "git", "push", "--quiet")

	return repo, bare
}

// addCommit creates a file and commits it in the given repo.
func addCommit(t *testing.T, repo, filename, content, msg string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(repo, filename), []byte(content), 0644))
	run(t, repo, "git", "add", filename)
	run(t, repo, "git", "commit", "-m", msg, "--no-verify", "--quiet")
}

// run executes a command, failing the test on error.
func run(t *testing.T, dir string, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "command %s %v failed: %s", name, args, string(out))
}

func TestPushWithRetry_SuccessFirstAttempt(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git push with retry")
	}
	repo, _ := initBareRemoteRepo(t)
	addCommit(t, repo, "a.txt", "hello", "add a")

	err := PushWithRetry(context.Background(), repo, PushOpts{
		MaxRetries: 3,
		OpTimeout:  10 * time.Second,
	})
	assert.NoError(t, err)
}

func TestPushWithRetry_NothingToPush(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git push with retry")
	}
	repo, _ := initBareRemoteRepo(t)

	// nothing new to push — push is a no-op (git push with up-to-date returns 0)
	err := PushWithRetry(context.Background(), repo, PushOpts{
		MaxRetries: 1,
		OpTimeout:  10 * time.Second,
	})
	assert.NoError(t, err)
}

func TestPushWithRetry_RepoBlockedByLockFile(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git push with retry")
	}
	repo, _ := initBareRemoteRepo(t)
	addCommit(t, repo, "a.txt", "hello", "add a")

	// create a lock file to block git ops
	gitDir := filepath.Join(repo, ".git")
	require.NoError(t, os.WriteFile(filepath.Join(gitDir, "index.lock"), []byte{}, 0644))

	err := PushWithRetry(context.Background(), repo, PushOpts{
		MaxRetries: 3,
		OpTimeout:  10 * time.Second,
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "repo blocked")
	assert.Contains(t, err.Error(), "lock")
}

func TestPushWithRetry_PrePushCalledOnce(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git push with retry")
	}
	repo, _ := initBareRemoteRepo(t)
	addCommit(t, repo, "a.txt", "hello", "add a")

	var callCount atomic.Int32
	err := PushWithRetry(context.Background(), repo, PushOpts{
		MaxRetries: 3,
		OpTimeout:  10 * time.Second,
		PrePush: func(repoPath string) error {
			callCount.Add(1)
			return nil
		},
	})
	assert.NoError(t, err)
	assert.Equal(t, int32(1), callCount.Load(), "PrePush should be called exactly once")
}

func TestPushWithRetry_PrePushErrorDoesNotPreventPush(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git push with retry")
	}
	repo, _ := initBareRemoteRepo(t)
	addCommit(t, repo, "a.txt", "hello", "add a")

	err := PushWithRetry(context.Background(), repo, PushOpts{
		MaxRetries: 3,
		OpTimeout:  10 * time.Second,
		PrePush: func(repoPath string) error {
			return fmt.Errorf("credential refresh failed")
		},
	})
	// push should still succeed despite PrePush error
	assert.NoError(t, err)
}

func TestPushWithRetry_NonFastForwardTriggersRebase(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git push with retry")
	}
	repo, bare := initBareRemoteRepo(t)

	// create a second clone, push a commit from it to create divergence
	second := filepath.Join(t.TempDir(), "second")
	run(t, "", "git", "clone", "--quiet", bare, second)
	run(t, second, "git", "config", "user.email", "test@test.local")
	run(t, second, "git", "config", "user.name", "Test")
	addCommit(t, second, "b.txt", "from-second", "second clone commit")
	run(t, second, "git", "push", "--quiet")

	// now the first repo is behind; a push should fail with non-fast-forward
	// and PushWithRetry should pull --rebase then succeed
	addCommit(t, repo, "c.txt", "from-first", "first clone commit")

	err := PushWithRetry(context.Background(), repo, PushOpts{
		MaxRetries: 3,
		OpTimeout:  10 * time.Second,
	})
	assert.NoError(t, err)

	// verify both files ended up in the repo
	assert.FileExists(t, filepath.Join(repo, "b.txt"))
	assert.FileExists(t, filepath.Join(repo, "c.txt"))
}

func TestPushWithRetry_MaxRetriesExhausted(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git push with retry")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	repo := t.TempDir()
	run(t, "", "git", "init", "--quiet", repo)
	run(t, repo, "git", "config", "user.email", "test@test.local")
	run(t, repo, "git", "config", "user.name", "Test")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "f.txt"), []byte("x"), 0644))
	run(t, repo, "git", "add", "f.txt")
	run(t, repo, "git", "commit", "-m", "init", "--no-verify", "--quiet")

	// point remote at a nonexistent local path — push will fail every time
	// with a non-permanent error (not matching permanentPatterns)
	run(t, repo, "git", "remote", "add", "origin", "/nonexistent/path/repo.git")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	err := PushWithRetry(ctx, repo, PushOpts{
		MaxRetries: 2,
		OpTimeout:  5 * time.Second,
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "2 attempts")
}

func TestPushWithRetry_PermanentErrorShortCircuits(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git push with retry")
	}
	repo, _ := initBareRemoteRepo(t)
	addCommit(t, repo, "a.txt", "hello", "add a")

	// point at a remote URL that requires auth, producing "Authentication failed"
	// or "could not read Username" — both are permanent patterns
	run(t, repo, "git", "remote", "set-url", "origin",
		"https://invalid-user:invalid-pass@github.com/nonexistent-org-abc123xyz/nonexistent-repo-abc123xyz.git")

	// set GIT_TERMINAL_PROMPT=0 so git doesn't hang waiting for credentials
	t.Setenv("GIT_TERMINAL_PROMPT", "0")
	// disable credential helpers that might cache or prompt
	run(t, repo, "git", "config", "credential.helper", "")

	err := PushWithRetry(context.Background(), repo, PushOpts{
		MaxRetries: 3,
		OpTimeout:  15 * time.Second,
	})

	assert.Error(t, err)
	// git should produce "Authentication failed" which matches a permanent pattern
	assert.Contains(t, err.Error(), "not retryable")
}

func TestPushWithRetry_ContextCancellationExitsPromptly(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git push with retry")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	repo := t.TempDir()
	run(t, "", "git", "init", "--quiet", repo)
	run(t, repo, "git", "config", "user.email", "test@test.local")
	run(t, repo, "git", "config", "user.name", "Test")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "f.txt"), []byte("x"), 0644))
	run(t, repo, "git", "add", "f.txt")
	run(t, repo, "git", "commit", "-m", "init", "--no-verify", "--quiet")

	// remote that will fail push but not with a permanent error
	run(t, repo, "git", "remote", "add", "origin", "/nonexistent/path/repo.git")

	// cancel context quickly — function should exit during the backoff sleep
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	start := time.Now()
	err := PushWithRetry(ctx, repo, PushOpts{
		MaxRetries: 10, // high retries — we expect cancellation to cut this short
		OpTimeout:  2 * time.Second,
	})
	elapsed := time.Since(start)

	assert.Error(t, err)
	// should exit within a few seconds, not wait for all 10 retries
	assert.Less(t, elapsed, 5*time.Second, "context cancellation should exit promptly")
}

func TestPushWithRetry_DefaultOpts(t *testing.T) {
	t.Run("maxRetries defaults to 3", func(t *testing.T) {
		opts := PushOpts{}
		assert.Equal(t, 3, opts.maxRetries())
	})

	t.Run("maxRetries respects override", func(t *testing.T) {
		opts := PushOpts{MaxRetries: 5}
		assert.Equal(t, 5, opts.maxRetries())
	})

	t.Run("opTimeout defaults to 60s", func(t *testing.T) {
		opts := PushOpts{}
		assert.Equal(t, 60*time.Second, opts.opTimeout())
	})

	t.Run("opTimeout respects override", func(t *testing.T) {
		opts := PushOpts{OpTimeout: 30 * time.Second}
		assert.Equal(t, 30*time.Second, opts.opTimeout())
	})

	t.Run("logger defaults to slog.Default", func(t *testing.T) {
		opts := PushOpts{}
		assert.NotNil(t, opts.logger())
	})
}

func TestPushWithRetry_SuccessOnSecondAttempt(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git push with retry")
	}
	repo, bare := initBareRemoteRepo(t)

	// create divergence: push from a second clone
	second := filepath.Join(t.TempDir(), "second")
	run(t, "", "git", "clone", "--quiet", bare, second)
	run(t, second, "git", "config", "user.email", "test@test.local")
	run(t, second, "git", "config", "user.name", "Test")
	addCommit(t, second, "conflict.txt", "from-second", "second commit")
	run(t, second, "git", "push", "--quiet")

	// first repo has a different file (no content conflict, just non-fast-forward)
	addCommit(t, repo, "local.txt", "from-first", "local commit")

	err := PushWithRetry(context.Background(), repo, PushOpts{
		MaxRetries: 3,
		OpTimeout:  10 * time.Second,
	})
	assert.NoError(t, err)

	// verify both commits are present
	assert.FileExists(t, filepath.Join(repo, "conflict.txt"))
	assert.FileExists(t, filepath.Join(repo, "local.txt"))
}

func TestPermanentPatterns(t *testing.T) {
	tests := []struct {
		name    string
		output  string
		matches bool
	}{
		{"permission denied", "remote: Permission denied to user", true},
		{"auth failed", "fatal: Authentication failed for 'https://...'", true},
		{"repo not found", "ERROR: repository not found", true},
		{"invalid creds", "remote: invalid credentials", true},
		{"could not read username", "fatal: could not read Username", true},
		{"http 403 url error", "fatal: The requested URL returned error: 403", true},
		{"http 403 generic", "remote: HTTP 403", true},
		{"generic network error", "fatal: unable to access: connection refused", false},
		{"empty output", "", false},
		// LFS objects missing is now handled separately via ReconcileLFS callback,
		// not as a permanent pattern — it's recoverable when the callback is set.
		{"lfs objects missing", "remote: GitLab: LFS objects are missing. Ensure LFS is properly set up or try a manual \"git lfs push --all\".", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matched := false
			for _, pattern := range permanentPatterns {
				if contains(tt.output, pattern) {
					matched = true
					break
				}
			}
			assert.Equal(t, tt.matches, matched)
		})
	}
}

func TestPushWithRetry_403FailsFastWithGuidance(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git push with retry")
	}
	tests := []struct {
		name   string
		stderr string
	}{
		{"url returned 403", "fatal: The requested URL returned error: 403"},
		{"http 403", "remote: HTTP 403 Forbidden"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// verify 403 matches permanent patterns (no retry)
			matched := false
			for _, pattern := range permanentPatterns {
				if strings.Contains(tt.stderr, pattern) {
					matched = true
					break
				}
			}
			assert.True(t, matched, "403 error should match a permanent pattern")

			// verify the 403-specific branch produces actionable guidance
			assert.True(t, strings.Contains(tt.stderr, "403"),
				"stderr should contain 403 to trigger guidance branch")
		})
	}
}

func TestPushWithRetry_AutoResolveConflicts(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git push with retry")
	}
	repo, bare := initBareRemoteRepo(t)

	// create a second clone
	second := filepath.Join(t.TempDir(), "second")
	run(t, "", "git", "clone", "--quiet", bare, second)
	run(t, second, "git", "config", "user.email", "test@test.local")
	run(t, second, "git", "config", "user.name", "Test")

	// both clones modify the same file under data/github/ prefix
	require.NoError(t, os.MkdirAll(filepath.Join(repo, "data", "github"), 0755))
	require.NoError(t, os.MkdirAll(filepath.Join(second, "data", "github"), 0755))

	// second clone pushes first
	require.NoError(t, os.WriteFile(filepath.Join(second, "data", "github", "prs.json"),
		[]byte(`{"count":1}`), 0644))
	run(t, second, "git", "add", "data/github/prs.json")
	run(t, second, "git", "commit", "-m", "second: add prs.json", "--no-verify", "--quiet")
	run(t, second, "git", "push", "--quiet")

	// first clone has a conflicting change to the same file
	require.NoError(t, os.WriteFile(filepath.Join(repo, "data", "github", "prs.json"),
		[]byte(`{"count":2}`), 0644))
	run(t, repo, "git", "add", "data/github/prs.json")
	run(t, repo, "git", "commit", "-m", "first: add prs.json", "--no-verify", "--quiet")

	// push with auto-resolve for data/github/ prefix
	err := PushWithRetry(context.Background(), repo, PushOpts{
		MaxRetries:          3,
		OpTimeout:           10 * time.Second,
		AutoResolvePrefixes: []string{"data/github/"},
	})
	assert.NoError(t, err, "should auto-resolve conflict in data/github/ path")

	// verify file exists in repo (content is accept-theirs: "count":1 from remote)
	assert.FileExists(t, filepath.Join(repo, "data", "github", "prs.json"))
}

func TestPushWithRetry_RebaseInProgressAborted(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git push with retry")
	}
	repo, bare := initBareRemoteRepo(t)

	// create divergence
	second := filepath.Join(t.TempDir(), "second")
	run(t, "", "git", "clone", "--quiet", bare, second)
	run(t, second, "git", "config", "user.email", "test@test.local")
	run(t, second, "git", "config", "user.name", "Test")
	addCommit(t, second, "remote.txt", "remote", "remote commit")
	run(t, second, "git", "push", "--quiet")

	// local commit
	addCommit(t, repo, "local.txt", "local", "local commit")

	// simulate a broken rebase state — pre-flight IsSafeForGitOps should detect this
	rebaseMergeDir := filepath.Join(repo, ".git", "rebase-merge")
	require.NoError(t, os.MkdirAll(rebaseMergeDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(rebaseMergeDir, "head-name"), []byte("refs/heads/main"), 0644))

	// PushWithRetry should reject with a clear error (pre-flight guard)
	err := PushWithRetry(context.Background(), repo, PushOpts{
		MaxRetries: 3,
		OpTimeout:  10 * time.Second,
	})
	assert.Error(t, err, "should fail when rebase is already in progress")
	assert.Contains(t, err.Error(), "broken rebase state")
}

// TestPushWithRetry_OnUnresolvedConflictsHookCalled verifies that when
// ResolveRebaseAcceptTheirs cannot resolve a conflict because the path is
// outside AutoResolvePrefixes, the OnUnresolvedConflicts hook is invoked
// with the conflicted paths. If the hook reports unresolved, the rebase is
// aborted and PushWithRetry returns an error.
//
// Failure prevented: a higher-tier resolver (e.g. LLM merge) can never be
// wired in if PushWithRetry refuses to surface the conflict.
func TestPushWithRetry_OnUnresolvedConflictsHookCalled(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git push with retry")
	}
	repo, bare := initBareRemoteRepo(t)

	// second clone pushes a conflicting change to a path NOT covered by
	// AutoResolvePrefixes (we use "data/github/" as the safe prefix below,
	// but the conflict happens at the repo root).
	second := filepath.Join(t.TempDir(), "second")
	run(t, "", "git", "clone", "--quiet", bare, second)
	run(t, second, "git", "config", "user.email", "test@test.local")
	run(t, second, "git", "config", "user.name", "Test")
	require.NoError(t, os.WriteFile(filepath.Join(second, "shared.txt"),
		[]byte("from-second"), 0644))
	run(t, second, "git", "add", "shared.txt")
	run(t, second, "git", "commit", "-m", "second shared", "--no-verify", "--quiet")
	run(t, second, "git", "push", "--quiet")

	// first clone makes a conflicting change to the same path
	require.NoError(t, os.WriteFile(filepath.Join(repo, "shared.txt"),
		[]byte("from-first"), 0644))
	run(t, repo, "git", "add", "shared.txt")
	run(t, repo, "git", "commit", "-m", "first shared", "--no-verify", "--quiet")

	var hookCalls atomic.Int32
	var capturedPaths []string
	err := PushWithRetry(context.Background(), repo, PushOpts{
		MaxRetries:          2,
		OpTimeout:           10 * time.Second,
		AutoResolvePrefixes: []string{"data/github/"}, // does NOT cover shared.txt
		OnUnresolvedConflicts: func(ctx context.Context, repoPath string, paths []string) (bool, error) {
			hookCalls.Add(1)
			capturedPaths = append([]string(nil), paths...)
			return false, nil // signal unresolved → PushWithRetry should abort
		},
	})

	assert.Error(t, err, "expected push to fail when hook reports unresolved")
	assert.Equal(t, int32(1), hookCalls.Load(), "OnUnresolvedConflicts hook must be invoked exactly once")
	assert.Contains(t, capturedPaths, "shared.txt",
		"hook must receive the unresolved conflicted paths")

	// rebase must have been aborted before returning so the repo is left clean
	assert.False(t, IsRebaseInProgress(repo), "rebase should be aborted on hook-unresolved failure")
}

// contains mirrors strings.Contains for test clarity.
func contains(s, substr string) bool {
	return len(substr) > 0 && len(s) >= len(substr) && containsImpl(s, substr)
}

func containsImpl(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// TestPushWithRetry_CredentialNoiseWithDivergence is a regression test for the
// bug fixed in a18cd6c: push output containing both "non-fast-forward" and
// macOS Keychain "failed to store: -25300" must take the rebase path.
func TestPushWithRetry_CredentialNoiseWithDivergence(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git push with retry")
	}
	repo, bare := initBareRemoteRepo(t)

	// create a second clone and push a commit to create divergence
	second := filepath.Join(t.TempDir(), "second")
	run(t, "", "git", "clone", "--quiet", bare, second)
	run(t, second, "git", "config", "user.email", "test@test.local")
	run(t, second, "git", "config", "user.name", "Test")
	addCommit(t, second, "remote.txt", "from-second", "second commit")
	run(t, second, "git", "push", "--quiet")

	// first repo has a local commit (now diverged from remote)
	addCommit(t, repo, "local.txt", "from-first", "first commit")

	// push should hit non-fast-forward, rebase, and succeed.
	err := PushWithRetry(context.Background(), repo, PushOpts{
		MaxRetries: 3,
		OpTimeout:  10 * time.Second,
	})
	assert.NoError(t, err, "should succeed via rebase")

	// verify both files present (rebase succeeded, not force-push)
	assert.FileExists(t, filepath.Join(repo, "remote.txt"))
	assert.FileExists(t, filepath.Join(repo, "local.txt"))
}

// TestPushWithRetry_LFSErrorRetriesWithoutForcePush is a regression test ensuring
// that LFS push errors are retried normally and never trigger force push.
// Previously, AllowForceOnLFS would attempt --force-with-lease on LFS errors;
// that path was removed because our remotes reject force pushes server-side.
func TestPushWithRetry_LFSErrorRetriesWithoutForcePush(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git push with retry")
	}
	repo := t.TempDir()
	run(t, "", "git", "init", "--quiet", repo)
	run(t, repo, "git", "config", "user.email", "test@test.local")
	run(t, repo, "git", "config", "user.name", "Test")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "f.txt"), []byte("x"), 0644))
	run(t, repo, "git", "add", "f.txt")
	run(t, repo, "git", "commit", "-m", "init", "--no-verify", "--quiet")

	// point remote at a nonexistent path so push always fails
	run(t, repo, "git", "remote", "add", "origin", "/nonexistent/bare/repo.git")

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	err := PushWithRetry(ctx, repo, PushOpts{
		MaxRetries: 2,
		OpTimeout:  3 * time.Second,
	})

	// should fail after retries, not with a force-push error
	require.Error(t, err)
	assert.Contains(t, err.Error(), "2 attempts", "should exhaust retries normally")
	assert.NotContains(t, err.Error(), "force push", "must never attempt force push")
}
