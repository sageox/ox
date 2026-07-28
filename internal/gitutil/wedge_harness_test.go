package gitutil

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A ledger wedge is any persistent state that stops a ledger from ever syncing
// again without a human running git by hand. Every wedge that reached production
// gets a case here, driven against a REAL bare remote and REAL clones — never a
// mock — because every one of these bugs survived unit tests that mocked the
// thing that actually broke.
//
// The contract each case asserts is the same three-part one, and all three parts
// are required. A test that only checks the first part is how these shipped:
//
//	1. the wedge is DETECTED
//	2. the repair CLEARS it
//	3. the ledger MAKES PROGRESS afterward — the next pull+push actually lands
//
// Part 3 is the one that matters. `git rebase --abort` "succeeding" while the
// ledger stays permanently un-syncable is the exact shape of the 13-day,
// 341-ahead/1055-behind production wedge.

// ledgerFixture is a real bare remote plus a local clone and a second clone
// standing in for the cloud summarizer. Reuse this for any new wedge case
// rather than hand-rolling another repo setup.
type ledgerFixture struct {
	t     *testing.T
	bare  string
	local string // the machine under test
	cloud string // the other writer (cloud summarizer / a teammate)
}

func newLedgerFixture(t *testing.T) *ledgerFixture {
	t.Helper()
	root := t.TempDir()
	f := &ledgerFixture{
		t:     t,
		bare:  filepath.Join(root, "bare.git"),
		local: filepath.Join(root, "local"),
		cloud: filepath.Join(root, "cloud"),
	}
	gitInRepo(t, root, "init", "--bare", "--initial-branch=main", f.bare)
	gitInRepo(t, root, "clone", f.bare, f.cloud)
	f.write(f.cloud, "sessions/s1/meta.json", `{"session_name":"s1","summary_status":""}`)
	f.git(f.cloud, "add", "-A")
	f.git(f.cloud, "commit", "-m", "seed")
	f.git(f.cloud, "push", "origin", "main")
	gitInRepo(t, root, "clone", f.bare, f.local)
	return f
}

func (f *ledgerFixture) git(dir string, args ...string) string {
	f.t.Helper()
	return gitInRepo(f.t, dir, args...)
}

// gitAllowFail runs git and returns output plus whether it succeeded. Needed for
// the steps that are SUPPOSED to fail while the repo is wedged.
func (f *ledgerFixture) gitAllowFail(dir string, args ...string) (string, bool) {
	f.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), // safe: git subprocess in a temp fixture repo, not the ox CLI
		"GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test.com",
		"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=commit.gpgsign", "GIT_CONFIG_VALUE_0=false",
	)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err == nil
}

