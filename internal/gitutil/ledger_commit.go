package gitutil

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/internal/sacred"
)

// CommitLedgerSnapshot commits the ledger index as an IMMUTABLE, pre-validated
// tree, closing the validation↔commit TOCTOU that a `git add` + validate +
// `git commit` pair leaves open (PR #811 review, Greptile P1; PR #910 review,
// CodeRabbit).
//
// Two porcelain shapes both re-read mutable state at commit time:
//
//   - a bare `git commit` re-reads the whole INDEX, so a concurrent daemon
//     `pull --rebase --autostash` can stage an unchecked blob (a stash-pop
//     conflict's markers) between the check and the commit;
//   - `git commit -- <pathspec>` re-reads the WORKTREE at those paths, so any
//     writer that does not take the advisory repo lock — raw human git, an
//     editor, the session recorder — can swap validated bytes for unvalidated
//     ones after validation ran.
//
// This commits exactly the tree it validated:
//
//  1. `git write-tree` on the repository index fails closed on unmerged
//     (UU/UD) entries, so a live conflict never reaches step 2.
//  2. With pathspecs, a temporary index is built from HEAD plus the
//     repository index's entries under those paths (deletions included) and
//     snapshotted with write-tree; that tree is what `git commit -- <pathspec>`
//     would have produced had it read the index instead of the worktree.
//     Without pathspecs the whole-index tree from step 1 is used.
//  3. Only the blobs the tree changes vs its parent are validated: index mode
//     (ValidateLedgerEntryMode) and content (ValidateLedgerBlob). An
//     unchanged blob was vetted when first committed; scanning the whole tree
//     would cost O(repo) and risk flagging historical content.
//  4. ADR-024 backstop: the same immutable delta is refused if it would delete
//     a mass of sacred files.
//  5. `git commit-tree` commits that exact tree and `git update-ref` advances
//     the branch with a compare-and-swap on the old tip — a concurrent writer
//     is neither swept into THIS commit nor able to silently clobber the ref.
//
// This is what `git commit` does internally (write-tree → commit-tree →
// update-ref); the only differences are the validation in the middle, the
// immutability of the committed tree, and that hooks never run (callers
// previously passed --no-verify, so nothing is lost).
//
// The caller MUST hold WithRepoLock from before its first git add through
// this call: the lock is what keeps other ox processes from replacing index
// entries mid-transaction; the immutable tree is what keeps everyone else out.
//
// Returns committed=false with nil error when the snapshot equals the parent's
// tree — the "nothing to commit" idempotency callers rely on.
func CommitLedgerSnapshot(ctx context.Context, repoPath, message string, pathspecs ...string) (committed bool, err error) {
	// Never mutate the ledger mid-rebase: moving the branch ref under a rebase
	// consumes the replay step (see .claude/rules/cache-only-design.md).
	if err := IsSafeForGitOps(repoPath); err != nil {
		return false, fmt.Errorf("unsafe Ledger commit: %w", err)
	}

	// Global fail-closed check on the real index, even for a path-scoped
	// commit: git refuses every commit while any stage is unresolved, and the
	// temporary index below would otherwise hide the conflict from write-tree.
	tree, err := writeIndexTree(ctx, repoPath, nil)
	if err != nil {
		return false, err
	}
	parent, err := currentBranchTip(ctx, repoPath)
	if err != nil {
		return false, err
	}

	if len(pathspecs) > 0 {
		tree, err = writeScopedIndexTree(ctx, repoPath, parent, pathspecs)
		if err != nil {
			return false, err
		}
	}

	if parent != "" {
		parentTree, err := cleanGitOutput(ctx, repoPath, "rev-parse", "--verify", parent+"^{tree}")
		if err != nil {
			return false, fmt.Errorf("resolve parent tree: %w", err)
		}
		if strings.TrimSpace(string(parentTree)) == tree {
			return false, nil
		}
	}

	if err := validateLedgerTree(ctx, repoPath, parent, tree, pathspecs...); err != nil {
		return false, err
	}
	if err := assertNoSacredMassDeletion(ctx, repoPath, parent, tree); err != nil {
		return false, err
	}
	if err := commitTreeToBranch(ctx, repoPath, tree, parent, message); err != nil {
		return false, err
	}
	return true, nil
}

