// Package gitopen centralizes opening a user-owned git repository with go-git
// in a way that can NEVER rewrite that repository's .git/config.
//
// Why this exists (issue #819): codedb opens the managed source project repo
// in place to read objects. go-git re-marshals config through the single
// contract Storer.SetConfig, and across go-git v6 alpha snapshots that has
// flipped core.bare, dropped extensions.worktreeConfig, and (in some snapshots)
// persisted config as a side effect of *opening* a worktree repo. On a linked
// worktree that silently flipped core.bare=true on the shared main .git/config,
// breaking every work-tree git command until it was manually reset.
//
// The daemon must never write the managed source repo's .git/config (see
// .claude/rules/daemon-git.md). We enforce that structurally rather than
// trusting any particular alpha's open path to be read-only: SetConfig — the
// only path through which go-git persists config in every version — is a no-op
// for these read-only opens. codedb only READS objects here, so denying the
// write is always safe.
package gitopen

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/go-git/go-billy/v6/osfs"
	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/config"
	"github.com/go-git/go-git/v6/plumbing/cache"
	"github.com/go-git/go-git/v6/storage"
	"github.com/go-git/go-git/v6/storage/filesystem"
	"github.com/go-git/go-git/v6/storage/filesystem/dotgit"
)

// readOnlyConfigStorer embeds a filesystem storer and turns SetConfig into a
// no-op. codedb opens repos purely to READ objects, so a config write is never
// needed and denying it is always safe. Returning nil (rather than an error)
// keeps an open-time SetConfig — which some go-git versions perform when a
// worktree filesystem is supplied — from aborting the open; the read path does
// not depend on the write having landed (go-git holds config in memory).
type readOnlyConfigStorer struct {
	*filesystem.Storage
}

// SetConfig deliberately does nothing so go-git can never rewrite the source
// repo's .git/config (issue #819). Never remove this override.
func (readOnlyConfigStorer) SetConfig(*config.Config) error { return nil }

// WrapReadOnlyConfig wraps a filesystem storer so its config can never be
// persisted. Use this whenever go-git opens a user-owned repo for read-only
// work (e.g. codedb's KeepDescriptors fast path builds its own storer and
// wraps it before git.Open).
func WrapReadOnlyConfig(s *filesystem.Storage) storage.Storer {
	return readOnlyConfigStorer{s}
}

// GuardedPlainOpen is a drop-in replacement for git.PlainOpen for user-owned
// repositories. It opens with config writes denied so #819 can never recur,
// covering three layouts:
//   - a normal checkout (.git is a directory),
//   - a linked worktree (.git is a file with a commondir → the shared main
//     checkout, resolved by ResolveGitDir),
//   - a submodule (.git is a file whose self-contained gitdir has no commondir).
//
// A bare repository (e.g. codedb's own cache clone) falls back to git.PlainOpen:
// its config legitimately carries core.bare=true and it has no worktree/
// extensions to lose, so it is not part of the #819 surface.
func GuardedPlainOpen(path string) (*git.Repository, error) {
	// Normal repo, or a linked worktree resolved to its shared main checkout —
	// either way the resolved root has a real .git directory holding objects.
	root, _ := ResolveGitDir(path)
	if fi, err := os.Stat(filepath.Join(root, ".git")); err == nil && fi.IsDir() {
		return openGuarded(filepath.Join(root, ".git"), root)
	}
	// A .git FILE that ResolveGitDir did not remap: a submodule, whose gitdir
	// (e.g. .git/modules/<name>) is self-contained. Guard it directly rather
	// than falling through to an unguarded open.
	if gitDir, ok := submoduleGitDir(path); ok {
		return openGuarded(gitDir, path)
	}
	// Bare repo (path is itself the git dir) or unresolvable layout.
	return git.PlainOpen(path)
}

// openGuarded opens the repo whose git directory is dotDir and worktree is
// wtDir, with config writes denied.
func openGuarded(dotDir, wtDir string) (*git.Repository, error) {
	dot := osfs.New(dotDir, osfs.WithBoundOS())
	wt := osfs.New(wtDir, osfs.WithBoundOS())
	s := filesystem.NewStorage(dotgit.NewRepositoryFilesystem(dot, nil), cache.NewObjectLRUDefault())
	return git.Open(readOnlyConfigStorer{s}, wt)
}

// ResolveGitDir returns the path to open with go-git and whether it is a linked
// worktree. For linked worktrees (where .git is a file containing "gitdir: ..."
// AND the target has a commondir), it follows commondir to the main repo's root
// so go-git can access the shared object store. For normal repos, submodules, or
// anything unresolvable, returns the path unchanged.
func ResolveGitDir(repoPath string) (string, bool) {
	worktreeGitDir, ok := gitdirTarget(repoPath)
	if !ok {
		return repoPath, false // normal repo, no .git, or unreadable .git file
	}

	// A linked worktree has a commondir pointing at the shared .git; a submodule
	// does not (its gitdir is self-contained and is handled by GuardedPlainOpen).
	commondirFile := filepath.Join(worktreeGitDir, "commondir")
	commondirBytes, err := os.ReadFile(commondirFile)
	if err != nil {
		return repoPath, false
	}
	commondir := strings.TrimSpace(string(commondirBytes))
	if !filepath.IsAbs(commondir) {
		commondir = filepath.Join(worktreeGitDir, commondir)
	}

	// commondir points to the main .git dir; the repo root is its parent
	return filepath.Dir(commondir), true
}

// submoduleGitDir returns the self-contained gitdir a submodule's ".git" file
// points at (no commondir), or ("", false) for anything else (linked worktree,
// normal repo, bare, unreadable).
func submoduleGitDir(repoPath string) (string, bool) {
	gitDir, ok := gitdirTarget(repoPath)
	if !ok {
		return "", false
	}
	if _, err := os.Stat(filepath.Join(gitDir, "commondir")); err == nil {
		return "", false // linked worktree — handled by ResolveGitDir
	}
	if fi, err := os.Stat(gitDir); err != nil || !fi.IsDir() {
		return "", false
	}
	return gitDir, true
}

// gitdirTarget reads a ".git" FILE ("gitdir: <path>") and returns the absolute
// target directory. Returns ("", false) when .git is a directory, is missing,
// is unreadable, or does not contain a gitdir pointer.
func gitdirTarget(repoPath string) (string, bool) {
	dotGit := filepath.Join(repoPath, ".git")
	info, err := os.Lstat(dotGit)
	if err != nil || info.IsDir() {
		return "", false
	}
	content, err := os.ReadFile(dotGit)
	if err != nil {
		return "", false
	}
	line := strings.TrimSpace(string(content))
	if !strings.HasPrefix(line, "gitdir: ") {
		return "", false
	}
	target := strings.TrimPrefix(line, "gitdir: ")
	if !filepath.IsAbs(target) {
		target = filepath.Join(repoPath, target)
	}
	return target, true
}
