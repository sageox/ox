package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/plan"
)

// CheckSlugPlanPendingWrites reports plan reviews and plan writes waiting in
// the Ledger clone's retry queues for the remote to hold them.
const CheckSlugPlanPendingWrites = "plan-pending-writes"

// CheckSlugPlanStrandedSaves and CheckSlugPlanUnsavedEdits cover plan changes
// no retry owns: a save whose commit never happened, and edits nobody saved.
const (
	CheckSlugPlanStrandedSaves = "plan-stranded-saves"
	CheckSlugPlanUnsavedEdits  = "plan-unsaved-edits"
)

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
	RegisterDoctorCheck(&DoctorCheck{
		Slug:     CheckSlugPlanStrandedSaves,
		Name:     "Ledger plan saves",
		Category: "Ledger Git Health",
		// Auto: it queues only what `ox plan save` already wrote, for the same
		// retry plan-pending-writes runs
		FixLevel:    FixLevelAuto,
		Description: "Queues plan saves whose commit never happened (a crash, an older ox, a lost retry marker) for the plan retry",
		Run:         func(fix bool) checkResult { return checkPlanStrandedSaves(getLedgerPath(), fix) },
	})
	RegisterDoctorCheck(&DoctorCheck{
		Slug:     CheckSlugPlanUnsavedEdits,
		Name:     "Ledger plan edits",
		Category: "Ledger Git Health",
		// Confirm: whether a change nobody saved is ready to share is a person's call
		FixLevel:    FixLevelConfirm,
		Description: "Lists plans with changes nobody saved; the fix publishes them as they are, once confirmed",
		Run:         func(fix bool) checkResult { return checkPlanUnsavedEdits(getLedgerPath(), fix) },
	})
}

// errSaveNeverCommitted and errPublishAsIs are the retry marker errors for
// plan dirs the two checks below queue; `Ledger plan retries` shows them
// until a retry lands.
var (
	errSaveNeverCommitted = errors.New("its save never committed; queued by ox doctor")
	errPublishAsIs        = errors.New("queued to publish as is by ox doctor --fix-slug=" + CheckSlugPlanUnsavedEdits)
)

// checkPlanStrandedSaves queues each plan dir holding a save that never
// committed. `ox plan save` appends a created or revised event to the plan's
// events.jsonl in the same locked step that writes the plan, so such an event
// missing from HEAD means the save ran and only its commit is missing: a
// crash, an older ox, or a retry marker lost to a re-clone. Queued, it
// publishes like any failed save, through the plan's own commit path.
func checkPlanStrandedSaves(ledgerPath string, fix bool) checkResult {
	const name = "Ledger plan saves"
	if ledgerPath == "" || !isGitRepo(ledgerPath) {
		return SkippedCheck(name, "no ledger checkout", "")
	}
	ctx, cancel := context.WithTimeout(context.Background(), planPendingCheckTimeout)
	defer cancel()
	dirs, err := unownedPlanDirs(ctx, ledgerPath)
	if err != nil {
		return SkippedCheck(name, "plan changes could not be listed", err.Error())
	}
	var stranded []string
	for _, dir := range dirs {
		if hasUncommittedSave(ctx, ledgerPath, dir) {
			stranded = append(stranded, dir)
		}
	}
	if len(stranded) == 0 {
		return PassedCheck(name, "no save waiting to commit")
	}
	if !fix {
		return WarningCheck(name, fmt.Sprintf("%d saved plan(s) never committed: %s", len(stranded), planDirList(stranded)),
			"Run `ox doctor` to queue them for the plan retry.")
	}
	for _, dir := range stranded {
		recordPlanPushOutcome(ctx, ledgerPath, filepath.Join(ledgerPath, filepath.FromSlash(dir)), time.Now(), errSaveNeverCommitted)
	}
	return PassedCheck(name, fmt.Sprintf("queued %d saved plan(s) for the plan retry: %s", len(stranded), planDirList(stranded)))
}

// checkPlanUnsavedEdits lists the plan dirs with changes no retry owns and no
// save wrote (checkPlanStrandedSaves runs first and queues those): edits
// nobody saved. Whether one is ready to share is a person's call, so the fix
// asks first, then queues each as a whole-dir retry and runs the retry, which
// publishes it as it is through the plan's own commit path.
func checkPlanUnsavedEdits(ledgerPath string, fix bool) checkResult {
	const name = "Ledger plan edits"
	if ledgerPath == "" || !isGitRepo(ledgerPath) {
		return SkippedCheck(name, "no ledger checkout", "")
	}
	ctx, cancel := context.WithTimeout(context.Background(), planPendingCheckTimeout)
	defer cancel()
	dirs, err := unownedPlanDirs(ctx, ledgerPath)
	if err != nil {
		return SkippedCheck(name, "plan changes could not be listed", err.Error())
	}
	if len(dirs) == 0 {
		return PassedCheck(name, "no unsaved plan edits")
	}
	list := planDirList(dirs)
	hint := "Publish them as they are: `ox doctor --fix-slug=" + CheckSlugPlanUnsavedEdits + "`.\n" +
		"Or keep editing and run `ox plan save --file <your source>`; it commits these edits to the plan's history first."
	if fix {
		ok, err := cli.ConfirmYesNoRequired(fmt.Sprintf("Publish %d plan(s) with unsaved edits as they are (%s)?", len(dirs), list), false, false)
		if err == nil && ok {
			return publishPlanDirs(ledgerPath, dirs)
		}
		hint = "Nothing was published: it needs a yes at a terminal, or --yes.\n" + hint
	}
	r := InfoCheck(name, fmt.Sprintf("%d plan(s) have unsaved edits: %s", len(dirs), list), hint)
	r.slug, r.fixLevel = CheckSlugPlanUnsavedEdits, FixLevelConfirm
	return r
}

