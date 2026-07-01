package daemon

import (
	"context"
	"io"
	"log/slog"
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

// --- Pre-existing rebase recovery ---
//
// These pin the behavior of recoverPreexistingRebase, the fix for the wedge
// where the daemon skipped a pre-existing rebase forever (sync_managed.go:159
// pre-fix). The original bug stranded every new session behind the wedge and
// spun the sync loop. A unit test on RebaseAge alone would pass even if the
// recovery were never wired in — these call the recovery decision directly so
// a regression to "bare skip" fails the build.

// makeWedgedRebase creates a real git repo stuck mid-rebase with a conflict,
// so .git/rebase-merge exists and IsRebaseInProgress reports true. No remote
// needed — the abort path operates purely locally.
func makeWedgedRebase(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()

	git := func(args ...string) {
		out, err := runGitOut(t, repo, args...)
		// Only the intentional conflicting rebase may exit non-zero; surface
		// any other setup failure (e.g. unsupported --initial-branch) so a
		// broken fixture fails loudly instead of masquerading as a wedge.
		if err != nil && args[0] != "rebase" {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
	}
	write := func(content string) {
		require.NoError(t, os.WriteFile(filepath.Join(repo, "file.txt"), []byte(content), 0o644))
	}

	git("init", "--initial-branch=main")
	write("base\n")
	git("add", "file.txt")
	git("commit", "-m", "base")
	git("checkout", "-b", "feature")
	write("feature change\n")
	git("add", "file.txt")
	git("commit", "-m", "feature")
	git("checkout", "main")
	write("main change\n")
	git("add", "file.txt")
	git("commit", "-m", "main")
	git("checkout", "feature")
	git("rebase", "main") // conflicts on file.txt → leaves the repo wedged

	require.True(t, gitutil.IsRebaseInProgress(repo), "setup should leave a wedged rebase")
	return repo
}

// backdateRebaseDir ages the in-progress rebase so RebaseAge reports it stale.
func backdateRebaseDir(t *testing.T, repo string, age time.Duration) {
	t.Helper()
	old := time.Now().Add(-age)
	touched := false
	for _, d := range []string{"rebase-merge", "rebase-apply"} {
		p := filepath.Join(repo, ".git", d)
		if _, err := os.Stat(p); err == nil {
			require.NoError(t, os.Chtimes(p, old, old))
			touched = true
		}
	}
	require.True(t, touched, "expected a rebase dir to backdate")
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// gitEnv is the hermetic env every git subprocess in these tests runs under:
// no system/global config, fixed identity, signing off.
func gitEnv() []string {
	return append(os.Environ(), // safe: hermetic env for git (not ox) subprocesses; ox is never spawned here
		"GIT_CONFIG_NOSYSTEM=1", "HOME="+os.TempDir(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test.com",
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=commit.gpgsign", "GIT_CONFIG_VALUE_0=false",
	)
}

// runGitOut runs git in dir, returning combined output and error so callers can
// assert on either the result or the failure.
func runGitOut(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("git", args...) // safe: git in a temp dir, not the ox CLI
	cmd.Dir = dir
	cmd.Env = gitEnv()
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// setupRemoteWithStuckRebase reproduces the production ENVIRONMENT: a clone
// with a local-ahead commit, a remote that advanced with a non-conflicting
// commit, and a rebase that stopped partway and was never continued (here via
// a failing `-x` exec, the no-conflict analog of the real "stopped at edit"
// wedge). Returns the clone path. After recovery, a normal pull must reconcile
// cleanly — proving the daemon makes PROGRESS, not just that it un-wedges.
func setupRemoteWithStuckRebase(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	bare := filepath.Join(root, "remote.git")
	seed := filepath.Join(root, "seed")
	clone := filepath.Join(root, "clone")

	mustGit := func(dir string, args ...string) {
		if out, err := runGitOut(t, dir, args...); err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
	}
	write := func(dir, name, content string) {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644))
	}

	mustGit(root, "init", "--bare", "--initial-branch=main", bare)

	// seed the remote with a base commit
	mustGit(root, "clone", bare, seed)
	write(seed, "base.txt", "base\n")
	mustGit(seed, "add", "base.txt")
	mustGit(seed, "commit", "-m", "base")
	mustGit(seed, "push", "origin", "main")

	// the clone the daemon manages: add a local-ahead commit (not pushed)
	mustGit(root, "clone", bare, clone)
	write(clone, "local.txt", "local work\n")
	mustGit(clone, "add", "local.txt")
	mustGit(clone, "commit", "-m", "local change")

	// remote advances with a NON-conflicting commit (different file)
	write(seed, "remote.txt", "remote work\n")
	mustGit(seed, "add", "remote.txt")
	mustGit(seed, "commit", "-m", "remote change")
	mustGit(seed, "push", "origin", "main")

	// leave the clone wedged in a rebase that stopped and was never continued
	// (a failing -x exec halts the rebase with a clean tree, no conflict).
	_, err := runGitOut(t, clone, "rebase", "-x", "exit 1", "HEAD~1")
	require.Error(t, err, "the failing -x exec should halt the rebase")
	require.True(t, gitutil.IsRebaseInProgress(clone), "clone should be wedged")
	return clone
}

// TestRecoverPreexistingRebase_NoRebase: a clean repo must not be treated as
// wedged — the caller proceeds to a normal pull.
func TestRecoverPreexistingRebase_NoRebase(t *testing.T) {
	repo := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".git"), 0o755))
	s := &SyncScheduler{}

	stop, res := s.recoverPreexistingRebase(context.Background(), repo, "ledger", discardLogger())

	assert.False(t, stop, "no rebase → caller should fall through to pull")
	assert.False(t, res.Skipped)
	assert.Nil(t, res.Issue)
}

