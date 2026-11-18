package daemon

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/gitutil"
	"github.com/sageox/ox/internal/ledger"
	"github.com/sageox/ox/internal/paths"
	whisperstore "github.com/sageox/ox/internal/whisper/store"
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

// Failed preparation must leave the original checkout authoritative, even when
// a replacement was already cloned and a later GC retries the operation.
func TestGC_FailedPreparationPreservesOriginalAndRetries(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real Ledger clones and deadline expiry")
	}
	for _, phase := range []string{
		"restore cancellation", "restore cancellation without tracked changes", "swap deadline",
		"untracked capture error", "cache inspection error", "cache restore error", "swap signal error", "backup cleanup error",
	} {
		t.Run(phase, func(t *testing.T) {
			if phase == "cache inspection error" && runtime.GOOS == "windows" {
				t.Skip("requires a symlink loop to make backup inspection fail")
			}
			if phase == "backup cleanup error" && (runtime.GOOS == "windows" || os.Geteuid() == 0) {
				t.Skip("requires directory permissions to refuse backup removal")
			}
			bare, clone, scheduler := setupClonedLedger(t)
			files := map[string]string{
				"sessions/.gitkeep":                        "tracked edit",
				"sessions/local.txt":                       "untracked content",
				".sageox/cache/recording.txt":              "cached recording",
				ledger.PlanReviewPendingDir + "/only.json": "pending review",
			}
			if phase == "restore cancellation without tracked changes" {
				delete(files, "sessions/.gitkeep")
			}
			for path, content := range files {
				full := filepath.Join(clone, path)
				require.NoError(t, os.MkdirAll(filepath.Dir(full), 0755))
				require.NoError(t, os.WriteFile(full, []byte(content), 0600))
			}
			original, err := os.Stat(filepath.Join(clone, ".git"))
			require.NoError(t, err)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			restoreCache := gcRestoreCache
			t.Cleanup(func() { gcRestoreCache = restoreCache })
			preserveCache := gcPreserveCache
			t.Cleanup(func() { gcPreserveCache = preserveCache })
			originalCopy := copyFile
			t.Cleanup(func() { copyFile = originalCopy })
			hookCalled := false
			wantResult := gcFailed
			switch phase {
			case "restore cancellation", "restore cancellation without tracked changes":
				gcRestoreCache = func(backup, dst string) error {
					err := restoreCache(backup, dst)
					cancel()
					return err
				}
			case "swap deadline":
				// The hook holds the already-prepared clone until an actual
				// deadline expires, without relying on racing a fast local clone.
				ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(5*time.Second))
				defer cancel()
				scheduler.gcSwapWindowTestHook = func() {
					hookCalled = true
					<-ctx.Done()
				}
			case "untracked capture error":
				wantResult = gcSkippedDirty
				copyFile = func(src, dst string) error {
					if src == filepath.Join(clone, "sessions", "local.txt") {
						hookCalled = true
						return errors.New("synthetic read failure")
					}
					return originalCopy(src, dst)
				}
			case "cache inspection error":
				gcPreserveCache = func(src, backup string) error {
					if err := preserveCache(src, backup); err != nil {
						return err
					}
					require.NoError(t, os.Rename(backup, backup+".saved"))
					require.NoError(t, os.Symlink(filepath.Base(backup), backup))
					_, err := os.Stat(backup)
					require.Error(t, err)
					require.False(t, os.IsNotExist(err))
					hookCalled = true
					return nil
				}
			case "cache restore error":
				gcRestoreCache = func(string, string) error { return errors.New("synthetic cache write failure") }
			case "swap signal error":
				gcRestoreCache = func(backup, dst string) error {
					err := restoreCache(backup, dst)
					require.NoError(t, os.Mkdir(clone+".gc-swap-lock", 0755))
					return err
				}
			case "backup cleanup error":
				scheduler.gcSwapWindowTestHook = func() {
					// Refuse removal only after restore has read the backup.
					require.NoError(t, os.Chmod(clone+".gc-cache", 0500))
				}
				t.Cleanup(func() { _ = os.Chmod(clone+".gc-cache", 0700) })
			}
			ws := WorkspaceState{ID: "ledger", Type: WorkspaceTypeLedger, Path: clone, CloneURL: "file://" + bare, Exists: true}
			require.Equal(t, wantResult, scheduler.runBlueGreenGC(ctx, ws))
			if phase == "swap deadline" || phase == "untracked capture error" || phase == "cache inspection error" {
				require.True(t, hookCalled, "the test must reach the injected failure")
			}
			if phase == "swap deadline" {
				require.ErrorIs(t, ctx.Err(), context.DeadlineExceeded)
			}
			current, err := os.Stat(filepath.Join(clone, ".git"))
			require.NoError(t, err)
			require.True(t, os.SameFile(original, current), "failed preparation must not replace the original checkout")
			require.NoDirExists(t, clone+".old")
			require.NoDirExists(t, clone+".new")
			for path, content := range files {
				got, err := os.ReadFile(filepath.Join(clone, path))
				require.NoError(t, err)
				require.Equal(t, content, string(got), path)
			}
			if phase == "cache inspection error" {
				require.NoError(t, os.Remove(clone+".gc-cache"), "failed inspection must retain the backup path")
				require.NoError(t, os.Rename(clone+".gc-cache.saved", clone+".gc-cache"))
			}
			if phase == "backup cleanup error" {
				require.NoError(t, os.Chmod(clone+".gc-cache", 0700))
			}
			if phase == "cache inspection error" || phase == "backup cleanup error" {
				got, err := os.ReadFile(filepath.Join(clone+".gc-cache", "recording.txt"))
				require.NoError(t, err, "failed retirement must retain the backup")
				require.Equal(t, "cached recording", string(got))
			}
			gcRestoreCache = restoreCache
			gcPreserveCache = preserveCache
			copyFile = originalCopy
			scheduler.gcSwapWindowTestHook = nil
			require.Equal(t, gcSuccess, scheduler.runBlueGreenGC(context.Background(), ws))
			for path, content := range files {
				got, err := os.ReadFile(filepath.Join(clone, path))
				require.NoError(t, err)
				require.Equal(t, content, string(got), "retry must preserve %s", path)
			}
		})
	}
}

