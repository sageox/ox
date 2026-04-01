//go:build slow

package daemon

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- A. Symlink safety ---

// TestGC_SymlinkInUntracked_NotFollowed verifies that a symlink pointing outside
// the repo is never dereferenced during GC's untracked file backup.
// Failure prevented: rogue symlink causes GC to copy external file contents
// (e.g., /etc/hosts) into the repo, leaking sensitive data.
func TestGC_SymlinkInUntracked_NotFollowed(t *testing.T) {
	if testing.Short() {
		t.Skip("short: Gitea digital twin")
	}

	g := getSharedGitea(t)
	cloneURL := g.createRepo(t, "gc-symlink-nofollow")

	cloneDir := filepath.Join(t.TempDir(), "repo")
	g.cloneRepo(t, cloneURL, cloneDir)

	// add ledger structure
	require.NoError(t, os.MkdirAll(filepath.Join(cloneDir, "sessions"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cloneDir, "sessions", ".gitkeep"), []byte(""), 0o644))
	cmd := exec.Command("git", "-C", cloneDir, "add", "sessions/.gitkeep")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git add: %s", string(out))
	cmd = exec.Command("git", "-C", cloneDir, "commit", "-m", "ledger structure")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "git commit: %s", string(out))
	cmd = exec.Command("git", "-C", cloneDir, "push")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "git push: %s", string(out))

	// create a symlink pointing to /etc/hosts (exists on macOS and Linux)
	symlinkPath := filepath.Join(cloneDir, "rogue-link")
	require.NoError(t, os.Symlink("/etc/hosts", symlinkPath))

	// also create a normal untracked file to verify GC still works
	normalPath := filepath.Join(cloneDir, "normal-file.txt")
	require.NoError(t, os.WriteFile(normalPath, []byte("normal-content"), 0o644))

	// read /etc/hosts content for comparison
	etcHostsContent, err := os.ReadFile("/etc/hosts")
	require.NoError(t, err, "/etc/hosts should be readable for comparison")

	// run GC
	isolateCredentials(t)
	projectDir := setupProjectWithConfig(t, "")
	s := newTestScheduler(projectDir)

	ws := WorkspaceState{
		ID:       "symlink-nofollow",
		Type:     WorkspaceTypeLedger,
		Path:     cloneDir,
		CloneURL: cloneURL,
		Exists:   true,
	}

	result := s.runBlueGreenGC(context.Background(), ws)
	assert.Equal(t, gcSuccess, result, "GC should succeed despite symlink in untracked files")

	// normal untracked file should be restored
	content, err := os.ReadFile(filepath.Join(cloneDir, "normal-file.txt"))
	require.NoError(t, err, "normal untracked file should survive GC")
	assert.Equal(t, "normal-content", string(content))

	// the symlink should either not exist, or still be a symlink — never a regular
	// file containing the contents of /etc/hosts
	restoredPath := filepath.Join(cloneDir, "rogue-link")
	fi, err := os.Lstat(restoredPath)
	if err == nil {
		// file exists after GC — check it was not dereferenced
		if fi.Mode()&os.ModeSymlink != 0 {
			// still a symlink, that's acceptable
			return
		}
		// it's a regular file — make sure it does NOT contain /etc/hosts content
		restoredContent, readErr := os.ReadFile(restoredPath)
		require.NoError(t, readErr)
		assert.False(t, bytes.Equal(restoredContent, etcHostsContent),
			"GC must never dereference symlinks and copy external file contents into the repo")
	}
	// if the file doesn't exist (os.IsNotExist), that's also acceptable — symlink was skipped
}

