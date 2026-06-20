package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/gitserver"
	"github.com/sageox/ox/internal/gitutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSyncScheduler_DoPull_LedgerDirExistsButNotGitRepo(t *testing.T) {
	// simulate a failed clone that left an empty directory behind
	tmpDir := t.TempDir()
	ledgerDir := filepath.Join(tmpDir, "ledger")
	require.NoError(t, os.MkdirAll(ledgerDir, 0755))

	// verify the directory exists but is NOT a git repo
	_, err := os.Stat(ledgerDir)
	require.NoError(t, err)
	assert.False(t, gitutil.IsGitRepo(ledgerDir))

	cfg := DefaultConfig()
	cfg.LedgerPath = ledgerDir
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	scheduler := NewSyncScheduler(cfg, logger)

	// doPull should detect the empty dir is not a git repo and return early
	// (enters the clone branch) rather than falling through to git fetch/pull
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// should not panic — previously this would fall through to git pull
	// on an empty directory since it only checked os.IsNotExist
	scheduler.doPull(ctx, nil, false, true)
}

func TestSyncScheduler_PullInProgress(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	// set up a real bare repo + clone so first doPull actually does work
	bareDir := filepath.Join(t.TempDir(), "bare")
	workDir := filepath.Join(t.TempDir(), "work")
	require.NoError(t, exec.Command("git", "init", "--bare", "--initial-branch=main", bareDir).Run())
	require.NoError(t, exec.Command("git", "clone", bareDir, workDir).Run())
	gitConfig(t, workDir)
	require.NoError(t, exec.Command("git", "-C", workDir, "commit", "--allow-empty", "-m", "init").Run())
	require.NoError(t, exec.Command("git", "-C", workDir, "push", "origin", "main").Run())

	cfg := DefaultConfig()
	cfg.LedgerPath = workDir
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	scheduler := NewSyncScheduler(cfg, logger)

	// use the production concurrency guard: doPull(ctx, nil, false, true) sets
	// pullInProgress=true on entry, false on exit. If we set it ourselves
	// beforehand, doPull returns early without doing anything.
	scheduler.mu.Lock()
	scheduler.pullInProgress = true
	scheduler.mu.Unlock()

	ctx := context.Background()
	scheduler.doPull(ctx, nil, false, true) // should return immediately (guard active)

	// still marked in-progress since doPull bailed before the defer that clears it
	scheduler.mu.Lock()
	assert.True(t, scheduler.pullInProgress, "pull should still be in-progress after early return")
	scheduler.mu.Unlock()

	// now clear and run for real to verify the guard doesn't permanently block
	scheduler.mu.Lock()
	scheduler.pullInProgress = false
	scheduler.mu.Unlock()

	require.NoError(t, scheduler.doPull(ctx, nil, false, true)) // should succeed
	assert.False(t, scheduler.LastSync().IsZero(), "lastSync should be set after real pull")
}

func TestSyncScheduler_PullTeamContexts_PathNotExist(t *testing.T) {
	// isolate from real credentials
	prevConfigDir := gitserver.TestSetConfigDirOverride(t.TempDir())
	prevForceFile := gitserver.TestSetForceFileStorage(true)
	t.Cleanup(func() {
		gitserver.TestSetConfigDirOverride(prevConfigDir)
		gitserver.TestSetForceFileStorage(prevForceFile)
	})

	// create temp project with team context pointing to non-existent path
	tmpDir := t.TempDir()
	sageoxDir := filepath.Join(tmpDir, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755))

	// write config with team context pointing to non-existent path
	configContent := `
[[team_contexts]]
team_id = "test-team"
team_name = "Test Team"
path = "/nonexistent/path/to/team/context"
`
	require.NoError(t, os.WriteFile(filepath.Join(sageoxDir, "config.local.toml"), []byte(configContent), 0644))

	// write project config with fake endpoint to prevent real API calls
	require.NoError(t, os.WriteFile(filepath.Join(sageoxDir, "config.json"),
		[]byte(`{"endpoint":"https://fake.test.invalid"}`), 0644))

	cfg := DefaultConfig()
	cfg.ProjectRoot = tmpDir
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	scheduler := NewSyncScheduler(cfg, logger)
	scheduler.pullTeamContexts(context.Background())

	// should have status entry marked as not existing
	status := scheduler.TeamContextStatus()
	require.Len(t, status, 1)
	assert.Equal(t, "test-team", status[0].TeamID)
	assert.Equal(t, "Test Team", status[0].TeamName)
	assert.False(t, status[0].Exists)
	assert.Equal(t, "path does not exist and no clone URL available", status[0].LastErr)
}

