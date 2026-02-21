package daemon

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// isolateCredentials is defined in sync_teamctx_test.go and shared across test files.

// setupSyncIntegrationGitRepos creates a bare remote, clones it as a ledger,
// and returns (bareDir, ledgerDir). The ledger has an initial commit pushed to remote.
func setupSyncIntegrationGitRepos(t *testing.T) (bareDir, ledgerDir string) {
	t.Helper()
	tmpDir := t.TempDir()
	bareDir = filepath.Join(tmpDir, "remote.git")
	ledgerDir = filepath.Join(tmpDir, "ledger")
	initWorkDir := filepath.Join(tmpDir, "init-work")

	require.NoError(t, exec.Command("git", "init", "--bare", bareDir).Run())
	require.NoError(t, exec.Command("git", "clone", bareDir, initWorkDir).Run())
	gitConfig(t, initWorkDir)
	require.NoError(t, os.WriteFile(filepath.Join(initWorkDir, "README.md"), []byte("initial"), 0644))
	require.NoError(t, exec.Command("git", "-C", initWorkDir, "add", "README.md").Run())
	require.NoError(t, exec.Command("git", "-C", initWorkDir, "commit", "-m", "initial").Run())
	require.NoError(t, exec.Command("git", "-C", initWorkDir, "push", "origin", "HEAD:main").Run())

	require.NoError(t, exec.Command("git", "clone", bareDir, ledgerDir).Run())
	gitConfig(t, ledgerDir)
	return bareDir, ledgerDir
}

// pushCommitToRemote creates a new commit via a temp clone and pushes to the bare remote.
func pushCommitToRemote(t *testing.T, bareDir, filename, content string) {
	t.Helper()
	pusherDir := filepath.Join(t.TempDir(), "pusher")
	require.NoError(t, exec.Command("git", "clone", bareDir, pusherDir).Run())
	gitConfig(t, pusherDir)
	require.NoError(t, os.WriteFile(filepath.Join(pusherDir, filename), []byte(content), 0644))
	require.NoError(t, exec.Command("git", "-C", pusherDir, "add", filename).Run())
	require.NoError(t, exec.Command("git", "-C", pusherDir, "commit", "-m", "add "+filename).Run())
	require.NoError(t, exec.Command("git", "-C", pusherDir, "push", "origin", "HEAD:main").Run())
}

// startSyncDaemon wires up a SyncScheduler + IPC server + returns a connected client.
// Caller must defer cancel().
func startSyncDaemon(t *testing.T, ledgerDir string) (client *Client, cancel context.CancelFunc) {
	t.Helper()

	ipcTmpDir, err := os.MkdirTemp("/tmp", "ox-sync-int-")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(ipcTmpDir) })
	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("XDG_RUNTIME_DIR", ipcTmpDir)

	// use a fake project root so endpoint.GetForProject returns empty (no credential injection)
	fakeProjectRoot := t.TempDir()

	cfg := DefaultConfig()
	cfg.LedgerPath = ledgerDir
	cfg.ProjectRoot = fakeProjectRoot
	cfg.SyncIntervalRead = 1 * time.Second
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	scheduler := NewSyncScheduler(cfg, logger)

	// prevent refreshCredentialsIfNeeded from running — it would read real auth
	// tokens and inject oauth2 credentials into local-path git remotes
	scheduler.mu.Lock()
	scheduler.lastCredentialRefresh = time.Now()
	scheduler.mu.Unlock()

	server := NewServer(logger)
	server.SetHandlers(
		func() error { return nil },
		func() {},
		func() *StatusData { return &StatusData{Running: true} },
	)
	server.SetSyncHandler(func(progress *ProgressWriter) error {
		return scheduler.SyncWithProgress(progress)
	})

	ctx, cancelFn := context.WithCancel(context.Background())

	go server.Start(ctx)
	time.Sleep(100 * time.Millisecond) // wait for socket

	return &Client{
		socketPath: SocketPath(),
		timeout:    10 * time.Second,
	}, cancelFn
}

