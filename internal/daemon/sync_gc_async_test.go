package daemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sageox/ox/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// primeConfigCacheLocked marks the registry's config cache as freshly
// loaded so a subsequent LoadFromConfig() call (e.g. inside TriggerGC)
// hits the cache short-circuit instead of calling rebuildFromConfigLocked,
// which prunes any workspace not declared in config.local.toml. Tests that
// inject a WorkspaceState directly into registry.workspaces (bypassing the
// config file) need this so TriggerGC's internal LoadFromConfig() doesn't
// wipe the fixture before runTriggerGC iterates it. Caller must hold
// registry.mu.
func primeConfigCacheLocked(registry *WorkspaceRegistry) {
	registry.localConfigCache = &config.LocalConfig{}
	registry.localConfigLoadedAt = time.Now()
}

// --- A. TriggerGCAsync single-flight + non-blocking contract ---

// TestTriggerGCAsync_SingleFlight pins the contract that TriggerGCAsync
// returns immediately (BackgroundStarted=true) without blocking on the
// reclone, that a concurrent second call while GC is in flight sees
// AlreadyRunning=true instead of starting a second sweep, and that
// gcInProgress is released once the background goroutine finishes.
//
// Failure prevented: a regression that drops the single-flight guard or
// blocks on the reclone before returning would reintroduce the exact bug
// this issue fixes — `ox doctor --gc` hanging on the IPC read deadline.
func TestTriggerGCAsync_SingleFlight(t *testing.T) {
	projectDir := setupProjectWithConfig(t, "# empty\n")
	s := newTestScheduler(projectDir)

	release := make(chan struct{})
	started := make(chan struct{})
	var startedOnce sync.Once
	var hookCalls int32
	s.gcAsyncTestHook = func() {
		atomic.AddInt32(&hookCalls, 1)
		startedOnce.Do(func() { close(started) })
		<-release
	}

	ctx := context.Background()

	callStart := time.Now()
	resp1 := s.TriggerGCAsync(ctx)
	callElapsed := time.Since(callStart)
	require.True(t, resp1.BackgroundStarted, "first call must report BackgroundStarted")
	require.False(t, resp1.AlreadyRunning, "first call must NOT be AlreadyRunning")
	assert.Less(t, callElapsed, 500*time.Millisecond,
		"TriggerGCAsync must return essentially immediately, not block on the reclone")

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("background goroutine did not reach the test hook in time")
	}

	// Concurrent calls while the first goroutine is parked in the hook must
	// all see AlreadyRunning — none may start a second sweep.
	const concurrent = 10
	results := make([]*TriggerGCResponse, concurrent)
	var wg sync.WaitGroup
	for i := range concurrent {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = s.TriggerGCAsync(ctx)
		}()
	}
	wg.Wait()

	for i, r := range results {
		assert.True(t, r.AlreadyRunning, "concurrent call %d must see AlreadyRunning while GC is in flight", i)
		assert.False(t, r.BackgroundStarted, "concurrent call %d must not start a second sweep", i)
	}
	assert.Equal(t, int32(1), atomic.LoadInt32(&hookCalls), "only one goroutine should ever reach the hook")

	// release the parked goroutine and wait (no sleep) for it to drain
	close(release)
	require.Eventually(t, func() bool {
		return atomic.LoadInt32(&s.gcInProgress) == 0
	}, 2*time.Second, 10*time.Millisecond, "gcInProgress must be released after the background goroutine finishes")

	// Cleanup discipline: bound how long we wait for the tracked goroutine.
	s.waitClones(2 * time.Second)
}

// --- B. Synchronous TriggerGC unaffected by the async refactor ---

