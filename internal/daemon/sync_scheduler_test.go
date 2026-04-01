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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewSyncScheduler(t *testing.T) {
	cfg := DefaultConfig()
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	scheduler := NewSyncScheduler(cfg, logger)

	assert.NotNil(t, scheduler)
	assert.Equal(t, cfg, scheduler.config)
	assert.Equal(t, logger, scheduler.logger)
	assert.NotNil(t, scheduler.triggerChan)
}

func TestSyncScheduler_TriggerSync(t *testing.T) {
	cfg := DefaultConfig()
	scheduler := NewSyncScheduler(cfg, nil)

	// first trigger should succeed
	scheduler.TriggerSync()

	// channel should have one item
	select {
	case <-scheduler.triggerChan:
		// expected
	default:
		t.Fatal("trigger channel should have one item")
	}

	// second trigger should not block (channel is buffered with size 1)
	done := make(chan bool)
	go func() {
		scheduler.TriggerSync()
		done <- true
	}()

	select {
	case <-done:
		// expected
	case <-time.After(5 * time.Second):
		t.Fatal("trigger should not block")
	}
}

func TestSyncScheduler_LastSync(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	// set up a real bare repo + clone so doPull exercises production code
	bareDir := filepath.Join(t.TempDir(), "bare")
	workDir := filepath.Join(t.TempDir(), "work")
	require.NoError(t, exec.Command("git", "init", "--bare", "--initial-branch=main", bareDir).Run())
	require.NoError(t, exec.Command("git", "clone", bareDir, workDir).Run())
	gitConfig(t, workDir)
	// seed an initial commit so ls-remote has a ref
	require.NoError(t, exec.Command("git", "-C", workDir, "commit", "--allow-empty", "-m", "init").Run())
	require.NoError(t, exec.Command("git", "-C", workDir, "push", "origin", "main").Run())

	cfg := DefaultConfig()
	cfg.LedgerPath = workDir
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	scheduler := NewSyncScheduler(cfg, logger)

	// initially zero
	assert.True(t, scheduler.LastSync().IsZero())

	// doPull → ls-remote detects "remote unchanged" → updates lastSync
	ctx := context.Background()
	require.NoError(t, scheduler.doPull(ctx, nil, false, true))

	assert.False(t, scheduler.LastSync().IsZero(), "lastSync should be set after doPull")
}

func TestSyncScheduler_Start_Context(t *testing.T) {
	cfg := DefaultConfig()
	cfg.SyncIntervalRead = 100 * time.Millisecond
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	scheduler := NewSyncScheduler(cfg, logger)

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	done := make(chan bool)
	go func() {
		scheduler.Start(ctx)
		done <- true
	}()

	select {
	case <-done:
		// expected - scheduler stopped when context canceled
	case <-time.After(5 * time.Second):
		t.Fatal("scheduler should stop when context canceled")
	}
}

func TestSyncScheduler_PerOperationFlags_Independent(t *testing.T) {
	cfg := DefaultConfig()
	cfg.LedgerPath = "/tmp/test"
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	scheduler := NewSyncScheduler(cfg, logger)

	// mark pull as in progress
	scheduler.mu.Lock()
	scheduler.pullInProgress = true
	scheduler.mu.Unlock()

	// clone semaphore should still have capacity (pull doesn't consume clone slots)
	select {
	case scheduler.cloneSem <- struct{}{}:
		<-scheduler.cloneSem // release immediately
	default:
		t.Fatal("clone semaphore should not be blocked by pull flag")
	}
}

func TestSyncScheduler_Sync_Method(t *testing.T) {
	cfg := DefaultConfig()
	cfg.LedgerPath = "" // no ledger, will return early
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	scheduler := NewSyncScheduler(cfg, logger)

	// should complete without error
	err := scheduler.Sync()
	assert.NoError(t, err)
}