// GC must wait for a writer before capturing state and exclude later writers
// through its swap, using the same cross-process lock as publication.
func TestGC_HoldsRepoLockFromCaptureThroughSwap(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real Ledger clones")
	}
	bare, clone, scheduler := setupClonedLedger(t)
	ws := WorkspaceState{ID: "ledger", Type: WorkspaceTypeLedger, Path: clone, CloneURL: "file://" + bare, Exists: true}
	require.NoError(t, gitutil.WithRepoLock(context.Background(), clone, func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		require.Equal(t, gcSkippedLocked, scheduler.runBlueGreenGC(ctx, ws))
		require.NoDirExists(t, clone+".new")
		return nil
	}))
	hookCalled := false
	scheduler.gcSwapWindowTestHook = func() {
		hookCalled = true
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		err := gitutil.WithRepoLock(ctx, clone, func() error {
			t.Error("a writer entered while GC was swapping the captured state")
			return nil
		})
		require.True(t, gitutil.IsRepoLockBusy(err), "GC must retain the publication lock through swap")
	}
	require.Equal(t, gcSuccess, scheduler.runBlueGreenGC(context.Background(), ws))
	require.True(t, hookCalled)
}

func TestGC_BusyLedgerSkipsCanceledRequest(t *testing.T) {
	scheduler := gcTestScheduler(t)
	ws := WorkspaceState{ID: "ledger", Type: WorkspaceTypeLedger, Path: filepath.Join(t.TempDir(), "ledger")}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	scheduler.ledgerMu.Lock()
	done := make(chan gcResult, 1)
	go func() { done <- scheduler.runBlueGreenGC(ctx, ws) }()
	select {
	case result := <-done:
		scheduler.ledgerMu.Unlock()
		require.Equal(t, gcSkippedLocked, result)
	case <-time.After(5 * time.Second):
		scheduler.ledgerMu.Unlock()
		<-done
		t.Fatal("optional GC waited for a busy Ledger after its caller canceled")
	}
	require.NoDirExists(t, ws.Path+".new")
}

// GC must apply its own execution budget even with a background caller. The
// same deadline covers clone negotiation and a later sparse-checkout fetch.
func TestGC_CloneBudgetPreservesOriginal(t *testing.T) {
	if testing.Short() {
		t.Skip("short: stalled Git HTTP clones")
	}
	for _, phase := range []string{"clone", "sparse checkout"} {
		t.Run(phase, func(t *testing.T) {
			_, clone, scheduler := setupClonedLedger(t)
			local := filepath.Join(clone, "sessions", "local.txt")
			require.NoError(t, os.WriteFile(local, []byte("only local copy"), 0600))
			original, err := os.Stat(filepath.Join(clone, ".git"))
			require.NoError(t, err)
			stallAfter := 1
			if phase == "sparse checkout" {
				stallAfter = 64 << 10
			}
			server := servePacedLedger(t, 128<<10, 256<<10, stallAfter)
			setCloneBudgets(t, teamContextCloneTimeout, 2*time.Second)
			ws := WorkspaceState{ID: "ledger", Type: WorkspaceTypeLedger, Path: clone, CloneURL: server.URL, Exists: true}
			started := time.Now()
			require.Equal(t, gcFailed, scheduler.runBlueGreenGC(context.Background(), ws))
			require.Less(t, time.Since(started), 10*time.Second, "the execution budget must stop stalled network work")
			if phase == "sparse checkout" {
				select {
				case <-server.streaming:
				default:
					t.Fatal("the clone never reached the lazy blob download")
				}
			}
			current, err := os.Stat(filepath.Join(clone, ".git"))
			require.NoError(t, err)
			require.True(t, os.SameFile(original, current))
			got, err := os.ReadFile(local)
			require.NoError(t, err)
			require.Equal(t, "only local copy", string(got))
			require.NoDirExists(t, clone+".new")
			require.NoDirExists(t, clone+".old")
		})
	}
}

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
	hasDiff, err := s.gcCaptureDiff(ctx, dir, diffFile, "HEAD")
	require.NoError(t, err)
	assert.False(t, hasDiff, "no changes should produce no diff")
	_, statErr := os.Stat(diffFile)
	assert.True(t, os.IsNotExist(statErr), "diff file should not be created when there are no changes")

	// make an uncommitted modification
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("modified"), 0644))

	hasDiff, err = s.gcCaptureDiff(ctx, dir, diffFile, "HEAD")
	require.NoError(t, err)
	assert.True(t, hasDiff, "uncommitted changes should produce a diff")

	data, err := os.ReadFile(diffFile)
	require.NoError(t, err)
	assert.Contains(t, string(data), "modified", "diff should contain the modification")
}

func TestGC_CaptureDiffTooLargeStopsProducer(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real oversized Git diff")
	}
	isolateCredentials(t)
	scheduler := gcTestScheduler(t)
	repo := t.TempDir()
	gcInitGitRepo(t, repo)
	// Leave more than a pipe's capacity unread after the capture limit.
	content := strings.Repeat("synthetic line\n", maxGCDiffSize/len("synthetic line\n")+100000)
	require.NoError(t, os.WriteFile(filepath.Join(repo, "README.md"), []byte(content), 0600))
	diffFile := filepath.Join(t.TempDir(), "local.patch")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	hasDiff, err := scheduler.gcCaptureDiff(ctx, repo, diffFile, "HEAD")
	require.ErrorContains(t, err, "diff too large")
	require.NoError(t, ctx.Err(), "capture must stop the producer itself, before the generous test deadline")
	require.False(t, hasDiff)
	require.NoFileExists(t, diffFile)
}

