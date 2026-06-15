package main

// Knowledge-bubble repo-health doctor checks — parity with the ledger and
// team-context git-repo doctoring (doctor_ledger_git.go, doctor_ledger_infra.go,
// doctor_team.go). A knowledge bubble is a daemon-managed git checkout under
// paths.KBDir(kb_id) with the same .sageox/ layout, sparse-checkout, and
// merge=union attributes a ledger or team context uses, so it shares their
// failure modes:
//
//   1. Missing clone   — the API lists a bubble that was never cloned locally
//                        (daemon never ran, or the global-sync owner died
//                        before reconciling it). AutoFix kicks a daemon sync.
//
//   2. Wedged repo     — a stuck merge/rebase leaves U-state files. A wedged
//                        bubble silently blocks the global-sync owner's
//                        syncBubbles pass for EVERY daemon on the endpoint, so
//                        this is Critical. AutoFix kicks a daemon sync (which
//                        cleans lock files + autostashes); if still wedged the
//                        check surfaces the exact manual abort command.
//
//   3. Sparse cone     — .sageox dropped out of the sparse-checkout cone, which
//                        would hide kb.yaml / sync.manifest and break the next
//                        pull's sparse reapply. AutoFix kicks a daemon sync
//                        (the daemon reapplies sparse from the manifest).
//
// DESIGN: detection is read-only and runs CLI-side; repairs go through the
// daemon (kbHookSync) rather than mutating the tree from the CLI. The daemon
// owns kb git writes and serializes them with a per-kb_id flock
// (internal/daemon/kb_lock.go); a CLI write here would race that lock. This is
// why these checks kick the daemon instead of aborting/committing inline the
// way the CLI-owned ledger checks do.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/gitutil"
)

// localKBDirsForHealth returns the kb root and the list of locally-cloned
// kb_ids, or a SkippedCheck when there's nothing to inspect (no endpoint, no
// local store, empty store). Shared preamble for the repo-health checks so
// each one reads as just its specific logic.
func localKBDirsForHealth(name string) (root string, ids []string, skip *checkResult) {
	root, err := kbHookRoot()()
	if err != nil {
		r := SkippedCheck(name, "endpoint unresolved", "")
		return "", nil, &r
	}
	if _, statErr := os.Stat(root); os.IsNotExist(statErr) {
		r := SkippedCheck(name, "no local kb store yet", "")
		return "", nil, &r
	} else if statErr != nil {
		r := SkippedCheck(name, "kb root unreadable", statErr.Error())
		return "", nil, &r
	}
	ids, err = listLocalKBIDs(root)
	if err != nil {
		r := SkippedCheck(name, "kb root readdir failed", err.Error())
		return "", nil, &r
	}
	return root, ids, nil
}

// ----------------------------------------------------------------------
// Check: missing local clone (API has it, disk doesn't)
// ----------------------------------------------------------------------

