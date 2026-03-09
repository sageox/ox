package main

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/gitserver"
)

func init() {
	RegisterDoctorCheck(&DoctorCheck{
		Slug:        CheckSlugSessionUncommitted,
		Name:        "uncommitted session files",
		Category:    "Sessions",
		FixLevel:    FixLevelSuggested,
		Description: "Detects session files in ledger that were never committed",
		Run:         checkSessionUncommitted,
	})
}

// checkSessionUncommitted looks for session files in the ledger working
// directory that were written but never git-committed. This can happen when
// session stop partially fails after writing files but before committing.
func checkSessionUncommitted(fix bool) checkResult {
	const name = "uncommitted session files"

	gitRoot := findGitRoot()
	if gitRoot == "" {
		return SkippedCheck(name, "no git root", "")
	}

	if !config.IsInitialized(gitRoot) {
		return SkippedCheck(name, "not initialized", "")
	}

	ledgerPath := getLedgerPath()
	if ledgerPath == "" {
		return SkippedCheck(name, "no ledger", "")
	}

	if !isGitRepo(ledgerPath) {
		return SkippedCheck(name, "ledger not a git repo", "")
	}

	output, err := exec.Command("git", "-C", ledgerPath, "status", "--porcelain", "sessions/").Output()
	if err != nil {
		return SkippedCheck(name, "git status failed", err.Error())
	}

	trimmed := strings.TrimSpace(string(output))
	if trimmed == "" {
		return PassedCheck(name, "no uncommitted session files")
	}

	lines := strings.Split(trimmed, "\n")
	count := len(lines)

	if !fix {
		return WarningCheck(name,
			fmt.Sprintf("%d uncommitted session file(s)", count),
			"run `ox doctor --fix` to commit and push")
	}

	return fixSessionUncommitted(ledgerPath, count)
}

// fixSessionUncommitted stages, commits, and pushes uncommitted session files.
func fixSessionUncommitted(ledgerPath string, count int) checkResult {
	const name = "uncommitted session files"

	// ensure .gitignore is in place before any commit to prevent cache file leakage
	gitserver.EnsureGitignoreBeforeCommit(ledgerPath)

	// --sparse: ledger repos use sparse-checkout
	if out, err := exec.Command("git", "-C", ledgerPath, "add", "--sparse", "sessions/").CombinedOutput(); err != nil {
		return FailedCheck(name, "staging failed",
			fmt.Sprintf("git add error: %s", strings.TrimSpace(string(out))))
	}

	if out, err := exec.Command("git", "-C", ledgerPath, "commit", "-m", "recover uncommitted sessions").CombinedOutput(); err != nil {
		errStr := strings.TrimSpace(string(out))
		if strings.Contains(errStr, "nothing to commit") {
			return PassedCheck(name, "nothing to commit after staging")
		}
		return FailedCheck(name, "commit failed",
			fmt.Sprintf("git commit error: %s", errStr))
	}

	// no --force: ledger history must never be rewritten
	if out, err := exec.Command("git", "-C", ledgerPath, "push").CombinedOutput(); err != nil {
		return WarningCheck(name,
			fmt.Sprintf("committed %d file(s) but push failed", count),
			fmt.Sprintf("git push error: %s", strings.TrimSpace(string(out))))
	}

	return PassedCheck(name, fmt.Sprintf("committed and pushed %d session file(s)", count))
}