func TestGC_CaptureDiffFailuresPreserveSourceAndRetry(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real Git diff capture")
	}
	for _, failure := range []string{"output is a directory", "invalid base"} {
		t.Run(failure, func(t *testing.T) {
			isolateCredentials(t)
			scheduler := gcTestScheduler(t)
			repo := t.TempDir()
			gcInitGitRepo(t, repo)
			// More than a pipe's capacity makes a missing cancellation hang at Wait.
			content := strings.Repeat("local change\n", 1<<16)
			source := filepath.Join(repo, "README.md")
			require.NoError(t, os.WriteFile(source, []byte(content), 0600))
			diffFile := filepath.Join(t.TempDir(), "local.patch")
			base, wantError := "HEAD", "create diff file"
			if failure == "output is a directory" {
				require.NoError(t, os.Mkdir(diffFile, 0755))
			} else {
				base, wantError = "missing-base", "git diff HEAD"
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			hasDiff, err := scheduler.gcCaptureDiff(ctx, repo, diffFile, base)
			require.ErrorContains(t, err, wantError)
			require.False(t, hasDiff)
			require.NoError(t, ctx.Err(), "capture must stop its child before the test deadline")
			got, err := os.ReadFile(source)
			require.NoError(t, err)
			require.Equal(t, content, string(got), "a failed capture must not alter its source")
			if failure == "output is a directory" {
				require.NoError(t, os.Remove(diffFile))
			} else {
				require.NoFileExists(t, diffFile, "a failed Git command must not leave a partial patch")
			}
			hasDiff, err = scheduler.gcCaptureDiff(context.Background(), repo, diffFile, "HEAD")
			require.NoError(t, err)
			require.True(t, hasDiff)
			require.NoError(t, exec.Command("git", "-C", repo, "checkout", "--", "README.md").Run())
			require.NoError(t, scheduler.gcRestoreDiff(context.Background(), repo, diffFile))
			got, err = os.ReadFile(source)
			require.NoError(t, err)
			require.Equal(t, content, string(got), "the retried capture must restore every local byte")
		})
	}
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

	unusualName := " spaces and\nnewline "
	if runtime.GOOS == "windows" {
		unusualName = " spaces and unicode α" // Windows disallows newline and trailing spaces.
	}
	files := map[string]string{
		"new.txt":        "new content",
		"sub/nested.txt": "nested",
		unusualName:      "unquoted path",
		"..notes/...cfg": "dot-prefixed directory",
		"...cfg":         "dot-prefixed file",
	}
	for name, content := range files {
		path := filepath.Join(dir, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0644))
	}

	has, err = s.gcCaptureUntracked(ctx, dir, destDir)
	require.NoError(t, err)
	assert.True(t, has)

	for name, content := range files {
		data, err := os.ReadFile(filepath.Join(destDir, name))
		require.NoError(t, err)
		assert.Equal(t, content, string(data), name)
	}
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
	hasDiff, err := s.gcCaptureDiff(ctx, srcDir, diffFile, "HEAD")
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
		TeamID:   "team-test",
		TeamName: "test-team",
		Path:     cloneDir,
		CloneURL: cloneURL,
		Exists:   true,
	}
	dbPath := filepath.Join(cloneDir, ".sageox", "cache", "whisper", "whisper.db")
	store, err := whisperstore.Open(dbPath)
	require.NoError(t, err)
	registry := NewWhisperRegistry(nil, nil)
	registry.AddTeamStore(ws.TeamID, store)
	t.Cleanup(func() { registry.Close() })
	s.whisperRegistry = registry
	entry := whisperstore.WhisperEntry{
		ID: "before-gc", Scope: "team", Type: whisperstore.WhisperTrigger,
		Source: "test", Topic: "gc", Content: "retained team whisper",
		Importance: whisperstore.ImportanceNormal, CreatedAt: time.Now(),
	}
	require.NoError(t, store.Add(entry))

	result := s.runBlueGreenGC(ctx, ws)
	require.Equal(t, gcSuccess, result, "full cycle should succeed")
	// A live handle would keep the renamed-away SQLite files mapped.
	_, err = store.GetAllWhispers("")
	require.ErrorContains(t, err, "database is closed")
	require.False(t, registry.HasTeamStore(ws.TeamID), "the next sync must reopen the team's store")
	reopened, err := whisperstore.Open(dbPath)
	require.NoError(t, err)
	registry.AddTeamStore(ws.TeamID, reopened)
	entries, err := registry.GetAllWhispers("")
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, entry.Content, entries[0].Content, "the replacement must retain the team whisper")
	entry.ID = "after-gc"
	require.NoError(t, reopened.Add(entry))
	entries, err = registry.GetAllWhispers("")
	require.NoError(t, err)
	require.Len(t, entries, 2, "the reopened replacement must accept new whispers")

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

// A reclone must preserve retries from both .git directories (#1356).
func TestGC_CarriesPlanRetriesIntoTheNewClone(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	isolateCredentials(t)

	tmp := t.TempDir()
	bareDir := filepath.Join(tmp, "ledger.bare")
	seedDir := filepath.Join(tmp, "seed")
	require.NoError(t, exec.Command("git", "init", "--bare", "--initial-branch=main", bareDir).Run())
	require.NoError(t, exec.Command("git", "clone", bareDir, seedDir).Run())
	gitConfig(t, seedDir)
	require.NoError(t, os.MkdirAll(filepath.Join(seedDir, "sessions"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(seedDir, "sessions", ".gitkeep"), []byte(""), 0644))
	require.NoError(t, exec.Command("git", "-C", seedDir, "add", ".").Run())
	require.NoError(t, exec.Command("git", "-C", seedDir, "commit", "-m", "initial").Run())
	require.NoError(t, exec.Command("git", "-C", seedDir, "push", "origin", "HEAD:main").Run())

	cloneDir := filepath.Join(tmp, "ledger")
	require.NoError(t, exec.Command("git", "clone", "file://"+bareDir, cloneDir).Run())
	gitConfig(t, cloneDir)
	pushDir := filepath.Join(".git", "ox-plan-push-pending")
	reviewDir := filepath.Join(".git", "ox-plan-review-pending")
	for _, dir := range []string{pushDir, reviewDir} {
		require.NoError(t, os.MkdirAll(filepath.Join(cloneDir, dir), 0755))
		require.NoError(t, os.WriteFile(filepath.Join(cloneDir, dir, "old.json"), []byte("old"), 0644))
	}

	s := gcTestScheduler(t)
	// stands in for a review retry the CLI writes into the new clone between
	// the swap and the carry: the hook runs just before the swap, so it writes
	// into the clone about to be swapped in
	s.gcSwapWindowTestHook = func() {
		dir := filepath.Join(cloneDir+".new", reviewDir)
		require.NoError(t, os.MkdirAll(dir, 0755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "newer.json"), []byte("newer"), 0644))
	}
	ws := WorkspaceState{
		ID:       "carry-test",
		Type:     WorkspaceTypeLedger,
		Path:     cloneDir,
		CloneURL: "file://" + bareDir,
		Exists:   true,
	}
	require.Equal(t, gcSuccess, s.runBlueGreenGC(context.Background(), ws), "both clones' retries must survive")

	data, err := os.ReadFile(filepath.Join(cloneDir, pushDir, "old.json"))
	require.NoError(t, err, "a plan retry must survive the reclone")
	assert.Equal(t, "old", string(data))
	data, err = os.ReadFile(filepath.Join(cloneDir, reviewDir, "newer.json"))
	require.NoError(t, err, "the carry must not replace a retry the CLI wrote into the new clone")
	assert.Equal(t, "newer", string(data))
	data, err = os.ReadFile(filepath.Join(cloneDir, reviewDir, "old.json"))
	require.NoError(t, err, "a populated destination must also retain the old clone's retry")
	assert.Equal(t, "old", string(data))
}

