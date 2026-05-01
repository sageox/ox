package daemon

import (
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// initGitRepoWithCommit creates a real git repo with an initial commit and returns
// the repo path and the SHA of the initial commit.
func initGitRepoWithCommit(t *testing.T) (string, string) {
	t.Helper()
	if testing.Short() {
		t.Skip("short: requires git commands")
	}

	dir := t.TempDir()
	cmds := [][]string{
		{"git", "-C", dir, "init"},
		{"git", "-C", dir, "config", "user.email", "test@test.com"},
		{"git", "-C", dir, "config", "user.name", "Test"},
		{"git", "-C", dir, "commit", "--allow-empty", "-m", "initial"},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "command %v failed: %s", args, string(out))
	}

	sha := getGitSHA(t, dir)
	return dir, sha
}

func getGitSHA(t *testing.T, dir string) string {
	t.Helper()
	cmd := exec.Command("git", "-C", dir, "rev-parse", "HEAD")
	out, err := cmd.Output()
	require.NoError(t, err)
	return string(out[:len(out)-1]) // trim newline
}

func commitFile(t *testing.T, dir, name, content, msg string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644))
	require.NoError(t, runGitWithSEGVRetry(exec.Command("git", "-C", dir, "add", name)),
		"git add failed")
	out, err := runGitOutputWithSEGVRetry(exec.Command("git", "-C", dir, "commit", "-m", msg))
	require.NoError(t, err, "commit failed: %s", string(out))
}

// runGitWithSEGVRetry runs a git subprocess and retries once on
// SIGSEGV / SIGBUS exit. ox-a9ak: under heavy preflight parallelism
// the system git binary occasionally crashes mid-command. This is an
// external-tool defect, not an ox bug, but we'd rather not flake the
// test suite over it. Single retry is enough — the crashes are
// transient under load and don't recur.
func runGitWithSEGVRetry(cmd *exec.Cmd) error {
	if err := cmd.Run(); err != nil {
		if isSEGVExit(err) {
			retried := exec.Command(cmd.Path, cmd.Args[1:]...)
			retried.Dir = cmd.Dir
			return retried.Run()
		}
		return err
	}
	return nil
}

func runGitOutputWithSEGVRetry(cmd *exec.Cmd) ([]byte, error) {
	out, err := cmd.CombinedOutput()
	if err != nil && isSEGVExit(err) {
		retried := exec.Command(cmd.Path, cmd.Args[1:]...)
		retried.Dir = cmd.Dir
		return retried.CombinedOutput()
	}
	return out, err
}

// isSEGVExit reports whether an exec error is a process-killed-by-
// signal exit for SIGSEGV or SIGBUS — the two signals we've seen the
// git binary die from under preflight load. Any other error (real
// non-zero exit, ENOENT, etc.) returns false so production failures
// surface normally.
func isSEGVExit(err error) bool {
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		return false
	}
	ws, ok := exitErr.Sys().(syscall.WaitStatus)
	if !ok {
		return false
	}
	if !ws.Signaled() {
		return false
	}
	switch ws.Signal() {
	case syscall.SIGSEGV, syscall.SIGBUS:
		return true
	}
	return false
}

func newDetectTestScheduler(t *testing.T) *SyncScheduler {
	t.Helper()
	cfg := DefaultConfig()
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	return NewSyncScheduler(cfg, logger)
}

// --------------------------------------------------------------------------
// detectChangedFiles
// --------------------------------------------------------------------------

func TestDetectChangedFiles_EmptyBaseSHA_ReturnsNil(t *testing.T) {
	// real-world failure prevented: after initial clone there is no previous SHA;
	// passing "" must gracefully return nil, not run a broken git diff
	t.Parallel()

	s := newDetectTestScheduler(t)
	result := s.detectChangedFiles("/any/path", "")
	assert.Nil(t, result, "empty baseSHA must return nil (no diff possible)")
}

