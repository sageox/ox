package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/sageox/ox/internal/gitutil"
	"github.com/sageox/ox/internal/ledger"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Ledger pull that loses the race to another writer (sageox/ox#1541) ---
//
// Several daemons share one Ledger clone, and CLI writers commit to it too.
// When one of them commits to main while this daemon's `pull --rebase` replays,
// git refuses to finish the rebase. The daemon used to treat that as fatal and
// leave the Ledger wedged in "rebase in progress", and every abort that later
// cleared it rewound main past the other writer's commit. These run the real
// pull pipeline against a real remote, with the other writer injected at the
// exact race point.

// installMidRebaseWriter adds a hookName hook that lands up to limit commits on
// main, one per run, the way a Ledger writer does: a private index,
// commit-tree, and a compare-and-swap update-ref. Commit n adds
// sessions/raced-<n>/summary.json. Two hooks reach the two race points:
//
//   - pre-rebase runs after git pinned orig-head, so main moves mid-replay;
//   - post-commit runs when `rebase --continue` commits a resolved step (not
//     during a pull that halts on its first conflict), so main moves while
//     conflicts are being resolved.
func installMidRebaseWriter(t *testing.T, repo, hookName string, limit int) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("windows: the injected writer is a POSIX shell hook")
	}
	hook := fmt.Sprintf(`#!/bin/sh
set -e
gitdir=$(git rev-parse --absolute-git-dir)
n=$(cat "$gitdir/ox-raced-writes" 2>/dev/null || echo 0)
[ "$n" -ge %d ] && exit 0
n=$((n + 1))
echo "$n" > "$gitdir/ox-raced-writes"
tip=$(git rev-parse refs/heads/main)
blob=$(printf 'summary %%s' "$n" | git hash-object -w --stdin)
export GIT_INDEX_FILE="$gitdir/ox-raced-index"
git read-tree "$tip"
git update-index --add --cacheinfo "100644,$blob,sessions/raced-$n/summary.json"
tree=$(git write-tree)
rm -f "$GIT_INDEX_FILE"
commit=$(git commit-tree "$tree" -p "$tip" -m "summary: raced-$n")
git update-ref refs/heads/main "$commit" "$tip"
`, limit)
	path := filepath.Join(repo, ".git", "hooks", hookName)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(hook), 0o755))
}

// racedLedgerClone returns a clone that is ahead and behind its remote, holds
// an uncommitted edit to a tracked file, and whose next `writes` rebases each
// lose the race to another writer.
func racedLedgerClone(t *testing.T, writes int) string {
	t.Helper()
	isolateCredentials(t)
	bare, writer := initBareRepo(t, "raced")
	write := func(dir, rel, content string) {
		require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(dir, rel)), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, rel), []byte(content), 0o644))
	}
	write(writer, "notes.txt", "committed")
	gitInDir(t, writer, "add", "-A")
	gitInDir(t, writer, "commit", "-m", "seed")
	gitInDir(t, writer, "push", "origin", "main")

	local := filepath.Join(t.TempDir(), "local")
	gitInDir(t, writer, "clone", bare, local)
	gitConfig(t, local)
	write(local, "sessions/local/meta.json", `{"s":"local"}`)
	gitInDir(t, local, "add", "-A")
	gitInDir(t, local, "commit", "-m", "local session")

	write(writer, "sessions/cloud/meta.json", `{"s":"cloud"}`)
	gitInDir(t, writer, "add", "-A")
	gitInDir(t, writer, "commit", "-m", "cloud session")
	gitInDir(t, writer, "push", "origin", "main")

	write(local, "notes.txt", "uncommitted edit")
	installMidRebaseWriter(t, local, "pre-rebase", writes)
	return local
}

func pullRacedLedger(t *testing.T, local string) ManagedRepoPullResult {
	t.Helper()
	s := newTestScheduler(t.TempDir())
	return s.pullManagedRepo(context.Background(), ManagedRepoPullOpts{
		RepoPath:         local,
		RepoName:         "ledger",
		ImmutablePaths:   true,
		DetectDivergence: true,
		ResolveRules:     ledger.DefaultResolveRules,
		Logger:           discardLogger(),
	})
}

// assertRacedCommitsKept checks that every commit the other writer landed is
// on main and nothing local was lost.
func assertRacedCommitsKept(t *testing.T, local string, writes int) {
	t.Helper()
	assert.False(t, gitutil.IsRebaseInProgress(local), "the Ledger must not be left mid-rebase")
	for n := 1; n <= writes; n++ {
		out, err := runGitOut(t, local, "cat-file", "-e", fmt.Sprintf("main:sessions/raced-%d/summary.json", n))
		assert.NoError(t, err, "main must keep raced commit %d, not leave it to the reflog: %s", n, out)
	}
	out, err := runGitOut(t, local, "cat-file", "-e", "main:sessions/local/meta.json")
	assert.NoError(t, err, "the local session commit must survive: %s", out)
	notes, err := os.ReadFile(filepath.Join(local, "notes.txt"))
	require.NoError(t, err)
	assert.Equal(t, "uncommitted edit", string(notes), "the autostashed edit must come back")
}

