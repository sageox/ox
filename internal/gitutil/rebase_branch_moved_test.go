package gitutil

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Rebase whose branch another writer moved (sageox/ox#1541) ---
//
// A sync rebase records the branch tip as orig-head, replays, and finishes with
// a compare-and-swap of the branch against orig-head. When another Ledger
// writer (a second daemon, a session summary, a murmur) commits to the branch
// in between, git refuses to finish and leaves the rebase on disk. Every abort
// path then rewound the branch to orig-head, dropping the writer's commit to
// the reflog. These drive a real `git pull --rebase --autostash` and inject the
// writer at the exact race point, then hold every recovery entry point to one
// contract: the rebase clears, the moved branch keeps the writer's commit, the
// autostash comes back, and the Ledger syncs afterward.

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

// racedRebase leaves f.local in the wedge from the issue: the local clone is
// ahead and behind, carries an uncommitted edit for --autostash, and a writer
// moved main during the pull's rebase. halted picks the second shape of the
// same race: the rebase also stopped on a conflict before reaching its finish.
// Returns the moved branch tip, which holds the writer's commit.
func racedRebase(t *testing.T, f *ledgerFixture, halted bool) string {
	t.Helper()
	// tracked, so the edit below is what --autostash parks; both shapes'
	// local commits pick it up
	f.write(f.local, "notes.txt", "committed")
	if halted {
		f.diverge("sessions/s1/meta.json", `{"s":"local"}`, `{"s":"cloud"}`)
	} else {
		f.cloudWrites("sessions/cloud/meta.json", `{"s":"cloud"}`, "cloud session")
		f.write(f.local, "sessions/local/meta.json", `{"s":"local"}`)
		f.commitAll(f.local, "local session")
	}
	f.write(f.local, "notes.txt", "uncommitted edit")
	installMidRebaseWriter(t, f.local, "pre-rebase", 1)

	out, ok := f.gitAllowFail(f.local, "pull", "--rebase", "--autostash")
	require.False(t, ok, "the pull must lose the race:\n%s", out)
	require.True(t, IsRebaseInProgress(f.local), "the fixture must be wedged mid-rebase")
	require.True(t, RebaseBranchMoved(context.Background(), f.local), "the writer must have moved main mid-rebase")
	require.FileExists(t, filepath.Join(f.local, ".git", "rebase-merge", "autostash"), "the edit must be parked in the rebase")
	if !halted {
		require.Contains(t, out, "cannot lock ref 'refs/heads/main'", "the finish must fail its compare-and-swap")
	}
	return f.git(f.local, "rev-parse", "refs/heads/main")
}

// assertKeptRacedCommit is the contract every recovery path must meet.
func assertKeptRacedCommit(t *testing.T, f *ledgerFixture, raced string) {
	t.Helper()
	assert.False(t, IsRebaseInProgress(f.local), "the rebase must be cleared")
	_, onMain := f.gitAllowFail(f.local, "merge-base", "--is-ancestor", raced, "refs/heads/main")
	assert.True(t, onMain, "main must keep the commit another writer landed mid-rebase, not leave it to the reflog")
	assert.Equal(t, "refs/heads/main", f.git(f.local, "symbolic-ref", "HEAD"), "HEAD must be back on main")
	assert.Equal(t, "summary 1", f.fileContent("sessions/raced-1/summary.json"), "the raced file must be checked out")
	assert.Equal(t, "uncommitted edit", f.fileContent("notes.txt"), "the autostash must be restored")

	// a halted shape still owes its conflict to the ordinary resolver
	_, err := f.reconcile([]string{"sessions/"})
	require.NoError(t, err)
	f.assertMakesProgress()
	_, published := f.gitAllowFail(f.bare, "cat-file", "-e", "main:sessions/raced-1/summary.json")
	assert.True(t, published, "the raced commit must reach the remote on the next sync")
}

// TestRebaseRecovery_KeepsCommitLandedMidRebase holds every abort entry point
// to the contract, for both shapes of the race. Failure prevented: a session
// summary committed during a sync silently leaves main and never uploads.
func TestRebaseRecovery_KeepsCommitLandedMidRebase(t *testing.T) {
	recoveries := []struct {
		name    string
		recover func(ctx context.Context, repo string) error
	}{
		{"AuditAndAbort (pull and push ladders)", func(ctx context.Context, repo string) error {
			return AuditAndAbort(ctx, repo, AuditOpRebase, "test", quietLogger())
		}},
		{"AbortOrClearRebase (doctor)", func(ctx context.Context, repo string) error {
			return AbortOrClearRebase(ctx, repo, "test", quietLogger())
		}},
		{"RescueIfNeededThenAbort (stale wedge, pull timeout)", func(ctx context.Context, repo string) error {
			_, err := RescueIfNeededThenAbort(ctx, repo, "test", quietLogger())
			return err
		}},
	}
	for _, shape := range []struct {
		name   string
		halted bool
	}{{"finish refused", false}, {"halted on a conflict", true}} {
		for _, rc := range recoveries {
			t.Run(shape.name+"/"+rc.name, func(t *testing.T) {
				t.Parallel()
				f := newLedgerFixture(t)
				raced := racedRebase(t, f, shape.halted)

				require.NoError(t, rc.recover(context.Background(), f.local))
				assertKeptRacedCommit(t, f, raced)
			})
		}
	}
}

// TestAuditAndAbort_UnmovedBranchStillRewinds is the negative control: with no
// concurrent writer, an abort must still return the branch to where the rebase
// started. Failure prevented: re-anchoring on every abort, which would keep a
// half-finished replay instead of undoing it.
func TestAuditAndAbort_UnmovedBranchStillRewinds(t *testing.T) {
	t.Parallel()
	f := newLedgerFixture(t)
	f.diverge("sessions/s1/meta.json", `{"s":"local"}`, `{"s":"cloud"}`)
	before := f.git(f.local, "rev-parse", "refs/heads/main")
	f.gitAllowFail(f.local, "pull", "--rebase", "--autostash")
	require.True(t, IsRebaseInProgress(f.local))
	require.False(t, RebaseBranchMoved(context.Background(), f.local))

	require.NoError(t, AuditAndAbort(context.Background(), f.local, AuditOpRebase, "test", quietLogger()))
	assert.Equal(t, before, f.git(f.local, "rev-parse", "refs/heads/main"))
}

// TestAuditAndAbort_DeletedBranchIsNotRefused: a rebase whose branch was
// deleted has nothing on that branch to keep, so the abort must proceed.
// Failure prevented: the fail-closed refusal turning a deleted branch into a
// wedge no cycle can clear.
func TestAuditAndAbort_DeletedBranchIsNotRefused(t *testing.T) {
	t.Parallel()
	f := newLedgerFixture(t)
	racedRebase(t, f, false)
	f.git(f.local, "update-ref", "-d", "refs/heads/main")

	require.NoError(t, AuditAndAbort(context.Background(), f.local, AuditOpRebase, "test", quietLogger()))
	assert.False(t, IsRebaseInProgress(f.local))
}

// TestResolveRebase_RefusesRebaseWhoseBranchMoved: no resolution can finish a
// rebase whose branch moved, and the empty-step advance would --skip it, so the
// resolver must hand it back for a re-anchored abort. Failure prevented: the
// resolve ladder grinding --continue/--skip (and the LLM tier) on a rebase that
// cannot finish, then reporting a conflict that does not exist.
func TestResolveRebase_RefusesRebaseWhoseBranchMoved(t *testing.T) {
	t.Parallel()
	f := newLedgerFixture(t)
	raced := racedRebase(t, f, false)

	err := ResolveRebaseAcceptTheirs(context.Background(), f.local, []string{"sessions/"})
	require.ErrorIs(t, err, ErrRebaseBranchMoved)
	assert.True(t, IsRebaseInProgress(f.local), "the resolver must not clear the rebase itself")
	assert.Equal(t, raced, f.git(f.local, "rev-parse", "refs/heads/main"))
}

// TestResolveRebase_RefusesBranchMovedDuringContinue: the branch can also move
// after a pass starts, when `--continue` commits a resolved step. The empty-step
// advance must hand that back too instead of trying --skip. Failure prevented:
// a moved branch surfacing as a generic resolve failure, which the daemon
// treats as a conflict instead of restarting the pull.
func TestResolveRebase_RefusesBranchMovedDuringContinue(t *testing.T) {
	t.Parallel()
	f := newLedgerFixture(t)
	f.diverge("sessions/s1/meta.json", `{"s":"local"}`, `{"s":"cloud"}`)
	installMidRebaseWriter(t, f.local, "post-commit", 1)
	f.gitAllowFail(f.local, "pull", "--rebase", "--autostash")
	require.True(t, IsRebaseInProgress(f.local))
	require.False(t, RebaseBranchMoved(context.Background(), f.local), "the halted pull must not have moved main yet")

	err := ResolveRebaseAcceptTheirs(context.Background(), f.local, []string{"sessions/"})
	require.ErrorIs(t, err, ErrRebaseBranchMoved)

	raced := f.git(f.local, "rev-parse", "refs/heads/main")
	require.NoError(t, AuditAndAbort(context.Background(), f.local, AuditOpRebase, "test", quietLogger()))
	_, onMain := f.gitAllowFail(f.local, "merge-base", "--is-ancestor", raced, "refs/heads/main")
	assert.True(t, onMain, "main must keep the commit landed mid-resolution")
	_, err = f.reconcile([]string{"sessions/"})
	require.NoError(t, err)
	f.assertMakesProgress()
}

// TestRebaseBranchMoved_StatesWithNoBranchToRewind: an abort can only lose a
// commit when the rebase names a branch and pinned where it started. Without
// either, there is nothing to re-anchor and the abort must not be refused.
// Failure prevented: a detached or zombie rebase that no recovery can clear.
func TestRebaseBranchMoved_StatesWithNoBranchToRewind(t *testing.T) {
	for _, tc := range []struct {
		name   string
		wedge  func(f *ledgerFixture)
		aborts bool
	}{
		{"rebase began on a detached HEAD", func(f *ledgerFixture) {
			f.git(f.local, "checkout", "-q", "--detach")
			f.gitAllowFail(f.local, "rebase", "--exec", "false", "HEAD~1")
		}, true},
		{"zombie state missing orig-head", func(f *ledgerFixture) {
			f.gitAllowFail(f.local, "rebase", "--exec", "false", "HEAD~1")
			require.NoError(t, os.Remove(filepath.Join(f.local, ".git", "rebase-merge", "orig-head")))
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newLedgerFixture(t)
			f.write(f.local, "sessions/local/meta.json", `{"s":"local"}`)
			f.commitAll(f.local, "local session")
			tc.wedge(f)
			require.True(t, IsRebaseInProgress(f.local), "the fixture must be mid-rebase")

			require.False(t, RebaseBranchMoved(context.Background(), f.local))
			require.NoError(t, reanchorMovedRebase(context.Background(), f.local, quietLogger()), "nothing to re-anchor is not a refusal")
			if tc.aborts {
				require.NoError(t, AuditAndAbort(context.Background(), f.local, AuditOpRebase, "test", quietLogger()))
				assert.False(t, IsRebaseInProgress(f.local))
			}
		})
	}
}

// TestAuditAndAbort_RefusesWhenBranchTipUnreadable is the fail-closed half: if
// the branch tip cannot be read, the abort cannot know whether it would rewind
// a commit, so it must leave the rebase for the next cycle. Failure prevented:
// a transient git failure turning into a commit lost to the reflog.
func TestAuditAndAbort_RefusesWhenBranchTipUnreadable(t *testing.T) {
	f := newLedgerFixture(t)
	raced := racedRebase(t, f, false)
	installFakeGit(t, "branchtipfail")

	err := AuditAndAbort(context.Background(), f.local, AuditOpRebase, "test", quietLogger())

	require.ErrorContains(t, err, "refused")
	assert.True(t, IsRebaseInProgress(f.local), "a refused abort leaves the rebase for the next cycle")
	assert.Equal(t, raced, f.git(f.local, "rev-parse", "main"), "the moved branch must be untouched")
}

// TestAuditAndAbort_RefusesWhenReanchorCannotBeWritten: the same fail-closed
// rule when orig-head cannot be rewritten. Failure prevented: an abort that
// rewinds the moved branch because its re-anchor silently failed.
func TestAuditAndAbort_RefusesWhenReanchorCannotBeWritten(t *testing.T) {
	t.Parallel()
	f := newLedgerFixture(t)
	raced := racedRebase(t, f, false)
	stateDir := filepath.Join(f.local, ".git", "rebase-merge")
	require.NoError(t, os.Chmod(stateDir, 0o555))
	t.Cleanup(func() { _ = os.Chmod(stateDir, 0o755) })
	if probe, err := os.CreateTemp(stateDir, "probe"); err == nil {
		_ = probe.Close()
		t.Skip("state dir still writable (running as root?); cannot fail the re-anchor")
	}

	err := AuditAndAbort(context.Background(), f.local, AuditOpRebase, "test", quietLogger())

	require.ErrorContains(t, err, "refused")
	assert.True(t, IsRebaseInProgress(f.local))
	assert.Equal(t, raced, f.git(f.local, "rev-parse", "main"), "the moved branch must be untouched")
}
