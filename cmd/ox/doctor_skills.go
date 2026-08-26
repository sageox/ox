package main

import (
	"fmt"
	"strings"

	"github.com/sageox/ox/internal/skillmanager"
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
	if plan.TargetCount == 0 {
		return SkippedCheck("Agent skills", "no project-selected skill targets", "Run `ox init` and select an AI coworker with native Agent Skills")
	}
	if len(plan.Warnings) > 0 {
		return WarningCheck("Agent skills", strings.Join(plan.Warnings, "; "), "Use the same or a newer ox version before reconciling")
	}
	if len(plan.Creates)+len(plan.Updates)+len(plan.Removes) == 0 && len(plan.Conflicts) == 0 {
		return PassedCheck("Agent skills", fmt.Sprintf("%d managed files across %d native target(s)", plan.DesiredFileCount, plan.TargetCount))
	}

	problem := describeSkillPlan(plan)
	if !fix {
		if len(plan.Conflicts) > 0 && len(plan.Creates)+len(plan.Updates)+len(plan.Removes) == 0 {
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

func describeSkillPlan(plan *skillmanager.ReconcilePlan) string {
	parts := make([]string, 0, 4)
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
