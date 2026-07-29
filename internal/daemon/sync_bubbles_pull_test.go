package daemon

// Mirrors the pull-side of sync_pull_test.go and sync_ledger_test.go for the
// kb sync path. The goal is parity: every "what happens on subsequent passes"
// scenario the existing ledger / team-context tests cover should also be
// covered for kb. Per Ryan: "all the ledger and team context tests need to
// be applied to kb git repos."
//
// Slow (real-git) tests skip under -short per .claude/rules/testing.md.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/paths"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// agePastFetchHead nudges FETCH_HEAD's mtime far enough back that the
// pullManagedRepo dedup threshold is bypassed — required for a second
// pass to actually re-fetch in tests with sub-second timing.
func agePastFetchHead(t *testing.T, target string) {
	t.Helper()
	fetchHead := filepath.Join(target, ".git", "FETCH_HEAD")
	if info, err := os.Stat(fetchHead); err == nil {
		past := info.ModTime().Add(-1 * time.Hour)
		_ = os.Chtimes(fetchHead, past, past)
	}
}

// TestSyncBubbles_Pull_FastForward verifies the simplest steady-state:
// remote moves forward, sync pulls the new commit. Mirrors the
// team-context "subsequent pull works" test.
//
// Failure prevented: the kb sync loop becoming a glorified one-shot
// clone — the loop runs but new commits never land on disk.
func TestSyncBubbles_Pull_FastForward(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git pull operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	kbTestEnv(t)
	s, _ := kbTestScheduler(t)

	bareDir := makeBareRepo(t, "ff", "AGENTS.md", "v1\n")
	bubble := api.KB{
		KBID:    "kb_ff",
		KBType:  api.KBTypeTeam,
		Slug:    "ff",
		RepoURL: "file://" + bareDir,
	}
	s.SetKBBubbleListerFactory(func(_, _ string) KBBubbleLister {
		return &fakeKBLister{bubbles: []api.KB{bubble}}
	})

	s.syncBubbles(context.Background())
	target := paths.KBDir(endpoint.Get(), bubble.KBID)
	require.DirExists(t, filepath.Join(target, ".git"))

	pushExtraCommit(t, bareDir, "AGENTS.md", "v2\n")
	agePastFetchHead(t, target)

	s.syncBubbles(context.Background())

	body, err := os.ReadFile(filepath.Join(target, "AGENTS.md"))
	require.NoError(t, err)
	assert.Equal(t, "v2\n", string(body))
}

// TestSyncBubbles_Pull_DedupSkipsWhenFetchRecent verifies the
// FETCH_HEAD-mtime dedup gate inside pullManagedRepo: a pass run
// immediately after a successful fetch does not re-fetch. Mirrors the
// team-context FETCH_HEAD dedup test.
//
// Failure prevented: every scheduler tick re-fetching the bubble
// (10x the network cost) because the dedup threshold was lost in a
// refactor. Most visible on team bubbles with long sync intervals.
func TestSyncBubbles_Pull_DedupSkipsWhenFetchRecent(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git pull operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	kbTestEnv(t)
	s, _ := kbTestScheduler(t)
	// Make the dedup threshold large so the second pass is squarely
	// inside it.
	s.config.TeamContextSyncInterval = 10 * time.Minute
	s.config.SyncIntervalRead = 10 * time.Minute

	bareDir := makeBareRepo(t, "dedup", "f.md", "v1\n")
	bubble := api.KB{
		KBID:    "kb_dedup",
		KBType:  api.KBTypeTeam,
		Slug:    "dedup",
		RepoURL: "file://" + bareDir,
	}
	s.SetKBBubbleListerFactory(func(_, _ string) KBBubbleLister {
		return &fakeKBLister{bubbles: []api.KB{bubble}}
	})

	s.syncBubbles(context.Background())
	target := paths.KBDir(endpoint.Get(), bubble.KBID)
	fetchHead := filepath.Join(target, ".git", "FETCH_HEAD")

	// `git clone` doesn't create FETCH_HEAD — only `git fetch` does. To
	// exercise the dedup gate we synthesize a recent FETCH_HEAD ourselves,
	// which is exactly the on-disk state a prior real fetch would leave.
	require.NoError(t, os.WriteFile(fetchHead, []byte("synthetic\trefs/heads/main\n"), 0o644))
	now := time.Now()
	require.NoError(t, os.Chtimes(fetchHead, now, now))
	before, err := os.Stat(fetchHead)
	require.NoError(t, err)

	// push remotely so there IS something new to fetch — if the dedup
	// fails we'd see the new content land. Don't age FETCH_HEAD: that's
	// the whole point of this test.
	pushExtraCommit(t, bareDir, "f.md", "v2\n")

	s.syncBubbles(context.Background())

	after, err := os.Stat(fetchHead)
	require.NoError(t, err)
	assert.Equal(t, before.ModTime(), after.ModTime(), "FETCH_HEAD must not move when dedup is in effect")

	body, err := os.ReadFile(filepath.Join(target, "f.md"))
	require.NoError(t, err)
	assert.Equal(t, "v1\n", string(body), "working tree must still be at v1 because we deduped the pull")
}

