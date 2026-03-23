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

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/daemon"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/gitserver"
	"github.com/sageox/ox/internal/ledger"
	"github.com/sageox/ox/internal/paths"
)

// fixSiblingLedgerStructure migrates a ledger from the sibling directory to the user directory.
// If the new path doesn't exist: moves the directory. If it does and old has no local changes: removes old.
func fixSiblingLedgerStructure(gitRoot string, localCfg *config.LocalConfig, issue repoPathIssue) bool {
	projectCfg, err := config.LoadProjectConfig(gitRoot)
	if err != nil || projectCfg == nil || projectCfg.RepoID == "" {
		fmt.Println("  Cannot migrate: project config missing repo_id. Run 'ox init' first.")
		fmt.Println()
		return false
	}

	ep := endpoint.GetForProject(gitRoot)
	newPath := config.DefaultLedgerPath(projectCfg.RepoID, ep)
	if newPath == "" {
		fmt.Println("  Cannot determine new ledger path.")
		fmt.Println()
		return false
	}

	fmt.Printf("  Migrating ledger to user directory:\n")
	fmt.Printf("    From: %s\n", issue.path)
	fmt.Printf("    To:   %s\n", newPath)
	fmt.Println()

	if isGitRepo(newPath) {
		// new path already exists — check if old has local changes
		if hasLocalGitChanges(issue.path) {
			fmt.Println("  Old ledger has uncommitted local changes. Keeping both copies.")
			fmt.Println("  Manually review and remove the old location when ready.")
			fmt.Println()
			return false
		}

		// no local changes, safe to remove old
		fmt.Println("  New location already exists. Removing old copy (no local changes).")
		if err := os.RemoveAll(issue.path); err != nil {
			fmt.Printf("  Failed to remove old ledger: %v\n", err)
			return false
		}
	} else {
		// new path doesn't exist — move
		if err := os.MkdirAll(filepath.Dir(newPath), 0755); err != nil {
			fmt.Printf("  Failed to create parent directory: %v\n", err)
			return false
		}
		if err := os.Rename(issue.path, newPath); err != nil {
			fmt.Printf("  Failed to move ledger: %v\n", err)
			fmt.Println("  (This can happen if source and target are on different filesystems.)")
			fmt.Println("  The ledger will be re-cloned at the new location on next sync.")
			return false
		}
	}

	// update config
	localCfg.Ledger = &config.LedgerConfig{
		Path: newPath,
	}

	// create symlinks (warn but don't fail migration)
	if err := config.CreateProjectLedgerSymlink(gitRoot, projectCfg.RepoID, ep); err != nil {
		fmt.Printf("  Warning: could not create ledger symlink: %v\n", err)
	}
	if projectCfg.TeamID != "" {
		if err := config.CreateProjectTeamSymlinks(gitRoot, projectCfg.TeamID, ep); err != nil {
			fmt.Printf("  Warning: could not create team symlinks: %v\n", err)
		}
	}

	fmt.Println("  Migrated successfully.")
	fmt.Println()
	return true
}

// hasLocalGitChanges returns true if the git repo at path has uncommitted changes.
func hasLocalGitChanges(repoPath string) bool {
	cmd := exec.Command("git", "-C", repoPath, "status", "--porcelain")
	output, err := cmd.Output()
	if err != nil {
		return true // assume changes on error (be conservative)
	}
	return strings.TrimSpace(string(output)) != ""
}

// fixLegacyStructure offers to migrate a ledger from the old _sageox_ledger suffix to the current structure.
func fixLegacyStructure(gitRoot string, localCfg *config.LocalConfig, issue repoPathIssue) bool {
	fmt.Println("  The ledger is using the old sibling directory structure (_sageox_ledger suffix).")
	fmt.Println("  The current structure uses endpoint-namespaced sibling directories:")
	fmt.Printf("    <project_parent>/<repo_name>_sageox/<endpoint>/ledger\n")
	fmt.Println()

	// for now, just suggest manual migration since we need repo_id from cloud
	fmt.Println("  To migrate:")
	fmt.Println("    1. Run `ox status` to get your repo_id")
	fmt.Println("    2. Move the ledger to the new location")
	fmt.Println("    3. Update .sageox/config.local.toml with the new path")
	fmt.Println()
	fmt.Println("  Alternatively, the ledger will be cloned to the new location")
	fmt.Println("  on next `ox doctor --fix` if the old one is removed.")
	fmt.Println()

	if cli.ConfirmYesNo("Continue using the current location for now?", true) {
		// update config to explicitly use the current path
		localCfg.Ledger = &config.LedgerConfig{
			Path: issue.path,
		}
		fmt.Println("  Keeping current location.")
		fmt.Println()
		return true
	}

	fmt.Println("  Skipped. Run migration manually when ready.")
	fmt.Println()
	return false
}

