//go:build slow

package daemon

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSync_PullAfterConcurrentPush verifies that pullManagedRepo fetches and
// merges a commit pushed from a separate clone to the Gitea digital twin.
func TestSync_PullAfterConcurrentPush(t *testing.T) {
	if testing.Short() {
		t.Skip("short: Gitea digital twin")
	}

	g := getSharedGitea(t)
	cloneURL := g.createRepo(t, "sync-pull")

	cloneDir := filepath.Join(t.TempDir(), "local")
	g.cloneRepo(t, cloneURL, cloneDir)

	// push a new file from a separate temp clone (simulates concurrent remote push)
	g.pushFromTempClone(t, cloneURL, "remote-file.txt", "from remote")

	// verify local clone does not have the file yet
	_, err := os.Stat(filepath.Join(cloneDir, "remote-file.txt"))
	require.True(t, os.IsNotExist(err), "remote-file.txt should not exist before pull")

	// set up scheduler and pull
	isolateCredentials(t)
	projectDir := setupProjectWithConfig(t, "")
	s := newTestScheduler(projectDir)

	opts := ManagedRepoPullOpts{
		RepoPath:     cloneDir,
		RepoName:     "test",
		ProjectRoot:  projectDir,
		SyncInterval: time.Second,
		MinFetchAge:  0,
		Logger:       slog.Default(),
	}

	ctx := context.Background()
	result := s.pullManagedRepo(ctx, opts)
	assert.NoError(t, result.Err, "pull should succeed")
	assert.False(t, result.Skipped, "pull should not be skipped")

	// verify remote file now exists locally
	content, err := os.ReadFile(filepath.Join(cloneDir, "remote-file.txt"))
	require.NoError(t, err, "remote-file.txt should exist after pull")
	assert.Equal(t, "from remote", string(content))
}

// TestSync_NonFastForward_RebasesCleanly verifies that pullManagedRepo handles
// a non-fast-forward scenario (local and remote diverged) by rebasing cleanly.
func TestSync_NonFastForward_RebasesCleanly(t *testing.T) {
	if testing.Short() {
		t.Skip("short: Gitea digital twin")
	}

	g := getSharedGitea(t)
	cloneURL := g.createRepo(t, "sync-rebase")

	cloneDir := filepath.Join(t.TempDir(), "local")
	g.cloneRepo(t, cloneURL, cloneDir)

	// make a local commit (not pushed)
	require.NoError(t, os.WriteFile(filepath.Join(cloneDir, "local-file.txt"), []byte("local content"), 0o644))
	cmd := exec.Command("git", "-C", cloneDir, "add", "local-file.txt")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git add failed: %s", string(out))
	cmd = exec.Command("git", "-C", cloneDir, "commit", "-m", "add local file")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "git commit failed: %s", string(out))

	// push a different file from remote (creates divergence)
	g.pushFromTempClone(t, cloneURL, "remote-file.txt", "from remote")

	// pull with rebase
	isolateCredentials(t)
	projectDir := setupProjectWithConfig(t, "")
	s := newTestScheduler(projectDir)

	opts := ManagedRepoPullOpts{
		RepoPath:     cloneDir,
		RepoName:     "test",
		ProjectRoot:  projectDir,
		SyncInterval: time.Second,
		MinFetchAge:  0,
		Logger:       slog.Default(),
	}

	result := s.pullManagedRepo(context.Background(), opts)
	assert.NoError(t, result.Err, "rebase pull should succeed without conflicts")
	assert.False(t, result.Skipped, "pull should not be skipped — divergence requires actual rebase")

	// verify both files exist
	content, err := os.ReadFile(filepath.Join(cloneDir, "local-file.txt"))
	require.NoError(t, err, "local-file.txt should exist after rebase")
	assert.Equal(t, "local content", string(content))

	content, err = os.ReadFile(filepath.Join(cloneDir, "remote-file.txt"))
	require.NoError(t, err, "remote-file.txt should exist after rebase")
	assert.Equal(t, "from remote", string(content))
}

// TestSync_PullWithUncommittedChanges_Autostash verifies that pullManagedRepo
// uses --autostash to preserve uncommitted local changes while pulling new
// remote commits.
func TestSync_PullWithUncommittedChanges_Autostash(t *testing.T) {
	if testing.Short() {
		t.Skip("short: Gitea digital twin")
	}

	g := getSharedGitea(t)
	cloneURL := g.createRepo(t, "sync-autostash")

	cloneDir := filepath.Join(t.TempDir(), "local")
	g.cloneRepo(t, cloneURL, cloneDir)

	// make uncommitted change to tracked file
	require.NoError(t, os.WriteFile(filepath.Join(cloneDir, "README.md"), []byte("modified"), 0o644))

	// push a new file from remote
	g.pushFromTempClone(t, cloneURL, "remote-new.txt", "remote content")

	// pull — should autostash the dirty README.md
	isolateCredentials(t)
	projectDir := setupProjectWithConfig(t, "")
	s := newTestScheduler(projectDir)

	opts := ManagedRepoPullOpts{
		RepoPath:     cloneDir,
		RepoName:     "test",
		ProjectRoot:  projectDir,
		SyncInterval: time.Second,
		MinFetchAge:  0,
		Logger:       slog.Default(),
	}

	result := s.pullManagedRepo(context.Background(), opts)
	assert.NoError(t, result.Err, "pull with uncommitted changes should succeed via autostash")
	assert.False(t, result.Skipped, "pull should not be skipped — new remote content requires actual pull")

	// verify uncommitted change preserved
	content, err := os.ReadFile(filepath.Join(cloneDir, "README.md"))
	require.NoError(t, err, "README.md should exist after pull")
	assert.Equal(t, "modified", string(content))

	// verify remote file pulled
	content, err = os.ReadFile(filepath.Join(cloneDir, "remote-new.txt"))
	require.NoError(t, err, "remote-new.txt should exist after pull")
	assert.Equal(t, "remote content", string(content))
}
