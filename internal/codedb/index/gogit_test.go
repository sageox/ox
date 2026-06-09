package index

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func replaceFormatVersion(content, version string) string {
	return strings.Replace(content,
		"repositoryformatversion = 0",
		"repositoryformatversion = "+version, 1)
}

func TestPlainOpenTolerant_NormalRepo(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	dir, _ := initGitRepo(t, 1)

	repo, err := plainOpenTolerant(dir)
	require.NoError(t, err)
	assert.NotNil(t, repo)
}

// TestPlainOpenTolerant_RepoWithExtensions verifies that repos with extensions
// and repositoryformatversion=1 open correctly. go-git v6 handles known
// extensions natively (objectformat, worktreeconfig).
func TestPlainOpenTolerant_RepoWithExtensions(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	dir, _ := initGitRepo(t, 1)

	gitConfig := filepath.Join(dir, ".git", "config")
	content, err := os.ReadFile(gitConfig)
	require.NoError(t, err)

	// extensions require repositoryformatversion=1
	newContent := replaceFormatVersion(string(content), "1")
	newContent += "\n[extensions]\n\tobjectformat = sha1\n"
	require.NoError(t, os.WriteFile(gitConfig, []byte(newContent), 0o644))

	repo, err := plainOpenTolerant(dir)
	require.NoError(t, err)
	assert.NotNil(t, repo)
}

func TestPlainOpenTolerant_FormatV1WithUnknownExtension(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	dir, _ := initGitRepo(t, 1)

	gitConfig := filepath.Join(dir, ".git", "config")
	content, err := os.ReadFile(gitConfig)
	require.NoError(t, err)

	newContent := replaceFormatVersion(string(content), "1")
	newContent += "\n[extensions]\n\tobjectformat = sha256\n"
	require.NoError(t, os.WriteFile(gitConfig, []byte(newContent), 0o644))

	repo, err := plainOpenTolerant(dir)
	require.NoError(t, err)
	assert.NotNil(t, repo)
}

func TestPlainOpenTolerant_NonRepoPath(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	dir := t.TempDir()

	repo, err := plainOpenTolerant(dir)
	assert.Error(t, err)
	assert.Nil(t, repo)
}

// TestPlainOpenTolerant_KeepDescriptors verifies that plainOpenTolerant uses
// KeepDescriptors for better performance. The repo must be readable (HEAD,
// tree, and blob objects all accessible) via the KeepDescriptors path.
func TestPlainOpenTolerant_KeepDescriptors(t *testing.T) {
	t.Parallel()
	dir, _ := initGitRepo(t, 3) // 3 commits → packfile after gc

	repo, err := plainOpenTolerant(dir)
	require.NoError(t, err)
	require.NotNil(t, repo)

	// Verify HEAD is resolvable
	head, err := repo.Head()
	require.NoError(t, err, "HEAD must be resolvable via KeepDescriptors path")
	require.False(t, head.Hash().IsZero())

	// Verify commit object is readable
	commit, err := repo.CommitObject(head.Hash())
	require.NoError(t, err, "commit object must be readable via KeepDescriptors path")

	// Verify tree and blob objects are readable — this exercises the packfile path
	tree, err := repo.TreeObject(commit.TreeHash)
	require.NoError(t, err, "tree object must be readable")

	err = tree.Files().ForEach(func(f *object.File) error {
		_, rErr := f.Contents()
		return rErr
	})
	require.NoError(t, err, "blob contents must be readable via KeepDescriptors path")
}

// TestPlainOpenTolerant_WorktreeWithExtensions verifies that linked worktrees
// whose main repo has extensions can be opened. The worktree's .git is a file
// pointing to the main repo, so extensions are read from the main repo's config.
func TestPlainOpenTolerant_WorktreeWithExtensions(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	mainDir, _ := initGitRepo(t, 1)

	worktreeDir := createLinkedWorktree(t, mainDir, "ext-test")

	// extensions require repositoryformatversion=1
	gitConfig := filepath.Join(mainDir, ".git", "config")
	content, err := os.ReadFile(gitConfig)
	require.NoError(t, err)

	newContent := replaceFormatVersion(string(content), "1")
	newContent += "\n[extensions]\n\tobjectformat = sha1\n"
	require.NoError(t, os.WriteFile(gitConfig, []byte(newContent), 0o644))

	repoOpenPath, _ := resolveGitDir(worktreeDir)
	repo, err := plainOpenTolerant(repoOpenPath)
	require.NoError(t, err)
	assert.NotNil(t, repo)
}

