package daemon

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/gitutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// COE 2026-09-02 / ADR-030 / bd ox-baz5.1 — the FETCH_HEAD race.
//
// git rewrites .git/FETCH_HEAD on every fetch with no lock of its own. When
// two fetches on one clone overlap, their appends interleave and `main`
// appears twice as a merge-eligible head; the daemon's next
// `git pull --rebase` then refuses with "Cannot rebase onto multiple
// branches." Before ADR-030 D1 four ox code paths fetched the same clone
// with nothing serializing them (daemon pull cycle, daemon wedge probe, CLI
// status, doctor --fix). Reproduced on the real monorepo ledger: 5/5 for the
// corrupted FETCH_HEAD, 2/6 for the failed pull.
//
// The failure this prevents: `ox status` run near a daemon sync cycle makes
// ledger sync fail with an error that reads like corruption, and (bugs .2/.3)
// leaves a red confirm-required issue on `ox status` until daemon restart.

// setupRacingLedger returns a bare remote, a local clone (the ledger under
// test) and a writer clone that advances the remote so every pull has work.
func setupRacingLedger(t *testing.T) (bare, local, writer string) {
	t.Helper()
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	bare = filepath.Join(t.TempDir(), "bare.git")
	out, err := runGitOut(t, t.TempDir(), "init", "--bare", "--initial-branch=main", bare)
	require.NoError(t, err, out)

	writer = filepath.Join(t.TempDir(), "writer")
	out, err = runGitOut(t, t.TempDir(), "clone", "--quiet", bare, writer)
	require.NoError(t, err, out)
	require.NoError(t, os.WriteFile(filepath.Join(writer, "seed.txt"), []byte("seed\n"), 0o644))
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-q", "-m", "seed"}, {"push", "-q", "-u", "origin", "main"}} {
		out, err = runGitOut(t, writer, args...)
		require.NoError(t, err, out)
	}
	// a couple of extra remote branches so FETCH_HEAD carries not-for-merge
	// lines too — the exact shape observed in production.
	for _, b := range []string{"ryan/regen-summaries", "ryan/summary-push"} {
		out, err = runGitOut(t, writer, "push", "-q", "origin", "main:refs/heads/"+b)
		require.NoError(t, err, out)
	}

	local = filepath.Join(t.TempDir(), "ledger")
	out, err = runGitOut(t, t.TempDir(), "clone", "--quiet", bare, local)
	require.NoError(t, err, out)
	return bare, local, writer
}

func advanceRemote(t *testing.T, writer string, n int) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(writer, fmt.Sprintf("c%03d.txt", n)), []byte("x\n"), 0o644))
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-q", "-m", fmt.Sprintf("remote %d", n)}, {"push", "-q", "origin", "main"}} {
		out, err := runGitOut(t, writer, args...)
		require.NoError(t, err, out)
	}
}

