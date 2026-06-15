package main

import (
	"fmt"
	"strings"

	"github.com/sageox/ox/internal/session/adapters"
	"github.com/sageox/ox/internal/version"
	"github.com/sageox/ox/pkg/adapterprotocol"
)

// checkAdapterRules validates that ox coworker rules are installed and current
// for EVERY external adapter that installs rules (claude-code AND droid both
// declare CapRulesInstaller). Unlike commands — where only the first adapter
// installs the ox-* slash commands — rules are installed per-agent into
// agent-specific rules roots (.claude/rules/, .factory/rules/), so the check
// must iterate all rules-capable adapters, not just the first.
func checkAdapterRules(fix bool) checkResult {
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return SkippedCheck("Coworker rules", "not in git repo", "")
	}

	eas := findRulesAdapters()
	if len(eas) == 0 {
		// codex-only repo (or no rules-capable adapter discovered): nothing to
		// check. Skip gracefully rather than erroring — the deterministic floor
		// reaches every agent via Layer 1 regardless of installed rule files.
		return SkippedCheck("Coworker rules", "no rules adapters", "")
	}

	var problems []string // human-readable per-adapter drift descriptions
	var driftAdapters []*adapters.ExternalAdapter
	var warnings []string

	for _, ea := range eas {
		result, err := ea.CheckRules(gitRoot, version.Version)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: %v", ea.Name(), err))
			continue
		}
		if result.Installed {
			continue
		}

		driftAdapters = append(driftAdapters, ea)
		var parts []string
		if len(result.Missing) > 0 {
			parts = append(parts, fmt.Sprintf("%d missing: %s", len(result.Missing), strings.Join(result.Missing, ", ")))
		}
		if len(result.Stale) > 0 {
			parts = append(parts, fmt.Sprintf("%d outdated: %s", len(result.Stale), strings.Join(result.Stale, ", ")))
		}
		problems = append(problems, fmt.Sprintf("%s: %s", ea.Name(), strings.Join(parts, "; ")))
	}

	// All adapters reported their rules installed and current.
	if len(driftAdapters) == 0 {
		if len(warnings) > 0 {
			return WarningCheck("Coworker rules", "check error", strings.Join(warnings, "; "))
		}
		return PassedCheck("Coworker rules", fmt.Sprintf("current for %d adapter(s)", len(eas)))
	}

	problemStr := strings.Join(problems, "; ")

	if fix {
		var fixed int
		var fixErrs []string
		for _, ea := range driftAdapters {
			if _, err := ea.InstallRules(gitRoot, version.Version); err != nil {
				fixErrs = append(fixErrs, fmt.Sprintf("%s: %v", ea.Name(), err))
				continue
			}
			fixed++
		}
		if len(fixErrs) > 0 {
			return FailedCheck("Coworker rules", problemStr,
				fmt.Sprintf("Fix failed: %s", strings.Join(fixErrs, "; ")))
		}
		// Drift was fixed, but if a sibling adapter errored during CheckRules its
		// state is unknown — surface that as a warning rather than reporting a
		// clean PassedCheck, which would be a false-green.
		if len(warnings) > 0 {
			return WarningCheck("Coworker rules",
				fmt.Sprintf("restored rules for %d adapter(s)", fixed),
				strings.Join(warnings, "; "))
		}
		return PassedCheck("Coworker rules", fmt.Sprintf("restored rules for %d adapter(s)", fixed))
	}

	return FailedCheck("Coworker rules", problemStr,
		"Run `ox doctor --fix` or `ox init` to restore")
}

// findRulesAdapters returns ALL discovered external adapters that declare
// CapRulesInstaller. Both claude-code and droid install rule files, so unlike
// findCommandsAdapter (which returns only the first) this returns every match.
func findRulesAdapters() []*adapters.ExternalAdapter {
	var eas []*adapters.ExternalAdapter
	for _, ea := range adapters.DiscoverExternalAdapters() {
		if ea.HasCapability(adapterprotocol.CapRulesInstaller) {
			eas = append(eas, ea)
		}
	}
	return eas
}

func init() {
	RegisterDoctorCheck(&DoctorCheck{
		Slug:        CheckSlugAdapterRules,
		Name:        "Coworker rules",
		Category:    "Integration",
		FixLevel:    FixLevelAuto,
		Description: "Verifies ox coworker rules are installed and current for every rules-capable adapter",
		Run:         checkAdapterRules,
	})
}
