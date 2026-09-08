package autofix

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
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

	// A LIVE recording session is reading these files right now. Merely finding
	// a state file is insufficient: crashed sessions leave .recording.json behind,
	// and treating that tombstone as live disables this repair forever.
	recording, recordingErr := hasLiveRecording(repoPath)
	if recordingErr != nil {
		res.Summary = "skipped: could not inspect recording sessions"
		return res
	}
	if recording {
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
	// errTrackedFile aborts the apply from inside the lock. The check must happen
	// BETWEEN planning and applying: inspecting the returned plan would describe
	// writes that already landed, which is how this guard came to enforce nothing.
	var trackedPaths []string
	errTrackedFile := errors.New("plan touches tracked files")

	var plan *skillmanager.ReconcilePlan
	plan, err = skillmanager.ReconcileUpdateGated(repoPath, version.Version,
		func(current skillmanager.DesiredSkills, currentTargets []adapterprotocol.SkillTarget) (skillmanager.DesiredSkills, []adapterprotocol.SkillTarget, error) {
			// Identity: the daemon reconciles what the project already selected and
			// never widens it.
			return current, currentTargets, nil
		},
		func(p *skillmanager.ReconcilePlan) error {
			// The lockfile is committed project intent. Even when normalization
			// produces no file action (for example pruning an unselected target),
			// the daemon must not rewrite it in the background.
			if p.LockChanged() {
				trackedPaths = []string{filepath.ToSlash(filepath.Join(".sageox", "skills.lock.json"))}
				return errTrackedFile
			}
			var lookupErr error
			trackedPaths, lookupErr = trackedPlanPaths(ctx, repoPath, p)
			if lookupErr != nil {
				// An UNANSWERED question is not a "no". Swallowing the error here
				// would let a canceled or failing `git ls-files` read as "nothing is
				// tracked", and the apply would proceed to rewrite a tracked file —
				// exactly what this gate exists to prevent.
				return lookupErr
			}
			if len(trackedPaths) > 0 {
				return errTrackedFile
			}
			return nil
		})
	if errors.Is(err, errTrackedFile) {
		res.Status = StatusFound
		res.Summary = fmt.Sprintf("%d tracked file(s) need updating; run `ox doctor --fix` (background updates never touch tracked files)", len(trackedPaths))
		return res
	}
	if errors.Is(err, errTrackedLookup) {
		// Stand down rather than guess. The next tick retries.
		res.Summary = "skipped: could not determine which files git tracks"
		return res
	}
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

func hasLiveRecording(repoPath string) (bool, error) {
	states, err := session.LoadAllRecordingStates(repoPath)
	if err != nil {
		return false, err
	}
	for _, state := range states {
		if state.IsAgentAlive() {
			return true, nil
		}
	}
	return false, nil
}

// trackedPlanPaths returns the planned paths that git currently tracks.
//
// A single `git ls-files` over the planned paths answers it. A FAILED lookup is
// NOT "nothing tracked": returning an empty list on error is what let the apply
// proceed and rewrite the very tracked file this gate exists to protect. Only the
// specific case of "this is not a git repository" — a normal state for a managed
// workspace — reads as nothing tracked; everything else returns errTrackedLookup
// and vetoes the apply.
func trackedPlanPaths(ctx context.Context, repoPath string, plan *skillmanager.ReconcilePlan) ([]string, error) {
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
		return nil, nil
	}
	// CommandContext so a canceled tick is not blocked waiting on git.
	cmd := exec.CommandContext(ctx, "git", append([]string{"ls-files", "-z", "--"}, candidates...)...)
	cmd.Dir = repoPath
	out, err := cmd.Output()
	if err != nil {
		// Distinguish "this is not a git repository" — a normal state for a managed
		// workspace — from a lookup that genuinely failed or was canceled. Only the
		// latter must veto; treating the former as a veto would stop the daemon
		// reconciling any non-git project.
		if !isGitRepo(repoPath) {
			return nil, nil
		}
		return nil, fmt.Errorf("%w: %w", errTrackedLookup, err)
	}
	var tracked []string
	for _, p := range strings.Split(string(out), "\x00") {
		if p != "" {
			tracked = append(tracked, p)
		}
	}
	return tracked, nil
}

// errTrackedLookup marks a failed or canceled tracked-path lookup, so the gate
// can abort the apply instead of reading the failure as "nothing is tracked".
var errTrackedLookup = errors.New("tracked-path lookup failed")

func isGitRepo(repoPath string) bool {
	cmd := exec.Command("git", "rev-parse", "--git-dir")
	cmd.Dir = repoPath
	return cmd.Run() == nil
}