// TestRecoverPreexistingRebase_FreshRebaseLeftAlone: a rebase that is only
// seconds old is almost always the daemon's own in-flight pull --rebase or a
// human mid-operation. It must be skipped, NOT aborted out from under them.
// Failure prevented: aborting an active rebase and corrupting concurrent work.
func TestRecoverPreexistingRebase_FreshRebaseLeftAlone(t *testing.T) {
	if testing.Short() {
		t.Skip("short: spawns git subprocesses to build a real rebase")
	}
	repo := makeWedgedRebase(t) // just-created → fresh
	s := &SyncScheduler{}

	stop, res := s.recoverPreexistingRebase(context.Background(), repo, "ledger", discardLogger())

	assert.True(t, stop, "fresh rebase → stop this cycle")
	assert.True(t, res.Skipped)
	assert.Equal(t, skipReasonRebaseInProgress, res.SkipReason)
	assert.True(t, gitutil.IsRebaseInProgress(repo), "fresh rebase must NOT be aborted")
}

// TestRecoverPreexistingRebase_StaleWedgeRecovered is the core regression for
// the reported bug: a rebase wedged long ago must be auto-aborted so the repo
// can sync again, instead of being skipped forever.
// Failure prevented: a pre-existing wedge deadlocks the daemon and strands
// every new session behind it (the exact production incident).
func TestRecoverPreexistingRebase_StaleWedgeRecovered(t *testing.T) {
	if testing.Short() {
		t.Skip("short: spawns git subprocesses to build a real rebase")
	}
	repo := makeWedgedRebase(t)
	backdateRebaseDir(t, repo, time.Hour) // older than staleRebaseThreshold
	s := &SyncScheduler{}

	stop, res := s.recoverPreexistingRebase(context.Background(), repo, "ledger", discardLogger())

	assert.False(t, stop, "stale wedge recovered → caller falls through to a clean pull")
	assert.Nil(t, res.Issue)
	assert.False(t, gitutil.IsRebaseInProgress(repo), "stale wedge must be aborted/recovered")
}

// makeZombieWedge injects the structurally-incomplete rebase-merge dir (only an
// `autostash` entry, no head-name/orig-head) that a process killed mid-`pull
// --rebase --autostash` leaves behind — the exact production wedge in bd
// ox-j3cl. Distinct from makeWedgedRebase, which drives a REAL conflict and thus
// leaves a COMPLETE, abortable state dir; that fixture never exercised the case
// where `git rebase --abort` ITSELF fails, so the deadlock survived it.
func makeZombieWedge(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	mustGit := func(args ...string) {
		if out, err := runGitOut(t, repo, args...); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	mustGit("init", "--initial-branch=main")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "f.txt"), []byte("base\n"), 0o644))
	mustGit("add", "f.txt")
	mustGit("commit", "-m", "base")

	// a real autostash object, faithful to production (stash created off a
	// dirty tree, then the tree restored clean)
	require.NoError(t, os.WriteFile(filepath.Join(repo, "f.txt"), []byte("dirty\n"), 0o644))
	out, err := runGitOut(t, repo, "stash", "create")
	require.NoError(t, err, "stash create: %s", out)
	stashOID := strings.TrimSpace(out)
	mustGit("checkout", "--", "f.txt")

	stateDir := filepath.Join(repo, ".git", "rebase-merge")
	require.NoError(t, os.MkdirAll(stateDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(stateDir, "autostash"), []byte(stashOID+"\n"), 0o644))
	require.True(t, gitutil.IsRebaseInProgress(repo), "zombie dir should read as rebase-in-progress")
	return repo
}

