package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/gitutil"
	"github.com/sageox/ox/internal/manifest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- #962 and its follow-up: what a failed conflict probe may and may not do ---
//
// #962: a few minutes of dead DNS made `git ls-files --unmerged` die on its
// context deadline. pullManagedRepo treated every non-nil error from autostash
// recovery as a confirmed merge conflict, so two provably clean clones were
// reported as "has unresolved conflicts: ... context deadline exceeded" and
// pinned behind RequiresConfirm — a gate nothing clears once the network
// returns.
//
// The follow-up regression: the first fix classified EVERY probe failure as
// retryable and returned a zero-value ManagedRepoPullResult. doPull cannot
// distinguish that from a healthy pull, so a DURABLY broken clone (corrupt
// .git/index) silently never synced again while every signal read green.
//
// The invariant both halves share: a cycle that did not sync must never be
// reportable as one that did.

// --- A. Durable failure: loud, but never a confirm-gated merge conflict ---

// An unreadable .git/index fails identically forever. It must reach the user,
// and it must NOT wear the false-alarm shape #962 removed.
func TestPullManagedRepo_UnreadableIndexIsVisibleAndNotAConflict(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git index states")
	}
	repo := newProbeTestRepo(t, "notes.txt")
	corruptIndex(t, repo)

	result := newTestScheduler(t.TempDir()).pullManagedRepo(context.Background(), ManagedRepoPullOpts{
		RepoPath:     repo,
		RepoName:     "team_qdur30tb4b",
		ResolveRules: []manifest.ResolveRule{{Mode: manifest.ResolveModeAuto, Path: "data/"}},
		Logger:       discardLogger(),
	})

	// The half worth keeping from the original test: still not a merge conflict.
	require.NotNil(t, result.Issue, "a durably broken clone must not fail silently")
	assert.NotEqual(t, IssueTypeMergeConflict, result.Issue.Type,
		"the probe found no conflicts — it could not run at all")
	assert.False(t, result.Issue.RequiresConfirm,
		"human gating is for merges a human can adjudicate, not for a corrupt index")

	// The half that was missing: the failure is visible.
	assert.Equal(t, IssueTypeRepoIntegrity, result.Issue.Type)
	assert.Equal(t, SeverityError, result.Issue.Severity)
	assert.Equal(t, "team_qdur30tb4b", result.Issue.Repo)
	assert.Contains(t, result.Issue.Summary, filepath.Join(repo, ".git", "index"),
		"the summary must name the file a human or agent has to repair")
	require.Error(t, result.Err, "doPull keys its failure path off Err, not Issue")
	assert.ErrorIs(t, result.Err, gitutil.ErrConflictProbeFailed)
	assert.False(t, result.Skipped, "Skipped is checked before Err and would swallow this")
	assert.False(t, result.PullRan,
		"the PRE-pull probe aborted this cycle, so no fetch ran and no autostash entry can have accumulated — doTeamSync routes on exactly this")
}

// The regression in its own terms: repeated cycles against a corrupt index must
// never produce the zero-value result doPull reads as a healthy sync. Modeled on
// the three-cycle reproduction — a single cycle would not have caught a fix that
// only reports the first failure.
func TestPullManagedRepo_CorruptIndexNeverLooksLikeSuccess(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git index states")
	}
	repo := newProbeTestRepo(t, "notes.txt")
	corruptIndex(t, repo)
	s := newTestScheduler(t.TempDir())

	for cycle := 1; cycle <= 3; cycle++ {
		result := s.pullManagedRepo(context.Background(), ManagedRepoPullOpts{
			RepoPath:     repo,
			RepoName:     "ledger",
			ResolveRules: []manifest.ResolveRule{{Mode: manifest.ResolveModeAuto, Path: "data/"}},
			Logger:       discardLogger(),
		})

		// This is precisely doPull's success path: not skipped, no error. Reaching
		// it means ClearSyncFailures + RecordPullSuccess + lastSync = now on a
		// cycle that never fetched a byte.
		require.Falsef(t, !result.Skipped && result.Err == nil,
			"cycle %d took doPull's success path: Skipped=%v SkipReason=%q Err=%v Issue=%v",
			cycle, result.Skipped, result.SkipReason, result.Err, result.Issue)
		require.NotNilf(t, result.Issue, "cycle %d must keep the issue visible, not report it once and go quiet", cycle)
		assert.Equal(t, IssueTypeRepoIntegrity, result.Issue.Type, "cycle %d", cycle)
	}
}

