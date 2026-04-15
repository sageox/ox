package main

import (
	"fmt"
	"strings"

	"github.com/sageox/ox/internal/session/adapters"
	"github.com/sageox/ox/internal/version"
	"github.com/sageox/ox/pkg/adapterprotocol"
)

// checkClaudeCommands validates that ox slash commands are installed in .claude/commands/.
func checkClaudeCommands(fix bool) checkResult {
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return SkippedCheck("Claude commands", "not in git repo", "")
	}

	ea := findCommandsAdapter()
	if ea == nil {
		return SkippedCheck("Claude commands", "adapter not available", "")
	}

	result, err := ea.CheckCommands(gitRoot, version.Version)
	if err != nil {
		return WarningCheck("Claude commands", "check error", err.Error())
	}

	if result.Installed {
		return PassedCheck("Claude commands", fmt.Sprintf("%d installed", result.Total))
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
		installResult, installErr := ea.InstallCommands(gitRoot, version.Version)
		if installErr != nil {
			return FailedCheck("Claude commands", problemStr,
				fmt.Sprintf("Fix failed: %v", installErr))
		}
		return PassedCheck("Claude commands",
			fmt.Sprintf("restored %d command(s)", len(installResult.FilesWritten)))
	}

	return FailedCheck("Claude commands", problemStr,
		"Run `ox doctor --fix` or `ox init` to restore")
}

// findCommandsAdapter returns the first external adapter with CapCommandsInstaller.
func findCommandsAdapter() *adapters.ExternalAdapter {
	for _, ea := range adapters.DiscoverExternalAdapters() {
		if ea.HasCapability(adapterprotocol.CapCommandsInstaller) {
			return ea
		}
	}
	return nil
}

func init() {
	RegisterDoctorCheck(&DoctorCheck{
		Slug:        CheckSlugClaudeCommands,
		Name:        "Claude commands",
		Category:    "Integration",
		FixLevel:    FixLevelAuto,
		Description: "Verifies ox slash commands are installed in .claude/commands/",
		Run:         checkClaudeCommands,
	})
}