func (f *ledgerFixture) write(dir, rel, content string) {
	f.t.Helper()
	p := filepath.Join(dir, rel)
	require.NoError(f.t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(f.t, os.WriteFile(p, []byte(content), 0o644))
}

func (f *ledgerFixture) commitAll(dir, msg string) {
	f.t.Helper()
	f.git(dir, "add", "-A")
	f.git(dir, "commit", "-m", msg)
}

// cloudWrites simulates the summarizer landing work on the remote.
func (f *ledgerFixture) cloudWrites(rel, content, msg string) {
	f.t.Helper()
	f.git(f.cloud, "pull", "--rebase")
	f.write(f.cloud, rel, content)
	f.commitAll(f.cloud, msg)
	f.git(f.cloud, "push", "origin", "main")
}

// diverge puts BOTH sides ahead on the same path — the precondition for every
// content-conflict wedge.
func (f *ledgerFixture) diverge(rel, localContent, cloudContent string) {
	f.t.Helper()
	f.cloudWrites(rel, cloudContent, "cloud update")
	f.write(f.local, rel, localContent)
	f.commitAll(f.local, "local update")
	f.git(f.local, "fetch", "origin")
}

// reconcile runs the production repair path: pull --rebase, then the resolver,
// looping until the rebase completes. Returns the number of resolve rounds.
func (f *ledgerFixture) reconcile(prefixes []string) (int, error) {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	f.gitAllowFail(f.local, "pull", "--rebase", "--autostash")
	rounds := 0
	for IsRebaseInProgress(f.local) {
		if err := ResolveRebaseAcceptTheirs(ctx, f.local, prefixes); err != nil {
			return rounds, err
		}
		rounds++
		if rounds > 100 {
			return rounds, fmt.Errorf("no convergence after %d rounds", rounds)
		}
	}
	return rounds, nil
}

// assertMakesProgress is the part-3 check. A repair that leaves the ledger
// un-pushable has not fixed anything, however clean its own exit code was.
func (f *ledgerFixture) assertMakesProgress() {
	f.t.Helper()
	f.write(f.local, "sessions/s1/probe.txt", "post-repair write")
	f.commitAll(f.local, "post-repair commit")

	// A repaired ledger must be able to complete a normal sync cycle. Pulling
	// first is part of that cycle — the point is that nothing WEDGES, not that
	// the local happened to already be up to date.
	if out, ok := f.gitAllowFail(f.local, "pull", "--rebase", "--autostash"); !ok {
		f.t.Fatalf("ledger still cannot pull after repair — wedge NOT cleared:\n%s", out)
	}
	if out, ok := f.gitAllowFail(f.local, "push", "origin", "main"); !ok {
		f.t.Fatalf("ledger still cannot push after repair — wedge NOT cleared:\n%s", out)
	}
	behind := f.git(f.local, "rev-list", "--count", "HEAD..origin/main")
	assert.Equal(f.t, "0", behind, "local must be fully reconciled with the remote")
}

func (f *ledgerFixture) fileContent(rel string) string {
	f.t.Helper()
	data, err := os.ReadFile(filepath.Join(f.local, rel))
	require.NoError(f.t, err)
	return string(data)
}

// --- A. The headline wedge: sessions/ conflicts ---

// TestWedge_SessionsMetaConflict_SelfHeals is the regression test for the
// production wedge: sessions/<id>/meta.json is written by both the cloud
// summarizer and the local CLI, and with no resolve rule the rebase halted,
// aborted, restored the pre-rebase state, and failed identically forever.
//
// Failure prevented: 281 conflicts, 341 unpushed commits, 13 days, zero
// escalation — and the repair loop "succeeding" every cycle while making no
// progress at all.
func TestWedge_SessionsMetaConflict_SelfHeals(t *testing.T) {
	t.Parallel()
	f := newLedgerFixture(t)
	const meta = "sessions/s1/meta.json"

	f.diverge(meta,
		`{"session_name":"s1","summary_status":"unrecoverable","summary_attempts":3}`,
		`{"session_name":"s1","summary_status":"ok","title":"Real summary"}`)

	// 1. detected: with the rebase actually halted, and without a sessions/
	// rule, the resolver refuses rather than silently doing something wrong.
	ctx := context.Background()
	f.gitAllowFail(f.local, "pull", "--rebase", "--autostash")
	require.True(t, IsRebaseInProgress(f.local), "fixture must be genuinely wedged")
	err := ResolveRebaseAcceptTheirs(ctx, f.local, []string{"data/"})
	require.Error(t, err, "must refuse when sessions/ is not an auto-resolve prefix")
	assert.Contains(t, err.Error(), "not under safe auto-resolve prefixes")

	// 2. cleared, and 3. progress — with the rule in place.
	_ = AuditAndAbort(ctx, f.local, AuditOpRebase, "test reset", nil)
	rounds, rerr := f.reconcile([]string{"data/", "sessions/"})
	require.NoError(t, rerr, "sessions/ conflicts must auto-resolve")
	assert.Positive(t, rounds, "the conflict must actually have been resolved, not skipped")
	f.assertMakesProgress()
}

// TestWedge_RepairIsIdempotent_NoInfinitePingPong guards the property that
// actually terminates the loop. A resolution that itself re-diverges produces
// commits and green exit codes on every cycle while never converging — it reads
// as recovery and is not.
func TestWedge_RepairIsIdempotent_NoInfinitePingPong(t *testing.T) {
	t.Parallel()
	f := newLedgerFixture(t)
	const meta = "sessions/s1/meta.json"

	f.diverge(meta, `{"s":"local"}`, `{"s":"cloud"}`)
	_, err := f.reconcile([]string{"sessions/"})
	require.NoError(t, err)
	f.assertMakesProgress()

	resolved := f.fileContent(meta)

	// Re-running the whole cycle must be a no-op, not another conflict.
	f.git(f.local, "fetch", "origin")
	rounds, err := f.reconcile([]string{"sessions/"})
	require.NoError(t, err)
	assert.Zero(t, rounds, "a settled ledger must not re-conflict with itself")
	assert.Equal(t, resolved, f.fileContent(meta), "resolution must be stable")
}

// --- B. The LFS pointer invariant ---

// TestWedge_PointerNeverLosesToHydratedContent covers the failure that would
// have been CREATED by fixing wedge A naively.
//
// Failure prevented: committing hydrated bytes over an LFS pointer breaks the
// LFS linkage, and every later push is rejected with "LFS objects are missing"
// — a permanent repo-wide wedge strictly worse than the one being fixed
// (.claude/rules/cache-only-design.md, 2026-04-25 incident).
func TestWedge_PointerNeverLosesToHydratedContent(t *testing.T) {
	t.Parallel()
	pointer := lfsPointer("aa11bb22cc33dd44ee55ff6677889900aabbccddeeff00112233445566778899", 8192)
	hydrated := "# Agent Session\n\nhydrated bytes that must never be committed\n"

	for _, tc := range []struct{ name, local, cloud string }{
		{"pointer on cloud side", hydrated, pointer},
		{"pointer on local side", pointer, hydrated},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newLedgerFixture(t)
			const sessionMD = "sessions/s1/session.md"

			f.diverge(sessionMD, tc.local, tc.cloud)
			_, err := f.reconcile([]string{"sessions/"})
			require.NoError(t, err)

			assert.Equal(t, pointer, f.fileContent(sessionMD),
				"the pointer must survive from EITHER side — that commutativity is what makes replicas converge")
			f.assertMakesProgress()
		})
	}
}

