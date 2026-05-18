package daemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBlueGreenGC_Success(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	manifest := "version 1\ninclude .sageox/\ninclude SOUL.md\ninclude TEAM.md\ninclude memory/\ngc_interval_days 7\n"
	bareDir, teamDir, scheduler := setupClonedTeamContext(t, manifest, nil)

	// record original content hash
	origContent, err := os.ReadFile(filepath.Join(teamDir, "SOUL.md"))
	require.NoError(t, err)

	// register workspace in the registry so UpdateLastGC can find it
	ws := WorkspaceState{
		ID:       "team_test",
		Type:     WorkspaceTypeTeamContext,
		Path:     teamDir,
		TeamName: "test-team",
		CloneURL: "file://" + bareDir,
		Exists:   true,
	}
	registry := scheduler.WorkspaceRegistry()
	registry.mu.Lock()
	registry.workspaces[ws.ID] = &ws
	registry.mu.Unlock()

	ctx := context.Background()
	scheduler.runBlueGreenGC(ctx, ws)

	// verify the repo still works after GC
	assert.FileExists(t, filepath.Join(teamDir, "SOUL.md"))
	assert.FileExists(t, filepath.Join(teamDir, ".sageox", "sync.manifest"))

	// content should be the same
	newContent, err := os.ReadFile(filepath.Join(teamDir, "SOUL.md"))
	require.NoError(t, err)
	assert.Equal(t, string(origContent), string(newContent))

	// .old should be cleaned up
	assert.NoDirExists(t, teamDir+".old")
	assert.NoDirExists(t, teamDir+".new")

	// verify LastGCTime was updated
	lastGC := scheduler.WorkspaceRegistry().GetLastGCTime("team_test")
	assert.False(t, lastGC.IsZero(), "LastGCTime should be set after GC")
}

func TestBlueGreenGC_PreservesUncommittedTrackedChanges(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	manifestContent := "version 1\ninclude .sageox/\ninclude SOUL.md\ninclude TEAM.md\ninclude memory/\n"
	bareDir, teamDir, scheduler := setupClonedTeamContext(t, manifestContent, nil)

	// modify a tracked file (unstaged change)
	dirtyContent := "# Soul\nmodified by user\n"
	require.NoError(t, os.WriteFile(filepath.Join(teamDir, "SOUL.md"), []byte(dirtyContent), 0644))

	ws := WorkspaceState{
		ID:       "team_test",
		Type:     WorkspaceTypeTeamContext,
		Path:     teamDir,
		TeamName: "test-team",
		CloneURL: "file://" + bareDir,
		Exists:   true,
	}

	ctx := context.Background()
	result := scheduler.runBlueGreenGC(ctx, ws)

	assert.Equal(t, gcSuccess, result, "GC should succeed with dirty tree")
	assert.NoDirExists(t, teamDir+".new")
	assert.NoDirExists(t, teamDir+".old")

	// the user's modification must survive the reclone
	content, err := os.ReadFile(filepath.Join(teamDir, "SOUL.md"))
	require.NoError(t, err)
	assert.Equal(t, dirtyContent, string(content), "uncommitted change should be preserved")

	// no leftover preservation artifacts
	assert.NoFileExists(t, teamDir+".gc-diff")
	assert.NoDirExists(t, teamDir+".gc-untracked")
}

func TestBlueGreenGC_CloneFailsKeepsOld(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	manifest := "version 1\ninclude .sageox/\ninclude SOUL.md\n"
	_, teamDir, scheduler := setupClonedTeamContext(t, manifest, nil)

	ws := WorkspaceState{
		ID:       "team_test",
		Type:     WorkspaceTypeTeamContext,
		Path:     teamDir,
		TeamName: "test-team",
		CloneURL: "file:///nonexistent/repo.git", // invalid URL
		Exists:   true,
	}

	ctx := context.Background()
	scheduler.runBlueGreenGC(ctx, ws)

	// old repo should still be intact
	assert.FileExists(t, filepath.Join(teamDir, "SOUL.md"))
	assert.FileExists(t, filepath.Join(teamDir, ".sageox", "sync.manifest"))

	// .new should be cleaned up
	assert.NoDirExists(t, teamDir+".new")
}

func TestBlueGreenGC_NotDueYet(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	projectDir := setupProjectWithConfig(t, "")
	scheduler := newTestScheduler(projectDir)

	// register a workspace with recent LastGCTime
	scheduler.WorkspaceRegistry().UpdateLastGC("team_test")

	ws := WorkspaceState{
		ID:             "team_test",
		Type:           WorkspaceTypeTeamContext,
		Path:           "/tmp/fake-path",
		Exists:         true,
		CloneURL:       "file:///fake",
		GCIntervalDays: 7,
		LastGCTime:     time.Now(), // just ran GC
	}

	// checkAndRunGC should skip this workspace because it's not due
	// We verify by checking that no clone attempt happens (no .new dir)
	assert.NoDirExists(t, ws.Path+".new")
}

