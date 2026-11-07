package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/daemon"
	"github.com/sageox/ox/internal/gitutil"
	"github.com/sageox/ox/internal/plan"
	"github.com/sageox/ox/internal/weblink"
)

// plan_share.go answers the question `ox plan save` used to leave open: can a
// teammate see this plan yet? A save that commits locally but never pushes
// looks identical to success from the agent's side — the incident that
// motivated this had a ledger 18 commits ahead / 3538 behind with saved plans
// staged and unpushed, and nothing said so. Every save now reports whether the
// plan reached the remote, and when it did not, why and the one command that
// fixes it.

// planShareStatus is the sharing verdict for one save. Shared is the only
// field a caller needs to branch on; the rest explain a false.
type planShareStatus struct {
	Shared    bool     `json:"shared"`
	Committed bool     `json:"committed"`
	Pushed    bool     `json:"pushed"`
	Ahead     int      `json:"ahead,omitempty"`
	Behind    int      `json:"behind,omitempty"`
	Reason    string   `json:"reason,omitempty"`
	Fix       string   `json:"fix,omitempty"`
	Warnings  []string `json:"warnings,omitempty"`
}

// planShareFacts are the observations classifyPlanShare decides from. Split
// out so every failure shape is table-testable without a real remote.
type planShareFacts struct {
	LedgerPath    string
	NoLedger      bool
	CommitErr     error // add or commit failed: files left in the working tree
	PushErr       error // commit landed locally, push failed
	Dirty         bool  // plan dir still has uncommitted changes after the attempt
	Ahead, Behind int
	UpstreamKnown bool
	DaemonRunning bool
}

// planDoctorFix is the repair command for every ledger-side sharing failure:
// doctor reconciles divergence, stale locks, and unpushed commits, and a local
// plan commit it pushes needs no re-save.
const planDoctorFix = "ox doctor --fix"

// classifyPlanShare turns the facts into a verdict. Order matters: the
// earliest failing step is the reason, because later facts (ahead/behind) are
// symptoms of it.
func classifyPlanShare(f planShareFacts) planShareStatus {
	st := planShareStatus{Ahead: f.Ahead, Behind: f.Behind}
	if !f.DaemonRunning {
		// Not a sharing failure for THIS save (the CLI pushes itself), but
		// without the daemon the ledger never pulls, so the local ledger drifts
		// behind and the next push has to rebase over everything it missed.
		st.Warnings = append(st.Warnings, "ox daemon is not running: the ledger is not syncing (start it with `ox daemon start`)")
	}
	diverged := f.UpstreamKnown && f.Ahead > 0 && f.Behind > 0
	switch {
	case f.NoLedger:
		st.Reason = "no ledger configured for this repo"
		st.Fix = planDoctorFix
	case f.CommitErr != nil:
		st.Reason = fmt.Sprintf("ledger commit failed, plan files left uncommitted: %v", f.CommitErr)
		st.Fix = planDoctorFix
	case f.PushErr != nil:
		st.Committed = true
		st.Reason = fmt.Sprintf("committed locally but push failed: %v", f.PushErr)
		if diverged {
			st.Reason = fmt.Sprintf("ledger diverged (%d ahead / %d behind origin); committed locally but push failed: %v", f.Ahead, f.Behind, f.PushErr)
		}
		st.Fix = planDoctorFix
	case f.Dirty:
		st.Committed = true
		st.Pushed = true
		st.Reason = "plan files still have uncommitted changes in the ledger"
		st.Fix = planDoctorFix
	case f.UpstreamKnown && f.Ahead > 0:
		// push reported success but local commits are still not on the
		// remote-tracking branch — trust the refs, not the exit code.
		st.Committed = true
		st.Pushed = true
		st.Reason = fmt.Sprintf("%d ledger commit(s) are still not on origin", f.Ahead)
		st.Fix = planDoctorFix
	default:
		st.Committed = true
		st.Pushed = true
		st.Shared = true
	}
	return st
}

// daemonRunningFn is indirected so tests never probe a real daemon socket.
var daemonRunningFn = daemon.IsRunning

// sharePlanDir pushes the plan dir a save committed and reports whether
// teammates can see it. ledgerPath is the save's Ledger ("" for none),
// commitErr its commit's outcome, and started when the save began.
func sharePlanDir(ledgerPath, planDir string, started time.Time, commitErr error) planShareStatus {
	f := planShareFacts{DaemonRunning: daemonRunningFn(), LedgerPath: ledgerPath}
	if ledgerPath == "" {
		f.NoLedger = true
		return classifyPlanShare(f)
	}
	// git run in a directory that is not a checkout reads any enclosing
	// repository, and a retry marker would create a .git dir the daemon reads
	// as a corrupt clone
	if !gitutil.IsGitRepo(ledgerPath) {
		f.CommitErr = fmt.Errorf("ledger %s is not a git checkout", ledgerPath)
		return classifyPlanShare(f)
	}

	f.CommitErr = commitErr
	if commitErr == nil {
		f.PushErr = pushLedger(context.Background(), ledgerPath)
	}
	// the save's files are already written: a failed attempt leaves a marker
	// for `ox agent prime` to retry
	recordPlanPushOutcome(context.Background(), ledgerPath, planDir, started, cmp.Or(f.CommitErr, f.PushErr))
	f.Dirty = planDirDirty(ledgerPath, planDir)
	f.Ahead, f.Behind, f.UpstreamKnown = gitAheadBehind(ledgerPath)
	return classifyPlanShare(f)
}

