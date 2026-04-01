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

// --- A. Leftover GC artifacts from a previous crash ---

// TestGC_LeftoverArtifacts_CleanedBeforeReclone verifies that stale artifacts
// from a previous GC crash (.new, .old, .gc-lock, .gc-diff, .gc-untracked)
// are cleaned up before the next GC proceeds.
// Failure prevented: leftover .gc-lock from a crashed GC permanently blocks
// all future GC cycles; leftover .new/.old waste disk and confuse the swap.
func TestGC_LeftoverArtifacts_CleanedBeforeReclone(t *testing.T) {
	if testing.Short() {
		t.Skip("short: Gitea digital twin")
	}

	g := getSharedGitea(t)
	cloneURL := g.createRepo(t, "gc-leftover-artifacts")

	cloneDir := filepath.Join(t.TempDir(), "repo")
	g.cloneRepo(t, cloneURL, cloneDir)

	// add ledger structure
	require.NoError(t, os.MkdirAll(filepath.Join(cloneDir, "sessions"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cloneDir, "sessions", ".gitkeep"), []byte(""), 0o644))
	cmd := exec.Command("git", "-C", cloneDir, "add", "sessions/.gitkeep")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git add failed: %s", string(out))
	cmd = exec.Command("git", "-C", cloneDir, "commit", "-m", "add ledger structure")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "git commit failed: %s", string(out))
	cmd = exec.Command("git", "-C", cloneDir, "push")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "git push failed: %s", string(out))

	// create fake leftover artifacts from a "crashed" previous GC
	newDir := cloneDir + ".new"
	require.NoError(t, os.MkdirAll(newDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(newDir, "partial-clone"), []byte("stale"), 0o644))

	oldDir := cloneDir + ".old"
	require.NoError(t, os.MkdirAll(oldDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(oldDir, "old-repo"), []byte("stale"), 0o644))

	lockPath := cloneDir + ".gc-lock"
	require.NoError(t, os.WriteFile(lockPath, []byte("stale"), 0o644))
	// set mtime to 10 minutes ago so the lock is considered stale
	staleTime := time.Now().Add(-10 * time.Minute)
	require.NoError(t, os.Chtimes(lockPath, staleTime, staleTime))

	diffFile := cloneDir + ".gc-diff"
	require.NoError(t, os.WriteFile(diffFile, []byte("old diff"), 0o644))

	untrackedDir := cloneDir + ".gc-untracked"
	require.NoError(t, os.MkdirAll(untrackedDir, 0o755))

	// run GC
	isolateCredentials(t)
	projectDir := setupProjectWithConfig(t, "")
	s := newTestScheduler(projectDir)

	ws := WorkspaceState{
		ID:       "leftover-artifacts",
		Type:     WorkspaceTypeLedger,
		Path:     cloneDir,
		CloneURL: cloneURL,
		Exists:   true,
	}

	result := s.runBlueGreenGC(context.Background(), ws)
	assert.Equal(t, gcSuccess, result, "GC should succeed after cleaning leftover artifacts")

	// all leftover artifacts should be gone
	for _, artifact := range []string{newDir, oldDir, lockPath, diffFile, untrackedDir} {
		_, statErr := os.Stat(artifact)
		assert.True(t, os.IsNotExist(statErr),
			"leftover artifact should be cleaned up: %s", artifact)
	}

	// repo should be valid and contain committed content
	_, err = os.Stat(filepath.Join(cloneDir, ".git", "HEAD"))
	require.NoError(t, err, ".git/HEAD should exist after GC")

	_, err = os.Stat(filepath.Join(cloneDir, "sessions", ".gitkeep"))
	require.NoError(t, err, "sessions/.gitkeep should exist after GC")
}

// --- B. Empty working tree (rogue agent deleted everything) ---

