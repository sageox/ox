package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/lfs"
)

// CheckSlugPlanPointersMissing detects captured plans whose plan.html is an LFS
// pointer with no backing blob in the content store.
const CheckSlugPlanPointersMissing = "plan-pointers-missing"

const planPointersCheckName = "plan content"

func init() {
	RegisterDoctorCheck(&DoctorCheck{
		Slug:     CheckSlugPlanPointersMissing,
		Name:     planPointersCheckName,
		Category: "Plans",
		// Suggested, not Auto: the fix rewrites unpushed history (blank + squash).
		// A bare `ox doctor` must not do that unasked.
		FixLevel:    FixLevelSuggested,
		Description: "Detects captured plans whose plan.html LFS pointer has no blob in the store (which wedges the ledger push)",
		Run:         checkPlanPointersMissing,
	})
}

// planPointer is one captured plan whose plan.html is an LFS pointer.
type planPointer struct {
	Name    string // dated-slug dir name
	RelPath string // plan.html path relative to the ledger
	ref     lfs.FileRef
}

// checkPlanPointersMissing reports plan.html pointers whose blob is absent from
// the remote store.
//
// # Why this check exists
//
// Until the GH #810 fix, `ox plan save` wrote a plan.html LFS pointer for a large
// render WITHOUT uploading the blob. The render was lost, and the committed
// pointer made GitLab's pre-receive hook reject every subsequent push (`LFS
// objects are missing`) — wedging the whole team's ledger. The self-healing
// reconcile that would have surfaced and cleared it only ever walked sessions/,
// so a poisoned plan pointer was invisible; one real ledger sat unpushable for 43
// commits. This check walks data/plans/ so an operator can SEE the wedge, and
// `--fix` runs the (now plan-aware) reconcile to clear it.
//
// The repair re-uploads a blob from the exact local recovery cache when one
// exists. The reconcile never blanks a pointer: a plan cannot be regenerated, so
// when nothing is recoverable the push stays paused until the bytes are restored.
func checkPlanPointersMissing(fix bool) checkResult {
	ledgerPath := getLedgerPath()
	if ledgerPath == "" {
		return SkippedCheck(planPointersCheckName, "no ledger found", "")
	}

	pointers := collectPlanHTMLPointers(filepath.Join(ledgerPath, "data", "plans"), ledgerPath)
	if len(pointers) == 0 {
		return PassedCheck(planPointersCheckName, "no plan pointers to verify")
	}

	client, err := lfs.NewClientFromLedger(ledgerPath, endpoint.GetForProject(findGitRoot()))
	if err != nil {
		// Can't verify without the store; a dehydrated clone with reachable
		// pointers is normal, so this is a skip, not a failure.
		return SkippedCheck(planPointersCheckName, "cannot reach the content store to verify plan blobs", err.Error())
	}

	return evaluatePlanPointers(client, pointers, fix, func() (*lfs.ReconcileResult, error) {
		return lfs.ReconcileAllPointers(context.Background(), ledgerPath, endpoint.GetForProject(findGitRoot()), slog.Default())
	})
}

// evaluatePlanPointers is the client-injectable core: classify the pointers, warn
// on missing blobs, and (under --fix) run the injected reconcile to clear them.
// Split out so the warn/fix wiring is testable with a fake LFS client and a fake
// reconcile, without a live ledger remote.
func evaluatePlanPointers(client *lfs.Client, pointers []planPointer, fix bool, reconcile func() (*lfs.ReconcileResult, error)) checkResult {
	missing, err := planPointersMissingOnRemote(client, pointers)
	if err != nil {
		// an inspection failure is not "all present": say the check is inconclusive
		return checkResult{
			name:    planPointersCheckName,
			warning: true,
			message: fmt.Sprintf("could not verify %d plan pointer(s) against the store (inconclusive)", len(pointers)),
			detail:  err.Error(),
		}
	}
	if len(missing) == 0 {
		return PassedCheck(planPointersCheckName, fmt.Sprintf("all %d plan pointer(s) backed by the store", len(pointers)))
	}
	if !fix {
		return planPointersWarning(missing)
	}
	res, err := reconcile()
	if err != nil {
		return checkResult{
			name:    planPointersCheckName,
			warning: true,
			message: fmt.Sprintf("%d plan pointer(s) missing; reconcile failed", len(missing)),
			detail:  err.Error(),
		}
	}
	// the aggregate upload count can come from other pointers: re-check the plan
	// pointers that were missing and pass only when every one of them is present
	stillMissing, err := planPointersMissingOnRemote(client, missing)
	if err != nil {
		return checkResult{
			name:    planPointersCheckName,
			warning: true,
			message: fmt.Sprintf("reconcile ran (restored %d blob(s)) but %d plan pointer(s) could not be re-verified (inconclusive)", res.RecoveredUploads, len(missing)),
			detail:  err.Error(),
		}
	}
	if len(stillMissing) > 0 {
		return checkResult{
			name:    planPointersCheckName,
			warning: true,
			message: fmt.Sprintf("%d plan pointer(s) still missing; no blob could be restored (push stays paused)", len(stillMissing)),
			detail:  planPointersWarning(stillMissing).detail,
		}
	}
	return PassedCheck(planPointersCheckName,
		fmt.Sprintf("restored %d plan pointer blob(s); all %d missing plan pointer(s) are now present", res.RecoveredUploads, len(missing)))
}

