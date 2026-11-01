package main

import (
	"strings"

	"github.com/sageox/ox/internal/config"
)

func init() {
	RegisterDoctorCheck(&DoctorCheck{
		Slug:        CheckSlugGitCommitHooks,
		Name:        "Git commit hooks",
		Category:    "Integration",
		FixLevel:    FixLevelSuggested,
		Description: "Verifies ox git hooks: prepare-commit-msg, post-commit, post-rewrite, and pre-push",
		Run: func(fix bool) checkResult {
			return checkGitCommitHooks(fix)
		},
	})
}

// checkGitCommitHooks verifies all ox-managed git hook sections are installed.
func checkGitCommitHooks(fix bool) checkResult {
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return SkippedCheck("Git commit hooks", "not in git repo", "")
	}

	if !config.IsInitialized(gitRoot) {
		return SkippedCheck("Git commit hooks", "not initialized", "")
	}

	missing := missingGitHooks(gitRoot)
	var names []string
	for _, spec := range allHookSpecs() {
		names = append(names, spec.name)
	}
	installedMessage := strings.Join(names, ", ") + " hooks installed"
	if len(missing) == 0 {
		return PassedCheck("Git commit hooks", installedMessage)
	}

	if fix {
		if err := InstallGitHooks(gitRoot); err != nil {
			return FailedCheck("Git commit hooks", "repair failed", err.Error())
		}
		return PassedCheck("Git commit hooks", installedMessage)
	}

	suffix := " hooks not installed"
	if len(missing) == 1 {
		suffix = " hook not installed"
	}
	return FailedCheck("Git commit hooks",
		strings.Join(missing, ", ")+suffix,
		"Run `ox doctor --fix` or `ox integrate install` to install")
}