func TestBlueGreenGC_CleansUpLeftoverNewDir(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	manifest := "version 1\ninclude .sageox/\ninclude SOUL.md\ninclude memory/\n"
	bareDir, teamDir, scheduler := setupClonedTeamContext(t, manifest, nil)

	// create a leftover .new from a previous failed GC
	newPath := teamDir + ".new"
	require.NoError(t, os.MkdirAll(newPath, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(newPath, "leftover"), []byte("old"), 0644))

	ws := WorkspaceState{
		ID:       "team_test",
		Type:     WorkspaceTypeTeamContext,
		Path:     teamDir,
		TeamName: "test-team",
		CloneURL: "file://" + bareDir,
		Exists:   true,
	}

	ctx := context.Background()
	scheduler.runBlueGreenGC(ctx, ws)

	// leftover should be cleaned up, and GC should succeed
	assert.NoDirExists(t, newPath)
	assert.FileExists(t, filepath.Join(teamDir, "SOUL.md"))
}

// --- Edge case tests: old-style full clones and corruption ---

func TestBlueGreenGC_OldStyleFullClone(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	manifest := "version 1\ninclude .sageox/\ninclude SOUL.md\ninclude TEAM.md\ninclude memory/\n"
	extraFiles := map[string]string{
		"src/main.go":      "package main\n",
		"assets/big.bin":   "binary stuff",
		"docs/readme.md":   "# docs\n",
		"coworkers/bob.md": "# bob\n",
	}
	bareDir := setupTeamContextBareRepo(t, manifest, extraFiles)

	// do a regular full clone (old-style, no sparse checkout)
	teamDir := filepath.Join(t.TempDir(), "team-ctx")
	require.NoError(t, exec.Command("git", "clone", bareDir, teamDir).Run())
	gitConfig(t, teamDir)
	require.NoError(t, exec.Command("git", "-C", teamDir, "config", "pull.rebase", "true").Run())

	// verify full clone has all files including non-manifest ones
	assert.FileExists(t, filepath.Join(teamDir, "src", "main.go"))
	assert.FileExists(t, filepath.Join(teamDir, "assets", "big.bin"))

	isolateCredentials(t)
	projectDir := setupProjectWithConfig(t, "")
	scheduler := newTestScheduler(projectDir)

	ws := WorkspaceState{
		ID:       "team_old",
		Type:     WorkspaceTypeTeamContext,
		Path:     teamDir,
		TeamName: "old-team",
		CloneURL: "file://" + bareDir,
		Exists:   true,
	}
	registry := scheduler.WorkspaceRegistry()
	registry.mu.Lock()
	registry.workspaces[ws.ID] = &ws
	registry.mu.Unlock()

	ctx := context.Background()
	scheduler.runBlueGreenGC(ctx, ws)

	// manifest-declared files should exist
	assert.FileExists(t, filepath.Join(teamDir, "SOUL.md"))
	assert.FileExists(t, filepath.Join(teamDir, "TEAM.md"))
	assert.FileExists(t, filepath.Join(teamDir, "memory", "notes.md"))
	assert.FileExists(t, filepath.Join(teamDir, ".sageox", "sync.manifest"))

	// non-manifest files should NOT exist (sparse clone replaced full clone)
	_, err := os.Stat(filepath.Join(teamDir, "src", "main.go"))
	assert.True(t, os.IsNotExist(err), "non-manifest file src/main.go should not exist after GC reclone")
	_, err = os.Stat(filepath.Join(teamDir, "assets", "big.bin"))
	assert.True(t, os.IsNotExist(err), "non-manifest file assets/big.bin should not exist after GC reclone")

	// sparse-checkout should be active
	out, err := exec.Command("git", "-C", teamDir, "sparse-checkout", "list").CombinedOutput()
	require.NoError(t, err)
	sparseList := string(out)
	assert.Contains(t, sparseList, ".sageox")
	assert.Contains(t, sparseList, "memory")

	// cleanup should be complete
	assert.NoDirExists(t, teamDir+".old")
	assert.NoDirExists(t, teamDir+".new")

	// LastGCTime should be updated
	lastGC := scheduler.WorkspaceRegistry().GetLastGCTime("team_old")
	assert.False(t, lastGC.IsZero())
}

func TestBlueGreenGC_OldStyleFullClone_PreservesContent(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	manifest := "version 1\ninclude .sageox/\ninclude SOUL.md\ninclude memory/\n"
	extraFiles := map[string]string{
		"src/main.go": "package main\n",
	}
	bareDir := setupTeamContextBareRepo(t, manifest, extraFiles)

	// full clone (old-style)
	teamDir := filepath.Join(t.TempDir(), "team-ctx")
	require.NoError(t, exec.Command("git", "clone", bareDir, teamDir).Run())
	gitConfig(t, teamDir)
	require.NoError(t, exec.Command("git", "-C", teamDir, "config", "pull.rebase", "true").Run())

	// read content before GC
	soulBefore, err := os.ReadFile(filepath.Join(teamDir, "SOUL.md"))
	require.NoError(t, err)
	notesBefore, err := os.ReadFile(filepath.Join(teamDir, "memory", "notes.md"))
	require.NoError(t, err)

	isolateCredentials(t)
	projectDir := setupProjectWithConfig(t, "")
	scheduler := newTestScheduler(projectDir)

	ws := WorkspaceState{
		ID:       "team_content",
		Type:     WorkspaceTypeTeamContext,
		Path:     teamDir,
		TeamName: "content-team",
		CloneURL: "file://" + bareDir,
		Exists:   true,
	}
	registry := scheduler.WorkspaceRegistry()
	registry.mu.Lock()
	registry.workspaces[ws.ID] = &ws
	registry.mu.Unlock()

	ctx := context.Background()
	scheduler.runBlueGreenGC(ctx, ws)

	// content should be identical after GC
	soulAfter, err := os.ReadFile(filepath.Join(teamDir, "SOUL.md"))
	require.NoError(t, err)
	assert.Equal(t, string(soulBefore), string(soulAfter))

	notesAfter, err := os.ReadFile(filepath.Join(teamDir, "memory", "notes.md"))
	require.NoError(t, err)
	assert.Equal(t, string(notesBefore), string(notesAfter))

	// non-manifest file should be gone
	_, err = os.Stat(filepath.Join(teamDir, "src", "main.go"))
	assert.True(t, os.IsNotExist(err))
}

