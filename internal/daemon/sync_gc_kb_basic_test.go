package daemon

// Knowledge-bubble GC parity tests, basic + team-style (ox-gzp.17).
//
// These tests mirror the *behavioral surface* of sync_gc_test.go and
// sync_team_gc_test.go — full-cycle scenarios, edge cases around stray
// dirs, and validation-style failure modes — adapted to the kb GC's
// triage+reaper paradigm.
//
// Key paradigm difference vs ledger/team-context GC: kb GC is NOT a
// blue-green reclone. There is no `<path>.new` / `<path>.old` staging
// dance. Instead, the GC pass:
//
//   - Lists local <kbRoot>/<kb_id>/ dirs
//   - Diffs against the API list
//   - Moves orphans into <kbRoot>/.trash/<kb_id>-<RFC3339>/ (triage)
//   - Reaps trash entries older than 7 days (reaper)
//
// Where a ledger test asserts ".old/.new are cleaned up", a kb test
// asserts ".trash/ is created lazily, contains only orphan entries,
// reaper drains it on schedule". The mapping is one-to-one in spirit:
// both ensure the GC is reversible until a grace period expires and
// never deletes authorized data.
//
// Scenarios mirrored from sync_gc_test.go + sync_team_gc_test.go (kb
// equivalents only — anything strictly tied to git-clone reclone is
// either inapplicable or already covered by sync_gc_kb_test.go):
//
//   - TestGC_FullCycle_TeamContext / _Ledger        → KBGC_FullCycle_*
//   - TestGC_SkipsWhenLocked                        → covered by existing
//                                                     concurrency test in
//                                                     sync_gc_kb_test.go
//   - TestBlueGreenGC_Success                       → KBGC_FullCycle_*
//   - TestBlueGreenGC_NotDueYet                     → kb GC has no
//                                                     interval gate (it's
//                                                     pure on-disk vs API
//                                                     reconciliation),
//                                                     intentionally skipped
//   - TestBlueGreenGC_CleansUpLeftoverNewDir        → KBGC_PreExistingTrash_*
//   - TestBlueGreenGC_PreExistingOldDir             → KBGC_PreExistingTrash_*
//   - TestBlueGreenGC_EmptyWorkspacePath            → KBGC_EmptyEndpoint_NoPanic
//   - TestBlueGreenGC_MissingGitDir                 → not applicable
//                                                     (kb GC doesn't gate
//                                                     on .git, it gates on
//                                                     dir presence)
//   - TestBlueGreenGC_WorkspacePathDoesNotExist     → KBGC_KBRootMissing_NoPanic
//   - TestBlueGreenGC_PreservesUntrackedFiles       → KBGC_TriageMovesEntireDir
//                                                     (proves untracked
//                                                     files inside the kb
//                                                     dir survive the move)
//
// Failure-prevention focus throughout: every test answers "what real
// disk-corruption / data-loss scenario does this prevent?"

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/paths"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Full-cycle parity ---

// TestKBGC_FullCycle_MixedAuthorizedAndOrphans verifies that a single
// GC pass over a tree containing both authorized and orphaned bubbles
// moves only the orphans into .trash/ and leaves authorized dirs
// byte-identical. This is the kb analog of TestGC_FullCycle_TeamContext.
//
// Failure prevented: a triage bug that either (a) sweeps authorized
// dirs into trash because of an off-by-one in the API set check, or (b)
// leaves orphans in place because the diff is computed wrong. Both have
// shipped in past GC implementations across the codebase, so the
// regression coverage is non-negotiable.
func TestKBGC_FullCycle_MixedAuthorizedAndOrphans(t *testing.T) {
	kbGCEnv(t)
	s, _ := kbTestScheduler(t)
	ctx := context.Background()

	keep1 := makeKBDir(t, "kb_keep_1", "alpha")
	keep2 := makeKBDir(t, "kb_keep_2", "beta")
	orphan1 := makeKBDir(t, "kb_orphan_1", "gone")
	orphan2 := makeKBDir(t, "kb_orphan_2", "also-gone")
	root := paths.KBDir("")

	s.runKBGC(ctx, stubListFn("kb_keep_1", "kb_keep_2"))

	// authorized dirs: untouched, content intact
	assert.DirExists(t, keep1)
	assert.DirExists(t, keep2)
	for _, p := range []string{keep1, keep2} {
		_, err := os.Stat(filepath.Join(p, "marker.txt"))
		require.NoError(t, err, "authorized kb marker must survive GC")
	}

	// orphans: moved out of canonical location
	assert.NoDirExists(t, orphan1)
	assert.NoDirExists(t, orphan2)

	trashEntries, err := os.ReadDir(filepath.Join(root, kbTrashDirName))
	require.NoError(t, err)
	assert.Len(t, trashEntries, 2, "exactly two orphans expected in trash")
}

