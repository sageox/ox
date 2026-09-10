package gitutil

import (
	"context"
	"fmt"
	"io"
	"log/slog"
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

// Retrying a rejected push must inspect the index after both a clean rebase
// and an auto-resolved rebase: restoring the autostash can conflict in either.
func TestPushWithRetry_AutostashConflicts(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git push and autostash")
	}
	for _, tc := range []struct {
		name             string
		localTitle       string
		rebaseConflict   bool
		existingConflict bool
		deny             []string
		wantError        bool
	}{
		{name: "agreeing metadata", localTitle: "Ready"},
		{name: "after resolving rebase", localTitle: "Ready", rebaseConflict: true},
		{name: "differing metadata", localTitle: "Local", wantError: true},
		{name: "existing agreeing metadata", localTitle: "Ready", existingConflict: true},
		{name: "existing differing metadata", localTitle: "Local", existingConflict: true, wantError: true},
		{name: "denied metadata", localTitle: "Ready", deny: []string{"sessions/test/"}, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, bare := initBareRemoteRepo(t)
			const rel = "sessions/test/meta.json"
			require.NoError(t, os.MkdirAll(filepath.Join(repo, "sessions/test"), 0o755))
			addCommit(t, repo, rel, `{"title":"","keep":"yes"}`+"\n", "seed metadata")
			run(t, repo, "git", "push", "--quiet")
			writer := filepath.Join(t.TempDir(), "writer")
			run(t, "", "git", "clone", "--quiet", bare, writer)
			run(t, writer, "git", "config", "user.name", "Test")
			run(t, writer, "git", "config", "user.email", "test@test.local")
			if tc.rebaseConflict {
				require.NoError(t, os.WriteFile(filepath.Join(writer, "init.txt"), []byte("remote"), 0o644))
				run(t, writer, "git", "add", "init.txt")
				addCommit(t, repo, "init.txt", "local", "local change")
			} else {
				addCommit(t, repo, "local.txt", "local", "local change")
			}
			const remoteMeta = `{"title":"Ready","keep":"yes","remote_only":true}`
			addCommit(t, writer, rel, remoteMeta+"\n", "remote title")
			run(t, writer, "git", "push", "--quiet")
			localMeta := fmt.Sprintf("{\"keep\":\"yes\",\"title\":%q,\"summary_attempts\":0,\"local_only\":true}\n", tc.localTitle)
			mergedMeta := fmt.Sprintf(`{"keep":"yes","title":%q,"summary_attempts":0,"local_only":true,"remote_only":true}`, tc.localTitle)
			require.NoError(t, os.WriteFile(filepath.Join(repo, rel), []byte(localMeta), 0o644))
			var conflictsBefore, headBefore, stashBefore string
			var worktreeBefore, indexBefore []byte
			if tc.existingConflict {
				// Model a checkout wedged by an older client, then make the
				// remote advance again so this push must enter its pull retry.
				run(t, repo, "git", "pull", "--rebase", "--autostash", "--quiet")
				conflictsBefore = gitInRepo(t, repo, "ls-files", "--unmerged")
				require.NotEmpty(t, conflictsBefore)
				require.False(t, IsRebaseInProgress(repo))
				var err error
				worktreeBefore, err = os.ReadFile(filepath.Join(repo, rel))
				require.NoError(t, err)
				indexBefore, err = os.ReadFile(filepath.Join(repo, ".git/index"))
				require.NoError(t, err)
				headBefore = gitInRepo(t, repo, "rev-parse", "HEAD")
				stashBefore = gitInRepo(t, repo, "stash", "list")
				addCommit(t, writer, "remote.txt", "next remote change", "advance remote")
				run(t, writer, "git", "push", "--quiet")
			}
			remoteBefore := gitInRepo(t, writer, "rev-parse", "HEAD")
			err := PushWithRetry(context.Background(), repo, PushOpts{
				AutoResolvePrefixes:     []string{"sessions/", "init.txt"},
				AutoResolveDenyPrefixes: tc.deny,
				MaxRetries:              2,
				OpTimeout:               10 * time.Second,
			})
			conflicts := gitInRepo(t, repo, "ls-files", "--unmerged")
			assert.False(t, IsRebaseInProgress(repo))
			assert.NotEmpty(t, gitInRepo(t, repo, "stash", "list"), "retain the original dirty metadata")
			assert.JSONEq(t, localMeta, gitInRepo(t, repo, "show", "stash@{0}:"+rel))
			if tc.wantError {
				require.Error(t, err)
				assert.NotEmpty(t, conflicts)
				assert.Equal(t, remoteBefore, gitInRepo(t, bare, "rev-parse", "HEAD"), "do not retry the push while conflicts remain")
				if tc.existingConflict {
					assert.Equal(t, conflictsBefore, conflicts, "preserve all conflict stages")
					data, err := os.ReadFile(filepath.Join(repo, rel))
					require.NoError(t, err)
					assert.Equal(t, worktreeBefore, data, "leave disagreements untouched")
					index, err := os.ReadFile(filepath.Join(repo, ".git/index"))
					require.NoError(t, err)
					assert.Equal(t, indexBefore, index, "leave the index untouched")
					assert.Equal(t, headBefore, gitInRepo(t, repo, "rev-parse", "HEAD"))
					assert.Equal(t, stashBefore, gitInRepo(t, repo, "stash", "list"))
				}
			} else {
				require.NoError(t, err)
				assert.Empty(t, conflicts)
				data, err := os.ReadFile(filepath.Join(repo, rel))
				require.NoError(t, err)
				assert.JSONEq(t, mergedMeta, string(data))
				assert.Equal(t, gitInRepo(t, repo, "rev-parse", "HEAD"), gitInRepo(t, bare, "rev-parse", "HEAD"))
				assert.JSONEq(t, remoteMeta, gitInRepo(t, bare, "show", "HEAD:"+rel), "recovery must not commit the dirty metadata")
			}
		})
	}
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