// fixBrokenSymlink offers to recreate a broken symlink for team contexts.
func fixBrokenSymlink(localCfg *config.LocalConfig, issue repoPathIssue) bool {
	fmt.Println("  The symlink for this team context is broken.")
	fmt.Println()

	if !cli.ConfirmYesNo("Remove broken symlink and re-clone from cloud?", true) {
		fmt.Println("  Skipped.")
		fmt.Println()
		return false
	}

	// remove the broken symlink
	if err := os.Remove(issue.path); err != nil {
		fmt.Printf("  Failed to remove symlink: %v\n", err)
		return false
	}

	// clone fresh from cloud
	if err := cloneRepoForFix(repoPathIssue{
		repoType: "team-context",
		path:     issue.path,
		teamID:   issue.teamID,
		teamName: issue.teamName,
		endpoint: issue.endpoint,
		issue:    "missing",
	}); err != nil {
		fmt.Printf("  Clone failed: %v\n", err)
		return false
	}

	// update config
	localCfg.SetTeamContext(issue.teamID, issue.teamName, issue.path)
	fmt.Println("  Symlink recreated successfully.")
	fmt.Println()
	return true
}

// fixRepoPathIssues prompts user to fix repo path issues.
// Options vary by issue type:
//   - missing/empty-dir/not-git-repo: Clone from cloud, enter existing path, or skip
//   - legacy-structure: Offer to migrate to new centralized structure
//   - broken-symlink: Offer to recreate symlink
//   - other: Enter new path or skip
//
// Returns appropriate check result.
func fixRepoPathIssues(gitRoot string, localCfg *config.LocalConfig, issues []repoPathIssue) checkResult {
	fmt.Println()
	cli.PrintWarning("Git repository issue(s) detected")
	fmt.Println()

	// get the endpoint for this project (checks project config, env var, then default)
	projectEndpoint := endpoint.GetForProject(gitRoot)

	// check if authenticated for this endpoint - can't clone without auth
	authenticated, _ := auth.IsAuthenticatedForEndpoint(projectEndpoint)
	if !authenticated {
		fmt.Println("  You are not logged in. Run 'ox login' first to clone repos from cloud.")
		fmt.Println()
		return WarningCheck("git repo paths",
			fmt.Sprintf("%d repo(s) with issues", len(issues)),
			"Run `ox login` first, then `ox doctor --fix` to clone repos")
	}

	// refresh git credentials before attempting fixes using project endpoint
	if token, err := auth.GetTokenForEndpoint(projectEndpoint); err == nil && token != nil {
		client := api.NewRepoClientWithEndpoint(projectEndpoint).WithAuthToken(token.AccessToken)
		if err := fetchAndSaveGitCredentials(client); err != nil {
			slog.Warn("failed to refresh git credentials", "error", err)
		}
	}

	var fixed, skipped int

	for _, issue := range issues {
		var repoLabel string
		switch issue.repoType {
		case "ledger":
			repoLabel = "Ledger repo"
		case "team-context-symlink":
			if issue.teamName != "" {
				repoLabel = fmt.Sprintf("Team context symlink: %s", issue.teamName)
			} else {
				repoLabel = fmt.Sprintf("Team context symlink: %s", issue.teamID)
			}
		default:
			if issue.teamName != "" {
				repoLabel = fmt.Sprintf("Team context: %s", issue.teamName)
			} else {
				repoLabel = fmt.Sprintf("Team context: %s", issue.teamID)
			}
		}

		// describe the issue
		var issueDesc string
		switch issue.issue {
		case "missing":
			issueDesc = "not found"
		case "not-git-repo":
			issueDesc = "exists but is not a git repository"
		case "empty-dir":
			issueDesc = "is an empty directory"
		case "sibling-structure":
			issueDesc = "using deprecated sibling directory (migrating to user directory)"
		case "legacy-structure":
			issueDesc = "using old sibling directory structure"
		case "broken-symlink":
			issueDesc = "symlink target does not exist"
		case "invalid-symlink":
			issueDesc = "is not a valid symlink"
		default:
			issueDesc = issue.issue
		}

		fmt.Printf("  %s %s at:\n", repoLabel, issueDesc)
		fmt.Printf("    %s\n", issue.path)
		fmt.Println()

		// handle different issue types
		switch issue.issue {
		case "sibling-structure":
			if fixSiblingLedgerStructure(gitRoot, localCfg, issue) {
				fixed++
			} else {
				skipped++
			}
		case "legacy-structure":
			if fixLegacyStructure(gitRoot, localCfg, issue) {
				fixed++
			} else {
				skipped++
			}
		case "broken-symlink", "invalid-symlink":
			if fixBrokenSymlink(localCfg, issue) {
				fixed++
			} else {
				skipped++
			}
		case "missing", "empty-dir", "not-git-repo":
			// for directories with potential data, ask before cloning
			if issue.issue == "not-git-repo" {
				if !cli.ConfirmYesNo("Clone from cloud?", true) {
					skipped++
					fmt.Println("  Skipped.")
					fmt.Println()
					continue
				}
			}

			// attempt clone
			if err := cloneRepoForFix(issue); err != nil {
				fmt.Printf("  Clone failed: %v\n", err)
				skipped++
				continue
			}

			// update config with the path
			switch issue.repoType {
			case "ledger":
				localCfg.Ledger = &config.LedgerConfig{
					Path: issue.path,
				}
			case "team-context":
				localCfg.SetTeamContext(issue.teamID, issue.teamName, issue.path)
			}
			fixed++
			fmt.Println("  Cloned successfully.")
			fmt.Println()
		default:
			fmt.Println("  Cannot auto-fix this issue. Skipping.")
			skipped++
		}
	}

	// save config if any fixes were made
	if fixed > 0 {
		if err := config.SaveLocalConfig(gitRoot, localCfg); err != nil {
			return FailedCheck("git repo paths", "save failed", err.Error())
		}
	}

	// determine result
	if fixed == len(issues) {
		return PassedCheck("git repo paths", fmt.Sprintf("fixed %d repo(s)", fixed))
	}
	if fixed > 0 {
		return WarningCheck("git repo paths",
			fmt.Sprintf("fixed %d, skipped %d", fixed, skipped),
			"Run `ox doctor --fix` again to fix remaining issues")
	}
	return WarningCheck("git repo paths",
		fmt.Sprintf("%d repo(s) with issues", len(issues)),
		"Issues unchanged. Run `ox doctor --fix` to try again")
}

