package daemon

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/ledger"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupLedgerBareRepo creates a bare git repo with ledger structure (sessions/, .sync/, audit/).
func setupLedgerBareRepo(t *testing.T) string {
	t.Helper()
	tmpDir := t.TempDir()
	bareDir := filepath.Join(tmpDir, "ledger.bare")
	workDir := filepath.Join(tmpDir, "work")

	require.NoError(t, exec.Command("git", "init", "--bare", "--initial-branch=main", bareDir).Run())
	require.NoError(t, exec.Command("git", "-C", bareDir, "config", "uploadpack.allowfilter", "true").Run())

	require.NoError(t, exec.Command("git", "clone", bareDir, workDir).Run())
	gitConfig(t, workDir)

	// create ledger directory structure
	for _, dir := range []string{"sessions", ".sync", "audit"} {
		require.NoError(t, os.MkdirAll(filepath.Join(workDir, dir), 0755))
		require.NoError(t, os.WriteFile(filepath.Join(workDir, dir, ".gitkeep"), []byte(""), 0644))
	}

	// commit and push
	require.NoError(t, exec.Command("git", "-C", workDir, "add", ".").Run())
	require.NoError(t, exec.Command("git", "-C", workDir, "commit", "-m", "initial ledger").Run())
	require.NoError(t, exec.Command("git", "-C", workDir, "push", "origin", "HEAD:main").Run())

	return bareDir
}

// setupClonedLedger creates a bare repo with ledger structure, clones it with sparse checkout,
// and returns (bareDir, ledgerDir, scheduler).
func setupClonedLedger(t *testing.T) (string, string, *SyncScheduler) {
	t.Helper()
	isolateCredentials(t)

	bareDir := setupLedgerBareRepo(t)
	cloneURL := "file://" + bareDir

	projectDir := setupProjectWithConfig(t, "")
	scheduler := newTestScheduler(projectDir)

	ledgerDir := filepath.Join(t.TempDir(), "ledger")
	require.NoError(t, ledger.CloneWithSparseCheckout(ledgerDir, cloneURL))

	return bareDir, ledgerDir, scheduler
}

func TestBlueGreenGC_Ledger_Success(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	bareDir, ledgerDir, scheduler := setupClonedLedger(t)

	ws := WorkspaceState{
		ID:       "ledger",
		Type:     WorkspaceTypeLedger,
		Path:     ledgerDir,
		CloneURL: "file://" + bareDir,
		Exists:   true,
	}
	registry := scheduler.WorkspaceRegistry()
	registry.mu.Lock()
	registry.ledger = &ws
	registry.workspaces[ws.ID] = &ws
	registry.mu.Unlock()

	result := scheduler.runBlueGreenGC(context.Background(), ws)
	assert.Equal(t, gcSuccess, result)

	// verify the repo still works after GC
	assert.DirExists(t, filepath.Join(ledgerDir, "sessions"))
	assert.DirExists(t, filepath.Join(ledgerDir, ".git"))

	// artifacts cleaned up
	assert.NoDirExists(t, ledgerDir+".old")
	assert.NoDirExists(t, ledgerDir+".new")

	// LastGCTime was updated
	lastGC := scheduler.WorkspaceRegistry().GetLastGCTime("ledger")
	assert.False(t, lastGC.IsZero(), "LastGCTime should be set after GC")
}

func TestBlueGreenGC_Ledger_PreservesCacheDir(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	bareDir, ledgerDir, scheduler := setupClonedLedger(t)

	// create cache directory with content (simulates codedb indexes)
	cacheDir := filepath.Join(ledgerDir, ".sageox", "cache", "codedb")
	require.NoError(t, os.MkdirAll(cacheDir, 0755))
	cacheContent := "sqlite-database-content"
	require.NoError(t, os.WriteFile(filepath.Join(cacheDir, "index.db"), []byte(cacheContent), 0644))

	ws := WorkspaceState{
		ID:       "ledger",
		Type:     WorkspaceTypeLedger,
		Path:     ledgerDir,
		CloneURL: "file://" + bareDir,
		Exists:   true,
	}
	registry := scheduler.WorkspaceRegistry()
	registry.mu.Lock()
	registry.ledger = &ws
	registry.workspaces[ws.ID] = &ws
	registry.mu.Unlock()

	result := scheduler.runBlueGreenGC(context.Background(), ws)
	assert.Equal(t, gcSuccess, result)

	// cache should survive the reclone
	restoredContent, err := os.ReadFile(filepath.Join(ledgerDir, ".sageox", "cache", "codedb", "index.db"))
	require.NoError(t, err)
	assert.Equal(t, cacheContent, string(restoredContent), "cache should be preserved across reclone")

	// cache backup artifact should be cleaned up
	assert.NoDirExists(t, ledgerDir+".gc-cache")
}

