package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/manifest"
	"github.com/sageox/ox/internal/tokens"
)

// checkTeamContextHealth runs all team context related checks.
// Returns checks only if team contexts are configured.
func checkTeamContextHealth(opts doctorOptions) []checkResult {
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return nil
	}

	localCfg, err := config.LoadLocalConfig(gitRoot)
	if err != nil || len(localCfg.TeamContexts) == 0 {
		// no team contexts configured, check for legacy directories
		legacyCheck := checkLegacyTeamContexts(gitRoot)
		if legacyCheck.warning || !legacyCheck.passed {
			return []checkResult{legacyCheck}
		}
		return nil
	}

	var checks []checkResult

	// check each configured team context
	for _, tc := range localCfg.TeamContexts {
		check := checkSingleTeamContext(tc, opts)
		checks = append(checks, check)

		// check AGENTS.md for each team context (separate check)
		agentsMDCheck := checkTeamAgentsMD(tc)
		if agentsMDCheck.warning || !agentsMDCheck.passed {
			checks = append(checks, agentsMDCheck)
		}
	}

	// check for legacy team contexts that should be migrated
	legacyCheck := checkLegacyTeamContexts(gitRoot)
	if legacyCheck.warning {
		checks = append(checks, legacyCheck)
	}

	// check for orphaned team directories in centralized location
	orphanCheck := checkOrphanedTeamDirs(opts)
	if orphanCheck.warning || !orphanCheck.passed {
		checks = append(checks, orphanCheck)
	}

	return checks
}

// checkSingleTeamContext validates a single team context.
func checkSingleTeamContext(tc config.TeamContext, opts doctorOptions) checkResult {
	health := config.AnalyzeTeamContextHealth(tc.TeamID, tc.Path, tc.LastSync)

	name := fmt.Sprintf("Team: %s", tc.TeamName)
	if tc.TeamName == "" {
		name = fmt.Sprintf("Team: %s", tc.TeamID)
	}

	// check if path exists
	if !health.Exists {
		return FailedCheck(name, "directory missing", fmt.Sprintf("Path: %s", tc.Path))
	}

	// check if it's a git repo
	if !health.IsGitRepo {
		return FailedCheck(name, "not a git repository", fmt.Sprintf("Path: %s", tc.Path))
	}

	// check for orphaned backpointers
	if health.OrphanedCount > 0 {
		if opts.fix {
			cleaned, err := config.CleanupOrphanedBackpointers(tc.Path)
			if err == nil && cleaned > 0 {
				return PassedCheck(name, fmt.Sprintf("cleaned %d orphaned refs", cleaned))
			}
		}
		detail := fmt.Sprintf("%d workspace(s) reference deleted projects", health.OrphanedCount)
		if !opts.fix {
			detail += ". Run `ox doctor --fix` to clean up"
		}
		return WarningCheck(name, "orphaned references", detail)
	}

	// check for stale team context
	if health.IsStale {
		msg := fmt.Sprintf("no activity in %d days", int(health.LastSyncAge.Hours()/24))
		return WarningCheck(name, msg, "Consider removing if no longer needed")
	}

	// build status message
	var statusParts []string
	if health.ActiveCount > 0 {
		statusParts = append(statusParts, fmt.Sprintf("%d active", health.ActiveCount))
	}
	if len(health.Workspaces) > 0 {
		statusParts = append(statusParts, fmt.Sprintf("%d total workspaces", len(health.Workspaces)))
	}
	if !tc.LastSync.IsZero() {
		age := time.Since(tc.LastSync)
		if age < time.Hour {
			statusParts = append(statusParts, "synced recently")
		} else if age < 24*time.Hour {
			statusParts = append(statusParts, fmt.Sprintf("synced %dh ago", int(age.Hours())))
		} else {
			statusParts = append(statusParts, fmt.Sprintf("synced %dd ago", int(age.Hours()/24)))
		}
	}

	status := "ok"
	if len(statusParts) > 0 {
		status = strings.Join(statusParts, ", ")
	}

	return PassedCheck(name, status)
}

// checkLegacyTeamContexts looks for team contexts in the old sibling directory format.
func checkLegacyTeamContexts(projectRoot string) checkResult {
	legacyDirs, err := config.DiscoverLegacyTeamContexts(projectRoot)
	if err != nil || len(legacyDirs) == 0 {
		return SkippedCheck("Legacy team contexts", "none found", "")
	}

	// found legacy directories
	var names []string
	for _, dir := range legacyDirs {
		names = append(names, filepath.Base(dir))
	}

	detail := fmt.Sprintf("Found: %s", strings.Join(names, ", "))
	detail += ". Run `ox doctor --fix` to migrate to ~/.sageox/data/teams/"

	return WarningCheck("Legacy team contexts", fmt.Sprintf("%d found", len(legacyDirs)), detail)
}

