package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
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
	"github.com/sageox/ox/internal/repotools"
)

// checkGitAuth verifies git authentication/token validity.
// Checks if git credentials are configured and can access the remote.
func checkGitAuth() checkResult {
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return SkippedCheck("Git auth", "not in git repo", "")
	}

	// check if credential helper is configured
	credHelper := getGitConfigValue("credential.helper")
	if credHelper == "" {
		// no credential helper - check if SSH key exists as fallback
		sshConfigured := checkSSHAuth()
		if sshConfigured {
			return PassedCheck("Git auth", "SSH configured")
		}
		return WarningCheck("Git auth", "no credential helper",
			"Configure git credentials for remote operations")
	}

	// credential helper is configured
	return PassedCheck("Git auth", credHelper)
}

// checkSSHAuth checks if SSH authentication is likely configured for git.
func checkSSHAuth() bool {
	// check for common SSH key locations
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return false
	}

	sshKeys := []string{
		homeDir + "/.ssh/id_rsa",
		homeDir + "/.ssh/id_ed25519",
		homeDir + "/.ssh/id_ecdsa",
	}

	for _, key := range sshKeys {
		if _, err := os.Stat(key); err == nil {
			return true
		}
	}
	return false
}

// checkGitConnectivity verifies network connectivity to git remotes.
// Pings the configured origin remote to check reachability.
func checkGitConnectivity() checkResult {
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return SkippedCheck("Git connectivity", "not in git repo", "")
	}

	// get the origin remote URL
	cmd := exec.Command("git", "remote", "get-url", "origin")
	cmd.Dir = gitRoot
	output, err := cmd.Output()
	if err != nil {
		return SkippedCheck("Git connectivity", "no origin remote", "")
	}

	remoteURL := strings.TrimSpace(string(output))
	if remoteURL == "" {
		return SkippedCheck("Git connectivity", "no remote URL", "")
	}

	// create a command with timeout
	lsCmd := exec.Command("git", "ls-remote", "--exit-code", "-q", "origin", "HEAD")
	lsCmd.Dir = gitRoot

	// run with timeout using goroutine
	done := make(chan error, 1)
	go func() {
		done <- lsCmd.Run()
	}()

	select {
	case err := <-done:
		if err != nil {
			// check if it's an auth error vs network error
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) && exitErr.ExitCode() == 128 {
				return WarningCheck("Git connectivity", "auth failed",
					"Check credentials or SSH keys")
			}
			return WarningCheck("Git connectivity", "unreachable",
				"Check network connection or firewall settings")
		}
		return PassedCheck("Git connectivity", "reachable")
	case <-time.After(5 * time.Second):
		// kill the process on timeout
		_ = lsCmd.Process.Kill()
		return WarningCheck("Git connectivity", "timeout",
			"Remote did not respond within 5s")
	}
}

// checkGitConfig verifies local git configuration is properly set.
// Checks for user.name and user.email which are required for commits.
func checkGitConfig(fix bool) checkResult {
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return SkippedCheck("Git config", "not in git repo", "")
	}

	identity, err := repotools.DetectGitIdentity()
	if err != nil {
		return WarningCheck("Git config", "detection failed", err.Error())
	}

	if identity == nil || (identity.Name == "" && identity.Email == "") {
		return FailedCheck("Git config", "incomplete",
			"Run `git config --global user.name 'Name'` and `git config --global user.email 'email@example.com'`")
	}

	var issues []string
	if identity.Name == "" {
		issues = append(issues, "user.name not set")
	}
	if identity.Email == "" {
		issues = append(issues, "user.email not set")
	}

	if len(issues) > 0 {
		return WarningCheck("Git config", strings.Join(issues, ", "),
			"Configure missing git settings for proper commit attribution")
	}

	return PassedCheck("Git config", "user.name and user.email set")
}

// checkGitRepoState checks for uncommitted SageOx config under .sageox/.
// Only reports issues with SageOx-managed files — the user's repo state is their own business.
func checkGitRepoState() checkResult {
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return SkippedCheck("SageOx config", "not in git repo", "")
	}

	// only check for uncommitted changes under .sageox/
	statusCmd := exec.Command("git", "status", "--porcelain", ".sageox/")
	statusCmd.Dir = gitRoot
	output, err := statusCmd.Output()
	if err == nil && len(output) > 0 {
		lines := strings.Split(strings.TrimSpace(string(output)), "\n")
		count := 0
		for _, line := range lines {
			if line != "" {
				count++
			}
		}
		if count > 0 {
			return WarningCheck("SageOx config",
				fmt.Sprintf("%d uncommitted change(s) in .sageox/", count),
				"Run 'git add .sageox/ && git commit' to persist config")
		}
	}

	return PassedCheck("SageOx config", "committed and up to date")
}


// checkGitRemotes validates configured git remotes.
// Checks that origin is configured and URL format is valid.
func checkGitRemotes() checkResult {
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return SkippedCheck("Git remotes", "not in git repo", "")
	}

	urls, err := repotools.GetRemoteURLs()
	if err != nil {
		return WarningCheck("Git remotes", "detection failed", err.Error())
	}

	if len(urls) == 0 {
		return InfoCheck("Git remotes", "no remotes configured",
			"Add a remote with `git remote add origin <url>`")
	}

	// check for origin specifically
	originCmd := exec.Command("git", "remote", "get-url", "origin")
	originCmd.Dir = gitRoot
	originOutput, err := originCmd.Output()
	if err != nil {
		return WarningCheck("Git remotes", "no origin",
			fmt.Sprintf("Found %d remote(s) but no 'origin'", len(urls)))
	}

	originURL := strings.TrimSpace(string(originOutput))

	// basic URL validation
	if !isValidGitURL(originURL) {
		return WarningCheck("Git remotes", "invalid origin URL",
			fmt.Sprintf("URL format issue: %s", originURL))
	}

	return PassedCheck("Git remotes", fmt.Sprintf("%d configured", len(urls)))
}

// isValidGitURL performs basic validation of a git remote URL.
func isValidGitURL(url string) bool {
	if url == "" {
		return false
	}
	// accept SSH URLs (git@...), HTTPS URLs, and file:// URLs
	validPrefixes := []string{
		"git@",
		"https://",
		"http://",
		"ssh://",
		"git://",
		"file://",
	}
	for _, prefix := range validPrefixes {
		if strings.HasPrefix(url, prefix) {
			return true
		}
	}
	return false
}

// checkGitHooks verifies git hooks are not interfering with operations.
// Checks for common issues with pre-commit or other hooks.
func checkGitHooks() checkResult {
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return SkippedCheck("Git hooks", "not in git repo", "")
	}

	// check core.hooksPath configuration
	hooksPath := getGitConfigValue("core.hooksPath")

	// check default hooks directory
	defaultHooksDir := gitRoot + "/.git/hooks"
	if hooksPath != "" {
		defaultHooksDir = hooksPath
	}

	// look for active hooks (executable files without .sample extension)
	entries, err := os.ReadDir(defaultHooksDir)
	if err != nil {
		if os.IsNotExist(err) {
			return PassedCheck("Git hooks", "no hooks directory")
		}
		return SkippedCheck("Git hooks", "read error", "")
	}

	var activeHooks []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		// skip sample files
		if strings.HasSuffix(name, ".sample") {
			continue
		}
		// check if executable
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if info.Mode()&0111 != 0 {
			activeHooks = append(activeHooks, name)
		}
	}

	if len(activeHooks) == 0 {
		return PassedCheck("Git hooks", "none active")
	}

	// report active hooks (informational, not a problem)
	return PassedCheck("Git hooks", fmt.Sprintf("%d active", len(activeHooks)))
}

