package main

import (
	"context"
	"errors"
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

	// offline-safe: auth checks are irrelevant for local-only repos with no remotes
	urls, _ := repotools.GetRemoteURLs()
	if len(urls) == 0 {
		return SkippedCheck("Git auth", "no remotes configured (local-only repo)", "")
	}

	// check if credential helper is configured
	credHelper := getGitConfigValue("credential.helper")
	if credHelper != "" {
		return PassedCheck("Git auth", credHelper)
	}

	// no credential helper - check if SSH key exists as fallback
	if checkSSHAuth() {
		return PassedCheck("Git auth", "SSH configured")
	}

	// check if ox has its own PAT-based credentials for this project
	if config.IsInitialized(gitRoot) {
		projectEndpoint := endpoint.GetForProject(gitRoot)
		if projectEndpoint == "" {
			projectEndpoint = endpoint.Get()
		}
		creds, err := gitserver.LoadCredentialsForEndpoint(projectEndpoint)
		if err == nil && creds != nil && !creds.IsExpired() {
			return PassedCheck("Git auth", "SageOx credentials")
		}
	}

	return WarningCheck("Git auth", "no credential helper",
		"Configure git credentials for remote operations")
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
// offline-safe: skipped when no origin remote exists (local-only project repo)
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
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	lsCmd := exec.CommandContext(ctx, "git", "ls-remote", "--exit-code", "-q", "origin", "HEAD")
	lsCmd.Dir = gitRoot

	err = lsCmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return WarningCheck("Git connectivity", "timeout",
			"Remote did not respond within 5s")
	}
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
// Distinguishes staged (ready to commit, informational) from unstaged (needs action, warning).
func checkGitRepoState() checkResult {
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return SkippedCheck("Repo state", "not in git repo", "")
	}

	// only check for uncommitted changes under .sageox/
	statusCmd := exec.Command("git", "status", "--porcelain", ".sageox/")
	statusCmd.Dir = gitRoot
	output, err := statusCmd.Output()
	if err == nil && len(output) > 0 {
		lines := strings.Split(strings.TrimSpace(string(output)), "\n")
		hasUnstaged := false
		count := 0
		for _, line := range lines {
			if line == "" {
				continue
			}
			count++
			// porcelain format: XY filename (X=index, Y=worktree)
			if len(line) >= 2 && line[1] != ' ' {
				hasUnstaged = true
			}
		}
		if count > 0 {
			if hasUnstaged {
				return WarningCheck("Repo state",
					fmt.Sprintf("%d uncommitted change(s) in .sageox/", count),
					"Run 'git add .sageox/ && git commit' to persist config")
			}
			// all changes are staged — informational, expected after init
			return InfoCheck("Repo state",
				fmt.Sprintf("%d staged change(s) in .sageox/", count),
				"Run 'git commit' to persist config")
		}
	}

	return PassedCheck("Repo state", "committed and up to date")
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
		// offline-safe: local-only repos have no remotes; this is a valid configuration
		return InfoCheck("Git remotes", "no remotes configured",
			"Local-only repo (no remote configured)")
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
	oneHourAgo := time.Now().UTC().Add(-1 * time.Hour)

	for _, lock := range lockFiles {
		path := filepath.Join(gitDir, lock)
		if info, err := os.Stat(path); err == nil {
			found = append(found, lock)
			if info.ModTime().UTC().Before(oneHourAgo) {
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
				if authenticated, _ := auth.IsAuthenticatedForEndpoint(endpoint.GetForProject(gitRoot)); authenticated {
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

	// during the post-init bootstrap window, the daemon is still cloning repos,
	// so any of these states is a transient artifact of the clone in progress —
	// not a user-actionable problem. downgrade to info so a fresh install doesn't
	// surface scary errors. a partially-created or stale directory at the default
	// path counts the same as no directory at all for grace purposes.
	allTransient := true
	for _, issue := range issues {
		switch issue.issue {
		case "missing", "empty-dir", "not-git-repo":
			// transient — clone in progress or pre-clone state
		default:
			allTransient = false
		}
		if !allTransient {
			break
		}
	}
	if allTransient && isRecentlyInitialized(gitRoot) {
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

	// check if .sageox directory exists in the ledger checkout
	sageoxDir := filepath.Join(ledgerPath, ".sageox")
	if _, err := os.Stat(sageoxDir); os.IsNotExist(err) {
		return SkippedCheck("Ledger checkout .gitignore", "no .sageox in ledger", "")
	}

	// Use EnsureCheckoutGitignore to check and fix in one call.
	// It handles: missing file, missing entries, writing, and committing.
	// Without cache/ in the gitignore, daemon-written files appear as
	// untracked in git status and permanently block blue-green GC reclone.
	if !gitserver.CheckoutGitignoreNeedsFix(ledgerPath) {
		return PassedCheck("Ledger checkout .gitignore", "all entries present")
	}

	if fix {
		if err := gitserver.EnsureCheckoutGitignore(ledgerPath); err != nil {
			return FailedCheck("Ledger checkout .gitignore", "fix failed", err.Error())
		}
		return PassedCheck("Ledger checkout .gitignore", "updated")
	}

	return checkResult{
		name:    "Ledger checkout .gitignore",
		passed:  false,
		message: "missing or incomplete",
		detail:  "Run `ox doctor --fix` or `ox doctor --fix gitignore-missing` to fix",
	}
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

		// Use EnsureCheckoutGitignore to check and fix in one call.
		// It handles: missing file, missing entries, writing, and committing.
		// Without cache/ in the gitignore, daemon-written files like
		// cache/sync-state.json appear as untracked in git status and
		// permanently block blue-green GC reclone.
		needsFix := gitserver.CheckoutGitignoreNeedsFix(tc.Path)
		if needsFix {
			if fix {
				if err := gitserver.EnsureCheckoutGitignore(tc.Path); err != nil {
					slog.Warn("failed to fix .sageox/.gitignore in team context",
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

// checkTeamContextCloneStrategy detects team context clones that are full clones
// (not partial). Partial clones use --filter=blob:none which sets
// extensions.partialClone in the git config. Full clones download all blobs upfront
// and waste disk/bandwidth. This is informational only — blue-green reclone will
// eventually replace full clones with partial ones.
func checkTeamContextCloneStrategy() []checkResult {
	var results []checkResult

	gitRoot := findGitRoot()
	if gitRoot == "" {
		return results
	}

	localCfg, err := config.LoadLocalConfig(gitRoot)
	if err != nil || localCfg == nil {
		return results
	}

	for _, tc := range localCfg.TeamContexts {
		if tc.Path == "" || !isGitRepo(tc.Path) {
			continue
		}

		cmd := exec.Command("git", "-C", tc.Path, "config", "--get", "extensions.partialClone")
		output, err := cmd.Output()

		name := fmt.Sprintf("Team %s clone strategy", tc.TeamName)

		if err != nil || strings.TrimSpace(string(output)) == "" {
			results = append(results, InfoCheck(name,
				"full clone (not partial)",
				"Will be upgraded to partial clone on next reclone"))
		} else {
			results = append(results, PassedCheck(name, "partial clone"))
		}
	}

	return results
}