func TestBlueGreenGC_Ledger_PreservesUncommittedChanges(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	bareDir, ledgerDir, scheduler := setupClonedLedger(t)

	// modify a tracked file (unstaged change)
	gitkeepPath := filepath.Join(ledgerDir, "sessions", ".gitkeep")
	dirtyContent := "modified by user\n"
	require.NoError(t, os.WriteFile(gitkeepPath, []byte(dirtyContent), 0644))

	ws := WorkspaceState{
		ID:       "ledger",
		Type:     WorkspaceTypeLedger,
		Path:     ledgerDir,
		CloneURL: "file://" + bareDir,
		Exists:   true,
	}

	result := scheduler.runBlueGreenGC(context.Background(), ws)
	assert.Equal(t, gcSuccess, result)

	// the modification must survive the reclone
	content, err := os.ReadFile(gitkeepPath)
	require.NoError(t, err)
	assert.Equal(t, dirtyContent, string(content), "uncommitted change should be preserved")

	// no leftover preservation artifacts
	assert.NoFileExists(t, ledgerDir+".gc-diff")
	assert.NoDirExists(t, ledgerDir+".gc-untracked")
}

func TestBlueGreenGC_Ledger_PreservesUntrackedFiles(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	bareDir, ledgerDir, scheduler := setupClonedLedger(t)

	// create an untracked file in sessions/
	sessionDir := filepath.Join(ledgerDir, "sessions", "test-session")
	require.NoError(t, os.MkdirAll(sessionDir, 0755))
	untrackedContent := "session metadata\n"
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, "meta.json"), []byte(untrackedContent), 0644))

	ws := WorkspaceState{
		ID:       "ledger",
		Type:     WorkspaceTypeLedger,
		Path:     ledgerDir,
		CloneURL: "file://" + bareDir,
		Exists:   true,
	}

	result := scheduler.runBlueGreenGC(context.Background(), ws)
	assert.Equal(t, gcSuccess, result)

	// untracked file must survive
	content, err := os.ReadFile(filepath.Join(sessionDir, "meta.json"))
	require.NoError(t, err)
	assert.Equal(t, untrackedContent, string(content), "untracked file should be preserved")
}

func TestBlueGreenGC_Ledger_PushesUnpushedCommits(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	bareDir, ledgerDir, scheduler := setupClonedLedger(t)

	// make a local commit that is NOT pushed (simulates CLI session upload)
	sessionDir := filepath.Join(ledgerDir, "sessions", "test-session")
	require.NoError(t, os.MkdirAll(sessionDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, "meta.json"), []byte("session data"), 0644))
	gitConfig(t, ledgerDir)
	require.NoError(t, exec.Command("git", "-C", ledgerDir, "add", "--sparse", ".").Run())
	require.NoError(t, exec.Command("git", "-C", ledgerDir, "commit", "-m", "session: test-session").Run())

	// verify there are unpushed commits
	countOut, err := exec.Command("git", "-C", ledgerDir, "rev-list", "--count", "origin/main..HEAD").CombinedOutput()
	require.NoError(t, err)
	count, _ := strconv.Atoi(strings.TrimSpace(string(countOut)))
	assert.GreaterOrEqual(t, count, 1, "should have at least 1 unpushed commit")

	ws := WorkspaceState{
		ID:       "ledger",
		Type:     WorkspaceTypeLedger,
		Path:     ledgerDir,
		CloneURL: "file://" + bareDir,
		Exists:   true,
	}

	result := scheduler.runBlueGreenGC(context.Background(), ws)
	assert.Equal(t, gcSuccess, result)

	// verify the commit was pushed to the bare repo
	logOut, err := exec.Command("git", "-C", bareDir, "log", "--oneline", "main").CombinedOutput()
	require.NoError(t, err)
	assert.Contains(t, string(logOut), "session: test-session", "unpushed commit should be in bare repo after GC")

	// the recloned repo should have the session data
	content, err := os.ReadFile(filepath.Join(ledgerDir, "sessions", "test-session", "meta.json"))
	require.NoError(t, err)
	assert.Equal(t, "session data", string(content))
}