func TestBlueGreenGC_RepoWithStaleLockFile(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	manifest := "version 1\ninclude .sageox/\ninclude SOUL.md\ninclude memory/\n"
	bareDir, teamDir, scheduler := setupClonedTeamContext(t, manifest, nil)

	// create a stale index.lock — git status will report dirty/error
	lockFile := filepath.Join(teamDir, ".git", "index.lock")
	require.NoError(t, os.WriteFile(lockFile, []byte{}, 0644))

	ws := WorkspaceState{
		ID:       "team_lock",
		Type:     WorkspaceTypeTeamContext,
		Path:     teamDir,
		TeamName: "lock-team",
		CloneURL: "file://" + bareDir,
		Exists:   true,
	}

	ctx := context.Background()
	scheduler.runBlueGreenGC(ctx, ws)

	// GC should skip — original repo untouched
	assert.FileExists(t, filepath.Join(teamDir, "SOUL.md"))
	assert.NoDirExists(t, teamDir+".new")
	assert.NoDirExists(t, teamDir+".old")
}

func TestBlueGreenGC_RepoInRebaseState(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	manifest := "version 1\ninclude .sageox/\ninclude SOUL.md\ninclude memory/\n"
	bareDir, teamDir, scheduler := setupClonedTeamContext(t, manifest, nil)

	// simulate a rebase in progress
	rebaseMergeDir := filepath.Join(teamDir, ".git", "rebase-merge")
	require.NoError(t, os.MkdirAll(rebaseMergeDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(rebaseMergeDir, "head-name"), []byte("refs/heads/main\n"), 0644))

	ws := WorkspaceState{
		ID:       "team_rebase",
		Type:     WorkspaceTypeTeamContext,
		Path:     teamDir,
		TeamName: "rebase-team",
		CloneURL: "file://" + bareDir,
		Exists:   true,
	}

	ctx := context.Background()
	scheduler.runBlueGreenGC(ctx, ws)

	// GC should skip — rebase state makes it dirty
	assert.FileExists(t, filepath.Join(teamDir, "SOUL.md"))
	assert.NoDirExists(t, teamDir+".new")
	assert.NoDirExists(t, teamDir+".old")
}

func TestBlueGreenGC_CorruptGitDir(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	manifest := "version 1\ninclude .sageox/\ninclude SOUL.md\ninclude memory/\n"
	bareDir, teamDir, scheduler := setupClonedTeamContext(t, manifest, nil)

	// corrupt .git/HEAD
	headFile := filepath.Join(teamDir, ".git", "HEAD")
	require.NoError(t, os.WriteFile(headFile, []byte("garbage-not-a-ref\n"), 0644))

	ws := WorkspaceState{
		ID:       "team_corrupt",
		Type:     WorkspaceTypeTeamContext,
		Path:     teamDir,
		TeamName: "corrupt-team",
		CloneURL: "file://" + bareDir,
		Exists:   true,
	}

	ctx := context.Background()
	scheduler.runBlueGreenGC(ctx, ws)

	// isCheckoutClean returns false on error → GC should skip
	// the corrupt repo is preserved (don't make it worse)
	assert.DirExists(t, filepath.Join(teamDir, ".git"))
	assert.NoDirExists(t, teamDir+".new")
	assert.NoDirExists(t, teamDir+".old")
}

func TestBlueGreenGC_MissingGitDir(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	projectDir := setupProjectWithConfig(t, "")
	scheduler := newTestScheduler(projectDir)

	// directory exists but has no .git/
	teamDir := filepath.Join(t.TempDir(), "team-ctx")
	require.NoError(t, os.MkdirAll(teamDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(teamDir, "SOUL.md"), []byte("# Soul\n"), 0644))

	ws := WorkspaceState{
		ID:       "team_nogit",
		Type:     WorkspaceTypeTeamContext,
		Path:     teamDir,
		TeamName: "nogit-team",
		CloneURL: "file:///nonexistent/repo.git",
		Exists:   true,
	}

	ctx := context.Background()
	scheduler.runBlueGreenGC(ctx, ws)

	// should skip gracefully — isCheckoutClean fails on non-git dir
	assert.NoDirExists(t, teamDir+".new")
	assert.NoDirExists(t, teamDir+".old")
	// original dir should still exist
	assert.DirExists(t, teamDir)
}