// writeScopedIndexTree builds a temporary index holding parent's tree with the
// repository index's entries under pathspecs layered on top — additions,
// modifications, and deletions alike — and returns the tree it snapshots to.
// The worktree is never consulted; only index entries the caller already staged
// can reach the commit.
//
// The temporary index lives in the OS temp dir and is removed on return, so an
// interrupted run leaves nothing inside the clone.
func writeScopedIndexTree(ctx context.Context, repoPath, parent string, pathspecs []string) (string, error) {
	tmpDir, err := os.MkdirTemp("", "ox-ledger-index-")
	if err != nil {
		return "", fmt.Errorf("create temporary index: %w", err)
	}
	defer os.RemoveAll(tmpDir)
	scopedEnv := []string{"GIT_INDEX_FILE=" + filepath.Join(tmpDir, "index")}

	if parent == "" {
		if _, err := runPlumbing(ctx, repoPath, nil, scopedEnv, "read-tree", "--empty"); err != nil {
			return "", fmt.Errorf("initialize temporary index: %w", err)
		}
	} else if _, err := runPlumbing(ctx, repoPath, nil, scopedEnv, "read-tree", parent); err != nil {
		return "", fmt.Errorf("read parent tree into temporary index: %w", err)
	}

	// update-index --index-info consumes "<mode> SP <oid> SP <stage> TAB <path>"
	// records; mode 0 removes the path. Emit removals first so a deletion in
	// the repository index also deletes from the snapshot, then the live
	// entries exactly as ls-files --stage prints them. Both read the
	// repository's own index (no GIT_INDEX_FILE).
	var indexInfo []byte
	if parent != "" {
		args := append([]string{"diff-index", "--cached", "--name-only", "--diff-filter=D", "--no-renames", "-z", parent, "--"}, pathspecs...)
		deleted, err := cleanGitOutput(ctx, repoPath, args...)
		if err != nil {
			return "", fmt.Errorf("list staged Ledger deletions: %w", err)
		}
		for _, path := range splitNUL(deleted) {
			indexInfo = append(indexInfo, "0 0000000000000000000000000000000000000000 0\t"+path+"\x00"...)
		}
	}
	args := append([]string{"ls-files", "--stage", "-z", "--"}, pathspecs...)
	staged, err := cleanGitOutput(ctx, repoPath, args...)
	if err != nil {
		return "", fmt.Errorf("list staged Ledger entries: %w", err)
	}
	indexInfo = append(indexInfo, staged...)

	if len(indexInfo) > 0 {
		if _, err := runPlumbing(ctx, repoPath, indexInfo, scopedEnv, "update-index", "-z", "--index-info"); err != nil {
			return "", fmt.Errorf("stage Ledger paths into temporary index: %w", err)
		}
	}
	return writeIndexTree(ctx, repoPath, scopedEnv)
}

// assertNoSacredMassDeletion fails a ledger commit whose snapshot (parent→tree)
// would delete more than sacred.MassDeleteThreshold files under a sacred
// prefix. It runs AFTER snapshotting and BEFORE writing the commit, so the
// check binds exactly the tree being committed.
//
// Fail-closed, matching validateLedgerTree: an unborn branch (parent=="") has
// no deletions and passes; if the diff itself cannot be computed the caller
// must NOT commit, so the error propagates rather than defaulting to "safe".
func assertNoSacredMassDeletion(ctx context.Context, repoPath, parent, tree string) error {
	if parent == "" {
		return nil // first commit: nothing pre-existing to delete
	}
	out, err := cleanGitOutput(ctx, repoPath,
		"diff-tree", "--no-commit-id", "--name-only", "-r", "--diff-filter=D", parent, tree)
	if err != nil {
		return fmt.Errorf("sacred mass-delete guard: git diff-tree %s..%s: %w",
			shortOID(parent), shortOID(tree), err)
	}
	deleted := sacred.Filter(strings.Split(string(out), "\n"))
	if len(deleted) <= sacred.MassDeleteThreshold {
		return nil
	}
	if os.Getenv(sacred.OverrideEnv) == "1" {
		slog.WarnContext(ctx, "ledger sacred mass-deletion allowed by explicit override",
			"repo", repoPath, "sacred_deletions", len(deleted), "override_env", sacred.OverrideEnv)
		return nil
	}
	// Loud alert: this is a data-loss event caught at the last line of defense.
	slog.ErrorContext(ctx, "REFUSING ledger commit: sacred mass-deletion detected",
		"repo", repoPath,
		"sacred_deletions", len(deleted),
		"threshold", sacred.MassDeleteThreshold,
		"sample", sampleStrings(deleted, 5),
		"override_env", sacred.OverrideEnv)
	return fmt.Errorf("refusing commit: would delete %d files under sacred paths (%s) in one commit, "+
		"exceeds guard threshold %d — likely a sparse/GC-reconcile wipe (see ADR-024); "+
		"if this bulk removal is intentional, set %s=1",
		len(deleted), strings.Join(sacred.Prefixes, ", "), sacred.MassDeleteThreshold, sacred.OverrideEnv)
}

// commitTreeToBranch commits an already-validated tree and advances the current
// branch ref with a compare-and-swap on its old value, so a concurrent commit
// fails this update rather than being silently overwritten.
func commitTreeToBranch(ctx context.Context, repoPath, tree, parent, message string) error {
	args := []string{"commit-tree", tree, "-m", message}
	if parent != "" {
		args = append(args, "-p", parent)
	}
	out, err := cleanGitOutput(ctx, repoPath, args...)
	if err != nil {
		return fmt.Errorf("git commit-tree: %w", err)
	}
	commit := strings.TrimSpace(string(out))
	if commit == "" {
		return errors.New("git commit-tree returned an empty commit id")
	}

	refBytes, err := cleanGitOutput(ctx, repoPath, "symbolic-ref", "HEAD")
	if err != nil {
		return fmt.Errorf("resolve HEAD ref: %w", err)
	}
	branchRef := strings.TrimSpace(string(refBytes))

	upd := []string{"update-ref", "-m", "ox: " + message, branchRef, commit}
	if parent != "" {
		upd = append(upd, parent) // CAS: only advance if the tip is still parent
	}
	if _, err := cleanGitOutput(ctx, repoPath, upd...); err != nil {
		return fmt.Errorf("advance %s (concurrent ledger commit?): %w", branchRef, err)
	}
	return nil
}

// sampleStrings returns at most n elements of s, for bounded log output.
func sampleStrings(s []string, n int) []string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// shortOID abbreviates a git object id for log/error readability.
func shortOID(oid string) string {
	if len(oid) > 8 {
		return oid[:8]
	}
	return oid
}