func TestSyncScheduler_SyncWithProgress_PropagatesFetchError(t *testing.T) {
	// Set up a real git repo with a bogus remote so git fetch fails.
	// This is the exact scenario that was silently swallowed before:
	// doPull encounters "exit status 128" from git fetch, but
	// SyncWithProgress must return that error (not nil).
	tmpDir := t.TempDir()
	ledgerDir := filepath.Join(tmpDir, "ledger")

	// init a git repo with a remote that will fail to fetch
	cmd := exec.Command("git", "init", ledgerDir)
	require.NoError(t, cmd.Run())
	cmd = exec.Command("git", "-C", ledgerDir, "remote", "add", "origin", "https://127.0.0.1:1/nonexistent.git")
	require.NoError(t, cmd.Run())

	cfg := DefaultConfig()
	cfg.LedgerPath = ledgerDir
	cfg.SyncIntervalRead = 1 * time.Second // low threshold so FETCH_HEAD check doesn't skip
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	scheduler := NewSyncScheduler(cfg, logger)

	err := scheduler.SyncWithProgress(nil)
	assert.Error(t, err, "SyncWithProgress must propagate git fetch failures")
	assert.Contains(t, err.Error(), "ledger fetch failed")
}

func TestSyncScheduler_SyncWithProgress_PropagatesPullError(t *testing.T) {
	// Set up a repo where fetch succeeds but pull --rebase fails.
	// Create a local bare repo as remote, clone it, then force-push
	// a divergent history so pull --rebase fails with a conflict.
	tmpDir := t.TempDir()
	bareDir := filepath.Join(tmpDir, "bare.git")
	ledgerDir := filepath.Join(tmpDir, "ledger")

	// create bare repo with initial commit
	require.NoError(t, exec.Command("git", "init", "--bare", bareDir).Run())

	// clone, add initial commit
	require.NoError(t, exec.Command("git", "clone", bareDir, ledgerDir).Run())
	require.NoError(t, os.WriteFile(filepath.Join(ledgerDir, "file.txt"), []byte("original"), 0644))
	require.NoError(t, exec.Command("git", "-C", ledgerDir, "add", "file.txt").Run())
	require.NoError(t, exec.Command("git", "-C", ledgerDir, "commit", "-m", "initial").Run())
	require.NoError(t, exec.Command("git", "-C", ledgerDir, "push", "origin", "HEAD").Run())

	// create a local commit that will conflict
	require.NoError(t, os.WriteFile(filepath.Join(ledgerDir, "file.txt"), []byte("local change"), 0644))
	require.NoError(t, exec.Command("git", "-C", ledgerDir, "add", "file.txt").Run())
	require.NoError(t, exec.Command("git", "-C", ledgerDir, "commit", "-m", "local").Run())

	// force-push a conflicting commit to the bare repo from a temp clone
	tempClone := filepath.Join(tmpDir, "temp")
	require.NoError(t, exec.Command("git", "clone", bareDir, tempClone).Run())
	require.NoError(t, os.WriteFile(filepath.Join(tempClone, "file.txt"), []byte("remote change"), 0644))
	require.NoError(t, exec.Command("git", "-C", tempClone, "add", "file.txt").Run())
	require.NoError(t, exec.Command("git", "-C", tempClone, "commit", "--amend", "-m", "amended").Run())
	require.NoError(t, exec.Command("git", "-C", tempClone, "push", "--force", "origin", "HEAD").Run())

	cfg := DefaultConfig()
	cfg.LedgerPath = ledgerDir
	cfg.SyncIntervalRead = 1 * time.Second
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	scheduler := NewSyncScheduler(cfg, logger)

	err := scheduler.SyncWithProgress(nil)
	assert.Error(t, err, "SyncWithProgress must propagate git pull failures")
	// could be "pull failed" or "diverged" depending on detectDivergedBranches
	assert.Contains(t, err.Error(), "ledger")
}

func TestSyncScheduler_ActivityCallback(t *testing.T) {
	cfg := DefaultConfig()
	cfg.LedgerPath = ""
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	scheduler := NewSyncScheduler(cfg, logger)

	callCount := 0
	scheduler.SetActivityCallback(func() {
		callCount++
	})

	// recordActivity should call the callback
	scheduler.recordActivity()
	assert.Equal(t, 1, callCount)

	scheduler.recordActivity()
	assert.Equal(t, 2, callCount)
}

