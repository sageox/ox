// Package gitopen centralizes opening a user-owned git repository with go-git
// in a way that can NEVER rewrite that repository's .git/config.
//
// Why this exists (issue #819): codedb opens the managed source project repo
// in place to read objects. go-git persists config through exactly one contract
// — Storer.SetConfig — reached from Init, Clone, CreateRemote, tag ops, and
// setIsBare (which flips core.bare=true). On the pinned go-git (v6 alpha) a
// read-only open never reaches SetConfig, but a config round-trip there flips
// core.bare and drops extensions.worktreeConfig, so any accidental write is
// catastrophic: on a linked worktree it silently sets core.bare=true on the
// shared main .git/config, breaking every work-tree git command until manually
// reset.
//
// The daemon must never write the managed source repo's .git/config (see
// .claude/rules/daemon-git.md). We enforce that STRUCTURALLY and FAIL-CLOSED,
// rather than trusting any particular go-git version's open path to be
// read-only: every open here goes through a storer whose SetConfig is a no-op,
// and GuardedPlainOpen has no unguarded fallback for any layout it might hand a
// user repo to. codedb only READS objects, so denying the write is always safe.
//
// The #819 recurrence (issue reopened on v0.14.2) came from the one layout that
// slipped past the guard: a "bare hub + git worktree add" checkout, where
// commondir points directly at the bare hub (no ".git" wrapper). ResolveGitDir
// mis-derived the root as its parent, so GuardedPlainOpen missed every guarded
// branch and fell through to an unguarded git.PlainOpen on the shared hub
// config. Both are fixed here: ResolveGitDir is bare-hub aware, and the
// fallback is guarded.
package gitopen

import (
	"os"
	"path/filepath"
	"strings"

	billy "github.com/go-git/go-billy/v6"
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
// repositories. Every layout is opened with config writes denied so #819 can
// never recur — there is no unguarded fallback:
//   - a normal checkout (.git is a directory),
//   - a linked worktree off a non-bare main (.git is a file whose commondir
//     resolves to "<root>/.git"; ResolveGitDir returns <root>),
//   - a linked worktree off a BARE hub (.git is a file whose commondir resolves
//     to the hub git dir itself; ResolveGitDir returns that hub dir) — the #819
//     recurrence layout,
//   - a submodule (.git is a file whose self-contained gitdir has no commondir),
//   - a bare repository (path is itself the git dir, e.g. codedb's own cache
//     clone).
//
// codedb only READS objects, so a config write is never needed for any of them;
// guarding even the bare cache clone is harmless (SetConfig is unused on the
// read path) and keeps the open path uniformly fail-closed.
func GuardedPlainOpen(path string) (*git.Repository, error) {
	// Normal repo, or a linked worktree resolved to a non-bare main checkout —
	// either way the resolved root has a real .git directory holding objects.
	root, _ := ResolveGitDir(path)
	if fi, err := os.Stat(filepath.Join(root, ".git")); err == nil && fi.IsDir() {
		return openGuarded(filepath.Join(root, ".git"), root)
	}
	// The resolved root IS a git directory: a bare hub (resolved from a linked
	// worktree, no .git wrapper — the #819 recurrence) or a bare cache clone
	// opened directly. Guard it too rather than falling through unguarded.
	if isGitDir(root) {
		return openGuarded(root, "")
	}
	// A .git FILE that ResolveGitDir did not remap: a submodule, whose gitdir
	// (e.g. .git/modules/<name>) is self-contained.
	if gitDir, ok := submoduleGitDir(path); ok {
		return openGuarded(gitDir, path)
	}
	// Unresolvable layout: still open GUARDED (fail-closed) rather than handing
	// go-git a config-writable handle to a user repo. Worst case this errors as
	// "repository does not exist" — never a silent config rewrite (#819).
	return openGuarded(path, "")
}

// openGuarded opens the repo whose git directory is dotDir with config writes
// denied. wtDir is the worktree root, or "" for a bare repo (no worktree
// filesystem — codedb reads objects only, so no worktree is required).
func openGuarded(dotDir, wtDir string) (*git.Repository, error) {
	dot := osfs.New(dotDir, osfs.WithBoundOS())
	s := filesystem.NewStorage(dotgit.NewRepositoryFilesystem(dot, nil), cache.NewObjectLRUDefault())
	var wt billy.Filesystem
	if wtDir != "" {
		wt = osfs.New(wtDir, osfs.WithBoundOS())
	}
	return git.Open(readOnlyConfigStorer{s}, wt)
}

// isGitDir reports whether dir is itself a git directory (a bare repo, or the
// shared .git a worktree hub points at): it holds a HEAD file and an objects
// directory. Used to guard the bare-hub / bare-cache open paths that would
// otherwise fall through to an unguarded open.
func isGitDir(dir string) bool {
	if fi, err := os.Stat(filepath.Join(dir, "HEAD")); err != nil || fi.IsDir() {
		return false
	}
	if fi, err := os.Stat(filepath.Join(dir, "objects")); err != nil || !fi.IsDir() {
		return false
	}
	return true
}

// ResolveGitDir returns the path to open with go-git and whether it is a linked
// worktree. For linked worktrees (where .git is a file containing "gitdir: ..."
// AND the target has a commondir), it follows commondir to the shared object
// store so go-git can read it. For normal repos, submodules, or anything
// unresolvable, returns the path unchanged.
func ResolveGitDir(repoPath string) (string, bool) {
	worktreeGitDir, ok := gitdirTarget(repoPath)
	if !ok {
		return repoPath, false // normal repo, no .git, or unreadable .git file
	}

	// A linked worktree has a commondir pointing at the shared git dir; a
	// submodule does not (its gitdir is self-contained, handled by GuardedPlainOpen).
	commondirFile := filepath.Join(worktreeGitDir, "commondir")
	commondirBytes, err := os.ReadFile(commondirFile)
	if err != nil {
		return repoPath, false
	}
	commondir := strings.TrimSpace(string(commondirBytes))
	if !filepath.IsAbs(commondir) {
		commondir = filepath.Join(worktreeGitDir, commondir)
	}
	commondir = filepath.Clean(commondir)

	// commondir points at the SHARED git directory. Two layouts diverge here:
	//   - non-bare main checkout: commondir is "<root>/.git", so the repo root
	//     (whose ".git" subdir GuardedPlainOpen opens) is its parent.
	//   - bare hub (git init --bare + git worktree add, often with a manual
	//     `git config core.bare false`): commondir IS the hub git dir itself
	//     (e.g. "<name>.git"), with no ".git" wrapper — its parent is WRONG.
	//     Taking the parent here was the #819 recurrence: GuardedPlainOpen then
	//     missed every guarded branch and fell through to an unguarded open on
	//     the shared hub config.
	if filepath.Base(commondir) == ".git" {
		return filepath.Dir(commondir), true
	}
	return commondir, true
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
