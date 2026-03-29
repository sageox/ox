package daemon

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- helpers ---

// gcInitGitRepo creates a git repo at dir with an initial commit and returns the path.
// Configures user.email/user.name locally so commits work in CI.
func gcInitGitRepo(t *testing.T, dir string) {
	t.Helper()
	require.NoError(t, exec.Command("git", "init", "--initial-branch=main", dir).Run())
	gitConfig(t, dir)
	// initial commit so HEAD exists
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("init"), 0644))
	require.NoError(t, exec.Command("git", "-C", dir, "add", "README.md").Run())
	require.NoError(t, exec.Command("git", "-C", dir, "commit", "-m", "initial").Run())
}

// gcInitBareAndClone creates a bare repo, clones it, and returns (bareDir, cloneDir).
// The clone has a tracking branch so gcPushUnpushedCommits can detect unpushed commits.
func gcInitBareAndClone(t *testing.T, parent string) (string, string) {
	t.Helper()
	bareDir := filepath.Join(parent, "origin.git")
	cloneDir := filepath.Join(parent, "clone")

	require.NoError(t, exec.Command("git", "init", "--bare", "--initial-branch=main", bareDir).Run())

	// seed with an initial commit via a temp working tree
	workDir := filepath.Join(parent, "seed")
	require.NoError(t, exec.Command("git", "clone", bareDir, workDir).Run())
	gitConfig(t, workDir)
	require.NoError(t, os.WriteFile(filepath.Join(workDir, "README.md"), []byte("init"), 0644))
	require.NoError(t, exec.Command("git", "-C", workDir, "add", "README.md").Run())
	require.NoError(t, exec.Command("git", "-C", workDir, "commit", "-m", "initial").Run())
	require.NoError(t, exec.Command("git", "-C", workDir, "push", "origin", "HEAD:main").Run())

	require.NoError(t, exec.Command("git", "clone", bareDir, cloneDir).Run())
	gitConfig(t, cloneDir)

	return bareDir, cloneDir
}

// gcTestScheduler returns a minimal SyncScheduler suitable for calling
// the GC helper methods directly (gcCaptureDiff, gcRestoreDiff, etc.).
func gcTestScheduler(t *testing.T) *SyncScheduler {
	t.Helper()
	projectDir := setupProjectWithConfig(t, "# empty\n")
	return newTestScheduler(projectDir)
}

// --- Phase tests ---

func TestGC_CaptureDiff(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	isolateCredentials(t)

	s := gcTestScheduler(t)
	ctx := context.Background()
	dir := t.TempDir()
	gcInitGitRepo(t, dir)

	// no uncommitted changes => hasDiff=false
	diffFile := filepath.Join(dir, ".gc-diff")
	hasDiff, err := s.gcCaptureDiff(ctx, dir, diffFile)
	require.NoError(t, err)
	assert.False(t, hasDiff, "no changes should produce no diff")
	_, statErr := os.Stat(diffFile)
	assert.True(t, os.IsNotExist(statErr), "diff file should not be created when there are no changes")

	// make an uncommitted modification
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("modified"), 0644))

	hasDiff, err = s.gcCaptureDiff(ctx, dir, diffFile)
	require.NoError(t, err)
	assert.True(t, hasDiff, "uncommitted changes should produce a diff")

	data, err := os.ReadFile(diffFile)
	require.NoError(t, err)
	assert.Contains(t, string(data), "modified", "diff should contain the modification")
}

func TestGC_CaptureUntracked(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	isolateCredentials(t)

	s := gcTestScheduler(t)
	ctx := context.Background()
	dir := t.TempDir()
	gcInitGitRepo(t, dir)

	destDir := filepath.Join(t.TempDir(), "untracked-backup")

	// no untracked files => false
	has, err := s.gcCaptureUntracked(ctx, dir, destDir)
	require.NoError(t, err)
	assert.False(t, has)

	// add untracked files including a nested one
	require.NoError(t, os.WriteFile(filepath.Join(dir, "new.txt"), []byte("new content"), 0644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "sub"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "sub", "nested.txt"), []byte("nested"), 0644))

	has, err = s.gcCaptureUntracked(ctx, dir, destDir)
	require.NoError(t, err)
	assert.True(t, has)

	// verify captured files
	data, err := os.ReadFile(filepath.Join(destDir, "new.txt"))
	require.NoError(t, err)
	assert.Equal(t, "new content", string(data))

	data, err = os.ReadFile(filepath.Join(destDir, "sub", "nested.txt"))
	require.NoError(t, err)
	assert.Equal(t, "nested", string(data))
}

