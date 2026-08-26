package main

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"

	"github.com/sageox/ox/internal/gitutil"
	"github.com/sageox/ox/internal/sacred"
)

// commitLedgerSnapshot commits the ledger index as an IMMUTABLE, pre-validated
// tree, closing the validation↔commit TOCTOU that a plain `git add` + validate +
// `git commit` pair leaves open (PR #811 review, Greptile P1; builds on #749).
//
// The old shape validated the index, then ran a separate no-pathspec `git commit`
// that RE-READ the whole index at commit time. A concurrent daemon
// `pull --rebase --autostash` can stage an unchecked blob (a stash-pop conflict's
// markers) into that index in the window between the check and the commit; the
// commit then persists it.
//
// This commits exactly the tree it validated:
//
//  1. `git write-tree` snapshots the current index into an immutable tree OID.
//     write-tree itself FAILS on unmerged (UU/UD) entries, so a live conflict
//     fails closed here.
//  2. only the blobs this commit changes vs its parent are scanned for staged
//     conflict-marker content (an unchanged blob was vetted when first committed;
//     scanning the whole tree would cost O(repo) and risk flagging historical
//     content).
//  3. `git commit-tree` commits that exact tree, and `git update-ref` advances the
//     branch with a compare-and-swap on the old tip — a concurrent writer that
//     staged into the index afterward is neither swept into THIS commit nor able
//     to silently clobber the ref.
//
// This is what `git commit` does internally (write-tree → commit-tree →
// update-ref); the only difference is the validation inserted in the middle and
// the immutability of the committed tree. Hooks are already bypassed on these
// paths (`--no-verify` previously), so committing via plumbing loses nothing.
//
// Returns committed=false with nil error when there is nothing to commit (the
// snapshot tree equals the parent's tree) — matching the old "nothing to commit"
// idempotency the callers rely on.
func commitLedgerSnapshot(ctx context.Context, ledgerPath, message string) (committed bool, err error) {
	// Never mutate the ledger mid-rebase: moving the branch ref under a rebase
	// consumes the replay step (see .claude/rules/cache-only-design.md).
	if err := gitutil.IsSafeForGitOps(ledgerPath); err != nil {
		return false, fmt.Errorf("unsafe to commit ledger: %w", err)
	}

	tree, parent, err := snapshotLedgerIndexTree(ctx, ledgerPath)
	if err != nil {
		return false, err
	}

	// nothing to commit: the snapshot is identical to the parent's tree.
	if parent != "" {
		if parentTree, terr := ledgerTreeOfCommit(ctx, ledgerPath, parent); terr == nil && parentTree == tree {
			return false, nil
		}
	}

	if bad, serr := firstConflictMarkerInTree(ctx, ledgerPath, parent, tree); serr != nil {
		return false, serr
	} else if bad != "" {
		return false, fmt.Errorf("refusing to commit %s: contains an unresolved conflict "+
			"(likely a concurrent autostash-pop conflict; resolve it before retrying)", bad)
	}

	// ADR-024 backstop: never let a single commit wipe sacred trees. Scans the
	// exact snapshot being committed (parent→tree), so it binds the same
	// immutable tree the conflict scan above vetted — TOCTOU-safe.
	if err := assertNoSacredMassDeletion(ctx, ledgerPath, parent, tree); err != nil {
		return false, err
	}

	if err := commitTreeToBranch(ctx, ledgerPath, tree, parent, message); err != nil {
		return false, err
	}
	return true, nil
}