// checkOrphanedTeamDirs looks for team directories with no valid workspace references.
func checkOrphanedTeamDirs(opts doctorOptions) checkResult {
	teamDirs, err := config.DiscoverTeamContexts()
	if err != nil || len(teamDirs) == 0 {
		return SkippedCheck("Orphaned team dirs", "none found", "")
	}

	var orphanedPaths []string
	var orphanedNames []string
	for _, dir := range teamDirs {
		health := config.AnalyzeTeamContextHealth(filepath.Base(dir), dir, time.Time{})
		if health.IsOrphaned {
			orphanedPaths = append(orphanedPaths, dir)
			orphanedNames = append(orphanedNames, filepath.Base(dir))
		}
	}

	if len(orphanedPaths) == 0 {
		return SkippedCheck("Orphaned team dirs", "none", "")
	}

	detail := fmt.Sprintf("Teams with no active workspaces: %s", strings.Join(orphanedNames, ", "))

	if opts.fix {
		removed, err := promptTeamContextCleanup(orphanedPaths, opts.forceYes)
		if err != nil {
			return FailedCheck("Orphaned team dirs", "cleanup error", err.Error())
		}
		if len(removed) > 0 {
			return PassedCheck("Orphaned team dirs", fmt.Sprintf("removed %d: %s", len(removed), strings.Join(removed, ", ")))
		}
		return WarningCheck("Orphaned team dirs", fmt.Sprintf("%d found, skipped", len(orphanedPaths)), detail)
	}

	return WarningCheck("Orphaned team dirs", fmt.Sprintf("%d found", len(orphanedPaths)),
		detail+". Run `ox doctor --fix` to review")
}

// promptTeamContextCleanup interactively prompts for orphaned team context removal.
// Only called when --fix is used and user confirmation is needed.
func promptTeamContextCleanup(teamDirs []string, forceYes bool) ([]string, error) {
	if len(teamDirs) == 0 {
		return nil, nil
	}

	var removed []string

	for _, dir := range teamDirs {
		teamID := filepath.Base(dir)

		// load health info
		health := config.AnalyzeTeamContextHealth(teamID, dir, time.Time{})
		if !health.IsOrphaned {
			continue
		}

		// show details
		fmt.Printf("\nOrphaned team context: %s\n", teamID)
		fmt.Printf("  Path: %s\n", dir)
		fmt.Printf("  Workspaces: %d (all reference deleted projects)\n", len(health.Workspaces))

		if forceYes {
			// auto-confirm with -y flag
			if err := os.RemoveAll(dir); err != nil {
				return removed, fmt.Errorf("remove %s: %w", dir, err)
			}
			removed = append(removed, teamID)
			fmt.Printf("  Removed.\n")
			continue
		}

		// prompt user
		if cli.ConfirmYesNo("  Remove this team context?", false) {
			if err := os.RemoveAll(dir); err != nil {
				return removed, fmt.Errorf("remove %s: %w", dir, err)
			}
			removed = append(removed, teamID)
			fmt.Printf("  Removed.\n")
		} else {
			fmt.Printf("  Skipped.\n")
		}
	}

	return removed, nil
}

