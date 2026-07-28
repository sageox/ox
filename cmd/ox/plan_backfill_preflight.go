package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"

	"github.com/sageox/ox/internal/gitutil"
	"github.com/sageox/ox/internal/paths"
	"github.com/sageox/ox/internal/plan"
)

// plan_backfill_preflight.go owns the git plumbing for the two safety
// guards `ox plan backfill-titles` runs before it scans a single plan
// directory — the pure abort/proceed verdicts live in
// internal/plan/backfill_preflight.go (VerifyCorpusComplete,
// VerifyNotDiverged), following the same compute-vs-orchestrate split as
// ComputeBackfill/ApplyBackfillMeta. Neither guard fetches: like
// checkLedgerBranchStatus (doctor_ledger_git.go), they trust the daemon's
// own sync loop to keep origin/main current and only read cached refs —
// this must stay fast and side-effect-free since it runs on every
// invocation, dry-run included.

// preflightPlanBackfill runs both guards against ledgerPath before
// runPlanBackfillTitlesOnLedger computes or prints anything. Returning an
// error here aborts the command outright — in both dry-run and real mode,
// since a dry-run's retitle/rename table is only a trustworthy reviewable
// artifact if the corpus it scanned was actually complete.
func preflightPlanBackfill(ctx context.Context, out io.Writer, ledgerPath string, allowDiverged bool) error {
	localCount, err := plan.CountPlanDirs(paths.LedgerPlansDir(ledgerPath))
	if err != nil {
		return fmt.Errorf("count local plan dirs: %w", err)
	}
	remoteCount, remoteKnown := remotePlanDirCount(ctx, ledgerPath)

	note, err := plan.VerifyCorpusComplete(localCount, remoteCount, remoteKnown)
	if err != nil {
		slog.Error("plan_backfill_preflight_abort", "guard", "corpus_completeness", "local_count", localCount, "remote_count", remoteCount, "remote_known", remoteKnown, "error", err)
		return err
	}
	if note != "" {
		slog.Info("plan_backfill_preflight", "guard", "corpus_completeness", "local_count", localCount, "remote_count", remoteCount, "remote_known", remoteKnown, "note", note)
		fmt.Fprintln(out, note)
	}

	ahead, behind, divergenceRemoteKnown := aheadBehindCounts(ctx, ledgerPath)
	note, err = plan.VerifyNotDiverged(ahead, behind, divergenceRemoteKnown, allowDiverged)
	if err != nil {
		slog.Error("plan_backfill_preflight_abort", "guard", "divergence", "ahead", ahead, "behind", behind, "remote_known", divergenceRemoteKnown, "allow_diverged", allowDiverged, "error", err)
		return err
	}
	if note != "" {
		slog.Info("plan_backfill_preflight", "guard", "divergence", "ahead", ahead, "behind", behind, "remote_known", divergenceRemoteKnown, "note", note)
		fmt.Fprintln(out, note)
	}
	return nil
}

// remotePlanDirCount counts plan directories under data/plans on
// origin/main — the read-only remote-side count Guard 1 compares the
// working tree against. `git ls-tree` on a path that resolves to a subtree
// lists that subtree's immediate children non-recursively, exactly the
// per-directory count CountPlanDirs computes locally.
//
// remoteKnown=false whenever origin/main can't be resolved at all (no
// remote configured, a repo that's never been fetched, or — in tests — not
// a git repository yet). That is NOT the same as origin/main resolving to a
// tree with zero plans (an empty ledger): `git ls-tree` exits 0 with empty
// output when the path is simply absent from an otherwise-valid tree, so
// only a genuine git error (non-nil err) marks the remote unknown.
func remotePlanDirCount(ctx context.Context, ledgerPath string) (count int, remoteKnown bool) {
	out, err := gitutil.RunGit(ctx, ledgerPath, "ls-tree", "-d", "--name-only", "origin/main", "data/plans/")
	if err != nil {
		return 0, false
	}
	trimmed := strings.TrimSpace(out)
	if trimmed == "" {
		return 0, true
	}
	return len(strings.Split(trimmed, "\n")), true
}

// aheadBehindCounts returns the ledger's commit divergence from
// origin/main, mirroring fixLedgerBranchDiverged's git rev-list
// --left-right pattern (doctor_ledger_git.go) rather than inventing new
// plumbing. remoteKnown=false whenever origin/main can't be resolved.
func aheadBehindCounts(ctx context.Context, ledgerPath string) (ahead, behind int, remoteKnown bool) {
	out, err := gitutil.RunGit(ctx, ledgerPath, "rev-list", "--left-right", "--count", "origin/main...HEAD")
	if err != nil {
		return 0, 0, false
	}
	// "git rev-list --left-right --count L...R" prints "<only-in-L> <only-in-R>":
	// commits only reachable from origin/main (behind) first, then commits only
	// reachable from HEAD (ahead) — same order fixLedgerBranchDiverged parses.
	parts := strings.Fields(out)
	if len(parts) != 2 {
		return 0, 0, false
	}
	behindN, errBehind := strconv.Atoi(parts[0])
	aheadN, errAhead := strconv.Atoi(parts[1])
	if errBehind != nil || errAhead != nil {
		return 0, 0, false
	}
	return aheadN, behindN, true
}
