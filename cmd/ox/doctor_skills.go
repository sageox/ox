package main

import (
	"fmt"
	"strings"

	"github.com/sageox/ox/internal/skillmanager"
	"github.com/sageox/ox/internal/teamskills"
)

// checkClaudeSkills retains its historical registration name, but checks the
// project-selected native targets from .sageox/skills.lock.json. Detection is
// consulted only for the one-release inline-stamp migration.
func checkClaudeSkills(fix bool) checkResult {
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return SkippedCheck("Agent skills", "not in git repo", "")
	}
	plan, err := planCommittedSkills(gitRoot)
	if err != nil {
		return WarningCheck("Agent skills", "cannot inspect managed skills", err.Error())
	}
	if plan.TargetCount == 0 && !plan.RetiredSelections {
		return SkippedCheck("Agent skills", "no project-selected skill targets", "Run `ox init` and select an AI coworker with native Agent Skills")
	}
	if len(plan.Warnings) > 0 {
		return WarningCheck("Agent skills", strings.Join(plan.Warnings, "; "), "Use the same or a newer ox version before reconciling")
	}
	if len(plan.Creates)+len(plan.Updates)+len(plan.Removes) == 0 && len(plan.Conflicts) == 0 && !plan.RetiredSelections {
		// Converged is not the same as complete. A team skill withheld pending
		// approval leaves the repository looking exactly as it would if nobody had
		// authored it, so this is the one place a human finds out it exists.
		if withheld := plan.WithheldTeamSkills(); len(withheld) > 0 {
			return WarningCheck("Agent skills", describeWithheldTeamSkills(withheld),
				teamSkillApprovalHint(gitRoot))
		}
		return PassedCheck("Agent skills", fmt.Sprintf("%d managed files across %d native target(s)", plan.DesiredFileCount, plan.TargetCount))
	}

	problem := describeSkillPlan(plan)
	if !fix {
		if len(plan.Conflicts) > 0 && len(plan.Creates)+len(plan.Updates)+len(plan.Removes) == 0 && !plan.RetiredSelections {
			return WarningCheck("Agent skills", problem, describeSkillConflicts(plan.Conflicts))
		}
		return FailedCheck("Agent skills", problem, "Run `ox doctor --fix` to reconcile unchanged managed files")
	}
	applied, err := reconcileCommittedSkills(gitRoot)
	if err != nil {
		return FailedCheck("Agent skills", problem, fmt.Sprintf("Fix failed: %v", err))
	}
	plan = applied
	if len(plan.Conflicts) > 0 {
		return WarningCheck("Agent skills", fmt.Sprintf("reconciled with %d preserved conflict(s)", len(plan.Conflicts)), describeSkillConflicts(plan.Conflicts))
	}
	if withheld := plan.WithheldTeamSkills(); len(withheld) > 0 {
		return WarningCheck("Agent skills",
			fmt.Sprintf("reconciled %d file change(s); %s",
				len(plan.Creates)+len(plan.Updates)+len(plan.Removes), describeWithheldTeamSkills(withheld)),
			teamSkillApprovalHint(gitRoot))
	}
	return PassedCheck("Agent skills", fmt.Sprintf("reconciled %d file change(s) across %d native target(s)", len(plan.Creates)+len(plan.Updates)+len(plan.Removes), plan.TargetCount))
}

// describeSkillConflicts renders each preserved conflict as "<path> —
// <reason>" so `ox doctor` tells the user exactly which files to resolve
// and why ox left them alone, instead of just a count.
func describeSkillConflicts(conflicts []skillmanager.Conflict) string {
	if len(conflicts) == 0 {
		return ""
	}
	parts := make([]string, 0, len(conflicts))
	for _, c := range conflicts {
		parts = append(parts, fmt.Sprintf("%s — %s", c.Path, c.Reason))
	}
	return strings.Join(parts, "; ")
}

// describeWithheldTeamSkills names each team skill ox declined to materialize
// and what it would be approving, mirroring describeSkillConflicts.
//
// The reason is carried, not summarized to a count: "2 team skills need
// approval" tells a human nothing they can act on, whereas
// "deploy — needs approval: bundled-script (scripts/run.sh (under scripts/))"
// names the exact file they have to read before deciding.
func describeWithheldTeamSkills(withheld []skillmanager.TeamSkillDecision) string {
	parts := make([]string, 0, len(withheld))
	for _, d := range withheld {
		parts = append(parts, fmt.Sprintf("%s — %s", d.Name, d.Reason))
	}
	// Two different situations share NeedsApprove, and the count line must not
	// collapse them: a skill installed without its scripts is on disk and usable;
	// one whose manifest needs approval is not there at all.
	var held, partial int
	for _, d := range withheld {
		if d.InstalledAs == "" {
			held++
		} else {
			partial++
		}
	}
	var summary []string
	if held > 0 {
		summary = append(summary, fmt.Sprintf("%d withheld", held))
	}
	if partial > 0 {
		summary = append(summary, fmt.Sprintf("%d installed without scripts", partial))
	}
	return fmt.Sprintf("team skills %s: %s", strings.Join(summary, ", "), strings.Join(parts, "; "))
}

// teamSkillApprovalHint points at the committed approval store.
//
// It names the FILE rather than a command because there is no approval command
// yet — the store is written by hand or by a reviewer. Promising a command that
// does not exist would be worse than the silence this check replaces.
func teamSkillApprovalHint(repoRoot string) string {
	return fmt.Sprintf("Review the skill in your team context, then record a digest-pinned approval in %s",
		teamskills.ApprovalPath(repoRoot))
}

func describeSkillPlan(plan *skillmanager.ReconcilePlan) string {
	parts := make([]string, 0, 5)
	if plan.RetiredSelections {
		parts = append(parts, "retired skill selections")
	}
	if len(plan.Creates) > 0 {
		parts = append(parts, fmt.Sprintf("%d missing", len(plan.Creates)))
	}
	if len(plan.Updates) > 0 {
		parts = append(parts, fmt.Sprintf("%d outdated", len(plan.Updates)))
	}
	if len(plan.Removes) > 0 {
		parts = append(parts, fmt.Sprintf("%d retired", len(plan.Removes)))
	}
	if len(plan.Conflicts) > 0 {
		parts = append(parts, fmt.Sprintf("%d preserved conflict(s)", len(plan.Conflicts)))
	}
	return strings.Join(parts, "; ")
}

func init() {
	RegisterDoctorCheck(&DoctorCheck{
		Slug:        CheckSlugClaudeSkills,
		Name:        "Agent skills",
		Category:    "Integration",
		FixLevel:    FixLevelAuto,
		Description: "Reconciles project-selected native Agent Skills targets",
		Run:         checkClaudeSkills,
	})
}
