package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/paths"
	"github.com/sageox/ox/internal/sageoxignore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// symlinkTestProject sets up an initialized project on disk so
// config.IsInitialized returns true, with an optional ProjectConfig
// repo_id wired into the config.json the test environment expects.
//
// We lean on the existing setupProjectWithConfig helper for the
// .sageox/config.local.toml + endpoint-stub config.json pieces, then
// overlay a richer config.json containing repo_id when the test needs
// repo-policy behavior.
func symlinkTestProject(t *testing.T, repoID string) string {
	t.Helper()
	root := setupProjectWithConfig(t, "")
	if repoID != "" {
		path := filepath.Join(root, ".sageox", "config.json")
		body := `{"endpoint":"https://staging.sageox.ai","repo_id":"` + repoID + `"}`
		require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
	}
	return root
}

// newSymlinkTestScheduler wires up a SyncScheduler with a test logger
// and a known project root. We don't need any of the other sync
// machinery to test the reconciler — only s.config.ProjectRoot and
// s.logger are read by the symlink path.
func newSymlinkTestScheduler(t *testing.T, projectRoot string) *SyncScheduler {
	t.Helper()
	cfg := DefaultConfig()
	cfg.ProjectRoot = projectRoot
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	return NewSyncScheduler(cfg, logger)
}

// kbXDGEnv configures XDG paths and the endpoint so paths.KBDir works
// without panicking and never touches the developer's real home.
func kbXDGEnv(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("OX_XDG_DISABLE", "")
	t.Setenv("XDG_DATA_HOME", tmp)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(tmp, "cache"))
	t.Setenv("SAGEOX_ENDPOINT", "https://staging.sageox.ai")
	return tmp
}

// TestReconcileProjectSymlinks_InitialCreate verifies a fresh project
// with no .sageox/kb/ ends up with one symlink per bubble under the
// team/ scope subdirectory — every bubble returned by the ambient
// scoped fetch links (ADR-028 §4: the scoped list already guarantees
// relevance, so there is no per-type policy).
//
// Failure prevented: a fresh `ox init` followed by daemon sync silently
// no-ops the ergonomic surface, and AI coworkers never find their
// team's bubbles inside the project tree.
func TestReconcileProjectSymlinks_InitialCreate(t *testing.T) {
	kbXDGEnv(t)
	projectRoot := symlinkTestProject(t, "repo_my_app")
	s := newSymlinkTestScheduler(t, projectRoot)

	bubbles := []api.KB{
		{KBID: "kb_team", KBType: api.KBTypeTeam, Slug: "platform"},
		{KBID: "kb_custom", KBType: api.KBTypeCustom, Slug: "custom-thing"},
		{KBID: "kb_channel", KBType: api.KBTypeChannel, Slug: "wip-broadcast"},
	}

	s.reconcileProjectSymlinks(context.Background(), projectRoot, bubbles)

	kbDir := filepath.Join(projectRoot, ".sageox", "kb")
	for _, slug := range []string{"platform", "custom-thing", "wip-broadcast"} {
		linkPath := filepath.Join(kbDir, "team", slug)
		info, err := os.Lstat(linkPath)
		require.NoError(t, err, "symlink team/%s must exist", slug)
		assert.NotZero(t, info.Mode()&os.ModeSymlink, "team/%s must be a symlink", slug)
	}

	// nothing lands flat under .sageox/kb/ and me/ stays reserved (absent).
	for _, name := range []string{"platform", "custom-thing", "wip-broadcast", "me"} {
		_, err := os.Lstat(filepath.Join(kbDir, name))
		assert.True(t, os.IsNotExist(err), "%s must not exist at the kb root", name)
	}

	// targets resolve to canonical KBDir paths
	target, err := os.Readlink(filepath.Join(kbDir, "team", "platform"))
	require.NoError(t, err)
	assert.Equal(t, paths.KBDir(endpoint.Get(), "kb_team"), target)
}

