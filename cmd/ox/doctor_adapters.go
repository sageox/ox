package main

import (
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"

	"github.com/sageox/ox/internal/daemon"
	"github.com/sageox/ox/internal/session/adapters"
	"github.com/sageox/ox/internal/version"
	"github.com/sageox/ox/pkg/adapterprotocol"
)

// checkExternalAdapters discovers external adapters, registers them into the
// adapter registry (so they're available for session detection), and runs their
// diagnose subcommand, converting results into doctor checkResults.
func checkExternalAdapters(opts doctorOptions) []checkResult {
	// register first so external adapters are in the registry for session detection
	// after doctor runs. DiscoverExternalAdapters is called again below because
	// RegisterExternalAdapters doesn't return the list — it only mutates the registry.
	if err := adapters.RegisterExternalAdapters(); err != nil {
		slog.Warn("external adapter registration failed during doctor", "error", err)
	}

	discovered := adapters.DiscoverExternalAdapters()
	if len(discovered) == 0 {
		return nil // no adapters found, silently skip
	}

	var results []checkResult
	gitRoot := findGitRoot()

	for _, ea := range discovered {
		name := ea.Name()

		diagnoseResult, err := ea.Diagnose(gitRoot, "project", version.Version)
		if err != nil {
			// adapter crashed or timed out -- report as a warning, continue
			severity := "warning"
			if errors.Is(err, adapters.ErrAdapterTimeout) {
				slog.Warn("adapter diagnose timed out", "adapter", name, "error", err)
			} else {
				slog.Warn("adapter diagnose failed", "adapter", name, "error", err)
			}
			results = append(results, adapterErrorToCheckResult(name, err, severity))
			continue
		}

		if diagnoseResult.OK && len(diagnoseResult.Issues) == 0 {
			results = append(results, PassedCheck(
				name+" adapter",
				"all checks passed",
			))
			continue
		}

		// If the target CLI isn't installed, skip ALL issues from this adapter.
		// Most users only have one or two agents — showing "hooks not installed"
		// for absent CLIs is noise, not signal.
		cliNotInstalled := false
		for _, issue := range diagnoseResult.Issues {
			if strings.HasSuffix(issue.Slug, ":not-installed") || issue.Slug == "not-installed" {
				cliNotInstalled = true
				break
			}
		}
		if cliNotInstalled {
			continue
		}

		for _, issue := range diagnoseResult.Issues {
			slug := adapterIssueSlug(name, issue.Slug)
			cr := adapterIssueToCheckResult(name, issue)
			cr.slug = slug

			// handle fix execution for adapter-reported fixes
			if issue.Fix != "" && opts.shouldFixAdapterSlug(slug, issue.FixSafe) {
				fixErr := runAdapterFix(issue.Fix, issue.FixSafe, opts.forceYes)
				if fixErr != nil {
					cr.detail = fmt.Sprintf("fix failed: %s", fixErr)
				} else {
					// re-run diagnose to verify the fix worked
					verified := verifyAdapterFix(ea, gitRoot, issue.Slug)
					if verified {
						cr = PassedCheck(cr.name, "fixed: "+issue.Title)
						cr.slug = slug
					} else {
						cr.detail = "fix applied but issue persists"
					}
				}
			}

			results = append(results, cr)
		}
	}

	return results
}

// checkAdapterProcessHealth queries the daemon's adapter supervisor status
// and reports any adapters in crashed or degraded state. Returns nil if the
// daemon is not running or has no adapter status.
func checkAdapterProcessHealth(status *daemon.StatusData, verbose bool) []checkResult {
	if status == nil || len(status.Adapters) == 0 {
		return nil
	}

	var results []checkResult

	for adapterType, as := range status.Adapters {
		switch as.State {
		case "degraded":
			detail := fmt.Sprintf("respawn_count=%d, adapter has been disabled after repeated crashes", as.RespawnCount)
			if verbose && len(as.StderrTail) > 0 {
				detail += "\nstderr:\n  " + strings.Join(as.StderrTail, "\n  ")
			}
			results = append(results, WarningCheck(
				adapterType+" adapter process",
				"adapter degraded: max respawns exceeded",
				detail,
			).WithFixInfo(adapterType+":process-degraded", FixLevelCheckOnly))

		case "crashed":
			detail := fmt.Sprintf("respawn_count=%d, adapter process crashed and is awaiting respawn", as.RespawnCount)
			if verbose && len(as.StderrTail) > 0 {
				detail += "\nstderr:\n  " + strings.Join(as.StderrTail, "\n  ")
			}
			results = append(results, WarningCheck(
				adapterType+" adapter process",
				"adapter crashed",
				detail,
			).WithFixInfo(adapterType+":process-crashed", FixLevelCheckOnly))

		case "running":
			if as.RespawnCount > 0 {
				// running but has respawned -- surface as info for awareness
				results = append(results, InfoCheck(
					adapterType+" adapter process",
					fmt.Sprintf("running (respawned %d time(s))", as.RespawnCount),
					fmt.Sprintf("pid=%d, sessions=%d", as.PID, len(as.Sessions)),
				))
			}
			// healthy running adapters with no respawns are not reported to reduce noise
		}
	}

	return results
}