// TestPullManagedRepo_ConcurrentFetchDoesNotCorruptFETCH_HEAD drives the
// production pull pipeline while a cooperating peer (the shape of `ox status`
// or the wedge probe after ADR-030) fetches the same clone as fast as it can.
// Red on main: pullManagedRepo does not take the repo lock, so its internal
// fetch races the peer's and some iteration fails with "multiple branches".
// Green once every fetch/pull site goes through gitutil.WithRepoLock.
func TestPullManagedRepo_ConcurrentFetchDoesNotCorruptFETCH_HEAD(t *testing.T) {
	_, local, writer := setupRacingLedger(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// peer fetcher: the post-ADR-030 shape of every other ox fetch site.
	peerDone := make(chan struct{})
	go func() {
		defer close(peerDone)
		for ctx.Err() == nil {
			_ = gitutil.WithRepoLock(ctx, local, func() error {
				cmd := exec.CommandContext(ctx, "git", "-C", local, "fetch", "--quiet") // safe: git in a temp dir
				cmd.Env = gitEnv()
				_, _ = cmd.CombinedOutput()
				return nil
			})
		}
	}()

	s := newTestScheduler(t.TempDir())
	const iterations = 15
	var failures []string
	lastSkipped := false
	for i := 0; i < iterations; i++ {
		advanceRemote(t, writer, i)
		pullCtx := ctx
		if i == iterations-1 {
			// flock grants no fairness guarantee — the peer could in
			// principle win the whole RepoLockTimeout window on this last
			// call, which would report Skipped (not a failure) while
			// leaving local genuinely behind and failing the catch-up
			// assertions below for a reason this test doesn't intend to
			// detect. Stop racing before the call the assertions depend on.
			cancel()
			<-peerDone
			pullCtx = context.Background()
		}
		res := s.pullManagedRepo(pullCtx, ManagedRepoPullOpts{
			RepoPath:     local,
			RepoName:     "ledger",
			SyncInterval: 0,
			MinFetchAge:  time.Nanosecond, // the peer keeps FETCH_HEAD fresh; do not let dedup skip the pull
			Logger:       s.logger,
		})
		if i == iterations-1 {
			lastSkipped = res.Skipped
		}
		if res.Err != nil {
			failures = append(failures, res.Err.Error())
		}
		if res.Issue != nil && res.Issue.Type == IssueTypeRebaseStuck {
			failures = append(failures, "issue: "+res.Issue.Summary)
		}
	}

	assert.False(t, lastSkipped, "the final iteration must actually pull (peer is stopped by then), not skip — the catch-up assertions below only mean something if it ran")

	for _, f := range failures {
		assert.NotContains(t, f, "multiple branches", "FETCH_HEAD race reached git pull")
		assert.NotContains(t, f, "stuck in a broken rebase", "transient race latched as a stuck rebase")
	}
	assert.Empty(t, failures, "pull failures under concurrent fetch: %s", strings.Join(failures, " | "))

	// the clone must actually have kept up — this is not a test that passes by skipping.
	out, err := runGitOut(t, local, "rev-list", "--count", "origin/main..main")
	require.NoError(t, err, out)
	assert.Equal(t, "0", strings.TrimSpace(out), "local should have no unpushed commits")
	out, err = runGitOut(t, local, "rev-list", "--count", "main..origin/main")
	require.NoError(t, err, out)
	assert.Equal(t, "0", strings.TrimSpace(out), "local should be caught up with the remote after the last pull")
}

// TestDoPull_ClearsRebaseStuckOnSuccess — bd ox-baz5.3. IssueTypeRebaseStuck
// was raised on a failed cycle and never cleared: the ledger recovered on the
// next cycle while `ox status` kept telling the user to run `git rebase
// --abort` until the daemon restarted. A successful pull must clear it, the
// same way it already clears MergeConflict / SyncBackoff / Diverged.
func TestDoPull_ClearsRebaseStuckOnSuccess(t *testing.T) {
	// Fixture must leave the local clone genuinely BEHIND origin: doPull's
	// success path (where the clearing lives) is only reached past
	// pullManagedRepo's remoteRefCheck dedup, which short-circuits to a
	// no-op "Skipped" result — and skips clearing — whenever local HEAD
	// already matches the remote (setupGitRepo's fixture, used as-is,
	// always does).
	_, ledgerDir, writer := setupRacingLedger(t)
	advanceRemote(t, writer, 0)

	cfg := DefaultConfig()
	cfg.LedgerPath = ledgerDir
	cfg.SyncIntervalRead = time.Second
	scheduler := NewSyncScheduler(cfg, newTestScheduler(t.TempDir()).logger)
	tracker := NewIssueTracker()
	scheduler.SetIssueTracker(tracker)

	// the latched state from a previous, failed cycle
	tracker.SetIssue(DaemonIssue{Type: IssueTypeRebaseStuck, Severity: SeverityError, Repo: "ledger", Summary: "ledger is stuck in a broken rebase state", RequiresConfirm: true})
	tracker.SetIssue(DaemonIssue{Type: IssueTypeSyncBackoff, Severity: SeverityWarning, Repo: "ledger", Summary: "Sync suspended after 1 consecutive failures"})

	require.NoError(t, scheduler.doPull(context.Background(), nil, false, true))

	_, stuck := tracker.GetIssue(IssueTypeRebaseStuck, "ledger")
	assert.False(t, stuck, "a successful pull must clear the stale rebase-stuck error")
	_, backoff := tracker.GetIssue(IssueTypeSyncBackoff, "ledger")
	assert.False(t, backoff, "a successful pull must clear the stale sync-suspended warning")
}