// getGitConfigValue reads a git configuration value.
func getGitConfigValue(key string) string {
	cmd := exec.Command("git", "config", "--get", key)
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

// checkGitLFS checks if Git LFS is properly configured when needed.
// Returns warning if LFS files are present but LFS is not installed.
func checkGitLFS() checkResult {
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return SkippedCheck("Git LFS", "not in git repo", "")
	}

	// check if .gitattributes contains LFS patterns
	gitattrsPath := gitRoot + "/.gitattributes"
	content, err := os.ReadFile(gitattrsPath)
	if err != nil {
		return SkippedCheck("Git LFS", "no .gitattributes", "")
	}

	// look for LFS filter patterns
	hasLFSPatterns := strings.Contains(string(content), "filter=lfs")
	if !hasLFSPatterns {
		return SkippedCheck("Git LFS", "not used", "")
	}

	// LFS patterns found - check if git-lfs is installed
	_, err = exec.LookPath("git-lfs")
	if err != nil {
		return FailedCheck("Git LFS", "not installed",
			"Install git-lfs: https://git-lfs.com")
	}

	// check if LFS is initialized in this repo
	lfsCmd := exec.Command("git", "lfs", "env")
	lfsCmd.Dir = gitRoot
	if err := lfsCmd.Run(); err != nil {
		return WarningCheck("Git LFS", "not initialized",
			"Run `git lfs install` to initialize LFS in this repo")
	}

	return PassedCheck("Git LFS", "configured")
}

// checkStashedChanges reports if there are stashed changes.
// Informational only - stashes are valid workflow but good to be aware of.
func checkStashedChanges() checkResult {
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return SkippedCheck("Git stash", "not in git repo", "")
	}

	cmd := exec.Command("git", "stash", "list")
	cmd.Dir = gitRoot
	output, err := cmd.Output()
	if err != nil {
		return SkippedCheck("Git stash", "check failed", "")
	}

	if len(output) == 0 {
		return PassedCheck("Git stash", "empty")
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	count := len(lines)
	if count > 0 {
		return PassedCheck("Git stash", fmt.Sprintf("%d stash(es)", count))
	}

	return PassedCheck("Git stash", "empty")
}

// checkMergeConflicts checks if there are unresolved merge conflicts.
func checkMergeConflicts() checkResult {
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return SkippedCheck("Merge conflicts", "not in git repo", "")
	}

	// check for MERGE_HEAD file (indicates merge in progress)
	mergeHeadPath := gitRoot + "/.git/MERGE_HEAD"
	if _, err := os.Stat(mergeHeadPath); err == nil {
		// merge in progress - check for unmerged files
		cmd := exec.Command("git", "diff", "--name-only", "--diff-filter=U")
		cmd.Dir = gitRoot
		output, err := cmd.Output()
		if err != nil {
			return WarningCheck("Merge conflicts", "merge in progress",
				"Resolve the ongoing merge operation")
		}

		unmerged := strings.TrimSpace(string(output))
		if unmerged != "" {
			files := strings.Split(unmerged, "\n")
			return FailedCheck("Merge conflicts", fmt.Sprintf("%d unresolved", len(files)),
				"Resolve conflicts and complete the merge")
		}
		return WarningCheck("Merge conflicts", "merge in progress",
			"Complete merge with `git merge --continue` or abort with `git merge --abort`")
	}

	// check for REBASE_HEAD (rebase in progress)
	rebaseHeadPath := gitRoot + "/.git/rebase-merge"
	if _, err := os.Stat(rebaseHeadPath); err == nil {
		return WarningCheck("Merge conflicts", "rebase in progress",
			"Complete rebase with `git rebase --continue` or abort with `git rebase --abort`")
	}

	return PassedCheck("Merge conflicts", "none")
}

// checkGitFsck validates git object integrity using git fsck.
// Uses --connectivity-only for speed (skips blob content checks).
// Detects corrupted objects that can cause silent failures.
// Has a 5s timeout to avoid blocking on very large repos.
func checkGitFsck() checkResult {
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return SkippedCheck("git integrity", "not in git repo", "")
	}

	// run fsck with connectivity-only for speed and a timeout
	cmd := exec.Command("git", "fsck", "--connectivity-only", "--no-progress")
	cmd.Dir = gitRoot

	type fsckResult struct {
		output []byte
		err    error
	}
	done := make(chan fsckResult, 1)
	go func() {
		output, err := cmd.CombinedOutput()
		done <- fsckResult{output, err}
	}()

	select {
	case result := <-done:
		if result.err != nil {
			// fsck found issues - provide actionable guidance
			lines := strings.Split(strings.TrimSpace(string(result.output)), "\n")
			summary := "corruption detected"
			if len(lines) > 0 && lines[0] != "" {
				firstLine := lines[0]
				if len(firstLine) > 50 {
					firstLine = firstLine[:47] + "..."
				}
				summary = firstLine
			}

			detail := `Git repository has corrupted objects. This can happen after:
  • Disk errors or power loss during git operations
  • Incomplete clones or interrupted fetches

To fix:
  1. Try: git gc --prune=now  (repairs minor issues)
  2. If that fails: git fetch --all  (re-downloads from remote)
  3. Last resort: re-clone the repository

Run 'git fsck' for detailed error information.`

			return FailedCheck("git integrity", summary, detail)
		}
		return PassedCheck("git integrity", "object database OK")
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		return PassedCheck("git integrity", "skipped (large repo)")
	}
}

// checkGitLockFiles checks for stale git lock files from crashed processes.
// Lock files block all git operations and must be manually removed.
func checkGitLockFiles() checkResult {
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return SkippedCheck("git locks", "not in git repo", "")
	}

	gitDir := filepath.Join(gitRoot, ".git")
	lockFiles := []string{
		"index.lock",
		"shallow.lock",
		"config.lock",
		"HEAD.lock",
	}

	var found []string
	var oldLocks []string
	oneHourAgo := time.Now().Add(-1 * time.Hour)

	for _, lock := range lockFiles {
		path := filepath.Join(gitDir, lock)
		if info, err := os.Stat(path); err == nil {
			found = append(found, lock)
			if info.ModTime().Before(oneHourAgo) {
				oldLocks = append(oldLocks, lock)
			}
		}
	}

	if len(found) == 0 {
		return PassedCheck("git locks", "no stale lock files")
	}

	detail := fmt.Sprintf(
		"Lock files found: %s\n"+
			"If no git commands are running, remove with:\n"+
			"  rm %s/{%s}",
		strings.Join(found, ", "),
		gitDir,
		strings.Join(found, ","))

	if len(oldLocks) > 0 {
		return FailedCheck("git locks",
			fmt.Sprintf("%d stale lock file(s) > 1 hour old", len(oldLocks)),
			detail).WithFixInfo(CheckSlugGitLock, FixLevelSuggested)
	}

	return WarningCheck("git locks",
		"lock files present (may be from active git process)",
		detail)
}

