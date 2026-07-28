package main

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/gitserver"
)

// errBackfillCommitFailed marks a commitPlanBackfillToLedger failure at or
// before `git commit` — the ledger working tree is left with renames/edits
// (ApplyBackfillMeta and any git mv that got that far already ran) that were
// never captured in a commit. That is a materially worse, inconsistent state
// than a push-only failure, which leaves a complete, durable local commit for
// the next push to pick up — so the two must never be handled the same way.
// runPlanBackfillTitlesOnLedger uses errors.Is against this sentinel to route
// a pre-commit failure into a hard, non-zero-exit command failure instead of
// the best-effort warning a push failure gets.
var errBackfillCommitFailed = errors.New("backfill commit failed before landing locally")

// commitPlanToLedger durably commits a captured plan directory to the ledger
// and pushes it. This closes a real gap: plan.Save only materializes files into
// the ledger working tree, and commitAndPushLedger stages only sessions/<name>/
// — so without this, saved plans sit dirty-but-uncommitted indefinitely.
//
// Mirrors commitAndPushLedger's pattern (explicit-path `git add --sparse`,
// --no-verify commit, pushLedger with pull-rebase retry), scoped to the plan
// dir. Commit AND push are synchronous (the chosen durability model: the plan
// is on the remote before the caller returns). Best-effort on the caller's
// side: a push failure returns an error to log, but the local commit stands and
// the next push / `ox doctor` carries it.
func commitPlanToLedger(gitRoot, planDir string) error {
	ctx, err := config.LoadProjectContext(gitRoot)
	if err != nil || ctx == nil {
		return fmt.Errorf("no project context for %q: cannot commit plan", gitRoot)
	}
	ledgerPath := ctx.DefaultLedgerPath()
	if ledgerPath == "" {
		return fmt.Errorf("no ledger configured for %q: cannot commit plan", gitRoot)
	}

	// ensure .gitignore is in place before any commit to prevent cache leakage
	gitserver.EnsureGitignoreBeforeCommit(ledgerPath)

	// --sparse: ledger repos use sparse-checkout (cone mode).
	addArgs := []string{"-C", ledgerPath, "add", "--sparse", planDir}
	if out, err := exec.Command("git", addArgs...).CombinedOutput(); err != nil {
		return fmt.Errorf("git add failed: %s: %w", string(out), err)
	}

	commitMsg := fmt.Sprintf("plan: %s", filepath.Base(planDir))
	commitCmd := exec.Command("git", "-C", ledgerPath, "commit", "--no-verify", "-m", commitMsg)
	if out, err := commitCmd.CombinedOutput(); err != nil {
		if strings.Contains(string(out), "nothing to commit") {
			return nil // idempotent: re-save with no change
		}
		return fmt.Errorf("%s: %w", wrapCommitError(string(out), err), err)
	}

	return pushLedger(context.Background(), ledgerPath)
}

// commitPlanBackfillToLedger stages every backfilled plan rename, in-place
// plan edit, and session produced_plans fix into ONE commit and pushes it —
// the batch counterpart to commitPlanToLedger (which commits a single saved
// plan). A repair run can touch hundreds of plans; landing that as one
// reviewable commit beats one commit per directory. Mirrors
// commitPlanToLedger's pattern (--sparse, --no-verify, pushLedger with
// pull-rebase retry) rather than hand-rolling new git plumbing.
//
// Only the final push is best-effort. Every git op at or before `git
// commit` (mv, add, commit itself) returns an error wrapped in
// errBackfillCommitFailed on failure — unlike a push failure, those leave
// the working tree in a half-mutated state with nothing to anchor it, so
// the caller must treat that as a hard failure, not a warning.
//
// renames are (oldAbsPath, newAbsPath) pairs applied via `git mv --sparse`.
// touchedPlanDirs/touchedSessionDirs are absolute paths edited in-place
// (topic-only correction with no slug change; a session's produced_plans
// fix) that need a plain `git add`.
//
// Each rename is `git mv` followed by an explicit `git add` of the NEW path.
// `git mv` alone is NOT enough here: it only restages the move of whatever
// content is already in the index (HEAD) — a file mutated on disk but never
// `git add`'d before the mv (exactly ApplyBackfillMeta's write, which happens
// before this function is ever called) rides along as a rename of the OLD
// content, leaving the just-mutated bytes as an uncommitted diff on the NEW
// path immediately after the commit. Confirmed against git 2.55 directly —
// `git status --porcelain` shows "RM" (rename + pending modify), not a clean
// rename, when the source was dirty. The follow-up `git add` stages the
// current working-tree content at the new path in the same pass.
func commitPlanBackfillToLedger(ledgerPath string, renames [][2]string, touchedPlanDirs, touchedSessionDirs []string) error {
	gitserver.EnsureGitignoreBeforeCommit(ledgerPath)

	for _, pair := range renames {
		oldRel, err := filepath.Rel(ledgerPath, pair[0])
		if err != nil {
			return fmt.Errorf("relativize %q: %w", pair[0], err)
		}
		newRel, err := filepath.Rel(ledgerPath, pair[1])
		if err != nil {
			return fmt.Errorf("relativize %q: %w", pair[1], err)
		}
		mvArgs := []string{"-C", ledgerPath, "mv", "--sparse", oldRel, newRel}
		if out, err := exec.Command("git", mvArgs...).CombinedOutput(); err != nil {
			return fmt.Errorf("%w: git mv %s -> %s failed: %s: %w", errBackfillCommitFailed, oldRel, newRel, string(out), err)
		}
		addArgs := []string{"-C", ledgerPath, "add", "--sparse", newRel}
		if out, err := exec.Command("git", addArgs...).CombinedOutput(); err != nil {
			return fmt.Errorf("%w: git add %s (post-mv content) failed: %s: %w", errBackfillCommitFailed, newRel, string(out), err)
		}
	}

	addPaths := slices.Concat(touchedPlanDirs, touchedSessionDirs)
	if len(addPaths) > 0 {
		addArgs := append([]string{"-C", ledgerPath, "add", "--sparse"}, addPaths...)
		if out, err := exec.Command("git", addArgs...).CombinedOutput(); err != nil {
			return fmt.Errorf("%w: git add failed: %s: %w", errBackfillCommitFailed, string(out), err)
		}
	}

	commitMsg := fmt.Sprintf("plan: backfill %d title(s)", len(renames)+len(touchedPlanDirs))
	commitCmd := exec.Command("git", "-C", ledgerPath, "commit", "--no-verify", "-m", commitMsg)
	if out, err := commitCmd.CombinedOutput(); err != nil {
		if strings.Contains(string(out), "nothing to commit") {
			return nil // idempotent: re-run with nothing left to change
		}
		return fmt.Errorf("%w: %s: %w", errBackfillCommitFailed, wrapCommitError(string(out), err), err)
	}

	// Everything from here down is push-only: the commit above already
	// landed locally, so a failure past this point must NOT match
	// errBackfillCommitFailed — see the caller's best-effort push contract.
	return pushLedger(context.Background(), ledgerPath)
}