// fixMissingRepos fetches repos from cloud and clones them when no repos are configured.
// This is called when authenticated + .sageox exists but no ledger/team contexts are set up.
func fixMissingRepos(gitRoot string, localCfg *config.LocalConfig) checkResult {
	fmt.Println()

	// get the endpoint for this project (checks project config, env var, then default)
	projectEndpoint := endpoint.GetForProject(gitRoot)

	// get auth token for this endpoint
	token, err := auth.GetTokenForEndpoint(projectEndpoint)
	if err != nil {
		return FailedCheck("git repo paths", "auth error", err.Error())
	}
	if token == nil || token.AccessToken == "" {
		return FailedCheck("git repo paths", "not authenticated",
			"Run `ox login` first")
	}

	client := api.NewRepoClientWithEndpoint(projectEndpoint).WithAuthToken(token.AccessToken)

	// fetch team context repos (user-scoped) and save git credentials
	repos, err := cli.WithSpinner("Fetching repos from cloud...", func() (*api.ReposResponse, error) {
		return client.GetRepos()
	})
	if err != nil {
		return FailedCheck("git repo paths", "API error", err.Error())
	}
	if repos != nil {
		if err := saveGitCredentialsFromRepos(repos, projectEndpoint); err != nil {
			slog.Warn("failed to save git credentials", "error", err)
		}
	}

	// fetch repo detail (project-scoped) for ledger URL and team contexts
	// GetRepos() only returns team-context repos; ledger comes from GetRepoDetail()
	projectCfg, _ := config.LoadProjectConfig(gitRoot)
	var repoDetail *api.RepoDetailResponse
	if projectCfg != nil && projectCfg.RepoID != "" {
		repoDetail, err = client.GetRepoDetail(projectCfg.RepoID)
		if err != nil {
			slog.Warn("failed to fetch repo detail", "error", err)
		}
	}

	var fixed, skipped, total int

	// clone ledger from repo detail API (GetRepos doesn't return ledgers)
	if repoDetail != nil && repoDetail.Ledger != nil && repoDetail.Ledger.Status == "ready" && repoDetail.Ledger.RepoURL != "" {
		total++

		var ledgerPath string
		ledgerPath, err = ledger.DefaultPath()
		if err != nil || ledgerPath == "" {
			// fallback: derive directly from project config
			if projectCfg, cfgErr := config.LoadProjectConfig(gitRoot); cfgErr == nil && projectCfg.RepoID != "" {
				ep := endpoint.GetForProject(gitRoot)
				ledgerPath = config.DefaultLedgerPath(projectCfg.RepoID, ep)
			}
		}

		fmt.Printf("  Ledger: %s\n", repoDetail.Ledger.RepoURL)
		fmt.Printf("    Cloning to: %s\n", ledgerPath)

		if err := os.MkdirAll(filepath.Dir(ledgerPath), 0755); err != nil {
			fmt.Printf("    Error creating directory: %v\n", err)
			skipped++
		} else if err := cloneViaDaemon(repoDetail.Ledger.RepoURL, ledgerPath, "ledger", projectEndpoint); err != nil {
			fmt.Printf("    Clone failed: %v\n", err)
			skipped++
		} else {
			localCfg.Ledger = &config.LedgerConfig{
				Path: ledgerPath,
			}
			fixed++
		}
		fmt.Println()
	}

	// build team context list from repo detail (preferred, has team_id) or GetRepos fallback
	type teamContextInfo struct {
		teamID   string
		teamName string
		cloneURL string
	}
	var teamContexts []teamContextInfo

	if repoDetail != nil {
		for _, tc := range repoDetail.TeamContexts {
			if tc.StableID() != "" && tc.RepoURL != "" {
				teamContexts = append(teamContexts, teamContextInfo{
					teamID:   tc.StableID(),
					teamName: tc.Name,
					cloneURL: tc.RepoURL,
				})
			}
		}
	} else if repos != nil {
		// fallback: use GetRepos response (team_id field is directly available)
		for _, repo := range repos.Repos {
			if repo.Type == "team-context" && repo.TeamID != "" {
				teamContexts = append(teamContexts, teamContextInfo{
					teamID:   repo.TeamID,
					teamName: repo.Name,
					cloneURL: repo.URL,
				})
			}
		}
	}

	// clone team contexts
	for _, tc := range teamContexts {
		total++

		tcPath := paths.TeamContextDir(tc.teamID, projectEndpoint)

		displayName := tc.teamName
		if displayName == "" {
			displayName = tc.teamID
		}

		// skip if already a valid git repo (team contexts are shared across projects)
		if info, statErr := os.Stat(filepath.Join(tcPath, ".git")); statErr == nil && info.IsDir() {
			localCfg.SetTeamContext(tc.teamID, displayName, tcPath)
			fixed++
			continue
		}

		fmt.Printf("  Team Context (%s): %s\n", displayName, tc.cloneURL)
		fmt.Printf("    Cloning to: %s\n", tcPath)

		if err := os.MkdirAll(filepath.Dir(tcPath), 0755); err != nil {
			fmt.Printf("    Error creating directory: %v\n", err)
			skipped++
			continue
		}

		if err := cloneViaDaemon(tc.cloneURL, tcPath, "team-context", projectEndpoint); err != nil {
			fmt.Printf("    Clone failed: %v\n", err)
			skipped++
			continue
		}

		localCfg.SetTeamContext(tc.teamID, displayName, tcPath)
		fixed++
		fmt.Println()
	}

	// save config if any fixes were made
	if fixed > 0 {
		if err := config.SaveLocalConfig(gitRoot, localCfg); err != nil {
			return FailedCheck("git repo paths", "save failed", err.Error())
		}
	}

	// determine result
	if total == 0 {
		return WarningCheck("git repo paths", "no repos from cloud",
			"Cloud has not provisioned any repos yet")
	}
	if fixed == total {
		return PassedCheck("git repo paths", fmt.Sprintf("cloned %d repo(s)", fixed))
	}
	if fixed > 0 {
		return WarningCheck("git repo paths",
			fmt.Sprintf("cloned %d, failed %d", fixed, skipped),
			"Run `ox doctor --fix` again to retry failed repos")
	}
	return FailedCheck("git repo paths",
		fmt.Sprintf("failed to clone %d repo(s)", skipped),
		"Run `ox doctor --fix` to try again")
}

