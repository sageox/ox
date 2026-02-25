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
	case <-time.After(100 * time.Millisecond):
		t.Fatal("trigger should not block")
	}
}

func TestSyncScheduler_LastSync(t *testing.T) {
	cfg := DefaultConfig()
	scheduler := NewSyncScheduler(cfg, nil)

	// initially zero
	assert.True(t, scheduler.LastSync().IsZero())

	// update directly
	scheduler.mu.Lock()
	now := time.Now()
	scheduler.lastSync = now
	scheduler.mu.Unlock()

	assert.Equal(t, now, scheduler.LastSync())
}
func TestSyncScheduler_Start_Context(t *testing.T) {
	cfg := DefaultConfig()
	cfg.SyncIntervalRead = 100 * time.Millisecond
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	scheduler := NewSyncScheduler(cfg, logger)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	done := make(chan bool)
	go func() {
		scheduler.Start(ctx)
		done <- true
	}()

	select {
	case <-done:
		// expected - scheduler stopped when context canceled
	case <-time.After(200 * time.Millisecond):
		t.Fatal("scheduler should stop when context canceled")
	}
}

func TestSyncScheduler_NoLedgerPath(t *testing.T) {
	cfg := DefaultConfig()
	cfg.LedgerPath = "" // no ledger configured
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	scheduler := NewSyncScheduler(cfg, logger)

	// should not panic, just return early
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	scheduler.pullChanges(ctx)
	scheduler.syncAll(ctx)
}

func TestSyncScheduler_DoPull_LedgerDirExistsButNotGitRepo(t *testing.T) {
	// simulate a failed clone that left an empty directory behind
	tmpDir := t.TempDir()
	ledgerDir := filepath.Join(tmpDir, "ledger")
	require.NoError(t, os.MkdirAll(ledgerDir, 0755))

	// verify the directory exists but is NOT a git repo
	_, err := os.Stat(ledgerDir)
	require.NoError(t, err)
	assert.False(t, pathIsGitRepo(ledgerDir))

	cfg := DefaultConfig()
	cfg.LedgerPath = ledgerDir
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	scheduler := NewSyncScheduler(cfg, logger)

	// doPull should detect the empty dir is not a git repo and return early
	// (enters the clone branch) rather than falling through to git fetch/pull
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	// should not panic — previously this would fall through to git pull
	// on an empty directory since it only checked os.IsNotExist
	scheduler.doPull(ctx, nil, false)
}