// --- B. Genuine conflict: unchanged, and the original #962 fix still holds ---

// A probe that SUCCEEDED and found an unmerged index the resolver cannot repair
// must still reach the user as a confirm-gated merge conflict. Without this, the
// #962 fix would suppress real conflicts too.
func TestPullManagedRepo_GenuineUnmergedIndexStillMintsConflictIssue(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git index states")
	}
	repo := newProbeTestRepo(t, "notes.txt")
	writeUnmergedIndex(t, repo, "notes.txt")

	result := newTestScheduler(t.TempDir()).pullManagedRepo(context.Background(), ManagedRepoPullOpts{
		RepoPath:     repo,
		RepoName:     "ledger",
		ResolveRules: []manifest.ResolveRule{{Mode: manifest.ResolveModeAuto, Path: "data/"}},
		Logger:       discardLogger(),
	})

	require.NotNil(t, result.Issue)
	assert.Equal(t, IssueTypeMergeConflict, result.Issue.Type)
	assert.True(t, result.Issue.RequiresConfirm, "a real conflict is exactly what human gating is for")
	assert.Equal(t, "ledger", result.Issue.Repo)
	require.Error(t, result.Err)
	assert.ErrorContains(t, result.Err, "requires manual resolution")
}

// --- C. The re-probe itself ---
//
// classifyAutostashFailure never reads conflictErr's shape; it re-reads the
// index and branches on what it finds. These cases drive that decision directly,
// including the one pullManagedRepo cannot stage end-to-end: an ambiguous error
// (no sentinel, no context error) over an index that is in fact clean — the
// shape produced when a conflict is resolved between the failing probe and the
// confirmation.
func TestClassifyAutostashFailure(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git index states")
	}

	// Neither gitutil.ErrConflictProbeFailed nor a context error, so any
	// error-shape allowlist would misfile it.
	ambiguous := errors.New("cannot recover autostash while MERGE_HEAD is present or unreadable")
	pullSucceeded := ManagedRepoPullResult{AutoResolved: true}
	pullFailed := ManagedRepoPullResult{
		Err:   errors.New("pull failed"),
		Issue: &DaemonIssue{Type: IssueTypeDiverged, Severity: SeverityError, Repo: "ledger"},
	}

	tests := []struct {
		name string
		// index prepares the fixture repo's index state.
		index   func(t *testing.T, repo string)
		err     error
		pullRan bool
		in      ManagedRepoPullResult
		assert  func(t *testing.T, got ManagedRepoPullResult)
	}{
		{
			name:    "clean index before the pull becomes an explicit skip",
			index:   func(*testing.T, string) {},
			err:     ambiguous,
			pullRan: false,
			assert: func(t *testing.T, got ManagedRepoPullResult) {
				assert.True(t, got.Skipped)
				assert.Equal(t, skipReasonUnconfirmedConflict, got.SkipReason)
				assert.NoError(t, got.Err, "a clean index must not count toward the consecutive-failure backoff")
				assert.Nil(t, got.Issue)
			},
		},
		{
			// The #962 shape verbatim: the sync context died, so the probe
			// returned a context error. The re-probe uses context.WithoutCancel,
			// so it still reads the index and proves the clone clean.
			name:    "canceled probe over a clean index does not mint a conflict",
			index:   func(*testing.T, string) {},
			err:     fmt.Errorf("%w: git ls-files --unmerged: git ls-files: : %w", gitutil.ErrConflictProbeFailed, context.DeadlineExceeded),
			pullRan: false,
			assert: func(t *testing.T, got ManagedRepoPullResult) {
				assert.True(t, got.Skipped)
				assert.Nil(t, got.Issue, "#962: a timeout must never become a RequiresConfirm merge conflict")
				assert.NoError(t, got.Err)
			},
		},
		{
			name:    "clean index after a successful pull keeps the success",
			index:   func(*testing.T, string) {},
			err:     ambiguous,
			pullRan: true,
			in:      pullSucceeded,
			assert: func(t *testing.T, got ManagedRepoPullResult) {
				assert.Equal(t, pullSucceeded, got, "the pull really did sync; nothing may overwrite that")
			},
		},
		{
			name:    "clean index after a failed pull keeps the pull's verdict",
			index:   func(*testing.T, string) {},
			err:     ambiguous,
			pullRan: true,
			in:      pullFailed,
			assert: func(t *testing.T, got ManagedRepoPullResult) {
				assert.False(t, got.Skipped, "a failed pull must not be downgraded to a skip")
				require.NotNil(t, got.Issue)
				assert.Equal(t, IssueTypeDiverged, got.Issue.Type)
				assert.EqualError(t, got.Err, "pull failed")
			},
		},
		{
			name:    "conflicted index mints the confirm-gated merge conflict",
			index:   func(t *testing.T, repo string) { writeUnmergedIndex(t, repo, "notes.txt") },
			err:     ambiguous,
			pullRan: false,
			assert: func(t *testing.T, got ManagedRepoPullResult) {
				require.NotNil(t, got.Issue)
				assert.Equal(t, IssueTypeMergeConflict, got.Issue.Type)
				assert.True(t, got.Issue.RequiresConfirm)
				assert.ErrorIs(t, got.Err, ambiguous)
			},
		},
		{
			name:    "unreadable index is durable and overrides a stale skip",
			index:   corruptIndex,
			err:     ambiguous,
			pullRan: true,
			in:      ManagedRepoPullResult{Skipped: true, SkipReason: skipReasonRebaseInProgress},
			assert: func(t *testing.T, got ManagedRepoPullResult) {
				assert.False(t, got.Skipped, "doPull checks Skipped before Err")
				assert.Empty(t, got.SkipReason)
				require.NotNil(t, got.Issue)
				assert.Equal(t, IssueTypeRepoIntegrity, got.Issue.Type)
				assert.False(t, got.Issue.RequiresConfirm)
				assert.ErrorIs(t, got.Err, gitutil.ErrConflictProbeFailed)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			repo := newProbeTestRepo(t, "notes.txt")
			tc.index(t, repo)

			// A dead parent context: the re-probe must survive it, which is the
			// whole reason it runs under context.WithoutCancel.
			ctx, cancel := context.WithCancel(context.Background())
			cancel()

			got := tc.in
			classifyAutostashFailure(ctx, &got, tc.err, tc.pullRan, repo, "ledger", discardLogger())
			tc.assert(t, got)
		})
	}
}

