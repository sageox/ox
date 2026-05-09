package daemon

// Knowledge-bubble GC parity tests, "reopen" race behaviors (ox-gzp.19).
//
// Mirrors sync_gc_reopen_test.go's intent: prove that the GC pass
// behaves correctly when a previously-removed thing comes back. For
// the team-context / ledger world, that meant reopening the SQLite
// whisper store after a blue-green inode swap. For kb, the analogous
// race is:
//
//   - GC pass moves kb_id_X into .trash/<kb_id_X>-<ts>/
//   - Before the grace period expires, the API re-lists kb_id_X
//     (e.g. user re-subscribed, permission re-granted, transient
//     server-side outage resolved)
//   - The next kb sync pass needs to materialize kb_id_X at its
//     canonical path again
//
// Two contracts to pin:
//
//   (1) GC must NOT delete the canonical kb dir of a re-listed
//       kb_id, even when that kb_id was orphaned in a *previous* GC
//       pass and is now coming back. Once it's in the API list, it's
//       authorized — period.
//
//   (2) When kb_id_X has a stale entry in .trash/<kb_id_X>-<ts>/ AND
//       a fresh canonical dir at <kb_id_X>/ (because kb sync
//       re-cloned it), the GC pass must leave both alone:
//       - canonical dir: authorized, untouched
//       - trash entry:   awaits its grace period, untouched
//
// Behavior decision pinned: ON RE-LIST AFTER TRIAGE, kb sync clones
// FRESH into <kb_id>/. The trash entry is NOT moved back. This is the
// safe choice because:
//
//   - The sync loop's existence check is `os.Stat(target/.git)`, which
//     cannot find a .trash/ entry (different name, ".trash/<kb_id>-<ts>")
//   - "Restore from trash" would require a bespoke kb-aware code path
//     and concurrency-safe rename racing against the active grace
//     reaper — high cost, narrow benefit
//   - The user's local state in the trash entry is gone for grace-
//     period purposes only; if they made local edits inside the
//     orphaned bubble those are recoverable manually for 7 days but
//     are not auto-restored
//
// This contract is the opposite of bead ox-gzp.19's original
// suggestion ("restored from .trash/ rather than recloned"). The
// tests below assert the implemented behavior. If product later
// chooses the "restore from trash" semantics, these tests will fail
// loudly and surface the change in review — that's the desired
// behavior for a contract test.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sageox/ox/internal/paths"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Re-list contract ---

// TestKBGC_ReListedAfterTriage_TrashLeftAlone verifies that when a
// kb_id is in .trash/<kb_id>-<ts>/ AND the API re-lists kb_id, the
// GC pass leaves the trash entry alone. Specifically: triage doesn't
// resurrect it, and the reaper waits out its grace period normally.
//
// Failure prevented: a "smart" triage that, on seeing kb_id come
// back, attempts to rename .trash/<kb_id>-<ts>/ back to <kb_id>/.
// That would race against any concurrent kb sync that's already
// cloning kb_id fresh — and produce two kb dirs trying to occupy the
// same canonical path.
func TestKBGC_ReListedAfterTriage_TrashLeftAlone(t *testing.T) {
	kbGCEnv(t)
	s, _ := kbTestScheduler(t)
	ctx := context.Background()
	root := paths.KBDir("")

	// pre-existing recent trash entry for kb_returning
	trashDir := filepath.Join(root, kbTrashDirName)
	require.NoError(t, os.MkdirAll(trashDir, 0o755))
	recentTS := time.Now().UTC().Add(-1 * time.Hour).Format(time.RFC3339)
	trashed := filepath.Join(trashDir, fmt.Sprintf("kb_returning-%s", recentTS))
	require.NoError(t, os.MkdirAll(trashed, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(trashed, "old-content"), []byte("from-grace"), 0o644))

	// API re-lists kb_returning (no canonical dir yet — sync hasn't
	// re-cloned in this scenario)
	s.runKBGC(ctx, stubListFn("kb_returning"))

	// trash entry: untouched (waiting out its 7-day grace)
	assert.DirExists(t, trashed, "trash entry must remain untouched on re-list")
	data, err := os.ReadFile(filepath.Join(trashed, "old-content"))
	require.NoError(t, err)
	assert.Equal(t, "from-grace", string(data), "trash entry contents must remain bit-identical")

	// canonical dir: NOT created by GC (clone is sync's job, not GC's)
	canonical := paths.KBDir("kb_returning")
	_, err = os.Stat(canonical)
	assert.True(t, os.IsNotExist(err),
		"GC must not auto-restore from trash — canonical dir creation is the sync loop's job")
}