// publishPlanDirs queues each plan dir as a whole-dir retry and runs the plan
// retry now. A dir not published yet stays queued (or, if its marker could not
// be written, listed as unsaved), so `ox doctor` reports it again.
func publishPlanDirs(ledgerPath string, dirs []string) checkResult {
	const name = "Ledger plan edits"
	ctx, cancel := context.WithTimeout(context.Background(), planPushFlushTimeout)
	defer cancel()
	for _, dir := range dirs {
		recordPlanPushOutcome(ctx, ledgerPath, filepath.Join(ledgerPath, filepath.FromSlash(dir)), time.Now(), errPublishAsIs)
	}
	// the outcome is read back per dir below, whatever the retry pass returns
	_, _, _ = flushPlanRetries(ctx, ledgerPath)
	var left []string
	for _, dir := range dirs {
		if planRetryPending(ledgerPath, dir) || planDirDirty(ledgerPath, dir) {
			left = append(left, dir)
		}
	}
	if len(left) > 0 {
		return WarningCheck(name, fmt.Sprintf("%d of %d plan(s) not published yet: %s", len(left), len(dirs), planDirList(left)),
			"`Ledger plan retries` says why; `ox doctor` and `ox agent prime` retry them.")
	}
	return PassedCheck(name, fmt.Sprintf("published %d plan(s) as they were: %s", len(dirs), planDirList(dirs)))
}

// unownedPlanDirs lists the plan dirs (slash paths from the Ledger root) with
// uncommitted changes that no retry owns: no plan retry marker for the dir,
// and changed files other than review records a recovery entry lands.
// Unmerged files are left to ledger-unmerged-paths.
func unownedPlanDirs(ctx context.Context, ledgerPath string) ([]string, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", ledgerPath, "status", "--porcelain=v1", "-z", "--untracked-files=all", "--no-renames", "--", "data/plans")
	// as planDirDirty: no index refresh under index.lock, which a concurrent ox commit needs
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git status: %w", err)
	}
	reviews, err := listReviewPending(ledgerPath)
	if err != nil {
		return nil, err
	}
	owned := map[string]bool{}
	for _, e := range reviews {
		owned[e.PlanDir+"/"+e.Path] = true
	}
	seen := map[string]bool{}
	var dirs []string
	for _, rec := range strings.Split(string(out), "\x00") {
		// "XY path"
		if len(rec) < 4 || isUnmergedCode(rec[0], rec[1]) || owned[rec[3:]] {
			continue
		}
		// data/plans/<dir>/<file>: a path directly under data/plans/ belongs to no plan
		parts := strings.SplitN(rec[3:], "/", 4)
		if len(parts) < 4 {
			continue
		}
		dir := path.Join(parts[:3]...)
		if !seen[dir] && !planRetryPending(ledgerPath, dir) {
			dirs = append(dirs, dir)
		}
		seen[dir] = true
	}
	return dirs, nil
}

// hasUncommittedSave reports whether the plan dir's events.jsonl holds a
// created or revised event that HEAD's does not. Line endings are ignored: a
// clone with core.autocrlf checks the log out with CRLF.
func hasUncommittedSave(ctx context.Context, ledgerPath, dir string) bool {
	worktree, err := os.ReadFile(filepath.Join(ledgerPath, filepath.FromSlash(dir), "events.jsonl"))
	if err != nil {
		return false
	}
	committed := map[string]bool{}
	if head, err := gitStdout(ctx, ledgerPath, nil, "cat-file", "blob", "HEAD:"+dir+"/events.jsonl"); err == nil {
		for _, line := range strings.Split(string(head), "\n") {
			committed[strings.TrimSuffix(line, "\r")] = true
		}
	}
	for _, line := range strings.Split(string(worktree), "\n") {
		line = strings.TrimSuffix(line, "\r")
		var ev plan.Event
		if committed[line] || json.Unmarshal([]byte(line), &ev) != nil {
			continue
		}
		if ev.Kind == plan.EventCreated || ev.Kind == plan.EventRevised {
			return true
		}
	}
	return false
}

// planRetryPending reports whether a plan retry marker exists for the plan dir.
func planRetryPending(ledgerPath, dir string) bool {
	_, err := os.Stat(planPushMarkerPath(ledgerPath, dir))
	return err == nil
}

// planDirList names plan dirs by their base names, at most five.
func planDirList(dirs []string) string {
	names := make([]string, 0, len(dirs))
	for _, d := range dirs {
		names = append(names, path.Base(d))
	}
	if len(names) > 5 {
		return fmt.Sprintf("%s and %d more", strings.Join(names[:5], ", "), len(names)-5)
	}
	return strings.Join(names, ", ")
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