func TestGC_RestoreDiff(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	isolateCredentials(t)

	s := gcTestScheduler(t)
	ctx := context.Background()

	// create source repo and capture a diff
	srcDir := t.TempDir()
	gcInitGitRepo(t, srcDir)
	require.NoError(t, os.WriteFile(filepath.Join(srcDir, "README.md"), []byte("changed line"), 0644))

	diffFile := filepath.Join(t.TempDir(), "patch.diff")
	hasDiff, err := s.gcCaptureDiff(ctx, srcDir, diffFile)
	require.NoError(t, err)
	require.True(t, hasDiff)

	// create a "new clone" (same initial state as before modification)
	dstDir := t.TempDir()
	gcInitGitRepo(t, dstDir)

	// apply the captured diff
	err = s.gcRestoreDiff(ctx, dstDir, diffFile)
	require.NoError(t, err)

	data, err := os.ReadFile(filepath.Join(dstDir, "README.md"))
	require.NoError(t, err)
	assert.Equal(t, "changed line", string(data))
}

func TestGC_RestoreUntracked(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	isolateCredentials(t)

	s := gcTestScheduler(t)

	// prepare an untracked backup directory
	backupDir := filepath.Join(t.TempDir(), "backup")
	require.NoError(t, os.MkdirAll(filepath.Join(backupDir, "deep"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(backupDir, "a.txt"), []byte("aaa"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(backupDir, "deep", "b.txt"), []byte("bbb"), 0644))

	// create target repo
	repoDir := t.TempDir()
	gcInitGitRepo(t, repoDir)

	err := s.gcRestoreUntracked(repoDir, backupDir)
	require.NoError(t, err)

	data, err := os.ReadFile(filepath.Join(repoDir, "a.txt"))
	require.NoError(t, err)
	assert.Equal(t, "aaa", string(data))

	data, err = os.ReadFile(filepath.Join(repoDir, "deep", "b.txt"))
	require.NoError(t, err)
	assert.Equal(t, "bbb", string(data))
}

func TestGC_PreserveRestoreCache(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	repoDir := t.TempDir()
	cacheDir := filepath.Join(repoDir, ".sageox", "cache", "codedb")
	require.NoError(t, os.MkdirAll(cacheDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(cacheDir, "index.db"), []byte("sqlitedata"), 0644))

	backupDir := filepath.Join(t.TempDir(), "cache-backup")

	// preserve
	err := gcPreserveCache(repoDir, backupDir)
	require.NoError(t, err)

	data, err := os.ReadFile(filepath.Join(backupDir, "codedb", "index.db"))
	require.NoError(t, err)
	assert.Equal(t, "sqlitedata", string(data))

	// simulate new clone (no cache)
	newRepo := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(newRepo, ".sageox"), 0755))

	err = gcRestoreCache(backupDir, newRepo)
	require.NoError(t, err)

	data, err = os.ReadFile(filepath.Join(newRepo, ".sageox", "cache", "codedb", "index.db"))
	require.NoError(t, err)
	assert.Equal(t, "sqlitedata", string(data))
}

func TestGC_PreserveCache_NoCacheDir(t *testing.T) {
	// when .sageox/cache doesn't exist, preserve is a no-op
	repoDir := t.TempDir()
	backupDir := filepath.Join(t.TempDir(), "cache-backup")

	err := gcPreserveCache(repoDir, backupDir)
	require.NoError(t, err)

	_, statErr := os.Stat(backupDir)
	assert.True(t, os.IsNotExist(statErr), "backup dir should not be created when no cache exists")
}

// --- Lock tests ---

func TestGC_AcquireReleaseLock(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "gc.lock")

	f, err := acquireGCLock(lockPath)
	require.NoError(t, err)
	require.NotNil(t, f)

	// second acquire should fail (lock held)
	_, err = acquireGCLock(lockPath)
	assert.Error(t, err, "concurrent acquire should fail while lock is held")
	assert.Contains(t, err.Error(), "gc lock held")

	releaseGCLock(f, lockPath)

	// after release, acquire should succeed again
	f2, err := acquireGCLock(lockPath)
	require.NoError(t, err)
	require.NotNil(t, f2)
	releaseGCLock(f2, lockPath)
}

// --- Lifecycle tests ---