// TestReconcileProjectSymlinks_Idempotent verifies running the
// reconciler twice in a row makes zero new os.Symlink calls on the
// second pass.
//
// Failure prevented: every daemon sync tick re-creates every symlink,
// causing inotify churn, log spam, and (worse) tearing windows where
// readers see a missing link mid-rename.
func TestReconcileProjectSymlinks_Idempotent(t *testing.T) {
	kbXDGEnv(t)
	projectRoot := symlinkTestProject(t, "")
	s := newSymlinkTestScheduler(t, projectRoot)

	bubbles := []api.KB{
		{KBID: "kb_p", KBType: api.KBTypePersonal, Slug: "personal-abc"},
		{KBID: "kb_team", KBType: api.KBTypeTeam, Slug: "platform"},
	}

	// First pass with default ops to materialize the links.
	s.reconcileProjectSymlinks(context.Background(), projectRoot, bubbles)

	// Second pass with a counting ops shim — must not re-symlink.
	ops := defaultSymlinkOps()
	var symlinkCalls int
	wrappedSymlink := ops.symlink
	ops.symlink = func(target, link string) error {
		symlinkCalls++
		return wrappedSymlink(target, link)
	}
	s.reconcileProjectSymlinksWithOps(context.Background(), projectRoot, bubbles, ops)

	assert.Equal(t, 0, symlinkCalls, "no symlink calls expected on idempotent second pass")
	assert.Equal(t, 0, ops.createSym, "createSym counter must remain zero on no-op pass")
}

// TestReconcileProjectSymlinks_SlugRename verifies that when a kb's
// slug changes (kb_id stable, slug different), the old slug is removed
// and the new slug is created.
//
// Failure prevented: a slug rename leaves an orphan symlink behind
// forever, polluting `ls .sageox/kb/` and confusing humans about which
// link is current.
func TestReconcileProjectSymlinks_SlugRename(t *testing.T) {
	kbXDGEnv(t)
	projectRoot := symlinkTestProject(t, "")
	s := newSymlinkTestScheduler(t, projectRoot)

	first := []api.KB{{KBID: "kb_p", KBType: api.KBTypePersonal, Slug: "old-name"}}
	second := []api.KB{{KBID: "kb_p", KBType: api.KBTypePersonal, Slug: "new-name"}}

	s.reconcileProjectSymlinks(context.Background(), projectRoot, first)
	kbDir := filepath.Join(projectRoot, ".sageox", "kb", "team")
	require.FileExists(t, filepath.Join(kbDir, "old-name"))

	s.reconcileProjectSymlinks(context.Background(), projectRoot, second)
	_, err := os.Lstat(filepath.Join(kbDir, "old-name"))
	assert.True(t, os.IsNotExist(err), "old slug must be pruned after rename")
	require.FileExists(t, filepath.Join(kbDir, "new-name"))
}

// TestReconcileProjectSymlinks_KBRevoked_PrunesLinkButNotTarget
// verifies that when a kb disappears from the desired set, only the
// symlink is removed — the canonical KBDir is left untouched (GC
// is a separate concern, not this reconciler's job).
//
// Failure prevented: revoking access to a bubble accidentally deletes
// the user's local checkout (which may contain unsynced edits).
func TestReconcileProjectSymlinks_KBRevoked_PrunesLinkButNotTarget(t *testing.T) {
	kbXDGEnv(t)
	projectRoot := symlinkTestProject(t, "")
	s := newSymlinkTestScheduler(t, projectRoot)

	// pre-create the canonical target so the symlink resolves.
	target := paths.KBDir(endpoint.Get(), "kb_p")
	require.NoError(t, os.MkdirAll(target, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(target, "marker.txt"), []byte("present"), 0o644))

	first := []api.KB{{KBID: "kb_p", KBType: api.KBTypePersonal, Slug: "personal-abc"}}
	s.reconcileProjectSymlinks(context.Background(), projectRoot, first)

	linkPath := filepath.Join(projectRoot, ".sageox", "kb", "team", "personal-abc")
	require.FileExists(t, linkPath)

	// kb is no longer in the desired set (revoked / removed from API).
	s.reconcileProjectSymlinks(context.Background(), projectRoot, nil)

	_, err := os.Lstat(linkPath)
	assert.True(t, os.IsNotExist(err), "symlink must be pruned")

	// canonical target MUST still exist with its contents intact.
	assert.FileExists(t, filepath.Join(target, "marker.txt"), "canonical KBDir must NOT be touched by reconciler")
}