// checkTeamAgentsMD validates that a team context has an AGENTS.md file and it's not too large.
// This is critical for establishing team norms that guide AI agent planning.
func checkTeamAgentsMD(tc config.TeamContext) checkResult {
	// skip if team context doesn't exist
	if _, err := os.Stat(tc.Path); os.IsNotExist(err) {
		return SkippedCheck("Team AGENTS.md", "team context missing", "")
	}

	teamName := tc.TeamName
	if teamName == "" {
		teamName = tc.TeamID
	}
	name := fmt.Sprintf("Team %s: AGENTS.md", teamName)

	// check for AGENTS.md (critical for team context)
	agentsMDPath := filepath.Join(tc.Path, "coworkers", "ai", "claude", "AGENTS.md")
	agentsMDContent, err := os.ReadFile(agentsMDPath)
	if os.IsNotExist(err) {
		// AGENTS.md is critical - this is a warning, not a failure
		return WarningCheck(name, "missing",
			"AGENTS.md tells AI coworkers your team's conventions (coding style, review\n"+
				"        process, preferred tools). Without it, AI coworkers start each session\n"+
				"        with no team context. Visit https://sageox.ai to add team norms, or\n"+
				"        create the file manually at: "+agentsMDPath)
	} else if err != nil {
		return WarningCheck(name, "unreadable", fmt.Sprintf("Cannot read: %v", err))
	}

	// check token count (5000 tokens max recommended)
	const maxTokensAllowed = 5000
	tokenCount := tokens.EstimateTokens(string(agentsMDContent))

	if tokenCount > maxTokensAllowed {
		return WarningCheck(name, fmt.Sprintf("too large (~%d tokens)", tokenCount),
			fmt.Sprintf("AGENTS.md is ~%d tokens. Max recommended: %d tokens.\n", tokenCount, maxTokensAllowed)+
				"        Large files pollute agent context and reduce both planning and implementation effectiveness.\n"+
				"        Move detailed specs to separate files and reference them from AGENTS.md.")
	}

	return PassedCheck(name, fmt.Sprintf("ok (~%d tokens)", tokenCount))
}

// checkGCBlockedByUntracked detects team contexts where untracked or modified files
// are blocking blue-green GC reclone. The daemon's isCheckoutClean() uses
// `git status --porcelain` and skips GC if there's any output.
//
// POLICY: We currently require user confirmation before removing any
// untracked files from team context checkouts that aren't covered by
// .sageox/.gitignore. In the future, we may define "safe zones"
// (e.g., docs/) where users are expected to make local edits, and
// treat everything outside those zones as eligible for automatic
// cleanup during GC. Until that policy is decided, we surface the
// blocking files and let the user choose.
//
// .observations/ must ALWAYS block GC — it may contain un-pushed session data.
func checkGCBlockedByUntracked(fix bool) checkResult {
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return SkippedCheck("GC blocked", "not in git repo", "")
	}

	localCfg, err := config.LoadLocalConfig(gitRoot)
	if err != nil || len(localCfg.TeamContexts) == 0 {
		return SkippedCheck("GC blocked", "no team contexts", "")
	}

	var dirtyTeams []string
	var allFiles []string
	var hasSageoxFiles, hasObservationFiles, hasOtherFiles bool

	for _, tc := range localCfg.TeamContexts {
		if tc.Path == "" || !isGitRepo(tc.Path) {
			continue
		}

		output, gitErr := runGitStatus(tc.Path)
		if gitErr != nil {
			teamName := tc.TeamName
			if teamName == "" {
				teamName = tc.TeamID
			}
			return WarningCheck("GC blocked", "status check failed",
				fmt.Sprintf("failed to inspect team context %s: %v", teamName, gitErr))
		}
		if output == "" {
			continue
		}

		teamName := tc.TeamName
		if teamName == "" {
			teamName = tc.TeamID
		}
		dirtyTeams = append(dirtyTeams, teamName)

		for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
			if len(line) < 4 {
				continue
			}
			// porcelain format: XY filename (3rd char is space)
			file := strings.TrimSpace(line[3:])
			allFiles = append(allFiles, file)

			switch {
			case strings.HasPrefix(file, ".sageox/"):
				hasSageoxFiles = true
			case strings.HasPrefix(file, ".observations/"):
				hasObservationFiles = true
			default:
				hasOtherFiles = true
			}
		}
	}

	if len(dirtyTeams) == 0 {
		return PassedCheck("GC blocked", "all team contexts clean")
	}

	// build detail message showing the blocking files and what to do
	var detail strings.Builder
	detail.WriteString(fmt.Sprintf("Teams with dirty checkouts: %s\n", strings.Join(dirtyTeams, ", ")))
	detail.WriteString("        Blocking files:\n")
	for _, f := range allFiles {
		detail.WriteString(fmt.Sprintf("          %s\n", f))
	}

	if hasSageoxFiles && !hasOtherFiles && !hasObservationFiles {
		// only .sageox/ files blocking — auto-fixable via gitignore
		if fix {
			// the gitignore-missing check handles this; just note it
			detail.WriteString("        Fix: run `ox doctor --fix gitignore-missing` to update .sageox/.gitignore")
		} else {
			detail.WriteString("        Fix: run `ox doctor --fix` to update .sageox/.gitignore")
		}
		return WarningCheck("GC blocked", fmt.Sprintf("%d team(s) blocked by .sageox/ files", len(dirtyTeams)),
			detail.String())
	}

	if hasObservationFiles {
		detail.WriteString("        .observations/ files may contain un-pushed session data — do not delete\n")
	}
	if hasOtherFiles {
		detail.WriteString("        Other files need manual review: commit, stash, or remove them")
	}

	return WarningCheck("GC blocked",
		fmt.Sprintf("%d team(s) with uncommitted changes blocking GC", len(dirtyTeams)),
		detail.String())
}