// TestSyncIntegration_FullFlow verifies the complete CLI->daemon->git sync pipeline:
// IPC client sends sync request -> IPC server routes to SyncScheduler ->
// SyncScheduler does git fetch+pull -> git state updates -> progress streams back.
func TestSyncIntegration_FullFlow(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	isolateCredentials(t)

	bareDir, ledgerDir := setupSyncIntegrationGitRepos(t)
	pushCommitToRemote(t, bareDir, "new-file.txt", "from remote")

	// verify ledger does NOT have the new file yet
	_, err := os.Stat(filepath.Join(ledgerDir, "new-file.txt"))
	require.True(t, os.IsNotExist(err), "ledger should not have new-file.txt before sync")

	client, cancel := startSyncDaemon(t, ledgerDir)
	defer cancel()

	var mu sync.Mutex
	var progressStages []string
	err = client.SyncWithProgress(func(stage string, percent *int, message string) {
		mu.Lock()
		progressStages = append(progressStages, stage)
		mu.Unlock()
	})
	require.NoError(t, err)

	// verify git state: new commit should now be in ledger
	content, err := os.ReadFile(filepath.Join(ledgerDir, "new-file.txt"))
	require.NoError(t, err, "new-file.txt should exist after sync")
	assert.Equal(t, "from remote", string(content))

	// verify progress callbacks fired with expected stages
	mu.Lock()
	stages := progressStages
	mu.Unlock()
	assert.Contains(t, stages, "fetching", "should have received 'fetching' progress stage")
	assert.Contains(t, stages, "pulling", "should have received 'pulling' progress stage")
}

// TestSyncIntegration_AlreadyUpToDate verifies sync completes quickly when
// local ledger is already up to date with remote.
func TestSyncIntegration_AlreadyUpToDate(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	isolateCredentials(t)

	_, ledgerDir := setupSyncIntegrationGitRepos(t)

	client, cancel := startSyncDaemon(t, ledgerDir)
	defer cancel()

	var progressStages []string
	start := time.Now()
	err := client.SyncWithProgress(func(stage string, percent *int, message string) {
		progressStages = append(progressStages, stage)
	})
	elapsed := time.Since(start)

	require.NoError(t, err)
	assert.Less(t, elapsed, 5*time.Second, "up-to-date sync should be fast")

	// verify ledger HEAD is unchanged
	headCmd := exec.Command("git", "-C", ledgerDir, "rev-parse", "HEAD")
	headOut, err := headCmd.Output()
	require.NoError(t, err)
	assert.NotEmpty(t, string(headOut), "HEAD should exist")
}

// TestSyncIntegration_ProgressStageOrdering verifies that progress stages flow
// in the correct order during a sync that pulls new changes:
// "fetching" must come before "pulling".
func TestSyncIntegration_ProgressStageOrdering(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	isolateCredentials(t)

	bareDir, ledgerDir := setupSyncIntegrationGitRepos(t)
	pushCommitToRemote(t, bareDir, "change.txt", "changed")

	client, cancel := startSyncDaemon(t, ledgerDir)
	defer cancel()

	var mu sync.Mutex
	var stages []string
	err := client.SyncWithProgress(func(stage string, percent *int, message string) {
		mu.Lock()
		stages = append(stages, stage)
		mu.Unlock()
	})
	require.NoError(t, err)

	mu.Lock()
	capturedStages := stages
	mu.Unlock()

	fetchIdx := -1
	pullIdx := -1
	for i, s := range capturedStages {
		if s == "fetching" && fetchIdx == -1 {
			fetchIdx = i
		}
		if s == "pulling" && pullIdx == -1 {
			pullIdx = i
		}
	}

	require.NotEqual(t, -1, fetchIdx, "must receive 'fetching' stage")
	require.NotEqual(t, -1, pullIdx, "must receive 'pulling' stage")
	assert.Less(t, fetchIdx, pullIdx, "'fetching' must come before 'pulling'")

	// verify the change actually landed
	_, err = os.Stat(filepath.Join(ledgerDir, "change.txt"))
	require.NoError(t, err, "change.txt should exist after sync")
}

// gitConfig sets test-safe git user config in a specific directory (never in the real repo).
func gitConfig(t *testing.T, dir string) {
	t.Helper()
	cmd := exec.Command("git", "-C", dir, "config", "user.email", "test@test.com")
	require.NoError(t, cmd.Run())
	cmd = exec.Command("git", "-C", dir, "config", "user.name", "Test")
	require.NoError(t, cmd.Run())
}
