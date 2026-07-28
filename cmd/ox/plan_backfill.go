package main

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/paths"
	"github.com/sageox/ox/internal/plan"
	"github.com/spf13/cobra"
)

// plan_backfill.go implements `ox plan backfill-titles` — the one-off repair
// pass for plans saved by the pre-ox-1tjj.8 planTopic (which read the first
// H2 heading instead of the document's H1; see internal/plan/title.go). It
// re-derives every saved plan's topic/slug from plan.md via plan.Title +
// plan.Slugify — the compute half lives in internal/plan/backfill.go
// (ComputeBackfill, BackfillRenameMap, ComputeProducedPlansRewrite), so it
// is fully unit-tested without needing a real git repo; this file owns the
// CLI porcelain (table output, flags) and the git orchestration (mv/add/
// commit/push, in plan_commit.go's commitPlanBackfillToLedger).
//
// Before any of that, preflightPlanBackfill (plan_backfill_preflight.go)
// runs two safety guards: the working tree must have at least as many plan
// dirs as origin/main (an incomplete sparse-checkout cone would otherwise
// make this scan a fraction of the real corpus and report a false-clean
// summary), and the ledger must not be diverged from origin/main unless
// --allow-diverged says that's expected. Both guards run before the table
// is printed, in dry-run and real mode alike.
var planBackfillTitlesCmd = &cobra.Command{
	Use:    "backfill-titles",
	Hidden: true, // maintenance/CI tier — a one-off repair pass, taught via docs/prime, not human help
	Short:  "Re-derive title/slug for every saved plan from its plan.md H1 (repair pass)",
	Long: `Repairs plans saved by the pre-fix planTopic, which read the first H2
heading rather than the document's H1 (ox-1tjj.8: Parse splits markdown on H2
only, so an H1 followed by a "1. Context — Why Now" or "TL;DR" H2 saved to the
ledger under the H2's text). For every saved plan this re-derives topic/slug
via plan.Title/plan.Slugify, rewrites meta.json + re-stamps plan.html + appends
a 'revised' event, and renames the plan directory to the corrected slug WHILE
KEEPING ITS ORIGINAL DATE PREFIX — the plan's identity, not today's date. Any
sessions/*/meta.json produced_plans entry pointing at an old slug is rewritten
too, so 'ox agent stop' reconciliation keeps resolving it.

Always run --dry-run first: it prints the full retitle/rename table and
touches nothing — that table is the reviewable artifact before a real run.
Idempotent: a plan already matching its correct title/slug is a no-op (no
event, no rename, no commit). A single broken/unreadable plan dir is skipped
with a logged reason; it never aborts the batch.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		dryRun, _ := cmd.Flags().GetBool("dry-run")
		allowDiverged, _ := cmd.Flags().GetBool("allow-diverged")
		gitRoot := findGitRoot()
		ctx, err := config.LoadProjectContext(gitRoot)
		if err != nil || ctx == nil {
			return fmt.Errorf("no project context for %q: cannot backfill plans", gitRoot)
		}
		ledgerPath := ctx.DefaultLedgerPath()
		if ledgerPath == "" {
			return fmt.Errorf("no ledger configured for %q: cannot backfill plans", gitRoot)
		}
		return runPlanBackfillTitlesOnLedger(cmd, ledgerPath, dryRun, allowDiverged)
	},
}

func init() {
	planBackfillTitlesCmd.Flags().Bool("dry-run", false, "print the retitle/rename table; touch nothing")
	// --allow-diverged: escape hatch for Guard 2 (preflightPlanBackfill). Only
	// pass this when the ahead/behind state vs origin/main is already known and
	// expected — the guard exists because a mass directory rename landed on top
	// of an UNEXPECTED divergence turns a routine repair into a manual rebase.
	planBackfillTitlesCmd.Flags().Bool("allow-diverged", false, "skip the divergence preflight guard; only use when the ledger's divergence from origin/main is known and intentional")
	planCmd.AddCommand(planBackfillTitlesCmd)
}

// runPlanBackfillTitlesOnLedger is the testable core: it takes an already-
// resolved ledger path directly (no findGitRoot/config lookup) so tests can
// point it at a temp fixture. Runs preflightPlanBackfill's two safety guards
// FIRST — before any scan, before the table is printed, in both dry-run and
// real mode — then computes the full plan and prints the per-candidate
// table, and — only when dryRun is false — applies every change and lands
// one batch commit.
//
// The produced_plans rewrite map (plan.BackfillRenameMap) is built from a
// DIFFERENT candidate set in each branch, and that difference is load-
// bearing, not cosmetic: dry-run projects it from every non-skipped
// candidate (nothing is written, so there's nothing else to project from),
// while the real run derives it ONLY from candidates that ApplyBackfillMeta
// actually succeeded for. Building the real run's map from the full
// candidate list (as if every apply were guaranteed to succeed) would let a
// single failed plan silently redirect a session's produced_plans entry to
// a slug that plan was never actually renamed to.
func runPlanBackfillTitlesOnLedger(cmd *cobra.Command, ledgerPath string, dryRun, allowDiverged bool) error {
	out := cmd.OutOrStdout()
	ctx := cmd.Context()
	plansDir := paths.LedgerPlansDir(ledgerPath)

	if err := preflightPlanBackfill(ctx, out, ledgerPath, allowDiverged); err != nil {
		return err
	}

	candidates, err := plan.ComputeBackfill(plansDir)
	if err != nil {
		return fmt.Errorf("compute backfill: %w", err)
	}
	writeCandidateTable(out, candidates)
	total, retitled, renamedCount, skipped := backfillCounts(candidates)

	if dryRun {
		// Preview only: project the rewrite map from every non-skipped,
		// slug-changing candidate as if the whole batch will succeed — the
		// real run below re-derives this from actual outcomes instead.
		previewRenamed, ambiguous := plan.BackfillRenameMap(candidates)
		sessionRewrites, err := plan.ComputeProducedPlansRewrite(filepath.Join(ledgerPath, "sessions"), previewRenamed)
		if err != nil {
			return fmt.Errorf("compute produced_plans rewrite: %w", err)
		}
		reportAmbiguousSlugs(out, ambiguous)
		writeSessionRewriteTable(out, sessionRewrites)
		slog.Info("plan_backfill_titles_dry_run", "total", total, "retitled", retitled, "renamed", renamedCount, "skipped", skipped, "sessions_to_fix", len(sessionRewrites), "ambiguous_slugs", len(ambiguous))
		fmt.Fprintf(out, "\nDry run — nothing written. total=%d retitled=%d renamed=%d skipped=%d sessions_to_fix=%d ambiguous_slugs=%d\n",
			total, retitled, renamedCount, skipped, len(sessionRewrites), len(ambiguous))
		return nil
	}

	var renames [][2]string      // (old, new) absolute paths for git mv
	var touchedPlanDirs []string // topic-only correction, no rename: plain git add
	var applied []plan.BackfillPlan
	for _, c := range candidates {
		if !c.Changed() {
			continue
		}
		if err := plan.ApplyBackfillMeta(ctx, c.OldDir, c.NewTopic, c.NewSlug); err != nil {
			// A single plan's write failure must never abort the batch — log
			// and continue, matching ComputeBackfill's own skip-and-continue
			// contract for a broken source dir. Crucially, this candidate is
			// NOT added to `applied` — see BackfillRenameMap.
			slog.Warn("plan backfill: apply failed, skipping", "dir", c.OldDir, "error", err)
			continue
		}
		applied = append(applied, c)
		if c.NewDir != "" {
			renames = append(renames, [2]string{c.OldDir, c.NewDir})
		} else {
			touchedPlanDirs = append(touchedPlanDirs, c.OldDir)
		}
	}

	renamed, ambiguous := plan.BackfillRenameMap(applied)
	reportAmbiguousSlugs(out, ambiguous)
	sessionRewrites, err := plan.ComputeProducedPlansRewrite(filepath.Join(ledgerPath, "sessions"), renamed)
	if err != nil {
		return fmt.Errorf("compute produced_plans rewrite: %w", err)
	}

	var touchedSessionDirs []string
	for _, r := range sessionRewrites {
		if err := plan.ApplyProducedPlansRewrite(ctx, r.SessionDir, renamed); err != nil {
			slog.Warn("plan backfill: produced_plans rewrite failed, skipping", "dir", r.SessionDir, "error", err)
			continue
		}
		touchedSessionDirs = append(touchedSessionDirs, r.SessionDir)
	}
	writeSessionRewriteTable(out, sessionRewrites)

	if len(renames) == 0 && len(touchedPlanDirs) == 0 && len(touchedSessionDirs) == 0 {
		fmt.Fprintln(out, "Nothing to backfill — every saved plan already matches its post-fix title/slug.")
		return nil
	}

	if err := commitPlanBackfillToLedger(ledgerPath, renames, touchedPlanDirs, touchedSessionDirs); err != nil {
		if errors.Is(err, errBackfillCommitFailed) {
			// The mv/edit work above already landed on disk but never made it
			// into a commit — a materially different, worse state than "committed
			// but not pushed" below. This MUST fail the command: printing the
			// success summary here would tell an operator/CI the batch landed
			// when the ledger working tree is actually left dirty and half-done.
			slog.Error("plan backfill: commit failed, ledger working tree left partially modified", "ledger", ledgerPath, "error", err)
			return fmt.Errorf("backfill commit failed before landing locally — ledger working tree at %s has renamed/edited files that were never committed; run `git status` there, finish or discard the partial change by hand, then retry: %w", ledgerPath, err)
		}
		// Best-effort push, same contract as commitPlanToLedger/savePlanArtifacts:
		// the mv/edit/commit work above already landed in ONE local commit; a
		// push failure leaves that commit for the next push / `ox doctor`
		// rather than failing the whole command — re-running would otherwise
		// redo real work that already succeeded.
		slog.Warn("plan backfill: push failed, local commit stands", "error", err)
		cli.PrintHint("push failed (see log) — the local commit stands; retry with `ox doctor --fix` or a plain push.")
	}

	slog.Info("plan_backfill_titles", "total", total, "retitled", retitled, "renamed", renamedCount, "skipped", skipped, "sessions_fixed", len(touchedSessionDirs), "ambiguous_slugs", len(ambiguous))
	fmt.Fprintf(out, "\nBackfilled: total=%d retitled=%d renamed=%d skipped=%d sessions_fixed=%d ambiguous_slugs=%d\n",
		total, retitled, renamedCount, skipped, len(touchedSessionDirs), len(ambiguous))
	return nil
}

// writeCandidateTable prints the full old->new retitle/rename table — the
// artifact a human reviews before allowing a real run. Depends only on
// candidates, so it's identical whether printed before a dry-run preview or
// before a real run's apply loop.
func writeCandidateTable(out io.Writer, candidates []plan.BackfillPlan) {
	tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "OLD SLUG\tNEW SLUG\tOLD TOPIC\tNEW TOPIC\tACTION")
	for _, c := range candidates {
		if c.SkipReason != "" {
			fmt.Fprintf(tw, "%s\t—\t—\t—\tSKIPPED: %s\n", filepath.Base(c.OldDir), c.SkipReason)
			continue
		}
		action := "unchanged"
		switch {
		case c.Changed() && c.NewDir != "":
			action = "retitled + renamed"
		case c.Changed():
			action = "retitled"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			c.OldSlug, c.NewSlug, truncate(c.OldTopic, 40), truncate(c.NewTopic, 40), action)
	}
	_ = tw.Flush()
}

// writeSessionRewriteTable prints the sessions whose produced_plans need
// fixing. In dry-run this is a PROJECTION (computed from every non-skipped
// candidate, before anything is applied); in a real run it reflects the
// ACTUAL rewrite map, derived only from candidates that really applied — see
// runPlanBackfillTitlesOnLedger. No-op when there's nothing to report.
func writeSessionRewriteTable(out io.Writer, sessionRewrites []plan.ProducedPlansRewrite) {
	if len(sessionRewrites) == 0 {
		return
	}
	fmt.Fprintln(out, "\nSessions with stale produced_plans to fix:")
	tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "SESSION\tSTALE SLUGS")
	for _, r := range sessionRewrites {
		fmt.Fprintf(tw, "%s\t%s\n", filepath.Base(r.SessionDir), strings.Join(r.OldSlugs, ", "))
	}
	_ = tw.Flush()
}

// reportAmbiguousSlugs surfaces every old slug plan.BackfillRenameMap
// excluded because it would derive more than one distinct new slug —
// produced_plans stores only the bare slug, so there is no automatic way to
// resolve these; a human has to look at each one. Logs one structured
// warning per slug (so it shows up in the same pipeline that watches for
// backfill problems) and prints a table (so a dry-run reviewer sees it
// without grepping logs). No-op when there's nothing to report.
func reportAmbiguousSlugs(out io.Writer, ambiguous map[string][]string) {
	if len(ambiguous) == 0 {
		return
	}
	keys := make([]string, 0, len(ambiguous))
	for k := range ambiguous {
		keys = append(keys, k)
	}
	sort.Strings(keys) // deterministic order for the printed table and log sequence

	fmt.Fprintln(out, "\nAmbiguous old slugs — map to multiple new slugs, EXCLUDED from produced_plans rewrite (resolve by hand):")
	tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "OLD SLUG\tCOMPETING NEW SLUGS")
	for _, k := range keys {
		targets := ambiguous[k]
		slog.Warn("plan backfill: ambiguous old slug excluded from produced_plans rewrite", "old_slug", k, "competing_new_slugs", strings.Join(targets, ","))
		fmt.Fprintf(tw, "%s\t%s\n", k, strings.Join(targets, ", "))
	}
	_ = tw.Flush()
}

// backfillCounts summarizes a computed batch for the printed/logged totals:
// total scanned, how many need a topic/slug correction, how many of those
// also need a directory rename, and how many were unreadable and skipped.
func backfillCounts(candidates []plan.BackfillPlan) (total, retitled, renamed, skipped int) {
	for _, c := range candidates {
		total++
		if c.SkipReason != "" {
			skipped++
			continue
		}
		if c.Changed() {
			retitled++
		}
		if c.NewDir != "" {
			renamed++
		}
	}
	return total, retitled, renamed, skipped
}