// extractTeamIDFromRepoName extracts team ID from repo name.
// Common formats: "team-xxx-context", "xxx-team-context", "team_xxx"
func extractTeamIDFromRepoName(name string) string {
	// try to find "team_" or "team-" pattern
	name = strings.ToLower(name)

	// pattern: team_xxx or team-xxx
	if strings.HasPrefix(name, "team_") {
		parts := strings.SplitN(name[5:], "-", 2)
		if len(parts) > 0 && parts[0] != "" {
			return "team_" + parts[0]
		}
	}
	if strings.HasPrefix(name, "team-") {
		parts := strings.SplitN(name[5:], "-", 2)
		if len(parts) > 0 && parts[0] != "" {
			return "team_" + parts[0]
		}
	}

	// pattern: xxx-team-context (extract xxx as team ID)
	if strings.HasSuffix(name, "-team-context") {
		teamPart := strings.TrimSuffix(name, "-team-context")
		if teamPart != "" {
			return "team_" + teamPart
		}
	}

	return ""
}

// cloneViaDaemon clones a repo using the daemon's checkout capability.
// Clone operations go through daemon for centralized credential handling.
//
// ┌─────────────────────────────────────────────────────────────────────────────┐
// │ CRITICAL PATH EXCEPTION: This function has a direct clone fallback.        │
// │                                                                             │
// │ Per IPC architecture philosophy (docs/ai/specs/ipc-architecture.md):       │
// │ - IPC should NEVER be required for daemon to function                       │
// │ - Most operations should gracefully degrade when daemon unavailable         │
// │                                                                             │
// │ HOWEVER, clone is a CRITICAL PATH for product functionality:               │
// │ - Without clone, users cannot initialize their environment                  │
// │ - Without ledger/team-context repos, SageOx cannot function at all         │
// │ - Blocking users here creates a broken first-run experience                 │
// │                                                                             │
// │ Therefore, this function FALLS BACK to direct git clone when daemon is     │
// │ unavailable. This is an INTENTIONAL EXCEPTION to the normal pattern.       │
// └─────────────────────────────────────────────────────────────────────────────┘
func cloneViaDaemon(cloneURL, targetPath, repoType, endpointURL string) error {
	// Try daemon first (preferred path - centralized credential handling)
	if daemon.IsRunning() {
		client := daemon.NewClientWithTimeout(60 * time.Second)
		payload := daemon.CheckoutPayload{
			RepoPath: targetPath,
			CloneURL: cloneURL,
			RepoType: repoType,
		}

		result, err := client.Checkout(payload, func(stage string, percent *int, message string) {
			// progress updates - could be shown to user if needed
			if message != "" {
				fmt.Printf("    %s\n", message)
			}
		})
		if err != nil {
			return err
		}

		if result.AlreadyExists {
			fmt.Println("    Repository already exists.")
		} else if result.Cloned {
			fmt.Println("    Cloned successfully.")
		}

		return nil
	}

	// ─────────────────────────────────────────────────────────────────────────
	// FALLBACK: Direct clone when daemon unavailable
	// ─────────────────────────────────────────────────────────────────────────
	// This is a CRITICAL PATH EXCEPTION. Clone is required for product to
	// function at all. See function-level comment for rationale.
	// ─────────────────────────────────────────────────────────────────────────
	fmt.Println("    Note: Daemon not running, using direct clone")

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if repoType == "team-context" {
		// team contexts use shared two-phase partial clone (same code as daemon)
		creds, err := gitserver.LoadCredentialsForEndpoint(endpointURL)
		if err != nil {
			return fmt.Errorf("load credentials: %w", err)
		}
		if creds == nil {
			return fmt.Errorf("direct clone failed: %w", gitserver.ErrNoCredentials)
		}
		authURL, err := gitserver.BuildAuthURL(cloneURL, creds)
		if err != nil {
			return fmt.Errorf("build auth URL: %w", err)
		}
		result, err := gitserver.TwoPhaseClone(ctx, authURL, targetPath)
		if err != nil {
			return fmt.Errorf("direct clone failed: %w", err)
		}
		gitserver.ValidateTeamContextClone(targetPath, result.ManifestConfig)
	} else {
		// ledger: full clone
		if err := gitserver.CloneFromURLWithEndpoint(ctx, cloneURL, targetPath, endpointURL, nil); err != nil {
			return fmt.Errorf("direct clone failed: %w", err)
		}
	}

	fmt.Println("    Cloned successfully (direct).")
	return nil
}