// TestKBGC_FullCycle_TriageThenReap_SamePass verifies that a single
// runKBGC invocation runs both phases: an entry moved into trash NOW
// stays put (within grace), but a pre-existing expired trash entry is
// reaped in the same pass. The two phases must be independent.
//
// Failure prevented: a refactor that accidentally couples reap to
// triage's API-fetch path, causing the reaper to silently stop firing
// whenever the API is healthy and there's nothing new to triage.
func TestKBGC_FullCycle_TriageThenReap_SamePass(t *testing.T) {
	kbGCEnv(t)
	s, _ := kbTestScheduler(t)
	ctx := context.Background()
	root := paths.KBDir("")

	// pre-seed an expired trash entry from a prior pass
	trashDir := filepath.Join(root, kbTrashDirName)
	require.NoError(t, os.MkdirAll(trashDir, 0o755))
	expiredTS := time.Now().UTC().Add(-(kbTrashGracePeriod + 24*time.Hour)).Format(time.RFC3339)
	expired := filepath.Join(trashDir, fmt.Sprintf("kb_old-%s", expiredTS))
	require.NoError(t, os.MkdirAll(expired, 0o755))

	// new orphan to be triaged this pass
	freshOrphan := makeKBDir(t, "kb_fresh_orphan", "x")

	s.runKBGC(ctx, stubListFn())

	// reap fired
	assert.NoDirExists(t, expired, "expired trash must be reaped")
	// triage fired
	assert.NoDirExists(t, freshOrphan, "fresh orphan must be triaged")

	// the freshly-triaged entry is still in trash (within grace)
	entries, err := os.ReadDir(trashDir)
	require.NoError(t, err)
	require.Len(t, entries, 1, "only the freshly-triaged entry should remain")
	assert.Contains(t, entries[0].Name(), "kb_fresh_orphan-")
}

// --- Edge cases on the kb root ---

// TestKBGC_KBRootMissing_NoPanic verifies that runKBGC tolerates the
// canonical kb root not yet existing (fresh install, never logged in,
// never synced any bubbles). This is the kb analog of
// TestBlueGreenGC_WorkspacePathDoesNotExist.
//
// Failure prevented: GC tick panicking on a fresh install and taking
// down the scheduler goroutine, which would silently kill team-context
// and ledger GC too.
func TestKBGC_KBRootMissing_NoPanic(t *testing.T) {
	kbGCEnv(t)
	s, _ := kbTestScheduler(t)
	ctx := context.Background()

	// kb root has not been created — paths.KBDir("") resolves but the
	// directory itself is absent.
	root := paths.KBDir("")
	_, err := os.Stat(root)
	require.True(t, os.IsNotExist(err), "precondition: root must not exist")

	assert.NotPanics(t, func() {
		s.runKBGC(ctx, stubListFn("kb_anything"))
	})

	// nothing should have been created either
	_, err = os.Stat(root)
	assert.True(t, os.IsNotExist(err), "GC must not create root when there's nothing to do")
}

// TestKBGC_EmptyEndpoint_NoPanic verifies that runKBGC survives an
// unconfigured endpoint (paths.KBDir would normally panic on empty
// endpoint). This mirrors TestBlueGreenGC_EmptyWorkspacePath.
//
// Failure prevented: a daemon that's running before login (e.g. just
// after install, before `ox login`) panicking inside the GC goroutine
// the moment the first GC tick fires.
func TestKBGC_EmptyEndpoint_NoPanic(t *testing.T) {
	t.Setenv("OX_XDG_DISABLE", "")
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("SAGEOX_ENDPOINT", "") // explicitly empty

	s, _ := kbTestScheduler(t)
	ctx := context.Background()

	assert.NotPanics(t, func() {
		s.runKBGC(ctx, stubListFn("kb_anything"))
	})
}