// checkGitRepoPaths validates that configured git repo paths exist and are valid git repos.
// Checks ledger.path and team_contexts[].path from config.local.toml.
// Also checks default ledger path if no ledger is configured.
// Detects legacy sibling directory structure and suggests migration.
// With fix=true, prompts user to fix issues.
func checkGitRepoPaths(fix bool) checkResult {
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return SkippedCheck("git repo paths", "not in git repo", "")
	}

	localCfg, err := config.LoadLocalConfig(gitRoot)
	if err != nil {
		return FailedCheck("git repo paths", "load failed", err.Error())
	}

	// collect all issues
	var issues []repoPathIssue

	// check for sibling ledger structure (deprecated in favor of user-dir)
	siblingLedgerIssue := checkSiblingLedgerStructure(gitRoot, localCfg)
	if siblingLedgerIssue != nil {
		issues = append(issues, *siblingLedgerIssue)
	}

	// check for legacy ledger structure (oldest format)
	legacyLedgerIssue := checkLegacyLedgerStructure(gitRoot, localCfg)
	if legacyLedgerIssue != nil {
		issues = append(issues, *legacyLedgerIssue)
	}

	// check configured ledger path
	if localCfg.Ledger != nil && localCfg.Ledger.Path != "" {
		issue := validateRepoPath(localCfg.Ledger.Path)
		if issue != "" {
			issues = append(issues, repoPathIssue{
				repoType: "ledger",
				path:     localCfg.Ledger.Path,
				endpoint: endpoint.GetForProject(gitRoot),
				issue:    issue,
			})
		}
	} else if legacyLedgerIssue == nil {
		// no ledger configured and no legacy - check default path
		if defaultPath, err := ledger.DefaultPath(); err == nil && defaultPath != "" {
			if info, err := os.Stat(defaultPath); err == nil {
				// path exists - check if it's a valid git repo
				if info.IsDir() && !ledger.Exists(defaultPath) {
					// directory exists but not a git repo — classify by contents
					issueType := "not-git-repo"
					if entries, _ := os.ReadDir(defaultPath); len(entries) == 0 {
						issueType = "empty-dir"
					}
					issues = append(issues, repoPathIssue{
						repoType: "ledger",
						path:     defaultPath,
						endpoint: endpoint.GetForProject(gitRoot),
						issue:    issueType,
					})
				}
			}
		}
	}

	// check team context paths
	for _, tc := range localCfg.TeamContexts {
		if tc.Path != "" {
			issue := validateRepoPath(tc.Path)
			if issue != "" {
				issues = append(issues, repoPathIssue{
					repoType: "team-context",
					path:     tc.Path,
					teamID:   tc.TeamID,
					teamName: tc.TeamName,
					endpoint: endpoint.GetForProject(gitRoot),
					issue:    issue,
				})
			}
			// check symlink validity for team contexts in centralized location
			symlinkIssue := checkTeamContextSymlink(tc, gitRoot)
			if symlinkIssue != nil {
				issues = append(issues, *symlinkIssue)
			}
		}
	}

	// discover team contexts from repo detail API that aren't in local config
	// GetRepos() only returns member repos; GetRepoDetail() also returns public/read-only
	projectEndpoint := endpoint.GetForProject(gitRoot)
	projectCfg, _ := config.LoadProjectConfig(gitRoot)
	if projectCfg != nil && projectCfg.RepoID != "" {
		if token, err := auth.GetTokenForEndpoint(projectEndpoint); err == nil && token != nil {
			client := api.NewRepoClientWithEndpoint(projectEndpoint).WithAuthToken(token.AccessToken)
			if detail, err := client.GetRepoDetail(projectCfg.RepoID); err == nil && detail != nil {
				knownTeamIDs := make(map[string]bool, len(localCfg.TeamContexts))
				for _, tc := range localCfg.TeamContexts {
					knownTeamIDs[tc.TeamID] = true
				}
				for _, tc := range detail.TeamContexts {
					if knownTeamIDs[tc.StableID()] {
						continue
					}
					expectedPath := paths.TeamContextDir(tc.StableID(), projectEndpoint)
					issue := validateRepoPath(expectedPath)
					if issue != "" {
						issues = append(issues, repoPathIssue{
							repoType: "team-context",
							path:     expectedPath,
							teamID:   tc.StableID(),
							teamName: tc.Name,
							endpoint: projectEndpoint,
							cloneURL: tc.RepoURL,
							issue:    issue,
						})
					}
				}
			}
		}
	}

	if len(issues) == 0 {
		// check if anything is configured
		hasLedger := localCfg.Ledger != nil || ledger.Exists("")
		if !hasLedger && len(localCfg.TeamContexts) == 0 {
			// nothing configured - check if we should have something
			// if authenticated + .sageox exists, there SHOULD be a ledger
			sageoxDir := filepath.Join(gitRoot, ".sageox")
			if _, err := os.Stat(sageoxDir); err == nil {
				// .sageox exists - check if authenticated
				if authenticated, _ := auth.IsAuthenticated(); authenticated {
					// recently initialized? daemon may not have synced yet
					if isRecentlyInitialized(gitRoot) {
						return InfoCheck("git repo paths", "repos syncing",
							"Background sync is cloning repos. Run `ox doctor` again in a minute.")
					}
					// authenticated + .sageox exists but no repos configured = problem
					if fix {
						return fixMissingRepos(gitRoot, localCfg)
					}
					return WarningCheck("git repo paths", "no repos configured",
						"Run `ox doctor --fix` to fetch and clone repos from cloud")
				}
				// not authenticated - suggest login first
				return WarningCheck("git repo paths", "no repos configured",
					"Run `ox login` to authenticate, then `ox doctor --fix` to clone repos")
			}
			// no .sageox - skip silently
			return SkippedCheck("git repo paths", "no repos configured", "")
		}
		return PassedCheck("git repo paths", "all paths valid")
	}

	// issues found
	if fix {
		return fixRepoPathIssues(gitRoot, localCfg, issues)
	}

	// check if all issues are "missing" and the project was just initialized.
	// after ox init, the daemon may not have cloned repos yet -- this is expected,
	// not a failure. downgrade to info so users don't see scary errors on first run.
	allMissing := true
	for _, issue := range issues {
		if issue.issue != "missing" {
			allMissing = false
			break
		}
	}
	if allMissing && isRecentlyInitialized(gitRoot) {
		return InfoCheck("git repo paths",
			fmt.Sprintf("%d repo(s) syncing", len(issues)),
			"Background sync is cloning repos. Run `ox doctor` again in a minute.")
	}

	// build detail message listing issues
	var details []string
	for _, issue := range issues {
		var desc string
		switch issue.issue {
		case "missing":
			desc = "not found"
		case "not-git-repo":
			desc = "exists but not a git repo"
		case "empty-dir":
			desc = "empty directory"
		case "sibling-structure":
			desc = "using deprecated sibling directory (migrating to user directory)"
		case "legacy-structure":
			desc = "using old sibling directory structure"
		case "broken-symlink":
			desc = "symlink target does not exist"
		case "invalid-symlink":
			desc = "path is not a valid symlink"
		default:
			desc = issue.issue
		}

		switch issue.repoType {
		case "ledger":
			details = append(details, fmt.Sprintf("Ledger: %s (%s)", issue.path, desc))
		case "team-context-symlink":
			teamInfo := issue.teamID
			if issue.teamName != "" {
				teamInfo = fmt.Sprintf("%s (%s)", issue.teamName, issue.teamID)
			}
			details = append(details, fmt.Sprintf("Team symlink %s: %s (%s)", teamInfo, issue.path, desc))
		default:
			teamInfo := issue.teamID
			if issue.teamName != "" {
				teamInfo = fmt.Sprintf("%s (%s)", issue.teamName, issue.teamID)
			}
			details = append(details, fmt.Sprintf("Team %s: %s (%s)", teamInfo, issue.path, desc))
		}
	}

	return FailedCheck("git repo paths",
		fmt.Sprintf("%d repo(s) with issues", len(issues)),
		fmt.Sprintf("%s\n       Run `ox doctor --fix` to repair", strings.Join(details, "\n       ")))
}

