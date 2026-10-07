package gitutil

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// installFakeGit puts a git wrapper first on PATH that logs every invocation
// and simulates the concurrent-fetch races from #1202:
//
//   - "lockfail": the first `fetch` fails with a cannot-lock-ref error.
//   - "fetchhead": `pull` fails the way git does when another fetch rewrote
//     FETCH_HEAD with several for-merge heads; fetch and rebase pass through.
//
// It returns the path of the invocation log.
func installFakeGit(t *testing.T, mode string) string {
	t.Helper()
	realGit, err := exec.LookPath("git")
	require.NoError(t, err)

	dir := t.TempDir()
	logPath := filepath.Join(dir, "calls.log")
	counter := filepath.Join(dir, "fetch-count")
	script := `#!/bin/sh
echo "$@" >> '` + logPath + `'
# find the git subcommand, skipping -C <path> and -c <k=v> pairs
sub=""; skip=0
for a in "$@"; do
  if [ $skip -eq 1 ]; then skip=0; continue; fi
  case "$a" in -C|-c) skip=1; continue;; esac
  sub="$a"; break
done
case "` + mode + `:$sub" in
  lockfail:fetch)
    if [ ! -e '` + counter + `' ]; then
      touch '` + counter + `'
      echo "error: cannot lock ref 'refs/remotes/origin/main': is at aaa but expected bbb" >&2
      exit 1
    fi;;
  rebasehang:rebase)
    exec sleep 30;;
  fetchfail:fetch)
    echo "fatal: unable to access remote" >&2
    exit 128;;
  fetchhead:pull)
    echo "fatal: Cannot rebase onto multiple branches." >&2
    exit 128;;
esac
exec '` + realGit + `' "$@"
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}

// divergedRepo returns a clone that is behind and ahead of its remote, so the
// first push is rejected as non-fast-forward and the retry must reconcile.
func divergedRepo(t *testing.T) string {
	t.Helper()
	repo, bare := initBareRemoteRepo(t)
	second := filepath.Join(t.TempDir(), "second")
	run(t, "", "git", "clone", "--quiet", bare, second)
	run(t, second, "git", "config", "user.email", "test@test.local")
	run(t, second, "git", "config", "user.name", "Test")
	addCommit(t, second, "b.txt", "from-second", "second clone commit")
	run(t, second, "git", "push", "--quiet")
	addCommit(t, repo, "c.txt", "from-first", "first clone commit")
	return repo
}

func TestPushWithRetry_ConflictAbortsAndNeedsPullCycle(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git push with retry")
	}
	repo, bare := initBareRemoteRepo(t)
	second := filepath.Join(t.TempDir(), "second")
	run(t, "", "git", "clone", "--quiet", bare, second)
	run(t, second, "git", "config", "user.email", "test@test.local")
	run(t, second, "git", "config", "user.name", "Test")
	addCommit(t, second, "meta.json", `{"from":"second"}`, "second meta")
	run(t, second, "git", "push", "--quiet")
	addCommit(t, repo, "meta.json", `{"from":"first"}`, "first meta")

	err := PushWithRetry(context.Background(), repo, PushOpts{MaxRetries: 3, OpTimeout: 10 * time.Second})
	require.ErrorIs(t, err, ErrNeedsPullCycle)
	assert.False(t, IsRebaseInProgress(repo), "rebase must be aborted")
	content, readErr := os.ReadFile(filepath.Join(repo, "meta.json"))
	require.NoError(t, readErr)
	assert.JSONEq(t, `{"from":"first"}`, string(content), "local commit preserved")
}

func TestPushWithRetry_RebasesOntoRenamedTrackingRef(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git push with retry")
	}
	repo := divergedRepo(t)
	branch := currentBranch(t, repo)
	// map the remote branch into a differently named tracking ref
	run(t, repo, "git", "config", "remote.origin.fetch", "+refs/heads/"+branch+":refs/remotes/origin/stable")
	run(t, repo, "git", "update-ref", "-d", "refs/remotes/origin/"+branch)
	run(t, repo, "git", "fetch", "--quiet", "origin")
	run(t, repo, "git", "branch", "--set-upstream-to=origin/stable")

	err := PushWithRetry(context.Background(), repo, PushOpts{MaxRetries: 3, OpTimeout: 10 * time.Second})
	require.NoError(t, err)
	assert.FileExists(t, filepath.Join(repo, "b.txt"))
	assert.FileExists(t, filepath.Join(repo, "c.txt"))
}

func TestPushWithRetry_FetchFailureIsReportedWithoutRebase(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git push with retry")
	}
	repo := divergedRepo(t)
	logPath := installFakeGit(t, "fetchfail")

	err := PushWithRetry(context.Background(), repo, PushOpts{MaxRetries: 3, OpTimeout: 10 * time.Second})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "git fetch failed during retry")
	assert.NotErrorIs(t, err, ErrNeedsPullCycle)
	assert.False(t, IsRebaseInProgress(repo))

	calls, readErr := os.ReadFile(logPath)
	require.NoError(t, readErr)
	assert.Equal(t, 1, strings.Count(string(calls), "fetch --quiet"), "non-lock failures are not retried")
	assert.NotContains(t, string(calls), "rebase")
}

func TestPushWithRetry_RebaseTimeoutLeavesRepoClean(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git push with retry")
	}
	repo := divergedRepo(t)
	installFakeGit(t, "rebasehang")

	start := time.Now()
	err := PushWithRetry(context.Background(), repo, PushOpts{MaxRetries: 3, OpTimeout: 2 * time.Second})
	require.Error(t, err)
	assert.Less(t, time.Since(start), 20*time.Second, "hung rebase must be cut off by the pull budget")
	assert.False(t, IsRebaseInProgress(repo))
}

func TestPushWithRetry_NoUpstreamFallsBackToOriginBranch(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git push with retry")
	}
	repo := divergedRepo(t)
	run(t, repo, "git", "config", "push.default", "current")
	run(t, repo, "git", "branch", "--unset-upstream")

	err := PushWithRetry(context.Background(), repo, PushOpts{MaxRetries: 3, OpTimeout: 10 * time.Second})
	require.NoError(t, err)
	assert.FileExists(t, filepath.Join(repo, "b.txt"))
}

func TestUpstreamRefs_DetachedHeadErrors(t *testing.T) {
	repo, _ := initBareRemoteRepo(t)
	run(t, repo, "git", "checkout", "--quiet", "--detach")

	_, _, _, err := upstreamRefs(context.Background(), repo)
	require.Error(t, err)

	_, _, fetchErr := fetchUpstream(context.Background(), repo)
	require.Error(t, fetchErr)
}

func TestFetchUpstream_CancelDuringLockWait(t *testing.T) {
	repo := divergedRepo(t)
	installFakeGit(t, "lockfail")
	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
	defer cancel()

	_, _, err := fetchUpstream(ctx, repo)
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

func currentBranch(t *testing.T, repo string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", repo, "branch", "--show-current").Output()
	require.NoError(t, err)
	return strings.TrimSpace(string(out))
}

func TestPushWithRetry_RebasesExplicitRefNotFetchHead(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git push with retry")
	}
	repo := divergedRepo(t)
	branch := currentBranch(t, repo)
	logPath := installFakeGit(t, "fetchhead")

	err := PushWithRetry(context.Background(), repo, PushOpts{MaxRetries: 3, OpTimeout: 10 * time.Second})
	require.NoError(t, err)

	assert.FileExists(t, filepath.Join(repo, "b.txt"))
	assert.FileExists(t, filepath.Join(repo, "c.txt"))

	calls, err := os.ReadFile(logPath)
	require.NoError(t, err)
	assert.NotContains(t, string(calls), "pull")
	assert.Contains(t, string(calls), "rebase --autostash --quiet refs/remotes/origin/"+branch)
}

func TestPushWithRetry_RetriesFetchOnCannotLockRef(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git push with retry")
	}
	repo := divergedRepo(t)
	branch := currentBranch(t, repo)
	logPath := installFakeGit(t, "lockfail")

	err := PushWithRetry(context.Background(), repo, PushOpts{MaxRetries: 3, OpTimeout: 10 * time.Second})
	require.NoError(t, err)

	calls, err := os.ReadFile(logPath)
	require.NoError(t, err)
	assert.Equal(t, 2, strings.Count(string(calls), "fetch --quiet origin +refs/heads/"+branch+":refs/remotes/origin/"+branch), "fetch retried exactly once")
	assert.FileExists(t, filepath.Join(repo, "b.txt"))
}