func TestGC_FullCycle_TeamContext(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	isolateCredentials(t)

	tmp := t.TempDir()
	bareDir, cloneDir := gcInitBareAndClone(t, tmp)
	cloneURL := "file://" + bareDir

	// add team context structure to bare repo via the clone
	for _, f := range []string{"SOUL.md", ".sageox/config.json"} {
		fullPath := filepath.Join(cloneDir, f)
		require.NoError(t, os.MkdirAll(filepath.Dir(fullPath), 0755))
		require.NoError(t, os.WriteFile(fullPath, []byte("content"), 0644))
	}
	require.NoError(t, exec.Command("git", "-C", cloneDir, "add", ".").Run())
	require.NoError(t, exec.Command("git", "-C", cloneDir, "commit", "-m", "add structure").Run())
	require.NoError(t, exec.Command("git", "-C", cloneDir, "push", "origin", "HEAD:main").Run())

	// add uncommitted tracked changes and untracked files
	require.NoError(t, os.WriteFile(filepath.Join(cloneDir, "README.md"), []byte("dirty"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(cloneDir, "local-only.txt"), []byte("untracked"), 0644))

	projectDir := setupProjectWithConfig(t, "# empty\n")
	s := newTestScheduler(projectDir)
	ctx := context.Background()

	ws := WorkspaceState{
		ID:       "team-test",
		Type:     WorkspaceTypeTeamContext,
		TeamName: "test-team",
		Path:     cloneDir,
		CloneURL: cloneURL,
		Exists:   true,
	}

	result := s.runBlueGreenGC(ctx, ws)
	assert.Equal(t, gcSuccess, result, "full cycle should succeed")

	// the repo should still exist at the original path
	assert.DirExists(t, cloneDir)
	assert.FileExists(t, filepath.Join(cloneDir, ".git", "HEAD"))
	assert.FileExists(t, filepath.Join(cloneDir, "SOUL.md"))

	// uncommitted diff should be restored
	data, err := os.ReadFile(filepath.Join(cloneDir, "README.md"))
	require.NoError(t, err)
	assert.Equal(t, "dirty", string(data), "uncommitted tracked change should survive GC")

	// untracked file should be restored
	data, err = os.ReadFile(filepath.Join(cloneDir, "local-only.txt"))
	require.NoError(t, err)
	assert.Equal(t, "untracked", string(data), "untracked file should survive GC")

	// old and new staging dirs should be cleaned up
	assert.NoDirExists(t, cloneDir+".old")
	assert.NoDirExists(t, cloneDir+".new")
}

func TestGC_FullCycle_Ledger(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	isolateCredentials(t)

	tmp := t.TempDir()

	// set up bare ledger repo with sessions/ directory
	bareDir := filepath.Join(tmp, "ledger.bare")
	seedDir := filepath.Join(tmp, "seed")
	require.NoError(t, exec.Command("git", "init", "--bare", "--initial-branch=main", bareDir).Run())
	require.NoError(t, exec.Command("git", "clone", bareDir, seedDir).Run())
	gitConfig(t, seedDir)

	for _, dir := range []string{"sessions", ".sync"} {
		require.NoError(t, os.MkdirAll(filepath.Join(seedDir, dir), 0755))
		require.NoError(t, os.WriteFile(filepath.Join(seedDir, dir, ".gitkeep"), []byte(""), 0644))
	}
	require.NoError(t, exec.Command("git", "-C", seedDir, "add", ".").Run())
	require.NoError(t, exec.Command("git", "-C", seedDir, "commit", "-m", "initial ledger").Run())
	require.NoError(t, exec.Command("git", "-C", seedDir, "push", "origin", "HEAD:main").Run())

	// clone ledger
	cloneDir := filepath.Join(tmp, "ledger")
	require.NoError(t, exec.Command("git", "clone", "file://"+bareDir, cloneDir).Run())
	gitConfig(t, cloneDir)

	// add cache that should survive GC
	cacheDir := filepath.Join(cloneDir, ".sageox", "cache", "codedb")
	require.NoError(t, os.MkdirAll(cacheDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(cacheDir, "index.db"), []byte("precious"), 0644))

	// add untracked file
	require.NoError(t, os.WriteFile(filepath.Join(cloneDir, "local-notes.txt"), []byte("notes"), 0644))

	projectDir := setupProjectWithConfig(t, "# empty\n")
	s := newTestScheduler(projectDir)
	ctx := context.Background()

	ws := WorkspaceState{
		ID:       "ledger-test",
		Type:     WorkspaceTypeLedger,
		Path:     cloneDir,
		CloneURL: "file://" + bareDir,
		Exists:   true,
	}

	result := s.runBlueGreenGC(ctx, ws)
	assert.Equal(t, gcSuccess, result, "ledger full cycle should succeed")

	// repo exists at original path
	assert.DirExists(t, cloneDir)
	assert.FileExists(t, filepath.Join(cloneDir, ".git", "HEAD"))

	// sessions/ survived (came from the fresh clone)
	assert.DirExists(t, filepath.Join(cloneDir, "sessions"))

	// cache should be preserved
	data, err := os.ReadFile(filepath.Join(cloneDir, ".sageox", "cache", "codedb", "index.db"))
	require.NoError(t, err)
	assert.Equal(t, "precious", string(data), "cache should survive GC reclone")

	// untracked file restored
	data, err = os.ReadFile(filepath.Join(cloneDir, "local-notes.txt"))
	require.NoError(t, err)
	assert.Equal(t, "notes", string(data), "untracked file should survive GC")

	// staging artifacts cleaned up
	assert.NoDirExists(t, cloneDir+".old")
	assert.NoDirExists(t, cloneDir+".new")
	_, err = os.Stat(cloneDir + ".gc-cache")
	assert.True(t, os.IsNotExist(err), "cache backup should be cleaned up after restore")
}

func TestGC_SkipsWhenLocked(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	lockPath := filepath.Join(t.TempDir(), "repo.gc-lock")

	// hold the lock
	f, err := acquireGCLock(lockPath)
	require.NoError(t, err)
	defer releaseGCLock(f, lockPath)

	// second caller should see the lock and fail
	_, err = acquireGCLock(lockPath)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "gc lock held")
}

