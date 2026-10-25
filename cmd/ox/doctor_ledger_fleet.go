package main

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/internal/doctor/autofix"
	"github.com/sageox/ox/internal/endpoint"
)

func init() {
	RegisterDoctorCheck(&DoctorCheck{
		Slug:        CheckSlugLedgerFleetHealth,
		Name:        "Ledger fleet health",
		Category:    "Ledger Fleet Health",
		FixLevel:    FixLevelAuto,
		Description: "Checks every local Ledger checkout for a stale git operation, including repos not currently open",
		Run:         checkLedgerFleetHealth,
	})
}

func checkLedgerFleetHealth(fix bool) checkResult {
	projectRoot := findGitRoot()
	if projectRoot == "" {
		return SkippedCheck("Ledger fleet health", "not in git repo", "")
	}
	ledgers, err := autofix.DiscoverLedgerPaths(endpoint.GetForProject(projectRoot))
	if err != nil {
		return FailedCheck("Ledger fleet health", "could not enumerate local Ledgers", err.Error())
	}
	return checkLedgerFleetHealthPaths(ledgers, fix)
}

func checkLedgerFleetHealthPaths(ledgers []string, fix bool) checkResult {
	const name = "Ledger fleet health"
	if len(ledgers) == 0 {
		return SkippedCheck(name, "no local Ledgers", "")
	}

	children := make([]checkResult, 0)
	fixed := 0
	onlyWarnings := true
	for _, ledgerPath := range ledgers {
		result := checkLedgerStuckOperationAt(ledgerPath, fix)
		if result.passed && !result.warning && !result.skipped {
			if strings.HasPrefix(result.message, "cleared stuck ") {
				fixed++
			}
			continue
		}
		result.name = filepath.Base(ledgerPath)
		result.slug = CheckSlugLedgerStuckOperation
		result.fixLevel = FixLevelAuto
		onlyWarnings = onlyWarnings && result.warning
		children = append(children, result)
	}

	if len(children) > 0 {
		var result checkResult
		if onlyWarnings {
			result = WarningCheck(name,
				fmt.Sprintf("%d of %d local Ledgers are busy; recovery deferred", len(children), len(ledgers)),
				"another ox operation currently owns each Ledger lock")
		} else {
			result = CriticalCheck(name,
				fmt.Sprintf("%d of %d local Ledgers need attention", len(children), len(ledgers)),
				"stale git operations block background synchronization")
		}
		result.children = children
		return result
	}
	if fixed > 0 {
		return PassedCheck(name, fmt.Sprintf("cleared stale operations in %d of %d local Ledgers", fixed, len(ledgers)))
	}
	return PassedCheck(name, fmt.Sprintf("%d local Ledgers healthy", len(ledgers)))
}
