package main

import (
	"fmt"
	"strings"

	"github.com/sageox/ox/internal/session/adapters"
	"github.com/sageox/ox/internal/version"
	"github.com/sageox/ox/pkg/adapterprotocol"
)

// checkClaudeSkills validates that ox skills are installed under .claude/skills/.
// Mirrors checkClaudeCommands but targets the per-skill directory layout
// (.claude/skills/<name>/SKILL.md) via the skills_installer capability.
func checkClaudeSkills(fix bool) checkResult {
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return SkippedCheck("Claude skills", "not in git repo", "")
	}

	ea := findSkillsAdapter()
	if ea == nil {
		return SkippedCheck("Claude skills", "adapter not available", "")
	}

	result, err := ea.CheckSkills(gitRoot, version.Version)
	if err != nil {
		return WarningCheck("Claude skills", "check error", err.Error())
	}

	if result.Installed {
		return PassedCheck("Claude skills", fmt.Sprintf("%d installed", result.Total))
	}

	// build problem description
	var problems []string
	if len(result.Missing) > 0 {
		problems = append(problems, fmt.Sprintf("%d missing: %s", len(result.Missing), strings.Join(result.Missing, ", ")))
	}
	if len(result.Stale) > 0 {
		problems = append(problems, fmt.Sprintf("%d outdated: %s", len(result.Stale), strings.Join(result.Stale, ", ")))
	}
	problemStr := strings.Join(problems, "; ")

	if fix {
		installResult, installErr := ea.InstallSkills(gitRoot, version.Version)
		if installErr != nil {
			return FailedCheck("Claude skills", problemStr,
				fmt.Sprintf("Fix failed: %v", installErr))
		}
		return PassedCheck("Claude skills",
			fmt.Sprintf("restored %d skill(s)", len(installResult.FilesWritten)))
	}

	return FailedCheck("Claude skills", problemStr,
		"Run `ox doctor --fix` or `ox init` to restore")
}

// findSkillsAdapter returns the first external adapter with CapSkillsInstaller.
func findSkillsAdapter() *adapters.ExternalAdapter {
	for _, ea := range adapters.DiscoverExternalAdapters() {
		if ea.HasCapability(adapterprotocol.CapSkillsInstaller) {
			return ea
		}
	}
	return nil
}

func init() {
	RegisterDoctorCheck(&DoctorCheck{
		Slug:        CheckSlugClaudeSkills,
		Name:        "Claude skills",
		Category:    "Integration",
		FixLevel:    FixLevelAuto,
		Description: "Verifies ox skills are installed in .claude/skills/",
		Run:         checkClaudeSkills,
	})
}
