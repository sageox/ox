package index

import (
	"context"

	"github.com/go-git/go-git/v6/plumbing"
)

// treeChangeKind classifies one file-level change between two trees.
type treeChangeKind int

const (
	treeChangeInsert treeChangeKind = iota + 1
	treeChangeModify
	treeChangeDelete
)

// treeChange is one changed path between a parent tree and a child tree.
//
// Every treeDiffer reports the same set of changes as the go-git merkletrie
// diff after indexCommit's file filter: an insert or modify only when the new
// entry is a file (regular, executable or symlink), a delete only when the old
// entry is a file. Directories and submodules (gitlinks) never appear. Renames
// are reported as a delete plus an insert. Order is unspecified.
type treeChange struct {
	Kind    treeChangeKind
	Path    string
	OldHash plumbing.Hash // zero for inserts
	NewHash plumbing.Hash // zero for deletes
}

// treeDiffer computes file-level changes between two trees. parent is
// plumbing.ZeroHash for a root commit, which diffs against the empty tree.
type treeDiffer interface {
	DiffTrees(ctx context.Context, parent, child plumbing.Hash) ([]treeChange, error)
	Close() error
}