// adapterIssueSlug returns the namespaced slug for an adapter issue.
func adapterIssueSlug(adapterName, issueSlug string) string {
	return adapterName + ":" + issueSlug
}

// isAdapterSlug returns true if the slug contains a colon, indicating
// it is namespaced to an adapter (e.g., "claude-code:hooks-missing").
func isAdapterSlug(slug string) bool {
	return strings.Contains(slug, ":")
}

// adapterIssueToCheckResult converts a DiagnoseIssue into a doctor checkResult.
func adapterIssueToCheckResult(adapterName string, issue adapterprotocol.DiagnoseIssue) checkResult {
	displayName := adapterName + ": " + issue.Title

	detail := issue.Detail
	if issue.Fix != "" {
		if detail != "" {
			detail += " | "
		}
		detail += "fix: " + issue.Fix
	}

	switch issue.Severity {
	case "error":
		return CriticalCheck(displayName, issue.Title, detail)
	case "warning":
		return WarningCheck(displayName, issue.Title, detail)
	case "info":
		return InfoCheck(displayName, issue.Title, detail)
	default:
		return WarningCheck(displayName, issue.Title, detail)
	}
}

// adapterErrorToCheckResult creates a check result for an adapter that failed
// to run diagnose (crash, timeout, etc.).
func adapterErrorToCheckResult(adapterName string, err error, _ string) checkResult {
	return WarningCheck(
		adapterName+" adapter",
		"diagnose failed: "+err.Error(),
		"adapter may be misconfigured or crashing",
	).WithFixInfo(adapterName+":diagnose-error", FixLevelCheckOnly)
}

// shouldFixAdapterSlug checks whether an adapter-namespaced slug should be
// fixed. Adapter slugs are not in the DoctorCheckRegistry, so we handle them
// separately from core slugs.
func (opts doctorOptions) shouldFixAdapterSlug(slug string, fixSafe bool) bool {
	// check if this specific slug was requested via --fix-slug
	for _, s := range opts.fixSlugs {
		if s == slug {
			return true
		}
	}
	// if --fix flag is set and the fix is safe, apply it
	if opts.fix && fixSafe {
		return true
	}
	return false
}

// runAdapterFix executes a fix command reported by an adapter.
// If fixSafe is false and forceYes is false, this is a no-op (the fix
// requires user confirmation which is not yet implemented for adapter fixes).
func runAdapterFix(fixCmd string, fixSafe, forceYes bool) error {
	if !fixSafe && !forceYes {
		// non-safe fixes require explicit confirmation
		// for now, skip -- the user can run the command manually
		return fmt.Errorf("fix requires confirmation (run with --yes or execute manually: %s)", fixCmd)
	}

	slog.Info("running adapter fix", "command", fixCmd)
	parts := strings.Fields(fixCmd)
	if len(parts) == 0 {
		return fmt.Errorf("empty fix command")
	}
	cmd := exec.Command(parts[0], parts[1:]...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

// verifyAdapterFix re-runs diagnose on the adapter and checks whether the
// issue (identified by its original slug, without adapter prefix) is gone.
func verifyAdapterFix(ea *adapters.ExternalAdapter, repoRoot, issueSlug string) bool {
	result, err := ea.Diagnose(repoRoot, "project", version.Version)
	if err != nil {
		return false
	}
	for _, issue := range result.Issues {
		if issue.Slug == issueSlug {
			return false // issue still present
		}
	}
	return true
}
