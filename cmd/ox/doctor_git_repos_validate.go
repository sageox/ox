package main

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/gitserver"
	"github.com/sageox/ox/internal/paths"
)

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

// isValidGitURL performs basic validation of a git remote URL.
func isValidGitURL(u string) bool {
	if u == "" {
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
		if strings.HasPrefix(u, prefix) {
			return true
		}
	}
	return false
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
	return time.Since(info.ModTime().UTC()) < bootstrapGracePeriod
}
