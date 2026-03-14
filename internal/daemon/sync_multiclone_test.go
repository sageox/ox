package daemon

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sageox/ox/internal/gitserver"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupTeamContextRepo creates a bare remote + local clone for use as a team
// context in multi-clone tests. Returns (bareDir, cloneDir).
func setupTeamContextRepo(t *testing.T) (string, string) {
	t.Helper()
	teamDir := t.TempDir()
	setupGitRepo(t, teamDir)
	bareDir := teamDir + ".bare"

	cloneDir := filepath.Join(t.TempDir(), "team-clone")
	require.NoError(t, exec.Command("git", "clone", bareDir, cloneDir).Run())
	gitConfig(t, cloneDir)
	return bareDir, cloneDir
}

// multiCloneEnv sets up two project directories pointing to the same team
// context clone, with credentials. Returns (project1, project2, scheduler1, scheduler2).
func multiCloneEnv(t *testing.T, teamID, teamCloneDir string) (s1, s2 *SyncScheduler) {
	t.Helper()
	credDir := isolateCredentialsWithDir(t)

	project1 := setupProjectWithConfig(t, fmt.Sprintf(`
[[team_contexts]]
team_id = %q
team_name = "Test Team"
path = %q
`, teamID, teamCloneDir))

	project2 := setupProjectWithConfig(t, fmt.Sprintf(`
[[team_contexts]]
team_id = %q
team_name = "Test Team"
path = %q
`, teamID, teamCloneDir))

	writeCredentialsFile(t, credDir, gitserver.GitCredentials{
		Token:     "test-token",
		ServerURL: "https://git.fake.test.invalid",
		Username:  "oauth2",
		ExpiresAt: time.Now().Add(24 * time.Hour),
		Repos: map[string]gitserver.RepoEntry{
			teamID: {
				Name:   "Test Team",
				Type:   "team-context",
				URL:    "https://git.fake.test.invalid/test-team.git",
				TeamID: teamID,
			},
		},
	})

	return newTestScheduler(project1), newTestScheduler(project2)
}

// mustCompleteWithin runs fn in a goroutine and fails if it doesn't
// complete within timeout. Returns immediately on success.
func mustCompleteWithin(t *testing.T, timeout time.Duration, msg string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	select {
	case <-done:
	case <-time.After(timeout):
		t.Fatalf("timed out after %v: %s", timeout, msg)
	}
}

// ---------------------------------------------------------------------------
// Test 1: Sequential linking (the exact bug report scenario)
// ---------------------------------------------------------------------------

// TestMultiClone_TeamContextLinking verifies that two separate project directories
// (clones) with the same repo_id can both discover and sync a team context via the
// daemon without getting stuck. This is a regression test for the bug where a second
// clone wouldn't link the team context repo until the daemon was restarted.
func TestMultiClone_TeamContextLinking(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	_, teamCloneDir := setupTeamContextRepo(t)
	s1, s2 := multiCloneEnv(t, "team_link", teamCloneDir)

	// make FETCH_HEAD old so sync isn't skipped
	fetchHead := filepath.Join(teamCloneDir, ".git", "FETCH_HEAD")
	oldTime := time.Now().Add(-1 * time.Hour)
	_ = os.Chtimes(fetchHead, oldTime, oldTime)

	ctx := context.Background()

	// Phase 1: scheduler 1 syncs successfully
	s1.pullTeamContexts(ctx)
	tcs1 := s1.WorkspaceRegistry().GetTeamContexts()
	require.NotEmpty(t, tcs1, "scheduler 1 should discover team context")
	assert.True(t, tcs1[0].Exists, "team context should exist on disk after scheduler 1 sync")

	// Phase 2: scheduler 2 must also discover and sync without blocking
	mustCompleteWithin(t, 30*time.Second, "scheduler 2 got stuck on pullTeamContexts", func() {
		s2.pullTeamContexts(ctx)
	})

	tcs2 := s2.WorkspaceRegistry().GetTeamContexts()
	require.NotEmpty(t, tcs2, "scheduler 2 should discover team context")
	assert.True(t, tcs2[0].Exists, "team context should exist on disk for scheduler 2")
}