// HasUnmergedEntries is the single source of fact classifyAutostashFailure
// branches on, so its three outcomes are pinned directly.
func TestReprobeIndexConflicts(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git index states")
	}
	t.Run("clean", func(t *testing.T) {
		t.Parallel()
		conflicted, err := reprobeIndexConflicts(context.Background(), newProbeTestRepo(t, "notes.txt"))
		require.NoError(t, err)
		assert.False(t, conflicted)
	})
	t.Run("conflicted", func(t *testing.T) {
		t.Parallel()
		repo := newProbeTestRepo(t, "notes.txt")
		writeUnmergedIndex(t, repo, "notes.txt")
		conflicted, err := reprobeIndexConflicts(context.Background(), repo)
		require.NoError(t, err)
		assert.True(t, conflicted)
	})
	t.Run("unreadable is an error, never a clean verdict", func(t *testing.T) {
		t.Parallel()
		repo := newProbeTestRepo(t, "notes.txt")
		corruptIndex(t, repo)
		conflicted, err := reprobeIndexConflicts(context.Background(), repo)
		require.Error(t, err)
		assert.ErrorIs(t, err, gitutil.ErrConflictProbeFailed)
		assert.False(t, conflicted, "the bool is meaningless when err != nil; callers must check err first")
	})
	t.Run("survives a canceled parent context", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		conflicted, err := reprobeIndexConflicts(ctx, newProbeTestRepo(t, "notes.txt"))
		require.NoError(t, err, "context.WithoutCancel is what makes the #962 confirmation possible")
		assert.False(t, conflicted)
	})
}

// --- fixtures ---

// newProbeTestRepo returns a one-commit repo with no remote. These tests never
// reach fetch — autostash recovery runs first and returns before it — so the
// fixture deliberately skips the bare-remote scaffolding other tests need.
func newProbeTestRepo(t *testing.T, path string) string {
	t.Helper()
	repo := t.TempDir()
	out, err := runGitOut(t, repo, "init", "-b", "main", repo)
	require.NoError(t, err, out)
	require.NoError(t, os.WriteFile(filepath.Join(repo, path), []byte("base\n"), 0o644))
	out, err = runGitOut(t, repo, "add", path)
	require.NoError(t, err, out)
	out, err = runGitOut(t, repo, "commit", "-m", "base")
	require.NoError(t, err, out)
	return repo
}

// corruptIndex makes `git ls-files --unmerged` fail the same way forever, which
// is what separates a durable failure from the retryable #962 timeout. Garbage
// bytes are enough: git rejects the index on its signature/version header.
func corruptIndex(t *testing.T, repo string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".git", "index"), []byte("invalid index"), 0o644))
}