func TestBlueGreenGC_Ledger_SkipsWhenPushFails(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	bareDir, ledgerDir, scheduler := setupClonedLedger(t)

	// make a local commit
	require.NoError(t, os.WriteFile(filepath.Join(ledgerDir, "sessions", ".gitkeep"), []byte("local change"), 0644))
	gitConfig(t, ledgerDir)
	require.NoError(t, exec.Command("git", "-C", ledgerDir, "add", "--sparse", ".").Run())
	require.NoError(t, exec.Command("git", "-C", ledgerDir, "commit", "-m", "unpushable").Run())

	// break the bare repo so push will fail
	require.NoError(t, os.RemoveAll(bareDir))

	ws := WorkspaceState{
		ID:       "ledger",
		Type:     WorkspaceTypeLedger,
		Path:     ledgerDir,
		CloneURL: "file://" + bareDir,
		Exists:   true,
	}

	result := scheduler.runBlueGreenGC(context.Background(), ws)
	assert.Equal(t, gcSkippedDirty, result, "should skip when push fails")

	// original repo should still be intact
	assert.DirExists(t, filepath.Join(ledgerDir, "sessions"))
	assert.DirExists(t, filepath.Join(ledgerDir, ".git"))
}

func TestBlueGreenGC_Ledger_CloneFailsKeepsOld(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	_, ledgerDir, scheduler := setupClonedLedger(t)

	// record original content
	origContent, err := os.ReadFile(filepath.Join(ledgerDir, "sessions", ".gitkeep"))
	require.NoError(t, err)

	ws := WorkspaceState{
		ID:       "ledger",
		Type:     WorkspaceTypeLedger,
		Path:     ledgerDir,
		CloneURL: "file:///nonexistent/repo.git", // invalid URL
		Exists:   true,
	}

	result := scheduler.runBlueGreenGC(context.Background(), ws)
	assert.Equal(t, gcFailed, result)

	// old repo should still be intact
	assert.DirExists(t, filepath.Join(ledgerDir, "sessions"))
	assert.DirExists(t, filepath.Join(ledgerDir, ".git"))
	content, err := os.ReadFile(filepath.Join(ledgerDir, "sessions", ".gitkeep"))
	require.NoError(t, err)
	assert.Equal(t, string(origContent), string(content))

	// .new should be cleaned up
	assert.NoDirExists(t, ledgerDir+".new")
}