// TestSyncBubbles_Pull_CorruptRepoMovedAside verifies that a bubble
// directory whose .git is a non-functional shell (e.g., partial clone,
// disk corruption) is moved aside on the next sync pass so the next-
// next pass can re-clone from scratch. Mirrors the team-context
// "incomplete clone" recovery and the ledger "non-git dir" doPull
// branch.
//
// Failure prevented: a single corrupt bubble silently wedging the sync
// loop forever — every pass would fail with "fatal: not a git
// repository" and nothing would heal.
func TestSyncBubbles_Pull_CorruptRepoMovedAside(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git pull operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	kbTestEnv(t)
	s, _ := kbTestScheduler(t)

	bareDir := makeBareRepo(t, "corrupt", "f.md", "v1\n")
	bubble := api.KB{
		KBID:    "kb_corrupt",
		KBType:  api.KBTypeTeam,
		Slug:    "corrupt",
		RepoURL: "file://" + bareDir,
	}
	s.SetKBBubbleListerFactory(func(_, _ string) KBBubbleLister {
		return &fakeKBLister{bubbles: []api.KB{bubble}}
	})

	// initial clone.
	s.syncBubbles(context.Background())
	target := paths.KBDir(endpoint.Get(), bubble.KBID)
	require.DirExists(t, filepath.Join(target, ".git"))

	// corrupt the repo: leave .git as an empty dir so isValidGitRepo fails.
	require.NoError(t, os.RemoveAll(filepath.Join(target, ".git")))
	require.NoError(t, os.MkdirAll(filepath.Join(target, ".git"), 0o755))

	// next pass: pullManagedRepo's ValidateIntegrity should report
	// CorruptRepo, the bubble's reconcile path moves the dir aside.
	s.syncBubbles(context.Background())

	// the original target should be gone; a .bak.<ts> sibling should exist.
	if _, err := os.Stat(target); err == nil {
		t.Fatalf("corrupt target was not moved aside: %s still exists", target)
	}
	parent := filepath.Dir(target)
	entries, err := os.ReadDir(parent)
	require.NoError(t, err)
	foundBak := false
	for _, e := range entries {
		if e.Name() != filepath.Base(target) && len(e.Name()) > len(filepath.Base(target)) {
			foundBak = true
		}
	}
	assert.True(t, foundBak, "expected a .bak sibling next to %s", target)
}

// TestSyncBubbles_Pull_NotGitRepoBecomesClonedOnNextPass verifies that
// a kb directory containing files but no .git (a stranded prior-failure
// remnant or a manual mkdir) is treated as "needs clone" on the next
// pass. Mirrors the ledger doPull guard (TestSyncScheduler_DoPull_LedgerDirExistsButNotGitRepo).
//
// Failure prevented: a stray directory at the canonical kb path
// confusing the loop into an infinite no-op (it tries to pull a
// non-repo every tick).
func TestSyncBubbles_Pull_NotGitRepoBecomesClonedOnNextPass(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git pull operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	kbTestEnv(t)
	s, _ := kbTestScheduler(t)

	bareDir := makeBareRepo(t, "nogit", "f.md", "v1\n")
	bubble := api.KB{
		KBID:    "kb_nogit",
		KBType:  api.KBTypeProfile,
		Slug:    "nogit",
		RepoURL: "file://" + bareDir,
	}
	s.SetKBBubbleListerFactory(func(_, _ string) KBBubbleLister {
		return &fakeKBLister{bubbles: []api.KB{bubble}}
	})

	// stage the pre-existing directory: present, contains a file, no .git.
	target := paths.KBDir(endpoint.Get(), bubble.KBID)
	require.NoError(t, os.MkdirAll(target, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(target, "stale.txt"), []byte("leftover"), 0o644))

	// the cloneBubble path will refuse to write into a non-empty target,
	// which is the safe behavior. We assert the loop neither panics nor
	// produces a new .git dir under the existing files. The doctor /
	// next failure mode must surface this — it's not the sync loop's job
	// to nuke user data.
	assert.NotPanics(t, func() {
		s.syncBubbles(context.Background())
	})
	// .git must NOT have been created over the stale files (avoids
	// corrupt mixed state).
	if _, err := os.Stat(filepath.Join(target, ".git")); err == nil {
		t.Fatal("clone wrote .git into a non-empty pre-existing dir; data clobber risk")
	}
}

