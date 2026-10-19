package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// CheckSlugPlanPendingWrites reports plan reviews and plan writes waiting in
// the Ledger clone's retry queues for the remote to hold them.
const CheckSlugPlanPendingWrites = "plan-pending-writes"

const planPendingCheckName = "Ledger plan retries"

// planPendingCheckTimeout bounds all the local git reads one run of the check
// makes.
const planPendingCheckTimeout = 30 * time.Second

func init() {
	RegisterDoctorCheck(&DoctorCheck{
		Slug:     CheckSlugPlanPendingWrites,
		Name:     planPendingCheckName,
		Category: "Ledger Git Health",
		// Auto, as the session upload retry is: the fix is the retry
		// `ox agent prime` starts, which only publishes what the coworker
		// already saved and never deletes a review the remote does not hold
		FixLevel:    FixLevelAuto,
		Description: "Retries plan reviews and plan writes still waiting to reach the Ledger's remote, and reports the ones that do not land, and why",
		Run:         func(fix bool) checkResult { return checkPlanPendingWrites(getLedgerPath(), fix) },
	})
}

// checkPlanPendingWrites lists each plan review recovery entry and plan retry
// marker in the Ledger clone, with its age and why it has not landed.
// `ox agent prime` retries them in the background (plan_push_pending.go), and
// nothing else says when one never lands: its plan dir is gone, its entry
// cannot be read, or the remote refuses every push. The fix runs that same
// retry, which lands whatever can land now and clears what has nothing left to
// do, and never removes a review entry whose bytes the remote does not hold,
// since it may be the only copy of a review; the detail names its file so a
// coworker can recover or discard it deliberately.
func checkPlanPendingWrites(ledgerPath string, fix bool) checkResult {
	const name = planPendingCheckName
	if ledgerPath == "" || !isGitRepo(ledgerPath) {
		return SkippedCheck(name, "no ledger checkout", "")
	}
	var landed int
	var retryNote string
	if fix && planRetriesPending(ledgerPath) > 0 {
		rctx, rcancel := context.WithTimeout(context.Background(), planPushFlushTimeout)
		ok, _, err := flushPlanRetries(rctx, ledgerPath)
		rcancel()
		landed = ok
		if err != nil {
			retryNote = fmt.Sprintf("This run retried nothing: %v.\n", err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), planPendingCheckTimeout)
	defer cancel()

	lines := append(pendingReviewLines(ctx, ledgerPath), pendingPlanLines(ledgerPath)...)
	if len(lines) == 0 {
		if landed > 0 {
			return PassedCheck(name, fmt.Sprintf("%d plan retr(ies) landed", landed))
		}
		return PassedCheck(name, "nothing waiting")
	}

	var sb strings.Builder
	sb.WriteString("`ox doctor` and each `ox agent prime` retry these. When the branch push fails, ")
	sb.WriteString("Ledger branch status shows its error. ox never deletes a review the remote does not hold: ")
	sb.WriteString("to recover or discard one, use the entry file named.\n")
	sb.WriteString(retryNote)
	shown := min(len(lines), 10)
	for _, l := range lines[:shown] {
		fmt.Fprintf(&sb, "  %s\n", l)
	}
	if len(lines) > shown {
		fmt.Fprintf(&sb, "  ... and %d more\n", len(lines)-shown)
	}
	return WarningCheck(name, fmt.Sprintf("%d plan retr(ies) pending", len(lines)), strings.TrimSuffix(sb.String(), "\n"))
}

// pendingReviewLines describes each review recovery entry still waiting. One
// whose record the remote tip already holds is not waiting: the next retry
// clears it.
func pendingReviewLines(ctx context.Context, ledgerPath string) (lines []string) {
	dir := filepath.Join(ledgerPath, filepath.FromSlash(planReviewPendingDir))
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			lines = append(lines, fmt.Sprintf("%s: unreadable (%v)", dir, err))
		}
		return lines
	}
	for _, de := range entries {
		if !isPendingEntry(de) {
			continue
		}
		file := filepath.Join(dir, de.Name())
		age := pendingAge(de, time.Time{})
		e, err := readReviewPending(file)
		if err != nil {
			lines = append(lines, fmt.Sprintf("unreadable, %s: %v; kept, as it may hold the only copy of a review: repair or delete it by hand", age, err))
			continue
		}
		// the record is published even if a backfill moved it to another plan dir
		if remoteHoldsBlob(ctx, ledgerPath, path.Dir(e.PlanDir), e.Data) {
			continue
		}
		reason := "not committed"
		if _, err := os.Stat(filepath.Join(ledgerPath, filepath.FromSlash(e.PlanDir))); errors.Is(err, os.ErrNotExist) {
			reason = "its plan dir is gone: restore it, or delete the entry to discard the review"
		} else if oid, err := gitStdout(ctx, ledgerPath, e.Data, "hash-object", "--stdin"); err == nil && blobAt(ctx, ledgerPath, "HEAD", e.PlanDir+"/"+e.Path) == strings.TrimSpace(string(oid)) {
			reason = "committed, but the remote does not hold it"
		}
		lines = append(lines, fmt.Sprintf("review %s %s on %s, %s: %s; entry %s", e.Kind, e.ID, e.PlanDir, age, reason, file))
	}
	return lines
}

// pendingPlanLines describes each plan retry marker still waiting. One whose
// plan dir is gone is not: the next retry drops it.
func pendingPlanLines(ledgerPath string) (lines []string) {
	dir := filepath.Join(ledgerPath, filepath.FromSlash(planPushPendingDir))
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			lines = append(lines, fmt.Sprintf("%s: unreadable (%v)", dir, err))
		}
		return lines
	}
	for _, de := range entries {
		if !isPendingEntry(de) {
			continue
		}
		file := filepath.Join(dir, de.Name())
		m, err := readPlanPushMarker(file)
		if err != nil {
			lines = append(lines, fmt.Sprintf("unreadable, %s: %v; no retry runs for it: delete it, and re-run the plan's last save if teammates cannot see it", pendingAge(de, time.Time{}), err))
			continue
		}
		if _, err := os.Stat(filepath.Join(ledgerPath, filepath.FromSlash(m.PlanDir))); errors.Is(err, os.ErrNotExist) {
			continue
		}
		lines = append(lines, fmt.Sprintf("plan %s, %s: %d failed attempt(s), last: %s", m.PlanDir, pendingAge(de, m.FirstFailedAt), m.FailedAttempts, m.LastError))
	}
	return lines
}

// pendingAge is how long an entry has waited: since since when it is set,
// else since its file was written.
func pendingAge(de os.DirEntry, since time.Time) string {
	if since.IsZero() {
		info, err := de.Info()
		if err != nil {
			return "age unknown"
		}
		since = info.ModTime()
	}
	return formatDurationRough(time.Since(since))
}