// writeUnmergedIndex stages all three merge stages for path directly.
// update-index --index-info is the only way to produce the unmerged index
// `git ls-files --unmerged` reports without a remote, a rebase, or a network.
func writeUnmergedIndex(t *testing.T, repo, path string) {
	t.Helper()
	hash := func(content string) string {
		cmd := exec.Command("git", "hash-object", "-w", "--stdin") // safe: git in a temp dir
		cmd.Dir = repo
		cmd.Env = gitEnv()
		cmd.Stdin = strings.NewReader(content)
		out, err := cmd.Output()
		require.NoError(t, err)
		return strings.TrimSpace(string(out))
	}
	stages := fmt.Sprintf("0 %s\t%s\n100644 %s 1\t%s\n100644 %s 2\t%s\n100644 %s 3\t%s\n",
		strings.Repeat("0", 40), path,
		hash("base\n"), path, hash("ours\n"), path, hash("theirs\n"), path)

	cmd := exec.Command("git", "update-index", "--index-info") // safe: git in a temp dir
	cmd.Dir = repo
	cmd.Env = gitEnv()
	cmd.Stdin = strings.NewReader(stages)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
}

// --- D. Bounded confirmation: one failed read is not a durable fault ---

// The re-probe's own failure mode. A read that fails for a reason that is NOT
// the index — a fork hitting EAGAIN, a stalled filesystem, a git killed mid-run
// — is byte-for-byte indistinguishable from a corrupt .git/index, so a
// single-sample verdict turns a blip into IssueTypeRepoIntegrity. Downstream
// that is worse than noise: for a team context it used to end in permanent sync
// suspension (the #906 shape, reached by a different path).
//
// Not parallel: gitShimFailingUnmergedProbe edits the process-wide PATH.
func TestReprobeIndexConflicts_BoundedConfirmation(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git index states")
	}

	t.Run("a read that fails once and then succeeds is not durable", func(t *testing.T) {
		repo := newProbeTestRepo(t, "notes.txt")
		gitShimFailingUnmergedProbe(t, 1)

		conflicted, err := reprobeIndexConflicts(context.Background(), repo)
		require.NoError(t, err, "one failed read proves nothing; the retry read the index fine")
		assert.False(t, conflicted)
	})

	t.Run("a read that fails every attempt is still durable", func(t *testing.T) {
		repo := newProbeTestRepo(t, "notes.txt")
		// One more than the bound, so the last attempt fails too.
		gitShimFailingUnmergedProbe(t, reprobeConfirmAttempts+1)

		_, err := reprobeIndexConflicts(context.Background(), repo)
		require.Error(t, err, "bounded retry must not become infinite patience")
		assert.ErrorIs(t, err, gitutil.ErrConflictProbeFailed)
	})
}

// gitShimFailingUnmergedProbe puts a `git` wrapper first on PATH that fails the
// first failures invocations carrying `--unmerged`, and passes everything else —
// including every later probe — through to the real binary.
//
// It stages the one failure class no real-git fixture can produce: a probe that
// fails for a reason OTHER than the index, leaving every other git command
// working normally. Corrupting .git/index cannot stand in for it, because that
// also breaks `git status`, and `git status` is what doTeamSync fingerprints
// with — so a corrupt index never even reaches the suspension branch.
//
// Callers must NOT t.Parallel(): the PATH edit is process-wide.
func gitShimFailingUnmergedProbe(t *testing.T, failures int) {
	t.Helper()
	realGit, err := exec.LookPath("git")
	require.NoError(t, err)

	dir := t.TempDir()
	counter := filepath.Join(dir, "probe-count")
	// The error text deliberately matches no isTransientSyncError substring:
	// "unclassifiable", not "known transient", is the case under test.
	script := fmt.Sprintf(`#!/bin/sh
for arg in "$@"; do
  [ "$arg" = "--unmerged" ] || continue
  n=$(cat %[1]q 2>/dev/null || echo 0)
  n=$((n + 1))
  printf '%%s' "$n" > %[1]q
  if [ "$n" -le %[2]d ]; then
    echo "fatal: unable to read the index: Resource temporarily unavailable" >&2
    exit 128
  fi
  break
done
exec %[3]q "$@"
`, counter, failures, realGit)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// --- E. Clearing the integrity issue once the clone is readable again ---

// IssueTypeRepoIntegrity is cleared on a successful pull, but a clone whose
// remote has stopped changing never gets one: every cycle dedups to a skip. The
// skip reasons that are only reachable AFTER a successful index read are proof
// enough, and without honoring them a repaired repo prompts forever for a
// repair it no longer needs.
//
// Not parallel: pullTeamContext mutates a shared scheduler.
func TestPullTeamContext_ReadableSkipClearsStaleIntegrityIssue(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git index states")
	}
	repo := newProbeTestRepo(t, "notes.txt")
	// A FETCH_HEAD younger than the dedup window is what makes this cycle skip
	// with "recently fetched" — decided only after ResolveAutostashConflicts
	// read the index and returned cleanly.
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".git", "FETCH_HEAD"),
		[]byte("0000000000000000000000000000000000000000\t\tbranch 'main' of origin\n"), 0o644))

	s := newTestScheduler(t.TempDir())
	s.issues = NewIssueTracker()
	repoName := filepath.Base(repo)
	s.issues.SetIssue(DaemonIssue{
		Type:     IssueTypeRepoIntegrity,
		Severity: SeverityError,
		Repo:     repoName,
		Summary:  "left over from a cycle that could not read the index",
	})

	_, err := s.pullTeamContext(context.Background(), repo)
	require.NoError(t, err)

	_, still := s.issues.GetIssue(IssueTypeRepoIntegrity, repoName)
	assert.False(t, still, "the skip proved the index is readable; the issue is stale")
}