// TestGC_SymlinkInUntracked_DanglingSymlink verifies that a dangling symlink
// (pointing to a nonexistent target) does not crash or fail GC.
// Failure prevented: dangling symlink causes panic or GC failure during
// untracked file backup, blocking all future GC cycles.
func TestGC_SymlinkInUntracked_DanglingSymlink(t *testing.T) {
	if testing.Short() {
		t.Skip("short: Gitea digital twin")
	}

	g := getSharedGitea(t)
	cloneURL := g.createRepo(t, "gc-dangling-symlink")

	cloneDir := filepath.Join(t.TempDir(), "repo")
	g.cloneRepo(t, cloneURL, cloneDir)

	// add ledger structure
	require.NoError(t, os.MkdirAll(filepath.Join(cloneDir, "sessions"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cloneDir, "sessions", ".gitkeep"), []byte(""), 0o644))
	cmd := exec.Command("git", "-C", cloneDir, "add", "sessions/.gitkeep")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git add: %s", string(out))
	cmd = exec.Command("git", "-C", cloneDir, "commit", "-m", "ledger structure")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "git commit: %s", string(out))
	cmd = exec.Command("git", "-C", cloneDir, "push")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "git push: %s", string(out))

	// create a dangling symlink pointing to a nonexistent path
	danglingPath := filepath.Join(cloneDir, "dangling-link")
	require.NoError(t, os.Symlink("/nonexistent/path/that/does/not/exist", danglingPath))

	// also create a normal untracked file
	normalPath := filepath.Join(cloneDir, "normal-file.txt")
	require.NoError(t, os.WriteFile(normalPath, []byte("normal-content"), 0o644))

	// run GC
	isolateCredentials(t)
	projectDir := setupProjectWithConfig(t, "")
	s := newTestScheduler(projectDir)

	ws := WorkspaceState{
		ID:       "dangling-symlink",
		Type:     WorkspaceTypeLedger,
		Path:     cloneDir,
		CloneURL: cloneURL,
		Exists:   true,
	}

	result := s.runBlueGreenGC(context.Background(), ws)
	assert.Equal(t, gcSuccess, result, "GC should succeed despite dangling symlink")

	// normal untracked file should be restored regardless of the dangling symlink
	content, err := os.ReadFile(filepath.Join(cloneDir, "normal-file.txt"))
	require.NoError(t, err, "normal untracked file should survive GC")
	assert.Equal(t, "normal-content", string(content))
}

// --- B. Large diff safety ---

// TestGC_LargeDiffCapture_DoesNotOOM verifies that GC handles a large
// uncommitted modification without crashing. The diff capture reads the entire
// diff into memory via cmd.Output(), so a large diff must not cause OOM.
// Failure prevented: large uncommitted file change causes GC to OOM during
// diff capture, killing the daemon process.
func TestGC_LargeDiffCapture_DoesNotOOM(t *testing.T) {
	if testing.Short() {
		t.Skip("short: Gitea digital twin")
	}

	g := getSharedGitea(t)
	cloneURL := g.createRepo(t, "gc-large-diff")

	cloneDir := filepath.Join(t.TempDir(), "repo")
	g.cloneRepo(t, cloneURL, cloneDir)

	// add ledger structure
	require.NoError(t, os.MkdirAll(filepath.Join(cloneDir, "sessions"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cloneDir, "sessions", ".gitkeep"), []byte(""), 0o644))
	cmd := exec.Command("git", "-C", cloneDir, "add", "sessions/.gitkeep")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git add: %s", string(out))
	cmd = exec.Command("git", "-C", cloneDir, "commit", "-m", "ledger structure")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "git commit: %s", string(out))
	cmd = exec.Command("git", "-C", cloneDir, "push")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "git push: %s", string(out))

	// create a 1MB tracked file, commit and push
	largeFile := filepath.Join(cloneDir, "large-data.bin")
	originalData := bytes.Repeat([]byte("A"), 1024*1024) // 1MB of 'A'
	require.NoError(t, os.WriteFile(largeFile, originalData, 0o644))

	cmd = exec.Command("git", "-C", cloneDir, "add", "large-data.bin")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "git add: %s", string(out))
	cmd = exec.Command("git", "-C", cloneDir, "commit", "-m", "add large file")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "git commit: %s", string(out))
	cmd = exec.Command("git", "-C", cloneDir, "push")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "git push: %s", string(out))

	// modify the file with different 1MB content (creates ~1MB diff)
	modifiedData := bytes.Repeat([]byte("B"), 1024*1024) // 1MB of 'B'
	require.NoError(t, os.WriteFile(largeFile, modifiedData, 0o644))

	// run GC
	isolateCredentials(t)
	projectDir := setupProjectWithConfig(t, "")
	s := newTestScheduler(projectDir)

	ws := WorkspaceState{
		ID:       "large-diff",
		Type:     WorkspaceTypeLedger,
		Path:     cloneDir,
		CloneURL: cloneURL,
		Exists:   true,
	}

	result := s.runBlueGreenGC(context.Background(), ws)
	assert.Equal(t, gcSuccess, result, "GC should succeed with large uncommitted diff")

	// the modified content should be restored after GC
	restoredContent, err := os.ReadFile(filepath.Join(cloneDir, "large-data.bin"))
	require.NoError(t, err, "large file should exist after GC")
	assert.Equal(t, len(modifiedData), len(restoredContent),
		"restored file should match modified size")
	assert.True(t, bytes.Equal(modifiedData, restoredContent),
		"restored file content should match the uncommitted modification")
}

