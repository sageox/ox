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
		// repair contacts the content store, so require an explicit fix request.
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
// reconcile uploads any recoverable cached bytes; missing pointers are kept
// intact and continue to block publication until their content is restored.
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
	missing, verifyErr := planPointersMissingOnRemote(client, pointers)
	if verifyErr != nil {
		return WarningCheck(planPointersCheckName, "could not verify plan blobs", verifyErr.Error())
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
	remaining, verifyErr := planPointersMissingOnRemote(client, pointers)
	if verifyErr != nil {
		return WarningCheck(planPointersCheckName, "could not verify plan blobs after reconciliation", verifyErr.Error())
	}
	if len(remaining) > 0 {
		return planPointersWarning(remaining)
	}
	return PassedCheck(planPointersCheckName,
		fmt.Sprintf("all %d plan pointer(s) backed by the store after reconciliation (%d recovered uploads)", len(pointers), res.RecoveredUploads))
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
// remote store. Only a 404 proves absence; incomplete or failed inspections
// return an error so doctor never reports an unverifiable pointer as healthy.
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
			return nil, err
		}
		seen := make(map[string]bool)
		for _, obj := range resp.Objects {
			seen[obj.OID] = true
			if obj.Error != nil && obj.Error.Code != http.StatusNotFound {
				return nil, fmt.Errorf("verify plan blob %s: HTTP %d", obj.OID, obj.Error.Code)
			}
			if obj.Error == nil {
				if obj.Actions == nil || obj.Actions.Download == nil || obj.Actions.Download.Href == "" {
					return nil, fmt.Errorf("content store omitted download action for plan blob %s", obj.OID)
				}
				continue
			}
			for _, idx := range oidToIdx[obj.OID] {
				missingIdx[idx] = true
			}
		}
		for _, obj := range objs[start:end] {
			if !seen[obj.OID] {
				return nil, fmt.Errorf("content store omitted plan blob %s", obj.OID)
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
	sb.WriteString("The store cannot supply these renders, which blocks ledger pushes. Cached original bytes may still be recoverable.\n")
	shown := min(len(missing), 5)
	for _, p := range missing[:shown] {
		fmt.Fprintf(&sb, "  %s\n", p.Name)
	}
	if len(missing) > shown {
		fmt.Fprintf(&sb, "  ... and %d more\n", len(missing)-shown)
	}
	sb.WriteString("\nRun `ox doctor --fix-slug plan-pointers-missing` to upload recoverable cached blobs. Missing pointers are preserved; restore their original content before retrying the push.")
	return checkResult{
		name:    planPointersCheckName,
		warning: true,
		message: fmt.Sprintf("%d plan render(s) missing from the store (push blocked)", len(missing)),
		detail:  sb.String(),
	}
}
