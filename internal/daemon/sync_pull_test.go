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
	cmd.Env = append(os.Environ(), // safe: git subprocess in temp dir, not ox CLI
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

func TestDetectDivergedBranches_NormalPush(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git operations")
	}
	bareDir, cloneDir := setupBareAndClone(t)

	// push a normal commit from a separate clone
	pushFromSeparateClone(t, bareDir, "remote.txt", "remote content")

	// fetch in our clone so we see the new commit
	gitCmd(t, cloneDir, "fetch", "origin")

	s := newPullTestScheduler(t, cloneDir)
	assert.False(t, s.detectDivergedBranches(context.Background()),
		"normal fast-forward should not be detected as force push")
}

func TestDetectDivergedBranches_Diverged(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git operations")
	}
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
	assert.True(t, s.detectDivergedBranches(context.Background()),
		"diverged branches should be detected as force push")
}

func TestDetectDivergedBranches_NoRemote(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git operations")
	}
	dir := t.TempDir()
	gitCmd(t, dir, "init")
	gitCmd(t, dir, "config", "user.name", "test")
	gitCmd(t, dir, "config", "user.email", "test@test.com")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "file.txt"), []byte("data"), 0o644))
	gitCmd(t, dir, "add", "file.txt")
	gitCmd(t, dir, "commit", "-m", "init")

	s := newPullTestScheduler(t, dir)
	assert.False(t, s.detectDivergedBranches(context.Background()),
		"repo with no remote should return false gracefully")
}

func TestDoPull_StaleLockFile(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git operations")
	}
	_, cloneDir := setupBareAndClone(t)

	// create a stale lock file
	lockPath := filepath.Join(cloneDir, ".git", "index.lock")
	require.NoError(t, os.WriteFile(lockPath, []byte("stale"), 0o644))

	s := newPullTestScheduler(t, cloneDir)

	err := s.doPull(context.Background(), nil, false, true)
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

func TestDoPull_CorruptRepo_RenamedAside(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git operations")
	}
	_, cloneDir := setupBareAndClone(t)

	// corrupt the repo by emptying .git/HEAD
	headPath := filepath.Join(cloneDir, ".git", "HEAD")
	require.NoError(t, os.WriteFile(headPath, []byte(""), 0o644))

	s := newPullTestScheduler(t, cloneDir)

	err := s.doPull(context.Background(), nil, true, true)
	assert.NoError(t, err, "doPull should return nil for corrupt repo (triggers re-clone)")

	// original path should no longer be a valid git repo (renamed aside)
	_, statErr := os.Stat(filepath.Join(cloneDir, ".git", "HEAD"))
	assert.True(t, os.IsNotExist(statErr), "original repo should be renamed aside")

	// verify .bak.* path exists
	parent := filepath.Dir(cloneDir)
	entries, _ := os.ReadDir(parent)
	found := false
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), filepath.Base(cloneDir)+".bak.") {
			found = true
			break
		}
	}
	assert.True(t, found, "backup directory should exist after corrupt repo detection")
}

func TestDoPull_RebaseInProgress_Skips(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git operations")
	}
	_, cloneDir := setupBareAndClone(t)

	// simulate broken rebase state
	rebaseMergeDir := filepath.Join(cloneDir, ".git", "rebase-merge")
	require.NoError(t, os.MkdirAll(rebaseMergeDir, 0o755))

	s := newPullTestScheduler(t, cloneDir)

	err := s.doPull(context.Background(), nil, false, true)
	assert.NoError(t, err, "doPull should skip silently when rebase is in progress")
}

func TestDoPull_DivergedLedger_RebasesSuccessfully(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git operations")
	}
	bareDir, cloneDir := setupBareAndClone(t)

	// create a local commit (simulates CLI session upload)
	require.NoError(t, os.WriteFile(filepath.Join(cloneDir, "local-session.txt"), []byte("session data"), 0o644))
	gitCmd(t, cloneDir, "add", "local-session.txt")
	gitCmd(t, cloneDir, "commit", "-m", "local session")

	// push a different file from a separate clone (simulates cloud/github sync)
	pushFromSeparateClone(t, bareDir, "remote-data.txt", "github sync data")

	s := newPullTestScheduler(t, cloneDir)

	err := s.doPull(context.Background(), nil, true, true)
	assert.NoError(t, err, "doPull should succeed by rebasing diverged branches")

	// verify both files exist (rebase landed both)
	assert.FileExists(t, filepath.Join(cloneDir, "local-session.txt"))
	assert.FileExists(t, filepath.Join(cloneDir, "remote-data.txt"))

	// verify no diverged or merge conflict issue was set
	for _, issue := range s.issues.GetIssues() {
		assert.NotEqual(t, IssueTypeDiverged, issue.Type,
			"no diverged issue should be set after successful rebase")
		assert.NotEqual(t, IssueTypeMergeConflict, issue.Type,
			"no merge conflict issue should be set after clean rebase")
	}
}