// The predicate's whole job is deciding which skips carry that proof. The
// negative rows matter most: the lock and rebase skips return BEFORE any index
// read, so clearing on them would retire an issue nothing re-examined.
func TestSkipProvesIndexReadable(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		reason string
		want   bool
	}{
		{skipReasonRemoteUnchanged, true},
		{skipReasonRecentlyFetched, true},
		{skipReasonUnconfirmedConflict, true},
		{skipReasonUnconfirmedIndex, false},
		{skipReasonRebaseInProgress, false},
		{skipReasonLockFilesPresent, false},
		{skipReasonRepoLockBusy, false},
		{"", false},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, skipProvesIndexReadable(tc.reason))
		})
	}
}

// --- F. The confirmation's own budget (PR #974 review) ---
//
// The confirmation ignores cancellation on purpose, but ignoring it for the
// WHOLE ladder made one wedged clone hold the sync cycle for the full retry
// sequence. Team sync waits on every bubble's confirmation serially and daemon
// shutdown waits on the scheduler goroutine behind them, so that time is
// shutdown latency multiplied by the number of wedged clones.
//
// The two tests below pin the two halves that must hold simultaneously: a
// shutdown cuts a SLOW ladder short without concluding anything, and a shutdown
// does NOT cut short the FAST ladder that actually diagnoses a corrupt index.

// Not parallel: gitShimSlowUnmergedProbe edits the process-wide PATH.
func TestReprobeIndexConflicts_ShutdownStopsASlowLadder(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git index states")
	}
	repo := newProbeTestRepo(t, "notes.txt")
	counter := gitShimSlowUnmergedProbe(t, 2)

	// Already canceled: the daemon is going away, exactly the state in which
	// the old ladder would still have spent its full retry sequence.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	_, err := reprobeIndexConflicts(ctx, repo)
	elapsed := time.Since(start)

	require.Error(t, err)
	assert.ErrorIs(t, err, errProbeAbandoned,
		"a ladder cut short proved nothing and must not be reported as a durable fault")
	assert.ErrorIs(t, err, gitutil.ErrConflictProbeFailed,
		"the last read's error stays in the chain so a log line still shows what failed")

	attempts := probeCount(t, counter)
	assert.Less(t, attempts, reprobeConfirmAttempts,
		"shutdown must stop the ladder early; it ran all %d attempts", reprobeConfirmAttempts)
	assert.Less(t, elapsed, reprobeConfirmAttempts*reprobeAttemptTimeout,
		"the point of the drain is that the full per-attempt ladder is never paid during shutdown")
}

// The mirror image, and the invariant a drain must not break: a corrupt index
// fails every read in microseconds, so the ladder finishes inside the drain and
// the clone is still reported as durably broken on this very cycle — with
// IssueTypeRepoIntegrity, not a confirm-gated merge conflict.
func TestReprobeIndexConflicts_ShutdownStillConfirmsAFastLadder(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git index states")
	}
	t.Parallel()
	repo := newProbeTestRepo(t, "notes.txt")
	corruptIndex(t, repo)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := reprobeIndexConflicts(ctx, repo)

	require.Error(t, err)
	assert.NotErrorIs(t, err, errProbeAbandoned,
		"three completed reads all failed — that IS the verdict, shutdown or not")
	assert.ErrorIs(t, err, gitutil.ErrConflictProbeFailed)
}

