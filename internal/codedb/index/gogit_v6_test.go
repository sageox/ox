package index

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestV6_PlainOpenAcceptsKnownExtensions verifies that go-git v6 natively
// handles objectformat and worktreeconfig extensions without needing the
// extensionSafeStorer workaround. If this test starts failing, the
// workaround in plainOpenTolerant is needed again.
func TestV6_PlainOpenAcceptsKnownExtensions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		extension string
	}{
		{"objectformat_sha1", "\n[extensions]\n\tobjectformat = sha1\n"},
		{"objectformat_sha256", "\n[extensions]\n\tobjectformat = sha256\n"},
		{"worktreeconfig", "\n[extensions]\n\tworktreeconfig = true\n"},
		{"multiple_extensions", "\n[extensions]\n\tobjectformat = sha1\n\tworktreeconfig = true\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir, _ := initGitRepo(t, 1)

			gitConfig := filepath.Join(dir, ".git", "config")
			content, err := os.ReadFile(gitConfig)
			require.NoError(t, err)

			newContent := replaceFormatVersion(string(content)+tt.extension, "1")
			require.NoError(t, os.WriteFile(gitConfig, []byte(newContent), 0o644))

			// v6 should open directly — no fallback to extensionSafeStorer needed
			repo, err := git.PlainOpen(dir)
			require.NoError(t, err, "go-git v6 should accept %s natively", tt.name)

			head, err := repo.Head()
			require.NoError(t, err)
			assert.False(t, head.Hash().IsZero(), "HEAD should resolve to a valid commit")
		})
	}
}

// TestV6_BlobHashConsistency verifies that blob hashes read via go-git v6
// match those from the git CLI. Catches any object format or hashing
// divergence introduced by the v5→v6 upgrade.
func TestV6_BlobHashConsistency(t *testing.T) {
	t.Parallel()
	dir, _ := initGitRepo(t, 1)

	// get blob hash from git CLI
	cmd := exec.Command("git", "rev-parse", "HEAD:file1.go")
	cmd.Dir = dir
	cliOut, err := cmd.Output()
	require.NoError(t, err)
	cliBlobHash := string(cliOut[:len(cliOut)-1])

	// get blob hash from go-git v6
	repo, err := git.PlainOpen(dir)
	require.NoError(t, err)

	head, err := repo.Head()
	require.NoError(t, err)

	commit, err := repo.CommitObject(head.Hash())
	require.NoError(t, err)

	tree, err := repo.TreeObject(commit.TreeHash)
	require.NoError(t, err)

	var goGitBlobHash string
	err = tree.Files().ForEach(func(f *object.File) error {
		if f.Name == "file1.go" {
			goGitBlobHash = f.Hash.String()
		}
		return nil
	})
	require.NoError(t, err)

	assert.Equal(t, cliBlobHash, goGitBlobHash,
		"go-git v6 blob hash must match git CLI (hash divergence = silent data corruption)")
}

// TestV6_TreeFilesReturnsFullPaths verifies that tree.Files().ForEach returns
// full relative paths (e.g., "src/main.go") not just basenames ("main.go").
// If this behavior changed in v6, diff detection would silently break.
func TestV6_TreeFilesReturnsFullPaths(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), // safe: git in temp dir, not ox subprocess
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=t@t.com",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=t@t.com")
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}

	run("init", "-b", "main")

	// create files in subdirectories
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "src", "pkg"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "root.go"), []byte("package main"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "src", "main.go"), []byte("package main"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "src", "pkg", "util.go"), []byte("package pkg"), 0o644))

	run("add", ".")
	run("commit", "-m", "init")

	repo, err := git.PlainOpen(dir)
	require.NoError(t, err)

	head, err := repo.Head()
	require.NoError(t, err)

	commit, err := repo.CommitObject(head.Hash())
	require.NoError(t, err)

	tree, err := repo.TreeObject(commit.TreeHash)
	require.NoError(t, err)

	paths := make(map[string]bool)
	err = tree.Files().ForEach(func(f *object.File) error {
		paths[f.Name] = true
		return nil
	})
	require.NoError(t, err)

	// must contain full relative paths, not just basenames
	assert.True(t, paths["root.go"], "should contain root.go")
	assert.True(t, paths["src/main.go"], "should contain src/main.go (full path)")
	assert.True(t, paths["src/pkg/util.go"], "should contain src/pkg/util.go (full path)")

	// must NOT contain bare filenames for nested files
	assert.False(t, paths["main.go"], "must not contain bare 'main.go' (would break diff detection)")
	assert.False(t, paths["util.go"], "must not contain bare 'util.go' (would break diff detection)")
}