func TestBlueGreenGC_Ledger_ValidationFailsKeepsOld(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	// create a bare repo WITHOUT sessions/ dir so validation will fail
	isolateCredentials(t)
	tmpDir := t.TempDir()
	bareDir := filepath.Join(tmpDir, "ledger.bare")
	workDir := filepath.Join(tmpDir, "work")

	require.NoError(t, exec.Command("git", "init", "--bare", "--initial-branch=main", bareDir).Run())
	require.NoError(t, exec.Command("git", "-C", bareDir, "config", "uploadpack.allowfilter", "true").Run())
	require.NoError(t, exec.Command("git", "clone", bareDir, workDir).Run())
	gitConfig(t, workDir)

	// only create .sync/ (no sessions/) so validateLedgerGCClone fails
	require.NoError(t, os.MkdirAll(filepath.Join(workDir, ".sync"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(workDir, ".sync", ".gitkeep"), []byte(""), 0644))
	require.NoError(t, exec.Command("git", "-C", workDir, "add", ".").Run())
	require.NoError(t, exec.Command("git", "-C", workDir, "commit", "-m", "initial").Run())
	require.NoError(t, exec.Command("git", "-C", workDir, "push", "origin", "HEAD:main").Run())

	// clone the good repo first (with sessions/)
	goodBareDir := setupLedgerBareRepo(t)
	projectDir := setupProjectWithConfig(t, "")
	scheduler := newTestScheduler(projectDir)
	ledgerDir := filepath.Join(t.TempDir(), "ledger")
	require.NoError(t, ledger.CloneWithSparseCheckout(ledgerDir, "file://"+goodBareDir))

	// now GC with the broken bare repo (no sessions/)
	ws := WorkspaceState{
		ID:       "ledger",
		Type:     WorkspaceTypeLedger,
		Path:     ledgerDir,
		CloneURL: "file://" + bareDir, // points to repo without sessions/
		Exists:   true,
	}

	result := scheduler.runBlueGreenGC(context.Background(), ws)
	assert.Equal(t, gcFailed, result)

	// original repo should be intact
	assert.DirExists(t, filepath.Join(ledgerDir, "sessions"))
	assert.NoDirExists(t, ledgerDir+".new")
}

func TestBlueGreenGC_Ledger_CacheWithNestedSubdirs(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	bareDir, ledgerDir, scheduler := setupClonedLedger(t)

	// create deeply nested cache structure (simulates real codedb layout)
	paths := map[string]string{
		".sageox/cache/codedb/index.db":          "main-index",
		".sageox/cache/codedb/shards/001/data":   "shard-data-1",
		".sageox/cache/codedb/shards/002/data":   "shard-data-2",
		".sageox/cache/codedb/bleve/store/index": "bleve-index",
		".sageox/cache/sync-state.json":          `{"last_sync":"2026-01-01"}`,
	}
	for relPath, content := range paths {
		fullPath := filepath.Join(ledgerDir, relPath)
		require.NoError(t, os.MkdirAll(filepath.Dir(fullPath), 0755))
		require.NoError(t, os.WriteFile(fullPath, []byte(content), 0644))
	}

	ws := WorkspaceState{
		ID:       "ledger",
		Type:     WorkspaceTypeLedger,
		Path:     ledgerDir,
		CloneURL: "file://" + bareDir,
		Exists:   true,
	}
	registry := scheduler.WorkspaceRegistry()
	registry.mu.Lock()
	registry.ledger = &ws
	registry.workspaces[ws.ID] = &ws
	registry.mu.Unlock()

	result := scheduler.runBlueGreenGC(context.Background(), ws)
	assert.Equal(t, gcSuccess, result)

	// all nested files should survive
	for relPath, expectedContent := range paths {
		fullPath := filepath.Join(ledgerDir, relPath)
		content, err := os.ReadFile(fullPath)
		require.NoError(t, err, "missing restored file: %s", relPath)
		assert.Equal(t, expectedContent, string(content), "content mismatch for %s", relPath)
	}
}

func TestBlueGreenGC_Ledger_AllPreservationMechanisms(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	bareDir, ledgerDir, scheduler := setupClonedLedger(t)
	gitConfig(t, ledgerDir)

	// 1. unpushed commit (simulates CLI session upload)
	committedSession := filepath.Join(ledgerDir, "sessions", "committed-session")
	require.NoError(t, os.MkdirAll(committedSession, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(committedSession, "meta.json"), []byte("committed"), 0644))
	require.NoError(t, exec.Command("git", "-C", ledgerDir, "add", "--sparse", ".").Run())
	require.NoError(t, exec.Command("git", "-C", ledgerDir, "commit", "-m", "session: committed-session").Run())

	// 2. uncommitted tracked change
	require.NoError(t, os.WriteFile(filepath.Join(ledgerDir, "sessions", ".gitkeep"), []byte("dirty"), 0644))

	// 3. untracked file (in-progress session)
	untrackedSession := filepath.Join(ledgerDir, "sessions", "untracked-session")
	require.NoError(t, os.MkdirAll(untrackedSession, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(untrackedSession, "meta.json"), []byte("untracked"), 0644))

	// 4. cache directory
	cacheDir := filepath.Join(ledgerDir, ".sageox", "cache", "codedb")
	require.NoError(t, os.MkdirAll(cacheDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(cacheDir, "index.db"), []byte("cache-data"), 0644))

	ws := WorkspaceState{
		ID:       "ledger",
		Type:     WorkspaceTypeLedger,
		Path:     ledgerDir,
		CloneURL: "file://" + bareDir,
		Exists:   true,
	}
	registry := scheduler.WorkspaceRegistry()
	registry.mu.Lock()
	registry.ledger = &ws
	registry.workspaces[ws.ID] = &ws
	registry.mu.Unlock()

	result := scheduler.runBlueGreenGC(context.Background(), ws)
	assert.Equal(t, gcSuccess, result)

	// verify all four preservation mechanisms worked
	// 1. committed session should be in the recloned repo (was pushed then cloned)
	content, err := os.ReadFile(filepath.Join(committedSession, "meta.json"))
	require.NoError(t, err)
	assert.Equal(t, "committed", string(content), "unpushed commit should survive")

	// 2. uncommitted tracked change should be restored
	content, err = os.ReadFile(filepath.Join(ledgerDir, "sessions", ".gitkeep"))
	require.NoError(t, err)
	assert.Equal(t, "dirty", string(content), "uncommitted change should survive")

	// 3. untracked file should be restored
	content, err = os.ReadFile(filepath.Join(untrackedSession, "meta.json"))
	require.NoError(t, err)
	assert.Equal(t, "untracked", string(content), "untracked file should survive")

	// 4. cache should be restored
	content, err = os.ReadFile(filepath.Join(cacheDir, "index.db"))
	require.NoError(t, err)
	assert.Equal(t, "cache-data", string(content), "cache should survive")

	// all artifacts cleaned up
	assert.NoDirExists(t, ledgerDir+".gc-cache")
	assert.NoFileExists(t, ledgerDir+".gc-diff")
	assert.NoDirExists(t, ledgerDir+".gc-untracked")
}

func TestBlueGreenGC_Ledger_NoCacheDir(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	bareDir, ledgerDir, scheduler := setupClonedLedger(t)

	// verify no cache exists
	assert.NoDirExists(t, filepath.Join(ledgerDir, ".sageox", "cache"))

	ws := WorkspaceState{
		ID:       "ledger",
		Type:     WorkspaceTypeLedger,
		Path:     ledgerDir,
		CloneURL: "file://" + bareDir,
		Exists:   true,
	}
	registry := scheduler.WorkspaceRegistry()
	registry.mu.Lock()
	registry.ledger = &ws
	registry.workspaces[ws.ID] = &ws
	registry.mu.Unlock()

	result := scheduler.runBlueGreenGC(context.Background(), ws)
	assert.Equal(t, gcSuccess, result)

	// should succeed without cache backup artifact
	assert.NoDirExists(t, ledgerDir+".gc-cache")
}

func TestValidateLedgerGCClone(t *testing.T) {
	dir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	s := &SyncScheduler{logger: logger}

	// missing .git → false
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "sessions"), 0755))
	assert.False(t, s.validateLedgerGCClone(dir))

	// has .git but missing sessions/ → false
	dir2 := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir2, ".git"), 0755))
	assert.False(t, s.validateLedgerGCClone(dir2))

	// has both → true
	dir3 := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir3, ".git"), 0755))
	require.NoError(t, os.MkdirAll(filepath.Join(dir3, "sessions"), 0755))
	assert.True(t, s.validateLedgerGCClone(dir3))
}