// sharePlanEvent is the lifecycle verbs' and the review page's Approve write:
// the event committed alone (commitPlanEvent), pushed, and the outcome
// classified like a save's, with only the event's own files checked for
// uncommitted changes. The push runs even when the event changes nothing:
// re-running a verb is how a coworker retries an event an earlier run could
// not share. An error means nothing was recorded. With no Ledger checkout the
// event is recorded and reported not shared, since nothing pulls or pushes
// that dir; a no-op there reports nothing (a zero status).
func sharePlanEvent(ctx context.Context, gitRoot, planDir string, kind plan.EventKind, fields plan.PlanEventFields) (bool, planShareStatus, error) {
	f := planShareFacts{DaemonRunning: daemonRunningFn()}
	ledgerPath, err := planLedgerPathFor(gitRoot)
	if err != nil || !gitutil.IsGitRepo(ledgerPath) {
		if err != nil {
			f.NoLedger = true
		} else {
			f.CommitErr = fmt.Errorf("ledger %s is not a git checkout", ledgerPath)
		}
		changed, err := plan.AppendPlanEvent(ctx, planDir, kind, fields)
		if !changed {
			return false, planShareStatus{}, err // nothing new, and nothing to push it
		}
		return true, classifyPlanShare(f), err
	}
	f.LedgerPath = ledgerPath
	started := time.Now()
	planCommitMu.Lock()
	defer planCommitMu.Unlock()
	changed, err := commitPlanEvent(ctx, ledgerPath, planDir, kind, fields)
	switch {
	case errors.Is(err, errEventNotCommitted):
		f.CommitErr = err
	case err != nil:
		return false, planShareStatus{}, err
	default:
		f.PushErr = pushLedger(ctx, ledgerPath)
	}
	recordPlanPushOutcome(ctx, ledgerPath, planDir, started, cmp.Or(f.CommitErr, f.PushErr), planEventFiles...)
	var paths []string
	for _, name := range planEventFiles {
		paths = append(paths, filepath.Join(planDir, name))
	}
	f.Dirty = planDirDirty(ledgerPath, paths...)
	f.Ahead, f.Behind, f.UpstreamKnown = gitAheadBehind(ledgerPath)
	return changed, classifyPlanShare(f), nil
}

// planDirDirty reports whether paths (a plan dir or files in one) have staged
// or unstaged changes in the ledger. An unreadable status reads as dirty:
// "could not look" must not be reported as "shared".
func planDirDirty(ledgerPath string, paths ...string) bool {
	cmd := exec.Command("git", append([]string{"-C", ledgerPath, "status", "--porcelain", "--"}, paths...)...)
	// without it, status refreshes the index under index.lock, and a concurrent
	// ox commit's `git add` fails on that lock
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	out, err := cmd.Output()
	if err != nil {
		return true
	}
	return strings.TrimSpace(string(out)) != ""
}

// snapshotPriorRevision commits (locally, no push) any not-yet-committed state
// of an existing plan dir before a save overwrites it. Normally every save
// commits, so this is a no-op; it matters exactly when an earlier save's commit
// failed, which is when the prior revision would otherwise exist nowhere but
// the working tree the new save is about to overwrite. Sacred tier: a revision
// must stay recoverable from ledger history. The caller holds the repo lock, and
// up is what it uploaded before taking it.
func snapshotPriorRevision(ctx context.Context, ledgerPath, planDir string, up planUploads) error {
	if ledgerPath == "" {
		return nil // nowhere to snapshot to; Save will fail on the same condition
	}
	// git run outside a checkout finds any enclosing repository; there is no
	// history here to keep the prior revision in, so it must not be overwritten
	if !gitutil.IsGitRepo(ledgerPath) {
		return fmt.Errorf("ledger %s is not a git checkout", ledgerPath)
	}
	if !planDirDirty(ledgerPath, planDir) {
		return nil
	}
	// a failed upload left a large plain plan.html behind; store it as the pointer
	// the save uploaded for it, and refuse the save when there is none rather
	// than lose or commit that render
	if plan.HasLargePlainHTML(planDir) {
		_, perr := plan.PointerizeHTML(planDir, up.refs...)
		if plan.HasLargePlainHTML(planDir) {
			return fmt.Errorf("prior plan.html is awaiting upload: %w", cmp.Or(perr, up.failure()))
		}
	}
	return commitPlanDirLocked(ctx, ledgerPath, planDir, "plan: prior revision of ")
}

// planShareURL is the web link for a saved plan: the same endpoint `ox status`
// and `ox pr header` use, and the same opaque /plan/<pln_id> route, so a link
// printed here is exactly what a teammate can open. "" when either the
// endpoint or the id is unknown — never a guessed link.
func planShareURL(gitRoot, planID string) string {
	if planID == "" {
		return ""
	}
	var cfg *config.ProjectConfig
	if gitRoot != "" {
		cfg, _ = config.LoadProjectConfig(gitRoot)
	}
	return weblink.ArtifactURL(prResolveEndpoint(cfg), planID, "/plan/", "pln_")
}

// planIDForDir returns the plan's stable pln_ id from its event log, or "".
func planIDForDir(planDir string) string {
	events, err := plan.LoadEvents(planDir)
	if err != nil || len(events) == 0 {
		return ""
	}
	return plan.Fold(events).PlanID
}

// planRevisionCount is 1 for a plan's first save and grows by one per
// `revised` event, so "revision 3" means the page has been saved three times.
func planRevisionCount(planDir string) int {
	events, err := plan.LoadEvents(planDir)
	if err != nil {
		return 0
	}
	return plan.Fold(events).Revisions
}