// checkTeamSparseCheckout verifies that every team context repo with
// sparse-checkout enabled includes the root-level file patterns (/* and !/*/).
// Without these, git blocks staging root-level files like .gitattributes.
func checkTeamSparseCheckout(fix bool) checkResult {
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return SkippedCheck("Team sparse checkout", "not in git repo", "")
	}

	localCfg, err := config.LoadLocalConfig(gitRoot)
	if err != nil || len(localCfg.TeamContexts) == 0 {
		return SkippedCheck("Team sparse checkout", "no team contexts", "")
	}

	var needsFix, fixed, checked int

	for _, tc := range localCfg.TeamContexts {
		if tc.Path == "" || !isGitRepo(tc.Path) {
			continue
		}

		// check if sparse-checkout is enabled
		sparseFile := filepath.Join(tc.Path, ".git", "info", "sparse-checkout")
		content, err := os.ReadFile(sparseFile)
		if err != nil {
			// no sparse-checkout file = not using sparse checkout, skip
			continue
		}

		checked++

		// check for root-level file patterns: /* and !/*/
		// git may write the negation as \!/*/ (escaped) in the file
		lines := strings.Split(string(content), "\n")
		hasRootGlob := false
		hasNegateRootDirs := false
		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			if trimmed == "/*" {
				hasRootGlob = true
			}
			if trimmed == "!/*/" || trimmed == "\\!/*/" {
				hasNegateRootDirs = true
			}
		}

		if hasRootGlob && hasNegateRootDirs {
			continue
		}

		needsFix++

		if fix {
			if repairErr := repairTeamSparseCheckout(tc.Path); repairErr != nil {
				slog.Warn("failed to repair sparse-checkout",
					"path", tc.Path, "error", repairErr)
				continue
			}
			fixed++
		}
	}

	if checked == 0 {
		return SkippedCheck("Team sparse checkout", "no sparse-checkout repos", "")
	}

	if needsFix == 0 {
		return PassedCheck("Team sparse checkout",
			fmt.Sprintf("%d repo(s) have root-level patterns", checked))
	}

	if fix && fixed == needsFix {
		return PassedCheck("Team sparse checkout",
			fmt.Sprintf("repaired %d repo(s)", fixed))
	}

	unfixed := needsFix - fixed
	return FailedCheck("Team sparse checkout",
		fmt.Sprintf("%d repo(s) missing root-level patterns (/* and !/*/)", unfixed),
		"Root-level files like .gitattributes cannot be staged without these patterns.\n"+
			"        Run `ox doctor` to auto-fix (FixLevelAuto)")
}

// repairTeamSparseCheckout re-applies sparse-checkout from the manifest
// with root-level file patterns included.
func repairTeamSparseCheckout(tcPath string) error {
	manifestPath := filepath.Join(tcPath, ".sageox", "sync.manifest")
	cfg := manifest.ParseFile(manifestPath)

	sparsePaths := manifest.ComputeSparseSet(cfg)
	if len(sparsePaths) == 0 {
		return fmt.Errorf("no sparse paths computed from manifest")
	}

	// ensure .sageox/ is always in the sparse set
	hasSageox := false
	for _, p := range sparsePaths {
		if p == ".sageox/" || p == ".sageox" {
			hasSageox = true
			break
		}
	}
	if !hasSageox {
		sparsePaths = append([]string{".sageox/"}, sparsePaths...)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	args := append([]string{"-C", tcPath, "sparse-checkout", "set", "--no-cone"}, sparsePaths...)
	cmd := exec.CommandContext(ctx, "git", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git sparse-checkout set: %w: %s", err, strings.TrimSpace(string(output)))
	}

	return nil
}

// runGitStatus runs git status --porcelain on a directory and returns the output.
func runGitStatus(dir string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "status", "--porcelain")
	output, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("git status timed out for %s", dir)
	}
	if err != nil {
		return "", fmt.Errorf("git status failed for %s: %s", dir, strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output)), nil
}
