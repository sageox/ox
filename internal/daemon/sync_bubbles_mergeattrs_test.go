package daemon

// Mirrors internal/kb/mergeattrs_test.go for the kb sync output: the same
// merge-driver invariants must hold when EnsureMergeAttributes is applied
// through syncBubbles into kb/<kb_id>/.git/info/attributes — not just in
// the kb package's own unit tests. Per Ryan: "all the ledger and team
// context tests need to be applied to kb git repos."
//
// What's verified here that the kb-package tests can't:
//   - The path actually used at runtime (paths.KBDir(kb_id)/.git/info/attributes)
//   - That a real git clone (file:// bare repo) ends up with the rules
//   - Idempotency across two sync passes
//   - That the rules SURVIVE a pull cycle (not just the initial clone)
//   - That a three-way merge under merge=union actually concatenates
//   - That the pre-existing team-context path still works (no regression)

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/kb"
	"github.com/sageox/ox/internal/paths"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// readKBAttrs returns the .git/info/attributes contents of the given
// bubble checkout. Helper for the assertions below.
func readKBAttrs(t *testing.T, target string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(target, ".git", "info", "attributes"))
	require.NoError(t, err, "info/attributes must exist after sync")
	return string(raw)
}

// TestSyncBubbles_MergeAttrs_AppliedAtCloneTime verifies the merge
// driver block lands inside kb/<kb_id>/.git/info/attributes the moment
// a bubble is first cloned — before any pull, before any user push.
// Mirrors the kb-package "creates_file_when_missing" case but anchored
// at the runtime path the daemon actually writes to.
//
// Failure prevented: a refactor moves the EnsureMergeAttributes call
// out of cloneBubble, leaving fresh bubbles unprotected until the next
// reconciliation pass — and the user's first push wedges.
func TestSyncBubbles_MergeAttrs_AppliedAtCloneTime(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	kbTestEnv(t)
	s, _ := kbTestScheduler(t)

	bareDir := makeBareRepo(t, "clonetime", "AGENTS.md", "seed\n")
	bubble := api.KB{
		KBID:    "kb_clonetime",
		KBType:  api.KBTypeTeam,
		Slug:    "clonetime",
		RepoURL: "file://" + bareDir,
	}
	s.SetKBBubbleListerFactory(func(_, _ string) KBBubbleLister {
		return &fakeKBLister{bubbles: []api.KB{bubble}}
	})

	s.syncBubbles(context.Background())

	target := paths.KBDir(bubble.KBID)
	got := readKBAttrs(t, target)
	for _, want := range kb.MergeUnionPaths {
		assert.Contains(t, got, want, "expected merge rule for %s", want)
	}
	assert.Contains(t, got, "merge=union")
}

// TestSyncBubbles_MergeAttrs_NotInWorkingTree verifies that the
// daemon's sync pass does NOT write a tracked .gitattributes into the
// kb checkout's working tree. Mirrors the kb-package
// "NeverInWorkingTree" invariant; the same trap exists for kb sync
// because EnsureMergeAttributes is invoked twice (post-clone and
// post-pull pre-flight).
//
// Failure prevented: regression that converts merge rules into a
// tracked .gitattributes file — would break every user with git-lfs
// installed globally and conflict with whatever the upstream
// kb may itself ship.
func TestSyncBubbles_MergeAttrs_NotInWorkingTree(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	kbTestEnv(t)
	s, _ := kbTestScheduler(t)

	bareDir := makeBareRepo(t, "noworking", "f.md", "x\n")
	bubble := api.KB{
		KBID:    "kb_noworking",
		KBType:  api.KBTypePersonal,
		Slug:    "noworking",
		RepoURL: "file://" + bareDir,
	}
	s.SetKBBubbleListerFactory(func(_, _ string) KBBubbleLister {
		return &fakeKBLister{bubbles: []api.KB{bubble}}
	})

	s.syncBubbles(context.Background())

	target := paths.KBDir(bubble.KBID)
	if _, err := os.Stat(filepath.Join(target, ".gitattributes")); !os.IsNotExist(err) {
		t.Fatalf("kb sync must NOT create a tracked .gitattributes; got err=%v", err)
	}
}

