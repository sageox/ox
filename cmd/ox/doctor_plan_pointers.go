package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/gitutil"
	"github.com/sageox/ox/internal/lfs"
)

// CheckSlugPlanPointersMissing finds plan pages (a plan.html above 1 MiB, kept as
// an LFS pointer) in unpushed commits whose content the store no longer has.
// GitLab refuses every push that carries such a pointer, so one page wedges the
// whole Ledger. The daemon's push repair fixes it on its own; this check is how
// an operator sees it and runs that repair by hand.
//
// The check this replaces (#815) was registered but never called by a doctor
// phase, and #1411 deleted it. This one is in ledgerGitHealthOrder, which
// TestLedgerGitHealthOrderCoversRegistry ties to the registry.
const CheckSlugPlanPointersMissing = "plan-pointers-missing"

const planPointersCheckName = "unpushed plan pages"

func init() {
	RegisterDoctorCheck(&DoctorCheck{
		Slug:     CheckSlugPlanPointersMissing,
		Name:     planPointersCheckName,
		Category: "Ledger Git Health",
		// Suggested: the repair rewrites unpushed history (a tombstone commit,
		// then a squash of the unpushed commits), so a bare `ox doctor` only
		// reports it.
		FixLevel:    FixLevelSuggested,
		Description: "Finds plan pages in unpushed commits whose content is missing from the store, which makes the Ledger refuse every push",
		Run:         checkPlanPointersMissing,
	})
}

// Test seams: the real ones read stored credentials and call the store.
var (
	findMissingPlanRenders = lfs.FindMissingPlanRenders
	reconcileLedgerPush    = lfs.ReconcileUnpushedPointers
)

func checkPlanPointersMissing(fix bool) checkResult {
	ledgerPath := getLedgerPath()
	if ledgerPath == "" {
		return SkippedCheck(planPointersCheckName, "no ledger found", "")
	}
	return runPlanPointersMissing(context.Background(), ledgerPath, endpoint.GetForProject(findGitRoot()), fix)
}

// runPlanPointersMissing is the check body for one resolved Ledger.
func runPlanPointersMissing(ctx context.Context, ledgerPath, endpointURL string, fix bool) checkResult {
	fixCommand := "ox doctor --fix-slug=" + CheckSlugPlanPointersMissing
	if _, err := ledgerUpstream(ctx, ledgerPath); errors.Is(err, errNoUpstream) {
		return SkippedCheck(planPointersCheckName, "ledger has no upstream branch", "")
	}
	missing, err := findMissingPlanRenders(ctx, ledgerPath, endpointURL)
	if err != nil {
		// an inspection failure is not "nothing missing"
		return WarningCheck(planPointersCheckName, "could not check unpushed plan pages against the content store", err.Error())
	}
	if len(missing) == 0 {
		return PassedCheck(planPointersCheckName, "every unpushed plan page is in the content store")
	}
	if !fix {
		return FailedCheck(planPointersCheckName,
			fmt.Sprintf("%d plan page(s) in unpushed commits are missing from the content store; the Ledger refuses every push", len(missing)),
			planPointersDetail(missing)+"\n       Run `"+fixCommand+"` (rewrites unpushed commits only).")
	}

	_, err = reconcileLedgerPush(ctx, ledgerPath, endpointURL, slog.Default())
	switch {
	case gitutil.IsRepoLockBusy(err):
		return SkippedCheck(planPointersCheckName, "ledger busy with another ox process", "Rerun `"+fixCommand+"` in a moment")
	case err != nil:
		return FailedCheck(planPointersCheckName, "the push repair did not finish", err.Error())
	}
	// the repair's own counts include sessions; verify the plan pages themselves
	still, err := findMissingPlanRenders(ctx, ledgerPath, endpointURL)
	if err != nil {
		return WarningCheck(planPointersCheckName, "repaired, but could not re-check unpushed plan pages", err.Error())
	}
	if len(still) > 0 {
		return FailedCheck(planPointersCheckName,
			fmt.Sprintf("%d plan page(s) still missing from the content store after the repair", len(still)),
			planPointersDetail(still))
	}
	restored, lost := 0, 0
	for _, m := range missing {
		if m.Recoverable {
			restored++
		} else {
			lost++
		}
	}
	return PassedCheck(planPointersCheckName,
		fmt.Sprintf("re-uploaded %d plan page(s) from this machine; replaced %d lost page(s) with a tombstone", restored, lost))
}

// planPointersDetail names each plan and what the repair does with it.
func planPointersDetail(missing []lfs.MissingPlanRender) string {
	var b strings.Builder
	for i, m := range missing {
		if i > 0 {
			b.WriteString("\n       ")
		}
		plan := strings.TrimSuffix(strings.TrimPrefix(m.Path, "data/plans/"), "/plan.html")
		if m.Recoverable {
			fmt.Fprintf(&b, "%s: a copy is on this machine; the repair uploads it again", plan)
		} else {
			fmt.Fprintf(&b, "%s: no copy anywhere; the repair replaces the page with a tombstone and keeps plan.md, meta.json and events.jsonl", plan)
		}
	}
	return b.String()
}