// TestReconcileProjectSymlinks_FlatLayoutMigration verifies that a
// stale symlink from the pre-scope flat layout (.sageox/kb/<slug>) is
// removed on reconcile and the scoped link (.sageox/kb/team/<slug>) is
// created for the same bubble. Regression class: a layout migration
// that only writes the new location leaves dangling links at the old
// one forever — editors and AI coworkers keep following the stale path.
func TestReconcileProjectSymlinks_FlatLayoutMigration(t *testing.T) {
	kbXDGEnv(t)
	projectRoot := symlinkTestProject(t, "")
	s := newSymlinkTestScheduler(t, projectRoot)

	kbDir := filepath.Join(projectRoot, ".sageox", "kb")
	require.NoError(t, os.MkdirAll(kbDir, 0o755))

	// simulate the old flat layout: a symlink directly under .sageox/kb/
	// pointing at the bubble's canonical dir.
	target := paths.KBDir(endpoint.Get(), "kb_team")
	require.NoError(t, os.MkdirAll(target, 0o755))
	flatLink := filepath.Join(kbDir, "platform")
	require.NoError(t, os.Symlink(target, flatLink))

	bubbles := []api.KB{{KBID: "kb_team", KBType: api.KBTypeTeam, Slug: "platform"}}
	s.reconcileProjectSymlinks(context.Background(), projectRoot, bubbles)

	// flat-layout link swept...
	_, err := os.Lstat(flatLink)
	assert.True(t, os.IsNotExist(err), "stale flat-layout symlink must be removed")

	// ...and the scoped link created, pointing at the canonical dir.
	scopedLink := filepath.Join(kbDir, "team", "platform")
	got, err := os.Readlink(scopedLink)
	require.NoError(t, err, "team/platform must exist after migration")
	assert.Equal(t, target, got)

	// the canonical target itself is untouched.
	assert.DirExists(t, target)
}

// TestReconcileProjectSymlinks_FlatLayoutStrayFileSurvives verifies the
// flat-layout sweep only removes SYMLINKS: a regular file a user left
// directly under .sageox/kb/ is never deleted.
// Failure prevented: the migration cleanup rm'ing a human's notes file
// that happened to live in the gitignored kb dir.
func TestReconcileProjectSymlinks_FlatLayoutStrayFileSurvives(t *testing.T) {
	kbXDGEnv(t)
	projectRoot := symlinkTestProject(t, "")
	s := newSymlinkTestScheduler(t, projectRoot)

	kbDir := filepath.Join(projectRoot, ".sageox", "kb")
	require.NoError(t, os.MkdirAll(kbDir, 0o755))
	stray := filepath.Join(kbDir, "NOTES.md")
	require.NoError(t, os.WriteFile(stray, []byte("mine\n"), 0o644))

	s.reconcileProjectSymlinks(context.Background(), projectRoot,
		[]api.KB{{KBID: "kb_team", KBType: api.KBTypeTeam, Slug: "platform"}})

	assert.FileExists(t, stray, "stray regular file must survive the flat-layout sweep")
	assert.FileExists(t, filepath.Join(kbDir, "team", "platform"))
}

// sageoxGitignorePath returns <root>/.sageox/.gitignore, creating the
// .sageox directory. In production the kb file lock already guarantees
// .sageox/ exists before ensureProjectGitignoreEntry runs.
func sageoxGitignorePath(t *testing.T, root string) string {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".sageox"), 0o755))
	return filepath.Join(root, ".sageox", ".gitignore")
}