// --- C. Stuck git operations ---

// TestWedge_StaleLockBlocksPushForever covers the ledger that sat blocked for
// three months on a next-index-<pid>.lock: the pid varies so it could not be
// enumerated, and the push pre-flight only ever REPORTED locks while the pull
// path swept them.
func TestWedge_StaleLockBlocksPushForever(t *testing.T) {
	t.Parallel()
	for _, lock := range []string{"index.lock", "next-index-13088.lock", "shallow.lock"} {
		t.Run(lock, func(t *testing.T) {
			f := newLedgerFixture(t)
			p := filepath.Join(f.local, ".git", lock)
			require.NoError(t, os.WriteFile(p, []byte("stale"), 0o644))
			// ownerless locks (index.lock, shallow.lock) require AbandonedLockAge;
			// pid-encoded ones are decided by owner liveness, so this covers both
			old := time.Now().Add(-2 * AbandonedLockAge)
			require.NoError(t, os.Chtimes(p, old, old))

			// 1. detected
			require.NotEmpty(t, HasLockFiles(filepath.Join(f.local, ".git")),
				"a lock we cannot see is a lock we can never clear")

			// 2. cleared by the production pre-flight, and 3. progress
			require.NoError(t, IsSafeForGitOps(f.local), "abandoned lock must self-heal")
			assert.NoFileExists(t, p)
			f.assertMakesProgress()
		})
	}
}