// TestV6_CloneBareAndResolveHead verifies that CloneOrFetch produces a bare
// repo where HEAD can be resolved — the entire IndexRepo path depends on this.
func TestV6_CloneBareAndResolveHead(t *testing.T) {
	t.Parallel()
	srcDir, tipHash := initGitRepo(t, 3)
	bareDir := filepath.Join(t.TempDir(), "bare.git")

	repo, err := CloneOrFetch("file://"+srcDir, bareDir)
	require.NoError(t, err, "CloneOrFetch should succeed")

	ref, err := resolveDefaultBranch(repo)
	require.NoError(t, err, "HEAD must be resolvable on bare clone")
	assert.Equal(t, plumbing.NewHash(tipHash), ref.tipOID,
		"bare clone HEAD must match source tip")
}

// TestV6_CloneOrFetchUpdatesOnRefetch verifies that calling CloneOrFetch on
// an existing bare repo fetches new commits. The current code swallows fetch
// errors (_ = fetch(repo)), so a broken fetch would be invisible.
func TestV6_CloneOrFetchUpdatesOnRefetch(t *testing.T) {
	t.Parallel()
	srcDir, _ := initGitRepo(t, 2)
	bareDir := filepath.Join(t.TempDir(), "bare.git")

	// initial clone
	_, err := CloneOrFetch("file://"+srcDir, bareDir)
	require.NoError(t, err)

	// add another commit to source
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = srcDir
		cmd.Env = append(os.Environ(), // safe: git in temp dir, not ox subprocess
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=t@t.com",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=t@t.com")
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
	require.NoError(t, os.WriteFile(filepath.Join(srcDir, "new.go"), []byte("package new"), 0o644))
	run("add", "new.go")
	run("commit", "-m", "commit 3")

	// get new tip from CLI
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = srcDir
	newTipOut, err := cmd.Output()
	require.NoError(t, err)
	newTip := string(newTipOut[:len(newTipOut)-1])

	// re-fetch
	repo, err := CloneOrFetch("file://"+srcDir, bareDir)
	require.NoError(t, err)

	ref, err := resolveDefaultBranch(repo)
	require.NoError(t, err)
	assert.Equal(t, plumbing.NewHash(newTip), ref.tipOID,
		"re-fetch should update bare repo to new tip")
}

// TestV6_GetTreeEntriesWithSubdirs verifies getTreeEntries returns full
// paths for files in subdirectories when called through the indexing pipeline.
func TestV6_GetTreeEntriesWithSubdirs(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), // safe: git in temp dir, not ox subprocess
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=t@t.com",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=t@t.com")
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}

	run("init", "-b", "main")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "internal", "pkg"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "internal", "server.go"), []byte("package internal"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "internal", "pkg", "helper.go"), []byte("package pkg"), 0o644))
	run("add", ".")
	run("commit", "-m", "init")

	repo, err := plainOpenTolerant(dir)
	require.NoError(t, err)

	head, err := repo.Head()
	require.NoError(t, err)

	commit, err := repo.CommitObject(head.Hash())
	require.NoError(t, err)

	cache := make(map[plumbing.Hash]map[string]plumbing.Hash)
	entries, err := getTreeEntries(repo, commit.TreeHash, cache)
	require.NoError(t, err)

	// verify full relative paths
	_, hasMain := entries["main.go"]
	_, hasServer := entries["internal/server.go"]
	_, hasHelper := entries["internal/pkg/helper.go"]

	assert.True(t, hasMain, "should have main.go")
	assert.True(t, hasServer, "should have internal/server.go")
	assert.True(t, hasHelper, "should have internal/pkg/helper.go")
	assert.Len(t, entries, 3, "should have exactly 3 entries")
}

// TestV6_HashZeroValueComparison verifies that plumbing.Hash zero-value
// checks work correctly in v6. The indexer uses (plumbing.Hash{}) comparison
// at getTreeEntries to reject zero hashes.
func TestV6_HashZeroValueComparison(t *testing.T) {
	t.Parallel()

	var zero plumbing.Hash
	assert.True(t, zero == (plumbing.Hash{}), "zero-value Hash must equal Hash{}")
	assert.True(t, zero.IsZero(), "zero-value Hash.IsZero() must be true")

	nonZero := plumbing.NewHash("abc123")
	assert.False(t, nonZero == (plumbing.Hash{}), "non-zero Hash must not equal Hash{}")
	assert.False(t, nonZero.IsZero(), "non-zero Hash.IsZero() must be false")

	// verify Hash works as map key (used in treeCache, visited sets)
	m := make(map[plumbing.Hash]bool)
	h1 := plumbing.NewHash("abc123")
	h2 := plumbing.NewHash("abc123")
	m[h1] = true
	assert.True(t, m[h2], "equal hashes must be identical map keys")
}