// ---------------------------------------------------------------------------
// Test 2: Concurrent sync (both pull the same dir at the same time)
// ---------------------------------------------------------------------------

// TestMultiClone_ConcurrentPullSameDir fires two schedulers' pullTeamContexts
// simultaneously on the same git repo directory. Verifies no deadlock, no panic,
// and no corrupted git state from concurrent fetch+pull.
func TestMultiClone_ConcurrentPullSameDir(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	_, teamCloneDir := setupTeamContextRepo(t)
	s1, s2 := multiCloneEnv(t, "team_conc", teamCloneDir)

	// make FETCH_HEAD old so both actually attempt to sync
	fetchHead := filepath.Join(teamCloneDir, ".git", "FETCH_HEAD")
	oldTime := time.Now().Add(-1 * time.Hour)
	_ = os.Chtimes(fetchHead, oldTime, oldTime)

	ctx := context.Background()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); s1.pullTeamContexts(ctx) }()
	go func() { defer wg.Done(); s2.pullTeamContexts(ctx) }()

	mustCompleteWithin(t, 30*time.Second, "concurrent pullTeamContexts deadlocked", func() {
		wg.Wait()
	})

	// both should see the team context (no corruption from concurrent access)
	for i, s := range []*SyncScheduler{s1, s2} {
		tcs := s.WorkspaceRegistry().GetTeamContexts()
		require.NotEmpty(t, tcs, "scheduler %d should see team context", i+1)
		assert.True(t, tcs[0].Exists, "team context should exist for scheduler %d", i+1)
	}

	// git repo should be in a clean state (no rebase-in-progress, no lock files)
	gitDir := filepath.Join(teamCloneDir, ".git")
	_, err := os.Stat(filepath.Join(gitDir, "rebase-merge"))
	assert.True(t, os.IsNotExist(err), "no rebase should be in progress")
	_, err = os.Stat(filepath.Join(gitDir, "index.lock"))
	assert.True(t, os.IsNotExist(err), "no index.lock should be left behind")
}

// ---------------------------------------------------------------------------
// Test 3: Concurrent clone when team context doesn't exist yet
// ---------------------------------------------------------------------------

// TestMultiClone_ConcurrentCloneAttempt verifies that when the team context
// directory doesn't exist, two schedulers both trying to clone it don't
// deadlock or corrupt each other. One clone should succeed; the other should
// either succeed or gracefully detect "already exists".
func TestMultiClone_ConcurrentCloneAttempt(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	credDir := isolateCredentialsWithDir(t)

	// team context path that does NOT exist yet
	teamCloneDir := filepath.Join(t.TempDir(), "nonexistent-team-clone")

	teamID := "team_clone_race"
	project1 := setupProjectWithConfig(t, fmt.Sprintf(`
[[team_contexts]]
team_id = %q
team_name = "Clone Race Team"
path = %q
`, teamID, teamCloneDir))

	project2 := setupProjectWithConfig(t, fmt.Sprintf(`
[[team_contexts]]
team_id = %q
team_name = "Clone Race Team"
path = %q
`, teamID, teamCloneDir))

	writeCredentialsFile(t, credDir, gitserver.GitCredentials{
		Token:     "test-token",
		ServerURL: "https://git.fake.test.invalid",
		Username:  "oauth2",
		ExpiresAt: time.Now().Add(24 * time.Hour),
		Repos: map[string]gitserver.RepoEntry{
			teamID: {
				Name:   "Clone Race Team",
				Type:   "team-context",
				URL:    "https://git.fake.test.invalid/clone-race-team.git",
				TeamID: teamID,
			},
		},
	})

	s1 := newTestScheduler(project1)
	s2 := newTestScheduler(project2)

	ctx := context.Background()

	// both fire concurrently — both will see ws.Exists=false and spawn
	// cloneInBackground goroutines. The key assertion: neither blocks forever.
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); s1.pullTeamContexts(ctx) }()
	go func() { defer wg.Done(); s2.pullTeamContexts(ctx) }()

	// pullTeamContexts itself should not block (clones run in background)
	mustCompleteWithin(t, 30*time.Second, "concurrent clone attempt deadlocked", func() {
		wg.Wait()
	})

	// both schedulers should at least discover the team context in their registry
	// (even if clone hasn't completed yet, the workspace entry should exist)
	for i, s := range []*SyncScheduler{s1, s2} {
		tcs := s.WorkspaceRegistry().GetTeamContexts()
		require.NotEmpty(t, tcs, "scheduler %d should have team context in registry", i+1)
		assert.Equal(t, teamID, tcs[0].TeamID)
	}
}

