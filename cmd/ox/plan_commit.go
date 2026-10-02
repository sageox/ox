package main

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/gitserver"
	"github.com/sageox/ox/internal/gitutil"
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

// planCommitStatus reports how far a plan write got toward the shared Ledger,
// so a caller that answers a human (the review page) can say "saved locally,
// not yet shared" instead of a blanket success.
//
// Committed means the plan dir's staged state is in a local Ledger commit —
// including the idempotent case where nothing changed since the last commit.
// Pushed means pushLedger returned nil, i.e. that commit is on the remote.
// Err carries the first failure (sanitized) when either is false.
type planCommitStatus struct {
	Committed bool
	Pushed    bool
	Err       string
}

// planCommitMu serializes plan commit+push inside one process. The review
// server handles POSTs concurrently, and two goroutines staging + committing
// the same clone race on .git/index.lock. gitutil.WithRepoLock already covers
// add+commit across processes, but it is not held across pushLedger (whose
// pull-rebase retry takes that same non-reentrant lock), so this mutex also
// keeps two in-process pushes from fighting over the branch tip.
var planCommitMu sync.Mutex

// planLedgerPathFor resolves the Ledger clone a plan under gitRoot commits to.
// A variable only so tests can point the review server at a temp Ledger
// without provisioning a full project context.
var planLedgerPathFor = func(gitRoot string) (string, error) {
	ctx, err := config.LoadProjectContext(gitRoot)
	if err != nil || ctx == nil {
		return "", fmt.Errorf("no project context for %q: cannot commit plan", gitRoot)
	}
	ledgerPath := ctx.DefaultLedgerPath()
	if ledgerPath == "" {
		return "", fmt.Errorf("no ledger configured for %q: cannot commit plan", gitRoot)
	}
	return ledgerPath, nil
}

// commitPlanToLedger durably commits a captured plan directory to the ledger
// and pushes it. This closes a real gap: plan.Save only materializes files into
// the ledger working tree, and commitAndPushLedger stages only sessions/<name>/
// — so without this, saved plans sit dirty-but-uncommitted indefinitely.
//
// Commit AND push are synchronous (the chosen durability model: the plan is on
// the remote before the caller returns). On any failure a pending marker is
// recorded so `ox agent prime` retries the push later through the full gate
// stack (see plan_push_pending.go) — the error is still returned for callers
// to log.
func commitPlanToLedger(gitRoot, planDir string) error {
	ledgerPath, err := planLedgerPathFor(gitRoot)
	if err != nil {
		return err
	}
	ctx := context.Background()
	_, err = commitAndPushPlanDir(ctx, ledgerPath, planDir)
	recordPlanPushOutcome(ctx, ledgerPath, planDir, err)
	return err
}

// commitPlanToLedgerStatus is commitPlanToLedger for callers that must report
// the outcome rather than just log it (the live review server's responses).
func commitPlanToLedgerStatus(ctx context.Context, gitRoot, planDir string) planCommitStatus {
	ledgerPath, err := planLedgerPathFor(gitRoot)
	if err != nil {
		return planCommitStatus{Err: err.Error()}
	}
	st, err := commitAndPushPlanDir(ctx, ledgerPath, planDir)
	recordPlanPushOutcome(ctx, ledgerPath, planDir, err)
	return st
}