// TestGC_EmptyRepo_HandlesGracefully verifies that if a rogue coding agent
// deletes all working tree files (keeping only .git), GC reclones from
// remote and restores the committed content.
// Failure prevented: empty working tree causes GC diff/untracked capture
// to fail or reclone validation to reject the repo.
func TestGC_EmptyRepo_HandlesGracefully(t *testing.T) {
	if testing.Short() {
		t.Skip("short: Gitea digital twin")
	}

	g := getSharedGitea(t)
	cloneURL := g.createRepo(t, "gc-empty-worktree")

	cloneDir := filepath.Join(t.TempDir(), "repo")
	g.cloneRepo(t, cloneURL, cloneDir)

	// add ledger structure and extra files, commit, push
	require.NoError(t, os.MkdirAll(filepath.Join(cloneDir, "sessions"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cloneDir, "sessions", ".gitkeep"), []byte(""), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(cloneDir, "data.txt"), []byte("important data"), 0o644))
	cmd := exec.Command("git", "-C", cloneDir, "add", "sessions/.gitkeep", "data.txt")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git add failed: %s", string(out))
	cmd = exec.Command("git", "-C", cloneDir, "commit", "-m", "add ledger structure and data")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "git commit failed: %s", string(out))
	cmd = exec.Command("git", "-C", cloneDir, "push")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "git push failed: %s", string(out))

	// remove all working tree files except .git
	entries, err := os.ReadDir(cloneDir)
	require.NoError(t, err)
	for _, entry := range entries {
		if entry.Name() == ".git" {
			continue
		}
		require.NoError(t, os.RemoveAll(filepath.Join(cloneDir, entry.Name())))
	}

	// verify working tree is empty (only .git remains)
	entries, err = os.ReadDir(cloneDir)
	require.NoError(t, err)
	require.Len(t, entries, 1, "only .git should remain")
	require.Equal(t, ".git", entries[0].Name())

	// run GC
	isolateCredentials(t)
	projectDir := setupProjectWithConfig(t, "")
	s := newTestScheduler(projectDir)

	ws := WorkspaceState{
		ID:       "empty-worktree",
		Type:     WorkspaceTypeLedger,
		Path:     cloneDir,
		CloneURL: cloneURL,
		Exists:   true,
	}

	result := s.runBlueGreenGC(context.Background(), ws)
	assert.Equal(t, gcSuccess, result, "GC should succeed on empty working tree")

	// committed files should be restored from remote
	_, err = os.Stat(filepath.Join(cloneDir, ".git", "HEAD"))
	require.NoError(t, err, ".git/HEAD should exist after GC")

	_, err = os.Stat(filepath.Join(cloneDir, "sessions", ".gitkeep"))
	require.NoError(t, err, "sessions/.gitkeep should be restored from remote")

	content, err := os.ReadFile(filepath.Join(cloneDir, "data.txt"))
	require.NoError(t, err, "data.txt should be restored from remote")
	assert.Equal(t, "important data", string(content))

	_, err = os.Stat(filepath.Join(cloneDir, "README.md"))
	require.NoError(t, err, "README.md (from auto-init) should be restored from remote")
}

// --- C. Empty remote (only auto-init commit) ---

// TestSync_EmptyRemote_PullSucceeds verifies that pulling from a remote with
// only the auto-init commit (just README.md) works without error.
// Failure prevented: pull on a fresh repo with no new commits returns
// an unexpected error instead of a clean skip/success.
func TestSync_EmptyRemote_PullSucceeds(t *testing.T) {
	if testing.Short() {
		t.Skip("short: Gitea digital twin")
	}

	g := getSharedGitea(t)
	cloneURL := g.createRepo(t, "sync-empty-remote")

	cloneDir := filepath.Join(t.TempDir(), "repo")
	g.cloneRepo(t, cloneURL, cloneDir)

	// pull immediately — nothing new to pull
	isolateCredentials(t)
	projectDir := setupProjectWithConfig(t, "")
	s := newTestScheduler(projectDir)

	opts := ManagedRepoPullOpts{
		RepoPath:     cloneDir,
		RepoName:     "test-empty-remote",
		ProjectRoot:  projectDir,
		SyncInterval: time.Second,
		MinFetchAge:  0,
		Logger:       slog.Default(),
	}

	result := s.pullManagedRepo(context.Background(), opts)

	// pull should either skip (nothing to pull) or succeed — no error
	assert.NoError(t, result.Err, "pull on empty remote should not error")

	// repo should remain valid
	cmd := exec.Command("git", "-C", cloneDir, "rev-parse", "--git-dir")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "repo should be valid after pull: %s", string(out))
}

// --- D. Remote unavailable during GC reclone ---