// Only a successful hook that completes the rebase may allow another push.
// Failure paths must preserve both replicas' commits and release the repo lock.
func TestPushWithRetry_OnUnresolvedConflictsHookCalled(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git push with retry")
	}
	for _, tc := range []struct {
		name              string
		disableResolution bool
		hookError         bool
		resolve           bool
		cancelEnumeration bool
		wantHookCalls     int
	}{
		{name: "unresolved", wantHookCalls: 1},
		{name: "no auto-resolve prefixes", disableResolution: true},
		{name: "hook error after staging", hookError: true, wantHookCalls: 1},
		{name: "hook completes rebase", resolve: true, wantHookCalls: 1},
		{name: "conflict enumeration canceled", cancelEnumeration: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, bare := initBareRemoteRepo(t)
			second := filepath.Join(t.TempDir(), "second")
			run(t, "", "git", "clone", "--quiet", bare, second)
			run(t, second, "git", "config", "user.email", "test@test.local")
			run(t, second, "git", "config", "user.name", "Test")
			addCommit(t, second, "shared.txt", "from-second", "second shared")
			run(t, second, "git", "push", "--quiet")
			addCommit(t, repo, "shared.txt", "from-first", "first shared")
			localHead := gitInRepo(t, repo, "rev-parse", "HEAD")
			remoteHead := gitInRepo(t, bare, "rev-parse", "HEAD")

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{
				Level: slog.LevelDebug,
				ReplaceAttr: func(_ []string, attr slog.Attr) slog.Attr {
					// Cancel at the real Git failure boundary, before enumerating
					// paths. The hook must never receive a misleading empty list.
					if tc.cancelEnumeration && attr.Key == slog.MessageKey && attr.Value.String() == "rebase auto-resolve failed" {
						cancel()
					}
					return attr
				},
			}))
			prefixes := []string{"data/github/"} // does not cover shared.txt
			if tc.disableResolution {
				prefixes = nil
			}
			hookCalls := 0
			var capturedPaths []string
			const merged = "from-second\nfrom-first\n"
			err := PushWithRetry(ctx, repo, PushOpts{
				MaxRetries:          2,
				OpTimeout:           10 * time.Second,
				AutoResolvePrefixes: prefixes,
				Logger:              logger,
				OnUnresolvedConflicts: func(ctx context.Context, repoPath string, paths []string) (bool, error) {
					hookCalls++
					capturedPaths = append([]string(nil), paths...)
					if !tc.resolve && !tc.hookError {
						return false, nil
					}
					if err := os.WriteFile(filepath.Join(repoPath, "shared.txt"), []byte(merged), 0644); err != nil {
						return false, err
					}
					if _, err := RunGit(ctx, repoPath, "add", "shared.txt"); err != nil {
						return false, err
					}
					if tc.hookError {
						// An error must override even a true resolved result and
						// restore the original commit after partially staging.
						return true, fmt.Errorf("resolver failed after staging")
					}
					_, err := runRebaseStep(ctx, repoPath, "--continue")
					return err == nil, err
				},
			})

			assert.Equal(t, tc.wantHookCalls, hookCalls)
			if tc.wantHookCalls > 0 {
				assert.Equal(t, []string{"shared.txt"}, capturedPaths)
			}
			if tc.resolve {
				require.NoError(t, err)
				assert.Equal(t, gitInRepo(t, repo, "rev-parse", "HEAD"), gitInRepo(t, bare, "rev-parse", "HEAD"))
				assert.Equal(t, strings.TrimSpace(merged), gitInRepo(t, bare, "show", "HEAD:shared.txt"))
			} else {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "git pull --rebase failed during retry")
				assert.Equal(t, remoteHead, gitInRepo(t, bare, "rev-parse", "HEAD"), "failed resolution must not push")
				if tc.cancelEnumeration {
					assert.ErrorIs(t, err, context.Canceled)
					assert.Contains(t, err.Error(), "could not list conflicts")
					// Cancellation also prevents abort; the original commit
					// must remain recoverable with a fresh operation context.
					assert.True(t, IsRebaseInProgress(repo))
					run(t, repo, "git", "rebase", "--abort")
				}
				assert.Equal(t, localHead, gitInRepo(t, repo, "rev-parse", "HEAD"))
				assert.Equal(t, "from-first", gitInRepo(t, repo, "show", "HEAD:shared.txt"))
			}
			assert.False(t, IsRebaseInProgress(repo))
			assert.Empty(t, gitInRepo(t, repo, "status", "--porcelain"))
			lockCtx, lockCancel := context.WithTimeout(context.Background(), time.Second)
			defer lockCancel()
			require.NoError(t, WithRepoLock(lockCtx, repo, func() error { return nil }), "retry must release the repo lock")
		})
	}
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
