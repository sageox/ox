package main

import (
	"fmt"
	"strings"

	"github.com/sageox/ox/internal/skillmanager"
)

// checkClaudeSkills retains its historical function name, but checks every
// project-selected native inventory target (skills and ox-owned rules) from
// .sageox/skills.lock.json. Detection is consulted only for legacy migration.
func checkClaudeSkills(fix bool) checkResult {
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return SkippedCheck("AI coworker assets", "not in git repo", "")
	}
	plan, err := planCommittedSkills(gitRoot)
	if err != nil {
		return WarningCheck("AI coworker assets", "cannot inspect managed assets", err.Error())
	}
	if plan.TargetCount == 0 && !plan.RetiredSelections {
		return SkippedCheck("AI coworker assets", "no project-selected native targets", "Run `ox init` and select an AI coworker")
	}
	if len(plan.Warnings) > 0 {
		return WarningCheck("AI coworker assets", strings.Join(plan.Warnings, "; "), "Use the same or a newer ox version before reconciling")
	}
	if unusable := plan.UnusableTeamSkills(); len(unusable) > 0 {
		return WarningCheck("AI coworker assets", describeUnusableTeamSkills(unusable),
			"Rename each skill in the Team Context, including its directory and the name: key in SKILL.md")
	}
	if len(plan.Creates)+len(plan.Updates)+len(plan.Removes) == 0 && len(plan.Conflicts) == 0 && !plan.RetiredSelections {
		// Converged is not the same as complete. A team skill withheld pending
		// approval leaves the repository looking exactly as it would if nobody had
		// authored it, so this is the one place a human finds out it exists.
		if withheld := plan.WithheldTeamSkills(); len(withheld) > 0 {
			return WarningCheck("AI coworker assets", describeWithheldTeamSkills(withheld),
				teamSkillApprovalHint(withheld))
		}
		return PassedCheck("AI coworker assets", fmt.Sprintf("%d managed files across %d native target(s)", plan.DesiredFileCount, plan.TargetCount))
	}

	problem := describeSkillPlan(plan)
	if !fix {
		if len(plan.Conflicts) > 0 && len(plan.Creates)+len(plan.Updates)+len(plan.Removes) == 0 && !plan.RetiredSelections {
			return WarningCheck("AI coworker assets", problem, describeSkillConflicts(plan.Conflicts))
		}
		return FailedCheck("AI coworker assets", problem, "Run `ox doctor --fix` to reconcile unchanged managed files")
	}
	applied, err := reconcileCommittedSkills(gitRoot)
	if err != nil {
		return FailedCheck("AI coworker assets", problem, fmt.Sprintf("Fix failed: %v", err))
	}
	plan = applied
	if len(plan.Conflicts) > 0 {
		return WarningCheck("AI coworker assets", fmt.Sprintf("reconciled with %d preserved conflict(s)", len(plan.Conflicts)), describeSkillConflicts(plan.Conflicts))
	}
	if unusable := plan.UnusableTeamSkills(); len(unusable) > 0 {
		return WarningCheck("AI coworker assets", describeUnusableTeamSkills(unusable),
			"Rename each skill in the Team Context, including its directory and the name: key in SKILL.md")
	}
	if withheld := plan.WithheldTeamSkills(); len(withheld) > 0 {
		return WarningCheck("AI coworker assets",
			fmt.Sprintf("reconciled %d file change(s); %s",
				len(plan.Creates)+len(plan.Updates)+len(plan.Removes), describeWithheldTeamSkills(withheld)),
			teamSkillApprovalHint(withheld))
	}
	return PassedCheck("AI coworker assets", fmt.Sprintf("reconciled %d file change(s) across %d native target(s)", len(plan.Creates)+len(plan.Updates)+len(plan.Removes), plan.TargetCount))
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

// teamSkillApprovalHint names the command that opens the gate.
//
// It used to name the approval FILE instead, because no approval command
// existed and promising one would have been worse than silence. `ox skills
// approve` now exists, so the hint names the action rather than asking a human
// to hand-compute a sha256 into committed JSON.
func teamSkillApprovalHint(withheld []skillmanager.TeamSkillDecision) string {
	if len(withheld) == 0 {
		return "Review the skill in the Team Context, then run `ox skills approve` to see what is waiting and approve it"
	}
	skill := withheld[0]
	if skill.InstalledAs != "" {
		return fmt.Sprintf("Review %s in the Team Context, then run `ox skills approve --allow-scripts %s`", skill.Name, skill.Name)
	}
	return fmt.Sprintf("Review %s in the Team Context, then run `ox skills approve %s`", skill.Name, skill.Name)
}

func describeUnusableTeamSkills(unusable []skillmanager.TeamSkillDecision) string {
	parts := make([]string, 0, len(unusable))
	for _, skill := range unusable {
		parts = append(parts, fmt.Sprintf("%s — %s", skill.Name, skill.Reason))
	}
	return "unusable team skills: " + strings.Join(parts, "; ")
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
		Name:        "AI coworker assets",
		Category:    "Integration",
		FixLevel:    FixLevelAuto,
		Description: "Reconciles project-selected native skill and rule targets",
		Run:         checkClaudeSkills,
	})
	RegisterDoctorCheckAlias(CheckSlugAdapterRules, CheckSlugClaudeSkills)
}
