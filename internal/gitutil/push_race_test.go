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
	assert.Equal(t, 2, strings.Count(string(calls), "fetch --quiet origin "+branch), "fetch retried exactly once")
	assert.FileExists(t, filepath.Join(repo, "b.txt"))
}