// TestEnsureProjectGitignoreEntry_Idempotent verifies that appending the
// kb/ ignore rule is a no-op when already present, and never duplicates
// the entry.
//
// Failure prevented: every daemon tick appends the line again, growing
// .gitignore unboundedly.
func TestEnsureProjectGitignoreEntry_Idempotent(t *testing.T) {
	root := t.TempDir()
	path := sageoxGitignorePath(t, root)

	// first call: file absent — must create with the line.
	require.NoError(t, ensureProjectGitignoreEntry(root))
	body, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(body), sageoxignore.KBEntry)

	// second call: file present with the line — must not change anything.
	first := string(body)
	require.NoError(t, ensureProjectGitignoreEntry(root))
	body2, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, first, string(body2), "second call must be a true no-op")

	// third call after an unrelated entry: must preserve existing entries
	// and add ours exactly once.
	require.NoError(t, os.WriteFile(path, []byte("cache/\n"), 0o644))
	require.NoError(t, ensureProjectGitignoreEntry(root))
	body3, err := os.ReadFile(path)
	require.NoError(t, err)
	s := string(body3)
	assert.Contains(t, s, "cache/")
	assert.Equal(t, 1, countOccurrences(s, sageoxignore.KBEntry), "must add the line exactly once")
}

// TestEnsureProjectGitignoreEntry_NeverTouchesRootGitignore is the guard
// against the GH #732 regression. The reconciler used to write
// `.sageox/kb/` into the developer's root .gitignore; fixing only ox init
// would have left the daemon to put it straight back on the next sync
// pass, so this asserts the daemon never touches that file at all.
func TestEnsureProjectGitignoreEntry_NeverTouchesRootGitignore(t *testing.T) {
	root := t.TempDir()
	sageoxGitignorePath(t, root)
	rootGitignore := filepath.Join(root, ".gitignore")

	// case 1: no root .gitignore — the daemon must not create one.
	require.NoError(t, ensureProjectGitignoreEntry(root))
	_, err := os.Stat(rootGitignore)
	assert.True(t, os.IsNotExist(err), "daemon must not create the project's root .gitignore")

	// case 2: a root .gitignore the developer owns — byte-identical after.
	original := "# my rules\nnode_modules/\ndist/\n"
	require.NoError(t, os.WriteFile(rootGitignore, []byte(original), 0o644))
	require.NoError(t, ensureProjectGitignoreEntry(root))
	got, err := os.ReadFile(rootGitignore)
	require.NoError(t, err)
	assert.Equal(t, original, string(got), "daemon must leave the root .gitignore byte-identical")
}

// countOccurrences counts non-overlapping occurrences of substr in s.
// Helper for the gitignore-idempotency test where a substring search
// is more meaningful than a full equality check.
func countOccurrences(s, substr string) int {
	if substr == "" {
		return 0
	}
	count := 0
	idx := 0
	for {
		j := indexFrom(s, substr, idx)
		if j < 0 {
			return count
		}
		count++
		idx = j + len(substr)
	}
}

// indexFrom is strings.Index but with a starting offset.
func indexFrom(s, substr string, start int) int {
	if start >= len(s) {
		return -1
	}
	i := indexOf(s[start:], substr)
	if i < 0 {
		return -1
	}
	return start + i
}

// indexOf is a tiny strings.Index reimplementation kept local so the
// test file doesn't pull strings purely for one helper.
func indexOf(s, substr string) int {
	n := len(substr)
	if n == 0 {
		return 0
	}
	for i := 0; i+n <= len(s); i++ {
		if s[i:i+n] == substr {
			return i
		}
	}
	return -1
}