func TestDoPull_DivergedLedger_ConflictInSafePath_AutoResolves(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git operations")
	}
	bareDir, cloneDir := setupBareAndClone(t)

	// local commit: modify a file under data/github/ (safe auto-resolve path)
	require.NoError(t, os.MkdirAll(filepath.Join(cloneDir, "data", "github"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cloneDir, "data", "github", "prs.json"), []byte(`{"local":true}`), 0o644))
	gitCmd(t, cloneDir, "add", "data/github/prs.json")
	gitCmd(t, cloneDir, "commit", "-m", "local github data")

	// push conflicting change from separate clone
	tmpClone := filepath.Join(t.TempDir(), "conflict-clone")
	gitCmd(t, t.TempDir(), "clone", bareDir, tmpClone)
	gitCmd(t, tmpClone, "config", "user.name", "test")
	gitCmd(t, tmpClone, "config", "user.email", "test@test.com")
	require.NoError(t, os.MkdirAll(filepath.Join(tmpClone, "data", "github"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(tmpClone, "data", "github", "prs.json"), []byte(`{"remote":true}`), 0o644))
	gitCmd(t, tmpClone, "add", "data/github/prs.json")
	gitCmd(t, tmpClone, "commit", "-m", "remote github data")
	gitCmd(t, tmpClone, "push", "origin", "HEAD")

	s := newPullTestScheduler(t, cloneDir)

	err := s.doPull(context.Background(), nil, true, true)
	assert.NoError(t, err, "doPull should auto-resolve conflict in data/github/")

	// verify no issues set
	issues := s.issues.GetIssues()
	assert.Empty(t, issues, "no issues should remain after auto-resolve")
}

func TestDoPull_ConflictInUnsafePath_ReportsIssueAndAborts(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git operations")
	}
	bareDir, cloneDir := setupBareAndClone(t)

	// local commit: modify SOUL.md (not under any safe auto-resolve prefix)
	require.NoError(t, os.WriteFile(filepath.Join(cloneDir, "SOUL.md"), []byte("local team soul"), 0o644))
	gitCmd(t, cloneDir, "add", "SOUL.md")
	gitCmd(t, cloneDir, "commit", "-m", "local soul edit")

	// push conflicting change from separate clone
	tmpClone := filepath.Join(t.TempDir(), "conflict-clone")
	gitCmd(t, t.TempDir(), "clone", bareDir, tmpClone)
	gitCmd(t, tmpClone, "config", "user.name", "test")
	gitCmd(t, tmpClone, "config", "user.email", "test@test.com")
	require.NoError(t, os.WriteFile(filepath.Join(tmpClone, "SOUL.md"), []byte("remote team soul"), 0o644))
	gitCmd(t, tmpClone, "add", "SOUL.md")
	gitCmd(t, tmpClone, "commit", "-m", "remote soul edit")
	gitCmd(t, tmpClone, "push", "origin", "HEAD")

	s := newPullTestScheduler(t, cloneDir)

	err := s.doPull(context.Background(), nil, true, true)
	assert.Error(t, err, "doPull should fail when conflict is in unsafe path")

	// rebase should have been aborted (not left in progress)
	rebaseMerge := filepath.Join(cloneDir, ".git", "rebase-merge")
	_, statErr := os.Stat(rebaseMerge)
	assert.True(t, os.IsNotExist(statErr), "rebase should be aborted, not left in progress")

	// after auto-resolve fails and rebase is aborted, UU entries are gone
	// but divergence is detected → IssueTypeDiverged
	issues := s.issues.GetIssues()
	foundDiverged := false
	for _, issue := range issues {
		if issue.Type == IssueTypeDiverged {
			foundDiverged = true
			assert.Contains(t, issue.Summary, "diverged")
			assert.Contains(t, issue.Summary, "ox doctor --fix")
			break
		}
	}
	assert.True(t, foundDiverged, "should report IssueTypeDiverged after auto-resolve fails on unsafe path")
}