func TestSyncScheduler_PullInProgress(t *testing.T) {
	cfg := DefaultConfig()
	cfg.LedgerPath = "/tmp/test"
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	scheduler := NewSyncScheduler(cfg, logger)

	// mark pull as in progress
	scheduler.mu.Lock()
	scheduler.pullInProgress = true
	scheduler.mu.Unlock()

	// should return early
	ctx := context.Background()
	scheduler.pullChanges(ctx)

	// should still be in progress (not reset by early return)
	scheduler.mu.Lock()
	assert.True(t, scheduler.pullInProgress)
	scheduler.mu.Unlock()
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
	// could be "pull failed" or "diverged" depending on detectForcePush
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

func TestSyncScheduler_PullTeamContexts_EmptyPath(t *testing.T) {
	// isolate from real credentials
	prevConfigDir := gitserver.TestSetConfigDirOverride(t.TempDir())
	prevForceFile := gitserver.TestSetForceFileStorage(true)
	t.Cleanup(func() {
		gitserver.TestSetConfigDirOverride(prevConfigDir)
		gitserver.TestSetForceFileStorage(prevForceFile)
	})

	// create temp project with team context with empty path
	tmpDir := t.TempDir()
	sageoxDir := filepath.Join(tmpDir, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755))

	// write config with empty path
	configContent := `
[[team_contexts]]
team_id = "test-team"
team_name = "Test Team"
path = ""
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
	assert.False(t, status[0].Exists)
	assert.Equal(t, "no path configured", status[0].LastErr)
}

func TestSyncScheduler_PullTeamContext_FetchHeadDeduplication(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	// check if git is available
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

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

	cfg := DefaultConfig()
	cfg.TeamContextSyncInterval = 10 * time.Minute
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	scheduler := NewSyncScheduler(cfg, logger)

	// pull should fail since it's not a git repo
	err := scheduler.pullTeamContext(context.Background(), tmpDir)
	assert.Error(t, err)
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

	// prevent refreshCredentialsIfNeeded from calling real API
	scheduler.mu.Lock()
	scheduler.lastCredentialRefresh = time.Now()
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

	// prevent refreshCredentialsIfNeeded from calling real API
	scheduler.mu.Lock()
	scheduler.lastCredentialRefresh = time.Now()
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
	scheduler.recordSync("team_context", 150*time.Millisecond, 0)

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
	scheduler.recordSync("pull", 100*time.Millisecond, 0)
	scheduler.recordSync("push", 200*time.Millisecond, 3)
	scheduler.recordSync("team_context", 50*time.Millisecond, 0)

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

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
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
	case <-time.After(500 * time.Millisecond):
		t.Fatal("scheduler should stop when context canceled")
	}
}

// Test SyncScheduler.Checkout with existing repo
func TestSyncScheduler_Checkout_AlreadyExists(t *testing.T) {
	// create temp git repo
	tmpDir := t.TempDir()
	gitDir := filepath.Join(tmpDir, ".git")
	require.NoError(t, os.MkdirAll(gitDir, 0755))

	cfg := DefaultConfig()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	scheduler := NewSyncScheduler(cfg, logger)

	result, err := scheduler.Checkout(CheckoutPayload{
		RepoPath: tmpDir,
		CloneURL: "https://example.com/repo.git",
		RepoType: "ledger",
	}, nil)

	require.NoError(t, err)
	assert.NotNil(t, result)
	assert.True(t, result.AlreadyExists)
	assert.False(t, result.Cloned)
	assert.Equal(t, tmpDir, result.Path)
}

// Test SyncScheduler.Checkout with non-git directory (self-healing)
func TestSyncScheduler_Checkout_ExistsButNotGit(t *testing.T) {
	// create temp directory without .git - simulates corrupt/incomplete clone
	parentDir := t.TempDir()
	repoDir := filepath.Join(parentDir, "repo")
	require.NoError(t, os.MkdirAll(repoDir, 0755))

	// create a file in the directory to verify backup works
	testFile := filepath.Join(repoDir, "testfile.txt")
	require.NoError(t, os.WriteFile(testFile, []byte("test"), 0644))

	cfg := DefaultConfig()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	scheduler := NewSyncScheduler(cfg, logger)

	// self-healing: should move directory aside and attempt clone
	// clone will fail due to untrusted host, but directory should be backed up
	result, err := scheduler.Checkout(CheckoutPayload{
		RepoPath: repoDir,
		CloneURL: "https://example.com/repo.git",
		RepoType: "ledger",
	}, nil)

	assert.Error(t, err)
	assert.Nil(t, result)
	// error should be from clone attempt (untrusted host), not "not a git repository"
	assert.Contains(t, err.Error(), "untrusted git host")

	// verify original directory was moved to backup (self-healing)
	backups, _ := filepath.Glob(filepath.Join(parentDir, "repo.bak.*"))
	assert.Len(t, backups, 1, "expected backup directory to be created")
	if len(backups) > 0 {
		// verify backup contains our test file
		backupTestFile := filepath.Join(backups[0], "testfile.txt")
		_, err := os.Stat(backupTestFile)
		assert.NoError(t, err, "backup should contain original files")
	}
}

// Test SyncScheduler.Checkout queues when all clone slots are busy (doesn't error)
func TestSyncScheduler_Checkout_ConcurrentClonesQueue(t *testing.T) {
	cfg := DefaultConfig()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	scheduler := NewSyncScheduler(cfg, logger)

	// fill all clone slots to simulate busy state
	for range maxConcurrentClones {
		scheduler.cloneSem <- struct{}{}
	}

	// hook fires just before the goroutine tries to acquire the semaphore,
	// so we know deterministically that it's about to block
	reachedSem := make(chan struct{})
	scheduler.onBeforeCloneSem = func() { close(reachedSem) }

	// use temp directory to pass path validation
	tmpDir := t.TempDir()
	repoPath := filepath.Join(tmpDir, "nonexistent-repo")

	// Checkout should block (not error) when all slots are busy.
	// Launch in goroutine and verify it doesn't return an error after we free a slot.
	done := make(chan struct{})
	var result *CheckoutResult
	var err error
	go func() {
		result, err = scheduler.Checkout(CheckoutPayload{
			RepoPath: repoPath,
			CloneURL: "http://127.0.0.1:1/repo.git", // local URL that fails instantly (no network round trip)
			RepoType: "ledger",
		}, nil)
		close(done)
	}()

	// wait for goroutine to reach the semaphore (deterministic, no sleep)
	select {
	case <-reachedSem:
		// goroutine is now about to block on the full semaphore
	case <-time.After(5 * time.Second):
		t.Fatal("Checkout goroutine did not reach semaphore in time")
	}

	select {
	case <-done:
		t.Fatal("Checkout should be blocking on semaphore, not returning immediately")
	default:
		// expected: still blocked
	}

	// free one slot — Checkout should proceed (and fail on clone, which is expected)
	<-scheduler.cloneSem

	select {
	case <-done:
		// Checkout completed (will error on actual clone, but NOT "another checkout in progress")
		if err != nil {
			assert.NotContains(t, err.Error(), "another checkout operation is in progress",
				"should never get the old contention error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Checkout did not unblock after freeing a clone slot")
	}

	// drain remaining slots
	for range maxConcurrentClones - 1 {
		<-scheduler.cloneSem
	}
	_ = result
}

// Test SyncScheduler.Checkout rejects path traversal attempts
func TestSyncScheduler_Checkout_PathTraversal(t *testing.T) {
	cfg := DefaultConfig()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	scheduler := NewSyncScheduler(cfg, logger)

	testCases := []struct {
		name     string
		repoPath string
	}{
		{"parent directory traversal", "/home/user/../../../etc/passwd"},
		{"embedded traversal", "/home/user/repos/../../../etc/passwd"},
		{"relative path", "relative/path/to/repo"},
		{"empty path", ""},
		{"double dot only", ".."},
		{"traversal at end", "/home/user/.."},
		{"outside home and tmp", "/etc/sageox/repo"},
		{"system directory", "/usr/local/repo"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := scheduler.Checkout(CheckoutPayload{
				RepoPath: tc.repoPath,
				CloneURL: "https://example.com/repo.git",
				RepoType: "ledger",
			}, nil)

			assert.Error(t, err)
			assert.Nil(t, result)
			assert.ErrorIs(t, err, ErrInvalidRepoPath)
		})
	}
}

// Test isValidRepoPath validates correct paths
func TestIsValidRepoPath(t *testing.T) {
	homeDir, err := os.UserHomeDir()
	require.NoError(t, err)

	tmpDir := os.TempDir()

	testCases := []struct {
		name     string
		path     string
		expected bool
	}{
		// valid paths
		{"home directory path", filepath.Join(homeDir, "repos", "myrepo"), true},
		{"temp directory path", filepath.Join(tmpDir, "test-repo"), true},
		{"nested home path", filepath.Join(homeDir, ".sageox", "data", "ledger"), true},
		{"/tmp path", "/tmp/test-repo", true},
		{"/private/tmp path", "/private/tmp/test-repo", true},
		{"nested /tmp path", "/tmp/myproject_sageox/ledger", true},

		// invalid paths
		{"empty path", "", false},
		{"relative path", "relative/path", false},
		{"path with traversal", filepath.Join(homeDir, "..", "etc"), false},
		{"system path", "/etc/passwd", false},
		{"var path", "/var/log/test", false},
		{"usr path", "/usr/local/bin", false},
		{"root path", "/", false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := isValidRepoPath(tc.path)
			assert.Equal(t, tc.expected, result, "path: %s", tc.path)
		})
	}
}

// Test SyncScheduler.Checkout rejects local paths (SSRF protection)
func TestSyncScheduler_Checkout_Clone_LocalPathRejected(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	// check if git is available
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	// create bare repo to clone from
	bareDir := t.TempDir()
	initBareCmd := exec.Command("git", "init", "--bare")
	initBareCmd.Dir = bareDir
	require.NoError(t, initBareCmd.Run())

	targetDir := filepath.Join(t.TempDir(), "cloned-repo")

	cfg := DefaultConfig()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	scheduler := NewSyncScheduler(cfg, logger)

	// SSRF protection: local paths are rejected
	result, err := scheduler.Checkout(CheckoutPayload{
		RepoPath: targetDir,
		CloneURL: bareDir, // local path - should be rejected
		RepoType: "ledger",
	}, nil)

	// local paths are now rejected as SSRF protection
	require.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "invalid clone URL")
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

	teamDir := filepath.Join(t.TempDir(), "team-context")
	require.NoError(t, os.MkdirAll(filepath.Join(teamDir, ".git"), 0755))

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

// setupGitRepo initializes a git repo in the given directory with an initial commit
// and sets up a local "origin" remote that can be fetched from.
func setupGitRepo(t *testing.T, dir string) {
	t.Helper()

	// create a bare repo to act as "origin"
	bareDir := filepath.Join(filepath.Dir(dir), filepath.Base(dir)+".bare")
	require.NoError(t, os.MkdirAll(bareDir, 0755))

	initBareCmd := exec.Command("git", "init", "--bare")
	initBareCmd.Dir = bareDir
	require.NoError(t, initBareCmd.Run())

	// init the working repo
	initCmd := exec.Command("git", "init")
	initCmd.Dir = dir
	require.NoError(t, initCmd.Run())

	// configure git
	configCmd := exec.Command("git", "config", "user.email", "test@test.com")
	configCmd.Dir = dir
	require.NoError(t, configCmd.Run())

	configCmd2 := exec.Command("git", "config", "user.name", "Test")
	configCmd2.Dir = dir
	require.NoError(t, configCmd2.Run())

	// add the bare repo as origin
	remoteCmd := exec.Command("git", "remote", "add", "origin", bareDir)
	remoteCmd.Dir = dir
	require.NoError(t, remoteCmd.Run())

	// create initial commit
	testFile := filepath.Join(dir, "README.md")
	require.NoError(t, os.WriteFile(testFile, []byte("# Test\n"), 0644))

	addCmd := exec.Command("git", "add", ".")
	addCmd.Dir = dir
	require.NoError(t, addCmd.Run())

	commitCmd := exec.Command("git", "commit", "-m", "initial commit")
	commitCmd.Dir = dir
	require.NoError(t, commitCmd.Run())

	// push to origin so we have a remote branch
	pushCmd := exec.Command("git", "push", "-u", "origin", "HEAD:main")
	pushCmd.Dir = dir
	require.NoError(t, pushCmd.Run())

	// set default branch to track origin/main
	branchCmd := exec.Command("git", "branch", "--set-upstream-to=origin/main")
	branchCmd.Dir = dir
	_ = branchCmd.Run() // might fail if already set, that's ok
}

// TestIsValidCloneURL tests SSRF protection for clone URLs
func TestIsValidCloneURL(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantErr bool
		errMsg  string
	}{
		// valid URLs - trusted hosts
		{
			name:    "github.com https",
			url:     "https://github.com/org/repo.git",
			wantErr: false,
		},
		{
			name:    "gitlab.com https",
			url:     "https://gitlab.com/org/repo.git",
			wantErr: false,
		},
		{
			name:    "git.sageox.io https",
			url:     "https://git.sageox.io/team/repo.git",
			wantErr: false,
		},
		{
			name:    "git.sageox.ai https",
			url:     "https://git.sageox.ai/team/repo.git",
			wantErr: false,
		},
		{
			name:    "subdomain of trusted host - github",
			url:     "https://api.github.com/repos/org/repo.git",
			wantErr: false, // api.github.com ends with .github.com, so it's trusted
		},
		{
			name:    "subdomain of trusted host - gitlab",
			url:     "https://enterprise.gitlab.com/org/repo.git",
			wantErr: false, // matches .gitlab.com
		},

		// invalid URLs - wrong schemes (SSRF vectors)
		{
			name:    "file:// scheme - local file access",
			url:     "file:///etc/passwd",
			wantErr: true,
			errMsg:  "URL has no host", // file:// URLs have empty host
		},
		{
			name:    "file:// scheme - windows path",
			url:     "file:///C:/Windows/System32/config/SAM",
			wantErr: true,
			errMsg:  "URL has no host", // file:// URLs have empty host
		},
		{
			name:    "git:// scheme - unauthenticated",
			url:     "git://github.com/org/repo.git",
			wantErr: true,
			errMsg:  "only https:// URLs are supported",
		},
		{
			name:    "ssh:// scheme",
			url:     "ssh://git@github.com/org/repo.git",
			wantErr: true,
			errMsg:  "only https:// URLs are supported",
		},
		{
			name:    "http:// scheme - insecure for remote hosts",
			url:     "http://github.com/org/repo.git",
			wantErr: true,
			errMsg:  "only https:// URLs are supported for remote hosts",
		},

		// valid URLs - local development (http:// allowed for localhost only)
		{
			name:    "http:// localhost - devcontainer support",
			url:     "http://localhost/repo.git",
			wantErr: false,
		},
		{
			name:    "http:// localhost with port - devcontainer support",
			url:     "http://localhost:8929/team/repo.git",
			wantErr: false,
		},
		{
			name:    "http:// 127.0.0.1 - devcontainer support",
			url:     "http://127.0.0.1/repo.git",
			wantErr: false,
		},
		{
			name:    "http:// 127.0.0.1 with port - devcontainer support",
			url:     "http://127.0.0.1:8929/repo.git",
			wantErr: false,
		},

		// http:// should fail for all other hosts (even local networks)
		{
			name:    "http:// .local domain - blocked",
			url:     "http://gitlab.local:8929/team/repo.git",
			wantErr: true,
			errMsg:  "only https:// URLs are supported for remote hosts",
		},
		{
			name:    "http:// 192.168.x.x - blocked",
			url:     "http://192.168.1.100:8929/repo.git",
			wantErr: true,
			errMsg:  "only https:// URLs are supported for remote hosts",
		},
		{
			name:    "http:// external host - blocked",
			url:     "http://evil-server.com/repo.git",
			wantErr: true,
			errMsg:  "only https:// URLs are supported for remote hosts",
		},
		{
			name:    "http:// gitlab.com - blocked (use https)",
			url:     "http://gitlab.com/org/repo.git",
			wantErr: true,
			errMsg:  "only https:// URLs are supported for remote hosts",
		},
		{
			name:    "ftp:// scheme",
			url:     "ftp://evil.com/malware.git",
			wantErr: true,
			errMsg:  "only https:// URLs are supported",
		},

		// invalid URLs - https on untrusted hosts (even local ones need http for dev)
		// Note: https:// to local hosts still fails because they're not in trustedGitHosts
		// Use http:// for local development instead
		{
			name:    "https localhost - not in trusted hosts",
			url:     "https://localhost/repo.git",
			wantErr: true,
			errMsg:  "untrusted git host",
		},
		{
			name:    "https 127.0.0.1 - not in trusted hosts",
			url:     "https://127.0.0.1/repo.git",
			wantErr: true,
			errMsg:  "untrusted git host",
		},
		{
			name:    "https 192.168.x.x - not in trusted hosts",
			url:     "https://192.168.1.1/repo.git",
			wantErr: true,
			errMsg:  "untrusted git host",
		},
		{
			name:    "https 10.x.x.x - not in trusted hosts",
			url:     "https://10.0.0.1/repo.git",
			wantErr: true,
			errMsg:  "untrusted git host",
		},
		{
			name:    "https 172.16.x.x - not in trusted hosts",
			url:     "https://172.16.0.1/repo.git",
			wantErr: true,
			errMsg:  "untrusted git host",
		},
		{
			name:    "internal hostname",
			url:     "https://internal-git.corp/repo.git",
			wantErr: true,
			errMsg:  "untrusted git host",
		},
		{
			name:    "arbitrary external host",
			url:     "https://evil-server.com/malware.git",
			wantErr: true,
			errMsg:  "untrusted git host",
		},
		{
			name:    "typosquatting - githubcom",
			url:     "https://githubcom.evil.com/repo.git",
			wantErr: true,
			errMsg:  "untrusted git host",
		},
		{
			name:    "typosquatting - github-com",
			url:     "https://github-com.evil.com/repo.git",
			wantErr: true,
			errMsg:  "untrusted git host",
		},

		// edge cases
		{
			name:    "empty URL",
			url:     "",
			wantErr: true,
			errMsg:  "clone URL is empty",
		},
		{
			name:    "malformed URL",
			url:     "not-a-valid-url",
			wantErr: true,
			errMsg:  "URL has no host", // parsed as path with no scheme/host
		},
		{
			name:    "URL with credentials (should still validate host)",
			url:     "https://user:pass@github.com/org/repo.git",
			wantErr: false,
		},
		{
			name:    "URL with port on trusted host",
			url:     "https://github.com:443/org/repo.git",
			wantErr: false,
		},
		{
			name:    "URL with port on untrusted host",
			url:     "https://evil.com:8080/repo.git",
			wantErr: true,
			errMsg:  "untrusted git host",
		},
		{
			name:    "AWS metadata endpoint",
			url:     "https://169.254.169.254/latest/meta-data/",
			wantErr: true,
			errMsg:  "untrusted git host",
		},
		{
			name:    "IPv6 localhost",
			url:     "https://[::1]/repo.git",
			wantErr: true,
			errMsg:  "untrusted git host",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := isValidCloneURL(tt.url)
			if tt.wantErr {
				require.Error(t, err, "expected error for URL: %s", tt.url)
				if tt.errMsg != "" {
					assert.Contains(t, err.Error(), tt.errMsg)
				}
			} else {
				require.NoError(t, err, "unexpected error for URL: %s", tt.url)
			}
		})
	}
}

// TestSyncScheduler_Checkout_SSRF_Prevention tests that Checkout rejects unsafe URLs
func TestSyncScheduler_Checkout_SSRF_Prevention(t *testing.T) {
	cfg := DefaultConfig()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	scheduler := NewSyncScheduler(cfg, logger)

	unsafeURLs := []struct {
		name string
		url  string
	}{
		{"file URL", "file:///etc/passwd"},
		{"localhost", "https://localhost/repo.git"},
		{"internal IP", "https://192.168.1.1/repo.git"},
		{"arbitrary host", "https://evil.com/repo.git"},
		{"git protocol", "git://github.com/repo.git"},
	}

	// use temp dir for the repo path to avoid path validation issues
	tmpDir := t.TempDir()

	for _, tc := range unsafeURLs {
		t.Run(tc.name, func(t *testing.T) {
			result, err := scheduler.Checkout(CheckoutPayload{
				RepoPath: filepath.Join(tmpDir, "repo"),
				CloneURL: tc.url,
				RepoType: "ledger",
			}, nil)

			require.Error(t, err)
			assert.Nil(t, result)
			assert.Contains(t, err.Error(), "invalid clone URL")
		})
	}
}

// TestSanitizeGitOutput tests credential sanitization in git command output
func TestSanitizeGitOutput(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "oauth2 token in clone URL",
			input:    "fatal: could not read from remote 'https://oauth2:ghp_abc123xyz789@github.com/org/repo.git'",
			expected: "fatal: could not read from remote 'https://oauth2:***@github.com/org/repo.git'",
		},
		{
			name:     "oauth2 token in error message",
			input:    "Cloning into 'repo'...\nfatal: Authentication failed for 'https://oauth2:secret_token_here@git.sageox.io/team/repo.git'",
			expected: "Cloning into 'repo'...\nfatal: Authentication failed for 'https://oauth2:***@git.sageox.io/team/repo.git'",
		},
		{
			name:     "multiple tokens in output",
			input:    "remote: oauth2:token1@host.com\nremote: oauth2:token2@host.com",
			expected: "remote: oauth2:***@host.com\nremote: oauth2:***@host.com",
		},
		{
			name:     "no credentials - unchanged",
			input:    "fatal: repository 'https://github.com/org/repo.git' not found",
			expected: "fatal: repository 'https://github.com/org/repo.git' not found",
		},
		{
			name:     "empty string",
			input:    "",
			expected: "",
		},
		{
			name:     "long token",
			input:    "https://oauth2:glpat-xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx@gitlab.com/repo.git",
			expected: "https://oauth2:***@gitlab.com/repo.git",
		},
		{
			name:     "token with special characters (no @)",
			input:    "https://oauth2:abc123!#$%^&*()_+-=xyz@host.com/repo.git",
			expected: "https://oauth2:***@host.com/repo.git",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := gitutil.SanitizeOutput(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestInjectGitCredentials tests credential injection into git URLs
func TestInjectGitCredentials(t *testing.T) {
	tests := []struct {
		name     string
		url      string
		username string
		password string
		expected string
	}{
		{
			name:     "https URL with credentials",
			url:      "https://github.com/org/repo.git",
			username: "oauth2",
			password: "token123",
			expected: "https://oauth2:token123@github.com/org/repo.git",
		},
		{
			name:     "https URL with port",
			url:      "https://git.example.com:8443/repo.git",
			username: "oauth2",
			password: "token123",
			expected: "https://oauth2:token123@git.example.com:8443/repo.git",
		},
		{
			name:     "http localhost URL with credentials",
			url:      "http://localhost:8929/org/repo.git",
			username: "oauth2",
			password: "token123",
			expected: "http://oauth2:token123@localhost:8929/org/repo.git",
		},
		{
			name:     "http localhost without port",
			url:      "http://localhost/org/repo.git",
			username: "oauth2",
			password: "token123",
			expected: "http://oauth2:token123@localhost/org/repo.git",
		},
		{
			name:     "http 127.0.0.1 URL with credentials",
			url:      "http://127.0.0.1:8929/org/repo.git",
			username: "oauth2",
			password: "token123",
			expected: "http://oauth2:token123@127.0.0.1:8929/org/repo.git",
		},
		{
			name:     "http external host - unchanged (security)",
			url:      "http://external-host.com/repo.git",
			username: "oauth2",
			password: "token123",
			expected: "http://external-host.com/repo.git",
		},
		{
			name:     "http .local domain - unchanged (security)",
			url:      "http://gitlab.local:8929/repo.git",
			username: "oauth2",
			password: "token123",
			expected: "http://gitlab.local:8929/repo.git",
		},
		{
			name:     "empty username - unchanged",
			url:      "https://github.com/org/repo.git",
			username: "",
			password: "token123",
			expected: "https://github.com/org/repo.git",
		},
		{
			name:     "empty password - unchanged",
			url:      "https://github.com/org/repo.git",
			username: "oauth2",
			password: "",
			expected: "https://github.com/org/repo.git",
		},
		{
			name:     "ssh URL - unchanged",
			url:      "git@github.com:org/repo.git",
			username: "oauth2",
			password: "token123",
			expected: "git@github.com:org/repo.git",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := injectGitCredentials(tt.url, tt.username, tt.password)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestIsClonePermanentError(t *testing.T) {
	permanent := []struct {
		name string
		msg  string
	}{
		{"auth failed", "fatal: Authentication failed for 'https://git.example.com/repo.git'"},
		{"permission denied", "Permission denied (publickey)"},
		{"could not read username", "fatal: could not read Username for 'https://git.example.com': terminal prompts disabled"},
		{"invalid credentials", "remote: invalid credentials"},
		{"repo not found", "fatal: repository not found"},
		{"not a git repo", "fatal: 'https://example.com/bad' does not appear to be a git repository"},
		{"http 401", "git clone failed: HTTP 401"},
		{"http 403", "git clone failed: HTTP 403"},
		{"http 404", "git clone failed: HTTP 404"},
		{"invalid clone url", "invalid clone URL: scheme must be https"},
	}

	for _, tt := range permanent {
		t.Run(tt.name, func(t *testing.T) {
			assert.True(t, isClonePermanentError(tt.msg), "expected permanent for: %s", tt.msg)
		})
	}

	transient := []struct {
		name string
		msg  string
	}{
		{"network timeout", "fatal: unable to access 'https://git.example.com/repo.git': Connection timed out"},
		{"dns failure", "fatal: unable to access: Could not resolve host: git.example.com"},
		{"connection reset", "fatal: the remote end hung up unexpectedly"},
		{"server 500", "error: RPC failed; HTTP 500 curl 22"},
		{"generic error", "git clone failed: exit status 128"},
	}

	for _, tt := range transient {
		t.Run(tt.name, func(t *testing.T) {
			assert.False(t, isClonePermanentError(tt.msg), "expected transient for: %s", tt.msg)
		})
	}
}

func TestClonePermanentBackoffMax(t *testing.T) {
	// permanent errors should have a much shorter max backoff than transient
	assert.Less(t, clonePermanentBackoffMax, cloneBackoffMax)
	assert.Equal(t, 5*time.Minute, clonePermanentBackoffMax)
}

func TestRemoteRefCheck_NoChanges(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	tmpDir := t.TempDir()
	repoDir := filepath.Join(tmpDir, "repo")
	require.NoError(t, os.MkdirAll(repoDir, 0755))
	setupGitRepo(t, repoDir)

	cfg := DefaultConfig()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	scheduler := NewSyncScheduler(cfg, logger)

	// local and remote are in sync — should return true (skip)
	ctx := context.Background()
	assert.True(t, scheduler.remoteRefCheck(ctx, repoDir), "should skip when remote matches local")
}

func TestRemoteRefCheck_WithChanges(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	tmpDir := t.TempDir()
	repoDir := filepath.Join(tmpDir, "repo")
	require.NoError(t, os.MkdirAll(repoDir, 0755))
	setupGitRepo(t, repoDir)

	// push a new commit to the bare remote from a separate clone
	bareDir := repoDir + ".bare"
	tempClone := filepath.Join(tmpDir, "temp-clone")
	require.NoError(t, exec.Command("git", "clone", bareDir, tempClone).Run())

	configCmd := exec.Command("git", "-C", tempClone, "config", "user.email", "test@test.com")
	require.NoError(t, configCmd.Run())
	configCmd2 := exec.Command("git", "-C", tempClone, "config", "user.name", "Test")
	require.NoError(t, configCmd2.Run())

	require.NoError(t, os.WriteFile(filepath.Join(tempClone, "new-file.txt"), []byte("new content"), 0644))
	require.NoError(t, exec.Command("git", "-C", tempClone, "add", ".").Run())
	require.NoError(t, exec.Command("git", "-C", tempClone, "commit", "-m", "new commit").Run())
	require.NoError(t, exec.Command("git", "-C", tempClone, "push", "origin", "HEAD:main").Run())

	cfg := DefaultConfig()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	scheduler := NewSyncScheduler(cfg, logger)

	// remote has a new commit — should return false (proceed with fetch)
	ctx := context.Background()
	assert.False(t, scheduler.remoteRefCheck(ctx, repoDir), "should not skip when remote has new commits")
}

func TestRemoteRefCheck_ErrorFallsThrough(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	tmpDir := t.TempDir()
	repoDir := filepath.Join(tmpDir, "repo")
	require.NoError(t, os.MkdirAll(repoDir, 0755))

	// init repo with a bogus remote that will fail ls-remote
	require.NoError(t, exec.Command("git", "init", repoDir).Run())
	require.NoError(t, exec.Command("git", "-C", repoDir, "remote", "add", "origin", "https://127.0.0.1:1/nonexistent.git").Run())

	cfg := DefaultConfig()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	scheduler := NewSyncScheduler(cfg, logger)

	// ls-remote should fail — function should return false (fall through to fetch)
	ctx := context.Background()
	assert.False(t, scheduler.remoteRefCheck(ctx, repoDir), "should fall through on ls-remote error")
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
	err := scheduler.doPull(ctx, nil, false)
	assert.NoError(t, err)

	// second pull should be skipped by ls-remote check (remote unchanged)
	err = scheduler.doPull(ctx, nil, false)
	assert.NoError(t, err) // no error — just skipped
}

func TestSyncBackoff_LedgerFetchFailure(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	tmpDir := t.TempDir()
	ledgerDir := filepath.Join(tmpDir, "ledger")

	// init repo with bogus remote so fetch fails
	require.NoError(t, exec.Command("git", "init", ledgerDir).Run())
	require.NoError(t, exec.Command("git", "-C", ledgerDir, "config", "user.email", "test@test.com").Run())
	require.NoError(t, exec.Command("git", "-C", ledgerDir, "config", "user.name", "Test").Run())
	require.NoError(t, os.WriteFile(filepath.Join(ledgerDir, "README.md"), []byte("test"), 0644))
	require.NoError(t, exec.Command("git", "-C", ledgerDir, "add", ".").Run())
	require.NoError(t, exec.Command("git", "-C", ledgerDir, "commit", "-m", "initial").Run())
	require.NoError(t, exec.Command("git", "-C", ledgerDir, "remote", "add", "origin", "https://127.0.0.1:1/nonexistent.git").Run())

	cfg := DefaultConfig()
	cfg.LedgerPath = ledgerDir
	cfg.SyncIntervalRead = 1 * time.Second
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	scheduler := NewSyncScheduler(cfg, logger)

	ctx := context.Background()

	// first attempt: should fail (fetch fails on bogus remote)
	err := scheduler.doPull(ctx, nil, false)
	assert.Error(t, err, "first pull should fail")

	// verify backoff was recorded
	failures, nextRetry := scheduler.workspaceRegistry.GetSyncRetryInfo("ledger")
	assert.Equal(t, 1, failures)
	assert.False(t, nextRetry.IsZero(), "next retry should be set")
	assert.True(t, nextRetry.After(time.Now()), "next retry should be in the future")

	// second attempt: should be skipped due to backoff (no error, just skipped)
	err = scheduler.doPull(ctx, nil, false)
	assert.NoError(t, err, "second pull should be skipped by backoff (returns nil)")
}

func TestSyncBackoff_ClearsOnSuccess(t *testing.T) {
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

	// artificially set failure state
	scheduler.workspaceRegistry.RecordSyncFailure("ledger")
	failures, _ := scheduler.workspaceRegistry.GetSyncRetryInfo("ledger")
	assert.Equal(t, 1, failures)

	// use forceSync=true to bypass backoff (simulates on-demand sync clearing backoff)
	ctx := context.Background()
	err := scheduler.doPull(ctx, nil, true)
	assert.NoError(t, err)

	// verify doPull's success path cleared the failure state
	failures, _ = scheduler.workspaceRegistry.GetSyncRetryInfo("ledger")
	assert.Equal(t, 0, failures)
}

func TestWorkspaceRegistry_SyncBackoff(t *testing.T) {
	// unit test the backoff math
	cfg := DefaultConfig()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	scheduler := NewSyncScheduler(cfg, logger)

	// initially should allow sync
	assert.True(t, scheduler.workspaceRegistry.ShouldSync("ledger"))

	// record failures and verify exponential backoff
	scheduler.workspaceRegistry.RecordSyncFailure("ledger")
	assert.False(t, scheduler.workspaceRegistry.ShouldSync("ledger"), "should be in backoff after 1 failure")

	failures, nextRetry := scheduler.workspaceRegistry.GetSyncRetryInfo("ledger")
	assert.Equal(t, 1, failures)
	assert.True(t, nextRetry.After(time.Now()))

	// clear should reset
	scheduler.workspaceRegistry.ClearSyncFailures("ledger")
	assert.True(t, scheduler.workspaceRegistry.ShouldSync("ledger"), "should allow sync after clear")

	failures, _ = scheduler.workspaceRegistry.GetSyncRetryInfo("ledger")
	assert.Equal(t, 0, failures)
}

func TestSyncBackoffMax(t *testing.T) {
	assert.Equal(t, 30*time.Minute, syncBackoffMax)
}

func TestExponentialBackoff(t *testing.T) {
	base := time.Minute
	maxBack := 30 * time.Minute

	tests := []struct {
		failures int
		expected time.Duration
	}{
		{0, 1 * time.Minute},
		{1, 1 * time.Minute},
		{2, 2 * time.Minute},
		{3, 4 * time.Minute},
		{4, 8 * time.Minute},
		{5, 16 * time.Minute},
		{6, 30 * time.Minute}, // 32min capped to 30
		{7, 30 * time.Minute}, // still capped
		{100, 30 * time.Minute}, // extreme value, still capped
	}
	for _, tt := range tests {
		got := exponentialBackoff(tt.failures, base, maxBack)
		assert.Equal(t, tt.expected, got, "failures=%d", tt.failures)
	}
}

func TestSyncBackoff_SeparateAPIAndGitKeys(t *testing.T) {
	// verify that API failures don't block git sync and vice versa
	cfg := DefaultConfig()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	scheduler := NewSyncScheduler(cfg, logger)

	// record API failure
	scheduler.workspaceRegistry.RecordSyncFailure("ledger-api")
	assert.False(t, scheduler.workspaceRegistry.ShouldSync("ledger-api"), "API should be in backoff")
	assert.True(t, scheduler.workspaceRegistry.ShouldSync("ledger"), "git sync should NOT be in backoff")

	// record git sync failure
	scheduler.workspaceRegistry.RecordSyncFailure("ledger")
	assert.False(t, scheduler.workspaceRegistry.ShouldSync("ledger"), "git sync should be in backoff")
	assert.False(t, scheduler.workspaceRegistry.ShouldSync("ledger-api"), "API should still be in backoff")

	// clear git sync — API should still be backed off
	scheduler.workspaceRegistry.ClearSyncFailures("ledger")
	assert.True(t, scheduler.workspaceRegistry.ShouldSync("ledger"), "git sync should be clear")
	assert.False(t, scheduler.workspaceRegistry.ShouldSync("ledger-api"), "API should still be in backoff")
}

func TestSyncBackoff_ForceBypassClearsState(t *testing.T) {
	cfg := DefaultConfig()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	scheduler := NewSyncScheduler(cfg, logger)

	// put ledger into backoff
	scheduler.workspaceRegistry.RecordSyncFailure("ledger")
	scheduler.workspaceRegistry.RecordSyncFailure("ledger")
	assert.False(t, scheduler.workspaceRegistry.ShouldSync("ledger"))

	// forceSync=true should clear and proceed
	assert.True(t, scheduler.shouldSyncOrBypass("ledger", true))
	// state should be cleared
	failures, _ := scheduler.workspaceRegistry.GetSyncRetryInfo("ledger")
	assert.Equal(t, 0, failures)
}