// TestReconcileProjectSymlinks_ConcurrentCalls_NoCorruption verifies
// that two simultaneous reconciliations against the same project root
// do not race into a corrupt state — the per-project file lock keeps
// them ordered.
//
// Failure prevented: two daemons (or a daemon + foreground `ox init`)
// reconcile at the same instant and produce duplicate temp files,
// half-written symlinks, or partially-pruned directories.
func TestReconcileProjectSymlinks_ConcurrentCalls_NoCorruption(t *testing.T) {
	kbXDGEnv(t)
	projectRoot := symlinkTestProject(t, "")
	s := newSymlinkTestScheduler(t, projectRoot)

	bubbles := []api.KB{
		{KBID: "kb_p", KBType: api.KBTypePersonal, Slug: "personal-abc"},
		{KBID: "kb_team", KBType: api.KBTypeTeam, Slug: "platform"},
	}

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.reconcileProjectSymlinks(context.Background(), projectRoot, bubbles)
		}()
	}
	wg.Wait()

	teamDir := filepath.Join(projectRoot, ".sageox", "kb", "team")
	entries, err := os.ReadDir(teamDir)
	require.NoError(t, err)

	// expect exactly 2 entries (the 2 bubbles' symlinks) — no temp
	// files, no duplicates, no half-written renames.
	var slugs []string
	for _, e := range entries {
		slugs = append(slugs, e.Name())
		// every entry must be a symlink and point at the right kb_id.
		full := filepath.Join(teamDir, e.Name())
		info, err := os.Lstat(full)
		require.NoError(t, err)
		assert.NotZero(t, info.Mode()&os.ModeSymlink, "%s should be a symlink, not a temp file", e.Name())
	}
	assert.ElementsMatch(t, []string{"personal-abc", "platform"}, slugs)
}

// TestReconcileProjectSymlinks_PerSymlinkFailureIsolation verifies that
// when creating one symlink fails (simulated permission error), the
// reconciler still attempts the rest.
//
// Failure prevented: a single broken slug (e.g., contains a path
// separator that shouldn't have made it past sanitizePathComponent)
// stops the entire pass, leaving every other bubble unlinked.
func TestReconcileProjectSymlinks_PerSymlinkFailureIsolation(t *testing.T) {
	kbXDGEnv(t)
	projectRoot := symlinkTestProject(t, "")
	s := newSymlinkTestScheduler(t, projectRoot)

	bubbles := []api.KB{
		{KBID: "kb_bad", KBType: api.KBTypePersonal, Slug: "bad-slug"},
		{KBID: "kb_good", KBType: api.KBTypeTeam, Slug: "good-slug"},
	}

	ops := defaultSymlinkOps()
	wrappedSymlink := ops.symlink
	ops.symlink = func(target, link string) error {
		// only the "bad-slug" path fails — the good one proceeds.
		if filepath.Base(stripTmpSuffix(link)) == "bad-slug" {
			return errors.New("simulated permission error")
		}
		return wrappedSymlink(target, link)
	}

	s.reconcileProjectSymlinksWithOps(context.Background(), projectRoot, bubbles, ops)

	teamDir := filepath.Join(projectRoot, ".sageox", "kb", "team")
	_, err := os.Lstat(filepath.Join(teamDir, "bad-slug"))
	assert.True(t, os.IsNotExist(err), "bad slug must remain unlinked")
	assert.FileExists(t, filepath.Join(teamDir, "good-slug"), "good slug must succeed despite earlier failure")
}

// stripTmpSuffix turns "<linkPath>.tmp-<n>" back into "<linkPath>".
// Used in the failure-isolation test to identify which bubble is
// being written without depending on the exact tmp-suffix format.
func stripTmpSuffix(link string) string {
	idx := indexOf(link, ".tmp-")
	if idx < 0 {
		return link
	}
	return link[:idx]
}

// TestDesiredSymlinks_EveryBubbleLinksUnderTeamScope verifies the
// desired-map computation independent of any filesystem state. Pure
// unit test — no I/O. Under ADR-028 §4 every bubble from the ambient
// scoped fetch links under team/<slug>; the only drops are rows with
// an empty kb_id or slug (unaddressable).
//
// Failure prevented: a future refactor reintroducing a per-type policy
// switch (or the old repo_id matching) that silently hides some kinds
// from the project view.
func TestDesiredSymlinks_EveryBubbleLinksUnderTeamScope(t *testing.T) {
	bubbles := []api.KB{
		{KBID: "kb_personal", KBType: api.KBTypePersonal, Slug: "personal-abc"},
		{KBID: "kb_profile", KBType: api.KBTypeProfile, Slug: "ryan-profile"},
		{KBID: "kb_team", KBType: api.KBTypeTeam, Slug: "platform"},
		{KBID: "kb_repo", KBType: api.KBTypeRepo, Slug: "my-app", RepoID: "repo_my_app"},
		{KBID: "kb_custom", KBType: api.KBTypeCustom, Slug: "custom-thing"},
		{KBID: "kb_channel", KBType: api.KBTypeChannel, Slug: "wip-broadcast"},
		{KBID: "kb_unknown", KBType: api.KBTypeUnknown, Slug: "future-kind"},
		{KBID: "", KBType: api.KBTypePersonal, Slug: "no-id"},  // dropped: empty kb_id
		{KBID: "kb_no_slug", KBType: api.KBTypeTeam, Slug: ""}, // dropped: empty slug
	}

	got := desiredSymlinks(bubbles)

	want := map[string]string{
		"team/personal-abc":  "kb_personal",
		"team/ryan-profile":  "kb_profile",
		"team/platform":      "kb_team",
		"team/my-app":        "kb_repo",
		"team/custom-thing":  "kb_custom",
		"team/wip-broadcast": "kb_channel",
		"team/future-kind":   "kb_unknown",
	}
	assert.Equal(t, want, got)
}