// TestGC_RemoteUnavailable_FailsGracefully verifies that if the remote is
// unreachable during GC, the reclone fails cleanly and the original repo
// is left completely intact — committed files, dirty state, and cache.
// Failure prevented: failed clone deletes the original repo before the
// swap, leaving the user with nothing.
func TestGC_RemoteUnavailable_FailsGracefully(t *testing.T) {
	if testing.Short() {
		t.Skip("short: Gitea digital twin")
	}

	g := getSharedGitea(t)
	cloneURL := g.createRepo(t, "gc-remote-unavail")

	cloneDir := filepath.Join(t.TempDir(), "repo")
	g.cloneRepo(t, cloneURL, cloneDir)

	// add ledger structure, commit, push
	require.NoError(t, os.MkdirAll(filepath.Join(cloneDir, "sessions"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cloneDir, "sessions", ".gitkeep"), []byte(""), 0o644))
	cmd := exec.Command("git", "-C", cloneDir, "add", "sessions/.gitkeep")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git add failed: %s", string(out))
	cmd = exec.Command("git", "-C", cloneDir, "commit", "-m", "add ledger structure")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "git commit failed: %s", string(out))
	cmd = exec.Command("git", "-C", cloneDir, "push")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "git push failed: %s", string(out))

	// add local state: dirty file
	require.NoError(t, os.WriteFile(filepath.Join(cloneDir, "README.md"), []byte("dirty-local-edit"), 0o644))

	// add local state: cache
	cacheDir := filepath.Join(cloneDir, ".sageox", "cache", "test")
	require.NoError(t, os.MkdirAll(cacheDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cacheDir, "data.db"), []byte("cache-content"), 0o644))

	// run GC with a non-existent remote
	isolateCredentials(t)
	projectDir := setupProjectWithConfig(t, "")
	s := newTestScheduler(projectDir)

	ws := WorkspaceState{
		ID:       "remote-unavail",
		Type:     WorkspaceTypeLedger,
		Path:     cloneDir,
		CloneURL: "http://localhost:1/nonexistent/repo.git",
		Exists:   true,
	}

	result := s.runBlueGreenGC(context.Background(), ws)
	assert.Equal(t, gcFailed, result, "GC should fail when remote is unavailable")

	// original repo should be completely intact
	_, err = os.Stat(filepath.Join(cloneDir, ".git", "HEAD"))
	require.NoError(t, err, ".git/HEAD should still exist after failed GC")

	_, err = os.Stat(filepath.Join(cloneDir, "sessions", ".gitkeep"))
	require.NoError(t, err, "sessions/.gitkeep should still exist after failed GC")

	dirtyContent, err := os.ReadFile(filepath.Join(cloneDir, "README.md"))
	require.NoError(t, err, "README.md should still exist after failed GC")
	assert.Equal(t, "dirty-local-edit", string(dirtyContent),
		"dirty state should be preserved after failed GC")

	cacheContent, err := os.ReadFile(filepath.Join(cacheDir, "data.db"))
	require.NoError(t, err, "cache should still exist after failed GC")
	assert.Equal(t, "cache-content", string(cacheContent),
		"cache should be preserved after failed GC")

	// no leftover temp directories
	_, err = os.Stat(cloneDir + ".new")
	assert.True(t, os.IsNotExist(err), ".new should not exist after failed GC")
	_, err = os.Stat(cloneDir + ".old")
	assert.True(t, os.IsNotExist(err), ".old should not exist after failed GC")
}

// --- E. Dirty file conflicts with remote content ---