func TestGC_PlanRetriesRetainedWhenCarryIsRefused(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	for _, obstruction := range []string{"destination is a file", "conflicting entry"} {
		t.Run(obstruction, func(t *testing.T) {
			conflict := obstruction == "conflicting entry"
			isolateCredentials(t)
			bareDir, cloneDir := gcInitBareAndClone(t, t.TempDir())
			require.NoError(t, os.MkdirAll(filepath.Join(cloneDir, "sessions"), 0755))
			require.NoError(t, os.WriteFile(filepath.Join(cloneDir, "sessions", ".gitkeep"), nil, 0644))
			require.NoError(t, exec.Command("git", "-C", cloneDir, "add", "sessions").Run())
			require.NoError(t, exec.Command("git", "-C", cloneDir, "commit", "-m", "ledger structure").Run())
			require.NoError(t, exec.Command("git", "-C", cloneDir, "push").Run())
			localFiles := map[string]string{
				"README.md":                  "unpublished edit",
				"data/plans/unsaved.txt":     "untracked plan",
				".sageox/cache/gc-state.txt": "local cache",
			}
			for path, contents := range localFiles {
				fullPath := filepath.Join(cloneDir, path)
				require.NoError(t, os.MkdirAll(filepath.Dir(fullPath), 0755))
				require.NoError(t, os.WriteFile(fullPath, []byte(contents), 0644))
			}
			pushDir := filepath.Join(".git", "ox-plan-push-pending")
			reviewDir := filepath.Join(".git", "ox-plan-review-pending")
			for _, dir := range []string{pushDir, reviewDir} {
				require.NoError(t, os.MkdirAll(filepath.Join(cloneDir, dir), 0755))
				require.NoError(t, os.WriteFile(filepath.Join(cloneDir, dir, "old.json"), []byte("original retry"), 0644))
			}
			copied := []string{filepath.Join(pushDir, "old.json")}
			if conflict {
				copied = append(copied, filepath.Join(reviewDir, "a-copied.json"))
				require.NoError(t, os.WriteFile(filepath.Join(cloneDir, reviewDir, "a-copied.json"), []byte("copied retry"), 0644))
			}
			s := gcTestScheduler(t)
			s.gcSwapWindowTestHook = func() {
				blocked := filepath.Join(cloneDir+".new", reviewDir)
				if conflict {
					require.NoError(t, os.MkdirAll(blocked, 0755))
					blocked = filepath.Join(blocked, "old.json")
				}
				require.NoError(t, os.WriteFile(blocked, []byte("newer retry"), 0644))
			}
			ws := WorkspaceState{ID: "carry-refused", Type: WorkspaceTypeLedger, Path: cloneDir, CloneURL: "file://" + bareDir, Exists: true}
			require.Equal(t, gcFailed, s.runBlueGreenGC(context.Background(), ws), "a refused carry must retain the old clone")
			for _, path := range copied {
				assert.NoFileExists(t, filepath.Join(cloneDir+".old", path), "a durable copy must retire its source")
			}
			data, err := os.ReadFile(filepath.Join(cloneDir+".old", reviewDir, "old.json"))
			require.NoError(t, err)
			require.Equal(t, "original retry", string(data), "the failed carry must keep its source")
			for path, contents := range localFiles {
				data, err := os.ReadFile(filepath.Join(cloneDir, path))
				assert.NoError(t, err, path)
				assert.Equal(t, contents, string(data), "a retry conflict must not strand local state: %s", path)
			}
			blocked := filepath.Join(cloneDir, reviewDir)
			if conflict {
				blocked = filepath.Join(blocked, "old.json")
			}
			data, err = os.ReadFile(blocked)
			require.NoError(t, err)
			require.Equal(t, "newer retry", string(data), "never replace destination bytes")
			require.NoFileExists(t, cloneDir+".gc-swap-lock", "a failed carry must release the CLI swap signal")

			// Simulate a crash before local-state restoration. A retry conflict
			// must not block restoring those backups on the next invocation.
			_, err = s.gcCaptureDiff(context.Background(), cloneDir+".old", cloneDir+".gc-diff", "HEAD")
			require.NoError(t, err)
			_, err = s.gcCaptureUntracked(context.Background(), cloneDir+".old", cloneDir+".gc-untracked")
			require.NoError(t, err)
			require.NoError(t, gcPreserveCache(cloneDir+".old", cloneDir+".gc-cache"))
			require.NoError(t, exec.Command("git", "-C", cloneDir, "checkout", "--", "README.md").Run())
			require.NoError(t, os.RemoveAll(filepath.Join(cloneDir, "data/plans/unsaved.txt")))
			require.NoError(t, os.RemoveAll(filepath.Join(cloneDir, ".sageox/cache")))
			require.NoError(t, os.WriteFile(cloneDir+".gc-swap-done", nil, 0600))

			// The CLI acknowledged the copied push retry before the restart.
			for _, path := range copied {
				require.NoError(t, os.Remove(filepath.Join(cloneDir, path)))
			}
			s = gcTestScheduler(t)
			require.Equal(t, gcFailed, s.runBlueGreenGC(context.Background(), ws))
			require.NoDirExists(t, cloneDir+".new")
			require.FileExists(t, filepath.Join(cloneDir+".old", reviewDir, "old.json"))
			for _, path := range copied {
				assert.NoFileExists(t, filepath.Join(cloneDir, path), "an acknowledged retry must not return")
			}
			for path, contents := range localFiles {
				data, err := os.ReadFile(filepath.Join(cloneDir, path))
				assert.NoError(t, err, path)
				assert.Equal(t, contents, string(data), "startup must restore local state despite the retry conflict: %s", path)
			}
			require.NoError(t, os.Remove(blocked))
			require.Equal(t, gcSuccess, s.runBlueGreenGC(context.Background(), ws))
			data, err = os.ReadFile(filepath.Join(cloneDir, reviewDir, "old.json"))
			require.NoError(t, err)
			require.Equal(t, "original retry", string(data))
			for _, path := range copied {
				require.NoFileExists(t, filepath.Join(cloneDir, path))
			}
			require.NoDirExists(t, cloneDir+".old")
		})
	}
}

func TestGC_CarryPlanRetriesRejectsNonRegularEntries(t *testing.T) {
	for _, tc := range []struct{ target, kind string }{
		{"source", "directory"}, {"source", "symlink"},
		{"destination", "directory"}, {"destination", "symlink"},
	} {
		t.Run(tc.target+" "+tc.kind, func(t *testing.T) {
			if tc.kind == "symlink" && runtime.GOOS == "windows" {
				t.Skip("creating symlinks requires elevated privileges on Windows")
			}
			oldPath, newPath := t.TempDir(), t.TempDir()
			src := filepath.Join(oldPath, ledger.PlanPushPendingDir, "retry.json")
			dst := filepath.Join(newPath, ledger.PlanPushPendingDir, "retry.json")
			for _, path := range []string{src, dst} {
				require.NoError(t, os.MkdirAll(filepath.Dir(path), 0755))
				require.NoError(t, os.WriteFile(path, []byte("retry"), 0644))
			}
			bad, other := src, dst
			if tc.target == "destination" {
				bad, other = dst, src
			}
			require.NoError(t, os.Remove(bad))
			if tc.kind == "directory" {
				require.NoError(t, os.Mkdir(bad, 0755))
			} else {
				require.NoError(t, os.Symlink(other, bad))
			}
			before, err := os.Lstat(bad)
			require.NoError(t, err)
			require.ErrorContains(t, gcCarryPlanRetries(oldPath, newPath), "not a regular file")
			after, err := os.Lstat(bad)
			require.NoError(t, err)
			require.True(t, os.SameFile(before, after), "refused entries must be retained")
			data, err := os.ReadFile(other)
			require.NoError(t, err)
			require.Equal(t, "retry", string(data))
		})
	}
}

