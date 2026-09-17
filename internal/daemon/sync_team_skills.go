package daemon

import (
	"path"
	"strings"

	"github.com/sageox/ox/internal/skillmanager"
	"github.com/sageox/ox/internal/teamdocs"
	"github.com/sageox/ox/internal/version"
	"github.com/sageox/ox/pkg/adapterprotocol"
)

// teamSkillsTouched reports whether a team-context pull changed anything under a
// skills root.
//
// It reads the roots from teamdocs.SkillRoots — the same list discovery walks —
// so the legacy coworkers/skills location cannot be found by one and ignored by
// the other. Paths come from `git diff --name-only`, which always emits
// forward slashes regardless of platform.
func teamSkillsTouched(changed []string) bool {
	for _, p := range changed {
		clean := path.Clean(strings.TrimSpace(p))
		for _, root := range teamdocs.SkillRoots {
			if strings.HasPrefix(clean, root+"/") {
				return true
			}
		}
	}
	return false
}

// reconcileTeamSkills materializes a team skill edit into this daemon's
// repository, on the team-context sync tick that noticed the edit.
//
// WHY HERE. Team content arrives on exactly one schedule — the team-context pull
// — and this function runs off the result of that pull rather than off a timer
// of its own, so there is no second cadence to keep in step and no polling on a
// repo that has not changed. It is EVENT-DRIVEN in the strict sense: changed
// carries the diff of the pull that just landed, so a tick where the team repo
// was already current does no work at all.
//
// The 30-minute `skills-inventory-drift` autofix check is the anti-entropy floor
// beneath this and stays the backstop for anything missed (a sparse refresh that
// materializes agents/ without moving HEAD, a daemon that was down for the pull).
// Without this hook that floor was the ONLY path, so a team skill edited at 09:00
// could first appear at 09:29 — or not until someone ran `ox doctor --fix`.
//
// NON-BLOCKING ON PURPOSE. This runs on the sync scheduler's goroutine, between a
// pull and the rest of the cycle. ReconcileUpdateNonBlocking yields
// ErrApplyInProgress after ~100ms rather than polling the project lock for ten
// seconds, and losing that race is a correct outcome: whoever holds the lock — an
// `ox agent prime`, an `ox init`, the autofix tick — is performing this same
// reconcile against this same catalog.
//
// STALENESS IS GUARDED, NOT ASSUMED. A running daemon executes the binary it was
// started with, so its built-in catalog can be older than the one that last wrote
// this repository (see retireStaleDaemonsAfterUpgrade). Plan's downgrade guard
// refuses exactly that case and returns a plan with no actions, which is why
// reconciling team content from here cannot pin a repo to a previous release's
// CLI skills.
func (s *SyncScheduler) reconcileTeamSkills(changed []string) {
	if !teamSkillsTouched(changed) {
		return
	}
	repoRoot := s.config.ProjectRoot
	if repoRoot == "" {
		return
	}
	// Never install into a repository that never opted in. Skill targets are a
	// human's choice made in `ox init`; a team-context edit is not consent to
	// start writing into someone's checkout.
	if _, _, selected := skillmanager.InstalledSource(repoRoot); !selected {
		s.logger.Debug("team skills changed but repo has no skill targets selected", "repo", repoRoot)
		return
	}

	plan, err := skillmanager.ReconcileUpdateNonBlocking(repoRoot, version.Version,
		func(desired skillmanager.DesiredSkills, targets []adapterprotocol.SkillTarget) (skillmanager.DesiredSkills, []adapterprotocol.SkillTarget, error) {
			// Identity: the daemon reconciles what the project already selected and
			// never widens it.
			return desired, targets, nil
		})
	if err != nil {
		// Debug, not Warn: losing the lock race is the expected outcome whenever a
		// session is priming, and the next team-context tick retries.
		s.logger.Debug("team skill reconcile did not run", "repo", repoRoot, "error", err)
		return
	}
	if plan == nil {
		return
	}

	// Log the no-op case too. "Reconciled 0 files" and "never ran" are different
	// facts, and only one of them means the pipeline is healthy.
	s.logger.Info("team skills reconciled",
		"repo", repoRoot,
		"created", len(plan.Creates),
		"updated", len(plan.Updates),
		"removed", len(plan.Removes),
		"withheld", len(plan.WithheldTeamSkills()))

	for _, d := range plan.WithheldTeamSkills() {
		// A skill held for approval is invisible from the repository — it looks
		// exactly like a skill nobody authored. Say so once per reconcile so the
		// fact exists somewhere a human or a log scraper can find it; `ox doctor`
		// is where they are told what to do about it.
		s.logger.Warn("team skill withheld pending approval",
			"repo", repoRoot, "skill", d.Name, "reason", d.Reason)
	}
}
