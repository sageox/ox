package main

import (
	"fmt"
	"log/slog"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/ledger"
)

const CheckSlugGitHubDataMigration = "github-data-migration"

func init() {
	RegisterDoctorCheck(&DoctorCheck{
		Slug:        CheckSlugGitHubDataMigration,
		Name:        "GitHub data migration",
		Category:    "Ledger Git Health",
		FixLevel:    FixLevelAuto,
		Description: "Migrates legacy GitHub data filenames to content-hash and UUID7 formats",
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

	if !ledger.NeedsMigration(ledgerPath) {
		return PassedCheck(name, "data version current")
	}

	if !fix {
		return WarningCheck(name,
			"legacy GitHub data files need migration",
			"run `ox doctor --fix` to migrate filenames to content-hash format")
	}

	logger := slog.Default()
	var totalMigrated, totalDeleted int

	// Phase 0+1: Migrate legacy PR/issue files in ledger
	migrated, deleted, err := ledger.MigrateLegacyGitHubFiles(ledgerPath, logger)
	if err != nil {
		return FailedCheck(name, "migration failed", fmt.Sprintf("error: %v", err))
	}
	totalMigrated += migrated
	totalDeleted += deleted

	// Mark ledger migration done
	if markErr := ledger.MarkMigration(ledgerPath, ledger.MigrationContentHashFilenames); markErr != nil {
		logger.Warn("failed to mark content_hash_filenames migration", "error", markErr)
	}

	// Phase 2+3: Migrate fact files in team context
	factPhaseComplete := true
	refsPhaseComplete := true
	gitRoot := findGitRoot()
	if gitRoot != "" {
		tcs := config.FindAllTeamContexts(gitRoot)
		for _, tc := range tcs {
			if tc.Path == "" {
				continue
			}
			factsMigrated, factErr := MigrateLegacyFactFiles(tc.Path, logger)
			if factErr != nil {
				logger.Warn("fact file migration failed", "tc", tc.Path, "error", factErr)
				factPhaseComplete = false
				continue
			}
			totalMigrated += factsMigrated

			refsUpdated, refErr := UpdateDailySummaryRefs(tc.Path, logger)
			if refErr != nil {
				logger.Warn("daily summary ref update failed", "tc", tc.Path, "error", refErr)
				refsPhaseComplete = false
			}
			if refsUpdated > 0 {
				logger.Info("updated daily summary refs", "tc", tc.Path, "count", refsUpdated)
			}
		}
	}

	// Only mark fact-related migrations if ALL team contexts succeeded
	if factPhaseComplete {
		if markErr := ledger.MarkMigration(ledgerPath, ledger.MigrationUUID7FactFilenames); markErr != nil {
			logger.Warn("failed to mark uuid7_fact_filenames migration", "error", markErr)
		}
	}
	if refsPhaseComplete {
		if markErr := ledger.MarkMigration(ledgerPath, ledger.MigrationDailySummaryRefs); markErr != nil {
			logger.Warn("failed to mark daily_summary_refs migration", "error", markErr)
		}
	}

	if totalMigrated == 0 && totalDeleted == 0 {
		return PassedCheck(name, "no legacy files found (version marker set)")
	}

	msg := fmt.Sprintf("migrated %d file(s)", totalMigrated)
	if totalDeleted > 0 {
		msg += fmt.Sprintf(", deleted %d corrupted", totalDeleted)
	}
	return PassedCheck(name, msg)
}