// --- Pre-existing-trash parity (mirrors leftover .new / .old tests) ---

// TestKBGC_PreExistingTrash_NewOrphansAddedAlongside verifies that a
// pre-existing .trash/ directory (left behind from earlier passes) does
// not block a new triage from depositing additional orphans. This is
// the kb analog of TestBlueGreenGC_PreExistingOldDir +
// TestBlueGreenGC_CleansUpLeftoverNewDir.
//
// Failure prevented: a triage that aborts on first failure (e.g.
// MkdirAll errors out because the dir already exists with weird mode
// bits) and silently leaves orphans behind on disk forever.
func TestKBGC_PreExistingTrash_NewOrphansAddedAlongside(t *testing.T) {
	kbGCEnv(t)
	s, _ := kbTestScheduler(t)
	ctx := context.Background()
	root := paths.KBDir("")
	trashDir := filepath.Join(root, kbTrashDirName)

	// pre-existing recent trash entry (within grace, must survive)
	require.NoError(t, os.MkdirAll(trashDir, 0o755))
	recentTS := time.Now().UTC().Add(-1 * time.Hour).Format(time.RFC3339)
	preExisting := filepath.Join(trashDir, fmt.Sprintf("kb_old_orphan-%s", recentTS))
	require.NoError(t, os.MkdirAll(preExisting, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(preExisting, "old-content"), []byte("preserved"), 0o644))

	// new orphan in the canonical root
	newOrphan := makeKBDir(t, "kb_new_orphan", "fresh")

	s.runKBGC(ctx, stubListFn())

	// pre-existing trash entry: untouched
	assert.DirExists(t, preExisting)
	data, err := os.ReadFile(filepath.Join(preExisting, "old-content"))
	require.NoError(t, err)
	assert.Equal(t, "preserved", string(data))

	// new orphan: moved into trash, original location empty
	assert.NoDirExists(t, newOrphan)
	entries, err := os.ReadDir(trashDir)
	require.NoError(t, err)
	assert.Len(t, entries, 2, "both pre-existing and new trash entries must coexist")
}

// --- Untracked-file preservation parity ---

// TestKBGC_TriageMovesEntireDir_IncludingUntrackedFiles verifies that
// triage uses os.Rename so every file inside the orphan dir — tracked,
// untracked, gitignored — moves intact. This is the kb analog of the
// TestBlueGreenGC_PreservesUntrackedFiles family.
//
// Failure prevented: a "tidy" refactor that switches to copy+delete or
// to walking the tree and selectively moving files, silently dropping
// the user's local untracked notes. The grace-period restoration
// guarantee depends on every byte surviving the move.
func TestKBGC_TriageMovesEntireDir_IncludingUntrackedFiles(t *testing.T) {
	kbGCEnv(t)
	s, _ := kbTestScheduler(t)
	ctx := context.Background()

	dir := makeKBDir(t, "kb_rich", "tracked-content")
	// untracked-style sidecar files, including a nested dir with a
	// gitignored marker (kb GC must not interpret .gitignore semantics).
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "notes", "drafts"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "notes", "drafts", "wip.md"), []byte("draft"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".local-settings"), []byte("setting=1"), 0o644))
	// also seed a .git dir so the move proves we don't traverse selectively
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".git"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o644))

	s.runKBGC(ctx, stubListFn())

	// canonical location empty
	assert.NoDirExists(t, dir)

	// all four file paths survive intact under .trash/
	root := paths.KBDir("")
	trashEntries, err := os.ReadDir(filepath.Join(root, kbTrashDirName))
	require.NoError(t, err)
	require.Len(t, trashEntries, 1)
	moved := filepath.Join(root, kbTrashDirName, trashEntries[0].Name())

	for _, rel := range []string{
		"marker.txt",
		"notes/drafts/wip.md",
		".local-settings",
		".git/HEAD",
	} {
		_, err := os.Stat(filepath.Join(moved, rel))
		assert.NoError(t, err, "expected %q to survive triage move", rel)
	}
}

// --- Validation parity ---

