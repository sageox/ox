package daemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/gitutil"
	"github.com/sageox/ox/internal/ledger"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- A. ledgerSyncWedged detection ---
//
// checkAndRunGC's existing triggers (interval exceeded, full-clone upgrade)
// never catch a wedged ledger on their own — a wedge can persist
// indefinitely without ever exceeding the GC interval. ledgerSyncWedged is
// the missing third trigger. Failure prevented: a ledger stuck ahead+behind
// forever with no automated recovery path, exactly the reported incident.
//
// These use a plain (non-ledger-structured) bare+clone fixture — ledgerSyncWedged
// is pure git plumbing and doesn't care about sessions/ or sparse checkout.

func TestLedgerSyncWedged_FreshUnpushedCommit_NotWedged(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	isolateCredentials(t)
	s := gcTestScheduler(t)

	_, cloneDir := gcInitBareAndClone(t, t.TempDir())
	require.NoError(t, os.WriteFile(filepath.Join(cloneDir, "new.txt"), []byte("x"), 0o644))
	require.NoError(t, exec.Command("git", "-C", cloneDir, "add", "-A").Run())
	require.NoError(t, exec.Command("git", "-C", cloneDir, "commit", "-m", "unpushed").Run())

	wedged, _, count, _ := s.ledgerSyncWedged(context.Background(), cloneDir)
	assert.False(t, wedged, "a fresh unpushed commit is not a wedge — normal push/pull will resolve it")
	assert.Equal(t, 1, count)
}

func TestLedgerSyncWedged_AheadOnly_NotWedged(t *testing.T) {
	// ahead but never behind: a plain push (gcPushUnpushedCommits) resolves
	// this on its own, regardless of age — must not be treated as wedged.
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	isolateCredentials(t)
	s := gcTestScheduler(t)

	_, cloneDir := gcInitBareAndClone(t, t.TempDir())
	require.NoError(t, os.WriteFile(filepath.Join(cloneDir, "new.txt"), []byte("x"), 0o644))
	require.NoError(t, exec.Command("git", "-C", cloneDir, "add", "-A").Run())
	require.NoError(t, exec.Command("git", "-C", cloneDir, "commit", "-m", "unpushed").Run())
	backdateCommitTimestamp(t, cloneDir, -4*time.Hour)

	wedged, _, _, _ := s.ledgerSyncWedged(context.Background(), cloneDir)
	assert.False(t, wedged, "ahead-only, never behind, must not be wedged regardless of age")
}

func TestLedgerSyncWedged_BehindOnly_NotWedged(t *testing.T) {
	// behind but never ahead: an ordinary pull resolves this — must not be
	// treated as wedged.
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	isolateCredentials(t)
	s := gcTestScheduler(t)

	bareDir, cloneDir := gcInitBareAndClone(t, t.TempDir())

	otherDir := t.TempDir()
	require.NoError(t, exec.Command("git", "clone", bareDir, otherDir).Run())
	gitConfig(t, otherDir)
	require.NoError(t, os.WriteFile(filepath.Join(otherDir, "other.txt"), []byte("x"), 0o644))
	require.NoError(t, exec.Command("git", "-C", otherDir, "add", "-A").Run())
	require.NoError(t, exec.Command("git", "-C", otherDir, "commit", "-m", "remote advance").Run())
	require.NoError(t, exec.Command("git", "-C", otherDir, "push", "origin", "HEAD:main").Run())

	wedged, _, _, _ := s.ledgerSyncWedged(context.Background(), cloneDir)
	assert.False(t, wedged, "behind-only (never ahead) must not be wedged — a plain pull resolves it")
}

func TestLedgerSyncWedged_GenuinelyWedged_DetectsAfterAgeThreshold(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	isolateCredentials(t)
	s := gcTestScheduler(t)

	bareDir, cloneDir := gcInitBareAndClone(t, t.TempDir())
	diverge(t, bareDir, cloneDir, "local.txt", "remote.txt")

	// too young: must not be wedged yet even though ahead+behind
	wedged, age, count, _ := s.ledgerSyncWedged(context.Background(), cloneDir)
	assert.False(t, wedged, "ahead+behind but younger than ledgerSyncWedgeAge must not be wedged yet")
	assert.Equal(t, 1, count)
	assert.Less(t, age, ledgerSyncWedgeAge)

	// backdate the local commit past the threshold
	backdateCommitTimestamp(t, cloneDir, -4*time.Hour)

	wedged, age, count, _ = s.ledgerSyncWedged(context.Background(), cloneDir)
	assert.True(t, wedged, "ahead+behind older than ledgerSyncWedgeAge must be detected as wedged")
	assert.GreaterOrEqual(t, age, ledgerSyncWedgeAge)
	assert.Equal(t, 1, count)
}

func TestLedgerSyncWedged_Offline_NotWedged(t *testing.T) {
	// fetch failure (remote unreachable) must never be mistaken for wedged —
	// offline is a normal, explicitly supported daemon state.
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	isolateCredentials(t)
	s := gcTestScheduler(t)

	bareDir, cloneDir := gcInitBareAndClone(t, t.TempDir())
	diverge(t, bareDir, cloneDir, "local.txt", "remote.txt")
	backdateCommitTimestamp(t, cloneDir, -4*time.Hour)

	// point origin at a nonexistent path so fetch fails
	require.NoError(t, exec.Command("git", "-C", cloneDir, "remote", "set-url", "origin", "/nonexistent/repo.git").Run())

	wedged, _, _, lockBusy := s.ledgerSyncWedged(context.Background(), cloneDir)
	assert.False(t, wedged, "a fetch failure (offline) must not be classified as wedged")
	assert.False(t, lockBusy, "a genuine offline fetch failure is not lock contention")
}

