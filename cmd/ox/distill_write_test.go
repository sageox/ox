package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- A. Commit failure cleanup ---

// TestCommitMemoryFile_UnstagesOnCommitFailure verifies that when git commit
// fails (e.g., lock contention, hook failure), the file is unstaged from the
// index so it doesn't accumulate as dirty state across daemon --autostash cycles.
// Failure prevented: staged-but-uncommitted files blocking git pull --rebase (#445).
func TestCommitMemoryFile_UnstagesOnCommitFailure(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git repo setup + pre-commit hook")
	}
	tmp := t.TempDir()
	initGitRepo(t, tmp)

	// write a file to commit
	relPath := filepath.Join("memory", "test.md")
	fullPath := filepath.Join(tmp, relPath)
	require.NoError(t, os.MkdirAll(filepath.Dir(fullPath), 0o755))
	require.NoError(t, os.WriteFile(fullPath, []byte("content"), 0o644))

	// install a pre-commit hook that always fails
	hookDir := filepath.Join(tmp, ".git", "hooks")
	require.NoError(t, os.MkdirAll(hookDir, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(hookDir, "pre-commit"),
		[]byte("#!/bin/sh\nexit 1\n"),
		0o755,
	))

	// commitMemoryFile should return an error (commit blocked by hook)
	err := commitMemoryFile(tmp, relPath, "test commit")
	require.Error(t, err, "commitMemoryFile should fail when hook rejects commit")

	// the file must NOT be staged after failure
	statusCmd := exec.Command("git", "status", "--porcelain")
	statusCmd.Dir = tmp
	out, err := statusCmd.Output()
	require.NoError(t, err)

	// porcelain format: XY path — don't TrimSpace (leading space is meaningful)
	// file should be untracked ("??") not staged ("A ")
	for _, line := range strings.Split(string(out), "\n") {
		if len(line) >= 2 && strings.Contains(line, relPath) {
			assert.Equal(t, "??", line[:2], "file should be untracked, not staged, after commit failure")
		}
	}
}

// TestRemoveMemoryFile_UnstagesOnCommitFailure verifies that removeMemoryFile
// unstages the deletion when git commit fails.
// Failure prevented: staged deletions blocking git pull --rebase (#445).
func TestRemoveMemoryFile_UnstagesOnCommitFailure(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git repo setup + pre-commit hook")
	}
	tmp := t.TempDir()
	initGitRepo(t, tmp)

	// create and commit a file first
	relPath := filepath.Join("memory", "to-remove.md")
	fullPath := filepath.Join(tmp, relPath)
	require.NoError(t, os.MkdirAll(filepath.Dir(fullPath), 0o755))
	require.NoError(t, os.WriteFile(fullPath, []byte("content"), 0o644))
	require.NoError(t, commitMemoryFile(tmp, relPath, "add file"))

	// install a pre-commit hook that always fails
	hookDir := filepath.Join(tmp, ".git", "hooks")
	require.NoError(t, os.MkdirAll(hookDir, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(hookDir, "pre-commit"),
		[]byte("#!/bin/sh\nexit 1\n"),
		0o755,
	))

	// removeMemoryFile should return an error
	err := removeMemoryFile(tmp, relPath, "remove file")
	require.Error(t, err, "removeMemoryFile should fail when hook rejects commit")

	// the deletion must NOT be staged
	statusCmd := exec.Command("git", "status", "--porcelain")
	statusCmd.Dir = tmp
	out, err := statusCmd.Output()
	require.NoError(t, err)

	// porcelain format: XY path — X is staged status, Y is working tree status
	// " D" = unstaged deletion (OK), "D " = staged deletion (bug)
	// Note: don't TrimSpace — the leading space in " D" is meaningful.
	for _, line := range strings.Split(string(out), "\n") {
		if len(line) >= 2 && strings.Contains(line, relPath) {
			assert.NotEqual(t, "D", string(line[0]), "deletion should not be staged (first column) after commit failure")
		}
	}
}

// --- B. Happy path ---

// TestCommitMemoryFile_CommitsSuccessfully verifies the normal happy path.
func TestCommitMemoryFile_CommitsSuccessfully(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git repo setup")
	}
	tmp := t.TempDir()
	initGitRepo(t, tmp)

	relPath := filepath.Join("memory", "daily", "2026-03-11.md")
	fullPath := filepath.Join(tmp, relPath)
	require.NoError(t, os.MkdirAll(filepath.Dir(fullPath), 0o755))
	require.NoError(t, os.WriteFile(fullPath, []byte("test content"), 0o644))

	require.NoError(t, commitMemoryFile(tmp, relPath, "test commit"))

	// verify clean working tree
	statusCmd := exec.Command("git", "status", "--porcelain")
	statusCmd.Dir = tmp
	out, err := statusCmd.Output()
	require.NoError(t, err)
	assert.Empty(t, strings.TrimSpace(string(out)), "working tree should be clean after commit")
}

// TestCommitMemoryFile_NothingToCommit verifies the no-op case.
func TestCommitMemoryFile_NothingToCommit(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git repo setup")
	}
	tmp := t.TempDir()
	initGitRepo(t, tmp)

	relPath := filepath.Join("memory", "daily", "2026-03-11.md")
	fullPath := filepath.Join(tmp, relPath)
	require.NoError(t, os.MkdirAll(filepath.Dir(fullPath), 0o755))
	require.NoError(t, os.WriteFile(fullPath, []byte("test content"), 0o644))
	require.NoError(t, commitMemoryFile(tmp, relPath, "first commit"))

	// committing again with no changes should not error
	require.NoError(t, commitMemoryFile(tmp, relPath, "no-op commit"))
}