// classifyAutostashFailure must read the abandoned verdict as "nothing proved",
// not as the durable failure a completed ladder reports. Getting this wrong
// would mint IssueTypeRepoIntegrity on every laptop-lid-close — #962's mistake
// re-entered through the shutdown door.
//
// Not parallel: gitShimSlowUnmergedProbe edits the process-wide PATH.
func TestClassifyAutostashFailure_AbandonedConfirmationProvesNothing(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git index states")
	}
	ambiguous := errors.New("cannot recover autostash while MERGE_HEAD is present or unreadable")

	t.Run("before the pull it is a skip that does not claim a readable index", func(t *testing.T) {
		repo := newProbeTestRepo(t, "notes.txt")
		gitShimSlowUnmergedProbe(t, 2)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		var got ManagedRepoPullResult
		classifyAutostashFailure(ctx, &got, ambiguous, false, repo, "ledger", discardLogger())

		assert.True(t, got.Skipped, "a cycle that did nothing must never look like a sync")
		assert.Equal(t, skipReasonUnconfirmedIndex, got.SkipReason)
		assert.False(t, skipProvesIndexReadable(got.SkipReason),
			"the read never finished, so it cannot retire a standing integrity issue")
		assert.Nil(t, got.Issue, "an abandoned confirmation is not evidence of a broken clone")
		assert.NoError(t, got.Err)
	})

	// The round-4 regression, end to end through the real probe ladder. A
	// successful `git pull --rebase --autostash` exits zero even when the
	// autostash re-apply left unmerged entries behind — that is the entire
	// reason this cycle re-reads the index — so with no completed read a broken
	// worktree and a clean one are the same bytes. Before the fix this branch
	// handed doPull a result with Skipped=false and Err=nil, which clears sync
	// failures, retires IssueTypeRepoIntegrity, records a pull success and
	// stamps lastSync for a clone that may never sync again.
	t.Run("after a successful pull the cycle is still not reported as a sync", func(t *testing.T) {
		repo := newProbeTestRepo(t, "notes.txt")
		gitShimSlowUnmergedProbe(t, 2)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		got := ManagedRepoPullResult{AutoResolved: true, FetchHeadTime: time.Now()}
		classifyAutostashFailure(ctx, &got, ambiguous, true, repo, "ledger", discardLogger())

		require.False(t, resultReadsAsSuccess(&got),
			"took doPull's success path: Skipped=%v SkipReason=%q Err=%v", got.Skipped, got.SkipReason, got.Err)
		assert.Equal(t, skipReasonUnconfirmedIndex, got.SkipReason)
		assert.False(t, skipProvesIndexReadable(got.SkipReason),
			"a read that never finished proves nothing in EITHER direction, so it must not retire a standing integrity issue")
		assert.NoError(t, got.Err,
			"nothing is known to be wrong: an error would feed the backoff and, with PullRan set, the permanent fingerprint suspension")
		assert.Nil(t, got.Issue, "an abandoned confirmation is not evidence of a broken clone")
		assert.True(t, got.AutoResolved, "what the pull observed stays true; only the sync verdict is withheld")
	})

	t.Run("after a failed pull the pull's own verdict stands", func(t *testing.T) {
		repo := newProbeTestRepo(t, "notes.txt")
		gitShimSlowUnmergedProbe(t, 2)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		got := ManagedRepoPullResult{
			Err:   errors.New("pull failed"),
			Issue: &DaemonIssue{Type: IssueTypeDiverged, Severity: SeverityError, Repo: "ledger"},
		}
		classifyAutostashFailure(ctx, &got, ambiguous, true, repo, "ledger", discardLogger())

		assert.False(t, got.Skipped, "a failed pull must not be downgraded to a skip")
		require.NotNil(t, got.Issue)
		assert.Equal(t, IssueTypeDiverged, got.Issue.Type,
			"an abandoned confirmation must not overwrite the pull's classification with RepoIntegrity")
		assert.EqualError(t, got.Err, "pull failed")
	})
}

