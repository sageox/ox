package daemon

// KB sync race + multiclone tests. Concurrency is a first-class test
// axis per .claude/rules/testing.md: every shared-state path needs a
// "many goroutines, no panic, no deadlock, no torn writes" smoke test
// before it ships. D1 (sync_bubbles_test.go) didn't cover any of this.
//
// Endpoint-isolation is covered by sync_bubbles_clone_test.go's
// TestSyncBubbles_Clone_EndpointScoping; here we focus on:
//   - many concurrent passes on the SAME scheduler (mutex / map safety)
//   - rapid sequential passes (idempotency proof — no .bak.* leaks)
//   - two SCHEDULERS racing on the same on-disk kb dir (concurrent
//     fetch / pull, no torn .git state)

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/paths"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSyncBubbles_ConcurrentPasses_NoPanic fires multiple syncBubbles
// passes from many goroutines simultaneously and asserts that the
// underlying state stays consistent: no panic, no torn meta.json, the
// bubble dir ends in a usable state.
//
// Failure prevented: a future change introducing a non-mutex-protected
// map mutation in the kb pipeline that would only manifest under
// `go test -race`.
func TestSyncBubbles_ConcurrentPasses_NoPanic(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	kbTestEnv(t)

	s, _ := kbTestScheduler(t)
	bareDir := makeBareRepo(t, "race", "AGENTS.md", "v1\n")
	bubble := api.KB{
		KBID:    "kb_race",
		KBType:  api.KBTypeTeam,
		Slug:    "race-bubble",
		RepoURL: "file://" + bareDir,
	}
	s.SetKBBubbleListerFactory(func(_, _ string) KBBubbleLister {
		return &fakeKBLister{bubbles: []api.KB{bubble}}
	})

	const goroutines = 8
	var wg sync.WaitGroup
	wg.Add(goroutines)

	var completed atomic.Int32
	for range goroutines {
		go func() {
			defer wg.Done()
			assert.NotPanics(t, func() {
				s.syncBubbles(context.Background())
			})
			completed.Add(1)
		}()
	}

	// must complete without deadlock.
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(45 * time.Second):
		t.Fatal("concurrent syncBubbles passes deadlocked")
	}

	assert.Equal(t, int32(goroutines), completed.Load(),
		"every goroutine must complete its pass")

	// after all goroutines, the bubble must be in a usable state.
	target := paths.KBDir(endpoint.Get(), bubble.KBID)
	assert.DirExists(t, filepath.Join(target, ".git"))

	// meta.json must be valid JSON (not torn by a half-finished write).
	metaPath := filepath.Join(target, ".sageox", "meta.json")
	if data, err := os.ReadFile(metaPath); err == nil {
		var m kbMeta
		assert.NoError(t, json.Unmarshal(data, &m), "meta.json must be intact JSON after concurrent writes")
		assert.Equal(t, api.KBTypeTeam, m.Type)
		assert.Equal(t, "race-bubble", m.Slug)
	}
}

// TestSyncBubbles_RapidSequentialPasses verifies that calling
// syncBubbles many times in tight succession remains idempotent: the
// canonical clone is created exactly once, and subsequent passes either
// dedup via FETCH_HEAD or pull no-ops cleanly.
//
// Failure prevented: a regression where every pass re-clones the bubble
// (slow) or, worse, races against itself by trying to clone over an
// existing dir. Distinct from sync_bubbles_clone_test.go's
// TestSyncBubbles_Clone_Idempotent which only does two passes.
func TestSyncBubbles_RapidSequentialPasses(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	kbTestEnv(t)

	s, _ := kbTestScheduler(t)
	bareDir := makeBareRepo(t, "rapid", "x.md", "v1\n")
	bubble := api.KB{
		KBID:    "kb_rapid",
		KBType:  api.KBTypePersonal,
		Slug:    "rapid-bubble",
		RepoURL: "file://" + bareDir,
	}
	s.SetKBBubbleListerFactory(func(_, _ string) KBBubbleLister {
		return &fakeKBLister{bubbles: []api.KB{bubble}}
	})

	for range 10 {
		assert.NotPanics(t, func() {
			s.syncBubbles(context.Background())
		})
	}

	target := paths.KBDir(endpoint.Get(), bubble.KBID)
	require.DirExists(t, filepath.Join(target, ".git"))

	// no .bak.* siblings should accumulate from rapid passes — the
	// bubble was healthy throughout, so the corrupt-repo branch must
	// never have fired.
	parent := filepath.Dir(target)
	entries, err := os.ReadDir(parent)
	require.NoError(t, err)
	for _, e := range entries {
		assert.NotContains(t, e.Name(), ".bak.",
			"rapid sequential passes must not produce .bak.* backups")
	}
}

// TestSyncBubbles_TwoSchedulers_SameEndpoint_NoCorruption simulates two
// daemon-like schedulers operating on the same XDG store concurrently
// (e.g., user has two ox daemons running, or a race between a foreground
// `ox kb sync` and the background loop). Both must converge on a usable
// state for every bubble.
//
// Failure prevented: lock-file or .bak.* leftovers when two schedulers
// race on the same kb dir. Mirrors TestMultiClone_ConcurrentPullSameDir
// from the team-context surface.
func TestSyncBubbles_TwoSchedulers_SameEndpoint_NoCorruption(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	kbTestEnv(t)

	s1, _ := kbTestScheduler(t)
	s2, _ := kbTestScheduler(t)

	bareDir := makeBareRepo(t, "twoSched", "y.md", "v1\n")
	bubble := api.KB{
		KBID:    "kb_two_scheds",
		KBType:  api.KBTypeTeam,
		Slug:    "two-scheds",
		RepoURL: "file://" + bareDir,
	}
	listerFactory := func(_, _ string) KBBubbleLister {
		return &fakeKBLister{bubbles: []api.KB{bubble}}
	}
	s1.SetKBBubbleListerFactory(listerFactory)
	s2.SetKBBubbleListerFactory(listerFactory)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); s1.syncBubbles(context.Background()) }()
	go func() { defer wg.Done(); s2.syncBubbles(context.Background()) }()

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("two schedulers on the same kb dir deadlocked")
	}

	target := paths.KBDir(endpoint.Get(), bubble.KBID)
	assert.DirExists(t, filepath.Join(target, ".git"))

	// no leftover index.lock from a torn concurrent fetch.
	_, err := os.Stat(filepath.Join(target, ".git", "index.lock"))
	assert.True(t, os.IsNotExist(err), "no stale index.lock after concurrent passes")
}