// assertNoSacredMassDeletion fails a ledger commit whose snapshot (parent→tree)
// would delete more than sacredMassDeleteThreshold files under a sacredPrefix.
// Call it AFTER snapshotting the index tree and BEFORE writing the commit, so
// the check binds exactly the tree being committed.
//
// Fail-closed, matching firstConflictMarkerInTree: an unborn branch (parent=="")
// has no deletions and passes; if the diff itself cannot be computed the caller
// must NOT commit, so the error propagates rather than defaulting to "safe".
func assertNoSacredMassDeletion(ctx context.Context, ledgerPath, parent, tree string) error {
	if parent == "" {
		return nil // first commit: nothing pre-existing to delete
	}
	// diff-tree lists only paths whose status is Deletion between the two trees.
	out, err := ledgerGit(ctx, ledgerPath,
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
			"repo", ledgerPath, "sacred_deletions", len(deleted), "override_env", sacred.OverrideEnv)
		return nil
	}
	// Loud alert: this is a data-loss event caught at the last line of defense.
	slog.ErrorContext(ctx, "REFUSING ledger commit: sacred mass-deletion detected",
		"repo", ledgerPath,
		"sacred_deletions", len(deleted),
		"threshold", sacred.MassDeleteThreshold,
		"sample", sampleStrings(deleted, 5),
		"override_env", sacred.OverrideEnv)
	return fmt.Errorf("refusing commit: would delete %d files under sacred paths (%s) in one commit, "+
		"exceeds guard threshold %d — likely a sparse/GC-reconcile wipe (see ADR-024); "+
		"if this bulk removal is intentional, set %s=1",
		len(deleted), strings.Join(sacred.Prefixes, ", "), sacred.MassDeleteThreshold, sacred.OverrideEnv)
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

// snapshotLedgerIndexTree writes the current index to an immutable tree and
// returns that tree OID plus the current branch tip (parent), which is "" on an
// unborn branch. `git write-tree` errors on unmerged index entries, so a live
// conflict is surfaced as an error here (fail closed).
func snapshotLedgerIndexTree(ctx context.Context, ledgerPath string) (tree, parent string, err error) {
	tb, err := ledgerGit(ctx, ledgerPath, "write-tree")
	if err != nil {
		return "", "", fmt.Errorf("snapshot ledger index (unresolved conflict in index?): %w", err)
	}
	tree = strings.TrimSpace(string(tb))
	if tree == "" {
		return "", "", fmt.Errorf("git write-tree returned an empty tree")
	}
	// --verify --quiet HEAD exits nonzero (=> err) on an unborn branch; parent
	// stays "" for the first-commit case.
	if pb, perr := ledgerGit(ctx, ledgerPath, "rev-parse", "--verify", "--quiet", "HEAD"); perr == nil {
		parent = strings.TrimSpace(string(pb))
	}
	return tree, parent, nil
}