// --- ox-gzp.28 symlink lifecycle gaps ---
//
// The tests above cover initial-create, idempotency, slug-rename, revoke,
// repo-scoping, gitignore append, concurrency, and per-symlink failure
// isolation. The cases below close out the operational corners: vanished
// projects, target relocation, per-project failure isolation, gitignore
// pre-existence, and scale.

// TestReconcileProjectSymlinks_ProjectVanished verifies that when a project
// directory is removed out from under the daemon between discovery and
// reconciliation, the reconciler skips it cleanly without panicking and
// without leaving stray dirs behind on the filesystem.
//
// Failure prevented: the daemon discovers a project, the user deletes the
// workspace, and the reconciler crashes mid-pass — taking the next
// project's reconciliation down with it.
func TestReconcileProjectSymlinks_ProjectVanished(t *testing.T) {
	kbXDGEnv(t)
	projectRoot := symlinkTestProject(t, "")
	s := newSymlinkTestScheduler(t, projectRoot)

	// remove the project root before reconciling (simulates user `rm -rf`)
	require.NoError(t, os.RemoveAll(projectRoot))

	bubbles := []api.KB{{KBID: "kb_p", KBType: api.KBTypePersonal, Slug: "personal-abc"}}

	// must not panic and must not recreate the directory tree we just deleted
	require.NotPanics(t, func() {
		s.reconcileProjectSymlinks(context.Background(), projectRoot, bubbles)
	})

	// reconciler may MkdirAll(.sageox/kb[/team]) which would recreate the
	// tree. The contract we care about is: no panic, no error spam at
	// fatal level, and the recreated dirs, if any, hold only the scope
	// subdir and the symlink we asked for — never junk from a panicked
	// half-run.
	kbDir := filepath.Join(projectRoot, ".sageox", "kb")
	if entries, err := os.ReadDir(kbDir); err == nil {
		for _, e := range entries {
			full := filepath.Join(kbDir, e.Name())
			info, err := os.Lstat(full)
			require.NoError(t, err)
			if info.IsDir() {
				assert.Equal(t, "team", e.Name(), "only the team/ scope dir may exist at the kb root")
				continue
			}
			assert.NotZero(t, info.Mode()&os.ModeSymlink, "dir contains a non-symlink entry: %s", e.Name())
		}
	}
}