// checkSiblingLedgerStructure detects if the project is using the deprecated sibling
// directory structure (<project_parent>/<repo_name>_sageox/<endpoint_slug>/ledger)
// instead of the canonical user directory (~/.local/share/sageox/<ep>/ledgers/<repo_id>/).
func checkSiblingLedgerStructure(gitRoot string, localCfg *config.LocalConfig) *repoPathIssue {
	repoName := filepath.Base(gitRoot)
	ep := endpoint.GetForProject(gitRoot)
	siblingPath := config.SiblingLedgerPath(repoName, gitRoot, ep)
	if siblingPath == "" {
		return nil
	}

	// check if configured path matches the sibling pattern
	if localCfg.Ledger != nil && localCfg.Ledger.Path != "" {
		if localCfg.Ledger.Path == siblingPath && isGitRepo(siblingPath) {
			return &repoPathIssue{
				repoType: "ledger",
				path:     siblingPath,
				endpoint: ep,
				issue:    "sibling-structure",
			}
		}
		return nil
	}

	// no explicit config — check if sibling path exists
	if isGitRepo(siblingPath) {
		return &repoPathIssue{
			repoType: "ledger",
			path:     siblingPath,
			endpoint: ep,
			issue:    "sibling-structure",
		}
	}

	return nil
}

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

// checkLegacyLedgerStructure detects if the project is using the old sibling directory
// ledger structure (<project_parent>/<repo_name>_sageox_ledger) instead of the current
// sibling structure (<project_parent>/<repo_name>_sageox/<endpoint_slug>/ledger).
//
// Uses config.LegacyLedgerPath for path derivation to ensure consistency
// with the canonical ledger path functions in internal/config/local_config.go.
func checkLegacyLedgerStructure(gitRoot string, localCfg *config.LocalConfig) *repoPathIssue {
	repoName := filepath.Base(gitRoot)

	// skip if ledger is explicitly configured (user may have intentional setup)
	if localCfg.Ledger != nil && localCfg.Ledger.Path != "" {
		// check if the configured path is using the old sibling pattern
		ledgerPath := localCfg.Ledger.Path

		// use shared function for legacy path derivation (see internal/config/local_config.go)
		oldPatternPath := config.LegacyLedgerPath(repoName, gitRoot)
		if ledgerPath == oldPatternPath {
			// check if new centralized path would be appropriate
			// (this requires repo_id which we may not have locally)
			return &repoPathIssue{
				repoType: "ledger",
				path:     ledgerPath,
				endpoint: endpoint.GetForProject(gitRoot),
				issue:    "legacy-structure",
			}
		}
		return nil
	}

	// no explicit config - check if legacy sibling directory exists
	// use shared function for legacy path derivation (see internal/config/local_config.go)
	legacyPath := config.LegacyLedgerPath(repoName, gitRoot)

	if _, err := os.Stat(legacyPath); err == nil {
		// legacy path exists - check if it's a valid git repo
		if isGitRepo(legacyPath) {
			return &repoPathIssue{
				repoType: "ledger",
				path:     legacyPath,
				endpoint: endpoint.GetForProject(gitRoot),
				issue:    "legacy-structure",
			}
		}
	}

	return nil
}

// checkTeamContextSymlink validates symlinks in the centralized team context location.
// Returns an issue if the symlink is broken or invalid.
func checkTeamContextSymlink(tc config.TeamContext, gitRoot string) *repoPathIssue {
	if tc.Path == "" || tc.TeamID == "" {
		return nil
	}

	// use project-scoped endpoint for centralized path validation
	projectEndpoint := endpoint.GetForProject(gitRoot)

	// check if path is in the centralized location
	teamsDir := paths.TeamsDataDir(projectEndpoint)
	if !strings.HasPrefix(tc.Path, teamsDir) {
		// not in centralized location, skip symlink check
		return nil
	}

	// check if path is a symlink
	info, err := os.Lstat(tc.Path)
	if err != nil {
		return nil // path doesn't exist - handled by other checks
	}

	if info.Mode()&os.ModeSymlink != 0 {
		// it's a symlink - check if target exists
		target, err := os.Readlink(tc.Path)
		if err != nil {
			return &repoPathIssue{
				repoType: "team-context-symlink",
				path:     tc.Path,
				teamID:   tc.TeamID,
				teamName: tc.TeamName,
				endpoint: projectEndpoint,
				issue:    "invalid-symlink",
			}
		}

		// resolve relative symlinks
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(tc.Path), target)
		}

		if _, err := os.Stat(target); os.IsNotExist(err) {
			return &repoPathIssue{
				repoType: "team-context-symlink",
				path:     tc.Path,
				teamID:   tc.TeamID,
				teamName: tc.TeamName,
				endpoint: projectEndpoint,
				issue:    "broken-symlink",
			}
		}
	}

	return nil
}

// validateRepoPath checks if a path exists and is a valid git repository.
// Returns empty string if valid, or issue type: "missing", "not-git-repo", "empty-dir", "not-directory"
func validateRepoPath(path string) string {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return "missing"
	}
	if err != nil {
		return "error"
	}

	if !info.IsDir() {
		return "not-directory"
	}

	// check for .git
	gitDir := filepath.Join(path, ".git")
	if _, err := os.Stat(gitDir); os.IsNotExist(err) {
		// check if empty
		entries, _ := os.ReadDir(path)
		if len(entries) == 0 {
			return "empty-dir"
		}
		return "not-git-repo"
	}

	return "" // valid
}