// gitShimSlowUnmergedProbe makes every `--unmerged` read take seconds and then
// fail, and passes everything else through. It stages the only shape that can
// hold a shutdown: a read that is slow AND uninformative. Redirecting the sleep's
// own descriptors matters — a grandchild holding the command's output pipes open
// would make the read outlast its context deadline and blur what is being timed.
//
// Returns the path of its probe counter. Callers must NOT t.Parallel().
func gitShimSlowUnmergedProbe(t *testing.T, sleepSeconds int) string {
	t.Helper()
	realGit, err := exec.LookPath("git")
	require.NoError(t, err)

	dir := t.TempDir()
	counter := filepath.Join(dir, "probe-count")
	script := fmt.Sprintf(`#!/bin/sh
for arg in "$@"; do
  [ "$arg" = "--unmerged" ] || continue
  n=$(cat %[1]q 2>/dev/null || echo 0)
  n=$((n + 1))
  printf '%%s' "$n" > %[1]q
  sleep %[2]d </dev/null >/dev/null 2>&1
  echo "fatal: unable to read the index: Resource temporarily unavailable" >&2
  exit 128
done
exec %[3]q "$@"
`, counter, sleepSeconds, realGit)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return counter
}

// probeCount reads how many `--unmerged` reads a shim actually served.
func probeCount(t *testing.T, counter string) int {
	t.Helper()
	raw, err := os.ReadFile(counter)
	require.NoError(t, err, "the shim must have served at least one probe")
	n, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	require.NoError(t, err)
	return n
}

// --- G. The invariant, enumerated (PR #974 review, round 4) ---
//
// Three rounds of this PR each shipped one variant of a single class of bug:
// something UNPROVEN reported as success. Round 1 dropped every probe failure
// and returned a zero-value result. Round 3 routed a joined post-pull probe
// error through backoff, bypassing suspension. Round 4 preserved a successful
// pull verdict when the confirmation was abandoned. Every one was a branch that
// forgot to fail the cycle.
//
// So the fix is not a fourth special case but a single guard —
// withheldUnprovenSuccess — and this is its enforcement: the whole
// (probe outcome × pullRan × incoming result) cross-product, asserting that
// exactly one combination may leave a result doPull reads as a completed sync.
// A future branch that forgets fails here instead of shipping.

// classifyIndexProof is the narrow point where a (bool, error) probe return
// becomes the one fact everything downstream branches on, so its whole mapping
// is pinned — including the ordering that makes an abandoned ladder outrank the
// last read's error it still carries in its chain.
func TestClassifyIndexProof(t *testing.T) {
	t.Parallel()
	durable := fmt.Errorf("read index: %w", gitutil.ErrConflictProbeFailed)
	abandoned := abandonedProbe(durable, true)

	for _, tc := range []struct {
		name        string
		conflicted  bool
		probeErr    error
		want        indexProof
		wantVerdict string
		wantProves  bool
	}{
		{"completed read, no unmerged entries", false, nil, proofClean, "clean", true},
		{"completed read, unmerged entries", true, nil, proofConflicted, "conflicted", true},
		{"every read completed and failed", false, durable, proofUnreadable, "unreadable", true},
		{
			// The bool is meaningless whenever err != nil; the error wins.
			name: "a failed read's bool is ignored", conflicted: true, probeErr: durable,
			want: proofUnreadable, wantVerdict: "unreadable", wantProves: true,
		},
		{
			// errProbeAbandoned wraps the last read's failure, so an
			// error-first test would file a laptop-lid-close as a corrupt clone.
			name: "an abandoned ladder outranks the error it carries", probeErr: abandoned,
			want: proofUnknown, wantVerdict: "unconfirmed", wantProves: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := classifyIndexProof(tc.conflicted, tc.probeErr)
			assert.Equal(t, tc.want, got)
			assert.Equal(t, tc.wantVerdict, got.String(), "index_verdict is a logged contract")
			assert.Equal(t, tc.wantProves, got.provesIndexState())
		})
	}
}