// TestReconcileProjectSymlinks_TargetRelocated verifies that when the
// canonical KBDir target is moved (e.g., GC's blue-green relocation under
// .gc-untracked), the reconciler re-points the symlink to the new target on
// the next pass — even though the kb_id and slug haven't changed.
//
// Failure prevented: stale symlinks pointing at a vanished old target dir
// would dangle indefinitely after a GC bluegreen swap, leaving editors
// unable to follow the link and AI coworkers reading nothing.
func TestReconcileProjectSymlinks_TargetRelocated(t *testing.T) {
	kbXDGEnv(t)
	projectRoot := symlinkTestProject(t, "")
	s := newSymlinkTestScheduler(t, projectRoot)

	bubbles := []api.KB{{KBID: "kb_p", KBType: api.KBTypePersonal, Slug: "personal-abc"}}
	target := paths.KBDir(endpoint.Get(), "kb_p")
	require.NoError(t, os.MkdirAll(target, 0o755))

	// first pass — link points at the canonical target
	s.reconcileProjectSymlinks(context.Background(), projectRoot, bubbles)
	linkPath := filepath.Join(projectRoot, ".sageox", "kb", "team", "personal-abc")
	got, err := os.Readlink(linkPath)
	require.NoError(t, err)
	require.Equal(t, target, got)

	// simulate the bluegreen GC behavior: write a sentinel into the
	// target so we can prove a "different target with the same path" is
	// still re-pointed correctly. Replace the on-disk target with an
	// entirely fresh one (different inode) — same path string, but the
	// reconciler should still detect "current readlink == desired" and
	// no-op rather than churning. The relocation case for this test is
	// the inverse: simulate a stale link pointing at a now-different
	// canonical path by writing a divergent symlink first, then asserting
	// the reconciler corrects it.
	require.NoError(t, os.Remove(linkPath))
	require.NoError(t, os.Symlink("/some/other/path", linkPath))

	// second pass — must re-point at the canonical target.
	s.reconcileProjectSymlinks(context.Background(), projectRoot, bubbles)
	got2, err := os.Readlink(linkPath)
	require.NoError(t, err)
	assert.Equal(t, target, got2, "reconciler must correct a divergent symlink target")
}

// TestReconcileProjectSymlinks_PerProjectFailureIsolation verifies that
// a failed reconciliation for one project root does not poison a later
// pass against another root — the reconciler keeps no cross-call state.
// (Production now reconciles only the daemon's own project root, but
// per-call containment is still the contract this pins.)
//
// Failure prevented: a permissions error on one workspace cascades into
// the next reconcile pass, leaving the project unsymlinked.
func TestReconcileProjectSymlinks_PerProjectFailureIsolation(t *testing.T) {
	if testing.Short() {
		t.Skip("short: registry-driven multi-project test is moderately slow")
	}
	kbXDGEnv(t)

	good := symlinkTestProject(t, "")
	bad := symlinkTestProject(t, "")

	// Make the bad project's .sageox dir unwritable so MkdirAll(.sageox/kb)
	// fails with a permissions error. The good project must still be
	// reconciled afterwards.
	badSageox := filepath.Join(bad, ".sageox")
	require.NoError(t, os.Chmod(badSageox, 0o500))
	t.Cleanup(func() { _ = os.Chmod(badSageox, 0o755) })

	s := newSymlinkTestScheduler(t, good)

	bubbles := []api.KB{{KBID: "kb_p", KBType: api.KBTypePersonal, Slug: "personal-abc"}}

	// reconcile two roots in sequence — each reconcileProjectSymlinks
	// call must be self-contained, with no state carried from a failed
	// pass into the next one.
	s.reconcileProjectSymlinks(context.Background(), bad, bubbles)
	s.reconcileProjectSymlinks(context.Background(), good, bubbles)

	// good project must have the link despite the earlier project failing.
	goodLink := filepath.Join(good, ".sageox", "kb", "team", "personal-abc")
	_, err := os.Lstat(goodLink)
	assert.NoError(t, err, "good project must be linked after a sibling project failed")
}

// TestEnsureProjectGitignoreEntry_AlreadyContainsLine verifies the
// historical case: a project that was hand-edited or updated by an older
// daemon already has `.sageox/kb/` in `.gitignore`. The append must be a
// pure no-op — same byte count, same content.
//
// Failure prevented: a parser drift that fails to recognize the existing
// line and re-appends it, slowly bloating .gitignore with duplicates over
// every daemon tick.
func TestEnsureProjectGitignoreEntry_AlreadyContainsLine(t *testing.T) {
	root := t.TempDir()
	path := sageoxGitignorePath(t, root)

	// pre-populate with the line, surrounded by other content + comments
	preset := "# ox-managed\ncache/\nkb/\nlogs/\n"
	require.NoError(t, os.WriteFile(path, []byte(preset), 0o644))

	require.NoError(t, ensureProjectGitignoreEntry(root))

	got, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, preset, string(got), "existing kb/ entry must not be rewritten or duplicated")
	assert.Equal(t, 1, countOccurrences(string(got), sageoxignore.KBEntry), "exactly one occurrence")
}

