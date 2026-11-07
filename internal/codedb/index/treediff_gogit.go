package index

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/go-git/go-git/v6/utils/merkletrie"
)

// goGitTreeDiffer diffs trees in-process with go-git's merkletrie. It is the
// reference implementation of treeDiffer and the fallback for any other one.
type goGitTreeDiffer struct {
	repo *git.Repository
}

// newTreeDiffer returns the treeDiffer an index run uses for its commits.
//
// It prefers one long-lived `git diff-tree --stdin` process: decoding every
// tree in go-git dominated the daemon's CPU profile on large histories (tree
// decode, flate and GC). go-git stays as the fallback when gitDir is unknown,
// git cannot start, or the process fails mid-run. The process lives until
// Close or until ctx ends.
func newTreeDiffer(ctx context.Context, repo *git.Repository, gitDir string) treeDiffer {
	goGit := &goGitTreeDiffer{repo: repo}
	if gitDir == "" {
		return goGit
	}
	gitDiffer, err := newGitTreeDiffer(ctx, gitDir)
	if err != nil {
		slog.Info("codedb tree diff using go-git", "git_dir", gitDir, "reason", err)
		return goGit
	}
	return &fallbackTreeDiffer{
		primary:  gitDiffer,
		fallback: goGit,
		onFallback: func(err error) {
			slog.Warn("codedb tree diff fell back to go-git", "git_dir", gitDir, "error", err)
		},
	}
}

// DiffTrees uses object.DiffTreeContext: merkle-trie hash comparison that only
// descends into subtrees whose hash changed, identical to git diff-tree
// semantics, so the cost is O(changed files) rather than O(all files).
func (d *goGitTreeDiffer) DiffTrees(ctx context.Context, parent, child plumbing.Hash) ([]treeChange, error) {
	childTree, err := d.repo.TreeObject(child)
	if err != nil {
		return nil, fmt.Errorf("get child tree: %w", err)
	}

	// An unreadable parent tree degrades to a diff against the empty tree, as
	// indexCommit always did, rather than failing the whole index run.
	var parentTree *object.Tree
	if !parent.IsZero() {
		parentTree, _ = d.repo.TreeObject(parent)
	}

	changes, err := object.DiffTreeContext(ctx, parentTree, childTree)
	if err != nil {
		return nil, fmt.Errorf("merkletrie diff: %w", err)
	}

	out := make([]treeChange, 0, len(changes))
	for _, change := range changes {
		action, err := change.Action()
		if err != nil {
			continue
		}
		switch action {
		case merkletrie.Insert:
			if !change.To.TreeEntry.Mode.IsFile() {
				continue // skip submodules and directories
			}
			out = append(out, treeChange{Kind: treeChangeInsert, Path: change.To.Name, NewHash: change.To.TreeEntry.Hash})
		case merkletrie.Modify:
			if !change.To.TreeEntry.Mode.IsFile() {
				continue // skip submodules and directories
			}
			out = append(out, treeChange{
				Kind:    treeChangeModify,
				Path:    change.To.Name,
				OldHash: change.From.TreeEntry.Hash,
				NewHash: change.To.TreeEntry.Hash,
			})
		case merkletrie.Delete:
			if !change.From.TreeEntry.Mode.IsFile() {
				continue // skip submodules and directories
			}
			out = append(out, treeChange{Kind: treeChangeDelete, Path: change.From.Name, OldHash: change.From.TreeEntry.Hash})
		}
	}
	return out, nil
}

// Close is a no-op: the go-git differ holds no resources beyond the repository,
// which the caller owns.
func (d *goGitTreeDiffer) Close() error { return nil }

// gitDirOf returns the git directory of the repository that go-git was opened
// on at openPath, or "" when it cannot be told cheaply (a submodule's .git
// file). Normal and linked-worktree checkouts resolve to "<root>/.git"; a bare
// repository, such as codedb's own clone cache, is its own git directory.
func gitDirOf(openPath string) string {
	dotGit := filepath.Join(openPath, ".git")
	if fi, err := os.Stat(dotGit); err == nil && fi.IsDir() {
		return dotGit
	}
	if fi, err := os.Stat(filepath.Join(openPath, "objects")); err != nil || !fi.IsDir() {
		return ""
	}
	if _, err := os.Stat(filepath.Join(openPath, "HEAD")); err != nil {
		return ""
	}
	return openPath
}