// ---------------------------------------------------------------------------
// Test 4: Rapid alternating syncs
// ---------------------------------------------------------------------------

// TestMultiClone_RapidAlternatingSync exercises two schedulers alternating
// rapid syncs on the same team context — simulating two terminal sessions
// in different clones repeatedly triggering sync.
func TestMultiClone_RapidAlternatingSync(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	_, teamCloneDir := setupTeamContextRepo(t)
	s1, s2 := multiCloneEnv(t, "team_rapid", teamCloneDir)
	ctx := context.Background()

	const iterations = 5
	for i := range iterations {
		s := s1
		if i%2 == 1 {
			s = s2
		}
		mustCompleteWithin(t, 10*time.Second, fmt.Sprintf("scheduler stuck on iteration %d", i), func() {
			s.pullTeamContexts(ctx)
		})
	}

	for i, s := range []*SyncScheduler{s1, s2} {
		tcs := s.WorkspaceRegistry().GetTeamContexts()
		require.NotEmpty(t, tcs, "scheduler %d should see team context after rapid syncs", i+1)
		assert.True(t, tcs[0].Exists, "team context should exist for scheduler %d", i+1)
	}
}

// ---------------------------------------------------------------------------
// Test 5: Clone semaphore exhaustion doesn't deadlock pull
// ---------------------------------------------------------------------------

// TestMultiClone_SemaphoreExhaustion verifies that when all clone semaphore
// slots are occupied, pullTeamContexts still completes for an already-cloned
// team context (because pull uses pullInProgress, not cloneSem).
func TestMultiClone_SemaphoreExhaustion(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	_, teamCloneDir := setupTeamContextRepo(t)
	s1, _ := multiCloneEnv(t, "team_sem", teamCloneDir)

	// exhaust all clone semaphore slots
	for range maxConcurrentClones {
		s1.cloneSem <- struct{}{}
	}
	t.Cleanup(func() {
		for range maxConcurrentClones {
			<-s1.cloneSem
		}
	})

	ctx := context.Background()

	// sync (pull path) should still work even with clone slots full
	mustCompleteWithin(t, 10*time.Second, "pullTeamContexts blocked by full clone semaphore", func() {
		s1.pullTeamContexts(ctx)
	})

	tcs := s1.WorkspaceRegistry().GetTeamContexts()
	require.NotEmpty(t, tcs, "should see team context despite full clone semaphore")
	assert.True(t, tcs[0].Exists)
}

// ---------------------------------------------------------------------------
// Test 6: Clone backoff recovery
// ---------------------------------------------------------------------------