// collectPlanHTMLPointers finds every data/plans/<dir>/plan.html that is an LFS
// pointer (a plain plan.html — the common, healthy case — is skipped).
func collectPlanHTMLPointers(plansDir, ledgerPath string) []planPointer {
	entries, err := os.ReadDir(plansDir)
	if err != nil {
		return nil
	}
	var out []planPointer
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		htmlPath := filepath.Join(plansDir, e.Name(), planHTMLFileName)
		if !lfs.IsPointerFile(htmlPath) {
			continue
		}
		ref, err := lfs.ReadPointerFile(htmlPath)
		if err != nil {
			continue
		}
		rel, _ := filepath.Rel(ledgerPath, htmlPath)
		out = append(out, planPointer{Name: e.Name(), RelPath: rel, ref: ref})
	}
	return out
}

// planHTMLFileName mirrors internal/plan's planHTMLFile (unexported there).
const planHTMLFileName = "plan.html"

// planPointersMissingOnRemote batch-checks which pointer OIDs are absent from the
// remote store. Only a 404 proves absence — a transient 401/429/5xx says nothing,
// so those are treated as "present" rather than false-alarming (mirrors the
// reconcile's guard). A batch error is returned, never folded into "nothing
// missing": the caller reports an inconclusive check instead of a pass.
func planPointersMissingOnRemote(client *lfs.Client, pointers []planPointer) ([]planPointer, error) {
	oidToIdx := make(map[string][]int, len(pointers))
	var objs []lfs.BatchObject
	for i, p := range pointers {
		oid := p.ref.BareOID()
		if _, seen := oidToIdx[oid]; !seen {
			objs = append(objs, lfs.BatchObject{OID: oid, Size: p.ref.Size})
		}
		oidToIdx[oid] = append(oidToIdx[oid], i)
	}

	const batchChunkSize = 50 // keep the Batch API body under WAF limits
	missingIdx := make(map[int]bool)
	for start := 0; start < len(objs); start += batchChunkSize {
		end := start + batchChunkSize
		if end > len(objs) {
			end = len(objs)
		}
		resp, err := client.BatchDownload(objs[start:end])
		if err != nil {
			return nil, fmt.Errorf("batch download check (%d-%d of %d): %w", start, end, len(objs), err)
		}
		for _, obj := range resp.Objects {
			if obj.Error == nil || obj.Error.Code != http.StatusNotFound {
				continue
			}
			for _, idx := range oidToIdx[obj.OID] {
				missingIdx[idx] = true
			}
		}
	}

	var out []planPointer
	for idx := range missingIdx {
		out = append(out, pointers[idx])
	}
	return out, nil
}

func planPointersWarning(missing []planPointer) checkResult {
	var sb strings.Builder
	sb.WriteString("These captured plans have a plan.html LFS pointer whose blob is missing from the content store.\n")
	sb.WriteString("The bytes were never uploaded (a pre-fix `ox plan save` of a render above 1MiB), so the render is ")
	sb.WriteString("unavailable until its bytes are restored — and the missing blob makes the ledger reject every push.\n")
	shown := min(len(missing), 5)
	for _, p := range missing[:shown] {
		fmt.Fprintf(&sb, "  %s\n", p.Name)
	}
	if len(missing) > shown {
		fmt.Fprintf(&sb, "  ... and %d more\n", len(missing)-shown)
	}
	sb.WriteString("\nRun `ox doctor --fix` to re-upload any blob still held in the local recovery cache. ")
	sb.WriteString("Pointers are never blanked: a plan with no recoverable blob keeps the push paused until its bytes are restored.")
	return checkResult{
		name:    planPointersCheckName,
		warning: true,
		message: fmt.Sprintf("%d plan render(s) missing from the store (push blocked)", len(missing)),
		detail:  sb.String(),
	}
}