// cloneRepoForFix clones a repo to fix a path issue.
// If directory exists but is not a git repo, asks for confirmation before removing.
// IMPORTANT: Checks cloud has repo URL BEFORE deleting anything to avoid data loss.
// NOTE: Clone operations go through daemon; CLI handles add/commit/push for session uploads.
func cloneRepoForFix(issue repoPathIssue) error {
	// FIRST: fetch the repo URL from cloud API BEFORE any directory operations
	// This prevents deleting directories when cloud has no repo to clone
	var repoURL string
	var fetchErr error

	// use pre-fetched URL if available (e.g., from GetRepoDetail for public repos)
	if issue.cloneURL != "" {
		repoURL = issue.cloneURL
	} else if issue.repoType == "ledger" {
		repoURL, fetchErr = fetchLedgerURLWithError(issue.endpoint)
	} else {
		repoURL, fetchErr = fetchTeamContextURLWithError(issue.teamID, issue.endpoint)
	}

	if fetchErr != nil {
		// Cloud doesn't have the repo - don't delete anything
		if issue.repoType == "ledger" {
			return fmt.Errorf("cloud has not provisioned ledger yet - no changes made")
		}
		return fmt.Errorf("cloud has not provisioned team context yet - no changes made")
	}

	// Now that we know cloud has the repo, proceed with directory cleanup if needed
	if issue.issue == "not-git-repo" || issue.issue == "empty-dir" {
		// list contents to show user what will be moved
		entries, _ := os.ReadDir(issue.path)
		if len(entries) > 0 {
			fmt.Printf("\n  Directory contains %d item(s):\n", len(entries))
			for i, entry := range entries {
				if i >= 5 {
					fmt.Printf("    ... and %d more\n", len(entries)-5)
					break
				}
				if entry.IsDir() {
					fmt.Printf("    %s/\n", entry.Name())
				} else {
					fmt.Printf("    %s\n", entry.Name())
				}
			}
			fmt.Println()

			// ask for confirmation - move, not delete
			if !cli.ConfirmYesNo("Move this directory aside and clone fresh?", true) {
				return fmt.Errorf("user declined to move directory")
			}
		}

		// move aside with timestamp instead of deleting (safer)
		backupPath := fmt.Sprintf("%s_backup_%d", issue.path, time.Now().Unix())
		fmt.Printf("  Moving directory to: %s\n", backupPath)
		if err := os.Rename(issue.path, backupPath); err != nil {
			// if rename fails (e.g., cross-device), try to just proceed
			// the clone will fail if directory exists
			fmt.Printf("  Warning: could not move directory: %v\n", err)
			fmt.Printf("  Attempting to clone anyway...\n")
		}
	}

	// Clone from cloud via daemon
	if issue.repoType == "ledger" {
		fmt.Printf("  Cloning ledger from: %s\n", repoURL)
	} else {
		fmt.Printf("  Cloning team context from: %s\n", repoURL)
	}
	fmt.Printf("  Cloning to: %s\n", issue.path)
	return cloneViaDaemon(repoURL, issue.path, issue.repoType, issue.endpoint)
}