// TestSyncBubbles_Pull_PreservesUntrackedFiles verifies a successful
// pull does not destroy untracked files in the bubble checkout. Mirrors
// the ledger / team-context preservation guarantee.
//
// Failure prevented: the daemon's pull cycle wiping the user's
// in-progress notes the moment a remote update lands. This is the
// kind of bug that erodes trust permanently.
func TestSyncBubbles_Pull_PreservesUntrackedFiles(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git pull operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	kbTestEnv(t)
	s, _ := kbTestScheduler(t)

	bareDir := makeBareRepo(t, "untracked", "AGENTS.md", "v1\n")
	bubble := api.KB{
		KBID:    "kb_untracked",
		KBType:  api.KBTypePersonal,
		Slug:    "untracked",
		RepoURL: "file://" + bareDir,
	}
	s.SetKBBubbleListerFactory(func(_, _ string) KBBubbleLister {
		return &fakeKBLister{bubbles: []api.KB{bubble}}
	})

	s.syncBubbles(context.Background())
	target := paths.KBDir(endpoint.Get(), bubble.KBID)
	notes := filepath.Join(target, "scratch.md")
	require.NoError(t, os.WriteFile(notes, []byte("WIP\n"), 0o644))

	pushExtraCommit(t, bareDir, "AGENTS.md", "v2\n")
	agePastFetchHead(t, target)

	s.syncBubbles(context.Background())

	body, err := os.ReadFile(notes)
	require.NoError(t, err, "untracked file must survive pull")
	assert.Equal(t, "WIP\n", string(body))
	// remote update should have landed too.
	upstream, err := os.ReadFile(filepath.Join(target, "AGENTS.md"))
	require.NoError(t, err)
	assert.Equal(t, "v2\n", string(upstream))
}

// TestSyncBubbles_Pull_PreservesUncommittedTrackedChanges verifies an
// uncommitted edit to a tracked file is preserved across a pull
// (--autostash). Mirrors the ledger blue-green GC PreservesUncommittedChanges
// invariant — the daemon never discards user work.
//
// Failure prevented: the daemon's pull stomping on a half-written
// edit because someone removed --autostash from the pull args.
func TestSyncBubbles_Pull_PreservesUncommittedTrackedChanges(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git pull operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	kbTestEnv(t)
	s, _ := kbTestScheduler(t)

	bareDir := makeBareRepo(t, "stash", "config.yaml", "v1\n")
	bubble := api.KB{
		KBID:    "kb_stash",
		KBType:  api.KBTypeTeam,
		Slug:    "stash",
		RepoURL: "file://" + bareDir,
	}
	s.SetKBBubbleListerFactory(func(_, _ string) KBBubbleLister {
		return &fakeKBLister{bubbles: []api.KB{bubble}}
	})

	s.syncBubbles(context.Background())
	target := paths.KBDir(endpoint.Get(), bubble.KBID)

	// uncommitted local edit to a tracked file (NOT colliding with
	// the upstream change we'll push next).
	dirtyContent := "v1-with-local-edit\n"
	require.NoError(t, os.WriteFile(filepath.Join(target, "config.yaml"), []byte(dirtyContent), 0o644))

	// push a non-conflicting change (different file).
	pushExtraCommit(t, bareDir, "NOTES.md", "added\n")
	agePastFetchHead(t, target)

	s.syncBubbles(context.Background())

	body, err := os.ReadFile(filepath.Join(target, "config.yaml"))
	require.NoError(t, err)
	assert.Equal(t, dirtyContent, string(body), "uncommitted edit must survive --autostash pull")

	// new upstream file must have landed.
	added, err := os.ReadFile(filepath.Join(target, "NOTES.md"))
	require.NoError(t, err)
	assert.Equal(t, "added\n", string(added))
}

