package index

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-git/go-git/v6/plumbing"
	"github.com/stretchr/testify/require"
)

// tdGoGitFixture builds four commits that exercise every filter indexCommit
// applies: modify, delete, insert, an executable file, a symlink (a file), and
// a gitlink (not a file) that is added and then changed.
func tdGoGitFixture(t *testing.T) (dir string, commits []string) {
	t.Helper()
	if testing.Short() {
		t.Skip("short: builds a real git repository for CodeDB indexing")
	}
	dir = t.TempDir()
	bfGit(t, dir, "init", "-q", "-b", "main")

	commits = append(commits, bfCommit(t, dir, "c1", map[string]string{
		"a.go":     "package a\n",
		"dir/b.go": "package b\n",
		"run.sh":   "#!/bin/sh\n",
	}))
	commits = append(commits, bfCommit(t, dir, "c2", map[string]string{
		"a.go": "package a\nvar X = 1\n",
		"c.go": "package c\n",
	}, "dir/b.go"))

	require.NoError(t, os.Symlink("a.go", filepath.Join(dir, "ln")))
	bfGit(t, dir, "add", "--", "ln")
	bfGit(t, dir, "update-index", "--add", "--cacheinfo", "160000,1111111111111111111111111111111111111111,vendor/sub")
	bfGit(t, dir, "commit", "-q", "-m", "c3")
	commits = append(commits, bfGit(t, dir, "rev-parse", "HEAD"))

	bfGit(t, dir, "update-index", "--cacheinfo", "160000,2222222222222222222222222222222222222222,vendor/sub")
	bfGit(t, dir, "commit", "-q", "-m", "c4")
	commits = append(commits, bfGit(t, dir, "rev-parse", "HEAD"))
	return dir, commits
}

func TestGoGitTreeDiffer_DiffTrees(t *testing.T) {
	t.Parallel()
	dir, commits := tdGoGitFixture(t)
	repo, err := plainOpenTolerant(dir)
	require.NoError(t, err)
	t.Cleanup(func() { repo.Close() })
	// the go-git differ directly: newTreeDiffer would prefer the git process
	differ := &goGitTreeDiffer{repo: repo}
	t.Cleanup(func() { require.NoError(t, differ.Close()) })

	blob := func(commit, path string) plumbing.Hash {
		return plumbing.NewHash(bfGit(t, dir, "rev-parse", commit+":"+path))
	}
	tree := func(commit string) plumbing.Hash {
		return plumbing.NewHash(bfGit(t, dir, "rev-parse", commit+"^{tree}"))
	}
	c1, c2, c3, c4 := commits[0], commits[1], commits[2], commits[3]
	missing := plumbing.NewHash("3333333333333333333333333333333333333333")

	rootChanges := []treeChange{
		{Kind: treeChangeInsert, Path: "a.go", NewHash: blob(c1, "a.go")},
		{Kind: treeChangeInsert, Path: "dir/b.go", NewHash: blob(c1, "dir/b.go")},
		{Kind: treeChangeInsert, Path: "run.sh", NewHash: blob(c1, "run.sh")},
	}

	tests := []struct {
		name    string
		parent  plumbing.Hash
		child   plumbing.Hash
		want    []treeChange
		wantErr bool
	}{
		{"root commit diffs against the empty tree", plumbing.ZeroHash, tree(c1), rootChanges, false},
		{"modify, delete and insert", tree(c1), tree(c2), []treeChange{
			{Kind: treeChangeModify, Path: "a.go", OldHash: blob(c1, "a.go"), NewHash: blob(c2, "a.go")},
			{Kind: treeChangeDelete, Path: "dir/b.go", OldHash: blob(c1, "dir/b.go")},
			{Kind: treeChangeInsert, Path: "c.go", NewHash: blob(c2, "c.go")},
		}, false},
		{"a symlink counts as a file, a gitlink does not", tree(c2), tree(c3), []treeChange{
			{Kind: treeChangeInsert, Path: "ln", NewHash: blob(c3, "ln")},
		}, false},
		{"a changed gitlink reports nothing", tree(c3), tree(c4), nil, false},
		{"identical trees report nothing", tree(c4), tree(c4), nil, false},
		{"an unreadable parent tree degrades to the empty tree", missing, tree(c1), rootChanges, false},
		{"a missing child tree is an error", tree(c1), missing, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := differ.DiffTrees(context.Background(), tt.parent, tt.child)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.ElementsMatch(t, tt.want, got) // order is unspecified
		})
	}
}

func TestIndexState_ParentTreeHash(t *testing.T) {
	t.Parallel()
	dir, commits := tdGoGitFixture(t)
	repo, err := plainOpenTolerant(dir)
	require.NoError(t, err)
	t.Cleanup(func() { repo.Close() })
	st := &indexState{repo: repo}

	parent := plumbing.NewHash(commits[0])
	parentTree := plumbing.NewHash(bfGit(t, dir, "rev-parse", commits[0]+"^{tree}"))
	missing := plumbing.NewHash("3333333333333333333333333333333333333333")
	walkTree := plumbing.NewHash("4444444444444444444444444444444444444444")

	tests := []struct {
		name string
		cd   commitData
		want plumbing.Hash
	}{
		{"root commit", commitData{}, plumbing.ZeroHash},
		{"tree handed over by the walk is used without a decode", commitData{parentIDs: []plumbing.Hash{missing}, parentTree: walkTree}, walkTree},
		{"parent outside the walk is decoded", commitData{parentIDs: []plumbing.Hash{parent}}, parentTree},
		{"unreadable parent diffs against the empty tree", commitData{parentIDs: []plumbing.Hash{missing}}, plumbing.ZeroHash},
		{"only the first parent counts", commitData{parentIDs: []plumbing.Hash{parent, missing}}, parentTree},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, st.parentTreeHash(tt.cd))
		})
	}
}

func TestGitDirOf(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git operations")
	}

	normal := t.TempDir()
	bfGit(t, normal, "init", "-q", "-b", "main")
	bare := t.TempDir()
	bfGit(t, bare, "init", "-q", "--bare")
	empty := t.TempDir()
	objectsOnly := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(objectsOnly, "objects"), 0o755))
	submodule := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(submodule, ".git"), []byte("gitdir: ../.git/modules/x\n"), 0o644))

	tests := []struct {
		name string
		path string
		want string
	}{
		{"checkout", normal, filepath.Join(normal, ".git")},
		{"bare repository is its own git dir", bare, bare},
		{"not a repository", empty, ""},
		{"objects dir without HEAD", objectsOnly, ""},
		{"submodule .git file is not resolved", submodule, ""},
		{"missing path", filepath.Join(empty, "nope"), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, gitDirOf(tt.path))
		})
	}
}
