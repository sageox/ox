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
// A missing-blob plan pointer is unrecoverable — the bytes were never stored
// anywhere — so the only repair is to blank the dead pointer and squash it out of
// the push pack, which is exactly what the reconcile does.
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

	missing := planPointersMissingOnRemote(client, pointers)
	if len(missing) == 0 {
		return PassedCheck(planPointersCheckName, fmt.Sprintf("all %d plan pointer(s) backed by the store", len(pointers)))
	}

	if !fix {
		return planPointersWarning(missing)
	}
	return repairPlanPointers(ledgerPath, missing)
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
// reconcile's guard). On any batch error it returns nil (optimistic: never invent
// a wedge that isn't confirmed).
func planPointersMissingOnRemote(client *lfs.Client, pointers []planPointer) []planPointer {
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
			return nil
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
	return out
}

func planPointersWarning(missing []planPointer) checkResult {
	var sb strings.Builder
	sb.WriteString("These captured plans have a plan.html LFS pointer whose blob is missing from the content store.\n")
	sb.WriteString("The bytes were never uploaded (a pre-fix `ox plan save` of a render above 256KB), so the render is ")
	sb.WriteString("unrecoverable — and the missing blob makes the ledger reject every push.\n")
	shown := min(len(missing), 5)
	for _, p := range missing[:shown] {
		fmt.Fprintf(&sb, "  %s\n", p.Name)
	}
	if len(missing) > shown {
		fmt.Fprintf(&sb, "  ... and %d more\n", len(missing)-shown)
	}
	sb.WriteString("\nRun `ox doctor --fix` to blank the dead pointers and unblock the push.")
	return checkResult{
		name:    planPointersCheckName,
		warning: true,
		message: fmt.Sprintf("%d plan render(s) missing from the store (push blocked)", len(missing)),
		detail:  sb.String(),
	}
}

// repairPlanPointers runs the plan-aware reconcile, which blanks the orphaned
// pointers and squashes them out of the unpushed pack so the push can proceed.
func repairPlanPointers(ledgerPath string, missing []planPointer) checkResult {
	res, err := lfs.ReconcileUnpushedPointers(context.Background(), ledgerPath, endpoint.GetForProject(findGitRoot()), slog.Default())
	if err != nil {
		return checkResult{
			name:    planPointersCheckName,
			warning: true,
			message: fmt.Sprintf("%d plan pointer(s) missing; reconcile failed", len(missing)),
			detail:  err.Error(),
		}
	}
	return PassedCheck(planPointersCheckName,
		fmt.Sprintf("reconciled %d orphaned pointer(s); ledger push unblocked", res.Replaced))
}