func TestGC_CarryPlanRetriesRecoversFromPermissionFailures(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("requires Unix file permissions without root privileges")
	}
	for _, tc := range []struct {
		name, path   string
		mode         os.FileMode
		existingDest bool
	}{
		{"unreadable inventory", "source directory", 0111, false},
		{"refused hard link", "destination directory", 0555, false},
		{"unreadable source", "source", 0000, true},
		{"unreadable destination", "destination", 0000, true},
		{"unsyncable clone directory", "clone", 0300, false},
		{"refused source retirement", "source directory", 0555, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			oldPath, newPath := t.TempDir(), t.TempDir()
			src := filepath.Join(oldPath, ledger.PlanPushPendingDir, "retry.json")
			dst := filepath.Join(newPath, ledger.PlanPushPendingDir, "retry.json")
			require.NoError(t, os.MkdirAll(filepath.Dir(src), 0755))
			require.NoError(t, os.MkdirAll(filepath.Dir(dst), 0755))
			require.NoError(t, os.WriteFile(src, []byte("original retry"), 0644))
			if tc.existingDest {
				require.NoError(t, os.WriteFile(dst, []byte("existing retry"), 0644))
			}
			path := map[string]string{
				"source": src, "destination": dst,
				"source directory": filepath.Dir(src), "destination directory": filepath.Dir(dst),
				"clone": newPath,
			}[tc.path]
			info, err := os.Stat(path)
			require.NoError(t, err)
			require.NoError(t, os.Chmod(path, tc.mode))
			t.Cleanup(func() { _ = os.Chmod(path, info.Mode().Perm()) })
			require.Error(t, gcCarryPlanRetries(oldPath, newPath))
			require.NoError(t, os.Chmod(path, info.Mode().Perm()))
			data, err := os.ReadFile(src)
			require.NoError(t, err, "a failed carry must retain its source")
			require.Equal(t, "original retry", string(data))
			if tc.existingDest {
				data, err = os.ReadFile(dst)
				require.NoError(t, err)
				require.Equal(t, "existing retry", string(data), "never replace destination bytes")
				require.NoError(t, os.Remove(dst))
			}
			require.NoError(t, gcCarryPlanRetries(oldPath, newPath))
			require.NoFileExists(t, src, "a successful retry retires its source")
			data, err = os.ReadFile(dst)
			require.NoError(t, err)
			require.Equal(t, "original retry", string(data))
		})
	}
}

func TestGC_PlanRetriesSurviveInterruptedSwap(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	for _, phase := range []string{"before rename", "between renames", "between renames scheduled", "after swap scheduled", "before marker", "before carry", "partial carry", "equivalent carry"} {
		t.Run(phase, func(t *testing.T) {
			isolateCredentials(t)
			bareDir, cloneDir := gcInitBareAndClone(t, t.TempDir())
			require.NoError(t, os.MkdirAll(filepath.Join(cloneDir, "sessions"), 0755))
			require.NoError(t, os.WriteFile(filepath.Join(cloneDir, "sessions", ".gitkeep"), nil, 0644))
			require.NoError(t, exec.Command("git", "-C", cloneDir, "add", "sessions").Run())
			require.NoError(t, exec.Command("git", "-C", cloneDir, "commit", "-m", "ledger structure").Run())
			require.NoError(t, exec.Command("git", "-C", cloneDir, "push").Run())
			pushDir := filepath.Join(".git", "ox-plan-push-pending")
			reviewDir := filepath.Join(".git", "ox-plan-review-pending")
			for _, dir := range []string{pushDir, reviewDir} {
				require.NoError(t, os.MkdirAll(filepath.Join(cloneDir, dir), 0755))
				require.NoError(t, os.WriteFile(filepath.Join(cloneDir, dir, "old.json"), []byte("original retry"), 0644))
			}
			require.NoError(t, os.WriteFile(filepath.Join(cloneDir, "README.md"), []byte("unpublished edit"), 0644))
			s := gcTestScheduler(t)
			hasDiff, err := s.gcCaptureDiff(context.Background(), cloneDir, cloneDir+".gc-diff", "HEAD")
			require.NoError(t, err)
			require.True(t, hasDiff)
			require.NoError(t, exec.Command("git", "clone", "file://"+bareDir, cloneDir+".new").Run())
			require.NoError(t, os.WriteFile(cloneDir+".gc-swap-lock", nil, 0600))
			if phase != "before rename" {
				require.NoError(t, os.Rename(cloneDir, cloneDir+".old"))
				if !strings.HasPrefix(phase, "between renames") {
					require.NoError(t, os.Rename(cloneDir+".new", cloneDir))
				}
			}
			if phase == "before carry" || phase == "partial carry" || phase == "equivalent carry" {
				require.NoError(t, os.WriteFile(cloneDir+".gc-swap-done", nil, 0600))
			}
			if phase == "partial carry" || phase == "equivalent carry" {
				require.NoError(t, os.MkdirAll(filepath.Join(cloneDir, pushDir), 0755))
				if phase == "partial carry" {
					require.NoError(t, os.Link(filepath.Join(cloneDir+".old", pushDir, "old.json"), filepath.Join(cloneDir, pushDir, "old.json")))
				} else {
					require.NoError(t, os.WriteFile(filepath.Join(cloneDir, pushDir, "old.json"), []byte("original retry"), 0644))
				}
			}
			ws := WorkspaceState{ID: "interrupted-carry", Type: WorkspaceTypeLedger, Path: cloneDir, CloneURL: "file://" + bareDir, Exists: true}
			if strings.HasPrefix(phase, "between renames") || strings.HasSuffix(phase, "scheduled") {
				ws.ID, ws.Exists = "ledger", !strings.HasPrefix(phase, "between renames")
				if phase == "after swap scheduled" {
					ws.LastGCTime = time.Now()
					require.NoError(t, exec.Command("git", "-C", cloneDir, "config", "remote.origin.promisor", "true").Run())
				}
				registry := s.WorkspaceRegistry()
				registry.mu.Lock()
				primeConfigCacheLocked(registry)
				registry.ledger = &ws
				registry.workspaces[ws.ID] = &ws
				registry.mu.Unlock()
				if strings.HasSuffix(phase, "scheduled") {
					s.checkAndRunGC(context.Background())
				} else {
					response := s.TriggerGC(context.Background())
					require.True(t, response.LedgerTriggered)
					require.Empty(t, response.Errors)
				}
				require.True(t, registry.GetLedger().Exists, "restoring the clone must resume ordinary sync")
			} else {
				require.Equal(t, gcSuccess, s.runBlueGreenGC(context.Background(), ws))
			}
			for _, dir := range []string{pushDir, reviewDir} {
				data, err := os.ReadFile(filepath.Join(cloneDir, dir, "old.json"))
				require.NoError(t, err, "the only retry copy must survive every swap boundary")
				require.Equal(t, "original retry", string(data))
			}
			data, err := os.ReadFile(filepath.Join(cloneDir, "README.md"))
			require.NoError(t, err)
			require.Equal(t, "unpublished edit", string(data))
			for _, suffix := range []string{".old", ".new", ".gc-swap-lock", ".gc-swap-done", ".gc-diff"} {
				require.NoFileExists(t, cloneDir+suffix)
				require.NoDirExists(t, cloneDir+suffix)
			}
		})
	}
}