func TestSyncScheduler_TeamContextStatus(t *testing.T) {
	// isolate from real credentials
	prevConfigDir := gitserver.TestSetConfigDirOverride(t.TempDir())
	prevForceFile := gitserver.TestSetForceFileStorage(true)
	t.Cleanup(func() {
		gitserver.TestSetConfigDirOverride(prevConfigDir)
		gitserver.TestSetForceFileStorage(prevForceFile)
	})

	// create temp project with team contexts configured
	tmpDir := t.TempDir()
	sageoxDir := filepath.Join(tmpDir, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755))

	// create team context as git repo so it's detected as valid
	team1Dir := filepath.Join(t.TempDir(), "team1-context")
	require.NoError(t, os.MkdirAll(filepath.Join(team1Dir, ".git"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(team1Dir, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0644))

	// write config with team contexts
	configContent := fmt.Sprintf(`
[[team_contexts]]
team_id = "team1"
team_name = "Team One"
path = %q

[[team_contexts]]
team_id = "team2"
team_name = "Team Two"
path = "/nonexistent/path"
`, team1Dir)
	require.NoError(t, os.WriteFile(filepath.Join(sageoxDir, "config.local.toml"), []byte(configContent), 0644))

	cfg := DefaultConfig()
	cfg.ProjectRoot = tmpDir
	cfg.LedgerPath = ""
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	scheduler := NewSyncScheduler(cfg, logger)

	// load config into registry
	require.NoError(t, scheduler.workspaceRegistry.LoadFromConfig())

	// retrieve and verify
	status := scheduler.TeamContextStatus()
	assert.Len(t, status, 2)

	// find each team
	var team1Status, team2Status *TeamContextSyncStatus
	for i := range status {
		switch status[i].TeamID {
		case "team1":
			team1Status = &status[i]
		case "team2":
			team2Status = &status[i]
		}
	}

	require.NotNil(t, team1Status, "team1 status should exist")
	require.NotNil(t, team2Status, "team2 status should exist")

	assert.Equal(t, "Team One", team1Status.TeamName)
	assert.True(t, team1Status.Exists) // path exists

	assert.Equal(t, "Team Two", team2Status.TeamName)
	assert.False(t, team2Status.Exists) // path doesn't exist
}

func TestSyncScheduler_PullTeamContexts_NoProjectRoot(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ProjectRoot = "" // no project root
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	scheduler := NewSyncScheduler(cfg, logger)

	// should return early without error
	scheduler.pullTeamContexts(context.Background())

	// no status set
	assert.Empty(t, scheduler.TeamContextStatus())
}

func TestSyncScheduler_PullTeamContexts_NoTeamContextsConfigured(t *testing.T) {
	// isolate from real credentials
	prevConfigDir := gitserver.TestSetConfigDirOverride(t.TempDir())
	prevForceFile := gitserver.TestSetForceFileStorage(true)
	t.Cleanup(func() {
		gitserver.TestSetConfigDirOverride(prevConfigDir)
		gitserver.TestSetForceFileStorage(prevForceFile)
	})

	// create temp project with no team contexts in config
	tmpDir := t.TempDir()
	sageoxDir := filepath.Join(tmpDir, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755))

	// write empty local config
	configContent := "# empty config\n"
	require.NoError(t, os.WriteFile(filepath.Join(sageoxDir, "config.local.toml"), []byte(configContent), 0644))

	// write project config with fake endpoint to prevent real API calls
	require.NoError(t, os.WriteFile(filepath.Join(sageoxDir, "config.json"),
		[]byte(`{"endpoint":"https://fake.test.invalid"}`), 0644))

	cfg := DefaultConfig()
	cfg.ProjectRoot = tmpDir
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	scheduler := NewSyncScheduler(cfg, logger)
	scheduler.pullTeamContexts(context.Background())

	// should have no status entries
	assert.Empty(t, scheduler.TeamContextStatus())
}