// TestTriggerGC_RemainsSynchronousAfterRefactor proves TriggerGC's external
// contract is unchanged by the runTriggerGC extraction: it still blocks
// until the reclone finishes, still returns the same legacy fields, and
// never sets the new async-only fields. CLI binaries that predate
// trigger_gc_async depend on this: their `ox doctor --gc` prints the
// reclone counts from this response.
func TestTriggerGC_RemainsSynchronousAfterRefactor(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	isolateCredentials(t)

	tmp := t.TempDir()
	bareDir, cloneDir := gcInitBareAndClone(t, tmp)
	cloneURL := "file://" + bareDir

	for _, f := range []string{"SOUL.md", ".sageox/config.json"} {
		fullPath := filepath.Join(cloneDir, f)
		require.NoError(t, os.MkdirAll(filepath.Dir(fullPath), 0755))
		require.NoError(t, os.WriteFile(fullPath, []byte("content"), 0644))
	}
	require.NoError(t, exec.Command("git", "-C", cloneDir, "add", ".").Run())
	require.NoError(t, exec.Command("git", "-C", cloneDir, "commit", "-m", "add structure").Run())
	require.NoError(t, exec.Command("git", "-C", cloneDir, "push", "origin", "HEAD:main").Run())

	projectDir := setupProjectWithConfig(t, "# empty\n")
	s := newTestScheduler(projectDir)
	ctx := context.Background()

	ws := &WorkspaceState{
		ID:       "team-test",
		Type:     WorkspaceTypeTeamContext,
		TeamName: "test-team",
		Path:     cloneDir,
		CloneURL: cloneURL,
		Exists:   true,
	}
	registry := s.WorkspaceRegistry()
	registry.mu.Lock()
	registry.workspaces[ws.ID] = ws
	primeConfigCacheLocked(registry)
	registry.mu.Unlock()

	resp := s.TriggerGC(ctx)

	assert.Equal(t, 1, resp.Triggered, "sync TriggerGC must still perform the reclone inline")
	assert.Empty(t, resp.Errors)
	assert.False(t, resp.BackgroundStarted, "sync path must never set the async-only field")
	assert.False(t, resp.AlreadyRunning, "sync path must never set the async-only field")
	assert.Equal(t, int32(0), atomic.LoadInt32(&s.gcInProgress),
		"gcInProgress must be released synchronously before TriggerGC returns")

	// the reclone must have actually completed by the time TriggerGC
	// returns — the invariant those older CLIs depend on.
	assert.FileExists(t, filepath.Join(cloneDir, "SOUL.md"))
	assert.NoDirExists(t, cloneDir+".new")
	assert.NoDirExists(t, cloneDir+".old")
}

// --- C. IssueTypeGCFailed wiring ---

// TestTriggerGC_IssueTypeGCFailed_SetOnFailureClearedOnSuccess proves a
// gcFailed reclone result surfaces via the IssueTracker (visible through
// `ox daemon status`) and is cleared on the next successful reclone for
// the same repo.
//
// Failure prevented: without this wiring, a background GC failure
// (triggered via TriggerGCAsync) is silently discarded — the goroutine's
// return value is never read by anyone, so a failing reclone would retry
// forever with zero user-visible signal.
func TestTriggerGC_IssueTypeGCFailed_SetOnFailureClearedOnSuccess(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	manifest := "version 1\ninclude .sageox/\ninclude SOUL.md\n"
	bareDir, teamDir, scheduler := setupClonedTeamContext(t, manifest, nil)
	scheduler.issues = NewIssueTracker()
	ctx := context.Background()

	// Phase 1: CloneURL is bad, so the reclone itself fails (push/capture
	// phases succeed against the real origin — only the fresh clone fails).
	badWS := &WorkspaceState{
		ID:       "team_test",
		Type:     WorkspaceTypeTeamContext,
		TeamName: "test-team",
		Path:     teamDir,
		CloneURL: "file:///nonexistent/repo.git",
		Exists:   true,
	}
	registry := scheduler.WorkspaceRegistry()
	registry.mu.Lock()
	registry.workspaces[badWS.ID] = badWS
	// Primed once — the 30s cache window comfortably covers both TriggerGC
	// calls below, so goodWS's injection doesn't need to re-prime it.
	primeConfigCacheLocked(registry)
	registry.mu.Unlock()

	resp1 := scheduler.TriggerGC(ctx)
	require.Len(t, resp1.Errors, 1)
	assert.Contains(t, resp1.Errors[0], "reclone failed")

	issues := scheduler.issues.GetIssues()
	require.Len(t, issues, 1, "gcFailed must surface exactly one issue")
	assert.Equal(t, IssueTypeGCFailed, issues[0].Type)
	assert.Equal(t, "test-team", issues[0].Repo)
	assert.Equal(t, SeverityError, issues[0].Severity)

	// Phase 2: fix the CloneURL and trigger again — success must clear the issue.
	goodWS := &WorkspaceState{
		ID:       "team_test",
		Type:     WorkspaceTypeTeamContext,
		TeamName: "test-team",
		Path:     teamDir,
		CloneURL: "file://" + bareDir,
		Exists:   true,
	}
	registry.mu.Lock()
	registry.workspaces[goodWS.ID] = goodWS
	registry.mu.Unlock()

	resp2 := scheduler.TriggerGC(ctx)
	assert.Equal(t, 1, resp2.Triggered)
	assert.Empty(t, resp2.Errors)
	assert.Empty(t, scheduler.issues.GetIssues(), "IssueTypeGCFailed must clear on the next successful reclone")
}