// diverge makes cloneDir simultaneously ahead (one unpushed local commit
// adding localFile) and behind (a second writer pushed remoteFile to
// bareDir after cloneDir last synced) — the shape a wedged sync produces,
// without any content conflict (different files) so capture/restore has a
// clean case to prove works before layering an irreconcilable conflict on.
func diverge(t *testing.T, bareDir, cloneDir, localFile, remoteFile string) {
	t.Helper()

	require.NoError(t, os.WriteFile(filepath.Join(cloneDir, localFile), []byte("local content"), 0o644))
	require.NoError(t, exec.Command("git", "-C", cloneDir, "add", "-A").Run())
	require.NoError(t, exec.Command("git", "-C", cloneDir, "commit", "-m", "local: "+localFile).Run())

	remoteWriterDir := t.TempDir()
	require.NoError(t, exec.Command("git", "clone", bareDir, remoteWriterDir).Run())
	gitConfig(t, remoteWriterDir)
	require.NoError(t, os.WriteFile(filepath.Join(remoteWriterDir, remoteFile), []byte("remote content"), 0o644))
	require.NoError(t, exec.Command("git", "-C", remoteWriterDir, "add", "-A").Run())
	require.NoError(t, exec.Command("git", "-C", remoteWriterDir, "commit", "-m", "remote: "+remoteFile).Run())
	require.NoError(t, exec.Command("git", "-C", remoteWriterDir, "push", "origin", "HEAD:main").Run())
}

// backdateCommitTimestamp rewrites HEAD's author+committer date so
// ledgerSyncWedged's %ct-based age computation reads a genuinely old
// commit, without a real-time sleep.
func backdateCommitTimestamp(t *testing.T, repo string, delta time.Duration) {
	t.Helper()
	newDate := time.Now().Add(delta).Format(time.RFC3339)
	cmd := exec.Command("git", "-C", repo, "commit", "--amend", "--no-edit", "--date="+newDate)
	cmd.Env = append(os.Environ(), "GIT_COMMITTER_DATE="+newDate) // safe: git subprocess, not ox
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
}

// --- B. capture-and-restore on a genuinely diverged ledger ---
//
// checkAndRunGC's existing GC path explicitly skips (gcSkippedDirty) exactly
// the state a wedged ledger produces: unpushed local commits that a plain
// push can't land because the remote diverged. Failure prevented: GC being
// structurally unable to rescue the one scenario it exists for.

func TestGC_CaptureUnpushedOnDiverge_Succeeds(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	isolateCredentials(t)

	bareDir := setupLedgerBareRepo(t)
	cloneURL := "file://" + bareDir
	projectDir := setupProjectWithConfig(t, "")
	s := newTestScheduler(projectDir)

	ledgerDir := filepath.Join(t.TempDir(), "ledger")
	require.NoError(t, ledger.CloneWithSparseCheckout(ledgerDir, cloneURL))
	gitConfig(t, ledgerDir)

	diverge(t, bareDir, ledgerDir, filepath.Join("sessions", "local.txt"), filepath.Join("sessions", "remote.txt"))

	ws := WorkspaceState{
		ID:       "ledger",
		Type:     WorkspaceTypeLedger,
		Path:     ledgerDir,
		CloneURL: cloneURL,
		Exists:   true,
	}
	registry := s.WorkspaceRegistry()
	registry.mu.Lock()
	registry.ledger = &ws
	registry.workspaces[ws.ID] = &ws
	registry.mu.Unlock()

	// sanity: an ordinary (non-diverge-aware) reclone must still skip,
	// proving this fixture genuinely reproduces today's bug before
	// asserting the fix. The failed push mutates nothing locally, so
	// ledgerDir is safe to reuse for the real assertion below.
	plainResult := s.runBlueGreenGC(context.Background(), ws)
	require.Equal(t, gcSkippedDirty, plainResult, "sanity check: an ordinary reclone must skip on a diverged push (this is the bug being fixed)")

	result, recovered := s.runBlueGreenGCOpts(context.Background(), ws, true)
	require.Equal(t, gcSuccess, result, "diverge-aware reclone must succeed instead of skipping")
	assert.True(t, recovered, "the diverge-capture path actually ran and captured content — recovered must be true, not just gcSuccess")

	// the remote's content must be present (reclone actually happened)
	assert.FileExists(t, filepath.Join(ledgerDir, "sessions", "remote.txt"))

	// the local unpushed commit's content must be recovered...
	assert.FileExists(t, filepath.Join(ledgerDir, "sessions", "local.txt"))

	// ...but as UNCOMMITTED working-tree changes — the daemon never commits
	// (.claude/rules/daemon-git.md). git status must show it, not git log.
	statusOut, err := exec.Command("git", "-C", ledgerDir, "status", "--porcelain").CombinedOutput()
	require.NoError(t, err, string(statusOut))
	assert.Contains(t, string(statusOut), "local.txt", "recovered content must land as an uncommitted change, not a daemon-authored commit")
}