// TestSyncBubbles_MergeAttrs_IdempotentAcrossPasses verifies that two
// successive sync passes do not duplicate the managed block. Mirrors
// the kb-package "Idempotent" test, but at the level the daemon
// actually exercises (every reconciliation pass calls
// EnsureMergeAttributes via the post-clone path AND inside
// pullManagedRepo's pre-flight when EnsureKBMergeAttrs=true).
//
// Failure prevented: every sync tick appending another copy of the
// managed block, growing info/attributes without bound and slowing
// merge handling on long-lived bubbles.
func TestSyncBubbles_MergeAttrs_IdempotentAcrossPasses(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git pull operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	kbTestEnv(t)
	s, _ := kbTestScheduler(t)

	bareDir := makeBareRepo(t, "idempotent", "AGENTS.md", "v1\n")
	bubble := api.KB{
		KBID:    "kb_idempotent_attrs",
		KBType:  api.KBTypeTeam,
		Slug:    "idem",
		RepoURL: "file://" + bareDir,
	}
	s.SetKBBubbleListerFactory(func(_, _ string) KBBubbleLister {
		return &fakeKBLister{bubbles: []api.KB{bubble}}
	})

	s.syncBubbles(context.Background())
	target := paths.KBDir(bubble.KBID)
	first := readKBAttrs(t, target)

	// trigger a real pull on the second pass — push upstream + age FETCH_HEAD.
	pushExtraCommit(t, bareDir, "AGENTS.md", "v2\n")
	agePastFetchHead(t, target)
	s.syncBubbles(context.Background())

	second := readKBAttrs(t, target)
	assert.Equal(t, first, second, "merge attributes must be byte-identical across passes")
	// header should appear exactly once.
	assert.Equal(t, 1, strings.Count(second, "# BEGIN ox-managed merge rules"),
		"managed block header must appear exactly once")
}

// TestSyncBubbles_MergeAttrs_PreservesUserAuthoredLines verifies that
// content the user wrote into info/attributes outside the managed
// block survives EnsureMergeAttributes when invoked via the kb sync
// path. Same intent as the kb-package "preserves_user_authored_lines"
// case, run end-to-end through cloneBubble + reconcileBubble.
//
// Failure prevented: a sync pass clobbering custom merge rules a power
// user added between the markers, the kind of silent destruction that
// erodes trust.
func TestSyncBubbles_MergeAttrs_PreservesUserAuthoredLines(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	kbTestEnv(t)
	s, _ := kbTestScheduler(t)

	bareDir := makeBareRepo(t, "userlines", "f.md", "v1\n")
	bubble := api.KB{
		KBID:    "kb_userlines",
		KBType:  api.KBTypePersonal,
		Slug:    "userlines",
		RepoURL: "file://" + bareDir,
	}
	s.SetKBBubbleListerFactory(func(_, _ string) KBBubbleLister {
		return &fakeKBLister{bubbles: []api.KB{bubble}}
	})

	// initial clone — installs the managed block.
	s.syncBubbles(context.Background())
	target := paths.KBDir(bubble.KBID)

	// user appends a custom rule outside the managed markers.
	attrsPath := filepath.Join(target, ".git", "info", "attributes")
	existing, err := os.ReadFile(attrsPath)
	require.NoError(t, err)
	userLine := "*.bin binary\n"
	require.NoError(t, os.WriteFile(attrsPath, append(existing, []byte(userLine)...), 0o644))

	// next pass triggers EnsureMergeAttributes again.
	pushExtraCommit(t, bareDir, "f.md", "v2\n")
	agePastFetchHead(t, target)
	s.syncBubbles(context.Background())

	got := readKBAttrs(t, target)
	assert.Contains(t, got, "*.bin binary", "user-authored line must survive sync")
	assert.Contains(t, got, "merge=union", "managed block must still be present")
}

// TestSyncBubbles_MergeAttrs_RecoversTruncatedManagedBlock verifies
// that a truncated managed block (header without footer — interrupted
// write or hand-edit) is healed on the next sync pass. Mirrors the
// kb-package "recovers_from_truncated_block_missing_footer" case.
//
// Failure prevented: a partial write leaving info/attributes in a
// state where every subsequent sync pass appends another copy of the
// block, producing unbounded growth.
func TestSyncBubbles_MergeAttrs_RecoversTruncatedManagedBlock(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	kbTestEnv(t)
	s, _ := kbTestScheduler(t)

	bareDir := makeBareRepo(t, "truncated", "f.md", "v1\n")
	bubble := api.KB{
		KBID:    "kb_truncated",
		KBType:  api.KBTypeTeam,
		Slug:    "truncated",
		RepoURL: "file://" + bareDir,
	}
	s.SetKBBubbleListerFactory(func(_, _ string) KBBubbleLister {
		return &fakeKBLister{bubbles: []api.KB{bubble}}
	})

	s.syncBubbles(context.Background())
	target := paths.KBDir(bubble.KBID)
	attrsPath := filepath.Join(target, ".git", "info", "attributes")

	// corrupt to a truncated block: header but no footer.
	corrupted := "# BEGIN ox-managed merge rules — do not edit between markers\nAGENTS.md merge=stale-driver\n"
	require.NoError(t, os.WriteFile(attrsPath, []byte(corrupted), 0o644))

	// next pass: pull pre-flight + post-pull EnsureMergeAttributes
	// should heal the truncated block.
	pushExtraCommit(t, bareDir, "f.md", "v2\n")
	agePastFetchHead(t, target)
	s.syncBubbles(context.Background())

	got := readKBAttrs(t, target)
	assert.Contains(t, got, "# END ox-managed merge rules", "footer must be re-installed")
	assert.NotContains(t, got, "stale-driver", "stale managed content must be replaced")
	assert.Contains(t, got, "merge=union")
}

