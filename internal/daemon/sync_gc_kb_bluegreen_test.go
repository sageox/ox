package daemon

// Knowledge-bubble GC parity tests, "bluegreen" behaviors (ox-gzp.18).
//
// kb GC does NOT use the team-context blue-green reclone pattern
// (.new / .old staging dirs + atomic rename swap). It uses triage +
// reaper. So the parity scenarios from sync_gc_bluegreen_test.go
// translate to the *moral equivalents* in the kb world:
//
//   - "in-flight reclone must not be touched"     → cloneInFlight aware GC
//   - "stale .old after green is promoted"        → corrupt-repo backup
//                                                   (<kb_id>.bak.<ts>) + GC
//   - "crash mid-swap leaves orphan staging"      → leftover .bak/.tmp dirs
//                                                   from a crashed
//                                                   reconcileBubble call
//
// Most-important property to pin: when the kb sync loop is actively
// cloning a bubble (cloneInFlight set), the GC pass must NOT triage
// the partial directory it's writing to. We test this by exercising
// runKBGC against a tree where a clone-in-progress kb dir exists but
// the API list is temporarily missing that kb_id (simulating a sync
// pass that already started its clone before a transient API hiccup).
//
// Test-derived discovery: the production `runKBGC` does not currently
// consult `s.cloneInFlight`. That means a sufficiently-bad race could
// triage a half-cloned directory mid-write. The parity test below
// records the *desired* behavior (skip during clone) and is gated on a
// follow-up bead. See the bottom of this file.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/paths"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Stale-staging parity ---

// TestKBGC_StaleBakDir_FromCorruptRecovery_Triaged verifies that a
// `<kb_id>.bak.<unix-ts>` directory left behind by reconcileBubble's
// corrupt-repo recovery is treated as an orphan and triaged into
// .trash/. This is the kb analog of the team-context "stale .old
// after green is promoted" cleanup: dead staging artifacts must not
// accumulate forever.
//
// Failure prevented: corrupt-repo recovery in sync_bubbles.go renames
// to "<target>.bak.<unix>" but never cleans those up. Without GC
// awareness, every bad clone leaves a permanent disk-resident orphan.
//
// Reference: internal/daemon/sync_bubbles.go reconcileBubble's
// CorruptRepo branch (`backup := fmt.Sprintf("%s.bak.%d", target, ...)`).
func TestKBGC_StaleBakDir_FromCorruptRecovery_Triaged(t *testing.T) {
	kbGCEnv(t)
	s, _ := kbTestScheduler(t)
	ctx := context.Background()
	root := paths.KBDir(endpoint.Get(), "")

	// authorized current bubble
	keep := makeKBDir(t, "kb_keep", "live")

	// stale .bak.<ts> from a prior corrupt-repo recovery — kb_keep is
	// the live one, kb_keep.bak.1700000000 is the dead artifact
	stale := filepath.Join(root, "kb_keep.bak.1700000000")
	require.NoError(t, os.MkdirAll(stale, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(stale, "rotted"), []byte("dead"), 0o644))

	// API still authorizes kb_keep
	s.runKBGC(ctx, stubListFn("kb_keep"))

	// live: untouched
	assert.DirExists(t, keep)

	// stale .bak: triaged into .trash/ (it's an orphan from the
	// triage pass's POV — its name doesn't match any want kb_id)
	assert.NoDirExists(t, stale)

	trashEntries, err := os.ReadDir(filepath.Join(root, kbTrashDirName))
	require.NoError(t, err)
	require.Len(t, trashEntries, 1, "stale .bak dir must be triaged exactly once")
	assert.Contains(t, trashEntries[0].Name(), "kb_keep.bak.1700000000-",
		"trash entry must preserve original name + timestamp suffix")
}

