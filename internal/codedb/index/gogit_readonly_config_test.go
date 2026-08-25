package index

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/go-git/go-billy/v6/osfs"
	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing/cache"
	"github.com/go-git/go-git/v6/storage/filesystem"
	"github.com/go-git/go-git/v6/storage/filesystem/dotgit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sageox/ox/internal/codedb/gitopen"
)

// Failure prevented: the daemon rewriting a managed source repo's .git/config
// (flipping core.bare, dropping extensions.worktreeConfig) when codedb opens it
// to index — which breaks every work-tree git command in the user's checkout
// until manually reset. See issue #819 and internal/codedb/gitopen.

// bugReportConfigShape rewrites .git/config into the shape from the #819 report:
// repositoryformatversion=1 (required for extensions), an extensions block with
// worktreeconfig=true, and a git-crypt filter with quoted values. Returns the
// snapshot bytes so tests can assert byte-identity afterward.
func bugReportConfigShape(t *testing.T, dir string) (cfgPath string, snapshot []byte) {
	t.Helper()
	cfgPath = filepath.Join(dir, ".git", "config")
	base, err := os.ReadFile(cfgPath)
	require.NoError(t, err)
	augmented := replaceFormatVersion(string(base)+
		"\n[extensions]\n\tworktreeconfig = true\n"+
		"[filter \"git-crypt\"]\n\tsmudge = \"git-crypt\" smudge\n\tclean = \"git-crypt\" clean\n", "1")
	require.NoError(t, os.WriteFile(cfgPath, []byte(augmented), 0o644))
	snapshot, err = os.ReadFile(cfgPath)
	require.NoError(t, err)
	return cfgPath, snapshot
}

// keepDescriptorsStorer builds the exact storer codedb's fast path uses.
func keepDescriptorsStorer(dir string) *filesystem.Storage {
	dot := osfs.New(filepath.Join(dir, ".git"), osfs.WithBoundOS())
	repositoryFs := dotgit.NewRepositoryFilesystem(dot, nil)
	return filesystem.NewStorageWithOptions(repositoryFs, cache.NewObjectLRUDefault(), filesystem.Options{
		KeepDescriptors: true,
	})
}

// TestReadOnlyConfigStorer_NeverWritesSourceConfig proves the guard intercepts
// the exact write contract that corrupts the config, with a negative control
// showing the unguarded storer DOES corrupt it (so this is not test theater).
func TestReadOnlyConfigStorer_NeverWritesSourceConfig(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git init")
	}
	dir, _ := initGitRepo(t, 1)
	cfgPath, snapshot := bugReportConfigShape(t, dir)
	wt := osfs.New(dir, osfs.WithBoundOS())

	// Guarded: SetConfig must be a no-op even when a caller drives the worst
	// case (flipping core.bare, the field that broke the user's checkout).
	repo, err := git.Open(gitopen.WrapReadOnlyConfig(keepDescriptorsStorer(dir)), wt)
	require.NoError(t, err)
	cfg, err := repo.Storer.Config()
	require.NoError(t, err)
	cfg.Core.IsBare = true
	require.NoError(t, repo.Storer.SetConfig(cfg)) // denied → no-op
	require.NoError(t, repo.Close())

	after, err := os.ReadFile(cfgPath)
	require.NoError(t, err)
	assert.Equal(t, string(snapshot), string(after),
		"guarded open must leave .git/config byte-identical (#819)")

	// Negative control: the UNGUARDED storer rewrites the file — this is the
	// hazard the guard exists to prevent. If go-git ever stopped writing here,
	// the guard would be unnecessary and this assertion would flag the change.
	require.NoError(t, os.WriteFile(cfgPath, snapshot, 0o644))
	repo2, err := git.Open(keepDescriptorsStorer(dir), wt)
	require.NoError(t, err)
	cfg2, err := repo2.Storer.Config()
	require.NoError(t, err)
	cfg2.Core.IsBare = true
	require.NoError(t, repo2.Storer.SetConfig(cfg2))
	require.NoError(t, repo2.Close())
	corrupted, err := os.ReadFile(cfgPath)
	require.NoError(t, err)
	assert.NotEqual(t, string(snapshot), string(corrupted),
		"sanity: unguarded go-git SetConfig rewrites .git/config (the #819 hazard)")
	assert.Contains(t, string(corrupted), "bare = true",
		"unguarded write flips core.bare — exactly what broke the reporter's checkout")
}