func TestSyncScheduler_TeamContextMultiple(t *testing.T) {
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

	// create two team context repos
	teamDir1 := t.TempDir()
	setupGitRepo(t, teamDir1)

	teamDir2 := t.TempDir()
	setupGitRepo(t, teamDir2)

	// write config with multiple team contexts
	configContent := fmt.Sprintf(`
[[team_contexts]]
team_id = "team-alpha"
team_name = "Team Alpha"
path = %q

[[team_contexts]]
team_id = "team-beta"
team_name = "Team Beta"
path = %q
`, teamDir1, teamDir2)
	require.NoError(t, os.WriteFile(filepath.Join(sageoxDir, "config.local.toml"), []byte(configContent), 0644))

	cfg := DefaultConfig()
	cfg.ProjectRoot = projectDir
	cfg.TeamContextSyncInterval = time.Minute
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	scheduler := NewSyncScheduler(cfg, logger)

	// prevent refreshCredentialsIfNeeded and discoverTeams from calling real API
	scheduler.mu.Lock()
	scheduler.lastCredentialRefresh = time.Now()
	scheduler.lastTeamDiscovery = time.Now()
	scheduler.mu.Unlock()

	// make FETCH_HEAD old for both repos
	for _, dir := range []string{teamDir1, teamDir2} {
		fetchHead := filepath.Join(dir, ".git", "FETCH_HEAD")
		oldTime := time.Now().Add(-1 * time.Hour)
		_ = os.Chtimes(fetchHead, oldTime, oldTime)
	}

	// run team context sync
	scheduler.pullTeamContexts(context.Background())

	// verify both teams synced
	status := scheduler.TeamContextStatus()
	require.Len(t, status, 2)

	// find each team
	var alphaStatus, betaStatus *TeamContextSyncStatus
	for i := range status {
		switch status[i].TeamID {
		case "team-alpha":
			alphaStatus = &status[i]
		case "team-beta":
			betaStatus = &status[i]
		}
	}

	require.NotNil(t, alphaStatus, "team-alpha status should exist")
	require.NotNil(t, betaStatus, "team-beta status should exist")

	assert.True(t, alphaStatus.Exists)
	assert.True(t, betaStatus.Exists)
	assert.Empty(t, alphaStatus.LastErr)
	assert.Empty(t, betaStatus.LastErr)
}

func TestSyncScheduler_RecordSync_TeamContext(t *testing.T) {
	cfg := DefaultConfig()
	scheduler := NewSyncScheduler(cfg, nil)

	// record a team context sync
	scheduler.recordSync("team_context", "team_abc", 150*time.Millisecond, 0)

	// verify history
	history := scheduler.SyncHistory()
	require.Len(t, history, 1)
	assert.Equal(t, "team_context", history[0].Type)
	assert.Equal(t, 150*time.Millisecond, history[0].Duration)
}

func TestSyncScheduler_SyncStats_WithTeamContext(t *testing.T) {
	cfg := DefaultConfig()
	scheduler := NewSyncScheduler(cfg, nil)

	// record various syncs
	scheduler.recordSync("pull", "ledger", 100*time.Millisecond, 0)
	scheduler.recordSync("push", "ledger", 200*time.Millisecond, 3)
	scheduler.recordSync("team_context", "team_abc", 50*time.Millisecond, 0)

	// verify stats
	stats := scheduler.SyncStats()
	assert.Equal(t, 3, stats.TotalSyncs)
	assert.Equal(t, 3, stats.SyncsLastHour)
}

func TestSyncScheduler_Start_TeamContextTicker(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping timing-sensitive test")
	}

	// create temp project
	projectDir := t.TempDir()

	cfg := DefaultConfig()
	cfg.ProjectRoot = projectDir
	cfg.SyncIntervalRead = 10 * time.Second   // long so it doesn't interfere
	cfg.TeamContextSyncInterval = time.Second // short for test
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	scheduler := NewSyncScheduler(cfg, logger)

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	// start scheduler - it should create the team context ticker
	done := make(chan bool)
	go func() {
		scheduler.Start(ctx)
		done <- true
	}()

	// wait for context to cancel
	select {
	case <-done:
		// expected - scheduler stopped when context canceled
	case <-time.After(5 * time.Second):
		t.Fatal("scheduler should stop when context canceled")
	}
}