// A recovered .old must not make a later attempt replay stale preparation
// backups over edits or deletions made after that preparation failed.
func TestGC_RecoveredSwapDoesNotReplayFailedPreparation(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real Ledger clones")
	}
	bare, clone, scheduler := setupClonedLedger(t)
	local := filepath.Join(clone, "sessions", "discarded.txt")
	cache := filepath.Join(clone, ".sageox", "cache", "recording.txt")
	retry := filepath.Join(ledger.PlanReviewPendingDir, "only.json")
	// Model a completed swap whose cleanup left part of the old clone.
	for path, content := range map[string]string{
		local:                              "before failed preparation",
		cache:                              "before failed preparation",
		filepath.Join(clone+".old", retry): "pending review",
	} {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0600))
	}
	require.NoError(t, os.WriteFile(clone+".gc-swap-done", nil, 0600))
	ws := WorkspaceState{
		ID: "ledger", Type: WorkspaceTypeLedger, Path: clone, Exists: true,
		CloneURL: "file://" + filepath.Join(t.TempDir(), "missing.git"),
	}
	require.Equal(t, gcFailed, scheduler.runBlueGreenGC(context.Background(), ws))
	for _, backup := range []string{
		filepath.Join(clone+".gc-untracked", "sessions", "discarded.txt"),
		filepath.Join(clone+".gc-cache", "recording.txt"),
	} {
		data, err := os.ReadFile(backup)
		require.NoError(t, err, "the failure must happen after capture")
		require.Equal(t, "before failed preparation", string(data))
	}
	require.NoError(t, os.WriteFile(cache, []byte("newer recording"), 0600))
	require.NoError(t, os.Remove(local))
	tracked := filepath.Join(clone, "sessions", ".gitkeep")
	require.NoError(t, os.WriteFile(tracked, []byte("newer tracked edit"), 0600))
	ws.CloneURL = "file://" + bare
	require.Equal(t, gcSuccess, scheduler.runBlueGreenGC(context.Background(), ws))
	for path, content := range map[string]string{
		cache:                       "newer recording",
		tracked:                     "newer tracked edit",
		filepath.Join(clone, retry): "pending review",
	} {
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Equal(t, content, string(data), path)
	}
	assert.NoFileExists(t, local, "a deleted local file must not return from a stale backup")
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

func TestCheckAndRunGC_TeamInterruptedSwapOutranksRecentGC(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real team context clones")
	}
	for _, state := range []string{"healthy clone", "completed swap", "missing canonical clone"} {
		t.Run(state, func(t *testing.T) {
			bare, clone, scheduler := setupClonedTeamContext(t, "version 1\ninclude .sageox/\ninclude SOUL.md\n", nil)
			cache := filepath.Join(clone, ".sageox", "cache", "sync-state.json")
			require.NoError(t, os.MkdirAll(filepath.Dir(cache), 0755))
			require.NoError(t, os.WriteFile(cache, []byte("local sync state"), 0644))
			if state != "healthy clone" {
				require.NoError(t, gcPreserveCache(clone, clone+".gc-cache"))
				require.NoError(t, os.Rename(clone, clone+".old"))
				if state == "completed swap" {
					require.NoError(t, exec.Command("git", "clone", "file://"+bare, clone).Run())
				}
			}
			if state != "missing canonical clone" {
				require.NoError(t, exec.Command("git", "-C", clone, "config", "remote.origin.promisor", "true").Run())
			}
			lastGC := time.Now()
			ws := WorkspaceState{ID: "team-recovery", Type: WorkspaceTypeTeamContext, Path: clone,
				CloneURL: "file://" + bare, Exists: state != "missing canonical clone", LastGCTime: lastGC}
			registry := scheduler.WorkspaceRegistry()
			registry.mu.Lock()
			primeConfigCacheLocked(registry)
			registry.ledger = nil
			registry.workspaces = map[string]*WorkspaceState{ws.ID: &ws}
			registry.mu.Unlock()

			scheduler.checkAndRunGC(context.Background())

			data, err := os.ReadFile(cache)
			require.NoError(t, err)
			require.Equal(t, "local sync state", string(data))
			require.NoDirExists(t, clone+".old")
			require.NoDirExists(t, clone+".new")
			if state == "healthy clone" {
				require.Equal(t, lastGC, registry.GetLastGCTime(ws.ID), "a healthy recent partial clone does not need GC")
			} else {
				require.True(t, registry.GetLastGCTime(ws.ID).After(lastGC), "an interrupted swap must be recovered immediately")
				require.True(t, registry.GetWorkspace(ws.ID).Exists)
			}
		})
	}
}