// TestMultiClone_CloneBackoffRecovery verifies that when a clone fails and
// enters exponential backoff, a second scheduler can still see the team
// context once it becomes available. Specifically tests that backoff state
// on one scheduler doesn't permanently prevent syncing.
func TestMultiClone_CloneBackoffRecovery(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	credDir := isolateCredentialsWithDir(t)

	// start with no clone dir — both schedulers will want to clone
	teamCloneDir := filepath.Join(t.TempDir(), "team-backoff")
	teamID := "team_backoff"

	project1 := setupProjectWithConfig(t, fmt.Sprintf(`
[[team_contexts]]
team_id = %q
team_name = "Backoff Team"
path = %q
`, teamID, teamCloneDir))

	project2 := setupProjectWithConfig(t, fmt.Sprintf(`
[[team_contexts]]
team_id = %q
team_name = "Backoff Team"
path = %q
`, teamID, teamCloneDir))

	writeCredentialsFile(t, credDir, gitserver.GitCredentials{
		Token:     "test-token",
		ServerURL: "https://git.fake.test.invalid",
		Username:  "oauth2",
		ExpiresAt: time.Now().Add(24 * time.Hour),
		Repos: map[string]gitserver.RepoEntry{
			teamID: {
				Name:   "Backoff Team",
				Type:   "team-context",
				URL:    "https://git.fake.test.invalid/backoff-team.git",
				TeamID: teamID,
			},
		},
	})

	s1 := newTestScheduler(project1)
	s2 := newTestScheduler(project2)

	ctx := context.Background()

	// Phase 1: scheduler 1 tries to clone, fails (URL is not real).
	// pullTeamContexts should not block — clone runs in background.
	mustCompleteWithin(t, 30*time.Second, "scheduler 1 pullTeamContexts blocked", func() {
		s1.pullTeamContexts(ctx)
	})

	// scheduler 1 should have the team context in registry but Exists=false
	tcs1 := s1.WorkspaceRegistry().GetTeamContexts()
	require.NotEmpty(t, tcs1)
	assert.False(t, tcs1[0].Exists, "team context should not exist (clone failed)")

	// Phase 2: manually create the clone dir (simulates another process cloning it)
	teamBareDir := filepath.Join(t.TempDir(), "bare-backoff")
	require.NoError(t, exec.Command("git", "init", "--bare", teamBareDir).Run())
	initDir := filepath.Join(t.TempDir(), "init-backoff")
	require.NoError(t, exec.Command("git", "clone", teamBareDir, initDir).Run())
	gitConfig(t, initDir)
	require.NoError(t, os.WriteFile(filepath.Join(initDir, "README.md"), []byte("init"), 0644))
	require.NoError(t, exec.Command("git", "-C", initDir, "add", "README.md").Run())
	require.NoError(t, exec.Command("git", "-C", initDir, "commit", "-m", "init").Run())
	require.NoError(t, exec.Command("git", "-C", initDir, "push", "origin", "HEAD:main").Run())
	require.NoError(t, exec.Command("git", "clone", teamBareDir, teamCloneDir).Run())
	gitConfig(t, teamCloneDir)

	// Phase 3: scheduler 2 should discover the now-existing repo and sync
	mustCompleteWithin(t, 30*time.Second, "scheduler 2 pullTeamContexts blocked after manual clone", func() {
		s2.pullTeamContexts(ctx)
	})

	tcs2 := s2.WorkspaceRegistry().GetTeamContexts()
	require.NotEmpty(t, tcs2)
	assert.True(t, tcs2[0].Exists, "team context should exist for scheduler 2 (manually cloned)")
}

// ---------------------------------------------------------------------------
// Test 7: High-concurrency stress test
// ---------------------------------------------------------------------------