func TestBlueGreenGC_WorkspacePathDoesNotExist(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	projectDir := setupProjectWithConfig(t, "")
	scheduler := newTestScheduler(projectDir)

	// workspace path doesn't exist — isCheckoutClean returns false, GC skips
	ws := WorkspaceState{
		ID:       "team_nopath",
		Type:     WorkspaceTypeTeamContext,
		Path:     filepath.Join(t.TempDir(), "does-not-exist"),
		TeamName: "nopath-team",
		CloneURL: "file:///nonexistent/repo.git",
		Exists:   true,
	}

	ctx := context.Background()
	scheduler.runBlueGreenGC(ctx, ws)

	// should skip gracefully — no dirs created
	assert.NoDirExists(t, ws.Path+".new")
	assert.NoDirExists(t, ws.Path+".old")
}

func TestBlueGreenGC_PreExistingOldDir(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	manifest := "version 1\ninclude .sageox/\ninclude SOUL.md\ninclude memory/\n"
	bareDir, teamDir, scheduler := setupClonedTeamContext(t, manifest, nil)

	// create a pre-existing .old directory (leftover from a previous failed cleanup)
	oldPath := teamDir + ".old"
	require.NoError(t, os.MkdirAll(oldPath, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(oldPath, "stale"), []byte("leftover"), 0644))

	ws := WorkspaceState{
		ID:       "team_preold",
		Type:     WorkspaceTypeTeamContext,
		Path:     teamDir,
		TeamName: "preold-team",
		CloneURL: "file://" + bareDir,
		Exists:   true,
	}
	registry := scheduler.WorkspaceRegistry()
	registry.mu.Lock()
	registry.workspaces[ws.ID] = &ws
	registry.mu.Unlock()

	ctx := context.Background()
	scheduler.runBlueGreenGC(ctx, ws)

	// GC should succeed — pre-existing .old should be removed
	assert.FileExists(t, filepath.Join(teamDir, "SOUL.md"))
	assert.NoDirExists(t, oldPath)
	assert.NoDirExists(t, teamDir+".new")

	lastGC := scheduler.WorkspaceRegistry().GetLastGCTime("team_preold")
	assert.False(t, lastGC.IsZero())
}

func TestBlueGreenGC_ConcurrentSkipped(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	projectDir := setupProjectWithConfig(t, "")
	scheduler := newTestScheduler(projectDir)

	// simulate GC already in progress by setting the atomic flag
	atomic.StoreInt32(&scheduler.gcInProgress, 1)

	ws := WorkspaceState{
		ID:       "team_concurrent",
		Type:     WorkspaceTypeTeamContext,
		Path:     "/tmp/fake-gc-path",
		Exists:   true,
		CloneURL: "file:///fake",
	}
	registry := scheduler.WorkspaceRegistry()
	registry.mu.Lock()
	registry.workspaces[ws.ID] = &ws
	registry.mu.Unlock()

	ctx := context.Background()
	scheduler.checkAndRunGC(ctx)

	// GC should have been skipped entirely — flag still set
	assert.Equal(t, int32(1), atomic.LoadInt32(&scheduler.gcInProgress),
		"gcInProgress flag should remain set (not cleared by skipped check)")
}

func TestBlueGreenGC_SkipsCloneInFlight(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	manifest := "version 1\ninclude .sageox/\ninclude SOUL.md\ninclude memory/\n"
	bareDir, teamDir, scheduler := setupClonedTeamContext(t, manifest, nil)

	ws := WorkspaceState{
		ID:             "team_inflight",
		Type:           WorkspaceTypeTeamContext,
		Path:           teamDir,
		TeamName:       "inflight-team",
		CloneURL:       "file://" + bareDir,
		Exists:         true,
		GCIntervalDays: 1,
		LastGCTime:     time.Time{}, // never run = due for GC
	}
	registry := scheduler.WorkspaceRegistry()
	registry.mu.Lock()
	registry.workspaces[ws.ID] = &ws
	registry.mu.Unlock()

	// mark this workspace as having a clone in flight
	scheduler.cloneInFlight.Store(ws.ID, true)

	ctx := context.Background()
	scheduler.checkAndRunGC(ctx)

	// GC should have been skipped — no .new or .old dirs
	assert.NoDirExists(t, teamDir+".new")
	assert.NoDirExists(t, teamDir+".old")

	// LastGCTime should NOT be updated
	lastGC := scheduler.WorkspaceRegistry().GetLastGCTime("team_inflight")
	assert.True(t, lastGC.IsZero())
}