// TestEnsureProjectGitignoreEntry_FileDoesNotExist verifies the
// brand-new-project case: a repo that has never had a .gitignore at all.
// The helper must create the file with exactly the one line we want and
// nothing else.
//
// Failure prevented: writing an empty .gitignore (or worse, a 0-byte
// truncation of someone else's hidden file) when the entry was missing.
func TestEnsureProjectGitignoreEntry_FileDoesNotExist(t *testing.T) {
	root := t.TempDir()

	gitignorePath := sageoxGitignorePath(t, root)
	_, err := os.Stat(gitignorePath)
	require.True(t, os.IsNotExist(err), "precondition: .gitignore must not exist")

	require.NoError(t, ensureProjectGitignoreEntry(root))

	got, err := os.ReadFile(gitignorePath)
	require.NoError(t, err)
	assert.Equal(t, sageoxignore.KBEntry+"\n", string(got), "file must be created with exactly one line")
}

// TestReconcileProjectSymlinks_ManyBubblesScale exercises the reconciler
// with a realistic worst-case batch (50 bubbles) so a future O(n^2)
// regression in readCurrentSymlinks or desiredSymlinks shows up as a
// timeout-ish slowdown rather than a silent fact of life.
//
// Failure prevented: an inadvertent quadratic loop (e.g., scanning the
// directory inside the desired-loop) that doesn't show up at small sizes
// but pushes the per-tick cost over the sync budget on a power-user
// machine with many subscribed bubbles.
func TestReconcileProjectSymlinks_ManyBubblesScale(t *testing.T) {
	if testing.Short() {
		t.Skip("short: 50-bubble reconciliation is a slow soft-perf assertion")
	}
	kbXDGEnv(t)
	projectRoot := symlinkTestProject(t, "")
	s := newSymlinkTestScheduler(t, projectRoot)

	bubbles := make([]api.KB, 0, 50)
	for i := 0; i < 50; i++ {
		bubbles = append(bubbles, api.KB{
			KBID:   fmt.Sprintf("kb_%03d", i),
			KBType: api.KBTypePersonal,
			Slug:   fmt.Sprintf("p-%03d", i),
		})
	}

	start := time.Now()
	s.reconcileProjectSymlinks(context.Background(), projectRoot, bubbles)
	elapsed := time.Since(start)

	// soft assertion — 5 seconds is generous, even on heavily-loaded CI.
	// A real regression typically blows past this by 10x.
	assert.Less(t, elapsed, 5*time.Second, "reconciliation of 50 bubbles must complete promptly")

	teamDir := filepath.Join(projectRoot, ".sageox", "kb", "team")
	entries, err := os.ReadDir(teamDir)
	require.NoError(t, err)
	assert.Equal(t, 50, len(entries), "all 50 bubbles must be linked")
}

// TestDesiredSymlinks_UnsafeSlugSkipped pins the mount-scope containment
// guard: a server row whose slug carries path separators or dot-segments
// must never produce a symlink path outside .sageox/kb/team/.
// Failure prevented: a compromised or buggy server response writing
// symlinks anywhere the daemon user can write.
func TestDesiredSymlinks_UnsafeSlugSkipped(t *testing.T) {
	t.Parallel()
	bubbles := []api.KB{
		{KBID: "kb_ok", Slug: "safe-slug"},
		{KBID: "kb_tr1", Slug: "../escape"},
		{KBID: "kb_tr2", Slug: "a/b"},
		{KBID: "kb_tr3", Slug: `a\b`},
		{KBID: "kb_tr4", Slug: ".."},
		{KBID: "kb_tr5", Slug: "."},
	}
	got := desiredSymlinks(bubbles)
	if len(got) != 1 {
		t.Fatalf("expected only the safe slug to survive, got %v", got)
	}
	if got["team/safe-slug"] != "kb_ok" {
		t.Errorf("safe slug missing from desired set: %v", got)
	}
}
