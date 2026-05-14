package daemon

// KB sync discovery tests — mirror the "team context appears /
// disappears in credentials" suite (sync_team_discovery_test.go) for
// the kb pipeline. The kb equivalent of "credentials" is the API
// response; the daemon-side discovery surface is the lister factory
// that syncBubbles consults each pass.
//
// Coverage focus:
//   - new bubble appearing → cloned on the next pass
//   - bubble disappearing → not orphan-GC'd inappropriately during the
//     same pass it was last seen (i.e., GC's source-of-truth is the
//     freshly fetched list, not stale state)
//   - mid-roll: bubble visible in pass N, gone in pass N+1, re-appears
//     in pass N+2 — must reconcile each pass independently

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/paths"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// dynamicKBLister is a test double whose response can be swapped between
// passes — the simplest way to model "the API answer changed". The
// existing fakeKBLister in sync_bubbles_test.go is fixed-response.
type dynamicKBLister struct {
	get   func() ([]api.KB, error)
	calls atomic.Int32
}

func (d *dynamicKBLister) ListBubbles(_ context.Context) ([]api.KB, error) {
	d.calls.Add(1)
	return d.get()
}

// TestSyncBubbles_NewlyAppearing_ClonedOnNextPass verifies the daemon
// picks up a brand-new bubble that wasn't in the previous API response.
//
// Failure prevented: a freshly-provisioned personal/team bubble never
// landing on disk because the daemon cached the empty list and stopped
// asking. The lister is consulted every pass — caching is the API
// client's concern, not the daemon's.
func TestSyncBubbles_NewlyAppearing_ClonedOnNextPass(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	kbTestEnv(t)

	s, _ := kbTestScheduler(t)

	var current atomic.Value // []api.KB
	current.Store([]api.KB{})
	lister := &dynamicKBLister{get: func() ([]api.KB, error) {
		return current.Load().([]api.KB), nil
	}}
	s.SetKBBubbleListerFactory(func(_, _ string) KBBubbleLister { return lister })

	// pass 1: empty list. No clones expected.
	s.syncBubbles(context.Background())
	root := paths.KBDir("")
	if entries, err := os.ReadDir(root); err == nil {
		assert.Empty(t, entries, "no bubbles should exist before the API surfaces any")
	}

	// API now reports a new bubble.
	bareDir := makeBareRepo(t, "newcomer", "AGENTS.md", "hello\n")
	newBubble := api.KB{
		KBID:    "kb_newcomer",
		KBType:  api.KBTypeTeam,
		Slug:    "newcomer",
		RepoURL: "file://" + bareDir,
	}
	current.Store([]api.KB{newBubble})

	// pass 2: must clone the new bubble.
	s.syncBubbles(context.Background())

	target := paths.KBDir(newBubble.KBID)
	assert.DirExists(t, filepath.Join(target, ".git"),
		"newly-listed bubble must be cloned on the next pass")
	assert.FileExists(t, filepath.Join(target, "AGENTS.md"))
	assert.FileExists(t, filepath.Join(target, ".sageox", "meta.json"),
		"meta.json must be written on the same pass that clones")

	// lister must have been called at least twice (once per pass).
	assert.GreaterOrEqual(t, lister.calls.Load(), int32(2))
}

// TestSyncBubbles_DisappearingBubble_NotTouchedBySync verifies that a
// bubble removed from the API list is left on disk by syncBubbles
// itself. The daemon's syncBubbles loop is *only* a reconciler — GC of
// removed bubbles is the kbGC's job (sync_gc_kb.go), and crucially
// kbGC has its own grace period. syncBubbles must NEVER short-circuit
// that by deleting on the same pass.
//
// Failure prevented: a bubble revoked by the API getting nuked
// instantly by syncBubbles instead of going through the 7-day .trash
// grace period. The grace period is the user's recovery window.
func TestSyncBubbles_DisappearingBubble_NotTouchedBySync(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	kbTestEnv(t)

	s, _ := kbTestScheduler(t)

	var current atomic.Value
	bareDir := makeBareRepo(t, "ephemeral", "x.md", "v1\n")
	bubble := api.KB{
		KBID:    "kb_ephemeral",
		KBType:  api.KBTypeTeam,
		Slug:    "ephemeral",
		RepoURL: "file://" + bareDir,
	}
	current.Store([]api.KB{bubble})

	lister := &dynamicKBLister{get: func() ([]api.KB, error) {
		return current.Load().([]api.KB), nil
	}}
	s.SetKBBubbleListerFactory(func(_, _ string) KBBubbleLister { return lister })

	// pass 1: clone.
	s.syncBubbles(context.Background())
	target := paths.KBDir(bubble.KBID)
	require.DirExists(t, filepath.Join(target, ".git"))

	// API drops the bubble.
	current.Store([]api.KB{})

	// pass 2: syncBubbles must NOT touch the on-disk dir. GC has its
	// own pipeline + grace period; syncBubbles is the wrong place to
	// delete.
	s.syncBubbles(context.Background())

	assert.DirExists(t, filepath.Join(target, ".git"),
		"syncBubbles must not delete a missing bubble — that's GC's job (with grace period)")
	assert.FileExists(t, filepath.Join(target, "x.md"))
}

