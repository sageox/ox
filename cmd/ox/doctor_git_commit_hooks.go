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
		// name what the repair added, so a fixed run reads differently from a clean one
		return PassedCheck("Git commit hooks", "installed "+hookNames(missing))
	}

	return FailedCheck("Git commit hooks",
		hookNames(missing)+" not installed",
		"Run `ox doctor --fix` or `ox integrate install` to install")
}

// hookNames renders hook names for doctor text: "post-commit hook" or
// "post-commit, post-rewrite hooks".
func hookNames(names []string) string {
	if len(names) == 1 {
		return names[0] + " hook"
	}
	return strings.Join(names, ", ") + " hooks"
}