// --- Preservation edge cases (medium risk) ---
// These exercise shared gcCaptureDiff/gcCaptureUntracked/gcRestoreDiff functions
// with the ledger's sparse-checkout clone mechanism, which can surface different
// behavior than team context's two-phase clone.

func TestBlueGreenGC_Ledger_PreservesStagedChanges(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	bareDir, ledgerDir, scheduler := setupClonedLedger(t)
	gitConfig(t, ledgerDir)

	// stage a change to a tracked file
	stagedContent := "staged modification\n"
	require.NoError(t, os.WriteFile(filepath.Join(ledgerDir, "sessions", ".gitkeep"), []byte(stagedContent), 0644))
	require.NoError(t, exec.Command("git", "-C", ledgerDir, "add", "--sparse", "sessions/.gitkeep").Run())

	ws := WorkspaceState{
		ID:       "ledger",
		Type:     WorkspaceTypeLedger,
		Path:     ledgerDir,
		CloneURL: "file://" + bareDir,
		Exists:   true,
	}
	registry := scheduler.WorkspaceRegistry()
	registry.mu.Lock()
	registry.ledger = &ws
	registry.workspaces[ws.ID] = &ws
	registry.mu.Unlock()

	result := scheduler.runBlueGreenGC(context.Background(), ws)
	assert.Equal(t, gcSuccess, result)

	content, err := os.ReadFile(filepath.Join(ledgerDir, "sessions", ".gitkeep"))
	require.NoError(t, err)
	assert.Equal(t, stagedContent, string(content), "staged change should be preserved")
}

func TestBlueGreenGC_Ledger_PreservesMixedStagedAndUnstaged(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	bareDir, ledgerDir, scheduler := setupClonedLedger(t)
	gitConfig(t, ledgerDir)

	// stage a change, then overwrite with a further unstaged change
	require.NoError(t, os.WriteFile(filepath.Join(ledgerDir, "sessions", ".gitkeep"), []byte("staged version"), 0644))
	require.NoError(t, exec.Command("git", "-C", ledgerDir, "add", "--sparse", "sessions/.gitkeep").Run())
	finalContent := "unstaged version on top of staged"
	require.NoError(t, os.WriteFile(filepath.Join(ledgerDir, "sessions", ".gitkeep"), []byte(finalContent), 0644))

	ws := WorkspaceState{
		ID:       "ledger",
		Type:     WorkspaceTypeLedger,
		Path:     ledgerDir,
		CloneURL: "file://" + bareDir,
		Exists:   true,
	}
	registry := scheduler.WorkspaceRegistry()
	registry.mu.Lock()
	registry.ledger = &ws
	registry.workspaces[ws.ID] = &ws
	registry.mu.Unlock()

	result := scheduler.runBlueGreenGC(context.Background(), ws)
	assert.Equal(t, gcSuccess, result)

	// the latest (unstaged) content should be what survives
	content, err := os.ReadFile(filepath.Join(ledgerDir, "sessions", ".gitkeep"))
	require.NoError(t, err)
	assert.Equal(t, finalContent, string(content), "latest working tree content should be preserved")
}

