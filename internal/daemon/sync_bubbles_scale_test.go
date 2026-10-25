package daemon

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/paths"
	"github.com/stretchr/testify/require"
)

// --- Scale tests for KB sync ---
//
// Failure prevented: per-bubble FD or wall-time growth that only shows up
// once a user accumulates many KBs (personal + profile + team contexts).
// As of ADR-017 the personal KB is on-by-default for every user, so
// linear-in-N footprint is now a live production concern, not a future
// one. These tests live in a separate file so they're easy to find when
// reasoning about scale behavior.

// TestKBSync_NoFDLeak_AtScale clones 50 small KB repos, then runs a full
// reconcile pass. After settling, asserts that the process FD count is
// within tolerance of the pre-sync baseline.
//
// Failure prevented: a regression where KB sync starts holding a per-KB
// long-lived FD (file watcher, sql.DB, flock, anything) — the
// post-PR-#619 audit established that per-KB FD cost at rest is ~0, and
// we want any change to that invariant to trip immediately.
func TestKBSync_NoFDLeak_AtScale(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("FD enumeration unavailable on Windows")
	}
	if testing.Short() {
		t.Skip("short: clones 50 git repos")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	kbTestEnv(t)

	s, _ := kbTestScheduler(t)

	const numKBs = 50
	bubbles := make([]api.KB, numKBs)
	for i := 0; i < numKBs; i++ {
		bareDir := makeBareRepo(t, fmt.Sprintf("kb-%03d", i), "notes.md", fmt.Sprintf("kb-%d\n", i))
		bubbles[i] = api.KB{
			KBID:    fmt.Sprintf("kb_%03d", i),
			KBType:  api.KBTypePersonal,
			Slug:    fmt.Sprintf("personal-%03d", i),
			RepoURL: "file://" + bareDir,
		}
	}
	s.SetKBBubbleListerFactory(func(_, _ string) KBBubbleLister {
		return &fakeKBLister{bubbles: bubbles}
	})

	// Baseline AFTER fixture setup (git init for 50 bare repos opens and
	// closes many FDs internally) and BEFORE the reconcile under test.
	baseline := countOpenFDs(t)

	start := time.Now()
	s.syncBubbles(context.Background())
	firstPass := time.Since(start)

	// Run a second pass — a regression where reconcile holds an FD beyond
	// the loop body would compound across passes.
	start = time.Now()
	s.syncBubbles(context.Background())
	secondPass := time.Since(start)

	final := countOpenFDs(t)
	delta := final - baseline

	// Tolerance: kbTestEnv + scheduler may open a few transient logger /
	// HTTP / git-subprocess pipe FDs we don't fully control. A regression
	// where a single FD per KB leaks would produce delta >= numKBs, far
	// above tolerance.
	const tolerance = 15
	if delta >= tolerance {
		t.Errorf("FD count grew by %d after reconciling %d KBs (baseline=%d, final=%d). "+
			"Per-KB leak would be ~%d. Tolerance: <%d. First pass: %v, second pass: %v.",
			delta, numKBs, baseline, final, numKBs, tolerance, firstPass, secondPass)
	}

	// Soft perf check: a regression where reconcile becomes per-KB
	// O(network) or quadratic in N would balloon wall time. We don't
	// gate on a strict bound because CI can be noisy, but we want the
	// numbers visible in test output so a slowdown is noticed early.
	t.Logf("KB sync at N=%d: first pass=%v, second pass=%v, FD delta=%d (baseline=%d → final=%d)",
		numKBs, firstPass, secondPass, delta, baseline, final)
}

// TestKBSync_SecondPass_IsFastAndIdempotent verifies that a second reconcile
// preserves existing clones and skips fetches. Timing remains diagnostic:
// the steady path still checks remote refs and reapplies sparse checkout,
// so its wall-time ratio to cloning varies with subprocess scheduling.
//
// Failure prevented: a user with N>>1 KBs has their daemon spend
// significant wall time on each sync tick, blocking the team-context
// pull loop and producing visible UI lag.
func TestKBSync_SecondPass_IsFastAndIdempotent(t *testing.T) {
	if testing.Short() {
		t.Skip("short: clones git repos")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	kbTestEnv(t)

	s, _ := kbTestScheduler(t)

	const numKBs = 20
	bubbles := make([]api.KB, numKBs)
	for i := 0; i < numKBs; i++ {
		bareDir := makeBareRepo(t, fmt.Sprintf("kb-%03d", i), "notes.md", "x\n")
		bubbles[i] = api.KB{
			KBID:    fmt.Sprintf("kb_%03d", i),
			KBType:  api.KBTypePersonal,
			Slug:    fmt.Sprintf("personal-%03d", i),
			RepoURL: "file://" + bareDir,
		}
	}
	lister := &fakeKBLister{bubbles: bubbles}
	s.SetKBBubbleListerFactory(func(_, _ string) KBBubbleLister { return lister })

	// First pass: cold clone of N KBs.
	start := time.Now()
	s.syncBubbles(context.Background())
	clonePass := time.Since(start)

	// Snapshot the actual fetch state and add a local marker that a re-clone
	// would lose. This tests the work avoided without inferring it from timing.
	fetchTimes := make(map[string]time.Time, numKBs)
	for _, b := range bubbles {
		target := paths.KBDir(endpoint.Get(), b.KBID)
		require.NoError(t, os.WriteFile(filepath.Join(target, ".git", "steady-pass-sentinel"), []byte("keep"), 0o600))
		info, err := os.Stat(filepath.Join(target, ".git", "FETCH_HEAD"))
		require.NoError(t, err)
		fetchTimes[target] = info.ModTime()
	}

	// Second pass: remote-ref checks are expected, but no clone or fetch.
	start = time.Now()
	s.syncBubbles(context.Background())
	steadyPass := time.Since(start)

	t.Logf("KB sync timing at N=%d: clone=%v, steady=%v", numKBs, clonePass, steadyPass)

	for target, before := range fetchTimes {
		marker, err := os.ReadFile(filepath.Join(target, ".git", "steady-pass-sentinel"))
		require.NoError(t, err, "steady pass must preserve existing clone %s", target)
		require.Equal(t, "keep", string(marker))
		info, err := os.Stat(filepath.Join(target, ".git", "FETCH_HEAD"))
		require.NoError(t, err)
		require.Equal(t, before, info.ModTime(), "steady pass must not fetch %s", target)
	}

	// And the API should be called exactly twice — once per syncBubbles
	// invocation. A regression that calls ListBubbles per-KB instead of
	// per-pass would trip immediately.
	if lister.callCnt != 2 {
		t.Errorf("ListBubbles call count = %d, want 2 (one per syncBubbles pass) — "+
			"suspect per-KB API call regression", lister.callCnt)
	}
}
