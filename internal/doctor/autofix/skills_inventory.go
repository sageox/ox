package autofix

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/skillmanager"
	"github.com/sageox/ox/internal/version"
	"github.com/sageox/ox/pkg/adapterprotocol"
)

// checkSkillsInventoryDrift repairs ox-managed skill files that no longer match
// the catalog compiled into the running binary.
//
// It is the counterpart to the cheap staleness compare `ox agent prime` performs.
// Prime deliberately does no work when the recorded revision matches, because it
// runs on the session hot path; that leaves one gap it cannot close — a managed
// file deleted or damaged *locally*, where the lockfile still says everything is
// current. Repairing that requires reading every managed file, which is exactly
// what a 30-minute unattended tick can afford and a session start cannot.
//
// Three boundaries make this safe to run unattended:
//
//   - It never installs. A project with no recorded targets has never selected an
//     AI coworker with native skills; `ox init` owns that decision, and the daemon
//     must not make it on the user's behalf.
//   - It never runs under a live session. Swapping a skill file mid-turn changes
//     the instructions an agent is already working from.
//   - It never touches the git index, and never rewrites a TRACKED file. Managed
//     skill files are ignored working-tree state; the committed on-ramp is not, and
//     a background process modifying a tracked file is precisely the intrusion the
//     #732 ruling was about — the developer would find a modified file in
//     `git status` having done nothing.
func checkSkillsInventoryDrift(ctx context.Context, repoPath string) CheckResult {
	const slug = "skills-inventory-drift"
	res := CheckResult{Slug: slug, Repo: repoPath, Status: StatusClean}
	if repoPath == "" {
		return res
	}
	if err := ctx.Err(); err != nil {
		return res
	}

	// A recording session is reading these files right now.
	if session.IsRecording(repoPath) {
		res.Summary = "skipped: session recording in progress"
		return res
	}

	_, targets, err := skillmanager.LoadDesired(repoPath)
	if err != nil {
		res.Status = StatusError
		res.Summary = fmt.Sprintf("read skills lockfile: %v", err)
		return res
	}
	if len(targets) == 0 {
		return res // never selected; not the daemon's decision to make
	}

	// Plan and Apply run inside one ReconcileUpdate so the read-modify-write is
	// serialized against `ox init`, `ox doctor`, and a concurrent session's prime.
	// Planning and applying as two independent steps leaves a window where another
	// process changes desired state between them, and this tick then writes a plan
	// built from state that no longer exists.
	var plan *skillmanager.ReconcilePlan
	plan, err = skillmanager.ReconcileUpdate(repoPath, version.Version,
		func(current skillmanager.DesiredSkills, currentTargets []adapterprotocol.SkillTarget) (skillmanager.DesiredSkills, []adapterprotocol.SkillTarget, error) {
			// Identity: the daemon reconciles what the project already selected and
			// never widens it.
			return current, currentTargets, nil
		})
	if err != nil {
		if errors.Is(err, skillmanager.ErrApplyInProgress) {
			res.Summary = "skipped: another ox process is reconciling"
			return res
		}
		res.Status = StatusError
		res.Summary = fmt.Sprintf("reconcile skills: %v", err)
		return res
	}
	if plan == nil {
		return res
	}
	if len(plan.Warnings) > 0 {
		// The downgrade / schema guard fired: an older binary must not rewrite a
		// newer project's inventory. Report rather than silently doing nothing.
		res.Status = StatusFound
		res.Summary = plan.Warnings[0]
		return res
	}
	// Refuse to write a tracked path. The committed on-ramp skill is the realistic
	// case: when its content changes in a release, applying here would silently
	// modify a file in the developer's working tree.
	if tracked := trackedPlanPaths(repoPath, plan); len(tracked) > 0 {
		res.Status = StatusFound
		res.Summary = fmt.Sprintf("%d tracked file(s) need updating; run `ox doctor --fix` (background updates never touch tracked files)", len(tracked))
		return res
	}

	changed := len(plan.Creates) + len(plan.Updates) + len(plan.Removes)
	if changed == 0 {
		if n := len(plan.Conflicts); n > 0 {
			res.Status = StatusFound
			res.Summary = fmt.Sprintf("%d skill file(s) preserved as conflicts", n)
		}
		return res
	}

	res.Status = StatusFixed
	res.Summary = fmt.Sprintf("reconciled %d ox-managed skill file(s)", changed)
	return res
}

// trackedPlanPaths returns the planned paths that git currently tracks.
//
// A single `git ls-files` over the planned paths answers it; an error is treated
// as "nothing tracked" because a repository without git is a normal state for this
// check and must not turn into a reported fault.
func trackedPlanPaths(repoPath string, plan *skillmanager.ReconcilePlan) []string {
	var candidates []string
	for _, a := range plan.Creates {
		candidates = append(candidates, a.Path)
	}
	for _, a := range plan.Updates {
		candidates = append(candidates, a.Path)
	}
	for _, a := range plan.Removes {
		candidates = append(candidates, a.Path)
	}
	if len(candidates) == 0 {
		return nil
	}
	cmd := exec.Command("git", append([]string{"ls-files", "-z", "--"}, candidates...)...)
	cmd.Dir = repoPath
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	var tracked []string
	for _, p := range strings.Split(string(out), "\x00") {
		if p != "" {
			tracked = append(tracked, p)
		}
	}
	return tracked
}