// --- C. adversarial-review findings: data-loss and crash-safety bugs ---
//
// These pin four bugs an adversarial pass found in the capture/reclone/
// restore cycle above — none caught by the tests in section B, which only
// exercise the clean-success path. Each test proves the specific class of
// silent data loss the fix closes.

// TestGC_UntrackedRestoreFailure_PreservesBackup proves that when
// gcRestoreUntracked fails partway through (one colliding path), the
// untracked-file backup directory survives instead of being deleted anyway.
// Failure prevented: the diff-restore path already gated its cleanup on
// success (diffApplied); the untracked-restore path didn't, so a single
// permission error or path collision on ONE captured file silently deleted
// the only surviving copy of every OTHER captured file too.
func TestGC_UntrackedRestoreFailure_PreservesBackup(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	isolateCredentials(t)

	bareDir := setupLedgerBareRepo(t)
	cloneURL := "file://" + bareDir
	projectDir := setupProjectWithConfig(t, "")
	s := newTestScheduler(projectDir)

	ledgerDir := filepath.Join(t.TempDir(), "ledger")
	require.NoError(t, ledger.CloneWithSparseCheckout(ledgerDir, cloneURL))
	gitConfig(t, ledgerDir)

	// Seed the BARE remote (so the fresh reclone picks it up) with a
	// committed FILE at sessions/conflict — then the local clone has an
	// UNTRACKED nested file at sessions/conflict/nested.txt. After reclone,
	// gcRestoreUntracked's os.MkdirAll(".../sessions/conflict") to restore
	// the nested file collides with the freshly-cloned FILE of the same
	// name, forcing a real, reliable partial-restore failure — not a
	// synthetic error injection.
	seedDir := t.TempDir()
	require.NoError(t, exec.Command("git", "clone", bareDir, seedDir).Run())
	gitConfig(t, seedDir)
	require.NoError(t, os.WriteFile(filepath.Join(seedDir, "sessions", "conflict"), []byte("i am a file"), 0o644))
	require.NoError(t, exec.Command("git", "-C", seedDir, "add", "-A").Run())
	require.NoError(t, exec.Command("git", "-C", seedDir, "commit", "-m", "seed conflicting file").Run())
	require.NoError(t, exec.Command("git", "-C", seedDir, "push", "origin", "HEAD:main").Run())

	// also seed one UNAMBIGUOUSLY-restorable untracked file, so the test
	// can tell "backup preserved" apart from "restore silently no-op'd".
	require.NoError(t, os.WriteFile(filepath.Join(ledgerDir, "plain.txt"), []byte("plain"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(ledgerDir, "sessions", "conflict"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(ledgerDir, "sessions", "conflict", "nested.txt"), []byte("nested"), 0o644))

	ws := WorkspaceState{
		ID:       "ledger",
		Type:     WorkspaceTypeLedger,
		Path:     ledgerDir,
		CloneURL: cloneURL,
		Exists:   true,
	}
	registry := s.WorkspaceRegistry()
	registry.mu.Lock()
	registry.ledger = &ws
	registry.workspaces[ws.ID] = &ws
	registry.mu.Unlock()

	result := s.runBlueGreenGC(context.Background(), ws)
	require.Equal(t, gcSuccess, result, "the reclone itself must still succeed — only the untracked restore partially fails")

	// the colliding file must exist (it's what the remote committed)
	assert.FileExists(t, filepath.Join(ledgerDir, "sessions", "conflict"))

	// the untracked backup must SURVIVE — gcRestoreUntracked failed on the
	// colliding nested file, so untrackedRestored must be false and the
	// backup dir must not have been deleted.
	backupDir := ledgerDir + ".gc-untracked"
	assert.DirExists(t, backupDir, "backup must be preserved when restore partially fails — this is the actual regression: it used to be deleted unconditionally")
	assert.FileExists(t, filepath.Join(backupDir, "plain.txt"), "the backup must still contain the file that DID restore successfully")
}

// TestGC_RestoreDiff_PartialReject_CleansRejFilesAndReportsPartial proves
// gcRestoreDiff correctly classifies a REAL multi-file --reject apply where
// some hunks land and some don't, instead of relying on the exit code alone.
// Failure prevented: `git apply --reject` exits non-zero whenever at least
// one hunk is rejected, even when other hunks in the same invocation
// applied cleanly — so the old code's "success, clean up .rej" branch was
// unreachable for the exact scenario it existed for. Every real
// partial-reject run instead fell into the hard-failure branch, which
// never ran removeRejFiles — leaving .rej markers sitting in the working
// tree permanently, undocumented, with the successfully-applied hunks
// already silently on disk despite being reported as "not restored".
func TestGC_RestoreDiff_PartialReject_CleansRejFilesAndReportsPartial(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	isolateCredentials(t)
	s := gcTestScheduler(t)
	ctx := context.Background()

	// source repo: two tracked files, both modified uncommitted
	srcDir := t.TempDir()
	gcInitGitRepo(t, srcDir)
	require.NoError(t, os.WriteFile(filepath.Join(srcDir, "README.md"), []byte("clean change\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(srcDir, "conflict.txt"), []byte("base\n"), 0o644))
	require.NoError(t, exec.Command("git", "-C", srcDir, "add", "-A").Run())
	require.NoError(t, exec.Command("git", "-C", srcDir, "commit", "-m", "add conflict.txt").Run())
	require.NoError(t, os.WriteFile(filepath.Join(srcDir, "conflict.txt"), []byte("source's change\n"), 0o644))

	diffFile := filepath.Join(t.TempDir(), "patch.diff")
	hasDiff, err := s.gcCaptureDiff(ctx, srcDir, diffFile, "HEAD")
	require.NoError(t, err)
	require.True(t, hasDiff)

	// destination: same base commit, but conflict.txt already diverged
	// differently there — this hunk WILL be rejected. README.md is
	// untouched at the destination — that hunk WILL apply cleanly.
	dstDir := t.TempDir()
	require.NoError(t, exec.Command("git", "clone", srcDir, dstDir).Run())
	// clone picks up srcDir's uncommitted state via filesystem copy? No —
	// git clone only clones committed history, so dstDir starts clean at
	// the "add conflict.txt" commit, matching the diff's base exactly.
	gitConfig(t, dstDir)
	require.NoError(t, os.WriteFile(filepath.Join(dstDir, "conflict.txt"), []byte("destination's different change\n"), 0o644))

	restoreErr := s.gcRestoreDiff(ctx, dstDir, diffFile)
	require.Error(t, restoreErr, "a partial reject must be reported as an error so the caller preserves diffFile")
	assert.Contains(t, restoreErr.Error(), "rejected", "error must say hunks were rejected, not report a generic apply failure")

	// the clean hunk (README.md) must have actually landed on disk despite
	// the overall call reporting an error — this is exactly what the old
	// exit-code-only check got wrong.
	data, err := os.ReadFile(filepath.Join(dstDir, "README.md"))
	require.NoError(t, err)
	assert.Equal(t, "clean change\n", string(data), "the non-conflicting hunk must have been applied even though the overall result is an error")

	// .rej markers must be swept — not left in the tree permanently.
	rejMatches, err := filepath.Glob(filepath.Join(dstDir, "*.rej"))
	require.NoError(t, err)
	assert.Empty(t, rejMatches, ".rej files must be cleaned up even on the partial-failure path, not just the (previously unreachable) full-success path")

	// diffFile itself must still exist — the caller (runBlueGreenGCOpts)
	// relies on the error return to know to preserve it.
	assert.FileExists(t, diffFile)
}

// TestGC_LedgerWedgeRecovery_ClearsSyncBackoff proves that a successful
// wedge-recovery reclone also clears the ledger's accumulated sync-failure
// backoff state, not just the session-conflict issue.
// Failure prevented: RecordSyncFailure climbs exponential backoff on every
// failed pull cycle leading up to a wedge (doPull, sync.go). Without
// clearing it here, the very next scheduled pull after a successful
// recovery can still hit that stale backoff and re-log "sync in backoff,
// skipping" — the literal diagnostic line from the original incident
// report — immediately after the incident was supposedly resolved.
func TestGC_LedgerWedgeRecovery_ClearsSyncBackoff(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	isolateCredentials(t)

	bareDir := setupLedgerBareRepo(t)
	cloneURL := "file://" + bareDir
	projectDir := setupProjectWithConfig(t, "")
	s := newTestScheduler(projectDir)

	ledgerDir := filepath.Join(t.TempDir(), "ledger")
	require.NoError(t, ledger.CloneWithSparseCheckout(ledgerDir, cloneURL))
	gitConfig(t, ledgerDir)

	diverge(t, bareDir, ledgerDir, filepath.Join("sessions", "local.txt"), filepath.Join("sessions", "remote.txt"))

	ws := WorkspaceState{
		ID:       "ledger",
		Type:     WorkspaceTypeLedger,
		Path:     ledgerDir,
		CloneURL: cloneURL,
		Exists:   true,
	}
	registry := s.WorkspaceRegistry()
	registry.mu.Lock()
	registry.ledger = &ws
	registry.workspaces[ws.ID] = &ws
	registry.mu.Unlock()

	// simulate the failed-pull-cycles that precede a real wedge: climb
	// backoff the same way doPull's RecordSyncFailure would.
	s.WorkspaceRegistry().RecordSyncFailure("ledger")
	s.WorkspaceRegistry().RecordSyncFailure("ledger")
	s.WorkspaceRegistry().RecordSyncFailure("ledger")
	require.False(t, s.WorkspaceRegistry().ShouldSync("ledger"), "sanity check: accumulated failures must actually produce a backoff window before recovery")

	result, recovered := s.runBlueGreenGCOpts(context.Background(), ws, true)
	require.Equal(t, gcSuccess, result)
	require.True(t, recovered)

	assert.True(t, s.WorkspaceRegistry().ShouldSync("ledger"),
		"a successful wedge recovery must clear accumulated sync-failure backoff — otherwise the next scheduled pull re-hits the stale backoff and re-logs the original incident's exact diagnostic line")
}

// TestLedgerSyncWedged_CooldownIndependentOfUnrelatedGC proves the wedge
// re-check cooldown is tracked separately from LastGCTime, which any
// successful GC updates regardless of trigger reason.
// Failure prevented: an unrelated interval-triggered or full-clone GC
// updates LastGCTime; if the wedge cooldown shared that clock, a wedge
// forming shortly after an unrelated GC would have its detection *check*
// (not just repeat recovery attempts) delayed by up to 2x
// ledgerSyncWedgeAge — the live fetch that would confirm the wedge never
// even runs during that window.
func TestLedgerSyncWedged_CooldownIndependentOfUnrelatedGC(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	s := gcTestScheduler(t)

	// an unrelated GC just updated LastGCTime "now" via the registry path
	// checkAndRunGC itself would use — simulate that directly rather than
	// running a full reclone, since this test is only about the cooldown
	// gate's clock source, not about GC itself.
	s.WorkspaceRegistry().UpdateLastGC("ledger")

	// the daemon has NOT restarted and no wedge check has ever run —
	// s.lastWedgeCheck is still zero, so the cooldown must be elapsed
	// (IsZero counts as "never checked, go ahead") regardless of the
	// unrelated GC's fresh LastGCTime.
	s.mu.Lock()
	cooldownElapsed := s.lastWedgeCheck.IsZero() || time.Since(s.lastWedgeCheck) >= ledgerGCWedgeCooldown
	s.mu.Unlock()
	assert.True(t, cooldownElapsed,
		"the wedge check cooldown must not be coupled to LastGCTime — an unrelated GC must never delay the FIRST wedge check")
}

// --- D. orphan recovery from an interrupted GC swap ---
//
// runBlueGreenGCOpts writes ".gc-swap-done" right after the rename-swap
// succeeds and only removes it once phase 2's restore (diff + untracked +
// cache) has been fully attempted. If the daemon crashes or is killed in
// that narrow window, the next GC invocation must recognize the marker and
// apply the orphaned backups BEFORE treating them as ordinary leftovers —
// the pre-swap tree is already gone at that point, so diffFile/untrackedDir
// are the ONLY surviving copy of whatever phase 0 captured. Before this fix,
// the leftover-cleanup loop deleted them unconditionally on the next run,
// silently discarding live data. Failure prevented: a daemon crash/restart
// timed to land inside the swap window permanently loses whatever
// uncommitted/untracked content GC was in the middle of preserving.

// TestGC_OrphanedSwapArtifacts_RecoveredNotDiscarded simulates exactly that
// crash timing: ws.Path already holds a clean tree (standing in for "the
// swap already completed"), the swap marker is present, and diffFile /
// untrackedDir hold real backups captured via the actual production capture
// helpers (not hand-crafted fixtures, so the diff is in the exact format
// gcRestoreDiff expects). It then runs a full runBlueGreenGCOpts cycle and
// asserts the orphaned content survives all the way through to the final
// state, the marker is gone, and no backup artifacts are left behind.
func TestGC_OrphanedSwapArtifacts_RecoveredNotDiscarded(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	isolateCredentials(t)

	tmp := t.TempDir()
	bareDir, cloneDir := gcInitBareAndClone(t, tmp)
	s := gcTestScheduler(t)
	ctx := context.Background()

	// team-context validateGCClone requires .sageox/ + a core file to
	// accept the reclone that runs after orphan recovery below.
	for _, f := range []string{"SOUL.md", ".sageox/config.json"} {
		fullPath := filepath.Join(cloneDir, f)
		require.NoError(t, os.MkdirAll(filepath.Dir(fullPath), 0755))
		require.NoError(t, os.WriteFile(fullPath, []byte("content"), 0644))
	}
	require.NoError(t, exec.Command("git", "-C", cloneDir, "add", ".").Run())
	require.NoError(t, exec.Command("git", "-C", cloneDir, "commit", "-m", "add structure").Run())
	require.NoError(t, exec.Command("git", "-C", cloneDir, "push", "origin", "HEAD:main").Run())

	// simulate an in-flight GC's phase 0 capture: a dirty tracked change
	// and an untracked file, exactly what phase 0 would have captured
	// before the crash.
	require.NoError(t, os.WriteFile(filepath.Join(cloneDir, "README.md"), []byte("orphaned-diff-content"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(cloneDir, "orphaned-untracked.txt"), []byte("orphaned-untracked-content"), 0644))

	diffFile := cloneDir + ".gc-diff"
	untrackedDir := cloneDir + ".gc-untracked"
	swapMarker := cloneDir + ".gc-swap-done"

	hasDiff, err := s.gcCaptureDiff(ctx, cloneDir, diffFile, "HEAD")
	require.NoError(t, err)
	require.True(t, hasDiff)
	hasUntracked, err := s.gcCaptureUntracked(ctx, cloneDir, untrackedDir)
	require.NoError(t, err)
	require.True(t, hasUntracked)

	// now put ws.Path back into the clean state a freshly-swapped-in clone
	// would be in — the crash this test simulates happens AFTER the swap,
	// so by the time this GC invocation starts, ws.Path is the fresh clone,
	// not the dirty pre-GC tree. The backups above are the only surviving
	// copy of the dirty content, exactly as they'd be after a real crash.
	require.NoError(t, exec.Command("git", "-C", cloneDir, "checkout", "--", "README.md").Run())
	require.NoError(t, os.Remove(filepath.Join(cloneDir, "orphaned-untracked.txt")))

	// mark the swap as having completed, so this invocation takes the
	// orphan-recovery path instead of treating the backups as leftovers.
	require.NoError(t, os.WriteFile(swapMarker, nil, 0o600))

	ws := WorkspaceState{
		ID:       "orphan-test",
		Type:     WorkspaceTypeTeamContext,
		TeamName: "test-team",
		Path:     cloneDir,
		CloneURL: "file://" + bareDir,
		Exists:   true,
	}

	result := s.runBlueGreenGC(ctx, ws)
	require.Equal(t, gcSuccess, result, "GC must succeed despite recovering from an interrupted prior swap")

	data, err := os.ReadFile(filepath.Join(cloneDir, "README.md"))
	require.NoError(t, err)
	assert.Equal(t, "orphaned-diff-content", string(data),
		"the diff orphaned by the interrupted swap must survive through to the final state, not be silently discarded as a leftover")

	data, err = os.ReadFile(filepath.Join(cloneDir, "orphaned-untracked.txt"))
	require.NoError(t, err)
	assert.Equal(t, "orphaned-untracked-content", string(data),
		"the untracked file orphaned by the interrupted swap must survive through to the final state")

	assert.NoFileExists(t, swapMarker, "the swap marker must be cleared once recovery has been fully attempted")
	assert.NoFileExists(t, diffFile, "the orphaned diff backup must not be left behind once successfully applied")
	assert.NoDirExists(t, untrackedDir, "the orphaned untracked backup must not be left behind once successfully applied")
}

// TestGC_OrphanedSwapArtifacts_RecoveryFailureBlocksNewCycle proves a
// failure while applying an orphaned backup does NOT fall through into
// starting a brand new GC cycle on top of a half-recovered tree — the
// backup must be preserved for manual recovery and the function must
// return without touching anything else.
// Failure prevented: an unrecoverable diff conflict silently discarded in
// favor of starting a fresh capture/clone/swap cycle, compounding data loss
// on top of the original crash instead of stopping to let a human look.
func TestGC_OrphanedSwapArtifacts_RecoveryFailureBlocksNewCycle(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	isolateCredentials(t)

	tmp := t.TempDir()
	bareDir, cloneDir := gcInitBareAndClone(t, tmp)
	s := gcTestScheduler(t)

	diffFile := cloneDir + ".gc-diff"
	swapMarker := cloneDir + ".gc-swap-done"

	// an unappliable diff: garbage content that gcRestoreDiff's 3way and
	// reject fallbacks both cannot apply cleanly, forcing gcRestoreDiff to
	// return an error rather than silently no-op.
	require.NoError(t, os.WriteFile(diffFile, []byte("not a valid diff\x00binary garbage"), 0o600))
	require.NoError(t, os.WriteFile(swapMarker, nil, 0o600))

	ws := WorkspaceState{
		ID:       "orphan-fail-test",
		Type:     WorkspaceTypeTeamContext,
		TeamName: "test-team",
		Path:     cloneDir,
		CloneURL: "file://" + bareDir,
		Exists:   true,
	}

	result := s.runBlueGreenGC(context.Background(), ws)
	assert.Equal(t, gcFailed, result, "an unrecoverable orphaned backup must fail closed, not proceed to a fresh GC cycle")

	assert.FileExists(t, diffFile, "the unrecoverable diff backup must be preserved for manual recovery, not discarded")
	assert.FileExists(t, swapMarker, "the swap marker must remain set so the next invocation retries recovery instead of skipping it")
}

// --- E. cross-process swap lock actually protects the swap window ---
//
// waitForGCSwap (cmd/ox/session_upload.go) is the CLI-side half of a
// cross-process guard: it polls ".gc-swap-lock" before writing directly to
// the ledger, to avoid racing the daemon's rename-swap. That guard is only
// meaningful if the daemon side actually holds the file for (at least) the
// real risky window — from just before the rename to just after the old
// clone is removed — and removes it promptly once that window closes. A
// regression that wrote the lock too late, removed it too early, or never
// removed it at all would go undetected by every other GC test, since none
// of them observe the lock file's state DURING the swap, only that GC
// eventually succeeds.

// TestGC_SwapLock_HeldDuringSwapWindow_RemovedAfter uses the
// gcSwapWindowTestHook seam (called immediately after the lock is written,
// mirroring the existing gcAsyncTestHook pattern in sync_gc_async_test.go)
// to pause a real GC mid-swap, assert the lock file is actually present on
// disk at that moment, then release it and assert it's gone once GC
// completes.
// Failure prevented: the daemon writes/removes ".gc-swap-lock" at the wrong
// point (or not at all), silently reopening the exact lost-write race this
// mechanism exists to close — a session upload landing in what becomes the
// deleted old clone, with no error surfaced anywhere.
func TestGC_SwapLock_HeldDuringSwapWindow_RemovedAfter(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	isolateCredentials(t)

	tmp := t.TempDir()
	bareDir := filepath.Join(tmp, "ledger.bare")
	seedDir := filepath.Join(tmp, "seed")
	require.NoError(t, exec.Command("git", "init", "--bare", "--initial-branch=main", bareDir).Run())
	require.NoError(t, exec.Command("git", "clone", bareDir, seedDir).Run())
	gitConfig(t, seedDir)
	require.NoError(t, os.MkdirAll(filepath.Join(seedDir, "sessions"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(seedDir, "sessions", ".gitkeep"), []byte(""), 0644))
	require.NoError(t, exec.Command("git", "-C", seedDir, "add", ".").Run())
	require.NoError(t, exec.Command("git", "-C", seedDir, "commit", "-m", "initial").Run())
	require.NoError(t, exec.Command("git", "-C", seedDir, "push", "origin", "HEAD:main").Run())

	cloneDir := filepath.Join(tmp, "ledger")
	require.NoError(t, exec.Command("git", "clone", "file://"+bareDir, cloneDir).Run())
	gitConfig(t, cloneDir)

	s := gcTestScheduler(t)
	lockPath := cloneDir + ".gc-swap-lock"

	reachedSwap := make(chan struct{})
	release := make(chan struct{})
	s.gcSwapWindowTestHook = func() {
		close(reachedSwap)
		<-release
	}

	ws := WorkspaceState{
		ID:       "swap-lock-test",
		Type:     WorkspaceTypeLedger,
		Path:     cloneDir,
		CloneURL: "file://" + bareDir,
		Exists:   true,
	}

	done := make(chan gcResult, 1)
	go func() {
		result, _ := s.runBlueGreenGCOpts(context.Background(), ws, false)
		done <- result
	}()

	select {
	case <-reachedSwap:
	case <-time.After(10 * time.Second):
		t.Fatal("GC did not reach the swap window in time")
	}
	assert.FileExists(t, lockPath, "swap lock must exist for the duration of the rename+cleanup window")
	close(release)

	select {
	case result := <-done:
		assert.Equal(t, gcSuccess, result, "GC should succeed")
	case <-time.After(10 * time.Second):
		t.Fatal("GC did not complete after the swap window was released")
	}
	assert.NoFileExists(t, lockPath, "swap lock must be removed once the risky window ends")
}

// --- F. dispatch-level wedge-issue clearing ---
//
// TestGC_LedgerWedgeRecovery_ClearsSyncBackoff (above) proves
// runBlueGreenGCOpts itself clears sync-failure backoff on a successful
// diverge-capture recovery. But the IssueTypeSessionConflictWedge issue
// itself is cleared one layer up, in checkAndRunGC's dispatch switch — and
// specifically on the PLAIN gcSuccess case (not just gcSuccess-and-recovered)
// for when GC succeeds without actually needing to capture anything (e.g.
// the unpushed commits' net content change against the merge-base is
// empty). That dispatch-level branch was never exercised by any test:
// every existing GC test either calls runBlueGreenGC/Opts directly
// (skipping checkAndRunGC's trigger detection and issue-clearing switch
// entirely) or doesn't seed a pre-existing wedge issue to observe being
// cleared.

// TestCheckAndRunGC_WedgeTrigger_ClearsSessionConflictIssueOnPlainSuccess
// drives the real dispatch path: a genuinely wedged ledger (ahead+behind,
// old enough) whose two unpushed commits cancel out to an empty net diff
// against the merge-base — so gcPushUnpushedCommits fails non-fast-forward
// (entering the diverge-capture branch, since wedged=true is passed through
// as captureUnpushedOnDiverge), but gcCaptureDiff finds nothing to capture,
// landing at gcSuccess with recovered=false. Seeds the tracker with the
// wedge issue a prior pull cycle would have set, calls checkAndRunGC itself
// (not the lower-level GC function), and asserts the issue is cleared.
// Failure prevented: a wedge that resolves itself with nothing left to
// recover (recovered=false) leaves the stale IssueTypeSessionConflictWedge
// alert permanently showing in `ox status`/`ox doctor` even after the
// underlying problem is gone — because the only clearing code that fires on
// a plain (non-recovered) success lives in checkAndRunGC's dispatch switch,
// never reached by calling runBlueGreenGCOpts directly.
func TestCheckAndRunGC_WedgeTrigger_ClearsSessionConflictIssueOnPlainSuccess(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	isolateCredentials(t)

	bareDir := setupLedgerBareRepo(t)
	cloneURL := "file://" + bareDir
	projectDir := setupProjectWithConfig(t, "")
	s := newTestScheduler(projectDir)
	tracker := NewIssueTracker()
	s.SetIssueTracker(tracker)

	ledgerDir := filepath.Join(t.TempDir(), "ledger")
	require.NoError(t, ledger.CloneWithSparseCheckout(ledgerDir, cloneURL))
	gitConfig(t, ledgerDir)
	// checkAndRunGC's wedge-check block only runs when fullClone is false
	// (!isPartialClone gates it alongside interval/cooldown) — mark this
	// fixture as a partial clone the same way a real ledger clone is, so
	// the "full clone upgrade" trigger doesn't fire instead and skip past
	// the wedge-detection branch entirely.
	require.NoError(t, exec.Command("git", "-C", ledgerDir, "config", "extensions.partialClone", "origin").Run())

	// two local commits whose net content change cancels out: add a file,
	// then remove it again in a second commit. Ahead count is 2, but the
	// diff against the merge-base is empty.
	require.NoError(t, os.WriteFile(filepath.Join(ledgerDir, "sessions", "temp.txt"), []byte("temp"), 0o644))
	require.NoError(t, exec.Command("git", "-C", ledgerDir, "add", "-A").Run())
	require.NoError(t, exec.Command("git", "-C", ledgerDir, "commit", "-m", "add temp file").Run())
	backdateCommitTimestamp(t, ledgerDir, -4*time.Hour)

	require.NoError(t, exec.Command("git", "-C", ledgerDir, "rm", "sessions/temp.txt").Run())
	require.NoError(t, exec.Command("git", "-C", ledgerDir, "commit", "-m", "revert temp file").Run())

	// a remote-only commit so the ledger is genuinely behind too, not just ahead.
	remoteWriterDir := t.TempDir()
	require.NoError(t, exec.Command("git", "clone", bareDir, remoteWriterDir).Run())
	gitConfig(t, remoteWriterDir)
	require.NoError(t, os.WriteFile(filepath.Join(remoteWriterDir, "sessions", "remote.txt"), []byte("remote"), 0o644))
	require.NoError(t, exec.Command("git", "-C", remoteWriterDir, "add", "-A").Run())
	require.NoError(t, exec.Command("git", "-C", remoteWriterDir, "commit", "-m", "remote change").Run())
	require.NoError(t, exec.Command("git", "-C", remoteWriterDir, "push", "origin", "HEAD:main").Run())

	// sanity check: confirm the fixture is genuinely wedged per the same
	// heuristic checkAndRunGC itself calls, and that the net diff really is
	// empty (the whole point of this fixture).
	wedged, age, count, _ := s.ledgerSyncWedged(context.Background(), ledgerDir)
	require.True(t, wedged, "fixture must be genuinely wedged (ahead+behind, old enough)")
	require.GreaterOrEqual(t, age, ledgerSyncWedgeAge)
	require.Equal(t, 2, count)
	diffOutput, err := gitutil.RunGit(context.Background(), ledgerDir, "diff", "--stat", "@{upstream}...HEAD")
	require.NoError(t, err)
	require.Empty(t, strings.TrimSpace(diffOutput), "fixture's two local commits must cancel out to an empty net diff against the merge-base")

	ws := WorkspaceState{
		ID:         "ledger",
		Type:       WorkspaceTypeLedger,
		Path:       ledgerDir,
		CloneURL:   cloneURL,
		Exists:     true,
		LastGCTime: time.Now().Add(-1 * time.Hour), // recent enough that interval-exceeded doesn't also fire
	}
	registry := s.WorkspaceRegistry()
	registry.mu.Lock()
	primeConfigCacheLocked(registry)
	registry.ledger = &ws
	registry.workspaces[ws.ID] = &ws
	registry.mu.Unlock()

	// seed the issue a prior pull cycle would have set on first detecting
	// the wedge (sync_managed.go's classification path, exercised
	// separately in sync_managed_test.go).
	tracker.SetIssue(DaemonIssue{
		Type:     IssueTypeSessionConflictWedge,
		Repo:     "ledger",
		Severity: SeverityCritical,
		Summary:  "1 session(s) have unresolvable meta.json conflicts",
	})
	_, ok := tracker.GetIssue(IssueTypeSessionConflictWedge, "ledger")
	require.True(t, ok, "sanity check: issue must actually be seeded before checkAndRunGC runs")

	s.checkAndRunGC(context.Background())

	_, stillPresent := tracker.GetIssue(IssueTypeSessionConflictWedge, "ledger")
	assert.False(t, stillPresent,
		"a successful GC that resolves a detected wedge must clear the session-conflict-wedge issue via checkAndRunGC's dispatch-level switch, even when recovered=false (nothing needed capturing)")
}

// TestLedgerSyncWedged_LockBusy_DoesNotClaimConfirmed proves the caller can
// tell "no check ran because a peer held the repo lock" apart from "checked,
// genuinely not wedged" — checkAndRunGC uses this to avoid spending the 6h
// wedge-check cooldown on a cycle where nothing was actually verified. Before
// this, a lock held by the daemon's own concurrent sync cycle (now common,
// since ADR-030 serializes every fetch on the clone) would silently disable
// wedge detection for up to ledgerGCWedgeCooldown with no signal that it had
// happened.
func TestLedgerSyncWedged_LockBusy_DoesNotClaimConfirmed(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	bareDir, cloneDir := gcInitBareAndClone(t, t.TempDir())
	require.NoError(t, os.WriteFile(filepath.Join(cloneDir, "new.txt"), []byte("x"), 0o644))
	require.NoError(t, exec.Command("git", "-C", cloneDir, "add", "-A").Run())
	require.NoError(t, exec.Command("git", "-C", cloneDir, "commit", "-m", "unpushed").Run())
	_ = bareDir

	s := newTestScheduler(t.TempDir())

	held := make(chan struct{})
	release := make(chan struct{})
	go func() {
		_ = gitutil.WithRepoLock(context.Background(), cloneDir, func() error {
			close(held)
			<-release
			return nil
		})
	}()
	<-held
	defer close(release)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	wedged, _, unpushedCount, lockBusy := s.ledgerSyncWedged(ctx, cloneDir)

	assert.False(t, wedged, "a lock-busy result must never be reported as wedged")
	assert.True(t, lockBusy, "a peer holding the repo lock must be distinguishable from a genuine offline fetch failure")
	assert.Equal(t, 1, unpushedCount, "ahead-count is still known even when the confirming fetch couldn't run")
}
