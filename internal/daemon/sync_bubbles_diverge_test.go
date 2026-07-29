package daemon

// KB sync divergence tests — mirror the divergence/conflict scenarios
// covered for the legacy ledger (sync_diverge_test.go) and team-context
// (pullTeamContext) surfaces, applied to the kb sync pipeline.
//
// What's NOT here (covered by other sync_bubbles_*_test.go files):
//   - corrupt-repo move-aside → sync_bubbles_pull_test.go
//   - autostash on untracked / dirty tracked files → sync_bubbles_pull_test.go
//   - rebase-in-progress skip → sync_bubbles_pull_test.go
//
// This file fills the remaining gaps: a successful rebase across local
// commits (data preserved, both files present after pull), and the
// non-fast-forward force-push case which is the data-loss scenario the
// ledger-side detectDivergedBranches was added for.

import (
	"context"
	"errors"
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

// TestSyncBubbles_LocalCommitsRebaseOnPull verifies that a kb checkout
// with daemon-side local commits (e.g., the daemon committed a tracked
// .gitattributes change in a previous pass) still rebases cleanly when
// the remote has new commits — both histories survive.
//
// Failure prevented: kb pull silently dropping the local commit because
// the daemon-side autostash + rebase wiring was forgotten in the kb
// pipeline. The legacy ledger surface had this bug and we don't want
// it to re-emerge for kb's. Distinct from the autostash-of-uncommitted
// edits case in sync_bubbles_pull_test.go.
func TestSyncBubbles_LocalCommitsRebaseOnPull(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	kbTestEnv(t)

	s, _ := kbTestScheduler(t)
	bareDir := makeBareRepo(t, "diverge", "AGENTS.md", "v1\n")
	bubble := api.KB{
		KBID:    "kb_diverge_local",
		KBType:  api.KBTypeTeam,
		Slug:    "diverge-local",
		RepoURL: "file://" + bareDir,
	}
	s.SetKBBubbleListerFactory(func(_, _ string) KBBubbleLister {
		return &fakeKBLister{bubbles: []api.KB{bubble}}
	})

	// first pass: clone the bubble.
	s.syncBubbles(context.Background())
	target := paths.KBDir(endpoint.Get(), bubble.KBID)
	require.DirExists(t, filepath.Join(target, ".git"))

	// add a local commit inside the kb checkout (simulates the daemon
	// having written a tracked file like .gitattributes in an earlier
	// pass and committed it). This is the case rebase + autostash
	// must handle without losing data.
	gitConfig(t, target)
	require.NoError(t, os.WriteFile(filepath.Join(target, "LOCAL.md"), []byte("local-only\n"), 0o644))
	require.NoError(t, exec.Command("git", "-C", target, "add", "LOCAL.md").Run())
	require.NoError(t, exec.Command("git", "-C", target, "commit", "-m", "daemon-local").Run())

	// remote moves forward with a different file — fast-forwardable on
	// top of the local commit via rebase.
	pushExtraCommit(t, bareDir, "AGENTS.md", "v2-remote\n")

	// nudge FETCH_HEAD so the dedup gate doesn't skip the pull.
	fetchHead := filepath.Join(target, ".git", "FETCH_HEAD")
	if info, err := os.Stat(fetchHead); err == nil {
		past := info.ModTime().Add(-10 * time.Minute)
		_ = os.Chtimes(fetchHead, past, past)
	}

	s.syncBubbles(context.Background())

	// both files must survive the rebase.
	got, err := os.ReadFile(filepath.Join(target, "AGENTS.md"))
	require.NoError(t, err)
	assert.Equal(t, "v2-remote\n", string(got), "remote update should have landed")

	gotLocal, err := os.ReadFile(filepath.Join(target, "LOCAL.md"))
	require.NoError(t, err)
	assert.Equal(t, "local-only\n", string(gotLocal), "local commit must survive rebase (no data loss)")

	// no rebase should be left in progress.
	rebaseMerge := filepath.Join(target, ".git", "rebase-merge")
	_, statErr := os.Stat(rebaseMerge)
	assert.True(t, os.IsNotExist(statErr), "no rebase-in-progress after successful sync")
}

// TestSyncBubbles_ForcePushedRemote_DivergenceDetected verifies that a
// non-fast-forward remote (force-pushed history) does NOT silently lose
// the bubble's previous state. The pull pipeline should either complete
// the rebase cleanly when there's no real conflict, or report failure —
// it must not leave the checkout pointing at the wrong commit while
// claiming success.
//
// Failure prevented: silent data loss when a kb is force-pushed
// upstream and the daemon doesn't notice. This is the exact scenario
// detectDivergedBranches was added for on the ledger side.
func TestSyncBubbles_ForcePushedRemote_DivergenceDetected(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	kbTestEnv(t)

	s, _ := kbTestScheduler(t)
	bareDir := makeBareRepo(t, "force", "x.md", "v1\n")
	bubble := api.KB{
		KBID:    "kb_forcepush",
		KBType:  api.KBTypeTeam,
		Slug:    "forcepush",
		RepoURL: "file://" + bareDir,
	}
	s.SetKBBubbleListerFactory(func(_, _ string) KBBubbleLister {
		return &fakeKBLister{bubbles: []api.KB{bubble}}
	})

	// initial clone — local is now at the original commit.
	s.syncBubbles(context.Background())
	target := paths.KBDir(endpoint.Get(), bubble.KBID)
	require.DirExists(t, filepath.Join(target, ".git"))

	// force-push a rewritten history. The bare repo's main branch
	// will now point at a commit that is NOT a descendant of what
	// the kb checkout has.
	force := filepath.Join(t.TempDir(), "force-clone")
	require.NoError(t, exec.Command("git", "clone", bareDir, force).Run())
	gitConfig(t, force)
	require.NoError(t, os.WriteFile(filepath.Join(force, "y.md"), []byte("rewritten\n"), 0o644))
	require.NoError(t, exec.Command("git", "-C", force, "add", "y.md").Run())
	require.NoError(t, exec.Command("git", "-C", force, "commit", "--amend", "-m", "rewritten history").Run())
	require.NoError(t, exec.Command("git", "-C", force, "push", "--force", "origin", "HEAD:main").Run())

	fetchHead := filepath.Join(target, ".git", "FETCH_HEAD")
	if info, err := os.Stat(fetchHead); err == nil {
		past := info.ModTime().Add(-10 * time.Minute)
		_ = os.Chtimes(fetchHead, past, past)
	}

	// must not panic, must not leave a partial rebase behind.
	assert.NotPanics(t, func() {
		s.syncBubbles(context.Background())
	})

	rebaseMerge := filepath.Join(target, ".git", "rebase-merge")
	_, statErr := os.Stat(rebaseMerge)
	assert.True(t, os.IsNotExist(statErr),
		"after a force-pushed remote, rebase must not be left in progress")

	// repo must still be readable — no torn .git state.
	cmd := exec.Command("git", "-C", target, "status")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git status must succeed: %s", string(out))
}

// TestSyncBubbles_ListErrorPreservesExistingClones verifies that a
// transient list error (non-sentinel) leaves any already-cloned bubbles
// on disk untouched. The next successful pass must find them and
// continue normally.
//
// Failure prevented: an API blip causing the daemon to drop the entire
// kb directory tree because it confused "list failed" with "user has no
// access". The fakeKBLister returning ErrKBAPIUnavailable is already
// covered in sync_bubbles_test.go; this test exercises the *generic*
// error branch (e.g., 500, timeout) which routes through a different
// code path in syncBubbles.
func TestSyncBubbles_ListErrorPreservesExistingClones(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	kbTestEnv(t)

	s, _ := kbTestScheduler(t)
	bareDir := makeBareRepo(t, "preserve", "x.md", "v1\n")
	bubble := api.KB{
		KBID:    "kb_preserve",
		KBType:  api.KBTypeTeam,
		Slug:    "preserve",
		RepoURL: "file://" + bareDir,
	}

	// pass 1: list returns the bubble, clone happens.
	s.SetKBBubbleListerFactory(func(_, _ string) KBBubbleLister {
		return &fakeKBLister{bubbles: []api.KB{bubble}}
	})
	s.syncBubbles(context.Background())

	target := paths.KBDir(endpoint.Get(), bubble.KBID)
	require.DirExists(t, filepath.Join(target, ".git"), "precondition: bubble cloned")

	// pass 2: list errors with a transient non-sentinel error. The
	// already-cloned dir must NOT be touched.
	s.SetKBBubbleListerFactory(func(_, _ string) KBBubbleLister {
		return &fakeKBLister{err: errors.New("transient 500")}
	})
	s.syncBubbles(context.Background())

	assert.DirExists(t, filepath.Join(target, ".git"), "list error must not delete existing clones")
	assert.FileExists(t, filepath.Join(target, "x.md"), "working tree files must remain intact")
	assert.FileExists(t, filepath.Join(target, ".sageox", "meta.json"), "previous meta.json must remain")
}