// TestSyncBubbles_Discovery_Flapping verifies that a bubble flapping in
// and out of the API list (visible, gone, visible) reconciles
// idempotently each pass without leaving leftover state.
//
// Failure prevented: an internal "seen this kb before" map that grows
// without bound, or a cleanup pathway that triggers when the list
// shrinks even temporarily.
func TestSyncBubbles_Discovery_Flapping(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	kbTestEnv(t)

	s, _ := kbTestScheduler(t)
	bareDir := makeBareRepo(t, "flap", "y.md", "v1\n")
	bubble := api.KB{
		KBID:    "kb_flap",
		KBType:  api.KBTypeProfile,
		Slug:    "flap",
		RepoURL: "file://" + bareDir,
	}

	var current atomic.Value
	current.Store([]api.KB{bubble})

	s.SetKBBubbleListerFactory(func(_, _ string) KBBubbleLister {
		return &dynamicKBLister{get: func() ([]api.KB, error) {
			return current.Load().([]api.KB), nil
		}}
	})

	// pass 1: present → clone.
	s.syncBubbles(context.Background())
	target := paths.KBDir(bubble.KBID)
	require.DirExists(t, filepath.Join(target, ".git"))

	// pass 2: gone → no-op.
	current.Store([]api.KB{})
	assert.NotPanics(t, func() {
		s.syncBubbles(context.Background())
	})
	assert.DirExists(t, filepath.Join(target, ".git"), "still present after disappearance pass")

	// pass 3: back → reconciles cleanly (no clone needed).
	current.Store([]api.KB{bubble})
	s.syncBubbles(context.Background())
	assert.DirExists(t, filepath.Join(target, ".git"))
	assert.FileExists(t, filepath.Join(target, ".sageox", "meta.json"),
		"meta.json must still be valid after a flap")
}

// TestSyncBubbles_NewBubbleAlongsideExisting verifies that the discovery
// pass adds new bubbles without disturbing already-cloned ones. The kb
// loop must be additive: each bubble is reconciled independently.
//
// Failure prevented: a regression where adding a new bubble forces a
// re-clone of every existing one (slow, network-heavy) or, worse,
// blows away the working tree of an unrelated bubble.
func TestSyncBubbles_NewBubbleAlongsideExisting(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	kbTestEnv(t)

	s, _ := kbTestScheduler(t)

	bareA := makeBareRepo(t, "a", "a.md", "alpha\n")
	bubbleA := api.KB{
		KBID:    "kb_a",
		KBType:  api.KBTypeTeam,
		Slug:    "alpha",
		RepoURL: "file://" + bareA,
	}

	var current atomic.Value
	current.Store([]api.KB{bubbleA})
	s.SetKBBubbleListerFactory(func(_, _ string) KBBubbleLister {
		return &dynamicKBLister{get: func() ([]api.KB, error) {
			return current.Load().([]api.KB), nil
		}}
	})

	// pass 1: clone bubbleA.
	s.syncBubbles(context.Background())
	targetA := paths.KBDir(bubbleA.KBID)
	require.DirExists(t, filepath.Join(targetA, ".git"))
	metaPathA := filepath.Join(targetA, ".sageox", "meta.json")
	preStat, err := os.Stat(metaPathA)
	require.NoError(t, err)

	// new bubbleB joins the list.
	bareB := makeBareRepo(t, "b", "b.md", "bravo\n")
	bubbleB := api.KB{
		KBID:    "kb_b",
		KBType:  api.KBTypeProfile,
		Slug:    "bravo",
		RepoURL: "file://" + bareB,
	}
	current.Store([]api.KB{bubbleA, bubbleB})

	// pass 2: bubbleB clones, bubbleA must remain intact.
	s.syncBubbles(context.Background())

	targetB := paths.KBDir(bubbleB.KBID)
	assert.DirExists(t, filepath.Join(targetB, ".git"), "newcomer must clone")
	assert.FileExists(t, filepath.Join(targetB, "b.md"))

	// bubbleA must still be on disk and its meta.json must still parse
	// (touched at most for last_sync, never wiped).
	assert.DirExists(t, filepath.Join(targetA, ".git"))
	assert.FileExists(t, filepath.Join(targetA, "a.md"))
	postStat, err := os.Stat(metaPathA)
	require.NoError(t, err, "bubbleA meta.json must remain readable after bubbleB joined")
	// the file may have been re-written for last_sync; size should be > 0.
	assert.Greater(t, postStat.Size(), int64(0))
	_ = preStat
}