// repoPathIssue represents a problem with a repo path.
type repoPathIssue struct {
	repoType string // "ledger", "team-context", or "team-context-symlink"
	path     string
	teamID   string // only for team-context
	teamName string // only for team-context
	endpoint string
	cloneURL string // pre-fetched clone URL (avoids re-lookup for public/read-only repos)
	issue    string // "missing", "not-git-repo", "empty-dir", "sibling-structure", "legacy-structure", "broken-symlink", "invalid-symlink"
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

		if err := cloneViaDaemon(tc.cloneURL, tcPath, "team_context", projectEndpoint); err != nil {
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
	//
	// Uses gitserver.CloneFromURLWithEndpoint which:
	// - Loads credentials from local credential store
	// - Builds authenticated URL with oauth2:TOKEN format
	// - Executes git clone directly
	// ─────────────────────────────────────────────────────────────────────────
	fmt.Println("    Note: Daemon not running, using direct clone")

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if err := gitserver.CloneFromURLWithEndpoint(ctx, cloneURL, targetPath, endpointURL, nil); err != nil {
		return fmt.Errorf("direct clone failed: %w", err)
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

// checkLedgerRemoteURL validates the ledger's remote credentials are current.
// Delegates to checkLedgerRemoteURLMatch for the actual PAT comparison.
func checkLedgerRemoteURL(localCfg *config.LocalConfig) checkResult {
	if localCfg == nil || localCfg.Ledger == nil || localCfg.Ledger.Path == "" {
		return SkippedCheck("Ledger remote URL", "no ledger configured", "")
	}

	if !isGitRepo(localCfg.Ledger.Path) {
		return SkippedCheck("Ledger remote URL", "ledger not a git repo", "")
	}

	// delegate to the PAT comparison check (read-only, no fix)
	return checkLedgerRemoteURLMatch(false)
}

// checkTeamContextRemoteURLs validates team context local git remotes match cloud URLs.
// Reads from locally saved git credentials (populated by checkGitCredentials in Category 0)
// to avoid duplicate API calls.
func checkTeamContextRemoteURLs(localCfg *config.LocalConfig) []checkResult {
	var results []checkResult

	if localCfg == nil {
		return results
	}

	for _, tc := range localCfg.TeamContexts {
		if tc.Path == "" {
			continue
		}

		if !isGitRepo(tc.Path) {
			continue
		}

		// get local origin URL
		cmd := exec.Command("git", "-C", tc.Path, "remote", "get-url", "origin")
		output, err := cmd.Output()
		if err != nil {
			results = append(results, WarningCheck(
				fmt.Sprintf("Team %s remote URL", tc.TeamName),
				"no origin remote configured",
				"Run: git -C "+tc.Path+" remote add origin <url>"))
			continue
		}
		localURL := strings.TrimSpace(string(output))

		// get cloud URL from locally cached credentials (no API call)
		cloudURL := getTeamURLFromCredentials(tc.TeamName)
		if cloudURL == "" {
			continue // skip if no cached URL
		}

		// compare
		if normalizeGitURLForCompare(localURL) != normalizeGitURLForCompare(cloudURL) {
			results = append(results, WarningCheck(
				fmt.Sprintf("Team %s remote URL", tc.TeamName),
				fmt.Sprintf("mismatch: local=%s, cloud=%s", gitserver.SanitizeRemoteURL(localURL), gitserver.SanitizeRemoteURL(cloudURL)),
				"Update local remote or re-clone from cloud URL"))
		} else {
			results = append(results, PassedCheck(
				fmt.Sprintf("Team %s remote URL", tc.TeamName),
				"matches cloud"))
		}
	}

	return results
}

// normalizeGitURLForCompare normalizes git URLs for comparison.
// Strips credentials (userinfo), protocol, .git suffix, and lowercases.
// Handles SSH vs HTTPS format differences.
func normalizeGitURLForCompare(rawURL string) string {
	rawURL = strings.TrimSpace(rawURL)
	rawURL = strings.ToLower(rawURL)

	// handle SSH format: git@host:path -> host/path
	if strings.HasPrefix(rawURL, "git@") {
		rawURL = strings.TrimPrefix(rawURL, "git@")
		rawURL = strings.Replace(rawURL, ":", "/", 1)
		return strings.TrimSuffix(rawURL, ".git")
	}

	// parse to strip userinfo (credentials) safely
	parsed, err := url.Parse(rawURL)
	if err != nil {
		// fallback to string-based stripping
		rawURL = strings.TrimPrefix(rawURL, "https://")
		rawURL = strings.TrimPrefix(rawURL, "http://")
		return strings.TrimSuffix(rawURL, ".git")
	}

	parsed.User = nil // strip oauth2:TOKEN@ credentials
	parsed.Scheme = ""
	parsed.Fragment = ""
	parsed.RawQuery = ""
	result := strings.TrimPrefix(parsed.String(), "//")
	return strings.TrimSuffix(result, ".git")
}

// checkLedgerStructureMigration checks if ledger should be migrated from sibling to centralized.
// This is a separate informational check that appears in the "Git Repository Health" category.
func checkLedgerStructureMigration() checkResult {
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return SkippedCheck("Ledger structure", "not in git repo", "")
	}

	localCfg, err := config.LoadLocalConfig(gitRoot)
	if err != nil {
		return SkippedCheck("Ledger structure", "config error", "")
	}

	// check if using legacy structure
	issue := checkLegacyLedgerStructure(gitRoot, localCfg)
	if issue != nil && issue.issue == "legacy-structure" {
		return InfoCheck("Ledger structure", "using legacy sibling directory",
			"Consider migrating to <repo>_sageox/<endpoint>/ledger (run 'ox doctor --fix')")
	}

	// check if using current sibling directory structure (<repo>_sageox/<endpoint>/ledger)
	if localCfg.Ledger != nil && localCfg.Ledger.Path != "" {
		if strings.Contains(localCfg.Ledger.Path, "_sageox"+string(filepath.Separator)) {
			return PassedCheck("Ledger structure", "sibling directory")
		}
	}

	return SkippedCheck("Ledger structure", "no ledger configured", "")
}

// checkProjectSymlinks ensures .sageox/ledger and .sageox/teams/primary symlinks exist
// and point to the actual configured paths (not just the XDG defaults).
func checkProjectSymlinks(fix bool) checkResult {
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return SkippedCheck("Project symlinks", "not in git repo", "")
	}
	if !config.IsInitialized(gitRoot) {
		return SkippedCheck("Project symlinks", "not initialized", "")
	}

	projectCfg, err := config.LoadProjectConfig(gitRoot)
	if err != nil || projectCfg == nil {
		return SkippedCheck("Project symlinks", "no project config", "")
	}
	ep := projectCfg.GetEndpoint()
	if ep == "" {
		return SkippedCheck("Project symlinks", "no endpoint", "")
	}

	localCfg, _ := config.LoadLocalConfig(gitRoot)

	// determine the actual ledger path: prefer config.local.toml, fall back to XDG default
	var ledgerTarget string
	if localCfg != nil && localCfg.Ledger != nil && localCfg.Ledger.Path != "" {
		ledgerTarget = localCfg.Ledger.Path
	} else if projectCfg.RepoID != "" {
		ledgerTarget = config.DefaultLedgerPath(projectCfg.RepoID, ep)
	}

	// determine the actual team context path
	var teamTarget string
	if projectCfg.TeamID != "" {
		teamTarget = paths.TeamContextDir(projectCfg.TeamID, ep)
	}

	// checkSymlink returns true if the symlink exists and points to the expected target
	checkSymlink := func(rel, expectedTarget string) bool {
		if expectedTarget == "" {
			return true // nothing to check
		}
		abs := filepath.Join(gitRoot, rel)
		target, err := os.Readlink(abs)
		if err != nil {
			return false // missing or not a symlink
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(abs), target)
		}
		return filepath.Clean(target) == filepath.Clean(expectedTarget)
	}

	var issues []string
	if ledgerTarget != "" && !checkSymlink(".sageox/ledger", ledgerTarget) {
		issues = append(issues, ".sageox/ledger")
	}
	if teamTarget != "" && !checkSymlink(".sageox/teams/primary", teamTarget) {
		issues = append(issues, ".sageox/teams/primary")
	}

	if len(issues) == 0 {
		return PassedCheck("Project symlinks", "ok")
	}

	if !fix {
		return WarningCheck("Project symlinks",
			fmt.Sprintf("%d need repair: %s", len(issues), strings.Join(issues, ", ")),
			"Run `ox doctor --fix` to create project symlinks")
	}

	// fix: create or update symlinks to point to actual configured paths
	var fixed int
	if ledgerTarget != "" {
		if err := config.CreateOrUpdateProjectSymlink(gitRoot, ".sageox/ledger", ledgerTarget); err == nil {
			fixed++
		}
	}
	if teamTarget != "" {
		if err := config.CreateProjectTeamSymlinks(gitRoot, projectCfg.TeamID, ep); err == nil {
			fixed++
		}
	}

	if fixed > 0 {
		return PassedCheck("Project symlinks", fmt.Sprintf("fixed %d symlinks", fixed))
	}
	return WarningCheck("Project symlinks", "could not create symlinks", "")
}