// TestWedge_StaleRebaseDirBlocksEveryPull covers the abandoned rebase — every
// subsequent pull fails with "already a rebase-merge directory" until cleared.
func TestWedge_StaleRebaseDirBlocksEveryPull(t *testing.T) {
	t.Parallel()
	f := newLedgerFixture(t)
	f.diverge("sessions/s1/meta.json", `{"s":"local"}`, `{"s":"cloud"}`)

	// leave a real halted rebase behind
	f.gitAllowFail(f.local, "pull", "--rebase", "--autostash")
	require.True(t, IsRebaseInProgress(f.local), "fixture must actually be wedged")

	// age it past the staleness threshold so it reads as abandoned, not in-flight
	stale := time.Now().Add(-2 * StaleRebaseThreshold)
	dir := filepath.Join(f.local, ".git", "rebase-merge")
	if _, err := os.Stat(dir); err != nil {
		dir = filepath.Join(f.local, ".git", "rebase-apply")
	}
	_ = filepath.Walk(dir, func(p string, _ os.FileInfo, _ error) error {
		_ = os.Chtimes(p, stale, stale)
		return nil
	})
	age, inProgress := RebaseAge(f.local)
	require.True(t, inProgress)
	assert.Greater(t, age, StaleRebaseThreshold, "must classify as stale, not fresh")

	// 2. cleared
	require.NoError(t, AbortOrClearRebase(context.Background(), f.local, "test", nil))
	assert.False(t, IsRebaseInProgress(f.local), "the rebase dir must be gone")

	// 3. progress. Clearing the rebase does not resolve the underlying content
	// conflict — it restores the ability to reach the resolver at all. The wedge
	// is only truly cleared if a normal reconcile now completes, which is exactly
	// what "already a rebase-merge directory" used to make impossible forever.
	_, err := f.reconcile([]string{"sessions/"})
	require.NoError(t, err, "after clearing, the normal repair path must work")
	f.assertMakesProgress()
}

// TestWedge_FreshRebaseIsNeverAborted is the inverse guard. Over-eager recovery
// that yanks a rebase out from under the daemon's own in-flight pull corrupts
// live work — a self-inflicted wedge.
func TestWedge_FreshRebaseIsNeverAborted(t *testing.T) {
	t.Parallel()
	f := newLedgerFixture(t)
	f.diverge("sessions/s1/meta.json", `{"s":"local"}`, `{"s":"cloud"}`)
	f.gitAllowFail(f.local, "pull", "--rebase", "--autostash")
	require.True(t, IsRebaseInProgress(f.local))

	age, inProgress := RebaseAge(f.local)
	require.True(t, inProgress)
	assert.Less(t, age, StaleRebaseThreshold,
		"a rebase started seconds ago must read as fresh so recovery leaves it alone")
}

// --- D. Divergence accounting ---

// TestWedge_DivergenceDetectedAndConverges pins the ahead+behind case end to
// end: detection must fire, and the repair must actually reach 0/0 rather than
// merely returning success.
func TestWedge_DivergenceDetectedAndConverges(t *testing.T) {
	t.Parallel()
	f := newLedgerFixture(t)
	f.diverge("sessions/s1/meta.json", `{"s":"local"}`, `{"s":"cloud"}`)

	ahead := f.git(f.local, "rev-list", "--count", "origin/main..HEAD")
	behind := f.git(f.local, "rev-list", "--count", "HEAD..origin/main")
	require.NotEqual(t, "0", ahead, "fixture must be genuinely diverged")
	require.NotEqual(t, "0", behind)

	_, err := f.reconcile([]string{"sessions/"})
	require.NoError(t, err)
	f.assertMakesProgress()

	assert.Equal(t, "0", f.git(f.local, "rev-list", "--count", "HEAD..origin/main"),
		"reconcile must converge, not just exit cleanly")
}