// commitAndPushPlanDir stages planDir, commits ONLY that path, and pushes.
//
// Path-scoped on purpose: a bare `git commit` takes the whole index, so any
// unrelated change someone else staged in the Ledger (a half-finished session
// upload, a doctor repair) would ship under a "plan:" subject — and past
// pushLedger's per-writer assumptions. `git commit -- <path>` would scope it
// too (and does work after an `add --sparse`), but it re-reads the WORKTREE at
// commit time; gitutil.CommitLedgerSnapshot commits the exact index entries
// under the pathspec as an immutable, validated tree instead, and is the
// canonical Ledger commit path (AGENTS.md).
//
// Add+commit run under gitutil.WithRepoLock (cross-process) and planCommitMu
// (in-process, held through the push as well). The push runs outside the repo
// lock because PushWithRetry takes it itself when it must pull.
func commitAndPushPlanDir(ctx context.Context, ledgerPath, planDir string) (planCommitStatus, error) {
	planCommitMu.Lock()
	defer planCommitMu.Unlock()

	rel, err := ledgerRelPath(ledgerPath, planDir)
	if err != nil {
		return planCommitStatus{Err: err.Error()}, err
	}

	if err := commitPlanLocalCtx(ctx, ledgerPath, planDir, ""); err != nil {
		return planCommitStatus{Err: gitutil.SanitizeOutput(err.Error())}, err
	}

	// Push even when the snapshot was a no-op: an earlier commit of this plan
	// may be the one still waiting on a failed push.
	st := planCommitStatus{Committed: true}
	if err := pushLedger(ctx, ledgerPath); err != nil {
		st.Err = gitutil.SanitizeOutput(err.Error())
		return st, fmt.Errorf("push plan %s: %w", rel, err)
	}
	st.Pushed = true
	return st, nil
}

// ledgerRelPath returns planDir relative to the Ledger root as a slash path,
// refusing anything outside it (a pathspec must never widen past the plan).
// Symlinks are resolved first: on macOS a temp or XDG path may be reached via
// /var while git reports /private/var.
func ledgerRelPath(ledgerPath, planDir string) (string, error) {
	resolve := func(p string) string {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return r
		}
		return p
	}
	rel, err := filepath.Rel(resolve(ledgerPath), resolve(planDir))
	if err != nil || rel == "." || !filepath.IsLocal(rel) {
		return "", fmt.Errorf("plan dir %q is not inside ledger %q", planDir, ledgerPath)
	}
	return filepath.ToSlash(rel), nil
}

// commitPlanLocal stages one plan dir and commits it to the ledger WITHOUT
// pushing. See commitPlanLocalCtx.
func commitPlanLocal(ledgerPath, planDir, msgPrefix string) error {
	return commitPlanLocalCtx(context.Background(), ledgerPath, planDir, msgPrefix)
}

// commitPlanLocalCtx is the single plan add+commit path: it stages one plan dir
// and commits it through the canonical gitutil.CommitLedgerSnapshot under
// ADR-030's repo lock, without pushing. msgPrefix defaults to "plan: ".
// "Nothing to commit" is success (an idempotent re-save).
//
// Path-scoped on purpose: the ledger index can hold unrelated staged files
// (the daemon's data/github imports were observed staged in the field), and a
// bare `git commit` sweeps them into a "plan:" commit. CommitLedgerSnapshot
// commits the exact index entries under the pathspec as an immutable tree,
// unlike `git commit -- <path>`, which re-reads the worktree at commit time.
func commitPlanLocalCtx(ctx context.Context, ledgerPath, planDir, msgPrefix string) error {
	rel, err := ledgerRelPath(ledgerPath, planDir)
	if err != nil {
		return err
	}
	if msgPrefix == "" {
		msgPrefix = "plan: "
	}
	// ensure .gitignore is in place before any commit to prevent cache leakage
	gitserver.EnsureGitignoreBeforeCommitCtx(ctx, ledgerPath)

	return gitutil.WithRepoLock(ctx, ledgerPath, func() error {
		// Mid-rebase safety belongs at index-mutation time, not just push time:
		// an unguarded `git add` during a conflicted rebase marks the conflict
		// resolved (see .claude/rules/cache-only-design.md).
		if err := gitutil.IsSafeForGitOps(ledgerPath); err != nil {
			return fmt.Errorf("ledger not safe for plan commit (%s): %w", ledgerPath, err)
		}
		// --sparse: ledger repos use sparse-checkout (cone mode).
		if out, err := gitutil.RunGit(ctx, ledgerPath, "add", "--sparse", "--", rel); err != nil {
			return fmt.Errorf("git add %s failed: %s: %w", rel, gitutil.SanitizeOutput(out), err)
		}
		if _, err := gitutil.CommitLedgerSnapshot(ctx, ledgerPath, msgPrefix+filepath.Base(planDir), rel); err != nil {
			return fmt.Errorf("commit plan %s: %w", rel, err)
		}
		return nil
	})
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