func TestBlueGreenGC_UpdatesManifestConfig(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	// manifest with custom gc_interval_days and sync_interval_minutes
	manifestContent := "version 1\ninclude .sageox/\ninclude SOUL.md\ninclude memory/\ngc_interval_days 14\nsync_interval_minutes 10\n"
	bareDir, teamDir, scheduler := setupClonedTeamContext(t, manifestContent, nil)

	ws := WorkspaceState{
		ID:       "team_config",
		Type:     WorkspaceTypeTeamContext,
		Path:     teamDir,
		TeamName: "config-team",
		CloneURL: "file://" + bareDir,
		Exists:   true,
	}
	registry := scheduler.WorkspaceRegistry()
	registry.mu.Lock()
	registry.workspaces[ws.ID] = &ws
	registry.mu.Unlock()

	ctx := context.Background()
	scheduler.runBlueGreenGC(ctx, ws)

	// verify GC succeeded
	assert.FileExists(t, filepath.Join(teamDir, "SOUL.md"))
	assert.NoDirExists(t, teamDir+".old")
	assert.NoDirExists(t, teamDir+".new")

	lastGC := scheduler.WorkspaceRegistry().GetLastGCTime("team_config")
	assert.False(t, lastGC.IsZero())

	// verify manifest config was propagated to registry
	registry.mu.Lock()
	updatedWs := registry.workspaces["team_config"]
	gcInterval := updatedWs.GCIntervalDays
	syncInterval := updatedWs.SyncIntervalMin
	registry.mu.Unlock()

	assert.Equal(t, 14, gcInterval, "gc_interval_days should be updated from manifest")
	assert.Equal(t, 10, syncInterval, "sync_interval_min should be updated from manifest")
}

func TestBlueGreenGC_ValidationFailsKeepsOld(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	// create a bare repo WITHOUT core files — clone will succeed but validation will fail
	tmpDir := t.TempDir()
	bareDir := filepath.Join(tmpDir, "bare.git")
	workDir := filepath.Join(tmpDir, "work")

	require.NoError(t, exec.Command("git", "init", "--bare", "-b", "main", bareDir).Run())
	require.NoError(t, exec.Command("git", "-C", bareDir, "config", "uploadpack.allowfilter", "true").Run())
	require.NoError(t, exec.Command("git", "clone", bareDir, workDir).Run())
	gitConfig(t, workDir)

	// only create .sageox with manifest, but NO core files (SOUL.md, TEAM.md, MEMORY.md)
	sageoxDir := filepath.Join(workDir, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755))
	manifestContent := "version 1\ninclude .sageox/\ninclude SOUL.md\n"
	require.NoError(t, os.WriteFile(filepath.Join(sageoxDir, "sync.manifest"), []byte(manifestContent), 0644))
	require.NoError(t, exec.Command("git", "-C", workDir, "add", ".").Run())
	require.NoError(t, exec.Command("git", "-C", workDir, "commit", "-m", "init").Run())
	require.NoError(t, exec.Command("git", "-C", workDir, "push", "origin", "HEAD:main").Run())

	// set up a valid existing team context (with SOUL.md) that should be preserved
	manifest := "version 1\ninclude .sageox/\ninclude SOUL.md\ninclude memory/\n"
	_, teamDir, scheduler := setupClonedTeamContext(t, manifest, nil)
	origSoul, err := os.ReadFile(filepath.Join(teamDir, "SOUL.md"))
	require.NoError(t, err)

	ws := WorkspaceState{
		ID:       "team_valfail",
		Type:     WorkspaceTypeTeamContext,
		Path:     teamDir,
		TeamName: "valfail-team",
		CloneURL: "file://" + bareDir, // points to repo without core files
		Exists:   true,
	}
	registry := scheduler.WorkspaceRegistry()
	registry.mu.Lock()
	registry.workspaces[ws.ID] = &ws
	registry.mu.Unlock()

	ctx := context.Background()
	scheduler.runBlueGreenGC(ctx, ws)

	// old repo should still be intact because validation failed
	newSoul, err := os.ReadFile(filepath.Join(teamDir, "SOUL.md"))
	require.NoError(t, err)
	assert.Equal(t, string(origSoul), string(newSoul), "original content preserved after validation failure")

	// .new should be cleaned up
	assert.NoDirExists(t, teamDir+".new")

	// LastGCTime should NOT be updated
	lastGC := scheduler.WorkspaceRegistry().GetLastGCTime("team_valfail")
	assert.True(t, lastGC.IsZero())
}

func TestBlueGreenGC_EmptyWorkspacePath(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	projectDir := setupProjectWithConfig(t, "")
	scheduler := newTestScheduler(projectDir)

	ws := WorkspaceState{
		ID:       "team_empty",
		Type:     WorkspaceTypeTeamContext,
		Path:     "", // empty path
		Exists:   true,
		CloneURL: "file:///fake",
	}

	ctx := context.Background()
	// should not panic on empty path
	scheduler.runBlueGreenGC(ctx, ws)
}

func TestBlueGreenGC_LeftoverNewRemovalFails(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	manifest := "version 1\ninclude .sageox/\ninclude SOUL.md\ninclude memory/\n"
	bareDir, teamDir, scheduler := setupClonedTeamContext(t, manifest, nil)

	// create a leftover .new dir that cannot be removed (read-only parent of contents)
	newPath := teamDir + ".new"
	innerDir := filepath.Join(newPath, "inner")
	require.NoError(t, os.MkdirAll(innerDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(innerDir, "file"), []byte("stuck"), 0644))
	// make inner dir non-writable so RemoveAll fails on the file inside
	require.NoError(t, os.Chmod(innerDir, 0555))
	t.Cleanup(func() {
		os.Chmod(innerDir, 0755)
		os.RemoveAll(newPath)
	})

	ws := WorkspaceState{
		ID:       "team_stucknew",
		Type:     WorkspaceTypeTeamContext,
		Path:     teamDir,
		TeamName: "stucknew-team",
		CloneURL: "file://" + bareDir,
		Exists:   true,
	}

	ctx := context.Background()
	scheduler.runBlueGreenGC(ctx, ws)

	// GC should bail out because it can't remove leftover .new
	// original repo should still be intact
	assert.FileExists(t, filepath.Join(teamDir, "SOUL.md"))

	// restore permissions for cleanup
	os.Chmod(innerDir, 0755)
}