func TestGC_InterruptedSwapRefusalPreservesBackups(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX symlink failure modes")
	}
	for _, obstruction := range []string{
		"swap lock directory", "dangling canonical symlink", "canonical symlink loop", "swap marker directory", "old clone symlink loop",
		"swap marker symlink loop", "cache backup symlink loop", "diff backup symlink loop", "untracked backup symlink loop",
	} {
		t.Run(obstruction, func(t *testing.T) {
			clone := filepath.Join(t.TempDir(), "clone")
			old := clone + ".old"
			require.NoError(t, os.MkdirAll(clone, 0755))
			retained := filepath.Join(old, ".git", "ox-plan-review-pending", "only.json")
			if obstruction == "old clone symlink loop" || obstruction == "swap marker symlink loop" {
				retained = filepath.Join(clone, ".git", "ox-plan-review-pending", "only.json")
			}
			require.NoError(t, os.MkdirAll(filepath.Dir(retained), 0755))
			require.NoError(t, os.WriteFile(retained, []byte("only retry copy"), 0644))
			for _, suffix := range []string{".gc-cache", ".gc-untracked"} {
				require.NoError(t, os.MkdirAll(clone+suffix, 0755))
				require.NoError(t, os.WriteFile(filepath.Join(clone+suffix, "only.txt"), []byte("only backup"), 0644))
			}
			loopPath := ""
			switch obstruction {
			case "swap lock directory":
				require.NoError(t, os.Mkdir(clone+".gc-swap-lock", 0755))
			case "dangling canonical symlink", "canonical symlink loop":
				require.NoError(t, os.Remove(clone))
				target := filepath.Join(filepath.Dir(clone), "missing")
				if obstruction == "canonical symlink loop" {
					target = clone
				}
				require.NoError(t, os.Symlink(target, clone))
			case "swap marker directory":
				require.NoError(t, os.Mkdir(clone+".gc-swap-done", 0755))
			case "old clone symlink loop":
				require.NoError(t, os.Symlink(old, old))
			case "swap marker symlink loop":
				loopPath = clone + ".gc-swap-done"
				require.NoError(t, os.WriteFile(loopPath, nil, 0644))
			case "cache backup symlink loop":
				loopPath = clone + ".gc-cache"
			case "diff backup symlink loop":
				loopPath = clone + ".gc-diff"
				require.NoError(t, os.WriteFile(loopPath, []byte("unrestored diff"), 0644))
			case "untracked backup symlink loop":
				loopPath = clone + ".gc-untracked"
			}
			if loopPath != "" {
				require.NoError(t, os.Rename(loopPath, loopPath+".saved"))
				require.NoError(t, os.Symlink(filepath.Base(loopPath), loopPath))
				_, err := os.Stat(loopPath)
				require.Error(t, err)
				require.False(t, os.IsNotExist(err))
			}
			scheduler := gcTestScheduler(t)
			ws := WorkspaceState{ID: "blocked-recovery", Type: WorkspaceTypeLedger, Path: clone}
			require.Equal(t, gcFailed, scheduler.runBlueGreenGC(context.Background(), ws))
			if loopPath != "" && obstruction != "swap marker symlink loop" {
				require.DirExists(t, old, "the old clone must survive failed recovery")
				retained = filepath.Join(clone, ledger.PlanReviewPendingDir, "only.json")
			}
			data, err := os.ReadFile(retained)
			require.NoError(t, err)
			require.Equal(t, "only retry copy", string(data))
			if loopPath != "" {
				_, err := os.Lstat(clone + ".gc-swap-done")
				require.NoError(t, err, "failed inspection must retain the recovery marker")
			}
			for _, suffix := range []string{".gc-cache", ".gc-untracked"} {
				backup := clone + suffix
				if backup == loopPath {
					backup += ".saved"
				}
				if suffix == ".gc-cache" && (obstruction == "diff backup symlink loop" || obstruction == "untracked backup symlink loop") {
					backup = filepath.Join(clone, ".sageox", "cache")
				}
				data, err := os.ReadFile(filepath.Join(backup, "only.txt"))
				require.NoError(t, err)
				require.Equal(t, "only backup", string(data), "failed recovery must retain captured content")
			}
			if obstruction == "diff backup symlink loop" {
				data, err := os.ReadFile(loopPath + ".saved")
				require.NoError(t, err)
				require.Equal(t, "unrestored diff", string(data))
			}
			require.NoDirExists(t, clone+".new")
			require.True(t, scheduler.ledgerMu.TryLock(), "failed recovery must release the ledger lock")
			scheduler.ledgerMu.Unlock()
		})
	}
}

func TestGC_SwapMarkerWriteFailureRetainsRetriesAndRecovers(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real Ledger clones")
	}
	bare, clone, scheduler := setupClonedLedger(t)
	t.Setenv("OX_XDG_DISABLE", "")
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	const repoID = "repo_gc_whisper"
	const endpointURL = "https://fake.test.invalid"
	require.NoError(t, os.WriteFile(filepath.Join(scheduler.config.ProjectRoot, ".sageox", "config.json"),
		[]byte(`{"repo_id":"repo_gc_whisper","endpoint":"https://fake.test.invalid"}`), 0600))
	scheduler.workspaceRegistry.endpoint = endpointURL
	canonical := paths.LedgersDataDir(repoID, endpointURL)
	require.NoError(t, os.MkdirAll(filepath.Dir(canonical), 0755))
	require.NoError(t, os.Rename(clone, canonical))
	clone = canonical
	dbPath := filepath.Join(paths.WhisperDBDir(repoID, endpointURL), "whisper.db")
	store, err := whisperstore.Open(dbPath)
	require.NoError(t, err)
	registry := NewWhisperRegistry(store, nil)
	t.Cleanup(func() { registry.Close() })
	scheduler.whisperRegistry = registry
	require.NoError(t, registry.Add("ledger", whisperstore.WhisperEntry{
		ID: "before-gc", Scope: "ledger", Type: whisperstore.WhisperTrigger,
		Source: "test", Topic: "gc", Content: "before", Importance: whisperstore.ImportanceNormal, CreatedAt: time.Now(),
	}))
	entry := filepath.Join(".git", "ox-plan-review-pending", "only.json")
	local := filepath.Join("sessions", "local.txt")
	for path, contents := range map[string]string{entry: "only retry copy", local: "unpublished content"} {
		full := filepath.Join(clone, path)
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0755))
		require.NoError(t, os.WriteFile(full, []byte(contents), 0644))
	}
	scheduler.gcSwapWindowTestHook = func() {
		require.NoError(t, os.Mkdir(clone+".gc-swap-done", 0755))
	}
	restoreCache := gcRestoreCache
	t.Cleanup(func() { gcRestoreCache = restoreCache })
	gcRestoreCache = func(backup, dst string) error {
		require.ErrorContains(t, store.Add(whisperstore.WhisperEntry{
			ID: "during-restore", Scope: "ledger", Type: whisperstore.WhisperTrigger,
			Content: "must be refused", CreatedAt: time.Now(),
		}), "database is closed", "restoration must not accept writes to the old store")
		return restoreCache(backup, dst)
	}
	ws := WorkspaceState{ID: "marker-refused", Type: WorkspaceTypeLedger, Path: clone, CloneURL: "file://" + bare, Exists: true}
	require.Equal(t, gcFailed, scheduler.runBlueGreenGC(context.Background(), ws))
	gcRestoreCache = restoreCache
	for path, contents := range map[string]string{entry: "only retry copy", local: "unpublished content"} {
		data, err := os.ReadFile(filepath.Join(clone+".old", path))
		require.NoError(t, err)
		require.Equal(t, contents, string(data))
	}
	require.NoDirExists(t, clone+".gc-untracked", "restored captures must not be reapplied on recovery")
	data, err := os.ReadFile(filepath.Join(clone, local))
	require.NoError(t, err)
	require.Equal(t, "unpublished content", string(data), "the swapped-in clone already contains local state")
	require.NoFileExists(t, clone+".gc-swap-lock")
	require.NotNil(t, registry.LedgerStore(), "failed cleanup must reopen the surviving clone's store")
	entries, _, err := registry.GetWhispersPage("", time.Time{}, 50)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "before-gc", entries[0].ID)
	require.NoError(t, registry.Add("ledger", whisperstore.WhisperEntry{
		ID: "after-failure", Scope: "ledger", Type: whisperstore.WhisperTrigger,
		Source: "test", Topic: "gc", Content: "after", Importance: whisperstore.ImportanceNormal, CreatedAt: time.Now(),
	}))

	scheduler.gcSwapWindowTestHook = nil
	require.NoError(t, os.Remove(clone+".gc-swap-done"))
	require.Equal(t, gcSuccess, scheduler.runBlueGreenGC(context.Background(), ws))
	for path, contents := range map[string]string{entry: "only retry copy", local: "unpublished content"} {
		data, err := os.ReadFile(filepath.Join(clone, path))
		require.NoError(t, err)
		require.Equal(t, contents, string(data))
	}
	require.NoDirExists(t, clone+".old")
	scheduler.reopenWhisperStoreAfterGC()
	entries, _, err = registry.GetWhispersPage("", time.Time{}, 50)
	require.NoError(t, err)
	require.Len(t, entries, 2, "both the preserved entry and the post-failure write must survive a retry")
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

