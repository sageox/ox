package main

import (
	"fmt"
	"log/slog"

	"github.com/sageox/ox/internal/ledger"
)

const CheckSlugGitHubDataMigration = "github-data-migration"

func init() {
	RegisterDoctorCheck(&DoctorCheck{
		Slug:        CheckSlugGitHubDataMigration,
		Name:        "GitHub data migration",
		Category:    "Ledger Git Health",
		FixLevel:    FixLevelAuto,
		Description: "Renames legacy GitHub data filenames to content-hash format and repairs corrupted files",
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
		return WarningCheck(name, msg, "run `ox doctor --fix` to migrate")
	}

	logger := slog.Default()

	migrated, deleted, err := ledger.MigrateLegacyGitHubFiles(ledgerPath, logger)
	if err != nil {
		return FailedCheck(name, "migration failed", fmt.Sprintf("error: %v", err))
	}

	if migrated == 0 && deleted == 0 {
		return PassedCheck(name, "no legacy files found")
	}

	msg := fmt.Sprintf("migrated %d file(s)", migrated)
	if deleted > 0 {
		msg += fmt.Sprintf(", deleted %d corrupted", deleted)
	}
	return PassedCheck(name, msg)
}