// The cross-product. Pure by construction — applyIndexProof takes the proof
// rather than producing it — so all of it runs without staging a real git index
// state, which is what makes exhaustive enumeration affordable at all.
func TestApplyIndexProof_NoUnprovenResultLooksLikeASync(t *testing.T) {
	t.Parallel()

	conflictErr := errors.New("cannot recover autostash while MERGE_HEAD is present or unreadable")
	durable := fmt.Errorf("read index: %w", gitutil.ErrConflictProbeFailed)
	abandoned := abandonedProbe(durable, true)

	// Every distinguishable return reprobeIndexConflicts can produce.
	probes := []struct {
		name       string
		conflicted bool
		probeErr   error
	}{
		{"clean", false, nil},
		{"conflicted", true, nil},
		{"unreadable", false, durable},
		{"unreadable with a stale bool", true, durable},
		{"abandoned", false, abandoned},
		{"abandoned with a stale bool", true, abandoned},
	}

	// Every shape pullManagedRepo can be holding when classification runs.
	// "remote unchanged" is deliberately absent: it is decided before the pull
	// with conflictErr still nil, so it can never reach here — and unlike the
	// rebase skip it WOULD claim a readable index.
	incoming := []struct {
		name string
		in   ManagedRepoPullResult
	}{
		{"nothing ran", ManagedRepoPullResult{}},
		{"pull succeeded", ManagedRepoPullResult{AutoResolved: true, Diverged: true}},
		{"pull failed", ManagedRepoPullResult{
			Err:   errors.New("pull failed"),
			Issue: &DaemonIssue{Type: IssueTypeDiverged, Severity: SeverityError, Repo: "ledger"},
		}},
		{"pull skipped mid-rebase", ManagedRepoPullResult{Skipped: true, SkipReason: skipReasonRebaseInProgress}},
	}

	for _, p := range probes {
		for _, pullRan := range []bool{false, true} {
			for _, inc := range incoming {
				t.Run(fmt.Sprintf("%s/pullRan=%v/%s", p.name, pullRan, inc.name), func(t *testing.T) {
					t.Parallel()
					proof := classifyIndexProof(p.conflicted, p.probeErr)
					in := inc.in
					got := in
					applyIndexProof(&got, proof, conflictErr, p.probeErr, pullRan, t.TempDir(), "ledger", discardLogger())

					// THE INVARIANT. A result reads as a completed sync only
					// when a read that RAN TO COMPLETION proved the index clean,
					// a pull actually ran, and that pull itself succeeded.
					// Every other cell of this table must fail the cycle.
					wantSuccess := proof == proofClean && pullRan && resultReadsAsSuccess(&in)
					require.Equal(t, wantSuccess, resultReadsAsSuccess(&got),
						"Skipped=%v SkipReason=%q Err=%v", got.Skipped, got.SkipReason, got.Err)

					// Retiring a standing IssueTypeRepoIntegrity requires a read
					// that finished. Abandoning one proves nothing in either
					// direction, so it must not clear the issue any more than it
					// may raise one.
					if got.Skipped && skipProvesIndexReadable(got.SkipReason) {
						assert.Equal(t, proofClean, proof,
							"skip reason %q claims the index was read", got.SkipReason)
					}

					// Human gating is only ever for a conflict a human can
					// adjudicate — #962's whole complaint was a timeout wearing
					// this shape, unclearable by anything the user can do.
					if got.Issue != nil && got.Issue.RequiresConfirm {
						assert.Equal(t, proofConflicted, proof)
					}

					// A proof that reports something WRONG must never leave a
					// skip set. doPull and pullTeamContext both test Skipped
					// before Err, so a skip surviving alongside an error sends
					// the cycle down the skip path and swallows the error and
					// its issue whole. proofUnreadable always cleared it;
					// proofConflicted did not, and an incoming
					// skipReasonRebaseInProgress could therefore hide a real
					// merge conflict. Asserted for BOTH so the two branches
					// cannot drift apart again.
					if proof == proofConflicted || proof == proofUnreadable {
						assert.False(t, got.Skipped,
							"%v sets Err/Issue, so a surviving skip (%q) would swallow them",
							proof, got.SkipReason)
						assert.Empty(t, got.SkipReason)
					}

					if proof != proofUnknown {
						return
					}
					// An unproven cycle stays ordinarily retryable: it may add
					// no error of its own (which would feed the backoff and,
					// with PullRan set, teamFailureTakesBackoff's permanent
					// worktree-fingerprint suspension) and may raise no issue.
					assert.NotErrorIs(t, got.Err, errProbeAbandoned,
						"the abandonment must not reach a consumer as a failure")
					assert.NotErrorIs(t, got.Err, gitutil.ErrConflictProbeFailed)
					// Only "no NEW issue": the pre-pull arm replaces the result
					// wholesale, so the two contradictory rows here (pullRan=false
					// carrying a pull verdict, which pullManagedRepo cannot
					// actually produce) legitimately drop it.
					if got.Issue != nil {
						assert.Same(t, in.Issue, got.Issue, "nothing was proved, so nothing new may be reported")
					}
				})
			}
		}
	}
}