func TestBlueGreenGC_Ledger_PreservesUntrackedInSubdirs(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	bareDir, ledgerDir, scheduler := setupClonedLedger(t)

	// create untracked files in deeply nested session subdirectories
	nestedDir := filepath.Join(ledgerDir, "sessions", "agent-abc", "artifacts", "2026")
	require.NoError(t, os.MkdirAll(nestedDir, 0755))
	nestedContent := "deep nested artifact\n"
	require.NoError(t, os.WriteFile(filepath.Join(nestedDir, "output.json"), []byte(nestedContent), 0644))

	ws := WorkspaceState{
		ID:       "ledger",
		Type:     WorkspaceTypeLedger,
		Path:     ledgerDir,
		CloneURL: "file://" + bareDir,
		Exists:   true,
	}
	registry := scheduler.WorkspaceRegistry()
	registry.mu.Lock()
	registry.ledger = &ws
	registry.workspaces[ws.ID] = &ws
	registry.mu.Unlock()

	result := scheduler.runBlueGreenGC(context.Background(), ws)
	assert.Equal(t, gcSuccess, result)

	content, err := os.ReadFile(filepath.Join(nestedDir, "output.json"))
	require.NoError(t, err)
	assert.Equal(t, nestedContent, string(content), "deeply nested untracked file should be preserved")
}

func TestBlueGreenGC_Ledger_PreservesBinaryUntrackedFile(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	bareDir, ledgerDir, scheduler := setupClonedLedger(t)

	// create a binary untracked file (e.g. session recording blob)
	binaryContent := make([]byte, 256)
	for i := range binaryContent {
		binaryContent[i] = byte(i)
	}
	require.NoError(t, os.WriteFile(filepath.Join(ledgerDir, "sessions", "recording.bin"), binaryContent, 0644))

	ws := WorkspaceState{
		ID:       "ledger",
		Type:     WorkspaceTypeLedger,
		Path:     ledgerDir,
		CloneURL: "file://" + bareDir,
		Exists:   true,
	}
	registry := scheduler.WorkspaceRegistry()
	registry.mu.Lock()
	registry.ledger = &ws
	registry.workspaces[ws.ID] = &ws
	registry.mu.Unlock()

	result := scheduler.runBlueGreenGC(context.Background(), ws)
	assert.Equal(t, gcSuccess, result)

	content, err := os.ReadFile(filepath.Join(ledgerDir, "sessions", "recording.bin"))
	require.NoError(t, err)
	assert.Equal(t, binaryContent, content, "binary untracked file should survive reclone with identical content")
}

func TestBlueGreenGC_Ledger_StagedDeletePreserved(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	bareDir, ledgerDir, scheduler := setupClonedLedger(t)
	gitConfig(t, ledgerDir)

	// stage a deletion of a tracked file
	require.NoError(t, exec.Command("git", "-C", ledgerDir, "rm", "sessions/.gitkeep").Run())

	ws := WorkspaceState{
		ID:       "ledger",
		Type:     WorkspaceTypeLedger,
		Path:     ledgerDir,
		CloneURL: "file://" + bareDir,
		Exists:   true,
	}
	registry := scheduler.WorkspaceRegistry()
	registry.mu.Lock()
	registry.ledger = &ws
	registry.workspaces[ws.ID] = &ws
	registry.mu.Unlock()

	result := scheduler.runBlueGreenGC(context.Background(), ws)
	assert.Equal(t, gcSuccess, result)

	// the deletion should be re-applied after reclone
	assert.NoFileExists(t, filepath.Join(ledgerDir, "sessions", ".gitkeep"), "staged delete should be preserved")
}