// TestPullManagedRepo_BranchMovedMidRebase_RestartsAndSyncs is the issue's
// incident: one writer commits mid-rebase. Failure prevented: the Ledger
// wedged in "rebase in progress", every session stop and plan save failing
// until a human repaired it, and the raced summary never reaching the remote.
func TestPullManagedRepo_BranchMovedMidRebase_RestartsAndSyncs(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git pull against a bare remote")
	}
	local := racedLedgerClone(t, 1)

	result := pullRacedLedger(t, local)

	require.NoError(t, result.Err, "losing one race must restart the pull in the same cycle, not fail it")
	assert.Nil(t, result.Issue, "a lost race is not a conflict or a divergence")
	assert.False(t, result.Skipped)
	assertRacedCommitsKept(t, local, 1)
	out, err := runGitOut(t, local, "merge-base", "--is-ancestor", "origin/main", "main")
	assert.NoError(t, err, "the restarted pull must land the remote's commits: %s", out)
}

// TestPullManagedRepo_BranchMovedEveryAttempt_FailsCleanly: a writer that keeps
// winning exhausts the in-cycle retry. Failure prevented: the retry budget
// running out with the rebase left on disk, or a stuck/diverged issue that
// tells the user to hand-repair a clone the next cycle fixes on its own.
func TestPullManagedRepo_BranchMovedEveryAttempt_FailsCleanly(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git pull against a bare remote")
	}
	local := racedLedgerClone(t, 99)

	result := pullRacedLedger(t, local)

	require.Error(t, result.Err)
	assert.ErrorContains(t, result.Err, "another writer moved the branch")
	assert.Nil(t, result.Issue, "a lost race is retried next cycle; it must not raise an issue")
	writes, err := os.ReadFile(filepath.Join(local, ".git", "ox-raced-writes"))
	require.NoError(t, err)
	require.Equal(t, "2\n", string(writes), "the pull must try exactly twice")
	assertRacedCommitsKept(t, local, 2)
}

// TestPullManagedRepo_BranchMovedDuringConflictResolution: the pull halts on a
// sessions/ conflict, and the other writer lands while the resolver commits
// the fix. Failure prevented: that race skipping the in-cycle restart and
// costing a failed sync cycle (and a backoff step) for a conflict the
// production rules resolve on their own.
func TestPullManagedRepo_BranchMovedDuringConflictResolution(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git pull against a bare remote")
	}
	local := makeSessionMetaConflictClone(t)
	installMidRebaseWriter(t, local, "post-commit", 1)

	result := pullRacedLedger(t, local)

	require.NoError(t, result.Err, "a race during conflict resolution must restart the pull in the same cycle")
	assert.Nil(t, result.Issue)
	assert.True(t, result.AutoResolved, "the restarted pull must resolve the conflict itself")
	assert.False(t, gitutil.IsRebaseInProgress(local))
	out, err := runGitOut(t, local, "cat-file", "-e", "main:sessions/raced-1/summary.json")
	assert.NoError(t, err, "main must keep the commit landed mid-resolution: %s", out)
	out, err = runGitOut(t, local, "merge-base", "--is-ancestor", "origin/main", "main")
	assert.NoError(t, err, "the restarted pull must land the remote's commits: %s", out)
}

// TestPullManagedRepo_BranchMovedDuringEveryResolution_FailsCleanly: a writer
// that wins the race on every resolve attempt exhausts the in-cycle retry.
// Failure prevented: the second lost race being reported as a conflict, or
// leaving the rebase on disk.
func TestPullManagedRepo_BranchMovedDuringEveryResolution_FailsCleanly(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git pull against a bare remote")
	}
	local := makeSessionMetaConflictClone(t)
	installMidRebaseWriter(t, local, "post-commit", 99)

	result := pullRacedLedger(t, local)

	require.ErrorContains(t, result.Err, "another writer moved the branch")
	assert.Nil(t, result.Issue, "a lost race is retried next cycle; it must not raise an issue")
	assert.False(t, gitutil.IsRebaseInProgress(local))
	for n := 1; n <= 2; n++ {
		out, err := runGitOut(t, local, "cat-file", "-e", fmt.Sprintf("main:sessions/raced-%d/summary.json", n))
		assert.NoError(t, err, "main must keep raced commit %d: %s", n, out)
	}
}

// TestBranchMovedResult_StuckAbortPointsAtDoctor: when the re-anchored abort
// itself fails, the issue must send the coworker to doctor, never to a plain
// `git rebase --abort`, which rewinds the moved branch past the other
// writer's commit. Failure prevented: the recovery hint causing the data loss.
func TestBranchMovedResult_StuckAbortPointsAtDoctor(t *testing.T) {
	result := branchMovedResult(ManagedRepoPullResult{}, errors.New("abort failed"), errors.New("pull failed"), "ledger")

	require.Error(t, result.Err)
	require.NotNil(t, result.Issue)
	assert.Equal(t, IssueTypeRebaseStuck, result.Issue.Type)
	assert.True(t, result.Issue.RequiresConfirm)
	assert.Contains(t, result.Issue.Summary, "ox doctor --fix")
	assert.NotContains(t, result.Issue.Summary, "rebase --abort")
}
