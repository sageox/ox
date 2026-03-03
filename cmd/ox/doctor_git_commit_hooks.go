package main

import (
	"github.com/sageox/ox/internal/config"
)

func init() {
	RegisterDoctorCheck(&DoctorCheck{
		Slug:        CheckSlugGitCommitHooks,
		Name:        "Git commit hooks",
		Category:    "Integration",
		FixLevel:    FixLevelSuggested,
		Description: "Verifies prepare-commit-msg hook is installed for commit attribution and session linking",
		Run: func(fix bool) checkResult {
			return checkGitCommitHooks(fix)
		},
	})
}

// checkGitCommitHooks verifies the ox prepare-commit-msg hook is installed.
// This hook appends configured trailers (Co-Authored-By, SageOx-Session) to
// commits automatically.
func checkGitCommitHooks(fix bool) checkResult {
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return SkippedCheck("Git commit hooks", "not in git repo", "")
	}

	if !config.IsInitialized(gitRoot) {
		return SkippedCheck("Git commit hooks", "not initialized", "")
	}

	if HasGitHooks(gitRoot) {
		return PassedCheck("Git commit hooks", "prepare-commit-msg installed")
	}

	if fix {
		if err := InstallGitHooks(gitRoot); err != nil {
			return FailedCheck("Git commit hooks", "repair failed", err.Error())
		}
		return PassedCheck("Git commit hooks", "installed prepare-commit-msg hook")
	}

	return FailedCheck("Git commit hooks",
		"prepare-commit-msg hook not installed",
		"Run `ox doctor --fix` or `ox integrate install` to install")
}