// TestKBGC_NonDirEntriesIgnored verifies that stray files at the kb
// root level (not directories) are not triaged. The triage loop only
// considers IsDir entries — a stray file is left alone. This is the kb
// analog of the validate*GCClone family: "don't act on shapes that
// don't match expectations".
//
// Failure prevented: a future refactor of the readdir loop that drops
// the IsDir guard and starts moving stray index files or symlinks into
// trash, which would corrupt the kb root's structural invariants.
func TestKBGC_NonDirEntriesIgnored(t *testing.T) {
	kbGCEnv(t)
	s, _ := kbTestScheduler(t)
	ctx := context.Background()

	// seed both a kb dir (orphan) and a stray top-level file
	makeKBDir(t, "kb_orphan", "x")
	root := paths.KBDir("")
	stray := filepath.Join(root, "stray-file")
	require.NoError(t, os.WriteFile(stray, []byte("not-a-bubble"), 0o644))

	s.runKBGC(ctx, stubListFn())

	// stray file untouched
	assert.FileExists(t, stray)
	data, err := os.ReadFile(stray)
	require.NoError(t, err)
	assert.Equal(t, "not-a-bubble", string(data))
}

// TestKBGC_DotPrefixedDirsIgnored verifies that dot-prefixed dirs at
// the kb root level (e.g. .trash, future .lock, .cache) are exempt from
// triage. The triage loop explicitly skips strings.HasPrefix(name, ".").
//
// Failure prevented: the GC pass eating its own state directories (a
// .lock dir, a future .stats cache) because the prefix guard was lost
// in a refactor.
func TestKBGC_DotPrefixedDirsIgnored(t *testing.T) {
	kbGCEnv(t)
	s, _ := kbTestScheduler(t)
	ctx := context.Background()
	root := paths.KBDir("")

	require.NoError(t, os.MkdirAll(root, 0o755))
	dotDir := filepath.Join(root, ".future-state")
	require.NoError(t, os.MkdirAll(dotDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dotDir, "marker"), []byte("ok"), 0o644))

	// API list authorizes nothing — but .future-state is dot-prefixed
	s.runKBGC(ctx, stubListFn())

	assert.DirExists(t, dotDir, "dot-prefixed dirs must survive triage")
	data, err := os.ReadFile(filepath.Join(dotDir, "marker"))
	require.NoError(t, err)
	assert.Equal(t, "ok", string(data))
}

// --- API list edge cases ---

// TestKBGC_NilListFn_NoPanic verifies that runKBGC tolerates a nil
// listFn without panicking. checkAndRunGC's wrapper passes through
// whatever buildKBGCListFn returns, and a misconfigured factory could
// in principle return nil. Mirrors the "unconfigured workspace" tests
// from team_gc.
//
// Failure prevented: a future refactor where buildKBGCListFn returns
// nil instead of an ErrKBAPIUnavailable-yielding closure, taking down
// the GC tick on every interval.
func TestKBGC_NilListFn_NoPanic(t *testing.T) {
	kbGCEnv(t)
	s, _ := kbTestScheduler(t)
	ctx := context.Background()

	// seed an orphan; with nil listFn, triage must skip and leave it
	orphan := makeKBDir(t, "kb_x", "y")

	assert.NotPanics(t, func() {
		s.runKBGC(ctx, nil)
	})

	// triage skipped → orphan untouched
	assert.DirExists(t, orphan, "orphan must survive when listFn is nil")
}

// TestKBGC_EmptyAPIIDsTreatedAsEmptySet verifies that an explicit empty
// kb_id slice (versus a nil slice) is interpreted correctly: the user
// has access to no bubbles, so all local kb dirs are orphans. This
// pins the contract — silent string filtering inside the triage must
// continue to drop empty IDs from the want set.
//
// Failure prevented: an empty-string kb_id sneaking into the want map
// and then matching local dirs whose name happens to be the empty
// string (which can't happen today, but future filesystem oddities
// could expose this — and the bug would be invisible until then).
func TestKBGC_EmptyAPIIDsTreatedAsEmptySet(t *testing.T) {
	kbGCEnv(t)
	s, _ := kbTestScheduler(t)
	ctx := context.Background()

	orphan := makeKBDir(t, "kb_drop", "x")

	// list returns an empty-string id mixed in with no real ones — the
	// triage's filter must reject the empty id and treat the set as
	// empty.
	s.runKBGC(ctx, stubListFn("", ""))

	assert.NoDirExists(t, orphan, "kb dir must be orphaned when API list is effectively empty")
}