func TestDetectChangedFiles_NoChanges_ReturnsNil(t *testing.T) {
	// real-world failure prevented: when HEAD hasn't changed since last pull,
	// detectChangedFiles must return nil so callers skip unnecessary reindex
	t.Parallel()

	dir, sha := initGitRepoWithCommit(t)
	s := newDetectTestScheduler(t)

	result := s.detectChangedFiles(dir, sha)
	assert.Nil(t, result, "same base and HEAD must return nil (no changes)")
}

func TestDetectChangedFiles_SingleFileChanged(t *testing.T) {
	// real-world failure prevented: a single file change after pull must be
	// detected so codedb can do a targeted reindex instead of full scan
	t.Parallel()

	dir, baseSHA := initGitRepoWithCommit(t)
	commitFile(t, dir, "README.md", "hello", "add readme")

	s := newDetectTestScheduler(t)
	result := s.detectChangedFiles(dir, baseSHA)

	assert.Equal(t, []string{"README.md"}, result)
}

func TestDetectChangedFiles_MultipleFilesChanged(t *testing.T) {
	// real-world failure prevented: multiple changed files must all be reported;
	// a bug that returns only the first would cause missed reindex
	t.Parallel()

	dir, baseSHA := initGitRepoWithCommit(t)
	commitFile(t, dir, "a.go", "package a", "add a")
	commitFile(t, dir, "b.go", "package b", "add b")

	s := newDetectTestScheduler(t)
	result := s.detectChangedFiles(dir, baseSHA)

	assert.Len(t, result, 2, "both files must be reported")
	assert.Contains(t, result, "a.go")
	assert.Contains(t, result, "b.go")
}

func TestDetectChangedFiles_SubdirectoryFiles(t *testing.T) {
	// real-world failure prevented: files in subdirectories must include the
	// relative path (e.g. "dir/file.go"), not just the basename
	t.Parallel()

	dir, baseSHA := initGitRepoWithCommit(t)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "internal", "pkg"), 0o755))
	commitFile(t, dir, filepath.Join("internal", "pkg", "util.go"), "package pkg", "add util")

	s := newDetectTestScheduler(t)
	result := s.detectChangedFiles(dir, baseSHA)

	assert.Equal(t, []string{"internal/pkg/util.go"}, result,
		"path must be relative to repo root, preserving directory structure")
}

func TestDetectChangedFiles_InvalidBaseSHA_ReturnsNil(t *testing.T) {
	// real-world failure prevented: if baseSHA is from a force-pushed branch
	// that no longer exists, git diff fails; must return nil not crash
	t.Parallel()

	dir, _ := initGitRepoWithCommit(t)
	s := newDetectTestScheduler(t)

	result := s.detectChangedFiles(dir, "deadbeef00000000000000000000000000000000")
	assert.Nil(t, result, "invalid SHA must return nil (graceful degradation)")
}

func TestDetectChangedFiles_NonExistentRepo_ReturnsNil(t *testing.T) {
	// real-world failure prevented: if workspace directory was deleted between
	// SHA capture and diff, must return nil not crash
	t.Parallel()

	s := newDetectTestScheduler(t)
	result := s.detectChangedFiles("/nonexistent/repo/path", "abc123")
	assert.Nil(t, result, "non-existent repo must return nil (graceful degradation)")
}

func TestDetectChangedFiles_CommitToCommit_NotWorkingTree(t *testing.T) {
	// real-world failure prevented: detectChangedFiles compares baseSHA..HEAD
	// (committed changes), NOT working tree. Unstaged changes must NOT appear.
	// This matters because the daemon calls this after git pull updates HEAD,
	// and uncommitted working tree changes are not relevant to reindex decisions.
	t.Parallel()

	dir, baseSHA := initGitRepoWithCommit(t)

	// create a committed change
	commitFile(t, dir, "committed.txt", "yes", "add committed")

	// create an unstaged working tree change
	require.NoError(t, os.WriteFile(filepath.Join(dir, "unstaged.txt"), []byte("no"), 0o644))

	s := newDetectTestScheduler(t)
	result := s.detectChangedFiles(dir, baseSHA)

	assert.Equal(t, []string{"committed.txt"}, result,
		"only committed changes should appear; unstaged working tree changes must NOT")
}