// checkKBMissingClone flags bubbles the kb API lists that have no local clone.
// The inverse of checkKBOrphans (on-disk but not in API). AutoFix kicks an
// immediate daemon sync so the daemon clones them on its next reconcile.
//
// Skips when the kb API is unavailable — without the source of truth we can't
// know what *should* be on disk.
func checkKBMissingClone(fix bool) checkResult {
	const name = "kb missing clones"

	root, err := kbHookRoot()()
	if err != nil {
		return SkippedCheck(name, "endpoint unresolved", "")
	}

	listCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	bubbles, err := kbHookList()(listCtx)
	if err != nil {
		if errors.Is(err, api.ErrKBAPIUnavailable) {
			kbHookLogger().Debug("kb_doctor missing-clone check skipped: kb API unavailable")
			return SkippedCheck(name, "kb API unavailable", "")
		}
		return SkippedCheck(name, "kb API list failed", err.Error())
	}
	if len(bubbles) == 0 {
		return PassedCheck(name, "no bubbles to sync")
	}

	// a bubble counts as cloned when <root>/<kb_id>/.git exists. A row present
	// in the dir list but without .git is a partial clone the daemon will heal;
	// we treat only a fully-absent dir as "missing" to avoid double-flagging
	// with the wedge/partial paths.
	var missing []string
	for _, b := range bubbles {
		if b.KBID == "" {
			continue
		}
		// provision-failed bubbles have no repo yet — that's the provisioning
		// check's job, not ours.
		if b.LifecycleState == kbLifecycleProvisionFailed {
			continue
		}
		gitDir := filepath.Join(root, b.KBID, ".git")
		if _, statErr := os.Stat(gitDir); os.IsNotExist(statErr) {
			label := b.Slug
			if label == "" {
				label = b.KBID
			}
			missing = append(missing, label)
		}
	}

	if len(missing) == 0 {
		return PassedCheck(name, fmt.Sprintf("%d bubble(s) cloned", len(bubbles)))
	}
	sort.Strings(missing)

	msg := fmt.Sprintf("%d bubble(s) not yet cloned: %s", len(missing), strings.Join(missing, ", "))
	detail := "daemon will clone on next sync; run `ox doctor --fix` to trigger it now"
	if !fix {
		return WarningCheck(name, msg, detail)
	}

	if syncErr := kickKBSync(); syncErr != nil {
		kbHookLogger().Warn("kb_doctor missing-clone autofix failed", "error", syncErr, "missing", len(missing))
		return WarningCheck(name, msg, fmt.Sprintf("Auto-fix hint: %v", syncErr))
	}
	return PassedCheck(name, fmt.Sprintf("kicked sync for %d missing bubble(s)", len(missing)))
}

// ----------------------------------------------------------------------
// Check: wedged repo (stuck merge / rebase)
// ----------------------------------------------------------------------

// checkKBWedged scans each local bubble for a stuck merge/rebase (U-state
// files or an in-progress rebase). A wedged bubble blocks the global-sync
// owner's syncBubbles pass, so it's Critical. AutoFix kicks a daemon sync and
// re-checks; anything still wedged gets the manual abort command.
func checkKBWedged(fix bool) checkResult {
	const name = "kb wedged repos"

	root, ids, skip := localKBDirsForHealth(name)
	if skip != nil {
		return *skip
	}
	if len(ids) == 0 {
		return PassedCheck(name, "no local bubbles")
	}

	wedged := make([]string, 0)
	for _, id := range ids {
		path := filepath.Join(root, id)
		if !isGitRepo(path) {
			continue
		}
		if kbRepoWedged(path) {
			wedged = append(wedged, id)
		}
	}

	if len(wedged) == 0 {
		return PassedCheck(name, "no wedged bubbles")
	}
	sort.Strings(wedged)

	msg := fmt.Sprintf("%d wedged bubble(s): %s", len(wedged), strings.Join(wedged, ", "))
	if !fix {
		r := CriticalCheck(name, msg, kbWedgedDetail(root, wedged))
		r.slug = CheckSlugKBWedged
		r.fixLevel = FixLevelAuto
		return r
	}

	if syncErr := kickKBSync(); syncErr != nil {
		r := CriticalCheck(name, msg, fmt.Sprintf("Auto-fix hint: %v\n%s", syncErr, kbWedgedDetail(root, wedged)))
		r.slug = CheckSlugKBWedged
		return r
	}

	// re-check: the daemon sync cleans lock files + autostashes and move-asides
	// corrupt repos, but a genuine rebase wedge may persist.
	var stillWedged []string
	for _, id := range wedged {
		if kbRepoWedged(filepath.Join(root, id)) {
			stillWedged = append(stillWedged, id)
		}
	}
	if len(stillWedged) > 0 {
		r := CriticalCheck(name,
			fmt.Sprintf("%d bubble(s) still wedged after sync", len(stillWedged)),
			kbWedgedDetail(root, stillWedged))
		r.slug = CheckSlugKBWedged
		return r
	}
	return PassedCheck(name, fmt.Sprintf("cleared %d wedged bubble(s)", len(wedged)))
}

