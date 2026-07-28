package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/daemon"
	"github.com/sageox/ox/internal/ledger"
)

// requiredSparseCone are the cone entries a ledger cannot function without.
// Both failures are silent and destructive — the cone doesn't just hide these
// paths, `git sparse-checkout set` DELETES them from the working tree — so
// doctor checks them explicitly rather than trusting ConfigureSparseCheckout
// to have been run by a build that knew about them.
var requiredSparseCone = []struct{ dir, impact string }{
	{".sageox", "codedb cache is wiped on every sparse-checkout set"},
	{"data/plans", "saved plans are deleted from the working tree ~60s after `ox plan save`"},
}

// normalizeConeEntry strips the surrounding slashes cone mode writes ("/.sageox/")
// so an entry compares equal to the bare directory name ConfigureSparseCheckout
// asks for (".sageox"). Without this the comparison never matches a real
// cone-mode file and every ledger reports a false failure.
func normalizeConeEntry(line string) string {
	return strings.Trim(strings.TrimSpace(line), "/")
}

// missingSparseConeDirs returns the requiredSparseCone entries absent from a
// sparse-checkout file's contents, preserving requiredSparseCone's order.
func missingSparseConeDirs(content string) []string {
	present := make(map[string]bool)
	for _, line := range strings.Split(content, "\n") {
		present[normalizeConeEntry(line)] = true
	}

	var missing []string
	for _, req := range requiredSparseCone {
		if !present[req.dir] {
			missing = append(missing, req.dir)
		}
	}
	return missing
}

// checkLedgerSparseCheckout verifies that the ledger repo's sparse-checkout
// cone includes every requiredSparseCone entry. A missing entry means git
// deletes that path from the working tree on the sync scheduler's next
// ~60s refresh.
func checkLedgerSparseCheckout(fix bool) checkResult {
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return SkippedCheck("Ledger sparse checkout", "not in git repo", "")
	}

	localCfg, err := config.LoadLocalConfig(gitRoot)
	if err != nil {
		return SkippedCheck("Ledger sparse checkout", "config error", "")
	}

	// resolve ledger path (same pattern as checkLedgerCheckoutGitignore)
	var ledgerPath string
	if localCfg.Ledger != nil && localCfg.Ledger.Path != "" {
		ledgerPath = localCfg.Ledger.Path
	} else {
		defaultPath, err := ledger.DefaultPath()
		if err != nil {
			return SkippedCheck("Ledger sparse checkout", "no ledger configured", "")
		}
		if !ledger.Exists(defaultPath) {
			return SkippedCheck("Ledger sparse checkout", "no ledger found", "")
		}
		ledgerPath = defaultPath
	}

	return checkLedgerSparseCheckoutAtPath(ledgerPath, fix)
}

// checkLedgerSparseCheckoutAtPath is checkLedgerSparseCheckout with the ledger
// already resolved. Split out so tests exercise THIS logic rather than a
// reimplementation of it — the test file previously carried its own copy of
// the cone matching, which meant a production-side change (like adding
// data/plans to requiredSparseCone) could not fail a single test.
func checkLedgerSparseCheckoutAtPath(ledgerPath string, fix bool) checkResult {
	if ledgerPath == "" {
		return SkippedCheck("Ledger sparse checkout", "no ledger path", "")
	}

	if !isGitRepo(ledgerPath) {
		return SkippedCheck("Ledger sparse checkout", "ledger not a git repo", "")
	}

	sparseFile := filepath.Join(ledgerPath, ".git", "info", "sparse-checkout")
	content, err := os.ReadFile(sparseFile)
	if err != nil {
		// no sparse-checkout file = not using sparse checkout, skip
		return SkippedCheck("Ledger sparse checkout", "sparse-checkout not enabled", "")
	}

	missing := missingSparseConeDirs(string(content))
	if len(missing) == 0 {
		return PassedCheck("Ledger sparse checkout", "required dirs in sparse-checkout cone")
	}

	missingList := strings.Join(missing, ", ")
	if fix {
		if repairErr := ledger.ConfigureSparseCheckout(ledgerPath); repairErr != nil {
			return FailedCheck("Ledger sparse checkout",
				fmt.Sprintf("%s missing from cone, repair failed", missingList),
				fmt.Sprintf("error: %v", repairErr))
		}
		return PassedCheck("Ledger sparse checkout",
			fmt.Sprintf("repaired: %s added to sparse-checkout cone", missingList))
	}

	var impacts strings.Builder
	for _, req := range requiredSparseCone {
		if slices.Contains(missing, req.dir) {
			fmt.Fprintf(&impacts, "        Without %s: %s.\n", req.dir, req.impact)
		}
	}

	return FailedCheck("Ledger sparse checkout",
		fmt.Sprintf("%s missing from sparse-checkout cone", missingList),
		impacts.String()+"        Run `ox doctor --fix` to auto-fix")
}

// checkCodeDBConsistency detects when codedb was previously indexed but the
// index directory is now missing — indicating data loss that requires a rebuild.
func checkCodeDBConsistency(fix bool) checkResult {
	projectRoot, err := findProjectRoot()
	if err != nil {
		return SkippedCheck("CodeDB consistency", "not in a project", "")
	}

	dataDir := resolveCodeDBDir(projectRoot)

	// try daemon first for LastIndexed info
	client := daemon.NewClientForCurrentRepoWithTimeout(500 * time.Millisecond)
	cs, daemonErr := client.CodeStatus()

	indexDirExists := false
	if _, statErr := os.Stat(dataDir); statErr == nil {
		indexDirExists = true
	}

	if daemonErr != nil {
		// daemon not running — check filesystem directly
		if !indexDirExists {
			// no daemon, no index dir = never indexed or cleanly removed
			return SkippedCheck("CodeDB consistency", "no index present", "")
		}
		// index dir exists, daemon not running — can't determine if it was fully indexed,
		// but the existing code-index check handles integrity
		return PassedCheck("CodeDB consistency", "index directory present")
	}

	// daemon responded
	if cs.LastIndexed.IsZero() && !indexDirExists {
		// never indexed — normal initial state
		return SkippedCheck("CodeDB consistency", "never indexed", "")
	}

	if !cs.LastIndexed.IsZero() && !indexDirExists {
		// was indexed but directory is gone — data loss
		return FailedCheck("CodeDB consistency",
			"index was built but is now missing",
			"codedb was last indexed at "+cs.LastIndexed.Format(time.RFC3339)+
				" but the index directory no longer exists.\n"+
				"        Run `ox code index` to rebuild")
	}

	return PassedCheck("CodeDB consistency", "index present and daemon aware")
}