func TestSyncScheduler_PullTeamContext_FetchHeadDeduplication(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	// check if git is available
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	// isolate from real credentials
	prevConfigDir := gitserver.TestSetConfigDirOverride(t.TempDir())
	prevForceFile := gitserver.TestSetForceFileStorage(true)
	t.Cleanup(func() {
		gitserver.TestSetConfigDirOverride(prevConfigDir)
		gitserver.TestSetForceFileStorage(prevForceFile)
	})

	// create temp git repo for team context
	teamDir := t.TempDir()
	setupGitRepo(t, teamDir)

	cfg := DefaultConfig()
	cfg.TeamContextSyncInterval = 10 * time.Minute // long interval
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	scheduler := NewSyncScheduler(cfg, logger)

	// first pull should succeed
	err := scheduler.pullTeamContext(context.Background(), teamDir)
	assert.NoError(t, err)

	// simulate recent FETCH_HEAD by touching the file
	fetchHead := filepath.Join(teamDir, ".git", "FETCH_HEAD")
	// the file already exists from the first fetch, but let's make sure it's recent
	require.NoError(t, os.WriteFile(fetchHead, []byte("fake-sha1\t\trefs/heads/main\n"), 0644))

	// second pull should be skipped due to recent fetch
	err = scheduler.pullTeamContext(context.Background(), teamDir)
	assert.NoError(t, err) // returns nil when skipped
}

func TestSyncScheduler_PullTeamContext_NotGitRepo(t *testing.T) {
	// create temp directory that is NOT a git repo
	tmpDir := t.TempDir()
	tcPath := filepath.Join(tmpDir, "team-ctx")
	require.NoError(t, os.MkdirAll(tcPath, 0755))

	cfg := DefaultConfig()
	cfg.TeamContextSyncInterval = 10 * time.Minute
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	scheduler := NewSyncScheduler(cfg, logger)

	// pull should handle gracefully: detect invalid repo, move aside for re-clone.
	// It returns an error so callers report the team context as not-usable (the
	// path is gone until the next cycle re-clones it) rather than "synced".
	err := scheduler.pullTeamContext(context.Background(), tcPath)
	assert.Error(t, err, "moving a corrupt repo aside must surface as not-usable, not success")
	// original path should be gone (moved to .bak)
	assert.NoDirExists(t, tcPath)
}

func TestSyncScheduler_TeamContextIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	// check if git is available
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	// isolate from real credentials
	prevConfigDir := gitserver.TestSetConfigDirOverride(t.TempDir())
	prevForceFile := gitserver.TestSetForceFileStorage(true)
	t.Cleanup(func() {
		gitserver.TestSetConfigDirOverride(prevConfigDir)
		gitserver.TestSetForceFileStorage(prevForceFile)
	})

	// create temp project directory
	projectDir := t.TempDir()
	sageoxDir := filepath.Join(projectDir, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755))

	// create temp git repo for team context
	teamDir := t.TempDir()
	setupGitRepo(t, teamDir)

	// write config with team context
	configContent := fmt.Sprintf(`
[[team_contexts]]
team_id = "test-team"
team_name = "Test Team"
path = %q
`, teamDir)
	require.NoError(t, os.WriteFile(filepath.Join(sageoxDir, "config.local.toml"), []byte(configContent), 0644))

	cfg := DefaultConfig()
	cfg.ProjectRoot = projectDir
	cfg.TeamContextSyncInterval = time.Minute // must be > 0 but we'll call pullTeamContexts directly
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	scheduler := NewSyncScheduler(cfg, logger)

	// prevent refreshCredentialsIfNeeded and discoverTeams from calling real API
	scheduler.mu.Lock()
	scheduler.lastCredentialRefresh = time.Now()
	scheduler.lastTeamDiscovery = time.Now()
	scheduler.mu.Unlock()

	// manually touch FETCH_HEAD to be old so the sync isn't skipped
	fetchHead := filepath.Join(teamDir, ".git", "FETCH_HEAD")
	oldTime := time.Now().Add(-1 * time.Hour)
	_ = os.Chtimes(fetchHead, oldTime, oldTime)

	// run team context sync
	scheduler.pullTeamContexts(context.Background())

	// verify status
	status := scheduler.TeamContextStatus()
	require.Len(t, status, 1)
	assert.Equal(t, "test-team", status[0].TeamID)
	assert.Equal(t, "Test Team", status[0].TeamName)
	assert.True(t, status[0].Exists)
	assert.Empty(t, status[0].LastErr)
	assert.False(t, status[0].LastSync.IsZero())
}

