package main

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/gitserver"
	"github.com/sageox/ox/internal/gitutil"
	"github.com/sageox/ox/internal/ledger"
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

	// stage per file through the pointer guard: hydrated artifacts must not be committed as raw content (#1174)
	ctx := context.Background()
	var committed bool
	err := gitutil.WithRepoLock(ctx, ledgerPath, func() error {
		staged, err := newSessionStageGuard(ledgerPath, filepath.Join(ledgerPath, "sessions")).stage(ctx)
		if err != nil {
			return err
		}
		if len(staged.Stage) == 0 {
			return nil
		}
		var commitErr error
		committed, commitErr = gitutil.CommitLedgerSnapshot(ctx, ledgerPath, "recover uncommitted sessions", "sessions/")
		return commitErr
	})
	if gitutil.IsRepoLockBusy(err) {
		return SkippedCheck(name, "ledger busy with another ox process", "Rerun `ox doctor --fix` in a moment")
	}
	if err != nil {
		return FailedCheck(name, "commit failed", fmt.Sprintf("session commit error: %s", err))
	}
	if !committed {
		return PassedCheck(name, "nothing safe to commit after staging")
	}

	if err := gitutil.PushWithRetry(context.Background(), ledgerPath, gitutil.PushOpts{
		AutoResolvePrefixes: ledger.AutoResolvePrefixes,
	}); err != nil {
		return WarningCheck(name,
			fmt.Sprintf("committed %d file(s) but push failed", count),
			fmt.Sprintf("push error: %s", err))
	}

	return PassedCheck(name, fmt.Sprintf("committed and pushed %d session file(s)", count))
}