// TestPlainOpenTolerant_AlternatesUpstreamLimitation pins the current
// go-git v6 behavior with .git/objects/info/alternates: object lookups fail
// because v6's storage layer (both via plainOpenTolerant's custom
// KeepDescriptors path AND vanilla git.PlainOpen) does NOT read the
// alternates file. Native git CLI handles this correctly; go-git does not.
//
// Subtests cover both alternates-path shapes — relative (common in --reference
// setups and manual configurations) and absolute (default for
// `git clone --shared`). Both fail today.
//
// Failure prevented: codedb's IndexLocalRepo (internal/codedb/index/indexer.go:310)
// opens user-controlled paths via plainOpenTolerant. If a user's repo uses
// shared/reference clones, codedb would silently fail or index incomplete
// history. Step 3 (ox-5b5p) of the shallow+shared epic gates IndexLocalRepo
// on alternates absence and surfaces a clear "alternates unsupported" error
// instead of producing wrong output.
//
// When this test starts FAILING — i.e., go-git v6 (or a successor) adds
// alternates support — remove the IndexLocalRepo gate and flip the assertions
// here to `require.NoError`. The test then becomes a positive regression test.
func TestPlainOpenTolerant_AlternatesUpstreamLimitation(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git clone operations")
	}

	cases := []struct {
		name         string
		makeRelative bool
	}{
		{name: "absolute_path", makeRelative: false},
		{name: "relative_path", makeRelative: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// Build source repo with 3 commits (forces packfile creation under gc).
			srcDir, _ := initGitRepo(t, 3)
			runGitIn(t, srcDir, "gc", "--quiet")

			// Clone with --shared so destination's alternates references source.
			// --no-checkout keeps working tree empty; we exercise the object store.
			parent := t.TempDir()
			dstDir := filepath.Join(parent, "shared-clone")
			cloneCmd := exec.Command("git", "clone", "--shared", "--no-checkout", srcDir, dstDir)
			cloneCmd.Env = append(os.Environ(), // safe: git CLI in temp dir, not ox subprocess
				"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@sageox.ai",
				"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@sageox.ai")
			out, err := cloneCmd.CombinedOutput()
			require.NoError(t, err, "git clone --shared: %s", out)

			altsPath := filepath.Join(dstDir, ".git", "objects", "info", "alternates")
			contents, err := os.ReadFile(altsPath)
			require.NoError(t, err)
			require.True(t, filepath.IsAbs(strings.TrimSpace(string(contents))),
				"git clone --shared should write absolute alternates path by default")

			if tc.makeRelative {
				srcObjects := filepath.Join(srcDir, ".git", "objects")
				rel, relErr := filepath.Rel(filepath.Dir(altsPath), srcObjects)
				require.NoError(t, relErr)
				require.Contains(t, rel, "..", "rewrite must be relative (contain ../)")
				require.NoError(t, os.WriteFile(altsPath, []byte(rel+"\n"), 0o644))
			}

			// Sanity: destination has no own objects — all reads must traverse
			// alternates. Without this, the test could pass with broken
			// alternates handling because git put loose objects locally.
			entries, err := os.ReadDir(filepath.Join(dstDir, ".git", "objects"))
			require.NoError(t, err)
			for _, e := range entries {
				if e.Name() == "info" || e.Name() == "pack" {
					continue
				}
				t.Fatalf("unexpected local object dir %q — test setup leaked objects out of alternates", e.Name())
			}

			// Repo opens fine — go-git only fails when it tries to read objects.
			repo, err := plainOpenTolerant(dstDir)
			require.NoError(t, err, "plainOpenTolerant should accept the repo")

			head, err := repo.Head()
			require.NoError(t, err, "HEAD ref is in .git/HEAD, no objects needed")

			// Pin the upstream limitation. Object lookups need the object store,
			// which lives behind alternates — go-git v6 doesn't follow.
			_, err = repo.CommitObject(head.Hash())
			require.Error(t, err,
				"upstream go-git v6 limitation: alternates not honored. "+
					"If this test starts passing, remove the IndexLocalRepo "+
					"alternates gate (see ox-5b5p) and flip this to require.NoError.")
			require.Contains(t, err.Error(), "object not found",
				"expected 'object not found' from alternates lookup failure")
		})
	}
}

// runGitIn is a tiny helper for the alternates test — runs `git <args>` in dir.
func runGitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), // safe: git CLI in temp dir, not ox subprocess
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@sageox.ai",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@sageox.ai")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
}
