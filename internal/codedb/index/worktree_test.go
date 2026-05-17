package index

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sageox/ox/internal/codedb/store"
)

// initGitRepo creates a git repo with N commits and returns the repo path and tip hash.
// Uses git CLI to avoid go-git quirks with config.
func initGitRepo(t *testing.T, numCommits int) (string, string) {
	t.Helper()
	dir := t.TempDir()

	run := func(args ...string) string {
		t.Helper()
		mkCmd := func() *exec.Cmd {
			cmd := exec.Command("git", args...)
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), // safe: git CLI in temp dir needs inherited PATH
				"GIT_AUTHOR_NAME=test",
				"GIT_AUTHOR_EMAIL=test@sageox.ai",
				"GIT_COMMITTER_NAME=test",
				"GIT_COMMITTER_EMAIL=test@sageox.ai",
			)
			return cmd
		}
		out, err := mkCmd().CombinedOutput()
		// ox-a9ak: under heavy preflight parallelism the system git
		// binary occasionally crashes mid-command (SIGSEGV / SIGBUS)
		// or emits transient object-DB errors ("bad tree object",
		// "invalid object", "Error building trees") when packed
		// objects haven't reached disk yet. External-tool defect,
		// transient under load — single retry is enough to keep the
		// test suite from flaking. Real non-zero exits surface on the
		// retry attempt.
		if err != nil && (isGitSignalCrash(err) || isGitTransientObjectError(out)) {
			out, err = mkCmd().CombinedOutput()
		}
		require.NoError(t, err, "git %v: %s", args, out)
		return string(out)
	}

	run("init", "-b", "main")

	for i := 1; i <= numCommits; i++ {
		fname := fmt.Sprintf("file%d.go", i)
		content := fmt.Sprintf("package main\nfunc F%d() {}\n", i)
		require.NoError(t, os.WriteFile(filepath.Join(dir, fname), []byte(content), 0o644))
		run("add", fname)
		run("commit", "-m", fmt.Sprintf("commit %d", i))
	}

	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	require.NoError(t, err)

	return dir, string(out[:len(out)-1]) // trim newline
}

// createLinkedWorktree creates a linked worktree from mainRepoDir on a new branch.
func createLinkedWorktree(t *testing.T, mainRepoDir, branchName string) string {
	t.Helper()
	worktreeDir := filepath.Join(t.TempDir(), "worktree")

	cmd := exec.Command("git", "worktree", "add", "-b", branchName, worktreeDir)
	cmd.Dir = mainRepoDir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git worktree add: %s", out)

	t.Cleanup(func() {
		cmd := exec.Command("git", "worktree", "remove", "--force", worktreeDir)
		cmd.Dir = mainRepoDir
		_ = cmd.Run()
	})

	return worktreeDir
}

func TestResolveGitDir_NormalRepo(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git worktree operations")
	}
	dir, _ := initGitRepo(t, 1)

	path, isWorktree := resolveGitDir(dir)
	assert.Equal(t, dir, path)
	assert.False(t, isWorktree)
}

func TestResolveGitDir_LinkedWorktree(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git worktree operations")
	}
	mainDir, _ := initGitRepo(t, 1)

	worktreeDir := createLinkedWorktree(t, mainDir, "feature-branch")

	path, isWorktree := resolveGitDir(worktreeDir)
	assert.True(t, isWorktree, "should detect linked worktree")

	// resolve symlinks for macOS /var → /private/var
	expectedDir, _ := filepath.EvalSymlinks(mainDir)
	actualDir, _ := filepath.EvalSymlinks(path)
	assert.Equal(t, expectedDir, actualDir, "should resolve to main repo root")
}

func TestResolveGitDir_NoGit(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git worktree operations")
	}
	dir := t.TempDir()

	path, isWorktree := resolveGitDir(dir)
	assert.Equal(t, dir, path)
	assert.False(t, isWorktree)
}

func TestResolveDefaultBranchGit(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git worktree operations")
	}
	dir, tipHash := initGitRepo(t, 3)

	ref, err := resolveDefaultBranchGit(dir)
	require.NoError(t, err)
	assert.Equal(t, "refs/heads/main", ref.name)
	assert.Equal(t, plumbing.NewHash(tipHash), ref.tipOID)
}

func TestResolveDefaultBranchGit_Worktree(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git worktree operations")
	}
	mainDir, _ := initGitRepo(t, 3)
	worktreeDir := createLinkedWorktree(t, mainDir, "wt-branch")

	// add a commit on the worktree branch
	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), // safe: git CLI in temp dir needs inherited PATH
			"GIT_AUTHOR_NAME=test",
			"GIT_AUTHOR_EMAIL=test@sageox.ai",
			"GIT_COMMITTER_NAME=test",
			"GIT_COMMITTER_EMAIL=test@sageox.ai",
		)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}

	require.NoError(t, os.WriteFile(filepath.Join(worktreeDir, "wt.txt"), []byte("worktree"), 0o644))
	run(worktreeDir, "add", "wt.txt")
	run(worktreeDir, "commit", "-m", "worktree commit")

	ref, err := resolveDefaultBranchGit(worktreeDir)
	require.NoError(t, err)
	assert.Equal(t, "refs/heads/wt-branch", ref.name)
	assert.False(t, ref.tipOID.IsZero(), "tip hash should be non-zero")
}

