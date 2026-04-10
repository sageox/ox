package main

import (
	"fmt"
	"log/slog"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/internal/gitserver"
	"github.com/sageox/ox/internal/ledger"
)

const CheckSlugGitHubDataMigration = "github-data-migration"

func init() {
	RegisterDoctorCheck(&DoctorCheck{
		Slug:        CheckSlugGitHubDataMigration,
		Name:        "GitHub data migration",
		Category:    "Ledger Git Health",
		FixLevel:    FixLevelSuggested,
		Description: "Renames legacy GitHub data filenames to content-hash format and deletes corrupted files with conflict markers",
		Run:         func(fix bool) checkResult { return checkGitHubDataMigration(fix) },
	})
}

// checkGitHubDataMigration checks for legacy-format GitHub data files
// and migrates them on --fix.
func checkGitHubDataMigration(fix bool) checkResult {
	const name = "GitHub data migration"

	ledgerPath := getLedgerPath()
	if ledgerPath == "" {
		return SkippedCheck(name, "no ledger found", "")
	}

	// Scan to see if there's anything to do
	legacyFiles, corruptedFiles, scanErr := ledger.ScanLegacyGitHubFiles(ledgerPath)
	if scanErr != nil {
		return FailedCheck(name, "scan failed", fmt.Sprintf("error: %v", scanErr))
	}

	if len(legacyFiles) == 0 && len(corruptedFiles) == 0 {
		return PassedCheck(name, "no legacy or corrupted files found")
	}

	if !fix {
		msg := fmt.Sprintf("%d legacy, %d corrupted", len(legacyFiles), len(corruptedFiles))
		return WarningCheck(name, msg, "run `ox doctor --fix` to migrate legacy files and delete corrupted ones")
	}

	logger := slog.Default()

	migrated, deleted, err := ledger.MigrateLegacyGitHubFiles(ledgerPath, logger)
	if err != nil {
		return FailedCheck(name, "migration failed", fmt.Sprintf("error: %v", err))
	}

	if migrated == 0 && deleted == 0 {
		return PassedCheck(name, "no legacy files found")
	}

	// commit the renames and deletions to the ledger
	if err := commitGitHubMigration(ledgerPath, migrated, deleted); err != nil {
		return FailedCheck(name, "migration succeeded but commit failed", fmt.Sprintf("error: %v", err))
	}

	msg := fmt.Sprintf("migrated %d file(s)", migrated)
	if deleted > 0 {
		msg += fmt.Sprintf(", deleted %d corrupted", deleted)
	}
	return PassedCheck(name, msg)
}

// commitGitHubMigration stages and commits only the migration changes in the ledger.
// Scopes staging to data/github/ to avoid sweeping unrelated dirty files into the commit.
func commitGitHubMigration(ledgerPath string, migrated, deleted int) error {
	gitserver.EnsureGitignoreBeforeCommit(ledgerPath)

	// stage only the github data directory — not the entire ledger
	githubDataDir := filepath.Join("data", "github")
	addCmd := exec.Command("git", "-C", ledgerPath, "add", "--sparse", "-A", githubDataDir)
	if output, err := addCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git add: %w: %s", err, strings.TrimSpace(string(output)))
	}

	commitMsg := fmt.Sprintf("ox doctor: migrate %d legacy GitHub data file(s)", migrated)
	if deleted > 0 {
		commitMsg += fmt.Sprintf(", delete %d corrupted", deleted)
	}
	commitCmd := exec.Command("git", "-C", ledgerPath, "commit", "-m", commitMsg)
	if output, err := commitCmd.CombinedOutput(); err != nil {
		errStr := strings.TrimSpace(string(output))
		if strings.Contains(errStr, "nothing to commit") {
			return nil
		}
		return fmt.Errorf("git commit: %w: %s", err, errStr)
	}

	return nil
}