// --- C. Concurrent GC safety ---

// TestGC_ConcurrentGCLock_SecondSkips verifies that two concurrent GC operations
// on the same workspace do not corrupt the repo. At least one must succeed and
// the repo must be valid afterward.
// Failure prevented: concurrent GC reclones race on the atomic swap, causing
// the directory to disappear or contain a half-swapped state.
func TestGC_ConcurrentGCLock_SecondSkips(t *testing.T) {
	if testing.Short() {
		t.Skip("short: Gitea digital twin")
	}

	g := getSharedGitea(t)
	cloneURL := g.createRepo(t, "gc-concurrent")

	cloneDir := filepath.Join(t.TempDir(), "repo")
	g.cloneRepo(t, cloneURL, cloneDir)

	// add ledger structure
	require.NoError(t, os.MkdirAll(filepath.Join(cloneDir, "sessions"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cloneDir, "sessions", ".gitkeep"), []byte(""), 0o644))
	cmd := exec.Command("git", "-C", cloneDir, "add", "sessions/.gitkeep")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git add: %s", string(out))
	cmd = exec.Command("git", "-C", cloneDir, "commit", "-m", "ledger structure")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "git commit: %s", string(out))
	cmd = exec.Command("git", "-C", cloneDir, "push")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "git push: %s", string(out))

	// add an untracked file so we can verify repo integrity after concurrent GC
	require.NoError(t, os.WriteFile(filepath.Join(cloneDir, "canary.txt"), []byte("canary"), 0o644))

	isolateCredentials(t)
	projectDir := setupProjectWithConfig(t, "")

	ws := WorkspaceState{
		ID:       "concurrent-gc",
		Type:     WorkspaceTypeLedger,
		Path:     cloneDir,
		CloneURL: cloneURL,
		Exists:   true,
	}

	// run two GC operations concurrently
	var wg sync.WaitGroup
	results := make([]gcResult, 2)

	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			s := newTestScheduler(projectDir)
			results[idx] = s.runBlueGreenGC(context.Background(), ws)
		}(i)
	}
	wg.Wait()

	// at least one GC should succeed
	anySuccess := results[0] == gcSuccess || results[1] == gcSuccess
	assert.True(t, anySuccess, "at least one concurrent GC should succeed, got: %v, %v", results[0], results[1])

	// no panics occurred (if we got here, neither goroutine panicked)

	// repo should be a valid git repo after concurrent GC
	_, err = os.Stat(filepath.Join(cloneDir, ".git", "HEAD"))
	require.NoError(t, err, ".git/HEAD should exist after concurrent GC")

	// sessions/ structure should survive
	_, err = os.Stat(filepath.Join(cloneDir, "sessions"))
	require.NoError(t, err, "sessions/ should exist after concurrent GC")

	// temp artifacts should be cleaned up
	_, err = os.Stat(cloneDir + ".old")
	assert.True(t, os.IsNotExist(err), ".old dir should not linger after concurrent GC")
	_, err = os.Stat(cloneDir + ".new")
	assert.True(t, os.IsNotExist(err), ".new dir should not linger after concurrent GC")
}
