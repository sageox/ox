package daemon

import (
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/gitserver"
	"github.com/stretchr/testify/require"
)

// bareRepoPath returns the bare repo path created by setupGitRepo for the given dir.
func bareRepoPath(dir string) string {
	return filepath.Join(filepath.Dir(dir), filepath.Base(dir)+".bare")
}

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
	cmd.Env = append(cmd.Env, "GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=commit.gpgsign", "GIT_CONFIG_VALUE_0=false")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return strings.TrimSpace(string(out))
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

// newPullTestScheduler creates a SyncScheduler for testing with minimal config.
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
	s.SetIssueTracker(NewIssueTracker())
	return s
}

// setupGitRepo initializes a git repo in the given directory with an initial commit
// and sets up a local "origin" remote that can be fetched from.
func setupGitRepo(t *testing.T, dir string) {
	t.Helper()

	// create a bare repo to act as "origin"
	bareDir := filepath.Join(filepath.Dir(dir), filepath.Base(dir)+".bare")
	require.NoError(t, os.MkdirAll(bareDir, 0755))

	initBareCmd := exec.Command("git", "init", "--bare", "-b", "main")
	initBareCmd.Dir = bareDir
	require.NoError(t, initBareCmd.Run())

	// init the working repo
	initCmd := exec.Command("git", "init", "-b", "main")
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