// TestKBGC_ReListedAfterTriage_FreshCloneCoexists verifies the
// post-resurrection steady state: a re-listed kb_id has been freshly
// cloned by kb sync into the canonical path, AND a stale trash entry
// from the original triage still exists. Both must be left alone by
// GC: the canonical is authorized, the trash entry is on its
// grace-period timer.
//
// Failure prevented: triage seeing a "duplicate" (canonical kb_id +
// trash kb_id-<ts>) and incorrectly deduplicating one of them. The
// trash name is unique by construction (kb_id + RFC3339), so the
// triage loop's "if want[name]" check correctly handles only the
// canonical name — but a future "tidy" pass that walks .trash/
// looking for "redundant" entries could regress this.
func TestKBGC_ReListedAfterTriage_FreshCloneCoexists(t *testing.T) {
	kbGCEnv(t)
	s, _ := kbTestScheduler(t)
	ctx := context.Background()
	root := paths.KBDir("")

	// stale trash entry from a prior pass
	trashDir := filepath.Join(root, kbTrashDirName)
	require.NoError(t, os.MkdirAll(trashDir, 0o755))
	staleTS := time.Now().UTC().Add(-2 * 24 * time.Hour).Format(time.RFC3339)
	stale := filepath.Join(trashDir, fmt.Sprintf("kb_back-%s", staleTS))
	require.NoError(t, os.MkdirAll(stale, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(stale, "old"), []byte("two-days-ago"), 0o644))

	// fresh canonical clone exists (kb sync recreated it)
	fresh := makeKBDir(t, "kb_back", "fresh-clone")

	// API authorizes
	s.runKBGC(ctx, stubListFn("kb_back"))

	// canonical: untouched
	assert.DirExists(t, fresh)
	data, err := os.ReadFile(filepath.Join(fresh, "marker.txt"))
	require.NoError(t, err)
	assert.Equal(t, "fresh-clone", string(data))

	// trash entry: untouched (still within 7d grace)
	assert.DirExists(t, stale)
	data, err = os.ReadFile(filepath.Join(stale, "old"))
	require.NoError(t, err)
	assert.Equal(t, "two-days-ago", string(data))
}

// TestKBGC_ReListedAfterGracePeriod_FreshCloneClean verifies the
// post-grace-period steady state: trash entry expired and reaped,
// canonical dir freshly cloned. After the next GC pass, only the
// canonical remains — disk fully reclaimed.
//
// Failure prevented: a grace-period regression where the reaper
// special-cases "kb_id is now in the API list" and refuses to delete
// the matching trash entry. That would leak disk on every
// revoke→regrant cycle.
func TestKBGC_ReListedAfterGracePeriod_FreshCloneClean(t *testing.T) {
	kbGCEnv(t)
	s, _ := kbTestScheduler(t)
	ctx := context.Background()
	root := paths.KBDir("")

	// trash entry beyond grace (reapable)
	trashDir := filepath.Join(root, kbTrashDirName)
	require.NoError(t, os.MkdirAll(trashDir, 0o755))
	expiredTS := time.Now().UTC().Add(-(kbTrashGracePeriod + 1*time.Hour)).Format(time.RFC3339)
	expired := filepath.Join(trashDir, fmt.Sprintf("kb_back-%s", expiredTS))
	require.NoError(t, os.MkdirAll(expired, 0o755))

	// fresh canonical
	fresh := makeKBDir(t, "kb_back", "fresh")

	s.runKBGC(ctx, stubListFn("kb_back"))

	assert.DirExists(t, fresh, "canonical kb dir must survive — re-listed and authorized")
	assert.NoDirExists(t, expired, "expired trash entry must be reaped even though kb_id is back in API")
}

