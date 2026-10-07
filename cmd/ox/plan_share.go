package main

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/daemon"
	"github.com/sageox/ox/internal/gitutil"
	"github.com/sageox/ox/internal/plan"
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
	UnsafeErr     error // mid-rebase / lock files: nothing was staged
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
	case f.UnsafeErr != nil:
		st.Reason = fmt.Sprintf("ledger is mid-rebase or locked, nothing was committed: %v", f.UnsafeErr)
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

// sharePlanDir commits and pushes one plan dir and reports whether it is now
// visible to teammates. It is commitPlanToLedger with the outcome observed
// instead of collapsed into a single logged error.
func sharePlanDir(gitRoot, planDir string) planShareStatus {
	f := planShareFacts{DaemonRunning: daemonRunningFn()}
	ctx, err := config.LoadProjectContext(gitRoot)
	if err != nil || ctx == nil || ctx.DefaultLedgerPath() == "" {
		f.NoLedger = true
		return classifyPlanShare(f)
	}
	f.LedgerPath = ctx.DefaultLedgerPath()

	if err := gitutil.IsSafeForGitOps(f.LedgerPath); err != nil {
		f.UnsafeErr = err
	} else if err := commitPlanLocal(f.LedgerPath, planDir, ""); err != nil {
		f.CommitErr = err
	} else if err := pushLedger(context.Background(), f.LedgerPath); err != nil {
		f.PushErr = err
	}
	f.Dirty = planDirDirty(f.LedgerPath, planDir)
	f.Ahead, f.Behind, f.UpstreamKnown = gitAheadBehind(f.LedgerPath)
	return classifyPlanShare(f)
}

// planDirDirty reports whether the plan dir has staged or unstaged changes in
// the ledger. An unreadable status reads as dirty: "could not look" must not
// be reported as "shared".
func planDirDirty(ledgerPath, planDir string) bool {
	out, err := exec.Command("git", "-C", ledgerPath, "status", "--porcelain", "--", planDir).Output()
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
// must stay recoverable from ledger history.
func snapshotPriorRevision(gitRoot, planDir string) error {
	ctx, err := config.LoadProjectContext(gitRoot)
	if err != nil || ctx == nil || ctx.DefaultLedgerPath() == "" {
		return nil // nowhere to snapshot to; Save will fail on the same condition
	}
	ledgerPath := ctx.DefaultLedgerPath()
	if !planDirDirty(ledgerPath, planDir) {
		return nil
	}
	if err := gitutil.IsSafeForGitOps(ledgerPath); err != nil {
		return fmt.Errorf("ledger not safe to snapshot prior revision: %w", err)
	}
	// a failed upload left a large plain plan.html behind; finish the upload before
	// the snapshot, and refuse the save if it still fails rather than lose or
	// commit that render
	if plan.HasLargePlainHTML(planDir) {
		if _, err := planDehydrateHTML(planDir, planLFSClientFn(gitRoot)); err != nil {
			return fmt.Errorf("prior plan.html is awaiting upload: %w", err)
		}
	}
	return commitPlanLocal(ledgerPath, planDir, "plan: prior revision of ")
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
	return artifactURL(prResolveEndpoint(cfg), planID, "/plan/", "pln_")
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
	n := 0
	for _, ev := range events {
		if ev.Kind == plan.EventCreated || ev.Kind == plan.EventRevised {
			n++
		}
	}
	return n
}
