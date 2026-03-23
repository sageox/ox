package gitutil

import (
	"os"
	"path/filepath"
)

// IsGitRepo checks whether path is the root of a valid git repository.
// It reads .git/HEAD, which works for both regular repos (where .git is a
// directory) and worktrees (where .git is a file pointing to the real git
// dir). A readable HEAD is the most reliable lightweight check — it catches
// partial clones and corrupt repos that a simple os.Stat(".git") would miss.
func IsGitRepo(path string) bool {
	if path == "" {
		return false
	}

	gitDir := filepath.Join(path, ".git")
	info, err := os.Stat(gitDir)
	if err != nil {
		return false
	}

	// regular repo: .git is a directory containing HEAD
	if info.IsDir() {
		_, err = os.ReadFile(filepath.Join(gitDir, "HEAD"))
		return err == nil
	}

	// worktree: .git is a file containing "gitdir: <path>"
	// if the file is readable, the worktree link exists
	_, err = os.ReadFile(gitDir)
	return err == nil
}