// TestRecoverPreexistingRebase_ZombieDirRecovered is the regression for bd
// ox-j3cl: the daemon must SELF-HEAL a structurally-incomplete rebase dir that
// `git rebase --abort` cannot clear, instead of surfacing IssueTypeRebaseStuck
// and re-looping forever. Pre-fix, recoverPreexistingRebase called AuditAndAbort
// directly, abort failed on this shape, and the ledger stayed suspended for
// weeks. This drives the actual decision path so a regression to "abort-only"
// fails the build.
func TestRecoverPreexistingRebase_ZombieDirRecovered(t *testing.T) {
	if testing.Short() {
		t.Skip("short: spawns git subprocesses to build a rebase wedge")
	}
	repo := makeZombieWedge(t)
	backdateRebaseDir(t, repo, time.Hour) // older than staleRebaseThreshold
	s := &SyncScheduler{}

	stop, res := s.recoverPreexistingRebase(context.Background(), repo, "ledger", discardLogger())

	assert.False(t, stop, "zombie wedge recovered → caller falls through to a clean pull")
	assert.Nil(t, res.Issue, "must NOT surface IssueTypeRebaseStuck for a recoverable zombie")
	assert.False(t, gitutil.IsRebaseInProgress(repo), "zombie rebase dir must be cleared")
}

// TestRecoverPreexistingRebase_StaleWedge_ApplyBackend covers the same CLASS of
// failure via git's other rebase backend. A wedge can leave `.git/rebase-apply`
// instead of `.git/rebase-merge` (older git, `--apply`, `git am`); recovery
// must handle both, since the production deadlock is "stuck in a persistent
// git state", not "stuck in one specific directory layout".
func TestRecoverPreexistingRebase_StaleWedge_ApplyBackend(t *testing.T) {
	if testing.Short() {
		t.Skip("short: spawns git subprocesses to build a real rebase")
	}
	repo := t.TempDir()
	mustGit := func(args ...string) {
		if out, err := runGitOut(t, repo, args...); err != nil && args[0] != "rebase" {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(c string) { require.NoError(t, os.WriteFile(filepath.Join(repo, "f.txt"), []byte(c), 0o644)) }

	mustGit("init", "--initial-branch=main")
	write("base\n")
	mustGit("add", "f.txt")
	mustGit("commit", "-m", "base")
	mustGit("checkout", "-b", "feature")
	write("feature\n")
	mustGit("add", "f.txt")
	mustGit("commit", "-m", "feature")
	mustGit("checkout", "main")
	write("main\n")
	mustGit("add", "f.txt")
	mustGit("commit", "-m", "main")
	mustGit("checkout", "feature")
	// --apply forces the am-based backend → .git/rebase-apply on conflict
	mustGit("rebase", "--apply", "main")
	require.True(t, gitutil.IsRebaseInProgress(repo), "expected an apply-backend wedge")
	require.DirExists(t, filepath.Join(repo, ".git", "rebase-apply"))

	backdateRebaseDir(t, repo, time.Hour)
	stop, res := (&SyncScheduler{}).recoverPreexistingRebase(context.Background(), repo, "ledger", discardLogger())

	assert.False(t, stop)
	assert.Nil(t, res.Issue)
	assert.False(t, gitutil.IsRebaseInProgress(repo), "apply-backend wedge must also be recovered")
}

// TestRecoverPreexistingRebase_RecoveryRestoresSyncProgress is the ENVIRONMENT
// test: a real remote, a local-ahead commit, a non-conflicting remote advance,
// and a rebase that stopped and was abandoned — the shape of the production
// incident. It asserts the daemon doesn't merely un-wedge but actually CATCHES
// UP: after recovery, a normal pull reconciles and the remote's commit lands.
// Failure prevented: "recovery" that aborts the wedge but leaves the repo
// unable to make forward progress (still stranded, just not mid-rebase).
func TestRecoverPreexistingRebase_RecoveryRestoresSyncProgress(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real bare remote + clone + rebase via git subprocesses")
	}
	clone := setupRemoteWithStuckRebase(t)
	backdateRebaseDir(t, clone, time.Hour)

	// 1. recovery un-wedges the abandoned rebase
	stop, res := (&SyncScheduler{}).recoverPreexistingRebase(context.Background(), clone, "ledger", discardLogger())
	require.False(t, stop, "stale wedge should be recovered, not skipped")
	require.Nil(t, res.Issue)
	require.False(t, gitutil.IsRebaseInProgress(clone))

	// 2. the daemon's normal pull (what pullManagedRepo runs after recovery)
	//    now reconciles cleanly with the advanced remote.
	if out, err := runGitOut(t, clone, "pull", "--rebase", "--autostash", "origin", "main"); err != nil {
		t.Fatalf("post-recovery pull should reconcile, got: %v\n%s", err, out)
	}

	// 3. progress proven: the remote's commit is present AND local work survived,
	//    with no leftover wedge.
	assert.False(t, gitutil.IsRebaseInProgress(clone))
	assert.FileExists(t, filepath.Join(clone, "remote.txt"), "remote commit must have landed")
	assert.FileExists(t, filepath.Join(clone, "local.txt"), "local-ahead work must survive")
}