func TestDoPull_DivergedWithMixedConflicts_AbortsRebase(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git operations")
	}
	bareDir, cloneDir := setupBareAndClone(t)

	// local commit: modify both a safe and unsafe file
	require.NoError(t, os.MkdirAll(filepath.Join(cloneDir, "data", "github"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cloneDir, "data", "github", "prs.json"), []byte(`{"local":true}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(cloneDir, "AGENTS.md"), []byte("local agents"), 0o644))
	gitCmd(t, cloneDir, "add", ".")
	gitCmd(t, cloneDir, "commit", "-m", "local mixed changes")

	// push conflicting changes to both files from remote
	tmpClone := filepath.Join(t.TempDir(), "conflict-clone")
	gitCmd(t, t.TempDir(), "clone", bareDir, tmpClone)
	gitCmd(t, tmpClone, "config", "user.name", "test")
	gitCmd(t, tmpClone, "config", "user.email", "test@test.com")
	require.NoError(t, os.MkdirAll(filepath.Join(tmpClone, "data", "github"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(tmpClone, "data", "github", "prs.json"), []byte(`{"remote":true}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(tmpClone, "AGENTS.md"), []byte("remote agents"), 0o644))
	gitCmd(t, tmpClone, "add", ".")
	gitCmd(t, tmpClone, "commit", "-m", "remote mixed changes")
	gitCmd(t, tmpClone, "push", "origin", "HEAD")

	s := newPullTestScheduler(t, cloneDir)

	err := s.doPull(context.Background(), nil, true, true)
	assert.Error(t, err, "doPull should fail when mixed safe/unsafe conflicts exist")

	// rebase must be aborted
	rebaseMerge := filepath.Join(cloneDir, ".git", "rebase-merge")
	_, statErr := os.Stat(rebaseMerge)
	assert.True(t, os.IsNotExist(statErr), "rebase should be aborted after mixed conflict failure")
}

func TestPullTeamContext_DivergedBranches_RebasesSuccessfully(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git operations")
	}
	bareDir, cloneDir := setupBareAndClone(t)

	// local commit (simulates daemon EnsureCheckoutGitignore or user edit)
	require.NoError(t, os.WriteFile(filepath.Join(cloneDir, "local-doc.md"), []byte("local docs"), 0o644))
	gitCmd(t, cloneDir, "add", "local-doc.md")
	gitCmd(t, cloneDir, "commit", "-m", "local documentation")

	// push a different file from remote (simulates new discussion synced)
	pushFromSeparateClone(t, bareDir, "remote-discussion.md", "architecture discussion")

	s := newPullTestScheduler(t, cloneDir)

	err := s.pullTeamContext(context.Background(), cloneDir)
	assert.NoError(t, err, "pullTeamContext should rebase diverged branches")

	// both files should exist after rebase
	assert.FileExists(t, filepath.Join(cloneDir, "local-doc.md"))
	assert.FileExists(t, filepath.Join(cloneDir, "remote-discussion.md"))

	// no diverged or conflict issues
	for _, issue := range s.issues.GetIssues() {
		assert.NotEqual(t, IssueTypeDiverged, issue.Type)
		assert.NotEqual(t, IssueTypeMergeConflict, issue.Type)
	}
}

func TestPullTeamContext_ConflictReportsIssue(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git operations")
	}
	bareDir, cloneDir := setupBareAndClone(t)

	// local commit: edit SOUL.md
	require.NoError(t, os.WriteFile(filepath.Join(cloneDir, "SOUL.md"), []byte("local soul"), 0o644))
	gitCmd(t, cloneDir, "add", "SOUL.md")
	gitCmd(t, cloneDir, "commit", "-m", "local soul")

	// push conflicting SOUL.md from remote
	tmpClone := filepath.Join(t.TempDir(), "conflict-clone")
	gitCmd(t, t.TempDir(), "clone", bareDir, tmpClone)
	gitCmd(t, tmpClone, "config", "user.name", "test")
	gitCmd(t, tmpClone, "config", "user.email", "test@test.com")
	require.NoError(t, os.WriteFile(filepath.Join(tmpClone, "SOUL.md"), []byte("remote soul"), 0o644))
	gitCmd(t, tmpClone, "add", "SOUL.md")
	gitCmd(t, tmpClone, "commit", "-m", "remote soul")
	gitCmd(t, tmpClone, "push", "origin", "HEAD")

	s := newPullTestScheduler(t, cloneDir)

	err := s.pullTeamContext(context.Background(), cloneDir)
	assert.Error(t, err, "pullTeamContext should fail on conflict (no auto-resolve prefixes)")

	// team context has no auto-resolve prefixes, so pullManagedRepo does NOT
	// enter the auto-resolve block. The diverged+failed pull reports IssueTypeDiverged.
	issues := s.issues.GetIssues()
	foundDiverged := false
	for _, issue := range issues {
		if issue.Type == IssueTypeDiverged {
			foundDiverged = true
			assert.Contains(t, issue.Summary, "diverged")
			assert.Contains(t, issue.Summary, "ox doctor --fix")
			break
		}
	}
	assert.True(t, foundDiverged, "should report IssueTypeDiverged for team context conflict")
}

func TestDoPull_SuccessfulPull(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git operations")
	}
	bareDir, cloneDir := setupBareAndClone(t)

	// push a change from a separate clone
	pushFromSeparateClone(t, bareDir, "new-file.txt", "new content")

	s := newPullTestScheduler(t, cloneDir)

	err := s.doPull(context.Background(), nil, true, true)
	assert.NoError(t, err)

	// verify the file was pulled
	data, err := os.ReadFile(filepath.Join(cloneDir, "new-file.txt"))
	assert.NoError(t, err)
	assert.Equal(t, "new content", string(data))
}