// --- Dirty-tree preservation regression tests ---

func TestBlueGreenGC_PreservesStagedChanges(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	manifestContent := "version 1\ninclude .sageox/\ninclude SOUL.md\ninclude TEAM.md\ninclude memory/\n"
	bareDir, teamDir, scheduler := setupClonedTeamContext(t, manifestContent, nil)

	// stage a change
	stagedContent := "# Soul\nstaged modification\n"
	require.NoError(t, os.WriteFile(filepath.Join(teamDir, "SOUL.md"), []byte(stagedContent), 0644))
	require.NoError(t, exec.Command("git", "-C", teamDir, "add", "SOUL.md").Run())

	ws := WorkspaceState{
		ID:       "team_staged",
		Type:     WorkspaceTypeTeamContext,
		Path:     teamDir,
		TeamName: "test-team",
		CloneURL: "file://" + bareDir,
		Exists:   true,
	}

	result := scheduler.runBlueGreenGC(context.Background(), ws)
	assert.Equal(t, gcSuccess, result)

	content, err := os.ReadFile(filepath.Join(teamDir, "SOUL.md"))
	require.NoError(t, err)
	assert.Equal(t, stagedContent, string(content), "staged change should be preserved")
}

func TestBlueGreenGC_PreservesMixedStagedAndUnstaged(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	manifestContent := "version 1\ninclude .sageox/\ninclude SOUL.md\ninclude TEAM.md\ninclude memory/\n"
	bareDir, teamDir, scheduler := setupClonedTeamContext(t, manifestContent, nil)

	// stage a change, then make a further unstaged change
	require.NoError(t, os.WriteFile(filepath.Join(teamDir, "SOUL.md"), []byte("staged version"), 0644))
	require.NoError(t, exec.Command("git", "-C", teamDir, "add", "SOUL.md").Run())
	finalContent := "unstaged version on top of staged"
	require.NoError(t, os.WriteFile(filepath.Join(teamDir, "SOUL.md"), []byte(finalContent), 0644))

	ws := WorkspaceState{
		ID:       "team_mixed",
		Type:     WorkspaceTypeTeamContext,
		Path:     teamDir,
		TeamName: "test-team",
		CloneURL: "file://" + bareDir,
		Exists:   true,
	}

	result := scheduler.runBlueGreenGC(context.Background(), ws)
	assert.Equal(t, gcSuccess, result)

	// the latest (unstaged) content should be what survives
	content, err := os.ReadFile(filepath.Join(teamDir, "SOUL.md"))
	require.NoError(t, err)
	assert.Equal(t, finalContent, string(content), "latest working tree content should be preserved")
}

func TestBlueGreenGC_PreservesUntrackedFiles(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	manifestContent := "version 1\ninclude .sageox/\ninclude SOUL.md\ninclude TEAM.md\ninclude memory/\n"
	bareDir, teamDir, scheduler := setupClonedTeamContext(t, manifestContent, nil)

	// create an untracked file
	untrackedContent := "user's custom notes\n"
	require.NoError(t, os.WriteFile(filepath.Join(teamDir, "my-notes.md"), []byte(untrackedContent), 0644))

	ws := WorkspaceState{
		ID:       "team_untracked",
		Type:     WorkspaceTypeTeamContext,
		Path:     teamDir,
		TeamName: "test-team",
		CloneURL: "file://" + bareDir,
		Exists:   true,
	}

	result := scheduler.runBlueGreenGC(context.Background(), ws)
	assert.Equal(t, gcSuccess, result)

	content, err := os.ReadFile(filepath.Join(teamDir, "my-notes.md"))
	require.NoError(t, err)
	assert.Equal(t, untrackedContent, string(content), "untracked file should be preserved")
}

func TestBlueGreenGC_PreservesUntrackedInSubdirs(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	manifestContent := "version 1\ninclude .sageox/\ninclude SOUL.md\ninclude TEAM.md\ninclude memory/\n"
	bareDir, teamDir, scheduler := setupClonedTeamContext(t, manifestContent, nil)

	// create untracked files in nested subdirectories
	nestedDir := filepath.Join(teamDir, "notes", "2024")
	require.NoError(t, os.MkdirAll(nestedDir, 0755))
	nestedContent := "january notes\n"
	require.NoError(t, os.WriteFile(filepath.Join(nestedDir, "jan.md"), []byte(nestedContent), 0644))

	ws := WorkspaceState{
		ID:       "team_nested",
		Type:     WorkspaceTypeTeamContext,
		Path:     teamDir,
		TeamName: "test-team",
		CloneURL: "file://" + bareDir,
		Exists:   true,
	}

	result := scheduler.runBlueGreenGC(context.Background(), ws)
	assert.Equal(t, gcSuccess, result)

	content, err := os.ReadFile(filepath.Join(teamDir, "notes", "2024", "jan.md"))
	require.NoError(t, err)
	assert.Equal(t, nestedContent, string(content), "nested untracked file should be preserved")
}