// --- Slug change across grace window ---

// TestKBGC_SlugChange_SamekbID_KeepsCanonical verifies that the
// triage gates on kb_id, not slug. A bubble whose slug changed
// server-side (kb_id stable) must remain in the canonical
// <kbRoot>/<kb_id>/ location — slug is metadata, kb_id is identity.
//
// Failure prevented: a regression where triage starts comparing
// against slugs (via kb meta.json) instead of kb_ids, causing every
// renamed bubble to be triaged on the next pass. The user would lose
// their local state for any bubble they renamed.
func TestKBGC_SlugChange_SamekbID_KeepsCanonical(t *testing.T) {
	kbGCEnv(t)
	s, _ := kbTestScheduler(t)
	ctx := context.Background()

	dir := makeKBDir(t, "kb_renamed", "user-data")
	// write a meta.json with the OLD slug; the API list returns just
	// kb_id, so triage doesn't actually care about the slug here, but
	// we seed it to prove the test isn't accidentally relying on slug.
	metaDir := filepath.Join(dir, ".sageox")
	require.NoError(t, os.MkdirAll(metaDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(metaDir, "meta.json"),
		[]byte(`{"type":"personal","slug":"old-slug"}`), 0o644))

	// API still authorizes kb_renamed (slug change doesn't affect kb_id)
	s.runKBGC(ctx, stubListFn("kb_renamed"))

	assert.DirExists(t, dir, "canonical kb dir must survive a slug change")
	data, err := os.ReadFile(filepath.Join(dir, "marker.txt"))
	require.NoError(t, err)
	assert.Equal(t, "user-data", string(data))
}

// --- Concurrent re-list during reap ---

// TestKBGC_ConcurrentReList_DuringReap_SafeUnderRace verifies that
// running two GC passes concurrently — one with the "old" API view
// (kb_id absent) and one with the "new" view (kb_id back) — does not
// corrupt either trash or canonical. The pass that wins the race
// determines whether kb_id gets triaged this tick; the second pass
// is consistent with whatever state the first one left.
//
// Failure prevented: a race where one pass triages kb_id and another
// — seeing the now-empty canonical position with kb_id authorized —
// resurrects from trash, ending up with the kb dir at both
// canonical AND in trash. Even if production never hits this today,
// pinning the safety property here prevents a future "smart"
// resurrection from regressing it.
func TestKBGC_ConcurrentReList_DuringReap_SafeUnderRace(t *testing.T) {
	if testing.Short() {
		t.Skip("short: spawns concurrent goroutines")
	}
	kbGCEnv(t)
	s, _ := kbTestScheduler(t)
	ctx := context.Background()

	makeKBDir(t, "kb_flapping", "x")
	root := paths.KBDir("")

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		// view A: kb_flapping is gone
		s.runKBGC(ctx, stubListFn())
	}()
	go func() {
		defer wg.Done()
		// view B: kb_flapping is back
		s.runKBGC(ctx, stubListFn("kb_flapping"))
	}()
	wg.Wait()

	// the kb_flapping dir is in EXACTLY ONE place: canonical or trash,
	// never both, never neither.
	canonicalExists := false
	if _, err := os.Stat(paths.KBDir("kb_flapping")); err == nil {
		canonicalExists = true
	}

	trashCount := 0
	if entries, err := os.ReadDir(filepath.Join(root, kbTrashDirName)); err == nil {
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), "kb_flapping-") {
				trashCount++
			}
		}
	}

	switch {
	case canonicalExists && trashCount == 0:
		// view B's pass ran (or A's triage was a no-op because B got there first)
	case !canonicalExists && trashCount == 1:
		// view A's pass triaged it
	default:
		t.Fatalf("invariant violated: canonicalExists=%v trashCount=%d "+
			"(must be {true,0} or {false,1})", canonicalExists, trashCount)
	}
}