// TestGC_DirtyFileConflictsWithRemote_PreservedForRecovery verifies that
// local uncommitted changes to a file that was also modified remotely are
// either applied after GC or preserved in a recovery artifact.
// Failure prevented: local edits silently lost during GC reclone when the
// same file was modified on the remote — coworker loses in-progress work.
func TestGC_DirtyFileConflictsWithRemote_PreservedForRecovery(t *testing.T) {
	if testing.Short() {
		t.Skip("short: Gitea digital twin")
	}

	g := getSharedGitea(t)
	cloneURL := g.createRepo(t, "gc-dirty-conflict")

	cloneDir := filepath.Join(t.TempDir(), "repo")
	g.cloneRepo(t, cloneURL, cloneDir)

	// add ledger structure + data.txt with "original" content
	require.NoError(t, os.MkdirAll(filepath.Join(cloneDir, "sessions"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cloneDir, "sessions", ".gitkeep"), []byte(""), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(cloneDir, "data.txt"), []byte("original"), 0o644))
	cmd := exec.Command("git", "-C", cloneDir, "add", "sessions/.gitkeep", "data.txt")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git add failed: %s", string(out))
	cmd = exec.Command("git", "-C", cloneDir, "commit", "-m", "add ledger structure and data")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "git commit failed: %s", string(out))
	cmd = exec.Command("git", "-C", cloneDir, "push")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "git push failed: %s", string(out))

	// modify data.txt remotely via a temp clone
	g.pushFromTempClone(t, cloneURL, "data.txt", "remote-version")

	// modify data.txt locally (uncommitted) — conflicts with remote
	require.NoError(t, os.WriteFile(filepath.Join(cloneDir, "data.txt"), []byte("local-version"), 0o644))

	// run GC
	isolateCredentials(t)
	projectDir := setupProjectWithConfig(t, "")
	s := newTestScheduler(projectDir)

	ws := WorkspaceState{
		ID:       "dirty-conflict",
		Type:     WorkspaceTypeLedger,
		Path:     cloneDir,
		CloneURL: cloneURL,
		Exists:   true,
	}

	result := s.runBlueGreenGC(context.Background(), ws)
	assert.Equal(t, gcSuccess, result, "GC should succeed even with conflicting dirty state")

	// the repo should be valid
	_, err = os.Stat(filepath.Join(cloneDir, ".git", "HEAD"))
	require.NoError(t, err, ".git/HEAD should exist after GC")

	// the key intent: local edits must never be silently lost
	// either data.txt contains local-version (diff applied successfully)
	// or a recovery file exists preserving the diff
	dataContent, err := os.ReadFile(filepath.Join(cloneDir, "data.txt"))
	require.NoError(t, err, "data.txt should exist after GC")

	localEditsApplied := string(dataContent) == "local-version"

	// check for recovery artifacts that would contain the diff
	diffFile := cloneDir + ".gc-diff"
	_, diffExists := os.Stat(diffFile)
	recoveryExists := diffExists == nil

	// look for .rej files in the clone dir
	rejFiles, _ := filepath.Glob(filepath.Join(cloneDir, "*.rej"))
	hasRejFiles := len(rejFiles) > 0

	localEditsPreserved := localEditsApplied || recoveryExists || hasRejFiles
	assert.True(t, localEditsPreserved,
		"local edits must either be applied (data.txt=%q) or preserved in a recovery artifact (gc-diff exists=%v, rej files=%v)",
		string(dataContent), recoveryExists, hasRejFiles)
}

// --- F. Missing remote branch ---

// TestSync_MissingRemoteBranch_HandlesGracefully verifies that if the tracked
// remote branch is deleted, the daemon handles this gracefully without panicking
// or corrupting the local repo.
// Failure prevented: deleted remote branch causes unhandled git error that
// panics the daemon sync loop.
func TestSync_MissingRemoteBranch_HandlesGracefully(t *testing.T) {
	if testing.Short() {
		t.Skip("short: Gitea digital twin")
	}

	g := getSharedGitea(t)
	cloneURL := g.createRepo(t, "sync-missing-branch")

	cloneDir := filepath.Join(t.TempDir(), "repo")
	g.cloneRepo(t, cloneURL, cloneDir)

	// from a temp clone, create a backup branch and delete main from remote
	tmpClone := filepath.Join(t.TempDir(), "tmp-clone")
	g.cloneRepo(t, cloneURL, tmpClone)

	cmd := exec.Command("git", "-C", tmpClone, "checkout", "-b", "backup")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "checkout -b backup: %s", string(out))

	cmd = exec.Command("git", "-C", tmpClone, "push", "origin", "backup")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "push backup: %s", string(out))

	cmd = exec.Command("git", "-C", tmpClone, "push", "origin", "--delete", "main")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "delete main: %s", string(out))

	// the local clone still tracks origin/main which no longer exists
	isolateCredentials(t)
	projectDir := setupProjectWithConfig(t, "")
	s := newTestScheduler(projectDir)

	opts := ManagedRepoPullOpts{
		RepoPath:     cloneDir,
		RepoName:     "test-missing-branch",
		ProjectRoot:  projectDir,
		SyncInterval: time.Second,
		MinFetchAge:  0,
		Logger:       slog.Default(),
	}

	result := s.pullManagedRepo(context.Background(), opts)

	// pull should return an error or skip — the tracked branch is gone
	// the critical thing is: no panic, no corruption
	if result.Err != nil {
		// error is acceptable — the tracked branch doesn't exist
		t.Logf("pull returned error (expected): %v", result.Err)
	} else {
		// skip is also acceptable — fetch may see no changes for the missing ref
		t.Logf("pull skipped=%v reason=%q (acceptable)", result.Skipped, result.SkipReason)
	}

	// repo must not be corrupted — git rev-parse should still work
	cmd = exec.Command("git", "-C", cloneDir, "rev-parse", "--git-dir")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "repo should not be corrupted after pull with missing branch: %s", string(out))
}