func TestBlueGreenGC_Ledger_DiffConflictPreservesDiffFile(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	bareDir, ledgerDir, scheduler := setupClonedLedger(t)
	gitConfig(t, ledgerDir)

	// push the current state so local is in sync
	require.NoError(t, exec.Command("git", "-C", ledgerDir, "push", "origin", "HEAD", "--quiet").Run())

	// push a conflicting change via a second clone
	pusherDir := filepath.Join(t.TempDir(), "pusher")
	require.NoError(t, exec.Command("git", "clone", bareDir, pusherDir).Run())
	gitConfig(t, pusherDir)
	require.NoError(t, os.WriteFile(filepath.Join(pusherDir, "sessions", ".gitkeep"), []byte("completely different remote content\nwith multiple lines\n"), 0644))
	require.NoError(t, exec.Command("git", "-C", pusherDir, "add", "sessions/.gitkeep").Run())
	require.NoError(t, exec.Command("git", "-C", pusherDir, "commit", "-m", "conflict").Run())
	require.NoError(t, exec.Command("git", "-C", pusherDir, "push", "origin", "HEAD:main").Run())

	// make a local uncommitted change that conflicts with the remote
	require.NoError(t, os.WriteFile(filepath.Join(ledgerDir, "sessions", ".gitkeep"), []byte("local edit\nwith different content\n"), 0644))

	ws := WorkspaceState{
		ID:       "ledger",
		Type:     WorkspaceTypeLedger,
		Path:     ledgerDir,
		CloneURL: "file://" + bareDir,
		Exists:   true,
	}
	registry := scheduler.WorkspaceRegistry()
	registry.mu.Lock()
	registry.ledger = &ws
	registry.workspaces[ws.ID] = &ws
	registry.mu.Unlock()

	result := scheduler.runBlueGreenGC(context.Background(), ws)
	// reclone succeeds even if diff apply fails — the repo is valid
	assert.Equal(t, gcSuccess, result)

	// the .gc-diff file should be preserved for manual recovery
	diffFile := ledgerDir + ".gc-diff"
	assert.FileExists(t, diffFile, "diff file should be preserved when apply fails")
	t.Cleanup(func() { os.Remove(diffFile) })
}

func TestBlueGreenGC_Ledger_CleanTreeStillWorks(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	bareDir, ledgerDir, scheduler := setupClonedLedger(t)

	// record original content
	origContent, err := os.ReadFile(filepath.Join(ledgerDir, "sessions", ".gitkeep"))
	require.NoError(t, err)

	ws := WorkspaceState{
		ID:       "ledger",
		Type:     WorkspaceTypeLedger,
		Path:     ledgerDir,
		CloneURL: "file://" + bareDir,
		Exists:   true,
	}
	registry := scheduler.WorkspaceRegistry()
	registry.mu.Lock()
	registry.ledger = &ws
	registry.workspaces[ws.ID] = &ws
	registry.mu.Unlock()

	result := scheduler.runBlueGreenGC(context.Background(), ws)
	assert.Equal(t, gcSuccess, result)

	content, err := os.ReadFile(filepath.Join(ledgerDir, "sessions", ".gitkeep"))
	require.NoError(t, err)
	assert.Equal(t, string(origContent), string(content))

	// no preservation artifacts should exist
	assert.NoFileExists(t, ledgerDir+".gc-diff")
	assert.NoDirExists(t, ledgerDir+".gc-untracked")
	assert.NoDirExists(t, ledgerDir+".gc-cache")
}

// --- Ledger-specific high risk tests ---
// These test ledger-only divergence points: ledgerMu, cache restore failure,
// and the scheduling interval check in checkAndRunGC.

func TestGcRestoreCache_FailureRetainsBackup(t *testing.T) {
	// unit test: gcRestoreCache fails when destination is unwritable,
	// and the backup dir is NOT removed by the caller on error.
	backupDir := filepath.Join(t.TempDir(), "cache-backup")
	require.NoError(t, os.MkdirAll(filepath.Join(backupDir, "codedb"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(backupDir, "codedb", "index.db"), []byte("precious-data"), 0644))

	// make restore destination unwritable: create .sageox as a FILE so MkdirAll fails
	dstRepo := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dstRepo, ".sageox"), []byte("blocker"), 0444))
	t.Cleanup(func() { os.Chmod(filepath.Join(dstRepo, ".sageox"), 0755) })

	err := gcRestoreCache(backupDir, dstRepo)
	require.Error(t, err, "restore should fail when .sageox is a file, not a directory")

	// backup must still exist for manual recovery
	assert.DirExists(t, backupDir, "backup should be retained on restore failure")
	content, readErr := os.ReadFile(filepath.Join(backupDir, "codedb", "index.db"))
	require.NoError(t, readErr)
	assert.Equal(t, "precious-data", string(content), "backup content should be intact")
}