// fetchLedgerURLWithError fetches the ledger git URL from the cloud API with error details.
// Uses the ledger-status API (project-scoped) NOT /api/v1/cli/repos (which only returns team contexts).
func fetchLedgerURLWithError(currentEndpoint string) (string, error) {
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return "", fmt.Errorf("not in a git repository")
	}

	// get repo_id from project config - required for ledger-status API
	projectCfg, err := config.LoadProjectConfig(gitRoot)
	if err != nil {
		return "", fmt.Errorf("failed to load project config: %w", err)
	}
	if projectCfg.RepoID == "" {
		return "", fmt.Errorf("project not registered with SageOx (no repo_id) - run 'ox init' first")
	}

	// use project endpoint, not current default endpoint
	projectEndpoint := projectCfg.GetEndpoint()
	if projectEndpoint == "" {
		projectEndpoint = currentEndpoint
	}

	token, err := auth.GetTokenForEndpoint(projectEndpoint)
	if err != nil {
		return "", fmt.Errorf("get auth token for %s: %w", projectEndpoint, err)
	}
	if token == nil || token.AccessToken == "" {
		return "", fmt.Errorf("not authenticated to %s - run 'ox login' first", projectEndpoint)
	}

	// use ledger-status API (project-scoped) to get ledger URL
	client := api.NewRepoClientWithEndpoint(projectEndpoint).WithAuthToken(token.AccessToken)
	status, err := client.GetLedgerStatus(projectCfg.RepoID)
	if err != nil {
		return "", fmt.Errorf("ledger-status API call failed: %w", err)
	}
	if status == nil {
		return "", fmt.Errorf("ledger-status API returned empty response")
	}
	if status.Status != "ready" {
		return "", fmt.Errorf("ledger not ready (status=%s): %s", status.Status, status.Message)
	}
	if status.RepoURL == "" {
		return "", fmt.Errorf("ledger is ready but has no repo URL")
	}

	return status.RepoURL, nil
}