// TestWedge_ManyConflictsConvergeInBoundedRounds guards against a repair that
// resolves one conflict per pass and effectively never finishes on a ledger
// carrying hundreds of them — the real one had 281.
func TestWedge_ManyConflictsConvergeInBoundedRounds(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: drives many real git commits")
	}
	f := newLedgerFixture(t)

	const n = 25
	for i := 0; i < n; i++ {
		f.write(f.cloud, fmt.Sprintf("sessions/s%d/meta.json", i), `{"s":"cloud"}`)
	}
	f.commitAll(f.cloud, "cloud batch")
	f.git(f.cloud, "push", "origin", "main")

	for i := 0; i < n; i++ {
		f.write(f.local, fmt.Sprintf("sessions/s%d/meta.json", i), `{"s":"local"}`)
	}
	f.commitAll(f.local, "local batch")
	f.git(f.local, "fetch", "origin")

	rounds, err := f.reconcile([]string{"sessions/"})
	require.NoError(t, err)
	assert.LessOrEqual(t, rounds, 5,
		"conflicts must resolve in batches per replayed commit, not one round per file")
	f.assertMakesProgress()
}

// --- E. Paths that must still be refused ---

// TestWedge_NonSessionConflictsStillRefuseAutoResolve ensures widening the
// auto-resolve set to sessions/ did not quietly make human-authored content
// last-writer-wins too.
func TestWedge_NonSessionConflictsStillRefuseAutoResolve(t *testing.T) {
	t.Parallel()
	for _, path := range []string{"AGENTS.md", "docs/architecture.md", "MEMORY.md"} {
		t.Run(path, func(t *testing.T) {
			f := newLedgerFixture(t)
			f.diverge(path, "local edit\n", "cloud edit\n")
			f.gitAllowFail(f.local, "pull", "--rebase", "--autostash")

			err := ResolveRebaseAcceptTheirs(context.Background(), f.local, []string{"data/", "sessions/"})
			require.Error(t, err, "human-authored content must never auto-resolve")
			assert.Contains(t, err.Error(), "not under safe auto-resolve prefixes")
		})
	}
}

// TestWedge_SequentiallyConflictingCommits is the case every earlier fixture
// missed, and the one the real ledger actually hit.
//
// Failure prevented: a rebase replays commits ONE AT A TIME, so
// `git rebase --continue` exits non-zero every time it commits the current step
// and halts on the next conflicting commit. Treating that exit code as failure
// made the caller abort the rebase, restore the pre-rebase state, and re-wedge
// the ledger on every attempt — forever. It cannot reproduce with a single
// conflicting commit, which is exactly why single-commit fixtures passed while
// a 344-commit ledger stayed stuck for 13 days.
func TestWedge_SequentiallyConflictingCommits(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: drives many real git commits")
	}
	f := newLedgerFixture(t)

	// Each local commit must conflict on its OWN file, so the replay halts at
	// EVERY step rather than collapsing into one. This is the real ledger's
	// shape: both sides edited many different sessions over the same period.
	const chain = 10
	for i := 0; i < chain; i++ {
		f.write(f.cloud, fmt.Sprintf("sessions/c%d/meta.json", i), fmt.Sprintf(`{"s":"cloud","n":%d}`, i))
	}
	f.commitAll(f.cloud, "cloud touches all sessions")
	f.git(f.cloud, "push", "origin", "main")

	for i := 0; i < chain; i++ {
		f.write(f.local, fmt.Sprintf("sessions/c%d/meta.json", i), fmt.Sprintf(`{"s":"local","n":%d}`, i))
		f.commitAll(f.local, fmt.Sprintf("local step %d", i))
	}
	f.git(f.local, "fetch", "origin")

	f.gitAllowFail(f.local, "pull", "--rebase", "--autostash")
	require.True(t, IsRebaseInProgress(f.local), "fixture must genuinely conflict")

	// ONE call must carry the rebase all the way through every halting step.
	err := ResolveRebaseAcceptTheirs(context.Background(), f.local, []string{"sessions/"})
	require.NoError(t, err,
		"halting on the next conflicting commit is PROGRESS; treating it as failure re-wedges the ledger")
	assert.False(t, IsRebaseInProgress(f.local),
		"the rebase must be fully finished, not parked on a later step")

	f.assertMakesProgress()
}