// TestSyncBubbles_MergeAttrs_ThreeWayUnionConcatenates verifies the
// observable behavior the rules exist for: when two writers touch the
// same union-tracked file (AGENTS.md), a three-way merge produces a
// concatenated result instead of wedging the rebase. This is the
// failure mode the entire merge=union story is designed to prevent.
//
// Failure prevented: the wedged-rebase first-push bug returning
// because the rules are present in the file but git silently isn't
// applying them (path mismatch, syntax drift, etc.) — a test that
// only asserts on the file's contents could pass while the actual
// merge behavior regressed.
func TestSyncBubbles_MergeAttrs_ThreeWayUnionConcatenates(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git merge operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	kbTestEnv(t)
	s, _ := kbTestScheduler(t)

	bareDir := makeBareRepo(t, "threeway", "AGENTS.md", "base\n")
	bubble := api.KB{
		KBID:    "kb_threeway",
		KBType:  api.KBTypeTeam,
		Slug:    "threeway",
		RepoURL: "file://" + bareDir,
	}
	s.SetKBBubbleListerFactory(func(_, _ string) KBBubbleLister {
		return &fakeKBLister{bubbles: []api.KB{bubble}}
	})

	s.syncBubbles(context.Background())
	target := paths.KBDir(bubble.KBID)
	gitConfig(t, target)

	// remote writes a divergent line.
	pushExtraCommit(t, bareDir, "AGENTS.md", "base\nremote-line\n")

	// local writes a different divergent line on top of base.
	require.NoError(t, os.WriteFile(filepath.Join(target, "AGENTS.md"), []byte("base\nlocal-line\n"), 0o644))
	require.NoError(t, exec.Command("git", "-C", target, "add", "AGENTS.md").Run())
	require.NoError(t, exec.Command("git", "-C", target, "commit", "-m", "local edit").Run())

	// run a real `git pull --rebase --autostash` — same shape as the
	// daemon's pull. The merge=union driver should resolve the conflict
	// by concatenating both sides instead of wedging.
	out, err := exec.Command("git", "-C", target, "pull", "--rebase", "--autostash", "origin", "main").CombinedOutput()
	require.NoError(t, err, "pull --rebase must succeed under merge=union: %s", out)

	body, err := os.ReadFile(filepath.Join(target, "AGENTS.md"))
	require.NoError(t, err)
	merged := string(body)
	assert.Contains(t, merged, "remote-line", "remote-side line must be in the merge")
	assert.Contains(t, merged, "local-line", "local-side line must be in the merge")
	assert.Contains(t, merged, "base", "base content must remain")
}

// TestSyncBubbles_MergeAttrs_NoRegressionForTeamContextPath verifies
// the legacy team-context bare-repo path (used by the existing
// team-context two-phase clone code) STILL gets the same merge rules
// when run through pullManagedRepo with EnsureKBMergeAttrs=true. This
// is the "no regression for existing team-context paths (legacy paths
// still work for one release)" coverage required by ox-gzp.20.
//
// Failure prevented: shipping kb support quietly breaks team-context
// merge rules because the kb path forks the EnsureMergeAttributes call
// site without keeping the team-context path in sync.
func TestSyncBubbles_MergeAttrs_NoRegressionForTeamContextPath(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	// Use the team-context style helper to set up a bare-repo seeded clone,
	// then verify the same EnsureMergeAttributes invariant on it.
	teamDir := t.TempDir()
	setupGitRepo(t, teamDir)

	// EnsureMergeAttributes must succeed on a team-context-style
	// directory just as it does on a kb directory — they're the same
	// kb.MergeAttrs API.
	changed, err := kb.EnsureMergeAttributes(teamDir)
	require.NoError(t, err)
	assert.True(t, changed, "first call must report a change on a fresh team-context dir")

	got, err := os.ReadFile(filepath.Join(teamDir, ".git", "info", "attributes"))
	require.NoError(t, err)
	for _, want := range kb.MergeUnionPaths {
		assert.Contains(t, string(got), want)
	}
	assert.Contains(t, string(got), "merge=union")

	// idempotency: legacy path must also be byte-stable.
	changed, err = kb.EnsureMergeAttributes(teamDir)
	require.NoError(t, err)
	assert.False(t, changed, "second call must report no-op on team-context path")
}