func TestResolveDefaultBranchWithPath_NormalRepo(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git worktree operations")
	}
	dir, tipHash := initGitRepo(t, 2)

	repo, err := git.PlainOpen(dir)
	require.NoError(t, err)

	ref, err := resolveDefaultBranchWithPath(repo, dir)
	require.NoError(t, err)
	assert.Equal(t, "refs/heads/main", ref.name)
	assert.Equal(t, plumbing.NewHash(tipHash), ref.tipOID)
}

func TestResolveDefaultBranchWithPath_WorktreeFallback(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git worktree operations")
	}
	mainDir, _ := initGitRepo(t, 2)
	worktreeDir := createLinkedWorktree(t, mainDir, "fallback-branch")

	// open the main repo (as IndexLocalRepo does for worktrees)
	repo, err := git.PlainOpen(mainDir)
	require.NoError(t, err)

	// go-git on main repo resolves main repo's HEAD (main), not the worktree's
	// so the git CLI fallback should be used for worktree path
	ref, err := resolveDefaultBranchGit(worktreeDir)
	require.NoError(t, err)
	assert.Equal(t, "refs/heads/fallback-branch", ref.name)

	// resolveDefaultBranchWithPath on the main repo should resolve go-git's HEAD
	ref2, err := resolveDefaultBranchWithPath(repo, mainDir)
	require.NoError(t, err)
	assert.Equal(t, "refs/heads/main", ref2.name)
}

func TestIndexLocalRepo_LinkedWorktree(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git worktree operations")
	}
	mainDir, _ := initGitRepo(t, 5)
	worktreeDir := createLinkedWorktree(t, mainDir, "index-branch")

	// add commits on the worktree branch
	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), // safe: git CLI in temp dir needs inherited PATH
			"GIT_AUTHOR_NAME=test",
			"GIT_AUTHOR_EMAIL=test@sageox.ai",
			"GIT_COMMITTER_NAME=test",
			"GIT_COMMITTER_EMAIL=test@sageox.ai",
		)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}

	for i := 1; i <= 3; i++ {
		fname := fmt.Sprintf("wt_file%d.go", i)
		content := fmt.Sprintf("package wt\nfunc WT%d() {}\n", i)
		require.NoError(t, os.WriteFile(filepath.Join(worktreeDir, fname), []byte(content), 0o644))
		run(worktreeDir, "add", fname)
		run(worktreeDir, "commit", "-m", fmt.Sprintf("worktree commit %d", i))
	}

	// index the worktree
	dataDir := filepath.Join(t.TempDir(), "codedb")
	require.NoError(t, os.MkdirAll(dataDir, 0o755))

	s, err := store.Open(dataDir)
	require.NoError(t, err)
	defer s.Close()

	err = IndexLocalRepo(context.Background(), s, worktreeDir, IndexOptions{})
	require.NoError(t, err)

	// verify commits: 5 from main + 3 from worktree branch = 8
	var commitCount int
	require.NoError(t, s.QueryRow("SELECT COUNT(*) FROM commits").Scan(&commitCount))
	assert.Equal(t, 8, commitCount, "should index all commits reachable from worktree HEAD")

	// verify blobs exist from committed files
	var blobCount int
	require.NoError(t, s.QueryRow("SELECT COUNT(*) FROM blobs").Scan(&blobCount))
	assert.Greater(t, blobCount, 0, "should have indexed blobs")

	// verify repo record uses worktree path, not main repo path
	var repoPath string
	require.NoError(t, s.QueryRow("SELECT path FROM repos LIMIT 1").Scan(&repoPath))
	assert.Equal(t, worktreeDir, repoPath, "repo path should be the worktree, not the main repo")
}

func TestIndexLocalRepo_NormalRepo(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git worktree operations")
	}
	dir, _ := initGitRepo(t, 3)

	dataDir := filepath.Join(t.TempDir(), "codedb")
	require.NoError(t, os.MkdirAll(dataDir, 0o755))

	s, err := store.Open(dataDir)
	require.NoError(t, err)
	defer s.Close()

	err = IndexLocalRepo(context.Background(), s, dir, IndexOptions{})
	require.NoError(t, err)

	var commitCount int
	require.NoError(t, s.QueryRow("SELECT COUNT(*) FROM commits").Scan(&commitCount))
	assert.Equal(t, 3, commitCount, "should index all 3 commits")
}

// isGitSignalCrash reports whether an exec error reflects the system
// git binary being killed by SIGSEGV / SIGBUS — the external-tool
// crash mode observed under preflight parallelism (ox-a9ak). Used by
// test helpers to retry once on signal-induced failures while still
// surfacing real non-zero exits.
func isGitSignalCrash(err error) bool {
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		return false
	}
	ws, ok := exitErr.Sys().(syscall.WaitStatus)
	if !ok || !ws.Signaled() {
		return false
	}
	switch ws.Signal() {
	case syscall.SIGSEGV, syscall.SIGBUS:
		return true
	}
	return false
}

// isGitTransientObjectError reports whether git's combined output matches
// the transient object-DB corruption pattern observed under heavy parallel
// CI load (ox-a9ak family). Symptoms include "bad tree object HEAD",
// "invalid object", and "Error building trees" emitted mid-loop in a long
// sequence of `git commit` calls. These resolve on retry once pending
// writes settle; real corruption persists across retries and surfaces then.
func isGitTransientObjectError(out []byte) bool {
	s := string(out)
	return strings.Contains(s, "bad tree object") ||
		strings.Contains(s, "invalid object") ||
		strings.Contains(s, "Error building trees")
}