func TestSyncScheduler_WorkspaceRegistry(t *testing.T) {
	// isolate from real credentials - use empty temp dir
	prevConfigDir := gitserver.TestSetConfigDirOverride(t.TempDir())
	prevForceFile := gitserver.TestSetForceFileStorage(true)
	t.Cleanup(func() {
		gitserver.TestSetConfigDirOverride(prevConfigDir)
		gitserver.TestSetForceFileStorage(prevForceFile)
	})

	// create temp project with ledger and team context
	tmpDir := t.TempDir()
	sageoxDir := filepath.Join(tmpDir, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755))

	ledgerDir := filepath.Join(t.TempDir(), "ledger")
	require.NoError(t, os.MkdirAll(filepath.Join(ledgerDir, ".git"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(ledgerDir, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0644))

	teamDir := filepath.Join(t.TempDir(), "team-context")
	require.NoError(t, os.MkdirAll(filepath.Join(teamDir, ".git"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(teamDir, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0644))

	// write config
	configContent := fmt.Sprintf(`
[ledger]
path = %q

[[team_contexts]]
team_id = "team-abc"
team_name = "Team ABC"
path = %q
`, ledgerDir, teamDir)
	require.NoError(t, os.WriteFile(filepath.Join(sageoxDir, "config.local.toml"), []byte(configContent), 0644))

	cfg := DefaultConfig()
	cfg.ProjectRoot = tmpDir
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	scheduler := NewSyncScheduler(cfg, logger)

	// get the workspace registry
	registry := scheduler.WorkspaceRegistry()
	require.NotNil(t, registry)

	// load config
	require.NoError(t, registry.LoadFromConfig())

	// verify ledger is tracked
	ledger := registry.GetLedger()
	require.NotNil(t, ledger)
	assert.Equal(t, ledgerDir, ledger.Path)
	assert.True(t, ledger.Exists)

	// verify team context is tracked
	teamContexts := registry.GetTeamContexts()
	require.Len(t, teamContexts, 1)
	assert.Equal(t, "team-abc", teamContexts[0].TeamID)
	assert.Equal(t, "Team ABC", teamContexts[0].TeamName)
	assert.True(t, teamContexts[0].Exists)

	// test error tracking
	registry.SetWorkspaceError("team-abc", "test error")
	tc := registry.GetWorkspace("team-abc")
	require.NotNil(t, tc)
	assert.Equal(t, "test error", tc.LastErr)

	registry.ClearWorkspaceError("team-abc")
	tc = registry.GetWorkspace("team-abc")
	assert.Empty(t, tc.LastErr)
}

func TestDoPull_SkipsWhenRemoteUnchanged(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	tmpDir := t.TempDir()
	ledgerDir := filepath.Join(tmpDir, "ledger")
	require.NoError(t, os.MkdirAll(ledgerDir, 0755))
	setupGitRepo(t, ledgerDir)

	cfg := DefaultConfig()
	cfg.LedgerPath = ledgerDir
	cfg.SyncIntervalRead = 1 * time.Second
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	scheduler := NewSyncScheduler(cfg, logger)

	ctx := context.Background()

	// first pull should succeed (fetches, finds nothing new or syncs)
	err := scheduler.doPull(ctx, nil, false, true)
	assert.NoError(t, err)

	// second pull should be skipped by ls-remote check (remote unchanged)
	err = scheduler.doPull(ctx, nil, false, true)
	assert.NoError(t, err) // no error — just skipped
}
