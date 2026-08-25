package index

import (
	"context"
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
	"github.com/sageox/ox/internal/codedb/store"
	"github.com/sageox/ox/internal/testguard"
)

// gitRunner returns a helper that runs git in cwd with a deterministic identity.
// Uses testguard.MinimalEnv (an allowlist), which strips GIT_DIR/GIT_WORK_TREE/
// GIT_COMMON_DIR so an inherited routing var can't retarget git at a repo
// outside the temp dir — cmd.Dir alone decides the target.
func gitRunner(t *testing.T) func(cwd string, args ...string) {
	t.Helper()
	env := testguard.MinimalEnv([]string{
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@sageox.ai",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@sageox.ai",
	})
	return func(cwd string, args ...string) {
		t.Helper()
		// Disable commit/tag signing so tests don't depend on the developer's
		// global git config (SSH/GPG signing key + agent, which MinimalEnv's
		// clean env intentionally cannot reach).
		full := append([]string{"-c", "commit.gpgsign=false", "-c", "tag.gpgsign=false"}, args...)
		cmd := exec.Command("git", full...)
		cmd.Dir = cwd
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
}

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
	require.Contains(t, string(snapshot), "bare = false", "fixture precondition")
	assert.NotContains(t, string(corrupted), "bare = false",
		"unguarded write flipped core.bare away from false — exactly what broke the reporter's checkout")
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

	runGit := gitRunner(t)

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
		// drive a config write; the guard must swallow it. require (not if) so a
		// Config() error can't make this assertion pass vacuously.
		cfg, cerr := repo.Storer.Config()
		require.NoError(t, cerr, label)
		cfg.Core.IsBare = true
		require.NoError(t, repo.Storer.SetConfig(cfg), label)
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

// TestIndexLocalRepo_PreservesWorktreeConfig drives the REAL production pipeline
// (IndexLocalRepo over a linked worktree) and asserts it never rewrites the
// shared .git/config or config.worktree. On the pinned go-git this open path is
// read-only, so it is green with and without the guard — it is a sentinel that
// fails loudly if a future go-git bump reintroduces open-time config writes on
// the production path (the #819 class). The version-independent proof that the
// guard itself works is TestReadOnlyConfigStorer_NeverWritesSourceConfig.
func TestIndexLocalRepo_PreservesWorktreeConfig(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git worktree + indexing")
	}
	mainDir, _ := initGitRepo(t, 3)
	worktreeDir := createLinkedWorktree(t, mainDir, "idx-cfg-branch")

	runGit := gitRunner(t)
	runGit(mainDir, "config", "core.repositoryformatversion", "1")
	runGit(mainDir, "config", "extensions.worktreeConfig", "true")
	runGit(worktreeDir, "config", "--worktree", "core.bare", "false")

	mainCfg := filepath.Join(mainDir, ".git", "config")
	mainSnap, err := os.ReadFile(mainCfg)
	require.NoError(t, err)
	wtCfg := filepath.Join(mainDir, ".git", "worktrees", filepath.Base(worktreeDir), "config.worktree")
	wtSnap, err := os.ReadFile(wtCfg)
	require.NoError(t, err)

	dataDir := filepath.Join(t.TempDir(), "codedb")
	require.NoError(t, os.MkdirAll(dataDir, 0o755))
	s, err := store.Open(dataDir)
	require.NoError(t, err)
	defer s.Close()

	require.NoError(t, IndexLocalRepo(context.Background(), s, worktreeDir, IndexOptions{}))

	got, err := os.ReadFile(mainCfg)
	require.NoError(t, err)
	assert.Equal(t, string(mainSnap), string(got), "IndexLocalRepo rewrote the shared .git/config")
	gotWt, err := os.ReadFile(wtCfg)
	require.NoError(t, err)
	assert.Equal(t, string(wtSnap), string(gotWt), "IndexLocalRepo rewrote config.worktree")
}

// TestGuardedOpen_Submodule_PreservesConfig covers the .git-is-a-file submodule
// layout: its gitdir (.git/modules/<name>) is self-contained (no commondir), so
// ResolveGitDir does not remap it. Before the fix, GuardedPlainOpen fell through
// to an unguarded git.PlainOpen for this shape, leaving the submodule's config
// writable. Found by adversarial review of #819.
func TestGuardedOpen_Submodule_PreservesConfig(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git submodule operations")
	}
	subSrc, _ := initGitRepo(t, 2)
	mainDir, _ := initGitRepo(t, 1)

	runGit := gitRunner(t)
	// local-path submodules need protocol.file.allow=always on modern git
	runGit(mainDir, "-c", "protocol.file.allow=always", "submodule", "add", subSrc, "sub")
	runGit(mainDir, "-c", "protocol.file.allow=always", "commit", "-m", "add submodule")

	subPath := filepath.Join(mainDir, "sub")
	require.FileExists(t, filepath.Join(subPath, ".git")) // .git is a FILE for a submodule
	subCfg := filepath.Join(mainDir, ".git", "modules", "sub", "config")
	require.FileExists(t, subCfg)

	// give the submodule config the bug shape and snapshot it
	base, err := os.ReadFile(subCfg)
	require.NoError(t, err)
	aug := replaceFormatVersion(string(base)+
		"\n[filter \"git-crypt\"]\n\tsmudge = \"git-crypt\" smudge\n", "1")
	require.NoError(t, os.WriteFile(subCfg, []byte(aug), 0o644))
	snap, err := os.ReadFile(subCfg)
	require.NoError(t, err)

	// sanity: this is NOT a linked worktree, so it must go through the submodule
	// guard branch (not ResolveGitDir's remap).
	_, isWorktree := resolveGitDir(subPath)
	assert.False(t, isWorktree, "submodule must not be classified as a linked worktree")

	repo, err := gitopen.GuardedPlainOpen(subPath)
	require.NoError(t, err)
	cfg, err := repo.Storer.Config()
	require.NoError(t, err)
	cfg.Core.IsBare = true
	require.NoError(t, repo.Storer.SetConfig(cfg)) // denied → no-op
	require.NoError(t, repo.Close())

	after, err := os.ReadFile(subCfg)
	require.NoError(t, err)
	assert.Equal(t, string(snap), string(after),
		"submodule .git/modules/<name>/config must be byte-identical (#819)")
}
