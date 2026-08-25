package index

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/go-git/go-billy/v6/osfs"
	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing/cache"
	"github.com/go-git/go-git/v6/storage/filesystem"
	"github.com/go-git/go-git/v6/storage/filesystem/dotgit"

	"github.com/sageox/ox/internal/codedb/gitopen"
)

// plainOpenTolerant opens a git repo via go-git with KeepDescriptors enabled for
// better performance. KeepDescriptors caches open pack file file descriptors across
// reads instead of reopening on every git object access — eliminates the dominant
// I/O overhead when reading thousands of objects from packfiles.
//
// Falls back to gitopen.GuardedPlainOpen if the custom open fails (e.g., .git is
// a file not a directory, as in submodule checkouts).
//
// Both the fast path and the fallback open the source repo through a storer
// whose config can never be persisted (gitopen.WrapReadOnlyConfig /
// GuardedPlainOpen). This is load-bearing: go-git re-marshals config through
// Storer.SetConfig, and across go-git v6 alpha snapshots that has flipped
// core.bare, dropped extensions.worktreeConfig, and (in some snapshots)
// persisted config as a side effect of opening a worktree repo. codedb only
// READS objects here, so denying the config write is always safe and prevents
// corrupting the user's managed .git/config — see issue #819 and
// internal/codedb/gitopen. TestV6_PlainOpenAcceptsKnownExtensions verifies v6
// still opens known extensions without erroring.
func plainOpenTolerant(path string) (*git.Repository, error) {
	if repo, err := plainOpenWithKeepDescriptors(path); err == nil {
		return repo, nil
	}
	return gitopen.GuardedPlainOpen(path)
}

// plainOpenWithKeepDescriptors opens a git repo using filesystem.Options{KeepDescriptors: true}.
// This caches packfile file descriptors across reads, avoiding the open+close overhead
// that accounts for ~47% of total CPU time on large repos.
//
// Only works when .git is a directory (not a file). Returns an error for linked
// worktrees whose .git is a file — the caller falls back to git.PlainOpen.
func plainOpenWithKeepDescriptors(path string) (*git.Repository, error) {
	dotPath := filepath.Join(path, ".git")
	fi, err := os.Stat(dotPath)
	if err != nil {
		return nil, fmt.Errorf("stat .git: %w", err)
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf(".git is not a directory: %s", dotPath)
	}

	dot := osfs.New(dotPath, osfs.WithBoundOS())
	wt := osfs.New(path, osfs.WithBoundOS())
	repositoryFs := dotgit.NewRepositoryFilesystem(dot, nil)
	s := filesystem.NewStorageWithOptions(repositoryFs, cache.NewObjectLRUDefault(), filesystem.Options{
		KeepDescriptors: true,
	})
	// Deny config writes: go-git must never rewrite the source repo's
	// .git/config while codedb reads its objects (issue #819).
	return git.Open(gitopen.WrapReadOnlyConfig(s), wt)
}

// plainOpenPool opens n independent Repository handles for the same git directory.
// Each handle has its own packfile reader state, enabling lock-free parallel blob reads.
func plainOpenPool(repoPath string, n int) ([]*git.Repository, error) {
	repos := make([]*git.Repository, 0, n)
	for i := 0; i < n; i++ {
		r, err := plainOpenTolerant(repoPath)
		if err != nil {
			for _, opened := range repos {
				opened.Close()
			}
			return nil, fmt.Errorf("open repo handle %d/%d: %w", i+1, n, err)
		}
		repos = append(repos, r)
	}
	return repos, nil
}