func TestGcPreserveCache_NoCacheReturnsNil(t *testing.T) {
	// unit test: gcPreserveCache returns nil when no cache exists
	srcRepo := t.TempDir()
	backupDir := filepath.Join(t.TempDir(), "backup")

	err := gcPreserveCache(srcRepo, backupDir)
	require.NoError(t, err, "should succeed when no cache exists")
	assert.NoDirExists(t, backupDir, "no backup should be created when no cache exists")
}

func TestBlueGreenGC_Ledger_MutexReleasedAfterSuccess(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	bareDir, ledgerDir, scheduler := setupClonedLedger(t)

	ws := WorkspaceState{
		ID:       "ledger",
		Type:     WorkspaceTypeLedger,
		Path:     ledgerDir,
		CloneURL: "file://" + bareDir,
		Exists:   true,
	}
	registry := scheduler.WorkspaceRegistry()
	registry.mu.Lock()
	registry.ledger = &ws
	registry.workspaces[ws.ID] = &ws
	registry.mu.Unlock()

	// run GC successfully
	result := scheduler.runBlueGreenGC(context.Background(), ws)
	assert.Equal(t, gcSuccess, result)

	// run GC again — would deadlock if ledgerMu was not released
	result = scheduler.runBlueGreenGC(context.Background(), ws)
	assert.Equal(t, gcSuccess, result, "second GC should succeed, proving mutex was released after first")
}

func TestBlueGreenGC_Ledger_MutexReleasedOnCloneFailure(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	bareDir, ledgerDir, scheduler := setupClonedLedger(t)

	ws := WorkspaceState{
		ID:       "ledger",
		Type:     WorkspaceTypeLedger,
		Path:     ledgerDir,
		CloneURL: "file:///nonexistent/repo.git",
		Exists:   true,
	}

	// first GC fails (bad clone URL)
	result := scheduler.runBlueGreenGC(context.Background(), ws)
	assert.Equal(t, gcFailed, result)

	// second GC with correct URL — if mutex leaked, this deadlocks
	ws.CloneURL = "file://" + bareDir
	registry := scheduler.WorkspaceRegistry()
	registry.mu.Lock()
	registry.ledger = &ws
	registry.workspaces[ws.ID] = &ws
	registry.mu.Unlock()

	result = scheduler.runBlueGreenGC(context.Background(), ws)
	assert.Equal(t, gcSuccess, result, "GC should succeed after prior clone failure, proving mutex was released")
}

func TestBlueGreenGC_Ledger_FullCloneUpgrade(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	// create bare repo and clone it as a FULL clone (not sparse)
	isolateCredentials(t)
	bareDir := setupLedgerBareRepo(t)
	cloneURL := "file://" + bareDir

	projectDir := setupProjectWithConfig(t, "")
	scheduler := newTestScheduler(projectDir)

	ledgerDir := filepath.Join(t.TempDir(), "ledger")
	require.NoError(t, exec.Command("git", "clone", cloneURL, ledgerDir).Run())

	// verify it's a full clone (not partial) — no partial clone filter set
	assert.False(t, isPartialClone(ledgerDir), "should start as full clone")

	ws := WorkspaceState{
		ID:       "ledger",
		Type:     WorkspaceTypeLedger,
		Path:     ledgerDir,
		CloneURL: cloneURL,
		Exists:   true,
	}
	registry := scheduler.WorkspaceRegistry()
	registry.mu.Lock()
	registry.ledger = &ws
	registry.workspaces[ws.ID] = &ws
	registry.mu.Unlock()

	result := scheduler.runBlueGreenGC(context.Background(), ws)
	assert.Equal(t, gcSuccess, result)

	// after GC, the repo should work and have sparse checkout configured
	assert.DirExists(t, filepath.Join(ledgerDir, "sessions"))
	assert.DirExists(t, filepath.Join(ledgerDir, ".git"))

	// verify sparse checkout is enabled (CloneWithSparseCheckout configures cone mode)
	sparseOut, err := exec.Command("git", "-C", ledgerDir, "config", "--get", "core.sparseCheckout").CombinedOutput()
	require.NoError(t, err)
	assert.Equal(t, "true", strings.TrimSpace(string(sparseOut)), "sparse checkout should be enabled after GC reclone")
}