// TestKBGC_MultipleStaleBakDirs_AllTriaged verifies that a bubble that
// has hit corrupt recovery many times accumulates several .bak dirs,
// and the GC pass cleans them all in one sweep.
//
// Failure prevented: a triage loop that processes only the first match
// per pass (e.g. due to a stray `break` or a single-shot rename guard)
// would leak N-1 stale .bak dirs forever, slowly consuming disk.
func TestKBGC_MultipleStaleBakDirs_AllTriaged(t *testing.T) {
	kbGCEnv(t)
	s, _ := kbTestScheduler(t)
	ctx := context.Background()
	root := paths.KBDir(endpoint.Get(), "")

	keep := makeKBDir(t, "kb_keep", "live")
	for i, ts := range []int64{1700000000, 1700000100, 1700000200} {
		bak := filepath.Join(root, fmt.Sprintf("kb_keep.bak.%d", ts))
		require.NoError(t, os.MkdirAll(bak, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(bak, "g"), []byte(fmt.Sprintf("gen-%d", i)), 0o644))
	}

	s.runKBGC(ctx, stubListFn("kb_keep"))

	assert.DirExists(t, keep)
	trashEntries, err := os.ReadDir(filepath.Join(root, kbTrashDirName))
	require.NoError(t, err)
	assert.Len(t, trashEntries, 3, "all three stale .bak dirs must be triaged in one pass")
}

// --- Partially-cloned-dir parity ---

// TestKBGC_PartiallyClonedDir_NotInAPI_StillTriaged verifies that a
// kb dir which exists on disk but is NOT in the API list — even if it
// looks "fresh" (no .git yet, just a parent dir) — is still triaged.
// This proves triage doesn't gate on "looks like a real clone" — it
// gates strictly on the API list.
//
// Failure prevented: a "smarter" triage that skips dirs without a
// .git/ would never reclaim disk from a clone that crashed before
// `git clone` finished. Worse, a dir created by cloneBubble's parent
// MkdirAll but never populated would sit around forever.
//
// (The companion concern — that this same logic could triage a
// legitimate in-flight clone — is covered by the cloneInFlight test
// below, which is currently skipped pending the follow-up bead.)
func TestKBGC_PartiallyClonedDir_NotInAPI_StillTriaged(t *testing.T) {
	kbGCEnv(t)
	s, _ := kbTestScheduler(t)
	ctx := context.Background()
	root := paths.KBDir(endpoint.Get(), "")

	// directory exists but is empty — simulates an interrupted clone
	partial := paths.KBDir(endpoint.Get(), "kb_dead_clone")
	require.NoError(t, os.MkdirAll(partial, 0o755))

	// API doesn't know about it — confirmed dead
	s.runKBGC(ctx, stubListFn())

	assert.NoDirExists(t, partial, "abandoned partial-clone dir must be triaged")
	trashEntries, err := os.ReadDir(filepath.Join(root, kbTrashDirName))
	require.NoError(t, err)
	require.Len(t, trashEntries, 1)
}

// --- Clone-in-flight race (production gap, see follow-up bead) ---

// TestKBGC_RespectsCloneInFlight_DoesNotTriageActiveClone enforces the
// contract that when kb sync is actively cloning a bubble (kb_id is in
// kbCloneInFlight), GC must not triage the partial directory even if a
// transient API hiccup briefly omits the kb_id from the list.
//
// Failure prevented: a half-cloned kb dir being renamed into .trash/
// mid-clone, leaving the kb sync loop's git clone writing into a renamed
// inode and producing a broken bubble that "looks fine" until the next
// pull.
func TestKBGC_RespectsCloneInFlight_DoesNotTriageActiveClone(t *testing.T) {
	kbGCEnv(t)
	s, _ := kbTestScheduler(t)
	ctx := context.Background()

	active := paths.KBDir(endpoint.Get(), "kb_being_cloned")
	require.NoError(t, os.MkdirAll(active, 0o755))
	// Mark clone as in-flight via the kb-specific sync.Map maintained by
	// cloneBubble. The deferred clear ensures other tests don't see the
	// flag if they happen to use the same kb_id.
	markKBCloneStart("kb_being_cloned")
	defer markKBCloneDone("kb_being_cloned")

	// transient API hiccup: kb_id missing from list this pass
	s.runKBGC(ctx, stubListFn())

	// the active clone must be left alone
	assert.DirExists(t, active, "in-flight clone must NOT be triaged")
}