// TestPlainOpenTolerant_PreservesSourceConfig exercises the production open path
// end to end (open + blob read + close) and asserts .git/config is untouched.
// Green on the pinned go-git even pre-fix (its open path is read-only); its job
// is catching a future go-git bump that reintroduces open-time config writes.
func TestPlainOpenTolerant_PreservesSourceConfig(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git init")
	}
	dir, _ := initGitRepo(t, 1)
	cfgPath, snapshot := bugReportConfigShape(t, dir)

	repo, err := plainOpenTolerant(dir)
	require.NoError(t, err)
	head, err := repo.Head()
	require.NoError(t, err)
	commit, err := repo.CommitObject(head.Hash())
	require.NoError(t, err)
	_, err = repo.TreeObject(commit.TreeHash) // touch the object store like a real index
	require.NoError(t, err)
	require.NoError(t, repo.Close())

	after, err := os.ReadFile(cfgPath)
	require.NoError(t, err)
	assert.Equal(t, string(snapshot), string(after),
		"plainOpenTolerant must not rewrite .git/config (#819)")
}

// TestGuardedOpen_LinkedWorktree_PreservesSharedConfig covers the reporter's
// actual environment: a linked git worktree whose shared main .git/config is
// the file that got corrupted. Both the resolve-then-open path and a direct
// GuardedPlainOpen on the worktree dir must leave the shared config AND the
// per-worktree config.worktree byte-identical.
func TestGuardedOpen_LinkedWorktree_PreservesSharedConfig(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git worktree operations")
	}
	mainDir, _ := initGitRepo(t, 1)

	gitEnv := append(os.Environ(), // safe: git in temp dir, not ox subprocess
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@sageox.ai",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@sageox.ai")
	runGit := func(cwd string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = cwd
		cmd.Env = gitEnv
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}

	wtDir := filepath.Join(t.TempDir(), "wt")
	runGit(mainDir, "worktree", "add", wtDir)
	// Enable the worktreeConfig extension (requires format version 1), then set
	// a --worktree value so the per-worktree config.worktree file exists — the
	// exact shape whose extension the #819 rewrite dropped.
	runGit(mainDir, "config", "core.repositoryformatversion", "1")
	runGit(mainDir, "config", "extensions.worktreeConfig", "true")
	runGit(wtDir, "config", "--worktree", "core.bare", "false")

	mainCfg := filepath.Join(mainDir, ".git", "config")
	mainSnap, err := os.ReadFile(mainCfg)
	require.NoError(t, err)
	wtCfgPath := filepath.Join(mainDir, ".git", "worktrees", filepath.Base(wtDir), "config.worktree")
	wtSnap, err := os.ReadFile(wtCfgPath)
	require.NoError(t, err)

	assertUnchanged := func(label string, open func() (*git.Repository, error)) {
		repo, err := open()
		require.NoError(t, err, label)
		// drive a config write; the guard must swallow it
		if cfg, cerr := repo.Storer.Config(); cerr == nil {
			cfg.Core.IsBare = true
			_ = repo.Storer.SetConfig(cfg)
		}
		require.NoError(t, repo.Close())

		got, err := os.ReadFile(mainCfg)
		require.NoError(t, err)
		assert.Equal(t, string(mainSnap), string(got), "%s: shared .git/config changed", label)
		gotWt, err := os.ReadFile(wtCfgPath)
		require.NoError(t, err)
		assert.Equal(t, string(wtSnap), string(gotWt), "%s: config.worktree changed", label)
	}

	// (a) codedb's path: resolveGitDir(worktree) → main root → plainOpenTolerant.
	openPath, isWorktree := resolveGitDir(wtDir)
	assert.True(t, isWorktree, "wtDir must be detected as a linked worktree")
	assertUnchanged("resolve+plainOpenTolerant", func() (*git.Repository, error) {
		return plainOpenTolerant(openPath)
	})
	// (b) direct GuardedPlainOpen on the worktree dir.
	assertUnchanged("GuardedPlainOpen(worktree)", func() (*git.Repository, error) {
		return gitopen.GuardedPlainOpen(wtDir)
	})
}