// ledgerTreeOfCommit resolves a commit-ish to its tree OID.
func ledgerTreeOfCommit(ctx context.Context, ledgerPath, commit string) (string, error) {
	b, err := ledgerGit(ctx, ledgerPath, "rev-parse", "--verify", commit+"^{tree}")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// firstConflictMarkerInTree returns the first path in `tree` (among the blobs it
// changes vs `parent`) whose committed content carries a git conflict marker, or
// "" if none. On an unborn branch (parent == "") every blob is new, so the whole
// tree is scanned.
func firstConflictMarkerInTree(ctx context.Context, ledgerPath, parent, tree string) (string, error) {
	if parent == "" {
		raw, err := ledgerGit(ctx, ledgerPath, "ls-tree", "-r", "-z", tree)
		if err != nil {
			return "", fmt.Errorf("git ls-tree: %w", err)
		}
		return firstMarkerInLsTree(ctx, ledgerPath, raw)
	}
	// no rename/copy detection (no -M/-C) => every record is meta + one path.
	raw, err := ledgerGit(ctx, ledgerPath, "diff-tree", "-r", "-z", parent, tree)
	if err != nil {
		return "", fmt.Errorf("git diff-tree: %w", err)
	}
	return firstMarkerInDiffTree(ctx, ledgerPath, raw)
}

// firstMarkerInDiffTree parses `git diff-tree -r -z` raw output. Each entry is a
// metadata token ":<srcmode> <dstmode> <srcsha> <dstsha> <status>" followed by a
// path token. Deletions (status D) have no blob to inspect.
func firstMarkerInDiffTree(ctx context.Context, ledgerPath string, raw []byte) (string, error) {
	toks := splitNULBytes(raw)
	for i := 0; i+1 < len(toks); i += 2 {
		fields := strings.Fields(strings.TrimPrefix(toks[i], ":"))
		path := toks[i+1]
		if len(fields) < 4 {
			continue
		}
		dstSHA := fields[3]
		status := ""
		if len(fields) >= 5 {
			status = fields[4]
		}
		if strings.HasPrefix(status, "D") || isAllZero(dstSHA) {
			continue // deleted from the tree — no blob to inspect
		}
		if marker, err := blobHasMarker(ctx, ledgerPath, dstSHA, path); err != nil {
			return "", err
		} else if marker {
			return path, nil
		}
	}
	return "", nil
}

// firstMarkerInLsTree parses `git ls-tree -r -z` output ("<mode> <type> <sha>\t<path>").
func firstMarkerInLsTree(ctx context.Context, ledgerPath string, raw []byte) (string, error) {
	for _, rec := range splitNULBytes(raw) {
		tab := strings.IndexByte(rec, '\t')
		if tab < 0 {
			continue
		}
		fields := strings.Fields(rec[:tab])
		path := rec[tab+1:]
		if len(fields) < 3 || fields[1] != "blob" {
			continue
		}
		if marker, err := blobHasMarker(ctx, ledgerPath, fields[2], path); err != nil {
			return "", err
		} else if marker {
			return path, nil
		}
	}
	return "", nil
}

// blobHasMarker reads a blob by OID and reports whether it carries a conflict
// marker. An unreadable blob is an error (fail closed) — an inaccessible blob is
// not proof it is safe.
func blobHasMarker(ctx context.Context, ledgerPath, oid, path string) (bool, error) {
	blob, err := ledgerGit(ctx, ledgerPath, "cat-file", "blob", oid)
	if err != nil {
		return false, fmt.Errorf("inspect staged blob %s: %w", path, err)
	}
	return gitutil.HasConflictMarkersBytes(blob), nil
}

// commitTreeToBranch commits an already-validated tree and advances the current
// branch ref with a compare-and-swap on its old value, so a concurrent commit
// fails this update rather than being silently overwritten.
func commitTreeToBranch(ctx context.Context, ledgerPath, tree, parent, message string) error {
	args := []string{"commit-tree", tree, "-m", message}
	if parent != "" {
		args = append(args, "-p", parent)
	}
	cb, err := ledgerGit(ctx, ledgerPath, args...)
	if err != nil {
		return fmt.Errorf("git commit-tree: %w", err)
	}
	commit := strings.TrimSpace(string(cb))
	if commit == "" {
		return fmt.Errorf("git commit-tree returned an empty commit")
	}

	refBytes, err := ledgerGit(ctx, ledgerPath, "symbolic-ref", "HEAD")
	if err != nil {
		return fmt.Errorf("resolve HEAD ref: %w", err)
	}
	branchRef := strings.TrimSpace(string(refBytes))

	upd := []string{"update-ref", "-m", "ox: " + message, branchRef, commit}
	if parent != "" {
		upd = append(upd, parent) // CAS: only advance if the tip is still `parent`
	}
	if _, err := ledgerGit(ctx, ledgerPath, upd...); err != nil {
		return fmt.Errorf("advance %s (concurrent ledger commit?): %w", branchRef, err)
	}
	return nil
}

// ledgerGit runs a git plumbing command against the ledger with the same
// non-interactive / locale-pinned hardening as gitutil.RunGit, but returns CLEAN
// stdout bytes (RunGit merges stderr, which would corrupt an OID or a binary
// blob). commit.gpgsign=false is mirrored for parity; the plumbing here does not
// sign, so it is a harmless no-op.
func ledgerGit(ctx context.Context, ledgerPath string, args ...string) ([]byte, error) {
	full := []string{"-C", ledgerPath, "-c", "commit.gpgsign=false", "-c", "tag.gpgsign=false"}
	full = append(full, args...)

	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Dir = ledgerPath
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C", "LANG=C")

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		sub := "git"
		if len(args) > 0 {
			sub = args[0]
		}
		return nil, fmt.Errorf("git %s: %w: %s", sub, err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// splitNULBytes splits NUL-delimited git output into records, dropping the
// trailing empty element.
func splitNULBytes(b []byte) []string {
	s := strings.TrimRight(string(b), "\x00")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\x00")
}

// isAllZero reports whether a git OID is the all-zeros null OID (a deletion's
// destination).
func isAllZero(oid string) bool {
	return oid != "" && strings.Trim(oid, "0") == ""
}
