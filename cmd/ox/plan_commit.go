package main

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/gitserver"
)

// commitPlanToLedger durably commits a captured plan directory to the ledger
// and pushes it. This closes a real gap: plan.Save only materializes files into
// the ledger working tree, and commitAndPushLedger stages only sessions/<name>/
// — so without this, saved plans sit dirty-but-uncommitted indefinitely.
//
// Mirrors commitAndPushLedger's pattern (explicit-path `git add --sparse`,
// --no-verify commit, pushLedger with pull-rebase retry), scoped to the plan
// dir. Commit AND push are synchronous (the chosen durability model: the plan
// is on the remote before the caller returns). Best-effort on the caller's
// side: a push failure returns an error to log, but the local commit stands and
// the next push / `ox doctor` carries it.
func commitPlanToLedger(gitRoot, planDir string) error {
	ctx, err := config.LoadProjectContext(gitRoot)
	if err != nil || ctx == nil {
		return fmt.Errorf("no project context for %q: cannot commit plan", gitRoot)
	}
	ledgerPath := ctx.DefaultLedgerPath()
	if ledgerPath == "" {
		return fmt.Errorf("no ledger configured for %q: cannot commit plan", gitRoot)
	}

	// ensure .gitignore is in place before any commit to prevent cache leakage
	gitserver.EnsureGitignoreBeforeCommit(ledgerPath)

	// --sparse: ledger repos use sparse-checkout (cone mode).
	addArgs := []string{"-C", ledgerPath, "add", "--sparse", planDir}
	if out, err := exec.Command("git", addArgs...).CombinedOutput(); err != nil {
		return fmt.Errorf("git add failed: %s: %w", string(out), err)
	}

	commitMsg := fmt.Sprintf("plan: %s", filepath.Base(planDir))
	commitCmd := exec.Command("git", "-C", ledgerPath, "commit", "--no-verify", "-m", commitMsg)
	if out, err := commitCmd.CombinedOutput(); err != nil {
		if strings.Contains(string(out), "nothing to commit") {
			return nil // idempotent: re-save with no change
		}
		return fmt.Errorf("%s: %w", wrapCommitError(string(out), err), err)
	}

	return pushLedger(context.Background(), ledgerPath)
}