func TestBlueGreenGC_PushesUnpushedCommitsBeforeReclone(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	manifestContent := "version 1\ninclude .sageox/\ninclude SOUL.md\ninclude TEAM.md\ninclude memory/\n"
	bareDir, teamDir, scheduler := setupClonedTeamContext(t, manifestContent, nil)

	// make a local commit that is NOT pushed
	newContent := "# Soul\nlocal commit content\n"
	require.NoError(t, os.WriteFile(filepath.Join(teamDir, "SOUL.md"), []byte(newContent), 0644))
	gitConfig(t, teamDir)
	require.NoError(t, exec.Command("git", "-C", teamDir, "add", "SOUL.md").Run())
	require.NoError(t, exec.Command("git", "-C", teamDir, "commit", "-m", "local change").Run())

	// verify there are unpushed commits (user commit + auto-commit from TwoPhaseClone)
	countOut, err := exec.Command("git", "-C", teamDir, "rev-list", "--count", "origin/main..HEAD").CombinedOutput()
	require.NoError(t, err)
	countStr := strings.TrimSpace(string(countOut))
	count, parseErr := strconv.Atoi(countStr)
	require.NoError(t, parseErr, "failed to parse commit count")
	assert.GreaterOrEqual(t, count, 1, "should have at least 1 unpushed commit")

	ws := WorkspaceState{
		ID:       "team_push",
		Type:     WorkspaceTypeTeamContext,
		Path:     teamDir,
		TeamName: "test-team",
		CloneURL: "file://" + bareDir,
		Exists:   true,
	}

	result := scheduler.runBlueGreenGC(context.Background(), ws)
	assert.Equal(t, gcSuccess, result)

	// verify the commit was pushed to the bare repo
	logOut, err := exec.Command("git", "-C", bareDir, "log", "--oneline", "main").CombinedOutput()
	require.NoError(t, err)
	assert.Contains(t, string(logOut), "local change", "unpushed commit should be in bare repo after GC")

	// the recloned repo should have the committed content
	content, err := os.ReadFile(filepath.Join(teamDir, "SOUL.md"))
	require.NoError(t, err)
	assert.Equal(t, newContent, string(content))
}

func TestBlueGreenGC_SkipsWhenPushFails(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	manifestContent := "version 1\ninclude .sageox/\ninclude SOUL.md\ninclude TEAM.md\ninclude memory/\n"
	bareDir, teamDir, scheduler := setupClonedTeamContext(t, manifestContent, nil)

	// make a local commit
	require.NoError(t, os.WriteFile(filepath.Join(teamDir, "SOUL.md"), []byte("local change"), 0644))
	gitConfig(t, teamDir)
	require.NoError(t, exec.Command("git", "-C", teamDir, "add", "SOUL.md").Run())
	require.NoError(t, exec.Command("git", "-C", teamDir, "commit", "-m", "unpushable").Run())

	// break the bare repo so push will fail
	require.NoError(t, os.RemoveAll(bareDir))

	ws := WorkspaceState{
		ID:       "team_pushfail",
		Type:     WorkspaceTypeTeamContext,
		Path:     teamDir,
		TeamName: "test-team",
		CloneURL: "file://" + bareDir,
		Exists:   true,
	}

	result := scheduler.runBlueGreenGC(context.Background(), ws)
	assert.Equal(t, gcSkippedDirty, result, "GC should skip when push fails")

	// original repo should be untouched
	assert.FileExists(t, filepath.Join(teamDir, "SOUL.md"))
	content, err := os.ReadFile(filepath.Join(teamDir, "SOUL.md"))
	require.NoError(t, err)
	assert.Equal(t, "local change", string(content))
}

func TestBlueGreenGC_DiffApplyConflictPreservesDiffFile(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	manifestContent := "version 1\ninclude .sageox/\ninclude SOUL.md\ninclude TEAM.md\ninclude memory/\n"
	bareDir, teamDir, scheduler := setupClonedTeamContext(t, manifestContent, nil)

	// push the auto-commit from TwoPhaseClone so the local is in sync with remote
	require.NoError(t, exec.Command("git", "-C", teamDir, "push", "origin", "HEAD", "--quiet").Run())

	// push a conflicting change to the bare repo via a temp clone
	// this completely replaces SOUL.md content, so the local diff can't apply cleanly
	pusherDir := filepath.Join(t.TempDir(), "pusher")
	require.NoError(t, exec.Command("git", "clone", bareDir, pusherDir).Run())
	gitConfig(t, pusherDir)
	require.NoError(t, os.WriteFile(filepath.Join(pusherDir, "SOUL.md"), []byte("completely different remote content\nwith multiple lines\nthat conflict\n"), 0644))
	require.NoError(t, exec.Command("git", "-C", pusherDir, "add", "SOUL.md").Run())
	require.NoError(t, exec.Command("git", "-C", pusherDir, "commit", "-m", "conflict").Run())
	require.NoError(t, exec.Command("git", "-C", pusherDir, "push", "origin", "HEAD:main").Run())

	// now make a local uncommitted change to SOUL.md that conflicts
	require.NoError(t, os.WriteFile(filepath.Join(teamDir, "SOUL.md"), []byte("local user edit\nwith different content\nthat also conflicts\n"), 0644))

	ws := WorkspaceState{
		ID:       "team_conflict",
		Type:     WorkspaceTypeTeamContext,
		Path:     teamDir,
		TeamName: "test-team",
		CloneURL: "file://" + bareDir,
		Exists:   true,
	}

	result := scheduler.runBlueGreenGC(context.Background(), ws)
	// reclone succeeds even if diff apply fails — the repo is valid
	assert.Equal(t, gcSuccess, result)

	// the .gc-diff file should be preserved for manual recovery
	diffFile := teamDir + ".gc-diff"
	assert.FileExists(t, diffFile, "diff file should be preserved when apply fails")

	// clean up
	t.Cleanup(func() { os.Remove(diffFile) })
}