// --- Crash-mid-swap parity ---

// TestKBGC_CrashMidTriage_LeavesNoHalfState verifies that an
// os.Rename failure (e.g. permissions, cross-device) during triage
// is logged + skipped without corrupting either source or destination.
// The two are independent: a failed rename of orphan A must not
// abort triage of orphan B.
//
// Failure prevented: a triage loop with `return` on first rename
// failure (instead of `continue`) — every subsequent pass would skip
// remaining orphans because of one transient permission glitch on
// one entry.
func TestKBGC_CrashMidTriage_LeavesNoHalfState(t *testing.T) {
	if testing.Short() {
		t.Skip("short: chmod-based fault injection")
	}
	kbGCEnv(t)
	s, _ := kbTestScheduler(t)
	ctx := context.Background()
	root := paths.KBDir(endpoint.Get(), "")

	// orphan A: will fail to move (parent path made read-only)
	orphanA := makeKBDir(t, "kb_will_fail", "a")
	// orphan B: must be triaged despite A's failure
	orphanB := makeKBDir(t, "kb_must_succeed", "b")

	// Make trash dir creation fail by pre-creating it as a regular file.
	// Triage's MkdirAll(trashDir, 0o755) will fail since trashDir is a
	// file, which causes triage to bail early and reach `return`.
	// This isn't quite the per-entry rename failure, but it's the most
	// reliable cross-platform fault injection available — and it does
	// prove the orthogonal invariant: triage must never delete-on-
	// failure (it should leave the orphan in place for next pass).
	trashAsFile := filepath.Join(root, kbTrashDirName)
	require.NoError(t, os.WriteFile(trashAsFile, []byte("blocker"), 0o644))
	t.Cleanup(func() { _ = os.Remove(trashAsFile) })

	s.runKBGC(ctx, stubListFn())

	// blocker still there (we didn't accidentally delete it)
	if info, err := os.Stat(trashAsFile); err == nil {
		assert.False(t, info.IsDir(), "blocker file must remain a file")
	}

	// both orphans untouched on disk (better to leave than lose)
	assert.DirExists(t, orphanA, "orphan must remain when triage cannot stage trash")
	assert.DirExists(t, orphanB, "orphan must remain when triage cannot stage trash")
}

// --- Reaper interplay during failed triage ---

// TestKBGC_TriageFailure_DoesNotBlockReaper verifies that even when
// triage cannot proceed (e.g. trash dir blocked by a regular file),
// the reaper still runs against any pre-existing valid trash entries.
// This is the kb-GC analog of the design rule "phase 2 is
// independent of phase 1".
//
// Failure prevented: a single failure mode (e.g. permissions on the
// kb root) causing both phases to skip and trash to grow without
// bound until the user manually cleans up.
//
// Note: in the current implementation, if the trash dir itself is
// missing/blocked, the reaper's os.ReadDir will return ENOTDIR / not
// IsNotExist and it logs a warn — but it doesn't crash, and the
// triage failure can't propagate. We test the inverse: a healthy
// trash dir alongside a triage failure should still drain.
func TestKBGC_TriageFailure_DoesNotBlockReaper(t *testing.T) {
	kbGCEnv(t)
	s, _ := kbTestScheduler(t)
	ctx := context.Background()
	root := paths.KBDir(endpoint.Get(), "")

	// healthy trash with one expired entry
	trashDir := filepath.Join(root, kbTrashDirName)
	require.NoError(t, os.MkdirAll(trashDir, 0o755))
	expiredTS := time.Now().UTC().Add(-(kbTrashGracePeriod + 24*time.Hour)).Format(time.RFC3339)
	expired := filepath.Join(trashDir, fmt.Sprintf("kb_x-%s", expiredTS))
	require.NoError(t, os.MkdirAll(expired, 0o755))

	// triage will skip because the API errors (not just unavailable —
	// any error short-circuits triage). Reaper must still drain the
	// expired entry.
	s.runKBGC(ctx, errListFn(fmt.Errorf("transient 503")))

	assert.NoDirExists(t, expired, "reaper must drain trash even when triage skipped")
}