// checkTeamContextSymlinks validates all team context symlinks in centralized location.
// Returns a single check result summarizing symlink health.
func checkTeamContextSymlinks() checkResult {
	gitRoot := findGitRoot()
	currentEndpoint := endpoint.GetForProject(gitRoot)
	teamsDir := paths.TeamsDataDir(currentEndpoint)

	// check if teams directory exists
	if _, err := os.Stat(teamsDir); os.IsNotExist(err) {
		return SkippedCheck("Team symlinks", "no teams directory", "")
	}

	entries, err := os.ReadDir(teamsDir)
	if err != nil {
		return SkippedCheck("Team symlinks", "read error", "")
	}

	var total, valid, broken int
	for _, entry := range entries {
		entryPath := filepath.Join(teamsDir, entry.Name())
		info, err := os.Lstat(entryPath)
		if err != nil {
			continue
		}

		// check if it's a symlink
		if info.Mode()&os.ModeSymlink != 0 {
			total++
			// check if target exists
			if _, err := os.Stat(entryPath); err == nil {
				valid++
			} else {
				broken++
			}
		}
	}

	if total == 0 {
		return SkippedCheck("Team symlinks", "no symlinks found", "")
	}

	if broken > 0 {
		return WarningCheck("Team symlinks", fmt.Sprintf("%d/%d broken", broken, total),
			"Run `ox doctor --fix` to repair broken symlinks")
	}

	return PassedCheck("Team symlinks", fmt.Sprintf("%d valid", valid))
}

// defaultGitignoreContent is the default content for .gitignore files in ledger and team context directories.
// This prevents accidental commits of OS files, editor temporary files, and local configuration.
const defaultGitignoreContent = `# OS files
.DS_Store
Thumbs.db

# Editor files
*.swp
*~

# Local config
.env.local
`

// checkLedgerGitignore checks if .gitignore exists in the ledger directory.
// With fix=true, creates a default .gitignore if missing.
func checkLedgerGitignore(fix bool) checkResult {
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return SkippedCheck("Ledger .gitignore", "not in git repo", "")
	}

	localCfg, err := config.LoadLocalConfig(gitRoot)
	if err != nil {
		return SkippedCheck("Ledger .gitignore", "config error", "")
	}

	// get ledger path
	var ledgerPath string
	if localCfg.Ledger != nil && localCfg.Ledger.Path != "" {
		ledgerPath = localCfg.Ledger.Path
	} else {
		// try default path
		defaultPath, err := ledger.DefaultPath()
		if err != nil {
			return SkippedCheck("Ledger .gitignore", "no ledger configured", "")
		}
		if !ledger.Exists(defaultPath) {
			return SkippedCheck("Ledger .gitignore", "no ledger found", "")
		}
		ledgerPath = defaultPath
	}

	// verify ledger exists and is a git repo
	if !isGitRepo(ledgerPath) {
		return SkippedCheck("Ledger .gitignore", "ledger not a git repo", "")
	}

	gitignorePath := filepath.Join(ledgerPath, ".gitignore")

	// check if .gitignore exists
	if _, err := os.Stat(gitignorePath); os.IsNotExist(err) {
		if fix {
			// create default .gitignore
			if err := os.WriteFile(gitignorePath, []byte(defaultGitignoreContent), 0644); err != nil {
				return FailedCheck("Ledger .gitignore", "create failed", err.Error())
			}
			return PassedCheck("Ledger .gitignore", "created default")
		}
		return FailedCheck("Ledger .gitignore", "missing",
			"Run `ox doctor --fix` to create a default .gitignore")
	}

	return PassedCheck("Ledger .gitignore", "present")
}

// checkTeamContextGitignore checks if .gitignore exists in all team context directories.
// With fix=true, creates default .gitignore files where missing.
func checkTeamContextGitignore(fix bool) checkResult {
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return SkippedCheck("Team context .gitignore", "not in git repo", "")
	}

	localCfg, err := config.LoadLocalConfig(gitRoot)
	if err != nil {
		return SkippedCheck("Team context .gitignore", "config error", "")
	}

	if len(localCfg.TeamContexts) == 0 {
		return SkippedCheck("Team context .gitignore", "no team contexts configured", "")
	}

	var missing, fixed, total int

	for _, tc := range localCfg.TeamContexts {
		if tc.Path == "" {
			continue
		}

		// verify team context exists and is a git repo
		if !isGitRepo(tc.Path) {
			continue
		}

		total++
		gitignorePath := filepath.Join(tc.Path, ".gitignore")

		// check if .gitignore exists
		if _, err := os.Stat(gitignorePath); os.IsNotExist(err) {
			if fix {
				// create default .gitignore
				if err := os.WriteFile(gitignorePath, []byte(defaultGitignoreContent), 0644); err != nil {
					slog.Warn("failed to create .gitignore in team context",
						"path", tc.Path, "error", err)
					missing++
					continue
				}
				fixed++
			} else {
				missing++
			}
		}
	}

	if total == 0 {
		return SkippedCheck("Team context .gitignore", "no valid team contexts", "")
	}

	if missing > 0 && !fix {
		return FailedCheck("Team context .gitignore",
			fmt.Sprintf("%d/%d missing", missing, total),
			"Run `ox doctor --fix` to create default .gitignore files")
	}

	if fixed > 0 {
		return PassedCheck("Team context .gitignore",
			fmt.Sprintf("created %d .gitignore file(s)", fixed))
	}

	return PassedCheck("Team context .gitignore",
		fmt.Sprintf("%d present", total))
}

// checkoutDotSageoxGitignoreContent is the default content for .sageox/.gitignore
// inside ledger and team context checkout directories.
// This prevents checkout.json and workspaces.jsonl from being committed.
const checkoutDotSageoxGitignoreContent = `# Local-only files - do not commit
checkout.json
workspaces.jsonl
`