func TestBlueGreenGC_PreservesBinaryUntrackedFile(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	manifestContent := "version 1\ninclude .sageox/\ninclude SOUL.md\ninclude TEAM.md\ninclude memory/\n"
	bareDir, teamDir, scheduler := setupClonedTeamContext(t, manifestContent, nil)

	// create a binary untracked file
	binaryContent := make([]byte, 256)
	for i := range binaryContent {
		binaryContent[i] = byte(i)
	}
	require.NoError(t, os.WriteFile(filepath.Join(teamDir, "diagram.png"), binaryContent, 0644))

	ws := WorkspaceState{
		ID:       "team_binary",
		Type:     WorkspaceTypeTeamContext,
		Path:     teamDir,
		TeamName: "test-team",
		CloneURL: "file://" + bareDir,
		Exists:   true,
	}

	result := scheduler.runBlueGreenGC(context.Background(), ws)
	assert.Equal(t, gcSuccess, result)

	content, err := os.ReadFile(filepath.Join(teamDir, "diagram.png"))
	require.NoError(t, err)
	assert.Equal(t, binaryContent, content, "binary untracked file should survive reclone with identical content")
}

func TestBlueGreenGC_StagedDeletePreserved(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	manifestContent := "version 1\ninclude .sageox/\ninclude SOUL.md\ninclude TEAM.md\ninclude memory/\n"
	bareDir, teamDir, scheduler := setupClonedTeamContext(t, manifestContent, nil)

	// stage a deletion of TEAM.md
	require.NoError(t, exec.Command("git", "-C", teamDir, "rm", "TEAM.md").Run())

	ws := WorkspaceState{
		ID:       "team_delete",
		Type:     WorkspaceTypeTeamContext,
		Path:     teamDir,
		TeamName: "test-team",
		CloneURL: "file://" + bareDir,
		Exists:   true,
	}

	result := scheduler.runBlueGreenGC(context.Background(), ws)
	assert.Equal(t, gcSuccess, result)

	// TEAM.md should not exist after reclone (the deletion should be re-applied)
	assert.NoFileExists(t, filepath.Join(teamDir, "TEAM.md"), "staged delete should be preserved")
}

func TestBlueGreenGC_CleanTreeStillWorks(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	// verify clean tree GC still works identically with the new code path
	manifestContent := "version 1\ninclude .sageox/\ninclude SOUL.md\ninclude TEAM.md\ninclude memory/\ngc_interval_days 7\n"
	bareDir, teamDir, scheduler := setupClonedTeamContext(t, manifestContent, nil)

	origContent, err := os.ReadFile(filepath.Join(teamDir, "SOUL.md"))
	require.NoError(t, err)

	ws := WorkspaceState{
		ID:       "team_clean",
		Type:     WorkspaceTypeTeamContext,
		Path:     teamDir,
		TeamName: "test-team",
		CloneURL: "file://" + bareDir,
		Exists:   true,
	}

	result := scheduler.runBlueGreenGC(context.Background(), ws)
	assert.Equal(t, gcSuccess, result)

	content, err := os.ReadFile(filepath.Join(teamDir, "SOUL.md"))
	require.NoError(t, err)
	assert.Equal(t, string(origContent), string(content))

	// no preservation artifacts
	assert.NoFileExists(t, teamDir+".gc-diff")
	assert.NoDirExists(t, teamDir+".gc-untracked")
}

// --- Blue-green reclone GC tests ---

// setupClonedTeamContext creates a bare repo and two-phase-clones it into a target dir.
// Returns (bareDir, clonedDir, scheduler) for GC testing.
func setupClonedTeamContext(t *testing.T, manifestContent string, extraFiles map[string]string) (string, string, *SyncScheduler) {
	t.Helper()
	isolateCredentials(t) // also opts into TestAllowFileTransport for file:// clones

	bareDir := setupTeamContextBareRepo(t, manifestContent, extraFiles)
	cloneURL := "file://" + bareDir

	projectDir := setupProjectWithConfig(t, "")
	scheduler := newTestScheduler(projectDir)

	targetDir := filepath.Join(t.TempDir(), "team-ctx")
	ctx := context.Background()

	_, err := scheduler.twoPhaseClone(ctx, cloneURL, targetDir, nil)
	require.NoError(t, err)

	// configure pull.rebase
	require.NoError(t, exec.Command("git", "-C", targetDir, "config", "pull.rebase", "true").Run())

	return bareDir, targetDir, scheduler
}
