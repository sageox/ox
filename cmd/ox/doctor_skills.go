package main

import (
	"fmt"
	"strings"

	"github.com/sageox/ox/internal/session/adapters"
	"github.com/sageox/ox/internal/version"
	"github.com/sageox/ox/pkg/adapterprotocol"
)

// checkClaudeSkills validates that ox skills are installed and current for EVERY
// external adapter that installs skills (not just the first). Mirrors
// checkAdapterRules: skills land in agent-specific roots (.claude/skills/...), so
// if a second adapter ever gains CapSkillsInstaller the check must cover it too
// rather than silently inspecting only the first. Targets the per-skill directory
// layout (.claude/skills/<name>/SKILL.md) via the skills_installer capability.
func checkClaudeSkills(fix bool) checkResult {
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return SkippedCheck("Claude skills", "not in git repo", "")
	}

	eas := findSkillsAdapters()
	if len(eas) == 0 {
		return SkippedCheck("Claude skills", "no skills adapters", "")
	}

	var problems []string // human-readable per-adapter drift descriptions
	var driftAdapters []*adapters.ExternalAdapter
	var warnings []string
	var total int

	for _, ea := range eas {
		result, err := ea.CheckSkills(gitRoot, version.Version)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: %v", ea.Name(), err))
			continue
		}
		total += result.Total
		if result.Installed {
			continue
		}

		driftAdapters = append(driftAdapters, ea)
		var parts []string
		if len(result.Missing) > 0 {
			parts = append(parts, fmt.Sprintf("%d missing: %s", len(result.Missing), strings.Join(result.Missing, ", ")))
		}
		if len(result.Stale) > 0 {
			parts = append(parts, fmt.Sprintf("%d outdated: %s", len(result.Stale), strings.Join(result.Stale, ", ")))
		}
		problems = append(problems, fmt.Sprintf("%s: %s", ea.Name(), strings.Join(parts, "; ")))
	}

	// All adapters reported their skills installed and current.
	if len(driftAdapters) == 0 {
		if len(warnings) > 0 {
			return WarningCheck("Claude skills", "check error", strings.Join(warnings, "; "))
		}
		return PassedCheck("Claude skills", fmt.Sprintf("%d installed", total))
	}

	problemStr := strings.Join(problems, "; ")

	if fix {
		var fixed int
		var fixErrs []string
		for _, ea := range driftAdapters {
			if _, err := ea.InstallSkills(gitRoot, version.Version); err != nil {
				fixErrs = append(fixErrs, fmt.Sprintf("%s: %v", ea.Name(), err))
				continue
			}
			fixed++
		}
		if len(fixErrs) > 0 {
			return FailedCheck("Claude skills", problemStr,
				fmt.Sprintf("Fix failed: %s", strings.Join(fixErrs, "; ")))
		}
		return PassedCheck("Claude skills", fmt.Sprintf("restored skills for %d adapter(s)", fixed))
	}

	return FailedCheck("Claude skills", problemStr,
		"Run `ox doctor --fix` or `ox init` to restore")
}

// findSkillsAdapters returns ALL discovered external adapters that declare
// CapSkillsInstaller. Mirrors findRulesAdapters: should a second adapter ever
// gain skills support, the doctor check must cover every one — unlike
// findCommandsAdapter (which returns only the first), this returns every match.
func findSkillsAdapters() []*adapters.ExternalAdapter {
	var eas []*adapters.ExternalAdapter
	for _, ea := range adapters.DiscoverExternalAdapters() {
		if ea.HasCapability(adapterprotocol.CapSkillsInstaller) {
			eas = append(eas, ea)
		}
	}
	return eas
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