// checkLedgerCheckoutGitignore checks if .sageox/.gitignore exists in the ledger checkout
// and properly ignores checkout.json. This is DIFFERENT from the root .gitignore check.
// The .sageox/.gitignore inside the checkout protects local metadata from being committed.
// With fix=true, creates the .gitignore if missing.
// Slug: gitignore-missing
func checkLedgerCheckoutGitignore(fix bool) checkResult {
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return SkippedCheck("Ledger checkout .gitignore", "not in git repo", "")
	}

	localCfg, err := config.LoadLocalConfig(gitRoot)
	if err != nil {
		return SkippedCheck("Ledger checkout .gitignore", "config error", "")
	}

	// get ledger path
	var ledgerPath string
	if localCfg.Ledger != nil && localCfg.Ledger.Path != "" {
		ledgerPath = localCfg.Ledger.Path
	} else {
		// try default path
		defaultPath, err := ledger.DefaultPath()
		if err != nil {
			return SkippedCheck("Ledger checkout .gitignore", "no ledger configured", "")
		}
		if !ledger.Exists(defaultPath) {
			return SkippedCheck("Ledger checkout .gitignore", "no ledger found", "")
		}
		ledgerPath = defaultPath
	}

	// verify ledger exists and is a git repo
	if !isGitRepo(ledgerPath) {
		return SkippedCheck("Ledger checkout .gitignore", "ledger not a git repo", "")
	}

	// check for .sageox/.gitignore inside the ledger checkout
	sageoxDir := filepath.Join(ledgerPath, ".sageox")
	gitignorePath := filepath.Join(sageoxDir, ".gitignore")

	// check if .sageox directory exists
	if _, err := os.Stat(sageoxDir); os.IsNotExist(err) {
		// .sageox doesn't exist - this is fine, no checkout.json to protect
		return SkippedCheck("Ledger checkout .gitignore", "no .sageox in ledger", "")
	}

	// .sageox exists - check if .gitignore exists
	if _, err := os.Stat(gitignorePath); os.IsNotExist(err) {
		if fix {
			// create .gitignore
			if err := os.WriteFile(gitignorePath, []byte(checkoutDotSageoxGitignoreContent), 0644); err != nil {
				return FailedCheck("Ledger checkout .gitignore", "create failed", err.Error())
			}
			return PassedCheck("Ledger checkout .gitignore", "created")
		}
		return checkResult{
			name:    "Ledger checkout .gitignore",
			passed:  false,
			message: "missing",
			detail:  "Run `ox doctor --fix` or `ox doctor --fix gitignore-missing` to create",
		}
	}

	// .gitignore exists - verify it contains checkout.json
	content, err := os.ReadFile(gitignorePath)
	if err != nil {
		return WarningCheck("Ledger checkout .gitignore", "read error", err.Error())
	}

	if !strings.Contains(string(content), "checkout.json") {
		if fix {
			// append checkout.json to existing .gitignore
			newContent := string(content)
			if !strings.HasSuffix(newContent, "\n") {
				newContent += "\n"
			}
			newContent += "checkout.json\n"
			if err := os.WriteFile(gitignorePath, []byte(newContent), 0644); err != nil {
				return FailedCheck("Ledger checkout .gitignore", "update failed", err.Error())
			}
			return PassedCheck("Ledger checkout .gitignore", "updated")
		}
		return checkResult{
			name:    "Ledger checkout .gitignore",
			passed:  false,
			message: "checkout.json not ignored",
			detail:  "Run `ox doctor --fix` or `ox doctor --fix gitignore-missing` to add",
		}
	}

	return PassedCheck("Ledger checkout .gitignore", "checkout.json ignored")
}

// checkTeamContextCheckoutGitignore checks if .sageox/.gitignore exists in all team context
// checkout directories and properly ignores checkout.json.
// With fix=true, creates the .gitignore files where missing.
// Slug: gitignore-missing
func checkTeamContextCheckoutGitignore(fix bool) checkResult {
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return SkippedCheck("Team checkout .gitignore", "not in git repo", "")
	}

	localCfg, err := config.LoadLocalConfig(gitRoot)
	if err != nil {
		return SkippedCheck("Team checkout .gitignore", "config error", "")
	}

	if len(localCfg.TeamContexts) == 0 {
		return SkippedCheck("Team checkout .gitignore", "no team contexts", "")
	}

	var missing, notIgnored, fixed, total int

	for _, tc := range localCfg.TeamContexts {
		if tc.Path == "" {
			continue
		}

		// verify team context exists and is a git repo
		if !isGitRepo(tc.Path) {
			continue
		}

		// check for .sageox directory
		sageoxDir := filepath.Join(tc.Path, ".sageox")
		if _, err := os.Stat(sageoxDir); os.IsNotExist(err) {
			// no .sageox - nothing to protect
			continue
		}

		total++
		gitignorePath := filepath.Join(sageoxDir, ".gitignore")

		// check if .gitignore exists
		if _, err := os.Stat(gitignorePath); os.IsNotExist(err) {
			if fix {
				// create .gitignore
				if err := os.WriteFile(gitignorePath, []byte(checkoutDotSageoxGitignoreContent), 0644); err != nil {
					slog.Warn("failed to create .sageox/.gitignore in team context",
						"path", tc.Path, "error", err)
					missing++
					continue
				}
				fixed++
			} else {
				missing++
			}
			continue
		}

		// .gitignore exists - verify it contains checkout.json
		content, err := os.ReadFile(gitignorePath)
		if err != nil {
			continue
		}

		if !strings.Contains(string(content), "checkout.json") {
			if fix {
				// append checkout.json
				newContent := string(content)
				if !strings.HasSuffix(newContent, "\n") {
					newContent += "\n"
				}
				newContent += "checkout.json\n"
				if err := os.WriteFile(gitignorePath, []byte(newContent), 0644); err != nil {
					slog.Warn("failed to update .sageox/.gitignore in team context",
						"path", tc.Path, "error", err)
					notIgnored++
					continue
				}
				fixed++
			} else {
				notIgnored++
			}
		}
	}

	if total == 0 {
		return SkippedCheck("Team checkout .gitignore", "no .sageox dirs in team contexts", "")
	}

	issues := missing + notIgnored
	if issues > 0 && !fix {
		var msg string
		if missing > 0 && notIgnored > 0 {
			msg = fmt.Sprintf("%d missing, %d incomplete", missing, notIgnored)
		} else if missing > 0 {
			msg = fmt.Sprintf("%d/%d missing", missing, total)
		} else {
			msg = fmt.Sprintf("%d/%d incomplete", notIgnored, total)
		}
		return checkResult{
			name:    "Team checkout .gitignore",
			passed:  false,
			message: msg,
			detail:  "Run `ox doctor --fix` or `ox doctor --fix gitignore-missing` to fix",
		}
	}

	if fixed > 0 {
		return PassedCheck("Team checkout .gitignore",
			fmt.Sprintf("fixed %d .gitignore file(s)", fixed))
	}

	return PassedCheck("Team checkout .gitignore",
		fmt.Sprintf("%d properly configured", total))
}

