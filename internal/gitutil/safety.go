// Package gitutil provides shared git safety primitives used by both the daemon
// (pull/fetch) and CLI (push/commit) code paths.
package gitutil

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// MinFetchHeadAge is the minimum age of FETCH_HEAD before we'll fetch again.
// Prevents redundant fetches if another process fetched recently.
const MinFetchHeadAge = 2 * time.Minute

// knownLockFiles are git lock files that indicate a crashed or in-progress git process.
var knownLockFiles = []string{
	"index.lock",
	"shallow.lock",
	"config.lock",
	"HEAD.lock",
}

// HasLockFiles checks .git/ for stale lock files that block git operations.
// Returns the names of lock files found (empty slice = safe to proceed).
func HasLockFiles(gitDir string) []string {
	var found []string
	for _, lock := range knownLockFiles {
		path := filepath.Join(gitDir, lock)
		if _, err := os.Stat(path); err == nil {
			found = append(found, lock)
		}
	}
	return found
}

// IsRebaseInProgress checks whether the repo is stuck in a broken rebase state.
// Returns true if .git/rebase-merge or .git/rebase-apply exists.
func IsRebaseInProgress(repoPath string) bool {
	gitDir := filepath.Join(repoPath, ".git")
	for _, dir := range []string{"rebase-merge", "rebase-apply"} {
		if _, err := os.Stat(filepath.Join(gitDir, dir)); err == nil {
			return true
		}
	}
	return false
}

// IsSafeForGitOps combines lock file and rebase state checks into a single
// pre-flight check. Returns nil if safe to proceed, or an error describing
// why the repo is blocked.
func IsSafeForGitOps(repoPath string) error {
	gitDir := filepath.Join(repoPath, ".git")

	if locks := HasLockFiles(gitDir); len(locks) > 0 {
		return fmt.Errorf("git lock files detected: %s (remove stale locks or wait for in-progress operation)", strings.Join(locks, ", "))
	}

	if IsRebaseInProgress(repoPath) {
		return fmt.Errorf("repo in broken rebase state (run 'git rebase --abort' in %s)", repoPath)
	}

	return nil
}

// FetchHeadAge returns how long ago FETCH_HEAD was last modified.
// Returns (0, false) if FETCH_HEAD doesn't exist or can't be read.
func FetchHeadAge(repoPath string) (time.Duration, bool) {
	fetchHead := filepath.Join(repoPath, ".git", "FETCH_HEAD")
	info, err := os.Stat(fetchHead)
	if err != nil {
		return 0, false
	}
	return time.Since(info.ModTime()), true
}
