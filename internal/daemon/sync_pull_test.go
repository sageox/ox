package daemon

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/gitserver"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// gitCmd runs a git command in the given directory with isolated config.
func gitCmd(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=test",
		"GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=test",
		"GIT_COMMITTER_EMAIL=test@test.com",
		"GIT_COMMITTER_DATE=2026-01-01T00:00:00+00:00",
	)
	// disable gpg signing which fails in isolated test environments
	cmd.Env = append(cmd.Env, "GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=commit.gpgsign", "GIT_CONFIG_VALUE_0=false")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// setupBareAndClone creates a bare repo + clone with an initial commit.
// Returns (bareDir, cloneDir).
func setupBareAndClone(t *testing.T) (string, string) {
	t.Helper()

	bareDir := filepath.Join(t.TempDir(), "bare.git")
	cloneDir := filepath.Join(t.TempDir(), "clone")

	gitCmd(t, t.TempDir(), "init", "--bare", "--initial-branch=main", bareDir)

	// create a temporary clone to push initial commit
	tmpClone := filepath.Join(t.TempDir(), "tmp-clone")
	gitCmd(t, t.TempDir(), "clone", bareDir, tmpClone)
	gitCmd(t, tmpClone, "config", "user.name", "test")
	gitCmd(t, tmpClone, "config", "user.email", "test@test.com")
	require.NoError(t, os.WriteFile(filepath.Join(tmpClone, "init.txt"), []byte("initial"), 0o644))
	gitCmd(t, tmpClone, "add", "init.txt")
	gitCmd(t, tmpClone, "commit", "-m", "initial")
	gitCmd(t, tmpClone, "push", "origin", "HEAD")

	// create the actual clone
	gitCmd(t, t.TempDir(), "clone", bareDir, cloneDir)
	gitCmd(t, cloneDir, "config", "user.name", "test")
	gitCmd(t, cloneDir, "config", "user.email", "test@test.com")

	return bareDir, cloneDir
}

// newTestScheduler creates a SyncScheduler for testing with minimal config.
func newPullTestScheduler(t *testing.T, ledgerDir string) *SyncScheduler {
	t.Helper()

	prevConfigDir := gitserver.TestSetConfigDirOverride(t.TempDir())
	prevForceFile := gitserver.TestSetForceFileStorage(true)
	t.Cleanup(func() {
		gitserver.TestSetConfigDirOverride(prevConfigDir)
		gitserver.TestSetForceFileStorage(prevForceFile)
	})

	cfg := DefaultConfig()
	cfg.LedgerPath = ledgerDir
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	s := NewSyncScheduler(cfg, logger)
	s.issues = NewIssueTracker()
	return s
}

// pushFromSeparateClone pushes a change from a fresh clone to the bare repo.
func pushFromSeparateClone(t *testing.T, bareDir, filename, content string) {
	t.Helper()
	tmpClone := filepath.Join(t.TempDir(), "push-clone")
	gitCmd(t, t.TempDir(), "clone", bareDir, tmpClone)
	gitCmd(t, tmpClone, "config", "user.name", "test")
	gitCmd(t, tmpClone, "config", "user.email", "test@test.com")
	require.NoError(t, os.WriteFile(filepath.Join(tmpClone, filename), []byte(content), 0o644))
	gitCmd(t, tmpClone, "add", filename)
	gitCmd(t, tmpClone, "commit", "-m", "add "+filename)
	gitCmd(t, tmpClone, "push", "origin", "HEAD")
}

func TestDetectForcePush_NormalPush(t *testing.T) {
	bareDir, cloneDir := setupBareAndClone(t)

	// push a normal commit from a separate clone
	pushFromSeparateClone(t, bareDir, "remote.txt", "remote content")

	// fetch in our clone so we see the new commit
	gitCmd(t, cloneDir, "fetch", "origin")

	s := newPullTestScheduler(t, cloneDir)
	assert.False(t, s.detectForcePush(context.Background()),
		"normal fast-forward should not be detected as force push")
}

func TestDetectForcePush_ForcePush(t *testing.T) {
	bareDir, cloneDir := setupBareAndClone(t)

	// make a local commit
	require.NoError(t, os.WriteFile(filepath.Join(cloneDir, "local.txt"), []byte("local"), 0o644))
	gitCmd(t, cloneDir, "add", "local.txt")
	gitCmd(t, cloneDir, "commit", "-m", "local commit")

	// force push a rewritten history from a separate clone
	tmpClone := filepath.Join(t.TempDir(), "force-clone")
	gitCmd(t, t.TempDir(), "clone", bareDir, tmpClone)
	gitCmd(t, tmpClone, "config", "user.name", "test")
	gitCmd(t, tmpClone, "config", "user.email", "test@test.com")
	require.NoError(t, os.WriteFile(filepath.Join(tmpClone, "rewritten.txt"), []byte("rewritten"), 0o644))
	gitCmd(t, tmpClone, "add", "rewritten.txt")
	gitCmd(t, tmpClone, "commit", "-m", "rewritten history")
	gitCmd(t, tmpClone, "push", "--force", "origin", "HEAD")

	// fetch the force-pushed changes
	gitCmd(t, cloneDir, "fetch", "origin")

	s := newPullTestScheduler(t, cloneDir)
	assert.True(t, s.detectForcePush(context.Background()),
		"diverged branches should be detected as force push")
}

func TestDetectForcePush_NoRemote(t *testing.T) {
	dir := t.TempDir()
	gitCmd(t, dir, "init")
	gitCmd(t, dir, "config", "user.name", "test")
	gitCmd(t, dir, "config", "user.email", "test@test.com")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "file.txt"), []byte("data"), 0o644))
	gitCmd(t, dir, "add", "file.txt")
	gitCmd(t, dir, "commit", "-m", "init")

	s := newPullTestScheduler(t, dir)
	assert.False(t, s.detectForcePush(context.Background()),
		"repo with no remote should return false gracefully")
}

func TestDoPull_StaleLockFile(t *testing.T) {
	_, cloneDir := setupBareAndClone(t)

	// create a stale lock file
	lockPath := filepath.Join(cloneDir, ".git", "index.lock")
	require.NoError(t, os.WriteFile(lockPath, []byte("stale"), 0o644))

	s := newPullTestScheduler(t, cloneDir)

	err := s.doPull(context.Background(), nil, false)
	assert.NoError(t, err, "doPull should skip gracefully when lock file exists")

	// verify issue was recorded
	issues := s.issues.GetIssues()
	found := false
	for _, issue := range issues {
		if issue.Type == IssueTypeGitLock {
			found = true
			break
		}
	}
	assert.True(t, found, "should record IssueTypeGitLock")
}

func TestDoPull_SuccessfulPull(t *testing.T) {
	bareDir, cloneDir := setupBareAndClone(t)

	// push a change from a separate clone
	pushFromSeparateClone(t, bareDir, "new-file.txt", "new content")

	s := newPullTestScheduler(t, cloneDir)

	err := s.doPull(context.Background(), nil, true)
	assert.NoError(t, err)

	// verify the file was pulled
	data, err := os.ReadFile(filepath.Join(cloneDir, "new-file.txt"))
	assert.NoError(t, err)
	assert.Equal(t, "new content", string(data))
}