// kbRepoWedged reports whether a bubble checkout is in a stuck merge/rebase.
func kbRepoWedged(path string) bool {
	if gitutil.IsRebaseInProgress(path) {
		return true
	}
	out, err := exec.Command("git", "-C", path, "status", "--porcelain=v1").Output()
	if err != nil {
		// can't inspect — don't claim wedged on a read failure.
		return false
	}
	return len(parseUnmergedPaths(string(out))) > 0
}

// kbWedgedDetail builds the manual-recovery hint listing each wedged bubble's
// path and the abort command, so a user can clear a wedge the daemon couldn't.
func kbWedgedDetail(root string, ids []string) string {
	var b strings.Builder
	b.WriteString("a stuck merge/rebase blocks the daemon's sync of these bubbles for EVERY project:\n")
	for _, id := range ids {
		path := filepath.Join(root, id)
		fmt.Fprintf(&b, "       %s — `git -C %s rebase --abort` (or `merge --abort`)\n", id, path)
	}
	return strings.TrimRight(b.String(), "\n")
}

// ----------------------------------------------------------------------
// Check: sparse-checkout cone integrity
// ----------------------------------------------------------------------

// checkKBSparseCheckout verifies that each sparse-checkout-enabled bubble keeps
// .sageox in its cone — without it, kb.yaml / sync.manifest are hidden and the
// next pull's sparse reapply breaks. Mirrors checkLedgerSparseCheckout and the
// team sparse-checkout check. AutoFix kicks a daemon sync (the daemon reapplies
// sparse from the manifest on pull).
func checkKBSparseCheckout(fix bool) checkResult {
	const name = "kb sparse checkout"

	root, ids, skip := localKBDirsForHealth(name)
	if skip != nil {
		return *skip
	}
	if len(ids) == 0 {
		return PassedCheck(name, "no local bubbles")
	}

	var broken []string
	var checked int
	for _, id := range ids {
		path := filepath.Join(root, id)
		if !isGitRepo(path) {
			continue
		}
		sparseFile := filepath.Join(path, ".git", "info", "sparse-checkout")
		content, err := os.ReadFile(sparseFile)
		if err != nil {
			// no sparse-checkout file = full checkout for this bubble; skip it.
			continue
		}
		checked++
		if !sparseConeIncludesSageox(string(content)) {
			broken = append(broken, id)
		}
	}

	if len(broken) == 0 {
		if checked == 0 {
			return SkippedCheck(name, "no sparse-checkout bubbles", "")
		}
		return PassedCheck(name, fmt.Sprintf("%d bubble(s) include .sageox in cone", checked))
	}
	sort.Strings(broken)

	msg := fmt.Sprintf("%d bubble(s) missing .sageox from sparse cone: %s", len(broken), strings.Join(broken, ", "))
	detail := "kb.yaml/sync.manifest are hidden; run `ox doctor --fix` to have the daemon reapply sparse from the manifest"
	if !fix {
		r := WarningCheck(name, msg, detail)
		r.slug = CheckSlugKBSparseCheckout
		r.fixLevel = FixLevelAuto
		return r
	}

	if syncErr := kickKBSync(); syncErr != nil {
		return WarningCheck(name, msg, fmt.Sprintf("Auto-fix hint: %v", syncErr))
	}
	return PassedCheck(name, fmt.Sprintf("kicked sync to reapply sparse for %d bubble(s)", len(broken)))
}

// sparseConeIncludesSageox reports whether a sparse-checkout file lists .sageox
// in its cone. Matches checkLedgerSparseCheckout's parse.
func sparseConeIncludesSageox(content string) bool {
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == ".sageox" || trimmed == ".sageox/" || trimmed == "/.sageox" || trimmed == "/.sageox/" {
			return true
		}
	}
	return false
}

// kickKBSync triggers an immediate daemon sync via the shared kb doctor hook,
// the same repair primitive checkKBStaleSync uses.
func kickKBSync() error {
	syncCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	return kbHookSync()(syncCtx)
}