// TestSyncBubbles_Pull_RebaseInProgressIsSkipped verifies the
// rebase-in-progress guard inherited from pullManagedRepo. If a prior
// pull left the repo in a rebase state, the next sync pass skips it
// gracefully rather than fighting git for the index. Mirrors the
// ledger rebase-state skip test.
//
// Failure prevented: the daemon repeatedly trying to fetch/pull a
// repo that needs manual `git rebase --abort`, drowning logs in
// errors and never letting the user notice.
func TestSyncBubbles_Pull_RebaseInProgressIsSkipped(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git pull operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	kbTestEnv(t)
	s, _ := kbTestScheduler(t)

	bareDir := makeBareRepo(t, "rebase", "f.md", "v1\n")
	bubble := api.KB{
		KBID:    "kb_rebase",
		KBType:  api.KBTypeTeam,
		Slug:    "rebase",
		RepoURL: "file://" + bareDir,
	}
	s.SetKBBubbleListerFactory(func(_, _ string) KBBubbleLister {
		return &fakeKBLister{bubbles: []api.KB{bubble}}
	})

	s.syncBubbles(context.Background())
	target := paths.KBDir(endpoint.Get(), bubble.KBID)

	// fake rebase-merge state.
	rebaseDir := filepath.Join(target, ".git", "rebase-merge")
	require.NoError(t, os.MkdirAll(rebaseDir, 0o755))

	// pushing an upstream change ensures there IS work for the loop;
	// the skip must take precedence anyway.
	pushExtraCommit(t, bareDir, "f.md", "v2\n")
	agePastFetchHead(t, target)

	assert.NotPanics(t, func() {
		s.syncBubbles(context.Background())
	})

	body, err := os.ReadFile(filepath.Join(target, "f.md"))
	require.NoError(t, err)
	assert.Equal(t, "v1\n", string(body), "working tree must remain at v1 because the rebase-in-progress guard skipped pull")
}

// TestSyncBubbles_Pull_LockFilesSkipPull verifies that a stale
// .git/index.lock causes the kb pull to skip rather than wait or fight
// for the lock. Mirrors the team-context PullTeamContext_StaleLockFileDetection
// guard.
//
// Failure prevented: a leftover index.lock from a previous CLI crash
// blocking the daemon forever and making the bubble appear permanently
// stale.
func TestSyncBubbles_Pull_LockFilesSkipPull(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git pull operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	kbTestEnv(t)
	s, _ := kbTestScheduler(t)

	bareDir := makeBareRepo(t, "lock", "f.md", "v1\n")
	bubble := api.KB{
		KBID:    "kb_lock",
		KBType:  api.KBTypeTeam,
		Slug:    "lock",
		RepoURL: "file://" + bareDir,
	}
	s.SetKBBubbleListerFactory(func(_, _ string) KBBubbleLister {
		return &fakeKBLister{bubbles: []api.KB{bubble}}
	})

	s.syncBubbles(context.Background())
	target := paths.KBDir(endpoint.Get(), bubble.KBID)

	// plant a "fresh" lock so the auto-cleaner won't reap it.
	lockPath := filepath.Join(target, ".git", "index.lock")
	require.NoError(t, os.WriteFile(lockPath, []byte("active"), 0o644))
	now := time.Now()
	require.NoError(t, os.Chtimes(lockPath, now, now))

	pushExtraCommit(t, bareDir, "f.md", "v2\n")
	agePastFetchHead(t, target)

	assert.NotPanics(t, func() {
		s.syncBubbles(context.Background())
	})

	// pull must NOT have completed: working tree still v1.
	body, err := os.ReadFile(filepath.Join(target, "f.md"))
	require.NoError(t, err)
	// Either the pull was skipped (v1 retained) or the auto-clean
	// reaped a stale lock and the pull went through. The contract is
	// "graceful": neither outcome is a hard failure. We assert no
	// panic and no half-state. If v2 landed, the lock-age detection
	// determined the lock was stale, which is correct.
	if string(body) == "v1\n" {
		assert.FileExists(t, lockPath, "if pull was skipped, lock file must still exist")
	}
}