// --- Failure tests ---

func TestGC_CloneFailure_Rollback(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	isolateCredentials(t)

	tmp := t.TempDir()
	repoDir := filepath.Join(tmp, "repo")
	gcInitGitRepo(t, repoDir)

	// add SOUL.md + .sageox so the repo has content worth preserving
	require.NoError(t, os.MkdirAll(filepath.Join(repoDir, ".sageox"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "SOUL.md"), []byte("soul"), 0644))
	require.NoError(t, exec.Command("git", "-C", repoDir, "add", ".").Run())
	require.NoError(t, exec.Command("git", "-C", repoDir, "commit", "-m", "add files").Run())

	projectDir := setupProjectWithConfig(t, "# empty\n")
	s := newTestScheduler(projectDir)
	ctx := context.Background()

	ws := WorkspaceState{
		ID:       "fail-clone",
		Type:     WorkspaceTypeTeamContext,
		TeamName: "test-team",
		Path:     repoDir,
		CloneURL: "file:///nonexistent/repo.git", // clone will fail
		Exists:   true,
	}

	result := s.runBlueGreenGC(ctx, ws)
	// repo has no remote, so gcPushUnpushedCommits sees dirty state → gcSkippedDirty
	assert.Equal(t, gcSkippedDirty, result, "should skip when repo has no remote to push to")

	// original repo must be preserved regardless
	assert.DirExists(t, repoDir)
	assert.FileExists(t, filepath.Join(repoDir, ".git", "HEAD"))
	data, err := os.ReadFile(filepath.Join(repoDir, "SOUL.md"))
	require.NoError(t, err)
	assert.Equal(t, "soul", string(data), "original repo content must be preserved")
}

// --- Validation tests ---

func TestGC_ValidateGCClone_Valid(t *testing.T) {
	s := &SyncScheduler{
		logger: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
	}

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".git"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".sageox"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SOUL.md"), []byte("soul"), 0644))

	assert.True(t, s.validateGCClone(dir, nil))
}

func TestGC_ValidateGCClone_MissingGit(t *testing.T) {
	s := &SyncScheduler{
		logger: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
	}

	dir := t.TempDir()
	// no .git directory
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".sageox"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SOUL.md"), []byte("soul"), 0644))

	assert.False(t, s.validateGCClone(dir, nil))
}

func TestGC_ValidateGCClone_MissingSageox(t *testing.T) {
	s := &SyncScheduler{
		logger: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
	}

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".git"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SOUL.md"), []byte("soul"), 0644))

	assert.False(t, s.validateGCClone(dir, nil))
}

func TestGC_ValidateGCClone_NoCoreFiles(t *testing.T) {
	s := &SyncScheduler{
		logger: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
	}

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".git"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".sageox"), 0755))
	// no SOUL.md, TEAM.md, or MEMORY.md

	assert.False(t, s.validateGCClone(dir, nil))
}

func TestGC_ValidateLedgerGCClone_Valid(t *testing.T) {
	s := &SyncScheduler{
		logger: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
	}

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".git"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "sessions"), 0755))

	assert.True(t, s.validateLedgerGCClone(dir))
}

func TestGC_ValidateLedgerGCClone_MissingSessions(t *testing.T) {
	s := &SyncScheduler{
		logger: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
	}

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".git"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0644))

	assert.False(t, s.validateLedgerGCClone(dir))
}
