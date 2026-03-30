package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/daemon"
	"github.com/sageox/ox/internal/ledger"
)

// checkLedgerSparseCheckout verifies that the ledger repo's sparse-checkout
// cone includes .sageox. Without it, the codedb cache directory gets wiped
// on every sparse-checkout set operation.
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

	if !isGitRepo(ledgerPath) {
		return SkippedCheck("Ledger sparse checkout", "ledger not a git repo", "")
	}

	sparseFile := filepath.Join(ledgerPath, ".git", "info", "sparse-checkout")
	content, err := os.ReadFile(sparseFile)
	if err != nil {
		// no sparse-checkout file = not using sparse checkout, skip
		return SkippedCheck("Ledger sparse checkout", "sparse-checkout not enabled", "")
	}

	// look for .sageox in the cone entries
	lines := strings.Split(string(content), "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == ".sageox" || trimmed == ".sageox/" {
			return PassedCheck("Ledger sparse checkout", ".sageox in sparse-checkout cone")
		}
	}

	// .sageox missing from sparse-checkout
	if fix {
		if repairErr := ledger.ConfigureSparseCheckout(ledgerPath); repairErr != nil {
			return FailedCheck("Ledger sparse checkout",
				".sageox missing from cone, repair failed",
				fmt.Sprintf("error: %v", repairErr))
		}
		return PassedCheck("Ledger sparse checkout", "repaired: .sageox added to sparse-checkout cone")
	}

	return FailedCheck("Ledger sparse checkout",
		".sageox missing from sparse-checkout cone",
		"Without .sageox in the cone, codedb cache gets wiped on sparse-checkout set.\n"+
			"        Run `ox doctor` to auto-fix")
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