// checkLedgerPathMismatch detects when ledger.DefaultPath() computed path differs from
// the path configured in config.local.toml. This can happen when:
// 1. User moved their project directory
// 2. DefaultPath computation logic changed between versions
// 3. config.local.toml was manually edited
// 4. Ledger exists at default path but config has no ledger entry
//
// With fix=true, offers to update config.local.toml to match the computed default path.
// Slug: ledger-path-mismatch
func checkLedgerPathMismatch(fix bool) checkResult {
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return SkippedCheck("Ledger path config", "not in git repo", "")
	}

	localCfg, err := config.LoadLocalConfig(gitRoot)
	if err != nil {
		return SkippedCheck("Ledger path config", "config load failed", "")
	}

	// compute the default ledger path
	defaultPath, err := ledger.DefaultPath()
	if err != nil {
		return SkippedCheck("Ledger path config", "cannot compute default path", "")
	}

	// case 1: ledger exists at default path but config has no ledger entry
	if localCfg.Ledger == nil || localCfg.Ledger.Path == "" {
		if ledger.Exists(defaultPath) {
			if fix {
				// offer to add ledger config
				fmt.Println()
				fmt.Println("  Ledger found at default path but not in config.local.toml:")
				fmt.Printf("    Default path: %s\n", defaultPath)
				fmt.Println()

				if cli.ConfirmYesNo("Add this ledger path to config.local.toml?", true) {
					if localCfg.Ledger == nil {
						localCfg.Ledger = &config.LedgerConfig{}
					}
					localCfg.Ledger.Path = defaultPath

					if err := config.SaveLocalConfig(gitRoot, localCfg); err != nil {
						return FailedCheck("Ledger path config", "save failed", err.Error())
					}
					return PassedCheck("Ledger path config", "added to config")
				}
				return WarningCheck("Ledger path config", "ledger not in config",
					"Run `ox doctor --fix ledger-path-mismatch` to add")
			}
			return WarningCheck("Ledger path config", "ledger exists but not configured",
				fmt.Sprintf("Ledger at %s is not in config.local.toml. Run `ox doctor --fix ledger-path-mismatch` to add", defaultPath))
		}
		// no ledger and no config - nothing to check
		return SkippedCheck("Ledger path config", "no ledger configured", "")
	}

	// case 2: config has ledger path - check if it matches default
	configuredPath := localCfg.Ledger.Path

	// normalize paths for comparison
	normalizedDefault, _ := filepath.Abs(defaultPath)
	normalizedConfigured, _ := filepath.Abs(configuredPath)

	if normalizedDefault == normalizedConfigured {
		// paths match
		return PassedCheck("Ledger path config", "matches default")
	}

	// paths differ - determine which exists
	defaultExists := ledger.Exists(defaultPath)
	configuredExists := ledger.Exists(configuredPath)

	if fix {
		fmt.Println()
		fmt.Println("  Ledger path mismatch detected:")
		fmt.Printf("    Computed default: %s", defaultPath)
		if defaultExists {
			fmt.Println(" (exists)")
		} else {
			fmt.Println(" (does not exist)")
		}
		fmt.Printf("    Configured path:  %s", configuredPath)
		if configuredExists {
			fmt.Println(" (exists)")
		} else {
			fmt.Println(" (does not exist)")
		}
		fmt.Println()

		// decide what to offer based on what exists
		if defaultExists && !configuredExists {
			// default exists, configured does not - suggest using default
			if cli.ConfirmYesNo("Update config to use the default path (where ledger exists)?", true) {
				localCfg.Ledger.Path = defaultPath
				if err := config.SaveLocalConfig(gitRoot, localCfg); err != nil {
					return FailedCheck("Ledger path config", "save failed", err.Error())
				}
				return PassedCheck("Ledger path config", "updated to default path")
			}
		} else if configuredExists && !defaultExists {
			// configured exists, default does not - this is intentional, pass
			fmt.Println("  The configured path exists and appears intentional.")
			fmt.Println("  No action needed.")
			return PassedCheck("Ledger path config", "intentionally differs from default")
		} else if defaultExists && configuredExists {
			// both exist - this is unusual, warn
			fmt.Println("  Both paths exist. This may indicate duplicate ledgers.")
			fmt.Println("  Manual review recommended.")
			return WarningCheck("Ledger path config", "both paths exist",
				"Review and remove duplicate ledger, then update config")
		} else {
			// neither exists - offer to update config to default (for future clone)
			if cli.ConfirmYesNo("Neither path exists. Update config to use default path?", true) {
				localCfg.Ledger.Path = defaultPath
				if err := config.SaveLocalConfig(gitRoot, localCfg); err != nil {
					return FailedCheck("Ledger path config", "save failed", err.Error())
				}
				return PassedCheck("Ledger path config", "updated to default path")
			}
		}
		return WarningCheck("Ledger path config", "mismatch not resolved",
			"Run `ox doctor --fix ledger-path-mismatch` to update")
	}

	// not fixing - report the mismatch
	var detail string
	if defaultExists && !configuredExists {
		detail = fmt.Sprintf("Config points to %s (missing), but ledger exists at %s. Run `ox doctor --fix ledger-path-mismatch` to update",
			configuredPath, defaultPath)
	} else if configuredExists && !defaultExists {
		detail = fmt.Sprintf("Config path differs from computed default. This may be intentional. Default would be: %s", defaultPath)
		return InfoCheck("Ledger path config", "differs from default", detail)
	} else if defaultExists && configuredExists {
		detail = fmt.Sprintf("Both paths exist - possible duplicate ledgers. Config: %s, Default: %s",
			configuredPath, defaultPath)
	} else {
		detail = fmt.Sprintf("Neither path exists. Config: %s, Default: %s. Run `ox doctor --fix ledger-path-mismatch` to update",
			configuredPath, defaultPath)
	}

	return WarningCheck("Ledger path config", "path mismatch", detail)
}


// getTeamURLFromCredentials reads a team context URL from locally saved git credentials.
// Matches by repo name (team slug). This avoids a duplicate API call.
func getTeamURLFromCredentials(teamName string) string {
	gitRoot := findGitRoot()
	projectEndpoint := endpoint.GetForProject(gitRoot)
	if projectEndpoint == "" {
		projectEndpoint = endpoint.Get()
	}

	creds, err := gitserver.LoadCredentialsForEndpoint(projectEndpoint)
	if err != nil || creds == nil {
		return ""
	}

	// try exact name match first
	if repo, ok := creds.Repos[teamName]; ok {
		return repo.URL
	}

	// try matching by type=team-context and partial name match
	for _, repo := range creds.Repos {
		if repo.Type == "team-context" && strings.Contains(repo.Name, teamName) {
			return repo.URL
		}
	}
	return ""
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

// bootstrapGracePeriod is the window after ox init during which missing repos
// are expected (daemon is still cloning). Chosen to be longer than typical clone
// time for small team context repos on a fast connection.
// Caveat: mtime-based detection is unreliable on NFS and CI cache-restore scenarios
// where file timestamps may not reflect actual write time.
const bootstrapGracePeriod = 5 * time.Minute

// isRecentlyInitialized checks if the project was initialized within the grace period.
// Uses config.json modification time as a proxy for init time.
func isRecentlyInitialized(gitRoot string) bool {
	configPath := filepath.Join(gitRoot, ".sageox", "config.json")
	info, err := os.Stat(configPath)
	if err != nil {
		return false
	}
	return time.Since(info.ModTime()) < bootstrapGracePeriod
}
