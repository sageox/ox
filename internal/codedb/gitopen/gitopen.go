// Package gitopen centralizes opening a user-owned git repository with go-git
// in a way that can NEVER rewrite that repository's .git/config.
//
// Why this exists (issue #819): codedb opens the managed source project repo
// in place to read objects. go-git v6's config Marshal is lossy — it drops
// unrecognized [extensions] subsections (notably extensions.worktreeConfig)
// and rewrites [core], and some go-git versions persist worktree/bare config
// as a side effect of *opening* a repo. On a linked-worktree checkout that
// silently flipped core.bare=true on the user's shared main .git/config,
// breaking every work-tree git command until it was manually reset.
//
// The daemon must never write the managed source repo's .git/config (see
// .claude/rules/daemon-git.md). We enforce that structurally here rather than
// trusting any particular go-git version's open path to be read-only: the
// storer's SetConfig — the single contract through which go-git persists
// config in every version — is turned into a no-op for these read-only opens.
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
// repositories. For a normal checkout (or a linked worktree, resolved to its
// shared main checkout), it opens with config writes denied so #819 can never
// recur. For a bare repository (e.g. codedb's own cache clone) it falls back
// to git.PlainOpen: a bare repo's config legitimately carries core.bare=true
// and has no worktree/extensions to lose, so it is not part of the #819
// surface and is not ours to protect.
func GuardedPlainOpen(path string) (*git.Repository, error) {
	root, _ := ResolveGitDir(path)
	gitDir := filepath.Join(root, ".git")
	if fi, err := os.Stat(gitDir); err == nil && fi.IsDir() {
		dot := osfs.New(gitDir, osfs.WithBoundOS())
		wt := osfs.New(root, osfs.WithBoundOS())
		s := filesystem.NewStorage(dotgit.NewRepositoryFilesystem(dot, nil), cache.NewObjectLRUDefault())
		return git.Open(readOnlyConfigStorer{s}, wt)
	}
	return git.PlainOpen(path)
}

// ResolveGitDir returns the path to open with go-git and whether it is a linked
// worktree. For linked worktrees (where .git is a file containing "gitdir: ..."),
// it follows commondir to the main repo's root so go-git can access the shared
// object store. For normal repos, returns the path unchanged.
func ResolveGitDir(repoPath string) (string, bool) {
	dotGit := filepath.Join(repoPath, ".git")
	info, err := os.Lstat(dotGit)
	if err != nil || info.IsDir() {
		return repoPath, false // normal repo or no .git
	}

	// .git is a file → linked worktree, read "gitdir: <path>"
	content, err := os.ReadFile(dotGit)
	if err != nil {
		return repoPath, false
	}
	line := strings.TrimSpace(string(content))
	if !strings.HasPrefix(line, "gitdir: ") {
		return repoPath, false
	}
	worktreeGitDir := strings.TrimPrefix(line, "gitdir: ")
	if !filepath.IsAbs(worktreeGitDir) {
		worktreeGitDir = filepath.Join(repoPath, worktreeGitDir)
	}

	// read commondir to find the shared .git (e.g., "../.." → main .git)
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
	mainRepoRoot := filepath.Dir(commondir)
	return mainRepoRoot, true
}
