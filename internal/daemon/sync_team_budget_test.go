package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTeamBudgetScheduler builds a scheduler over teamCount existing (but
// empty) team context directories. The pull itself is faked via runTeamPull,
// so no git or network is involved and the tests run under -short.
func newTeamBudgetScheduler(t *testing.T, teamCount int) (*SyncScheduler, []string, *lockedBuffer) {
	t.Helper()

	// discoverTeams reads live credentials; keep the test off the real machine
	isolated := t.TempDir()
	t.Setenv("HOME", isolated)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(isolated, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(isolated, "data"))

	projectRoot := t.TempDir()
	var cfgTOML strings.Builder
	teamIDs := make([]string, 0, teamCount)
	for i := range teamCount {
		teamID := fmt.Sprintf("team_budget_%d", i)
		teamPath := filepath.Join(t.TempDir(), "team-context")
		require.NoError(t, os.MkdirAll(teamPath, 0o755))
		// the registry's Exists check is IsGitRepo; an init is enough
		initCmd := exec.Command("git", "init", "-q", teamPath)
		initOut, initErr := initCmd.CombinedOutput()
		require.NoError(t, initErr, "git init: %s", initOut)
		fmt.Fprintf(&cfgTOML, "[[team_contexts]]\nteam_id = '%s'\nteam_name = 'Budget %d'\npath = '%s'\nlast_sync = 1970-01-01T00:00:00Z\nlast_gc = 1970-01-01T00:00:00Z\n\n", teamID, i, teamPath)
		teamIDs = append(teamIDs, teamID)
	}
	require.NoError(t, os.MkdirAll(filepath.Join(projectRoot, ".sageox"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(projectRoot, ".sageox", "config.local.toml"), []byte(cfgTOML.String()), 0o600))

	logs := &lockedBuffer{}
	cfg := DefaultConfig()
	cfg.ProjectRoot = projectRoot
	logger := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return NewSyncScheduler(cfg, logger), teamIDs, logs
}

// overrideTeamBudgets swaps the package-level budgets and the pull seam for
// the duration of one test. Tests using it must not run in parallel.
func overrideTeamBudgets(t *testing.T, perTeam, cycle time.Duration, pull func(ctx context.Context, path string) (teamPullOutcome, error)) {
	t.Helper()
	prevPer, prevCycle, prevRun := teamPullTimeout, teamCycleTimeout, runTeamPull
	teamPullTimeout, teamCycleTimeout = perTeam, cycle
	runTeamPull = func(_ *SyncScheduler, ctx context.Context, path string) (teamPullOutcome, error) {
		return pull(ctx, path)
	}
	t.Cleanup(func() {
		teamPullTimeout, teamCycleTimeout, runTeamPull = prevPer, prevCycle, prevRun
	})
}

func resultsByPath(results []TeamSyncResult) map[string]TeamSyncResult {
	byPath := make(map[string]TeamSyncResult, len(results))
	for _, r := range results {
		byPath[r.Path] = r
	}
	return byPath
}

// One slow team must exhaust only its own budget. On the shared-budget code
// the slow pull holds the single 60s context open, so every sibling that
// finishes after it inherits the expiry and fails.
func TestDoTeamSync_SlowTeamDoesNotFailOthers(t *testing.T) {
	scheduler, _, _ := newTeamBudgetScheduler(t, 4)
	var slowTeamPath atomic.Value
	var slowElapsed atomic.Int64
	overrideTeamBudgets(t, 150*time.Millisecond, 5*time.Second, func(ctx context.Context, path string) (teamPullOutcome, error) {
		if p, _ := slowTeamPath.Load().(string); p == path {
			begin := time.Now()
			<-ctx.Done() // blocks until its own budget expires
			slowElapsed.Store(int64(time.Since(begin)))
			return teamPullOutcome{PullRan: true}, fmt.Errorf("pull failed: %w", ctx.Err())
		}
		// each sibling fits its own budget, but the one queued behind the
		// semaphore finishes after the slow team's budget has expired
		select {
		case <-time.After(100 * time.Millisecond):
		case <-ctx.Done():
			return teamPullOutcome{PullRan: true}, fmt.Errorf("pull failed: %w", ctx.Err())
		}
		return teamPullOutcome{PullRan: true}, nil
	})

	require.NoError(t, scheduler.workspaceRegistry.LoadFromConfig())
	contexts := scheduler.workspaceRegistry.GetTeamContexts()
	require.Len(t, contexts, 4)
	slowTeamPath.Store(contexts[0].Path)

	cycleCtx, cancel := context.WithTimeout(context.Background(), teamCycleTimeout)
	defer cancel()
	results, err := scheduler.doTeamSync(cycleCtx, nil, true)
	require.NoError(t, err)
	require.Len(t, results, 4)

	byPath := resultsByPath(results)
	assert.Equal(t, "error", byPath[contexts[0].Path].Status, "the slow team really timed out")
	for _, ws := range contexts[1:] {
		assert.Equal(t, "synced", byPath[ws.Path].Status, "team %s must not inherit the slow team's expiry", ws.TeamID)
	}
	failures, _ := scheduler.workspaceRegistry.GetSyncRetryInfo(contexts[1].TeamID)
	assert.Zero(t, failures, "healthy teams must not enter backoff")

	// the slow pull must be cut off by its own 150ms budget, not by the 5s
	// cycle context; without a per-team timeout this would run ~5s
	assert.Less(t, time.Duration(slowElapsed.Load()), 2*time.Second, "slow team expired on the cycle budget, not its own")
	assert.Greater(t, time.Duration(slowElapsed.Load()), time.Duration(0), "slow team pull never ran")
}

func TestDoTeamSync_ParallelismIsBounded(t *testing.T) {
	scheduler, _, _ := newTeamBudgetScheduler(t, 8)

	var running, peak atomic.Int32
	overrideTeamBudgets(t, 5*time.Second, 30*time.Second, func(ctx context.Context, path string) (teamPullOutcome, error) {
		n := running.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(60 * time.Millisecond)
		running.Add(-1)
		return teamPullOutcome{PullRan: true}, nil
	})

	results, err := scheduler.doTeamSync(context.Background(), nil, true)
	require.NoError(t, err)
	require.Len(t, results, 8)
	for _, r := range results {
		assert.Equal(t, "synced", r.Status, r.Error)
	}
	assert.LessOrEqual(t, int(peak.Load()), teamPullConcurrency, "concurrent pulls must never exceed the semaphore size")
	assert.Greater(t, int(peak.Load()), 1, "pulls should still overlap")
}

// Teams the cycle never reached were not tried, so they must not be recorded
// as failures or enter backoff, and the cycle logs exactly one line for them.
func TestDoTeamSync_ExhaustedCycleSkipsRemainingTeams(t *testing.T) {
	scheduler, _, logs := newTeamBudgetScheduler(t, 6)

	var started atomic.Int32
	overrideTeamBudgets(t, 5*time.Second, 200*time.Millisecond, func(ctx context.Context, path string) (teamPullOutcome, error) {
		started.Add(1)
		<-ctx.Done() // hold a slot until the cycle budget runs out
		return teamPullOutcome{PullRan: true}, fmt.Errorf("pull failed: %w", ctx.Err())
	})

	cycleCtx, cancel := context.WithTimeout(context.Background(), teamCycleTimeout)
	defer cancel()
	results, err := scheduler.doTeamSync(cycleCtx, nil, true)
	require.NoError(t, err)
	require.Len(t, results, 6)

	var skipped, failed int
	for _, r := range results {
		switch r.Status {
		case "skipped":
			skipped++
			assert.Contains(t, r.Error, "budget", "skip reason should be visible to callers")
			failures, _ := scheduler.workspaceRegistry.GetSyncRetryInfo(r.TeamID)
			assert.Zero(t, failures, "skipped team %s must not record a sync failure", r.TeamID)
		case "error":
			failed++
		}
	}
	assert.Equal(t, 6-teamPullConcurrency, skipped, "teams beyond the semaphore never started")
	assert.Equal(t, teamPullConcurrency, failed, "teams whose pull ran and timed out keep failure handling")
	assert.EqualValues(t, teamPullConcurrency, started.Load())

	out := logs.String()
	assert.Equal(t, 1, strings.Count(out, "team sync budget exhausted"), "one summary line per cycle, got:\n%s", out)
	assert.Contains(t, out, fmt.Sprintf("skipped=%d", 6-teamPullConcurrency))
}

func TestApplySparseCheckout_SkipsQuietlyOnDoneContext(t *testing.T) {
	scheduler, _, logs := newTeamBudgetScheduler(t, 1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".sageox"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".sageox", "sync.manifest"), []byte("allow=/\n"), 0o600))

	cfg := scheduler.applySparseCheckout(ctx, dir)
	assert.NotNil(t, cfg, "manifest config is still returned so sync intervals apply")
	assert.NotContains(t, logs.String(), "level=WARN", "a done context must not produce a WARN")
	assert.NotContains(t, logs.String(), "sparse-checkout set failed")
}

// Background cycle and on-demand TeamSync are separate doTeamSync calls; the
// pull cap must hold across both, not per call.
func TestDoTeamSync_ParallelismBoundedAcrossOverlappingCalls(t *testing.T) {
	scheduler, _, _ := newTeamBudgetScheduler(t, 6)

	var running, peak atomic.Int32
	overrideTeamBudgets(t, 5*time.Second, 30*time.Second, func(ctx context.Context, path string) (teamPullOutcome, error) {
		n := running.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(80 * time.Millisecond)
		running.Add(-1)
		return teamPullOutcome{PullRan: true}, nil
	})

	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := scheduler.doTeamSync(context.Background(), nil, true)
			assert.NoError(t, err)
		}()
	}
	wg.Wait()

	assert.LessOrEqual(t, int(peak.Load()), teamPullConcurrency, "overlapping syncs must share the pull slots")
}
