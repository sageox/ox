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
)

// plainOpenTolerant opens a git repo via go-git with KeepDescriptors enabled for
// better performance. KeepDescriptors caches open pack file file descriptors across
// reads instead of reopening on every git object access — eliminates the dominant
// I/O overhead when reading thousands of objects from packfiles.
//
// Falls back to git.PlainOpen if the custom open fails (e.g., .git is a file not
// a directory, as in submodule checkouts).
//
// In go-git v5, this required an extensionSafeStorer workaround to strip
// [extensions] from the in-memory config (objectformat, worktreeconfig, etc.)
// because v5 rejected repos with any extensions it didn't recognize.
//
// go-git v6 handles all known extensions natively, making the workaround
// unnecessary. TestV6_PlainOpenAcceptsKnownExtensions verifies this.
// If a future git version adds extensions that v6 doesn't recognize,
// the workaround can be restored from git history.
func plainOpenTolerant(path string) (*git.Repository, error) {
	if repo, err := plainOpenWithKeepDescriptors(path); err == nil {
		return repo, nil
	}
	return git.PlainOpen(path)
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
	return git.Open(s, wt)
}