// fetchTeamContextURLWithError fetches the team context git URL from the cloud API with error details.
func fetchTeamContextURLWithError(teamID string, currentEndpoint string) (string, error) {
	if teamID == "" {
		return "", fmt.Errorf("team ID is empty")
	}

	token, err := auth.GetTokenForEndpoint(currentEndpoint)
	if err != nil {
		return "", fmt.Errorf("get auth token for %s: %w", currentEndpoint, err)
	}
	if token == nil || token.AccessToken == "" {
		return "", fmt.Errorf("not authenticated to %s - run 'ox login' first", currentEndpoint)
	}

	client := api.NewRepoClientWithEndpoint(currentEndpoint).WithAuthToken(token.AccessToken)
	teamInfo, err := client.GetTeamInfo(teamID)
	if err != nil {
		return "", fmt.Errorf("API call to %s for team %s failed: %w", currentEndpoint, teamID, err)
	}
	if teamInfo == nil {
		return "", fmt.Errorf("team %s not found at %s", teamID, currentEndpoint)
	}
	if teamInfo.RepoURL == "" {
		return "", fmt.Errorf("team %s at %s has no repo URL configured", teamID, currentEndpoint)
	}

	return teamInfo.RepoURL, nil
}

// saveGitCredentialsFromRepos builds and saves git credentials from an already-fetched
// ReposResponse. This avoids a duplicate /api/v1/cli/repos call when the response is
// already available (e.g., from fixMissingRepos).
func saveGitCredentialsFromRepos(repos *api.ReposResponse, projectEndpoint string) error {
	if repos == nil {
		return nil
	}

	creds := &gitserver.GitCredentials{
		Token:     repos.Token,
		ServerURL: repos.ServerURL,
		Username:  repos.Username,
		ExpiresAt: repos.ExpiresAt,
		Repos:     make(map[string]gitserver.RepoEntry),
	}

	for _, repo := range repos.Repos {
		creds.AddRepo(gitserver.RepoEntry{
			Name:   repo.Name,
			Type:   repo.Type,
			URL:    repo.URL,
			TeamID: repo.StableID(),
		})
	}

	ep := projectEndpoint
	if ep == "" {
		ep = endpoint.Get()
	}
	return gitserver.SaveCredentialsForEndpoint(ep, *creds)
}
