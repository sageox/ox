package index

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/codedb/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIndexLocalRepo_AlternatesReturnsSentinel verifies that IndexLocalRepo
// detects `.git/objects/info/alternates` and returns ErrAlternatesUnsupported
// rather than attempting to walk the repo (which would fail with bare
// "object not found" mid-walk because go-git v6 doesn't honor alternates).
//
// Failure prevented: codedb silently producing a partial or empty index for
// any repo using `git clone --shared` or `--reference`. The sentinel lets
// callers (cmd/ox/index.go, internal/daemon/codedb.go) surface a clear
// "indexing skipped, here's why" message instead of a cryptic error.
func TestIndexLocalRepo_AlternatesReturnsSentinel(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git clone operations")
	}

	srcDir, _ := initGitRepo(t, 3)
	runGitIn(t, srcDir, "gc", "--quiet")

	parent := t.TempDir()
	dstDir := filepath.Join(parent, "shared-clone")
	cmd := exec.Command("git", "clone", "--shared", "--no-checkout", srcDir, dstDir)
	cmd.Env = append(os.Environ(), // safe: git CLI in temp dir, not ox subprocess
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@sageox.ai",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@sageox.ai")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git clone --shared: %s", out)

	// Confirm fixture truly has alternates configured.
	altsPath := filepath.Join(dstDir, ".git", "objects", "info", "alternates")
	info, err := os.Stat(altsPath)
	require.NoError(t, err)
	require.Positive(t, info.Size(), "alternates file must be non-empty")

	dbDir := t.TempDir()
	s, err := store.Open(dbDir)
	require.NoError(t, err)
	defer s.Close()

	err = IndexLocalRepo(context.Background(), s, dstDir, IndexOptions{})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrAlternatesUnsupported,
		"IndexLocalRepo must return ErrAlternatesUnsupported sentinel for alternates repos")
}

// TestHasAlternates verifies the helper's detection logic in isolation,
// including the worktree case (where alternates lives on the common dir).
func TestHasAlternates(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git clone operations")
	}

	t.Run("no_alternates", func(t *testing.T) {
		t.Parallel()
		dir, _ := initGitRepo(t, 1)
		assert.False(t, hasAlternates(dir))
	})

	t.Run("shared_clone_has_alternates", func(t *testing.T) {
		t.Parallel()
		srcDir, _ := initGitRepo(t, 1)
		parent := t.TempDir()
		dstDir := filepath.Join(parent, "shared")
		cmd := exec.Command("git", "clone", "--shared", "--no-checkout", srcDir, dstDir)
		cmd.Env = append(os.Environ(), // safe: git CLI in temp dir, not ox subprocess
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@sageox.ai",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@sageox.ai")
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "%s", out)

		assert.True(t, hasAlternates(dstDir))
	})

	t.Run("worktree_inherits_alternates", func(t *testing.T) {
		t.Parallel()
		srcDir, _ := initGitRepo(t, 2)
		parent := t.TempDir()
		mainDir := filepath.Join(parent, "main")
		cmd := exec.Command("git", "clone", "--shared", srcDir, mainDir)
		cmd.Env = append(os.Environ(), // safe: git CLI in temp dir, not ox subprocess
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@sageox.ai",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@sageox.ai")
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "%s", out)

		wtDir := filepath.Join(parent, "wt")
		wtCmd := exec.Command("git", "worktree", "add", "-b", "feat", wtDir)
		wtCmd.Dir = mainDir
		out, err = wtCmd.CombinedOutput()
		require.NoError(t, err, "%s", out)

		// resolveGitDir resolves the worktree's .git file to the main
		// repo's git dir, which is where alternates lives.
		openPath, _ := resolveGitDir(wtDir)
		assert.True(t, hasAlternates(openPath),
			"worktree must inherit main repo's alternates via rev-parse --git-common-dir")
	})
}