// TestMultiClone_StressConcurrency fires many concurrent pullTeamContexts
// calls from multiple schedulers simultaneously. Exercises mutex, semaphore,
// and registry contention under load. Run with -race to detect data races.
func TestMultiClone_StressConcurrency(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	_, teamCloneDir := setupTeamContextRepo(t)

	credDir := isolateCredentialsWithDir(t)
	teamID := "team_stress"

	// create 4 schedulers (simulating 4 clones of same project)
	const numSchedulers = 4
	schedulers := make([]*SyncScheduler, numSchedulers)
	for i := range numSchedulers {
		project := setupProjectWithConfig(t, fmt.Sprintf(`
[[team_contexts]]
team_id = %q
team_name = "Stress Team"
path = %q
`, teamID, teamCloneDir))
		schedulers[i] = newTestScheduler(project)
	}

	writeCredentialsFile(t, credDir, gitserver.GitCredentials{
		Token:     "test-token",
		ServerURL: "https://git.fake.test.invalid",
		Username:  "oauth2",
		ExpiresAt: time.Now().Add(24 * time.Hour),
		Repos: map[string]gitserver.RepoEntry{
			teamID: {
				Name:   "Stress Team",
				Type:   "team-context",
				URL:    "https://git.fake.test.invalid/stress-team.git",
				TeamID: teamID,
			},
		},
	})

	ctx := context.Background()

	// fire 3 rounds of concurrent syncs from all schedulers
	const rounds = 3
	var completed atomic.Int32

	for round := range rounds {
		var wg sync.WaitGroup
		wg.Add(numSchedulers)
		for _, s := range schedulers {
			go func(sched *SyncScheduler) {
				defer wg.Done()
				sched.pullTeamContexts(ctx)
				completed.Add(1)
			}(s)
		}

		mustCompleteWithin(t, 30*time.Second,
			fmt.Sprintf("stress round %d deadlocked", round),
			func() { wg.Wait() })
	}

	assert.Equal(t, int32(rounds*numSchedulers), completed.Load(),
		"all sync calls should complete without panic or deadlock")

	// verify final state: all schedulers see the team context
	for i, s := range schedulers {
		tcs := s.WorkspaceRegistry().GetTeamContexts()
		require.NotEmpty(t, tcs, "scheduler %d should see team context after stress test", i+1)
		assert.True(t, tcs[0].Exists, "team context should exist for scheduler %d", i+1)
	}

	// git repo should be clean
	gitDir := filepath.Join(teamCloneDir, ".git")
	_, err := os.Stat(filepath.Join(gitDir, "index.lock"))
	assert.True(t, os.IsNotExist(err), "no stale index.lock after stress test")
}

// ---------------------------------------------------------------------------
// Test 8: Checkout TOCTOU on incomplete clone detection
// ---------------------------------------------------------------------------

// TestMultiClone_CheckoutTOCTOU verifies that Checkout's incomplete clone
// detection (checking .git exists but .sageox doesn't) doesn't race with
// concurrent operations. If one scheduler is pulling while another calls
// Checkout, the .sageox check shouldn't cause a valid clone to be renamed aside.
func TestMultiClone_CheckoutTOCTOU(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	_, teamCloneDir := setupTeamContextRepo(t)

	// create .sageox/ in the clone to simulate a complete clone
	sageoxDir := filepath.Join(teamCloneDir, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(sageoxDir, "config.json"), []byte("{}"), 0644))

	credDir := isolateCredentialsWithDir(t)
	teamID := "team_toctou"

	project := setupProjectWithConfig(t, fmt.Sprintf(`
[[team_contexts]]
team_id = %q
team_name = "TOCTOU Team"
path = %q
`, teamID, teamCloneDir))

	writeCredentialsFile(t, credDir, gitserver.GitCredentials{
		Token:     "test-token",
		ServerURL: "https://git.fake.test.invalid",
		Username:  "oauth2",
		ExpiresAt: time.Now().Add(24 * time.Hour),
		Repos: map[string]gitserver.RepoEntry{
			teamID: {
				Name:   "TOCTOU Team",
				Type:   "team-context",
				URL:    "https://git.fake.test.invalid/toctou-team.git",
				TeamID: teamID,
			},
		},
	})

	s := newTestScheduler(project)
	s.cloneSemTimeoutOverride = 2 * time.Second

	// call Checkout on an already-complete clone — should get AlreadyExists=true
	result, err := s.Checkout(CheckoutPayload{
		CloneURL: "https://git.fake.test.invalid/toctou-team.git",
		RepoPath: teamCloneDir,
		RepoType: "team-context",
	}, nil)
	require.NoError(t, err)
	assert.True(t, result.AlreadyExists, "Checkout should detect already-complete clone")
	assert.False(t, result.Cloned, "should not re-clone an existing complete repo")

	// verify the repo was NOT renamed aside
	_, err = os.Stat(filepath.Join(teamCloneDir, ".git"))
	require.NoError(t, err, "original .git should still exist")
	_, err = os.Stat(sageoxDir)
	require.NoError(t, err, "original .sageox should still exist")

	// verify no .bak.* directories were created
	parentDir := filepath.Dir(teamCloneDir)
	entries, err := os.ReadDir(parentDir)
	require.NoError(t, err)
	for _, e := range entries {
		assert.NotContains(t, e.Name(), ".bak.",
			"no backup directories should be created for a complete clone")
	}
}