// TestWedge_ReplayedCommitBecomesEmpty covers the adjacent shape: a replayed
// commit whose changes are already upstream. git halts with a non-zero exit and
// NO unmerged files, wanting `--skip`. A resolver that only understands
// conflicts must not mistake that for a failure worth aborting over.
func TestWedge_ReplayedCommitBecomesEmpty(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: drives real git commits")
	}
	f := newLedgerFixture(t)
	const meta = "sessions/s1/meta.json"
	const dup = "sessions/s2/meta.json"

	f.write(f.local, dup, `{"s":"identical"}`)
	f.commitAll(f.local, "local adds s2")
	f.cloudWrites(dup, `{"s":"identical"}`, "cloud adds the same s2")
	f.cloudWrites(meta, `{"s":"cloud"}`, "cloud update")
	f.write(f.local, meta, `{"s":"local"}`)
	f.commitAll(f.local, "local update")
	f.git(f.local, "fetch", "origin")

	f.gitAllowFail(f.local, "pull", "--rebase", "--autostash")
	require.True(t, IsRebaseInProgress(f.local), "fixture must genuinely start a rebase")

	err := ResolveRebaseAcceptTheirs(context.Background(), f.local, []string{"sessions/"})
	require.NoError(t, err,
		"an emptied replayed commit must not abort the rebase and re-wedge the ledger")
	assert.False(t, IsRebaseInProgress(f.local), "the rebase must run to completion")

	f.assertMakesProgress()
}

// TestWedge_RebaseHaltsWithNothingToResolve covers the path a code review
// caught that no fixture reached: a rebase that halts with ZERO unmerged
// entries.
//
// Failure prevented: the resolver used to return "no conflicted files found"
// whenever the index had no conflicts. During an ACTIVE rebase that is not an
// error at all — git halts this way when a replayed commit's changes are
// already upstream (rebase.empty=stop, and the default on older git). Returning
// an error made the caller abort, restoring the pre-rebase state and re-wedging
// the ledger. Modern git auto-drops empty commits, which is exactly why the
// end-to-end fixtures passed while the code path stayed broken — so this test
// forces the state directly instead of hoping git produces it.
func TestWedge_RebaseHaltsWithNothingToResolve(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: drives real git commits")
	}
	f := newLedgerFixture(t)
	const meta = "sessions/s1/meta.json"

	f.diverge(meta, `{"s":"local"}`, `{"s":"cloud"}`)
	f.gitAllowFail(f.local, "pull", "--rebase", "--autostash")
	require.True(t, IsRebaseInProgress(f.local), "fixture must halt on a conflict")

	// Resolve and stage by hand so the index is CLEAN while the rebase is still
	// running — the exact shape an empty replayed commit produces.
	f.git(f.local, "checkout", "--theirs", "--", meta)
	f.git(f.local, "add", "--", meta)
	require.Empty(t, f.git(f.local, "ls-files", "--unmerged"),
		"precondition: mid-rebase with nothing left to resolve")
	require.True(t, IsRebaseInProgress(f.local))

	// The resolver must ADVANCE this, not report "no conflicted files found".
	err := ResolveRebaseAcceptTheirs(context.Background(), f.local, []string{"sessions/"})
	require.NoError(t, err,
		"a rebase halted with nothing to resolve must be advanced, not treated as an error")
	assert.False(t, IsRebaseInProgress(f.local), "the rebase must run to completion")

	f.assertMakesProgress()
}

// TestResolveRebase_NoConflictsOutsideRebaseStillErrors keeps the original
// contract intact: called on a repo that is NOT mid-rebase, "no conflicted
// files found" is still the right answer. Without this, the fix above could
// silently turn a caller's programming error into a no-op.
func TestResolveRebase_NoConflictsOutsideRebaseStillErrors(t *testing.T) {
	t.Parallel()
	f := newLedgerFixture(t)
	require.False(t, IsRebaseInProgress(f.local))

	err := ResolveRebaseAcceptTheirs(context.Background(), f.local, []string{"sessions/"})
	require.Error(t, err, "no rebase in progress and no conflicts is a caller error")
	assert.Contains(t, err.Error(), "no conflicted files found")
}