// TestRemoveRejFiles_SweepsTreebutSkipsGit verifies the gc .rej cleanup that
// runs after `git apply --reject`: every .rej under the repo is deleted (so it
// can't be committed or block GC), while anything under .git is left untouched.
// Failure prevented: partial-apply conflict markers polluting ledger history.
func TestRemoveRejFiles_SweepsTreeButSkipsGit(t *testing.T) {
	dir := t.TempDir()
	// .rej files at various depths
	rejPaths := []string{
		filepath.Join(dir, "a.rej"),
		filepath.Join(dir, "sessions", "s1", "meta.json.rej"),
		filepath.Join(dir, "sessions", "s2", "session.md.rej"),
	}
	for _, p := range rejPaths {
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0755))
		require.NoError(t, os.WriteFile(p, []byte("reject"), 0644))
	}
	// a .rej inside .git must NOT be touched
	gitRej := filepath.Join(dir, ".git", "keep.rej")
	require.NoError(t, os.MkdirAll(filepath.Dir(gitRej), 0755))
	require.NoError(t, os.WriteFile(gitRej, []byte("internal"), 0644))
	// a non-.rej file must survive
	keep := filepath.Join(dir, "sessions", "s1", "meta.json")
	require.NoError(t, os.WriteFile(keep, []byte("real"), 0644))

	n := removeRejFiles(dir)

	assert.Equal(t, 3, n, "should remove exactly the 3 tree .rej files")
	for _, p := range rejPaths {
		_, err := os.Stat(p)
		assert.True(t, os.IsNotExist(err), "%s should be deleted", p)
	}
	_, err := os.Stat(gitRej)
	assert.NoError(t, err, ".git/*.rej must be preserved")
	_, err = os.Stat(keep)
	assert.NoError(t, err, "non-.rej files must survive")
}

// TestIsNonFastForwardErr_ClassifiesRejectionShapes proves the diverge-
// capture gate (runBlueGreenGCOpts's captureUnpushedOnDiverge path) only
// fires for an actual "remote has commits we don't" rejection, never for a
// superficially similar push failure. Pure string classification, no git
// subprocess or locale dependency needed — RunGit's LC_ALL=C/LANG=C pinning
// (internal/gitutil/run.go) is what keeps the *real* git messages this
// matches against in the English form these cases assume.
//
// Fixture text is verbatim real git/GitHub/GitLab CLI output (multi-line,
// including the "[remote rejected] ... (protected branch hook declined)"
// / "(pre-receive hook declined)" shape), not a paraphrase — a synthetic
// stand-in that merely says "protected branch" without the actual
// "rejected" wording real git emits would pass against a broadened,
// buggy `strings.Contains(msg, "rejected")` classifier just as easily as
// the correct one, silently failing to guard the exact bug class this test
// exists for.
//
// Failure prevented: a protected-branch rejection, a pre-receive hook
// decline (the exact GitLab-LFS "error trap" documented in
// .claude/rules/lfs-no-git-lfs-binary.md), an auth failure, or a network
// failure misclassified as "diverged" would make GC capture-and-discard
// local history against a merge-base that was never actually the problem —
// the opposite failure from today's overly-conservative gcSkippedDirty
// bail, but just as much a correctness bug (silently rewriting local state
// on top of the wrong diagnosis).
func TestIsNonFastForwardErr_ClassifiesRejectionShapes(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil error", nil, false},
		{
			"non-fast-forward rejection",
			errors.New(`git push: To github.com:org/repo.git
 ! [rejected]        main -> main (non-fast-forward)
error: failed to push some refs to 'github.com:org/repo.git'
hint: Updates were rejected because the tip of your current branch is behind
hint: its remote counterpart.: exit status 1`),
			true,
		},
		{
			"fetch-first rejection",
			errors.New(`git push: To github.com:org/repo.git
 ! [rejected]        main -> main (fetch first)
error: failed to push some refs to 'github.com:org/repo.git'
hint: Updates were rejected because the remote contains work that you do
hint: not have locally.: exit status 1`),
			true,
		},
		{
			"protected branch rejection (GitHub)",
			errors.New(`git push: remote: error: GH006: Protected branch update failed for refs/heads/main.
remote: error: Required status check "ci" is expected.
To github.com:org/repo.git
 ! [remote rejected] main -> main (protected branch hook declined)
error: failed to push some refs to 'github.com:org/repo.git': exit status 1`),
			false,
		},
		{
			"pre-receive hook decline (generic, and the GitLab LFS error trap)",
			errors.New(`git push: remote: GitLab: LFS objects are missing. Ensure LFS is properly set up or try a manual "git lfs push --all".
To gitlab.com:org/repo.git
 ! [remote rejected] main -> main (pre-receive hook declined)
error: failed to push some refs to 'gitlab.com:org/repo.git': exit status 1`),
			false,
		},
		{
			"auth failure",
			errors.New(`git push: remote: HTTP Basic: Access denied
fatal: Authentication failed for 'https://example.com/repo.git/': exit status 128`),
			false,
		},
		{
			"network unreachable",
			errors.New(`git push: fatal: unable to access 'https://example.com/repo.git/': Could not resolve host: example.com: exit status 128`),
			false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isNonFastForwardErr(tt.err))
		})
	}
}
