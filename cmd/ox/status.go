package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	lipgloss "charm.land/lipgloss/v2"
	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/daemon"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/gitserver"
	"github.com/sageox/ox/internal/ledger"
	"github.com/sageox/ox/internal/paths"
	"github.com/sageox/ox/internal/tips"
	"github.com/spf13/cobra"
)

// statusJSONOutput is the JSON output structure for ox status --json
type statusJSONOutput struct {
	Auth         *statusAuthJSON         `json:"auth"`
	Config       *statusConfigJSON       `json:"config"`
	Project      *statusProjectJSON      `json:"project"`
	Ledger       *statusLedgerJSON       `json:"ledger,omitempty"`
	TeamContexts []statusTeamContextJSON `json:"team_contexts,omitempty"`
	Daemon       *statusDaemonJSON       `json:"daemon,omitempty"`
}

type statusAuthJSON struct {
	Authenticated bool       `json:"authenticated"`
	Endpoint      string     `json:"endpoint"`
	User          string     `json:"user,omitempty"`
	Email         string     `json:"email,omitempty"`
	ExpiresAt     *time.Time `json:"expires_at,omitempty"`
}

type statusConfigJSON struct {
	UserConfigDir  string `json:"user_config_dir"`
	AuthFile       string `json:"auth_file"`
	AuthFileExists bool   `json:"auth_file_exists"`
}

type statusProjectJSON struct {
	Initialized bool   `json:"initialized"`
	Directory   string `json:"directory"`
	ConfigPath  string `json:"config_path,omitempty"`
}

type statusLedgerJSON struct {
	Configured  bool   `json:"configured"`
	Path        string `json:"path,omitempty"`
	Exists      bool   `json:"exists"`
	Branch      string `json:"branch,omitempty"`
	Status      string `json:"status,omitempty"`
	Error       string `json:"error,omitempty"`
	Visibility  string `json:"visibility,omitempty"`
	AccessLevel string `json:"access_level,omitempty"`
}

type statusTeamContextJSON struct {
	TeamID   string `json:"team_id"`
	TeamName string `json:"team_name,omitempty"`
	Path     string `json:"path"`
	Exists   bool   `json:"exists"`
	Branch   string `json:"branch,omitempty"`
	Status   string `json:"status,omitempty"`
	Error    string `json:"error,omitempty"`
}

type statusDaemonJSON struct {
	Running       bool   `json:"running"`
	Pid           int    `json:"pid,omitempty"`
	UptimeSeconds int64  `json:"uptime_seconds,omitempty"`
	TotalSyncs    int    `json:"total_syncs,omitempty"`
	SyncsLastHour int    `json:"syncs_last_hour,omitempty"`
	LastError     string `json:"last_error,omitempty"`
}

var statusJSONFlag bool

func init() {
	statusCmd.Flags().BoolVar(&statusJSONFlag, "json", false, "output in JSON format")
}

// status command styles - Tufte-inspired minimal design with brand colors
var (
	// section headers - title case, sage green, bold
	statusHeaderStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(cli.ColorPrimary)

	// content labels - left column, subtle gray, fixed width
	statusLabelStyle = lipgloss.NewStyle().
				Foreground(cli.ColorDim).
				Width(20)

	// default values - clean white
	statusValueStyle = lipgloss.NewStyle()

	// success values - sage green (logged in, yes, initialized)
	statusSuccessStyle = lipgloss.NewStyle().
				Foreground(cli.ColorSuccess)

	// error/negative values - muted red (not logged in, no, not initialized)
	statusErrorStyle = lipgloss.NewStyle().
				Foreground(cli.ColorError)

	// muted values - subtle gray (IDs, paths, technical details)
	statusMutedStyle = lipgloss.NewStyle().
				Foreground(cli.ColorDim)

	// highlighted values - warm gold (user identity, tier, important data)
	statusHighlightStyle = lipgloss.NewStyle().
				Foreground(cli.ColorSecondary)

	// warning values - yellow/amber
	statusWarningStyle = lipgloss.NewStyle().
				Foreground(cli.ColorWarning)

	// visibility values - semantic colors from sageox-mono design tokens
	statusPublicStyle = lipgloss.NewStyle().
				Foreground(cli.ColorPublic)

	statusPrivateStyle = lipgloss.NewStyle().
				Foreground(cli.ColorPrivate)
)

// inferSemantic auto-detects value semantic type from context
func inferSemantic(label, value string) string {
	valueLower := strings.ToLower(value)
	labelLower := strings.ToLower(label)

	// success indicators
	if valueLower == "logged in" || valueLower == "yes" ||
		valueLower == "initialized" || valueLower == "enabled" ||
		valueLower == "true" {
		return "success"
	}

	// error/negative indicators
	if valueLower == "not logged in" || valueLower == "no" ||
		valueLower == "not initialized" || valueLower == "none" ||
		valueLower == "disabled" || valueLower == "false" {
		return "error"
	}

	// highlight important user identity data in gold
	if labelLower == "user" || labelLower == "email" {
		return "highlight"
	}

	// muted for technical details (IDs, paths, directories)
	if strings.Contains(labelLower, "id") ||
		strings.Contains(labelLower, "path") ||
		strings.Contains(labelLower, "directory") ||
		strings.Contains(labelLower, "file") ||
		strings.Contains(labelLower, "expires") {
		return "muted"
	}

	return "default"
}

// formatValue applies semantic styling to a value
func formatValue(value string, semantic string) string {
	switch semantic {
	case "success":
		return statusSuccessStyle.Render("✓ " + value)
	case "error":
		return statusErrorStyle.Render("✗ " + value)
	case "warning":
		return statusWarningStyle.Render("⚠ " + value)
	case "highlight":
		return statusHighlightStyle.Render(value)
	case "muted":
		return statusMutedStyle.Render(value)
	default:
		return statusValueStyle.Render(value)
	}
}

// renderVisibility applies semantic color to a visibility value
func renderVisibility(visibility string) string {
	switch strings.ToLower(visibility) {
	case "public":
		return statusPublicStyle.Render(visibility)
	case "private":
		return statusPrivateStyle.Render(visibility)
	default:
		return statusValueStyle.Render(visibility)
	}
}

// renderVisibilityWithAccess renders "private (✓ member)" or "public (read-only)" on one line.
func renderVisibilityWithAccess(visibility, accessLevel string) string {
	s := renderVisibility(visibility)
	if accessLevel == "viewer" {
		s += statusMutedStyle.Render(" (read-only)")
	} else if accessLevel != "" {
		s += statusMutedStyle.Render(fmt.Sprintf(" (✓ %s)", accessLevel))
	}
	return s
}

// renderTable renders a section with a header and key-value rows
// Tufte-inspired: minimal ink, let data speak, subtle hierarchy
func renderTable(header string, rows [][]string) string {
	var b strings.Builder

	b.WriteString("\n")

	// title case header with subtle underline (matches help style)
	b.WriteString(statusHeaderStyle.Render(header))
	b.WriteString("\n")
	underline := strings.Repeat("─", len(header))
	b.WriteString(statusMutedStyle.Render(underline))
	b.WriteString("\n")

	for _, row := range rows {
		label := row[0]
		value := row[1]

		// determine semantic type (optional third element or auto-detect)
		semantic := "default"
		if len(row) > 2 {
			semantic = row[2]
		} else {
			semantic = inferSemantic(label, value)
		}

		b.WriteString(statusLabelStyle.Render(label))
		b.WriteString(formatValue(value, semantic))
		b.WriteString("\n")
	}

	return b.String()
}

// gitRepoStatus holds information about a git repository's status
type gitRepoStatus struct {
	Path             string
	Exists           bool
	Branch           string
	UncommittedCount int
	IsSynced         bool
	HasLastSync      bool
	LastSync         time.Time
	BehindCount      int
	Error            string
}

// getGitRepoStatus checks the status of a git repository at the given path.
// Returns status info including branch, uncommitted changes, and sync state.
func getGitRepoStatus(repoPath string, lastSync time.Time, hasLastSync bool) gitRepoStatus {
	status := gitRepoStatus{
		Path:        repoPath,
		LastSync:    lastSync,
		HasLastSync: hasLastSync,
	}

	// check if path exists
	if _, err := os.Stat(repoPath); os.IsNotExist(err) {
		status.Exists = false
		status.Error = "not found"
		return status
	}
	status.Exists = true

	// check if it's a git repo
	if !isGitRepo(repoPath) {
		status.Error = "not a git repo"
		return status
	}

	// get current branch (rev-parse fails on empty repos with no commits)
	branchCmd := exec.Command("git", "-C", repoPath, "rev-parse", "--abbrev-ref", "HEAD")
	branchOutput, err := branchCmd.Output()
	if err != nil {
		// empty repo (cloned but no commits yet) — not an error
		status.Branch = "(empty)"
		return status
	}
	status.Branch = strings.TrimSpace(string(branchOutput))

	// get uncommitted changes count
	statusCmd := exec.Command("git", "-C", repoPath, "status", "--porcelain")
	statusOutput, err := statusCmd.Output()
	if err == nil && len(statusOutput) > 0 {
		lines := strings.Split(strings.TrimSpace(string(statusOutput)), "\n")
		for _, line := range lines {
			if line != "" {
				status.UncommittedCount++
			}
		}
	}

	// check if synced with remote (only if uncommitted count is 0)
	if status.UncommittedCount == 0 {
		status.IsSynced = true
	}

	return status
}

// formatGitRepoStatus formats the git repo status for display
func formatGitRepoStatus(status gitRepoStatus) (string, string) {
	if !status.Exists {
		return "not found", "error"
	}

	if status.Error != "" {
		return status.Error, "error"
	}

	var parts []string

	if status.UncommittedCount > 0 {
		parts = append(parts, fmt.Sprintf("%d uncommitted", status.UncommittedCount))
	} else {
		parts = append(parts, "synced")
	}

	result := strings.Join(parts, ", ")

	if status.HasLastSync {
		result += fmt.Sprintf(" (%s)", formatTimeAgo(status.LastSync))
	}

	if status.UncommittedCount > 0 {
		return result, "warning"
	}
	return result, "success"
}

// formatTimeAgo formats a time as a human-readable relative time
func formatTimeAgo(t time.Time) string {
	now := time.Now()
	diff := now.Sub(t)

	switch {
	case diff < time.Minute:
		return "just now"
	case diff < time.Hour:
		mins := int(diff.Minutes())
		if mins == 1 {
			return "1 minute ago"
		}
		return fmt.Sprintf("%d minutes ago", mins)
	case diff < 24*time.Hour:
		hours := int(diff.Hours())
		if hours == 1 {
			return "1 hour ago"
		}
		return fmt.Sprintf("%d hours ago", hours)
	case diff < 7*24*time.Hour:
		days := int(diff.Hours() / 24)
		if days == 1 {
			return "1 day ago"
		}
		return fmt.Sprintf("%d days ago", days)
	default:
		weeks := int(diff.Hours() / 24 / 7)
		if weeks == 1 {
			return "1 week ago"
		}
		return fmt.Sprintf("%d weeks ago", weeks)
	}
}

// formatEndpointDisplay returns a shorter display name for an endpoint URL.
// e.g., "https://api.test.sageox.ai" -> "api.test.sageox.ai"
func formatEndpointDisplay(endpointURL string) string {
	if endpointURL == "" {
		return "(default)"
	}
	// strip protocol prefix for cleaner display
	endpointURL = strings.TrimPrefix(endpointURL, "https://")
	endpointURL = strings.TrimPrefix(endpointURL, "http://")
	return endpointURL
}

// getGitRemoteURL returns the origin remote URL for a git repo.
// Returns empty string on error or if remote doesn't exist.
func getGitRemoteURL(repoPath string) string {
	if repoPath == "" {
		return ""
	}
	cmd := exec.Command("git", "-C", repoPath, "remote", "get-url", "origin")
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

// extractGitHost extracts the hostname from a git clone URL.
// Handles both HTTPS (https://git.example.com/...) and SSH (git@git.example.com:...) URLs.
// Returns empty string if parsing fails.
func extractGitHost(cloneURL string) string {
	if cloneURL == "" {
		return ""
	}

	// handle SSH URLs (git@host:path)
	if strings.Contains(cloneURL, "@") && !strings.Contains(cloneURL, "://") {
		// git@git.example.com:user/repo.git -> git.example.com
		parts := strings.SplitN(cloneURL, "@", 2)
		if len(parts) == 2 {
			hostPart := strings.SplitN(parts[1], ":", 2)
			if len(hostPart) >= 1 {
				return hostPart[0]
			}
		}
		return ""
	}

	// handle HTTPS URLs
	cloneURL = strings.TrimPrefix(cloneURL, "https://")
	cloneURL = strings.TrimPrefix(cloneURL, "http://")

	// remove credentials if present (oauth2:token@host)
	if idx := strings.Index(cloneURL, "@"); idx != -1 {
		cloneURL = cloneURL[idx+1:]
	}

	// extract host (before first /)
	if idx := strings.Index(cloneURL, "/"); idx != -1 {
		return cloneURL[:idx]
	}
	return cloneURL
}

// isGitRepo checks if the given path is inside a git repository.
// Uses git command to properly handle worktrees and bare repos
// (checking for .git directory fails when .git is a file pointing elsewhere).
func isGitRepo(path string) bool {
	cmd := exec.Command("git", "-C", path, "rev-parse", "--is-inside-work-tree")
	err := cmd.Run()
	return err == nil
}

// getLedgerRemoteURL fetches the ledger git URL from the cloud API.
// Returns empty string if not available or on error.
// Designed to be fast - silently returns empty on any failure.
//
// Note: RepoClient uses http.Client internally which manages connection
// pooling automatically. No explicit Close() is needed - connections are
// reused across requests and cleaned up when idle.
func getLedgerRemoteURL(ep string) string {
	token, err := auth.GetTokenForEndpoint(ep)
	if err != nil || token == nil || token.AccessToken == "" {
		return ""
	}

	client := api.NewRepoClientWithEndpoint(ep).WithAuthToken(token.AccessToken)
	repos, err := client.GetRepos()
	if err != nil || repos == nil {
		return ""
	}

	// find the ledger repo
	for _, repo := range repos.Repos {
		if repo.Type == "ledger" {
			return repo.URL
		}
	}
	return ""
}

// getTeamContextRemoteURL fetches the team context git URL from the cloud API.
// Returns empty string if not available or on error.
// Designed to be fast - silently returns empty on any failure.
func getTeamContextRemoteURL(teamID, ep string) string {
	if teamID == "" {
		return ""
	}

	token, err := auth.GetTokenForEndpoint(ep)
	if err != nil || token == nil || token.AccessToken == "" {
		return ""
	}

	client := api.NewRepoClientWithEndpoint(ep).WithAuthToken(token.AccessToken)
	teamInfo, err := client.GetTeamInfo(teamID)
	if err != nil || teamInfo == nil {
		return ""
	}

	return teamInfo.RepoURL
}

// fetchRemoteURLs fetches remote URLs from the cloud API for ledger and team contexts
func fetchRemoteURLs(client *api.RepoClient, teamContexts []config.TeamContext) (ledgerURL string, teamURLs map[string]string) {
	teamURLs = make(map[string]string)

	// fetch repos for ledger URL
	repos, err := client.GetRepos()
	if err == nil && repos != nil {
		for _, repo := range repos.Repos {
			if repo.Type == "ledger" {
				ledgerURL = repo.URL
				break
			}
		}
	}

	// fetch team context URLs
	for _, tc := range teamContexts {
		if tc.TeamID == "" {
			continue
		}
		teamInfo, err := client.GetTeamInfo(tc.TeamID)
		if err == nil && teamInfo != nil && teamInfo.RepoURL != "" {
			teamURLs[tc.TeamID] = teamInfo.RepoURL
		}
	}

	return ledgerURL, teamURLs
}

// renderGitReposSection renders the git repositories status section
// Shows ledger and team contexts grouped by endpoint
// Always renders both sections, showing "(none)" if not configured
func renderGitReposSection(localCfg *config.LocalConfig, projectRoot string, daemonStatus *daemon.StatusData) string {
	var b strings.Builder

	hasLedger := localCfg != nil && localCfg.Ledger != nil && localCfg.Ledger.Path != ""

	// load project config for endpoint and repo_id (needed for path computation and API calls)
	var projectEndpoint string
	var projectCfg *config.ProjectConfig
	if projectRoot != "" {
		if loadedCfg, err := config.LoadProjectConfig(projectRoot); err == nil && loadedCfg != nil {
			projectCfg = loadedCfg
			projectEndpoint = projectCfg.GetEndpoint()
		}
	}
	// fall back to global endpoint if no project config
	if projectEndpoint == "" {
		projectEndpoint = endpoint.Get()
	}

	// fetch ALL repos from cloud API (not just configured ones)
	// this shows user what repos they SHOULD have access to
	var cloudRepos *api.ReposResponse
	var cloudLedgerURL string
	var cloudTeamContexts []api.RepoInfo

	// use project endpoint for auth check and API calls (not global default)
	// this ensures we query the correct endpoint when logged into multiple
	authenticated, _ := auth.IsAuthenticatedForEndpoint(projectEndpoint)

	// track ledger status from dedicated endpoint
	var ledgerStatus *api.LedgerStatusResponse
	var ledgerStatusErr error
	var userEmail string

	// repo detail for visibility/access info (works for both members and non-members)
	var repoDetail *api.RepoDetailResponse

	if authenticated {
		token, err := auth.GetTokenForEndpoint(projectEndpoint)
		if err == nil && token != nil {
			userEmail = token.UserInfo.Email
			client := api.NewRepoClientWithEndpoint(projectEndpoint).WithAuthToken(token.AccessToken)

			// fetch repo detail for visibility/access info
			if projectCfg != nil && projectCfg.RepoID != "" {
				repoDetail, _ = client.GetRepoDetail(projectCfg.RepoID)
			}

			// fetch repos for team contexts
			cloudRepos, err = client.GetRepos()
			if err == nil && cloudRepos != nil {
				// categorize cloud repos
				for _, repo := range cloudRepos.Repos {
					switch repo.Type {
					case "ledger":
						cloudLedgerURL = repo.URL
					case "team-context":
						cloudTeamContexts = append(cloudTeamContexts, repo)
					}
				}
			}

			// fetch ledger status from dedicated endpoint (source of truth for provisioning)
			if projectCfg != nil && projectCfg.RepoID != "" {
				ledgerStatus, ledgerStatusErr = client.GetLedgerStatus(projectCfg.RepoID)
				// if ledger is ready and we don't have URL from repos, use the one from status
				if ledgerStatus != nil && ledgerStatus.Status == "ready" && cloudLedgerURL == "" {
					cloudLedgerURL = ledgerStatus.RepoURL
				}
			}
		}
	}

	// Ledger - no sub-header, rendered under Project Status
	b.WriteString("\n")
	if projectRoot == "" {
		// not in a git repo — ledgers are per-project
		b.WriteString(statusLabelStyle.Render("Status"))
		b.WriteString(statusMutedStyle.Render("n/a (not in a git repo)"))
		b.WriteString("\n")
	} else if hasLedger {
		status := getGitRepoStatus(localCfg.Ledger.Path, localCfg.Ledger.LastSync, localCfg.Ledger.HasLastSync())

		repoID := ""
		if projectCfg != nil {
			repoID = projectCfg.RepoID
		}
		if repoID == "" {
			repoID = "(not registered)"
		}
		b.WriteString(statusLabelStyle.Render("Ledger"))
		b.WriteString(statusMutedStyle.Render(repoID))
		b.WriteString("\n")

		// show visibility and access level if available
		visibility := ""
		accessLevel := ""
		if repoDetail != nil {
			visibility = repoDetail.Visibility
			accessLevel = repoDetail.AccessLevel
		} else if ledgerStatus != nil {
			visibility = ledgerStatus.Visibility
			accessLevel = ledgerStatus.AccessLevel
		}
		if visibility != "" {
			b.WriteString(statusLabelStyle.Render("  Visibility"))
			b.WriteString(renderVisibilityWithAccess(visibility, accessLevel))
			b.WriteString("\n")
		} else if !authenticated {
			b.WriteString(statusLabelStyle.Render("  Visibility"))
			b.WriteString(formatValue("not authenticated", "warning"))
			b.WriteString("\n")
		}

		b.WriteString(statusLabelStyle.Render("  Path"))
		b.WriteString(statusMutedStyle.Render(localCfg.Ledger.Path))
		b.WriteString("\n")

		// check if ledger doesn't exist locally and user doesn't have access (ErrLedgerNotFound)
		ledgerNotAccessible := !status.Exists && errors.Is(ledgerStatusErr, api.ErrLedgerNotFound)

		// status line (indented)
		if ledgerNotAccessible {
			// user is logged in with a different account that doesn't have access
			b.WriteString(statusLabelStyle.Render("  Status"))
			b.WriteString(statusMutedStyle.Render("not accessible"))
			b.WriteString("\n")
			b.WriteString(statusLabelStyle.Render(""))
			if userEmail != "" {
				b.WriteString(statusMutedStyle.Render(fmt.Sprintf("Logged in as %s (no access to this ledger)", userEmail)))
			} else {
				b.WriteString(statusMutedStyle.Render("Current account has no access to this ledger"))
			}
			b.WriteString("\n")
		} else {
			statusText, semantic := formatGitRepoStatus(status)
			if accessLevel == "viewer" {
				statusText += " (read-only)"
			}
			b.WriteString(statusLabelStyle.Render("  Status"))
			b.WriteString(formatValue(statusText, semantic))
			b.WriteString("\n")

			// hint for missing repo
			if !status.Exists {
				b.WriteString(statusLabelStyle.Render(""))
				b.WriteString(statusMutedStyle.Render("Run 'ox doctor --fix' to re-clone"))
				b.WriteString("\n")
			}
		}
	} else if cloudLedgerURL != "" {
		// cloud has ledger but local doesn't - show as "not cloned" with expected path
		expectedPath, _ := ledger.DefaultPath()
		if expectedPath == "" {
			expectedPath = "(default location)"
		}
		b.WriteString(statusLabelStyle.Render("Status"))
		if isDaemonBootstrapping(daemonStatus) {
			b.WriteString(statusMutedStyle.Render("⟳ setting up..."))
		} else {
			b.WriteString(formatValue("not cloned", "warning"))
		}
		b.WriteString("\n")
		b.WriteString(statusLabelStyle.Render("  Path"))
		b.WriteString(statusMutedStyle.Render(expectedPath))
		b.WriteString("\n")
		b.WriteString(statusLabelStyle.Render("  Remote"))
		b.WriteString(statusMutedStyle.Render(cloudLedgerURL))
		b.WriteString("\n")
		b.WriteString(statusLabelStyle.Render(""))
		if isDaemonBootstrapping(daemonStatus) {
			b.WriteString(statusMutedStyle.Render("Initial clone in progress"))
		} else {
			b.WriteString(statusMutedStyle.Render("Run 'ox doctor --fix' to clone"))
		}
		b.WriteString("\n")
	} else if authenticated {
		// authenticated but cloud has no ledger URL yet - show status from dedicated endpoint
		var statusMsg, detailMsg string
		semantic := "warning"

		// check if access was denied (user not a member of this team/repo)
		isAccessDenied := ledgerStatusErr != nil && (errors.Is(ledgerStatusErr, api.ErrForbidden) ||
			strings.Contains(ledgerStatusErr.Error(), "access denied") ||
			strings.Contains(ledgerStatusErr.Error(), "not a member"))

		if isAccessDenied {
			// user doesn't have access to this ledger
			statusMsg = "not accessible"
			semantic = "warning"
			if userEmail != "" {
				detailMsg = fmt.Sprintf("Logged in as %s — you don't have access to this ledger", userEmail)
			} else {
				detailMsg = "Your account doesn't have access to this ledger"
			}
		} else if ledgerStatus != nil {
			switch ledgerStatus.Status {
			case "ready":
				statusMsg = "ready (not cloned locally)"
				detailMsg = "Ledger is provisioned but not cloned locally"
			case "pending":
				statusMsg = "provisioning..."
				detailMsg = "Ledger is being provisioned by the cloud"
				if ledgerStatus.Message != "" {
					detailMsg = ledgerStatus.Message
				}
			case "error":
				statusMsg = "provisioning failed"
				detailMsg = "Error: " + ledgerStatus.Message
				semantic = "error"
			default:
				statusMsg = ledgerStatus.Status
				detailMsg = ledgerStatus.Message
			}
		} else {
			statusMsg = "not configured"
			detailMsg = "No ledger provisioned for this project"
		}

		b.WriteString(statusLabelStyle.Render("Status"))
		b.WriteString(formatValue(statusMsg, semantic))
		b.WriteString("\n")
		if detailMsg != "" {
			b.WriteString(statusLabelStyle.Render(""))
			b.WriteString(statusMutedStyle.Render(detailMsg))
			b.WriteString("\n")
		}

		// show where ledger will be once provisioned
		if projectRoot != "" && ledgerStatus != nil && ledgerStatus.Status != "ready" {
			repoName := filepath.Base(projectRoot)
			siblingDir := config.DefaultSageoxSiblingDir(repoName, projectRoot)
			endpointSlug := endpoint.NormalizeSlug(projectEndpoint)
			if siblingDir != "" {
				b.WriteString(statusLabelStyle.Render(""))
				b.WriteString(statusMutedStyle.Render("Will be at:"))
				b.WriteString("\n")
				b.WriteString(statusLabelStyle.Render(""))
				b.WriteString(statusMutedStyle.Render("../" + filepath.Base(siblingDir) + "/"))
				b.WriteString("\n")
				b.WriteString(statusLabelStyle.Render(""))
				b.WriteString(statusMutedStyle.Render("└── " + endpointSlug + "/"))
				b.WriteString("\n")
				b.WriteString(statusLabelStyle.Render(""))
				b.WriteString(statusMutedStyle.Render("    └── ledger/"))
				b.WriteString("\n")
			}
		}
	} else {
		// not authenticated
		b.WriteString(statusLabelStyle.Render("Status"))
		b.WriteString(formatValue("none", "error"))
		b.WriteString("\n")
	}

	// build lookup for visibility/access from repo detail API
	teamDetail := make(map[string]api.RepoDetailTeamContext)
	if repoDetail != nil {
		for _, tc := range repoDetail.TeamContexts {
			teamDetail[tc.StableID()] = tc
		}
	}

	// partition team contexts into repo TC vs other TCs
	repoTeamID := ""
	if projectCfg != nil {
		repoTeamID = projectCfg.TeamID
	}

	type cloudTCEntry struct {
		info api.RepoInfo
	}
	type detailTCEntry struct {
		info api.RepoDetailTeamContext
	}

	var repoCloudTC *cloudTCEntry
	var otherCloudTCs []cloudTCEntry
	for _, cloudTC := range cloudTeamContexts {
		if repoTeamID != "" && cloudTC.StableID() == repoTeamID {
			repoCloudTC = &cloudTCEntry{info: cloudTC}
		} else {
			otherCloudTCs = append(otherCloudTCs, cloudTCEntry{info: cloudTC})
		}
	}

	var repoDetailTC *detailTCEntry
	var otherDetailTCs []detailTCEntry
	if repoDetail != nil {
		for _, dtc := range repoDetail.TeamContexts {
			if repoTeamID != "" && dtc.StableID() == repoTeamID {
				if repoCloudTC == nil {
					repoDetailTC = &detailTCEntry{info: dtc}
				}
			} else {
				otherDetailTCs = append(otherDetailTCs, detailTCEntry{info: dtc})
			}
		}
	}

	// helper: render a single cloud team context entry
	renderedTeams := make(map[string]bool)
	renderCloudTC := func(cloudTC api.RepoInfo) {
		expectedPath := paths.TeamContextDir(cloudTC.StableID(), projectEndpoint)
		if renderedTeams[expectedPath] {
			return
		}
		renderedTeams[expectedPath] = true

		b.WriteString(statusLabelStyle.Render("Team"))
		b.WriteString(statusValueStyle.Render(cloudTC.Name))
		b.WriteString("\n")

		visibility := "private"
		accessLevel := "member"
		if detail, ok := teamDetail[cloudTC.StableID()]; ok {
			if detail.Visibility != "" {
				visibility = detail.Visibility
			}
			if detail.AccessLevel != "" {
				accessLevel = detail.AccessLevel
			}
		}
		b.WriteString(statusLabelStyle.Render("  Visibility"))
		b.WriteString(renderVisibilityWithAccess(visibility, accessLevel))
		b.WriteString("\n")

		b.WriteString(statusLabelStyle.Render("  Path"))
		b.WriteString(statusMutedStyle.Render(expectedPath))
		b.WriteString("\n")

		gitDir := filepath.Join(expectedPath, ".git")
		if _, err := os.Stat(gitDir); err == nil {
			status := getGitRepoStatus(expectedPath, time.Time{}, false)
			if status.Error != "" {
				b.WriteString(statusLabelStyle.Render("  Status"))
				b.WriteString(formatValue(status.Error, "error"))
			} else if status.UncommittedCount > 0 {
				b.WriteString(statusLabelStyle.Render("  Status"))
				b.WriteString(formatValue(fmt.Sprintf("%d uncommitted", status.UncommittedCount), "warning"))
			} else {
				syncTimeStr := ""
				if daemonStatus != nil {
					for _, wsList := range daemonStatus.Workspaces {
						for _, ws := range wsList {
							if ws.Path == expectedPath && !ws.LastSync.IsZero() {
								syncTimeStr = fmt.Sprintf(" (%s)", formatTimeAgo(ws.LastSync))
								break
							}
						}
						if syncTimeStr != "" {
							break
						}
					}
				}
				b.WriteString(statusLabelStyle.Render("  Status"))
				b.WriteString(formatValue("synced"+syncTimeStr, "success"))
			}
			b.WriteString("\n")
		} else {
			b.WriteString(statusLabelStyle.Render("  Status"))
			if isDaemonBootstrapping(daemonStatus) {
				b.WriteString(statusMutedStyle.Render("⟳ setting up..."))
			} else {
				b.WriteString(formatValue("not cloned", "warning"))
			}
			b.WriteString("\n")
			b.WriteString(statusLabelStyle.Render("  Remote"))
			b.WriteString(statusMutedStyle.Render(cloudTC.URL))
			b.WriteString("\n")
			b.WriteString(statusLabelStyle.Render(""))
			if isDaemonBootstrapping(daemonStatus) {
				b.WriteString(statusMutedStyle.Render("Initial clone in progress"))
			} else {
				b.WriteString(statusMutedStyle.Render("Run 'ox doctor --fix' to clone"))
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	// helper: render a single detail-only team context entry
	renderDetailTC := func(detailTC api.RepoDetailTeamContext) {
		expectedPath := paths.TeamContextDir(detailTC.StableID(), projectEndpoint)
		if renderedTeams[expectedPath] {
			return
		}
		renderedTeams[expectedPath] = true

		b.WriteString(statusLabelStyle.Render("Team"))
		b.WriteString(statusValueStyle.Render(detailTC.Name))
		b.WriteString("\n")

		detailVisibility := "private"
		if detailTC.Visibility != "" {
			detailVisibility = detailTC.Visibility
		}
		b.WriteString(statusLabelStyle.Render("  Visibility"))
		b.WriteString(renderVisibilityWithAccess(detailVisibility, detailTC.AccessLevel))
		b.WriteString("\n")
		b.WriteString(statusLabelStyle.Render("  Path"))
		b.WriteString(statusMutedStyle.Render(expectedPath))
		b.WriteString("\n")

		gitDir := filepath.Join(expectedPath, ".git")
		if _, err := os.Stat(gitDir); err == nil {
			status := getGitRepoStatus(expectedPath, time.Time{}, false)
			if status.Error != "" {
				b.WriteString(statusLabelStyle.Render("  Status"))
				b.WriteString(formatValue(status.Error, "error"))
			} else if status.UncommittedCount > 0 {
				b.WriteString(statusLabelStyle.Render("  Status"))
				b.WriteString(formatValue(fmt.Sprintf("%d uncommitted", status.UncommittedCount), "warning"))
			} else {
				b.WriteString(statusLabelStyle.Render("  Status"))
				b.WriteString(formatValue("synced", "success"))
			}
			b.WriteString("\n")
		} else {
			b.WriteString(statusLabelStyle.Render("  Status"))
			if isDaemonBootstrapping(daemonStatus) {
				b.WriteString(statusMutedStyle.Render("⟳ setting up..."))
			} else {
				b.WriteString(formatValue("not cloned", "warning"))
			}
			b.WriteString("\n")
			b.WriteString(statusLabelStyle.Render("  Remote"))
			b.WriteString(statusMutedStyle.Render(detailTC.RepoURL))
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	hasAnyTeams := false

	// Repo team context - rendered inline under Project Status
	b.WriteString("\n")
	if repoCloudTC != nil {
		hasAnyTeams = true
		renderCloudTC(repoCloudTC.info)
	} else if repoDetailTC != nil {
		hasAnyTeams = true
		renderDetailTC(repoDetailTC.info)
	} else {
		// no repo team context found
		b.WriteString(statusLabelStyle.Render("Status"))
		b.WriteString(statusMutedStyle.Render("not configured"))
		b.WriteString("\n")
	}

	// Other team contexts
	hasOtherTCs := len(otherCloudTCs) > 0 || len(otherDetailTCs) > 0
	if hasOtherTCs {
		b.WriteString("\n")
		b.WriteString(statusHeaderStyle.Render("Other Team Contexts"))
		b.WriteString("\n")
		b.WriteString(statusMutedStyle.Render("───────────────────"))
		b.WriteString("\n")

		for _, entry := range otherCloudTCs {
			hasAnyTeams = true
			renderCloudTC(entry.info)
		}
		for _, entry := range otherDetailTCs {
			hasAnyTeams = true
			renderDetailTC(entry.info)
		}
	}

	if !hasAnyTeams && repoTeamID == "" {
		// no repo TC and no other TCs at all
		if authenticated && len(cloudTeamContexts) == 0 {
			b.WriteString(statusLabelStyle.Render(""))
			if userEmail != "" {
				b.WriteString(statusWarningStyle.Render(fmt.Sprintf("You have no teams assigned to your account yet (%s)", userEmail)))
			} else {
				b.WriteString(statusWarningStyle.Render("You have no teams assigned to your account yet"))
			}
			b.WriteString("\n")
		}
	}

	return b.String()
}

// sparkline configuration constants
const (
	sparklineLevels       = 8  // number of vertical levels in sparkline
	sparklineMaxLevel     = 7  // max index (sparklineLevels - 1)
	sparklineEmptyLevel   = 0  // level for buckets with no events
	sparklineBuckets      = 48 // 4 hours at 5-minute intervals
	sparklineWindow       = 4 * time.Hour
	sparklineBucketsPerHr = 12 // 60 min / 5 min intervals
)

// sparklineChars are Unicode block characters for sparkline rendering (8 levels)
var sparklineChars = []rune{'▁', '▂', '▃', '▄', '▅', '▆', '▇', '█'}

// renderSparkline creates a sparkline visualization from sync events.
// Buckets events into time slots and shows activity intensity.
func renderSparkline(events []daemon.SyncEvent, buckets int, window time.Duration) string {
	if len(events) == 0 || buckets <= 0 {
		return statusMutedStyle.Render("─" + strings.Repeat("─", buckets))
	}

	now := time.Now()
	bucketDuration := window / time.Duration(buckets)
	counts := make([]int, buckets)

	// count events in each bucket (most recent on right)
	for _, e := range events {
		age := now.Sub(e.Time)
		if age >= window || age < 0 {
			continue
		}
		// bucket 0 is oldest, bucket n-1 is most recent
		idx := buckets - 1 - int(age/bucketDuration)
		if idx >= 0 && idx < buckets {
			counts[idx]++
		}
	}

	// find max for scaling
	maxCount := 1
	for _, c := range counts {
		if c > maxCount {
			maxCount = c
		}
	}

	// render sparkline with hour separators
	var sb strings.Builder
	for i, c := range counts {
		// add hour separator (but not at the start)
		if i > 0 && i%sparklineBucketsPerHr == 0 {
			sb.WriteRune('|')
		}

		if c == 0 {
			sb.WriteRune(sparklineChars[sparklineEmptyLevel]) // baseline for empty
		} else {
			// scale to sparklineMaxLevel range, minimum 1 for any non-zero count
			level := (c * sparklineMaxLevel) / maxCount
			if level < 1 {
				level = 1 // ensure non-zero activity is visible
			} else if level > sparklineMaxLevel {
				level = sparklineMaxLevel
			}
			sb.WriteRune(sparklineChars[level])
		}
	}

	return cli.StyleDim.Render(sb.String())
}

// renderSparklineTimeMarkers renders time markers aligned below the sparkline
func renderSparklineTimeMarkers() string {
	// sparkline total width: 48 buckets + 3 separators = 51 chars
	// markers: "4h ago" at left, "2h" in center, "now" at right
	const width = sparklineBuckets + 3 // 51

	line := make([]byte, width)
	for i := range line {
		line[i] = ' '
	}

	// "4h ago" at position 0
	copy(line[0:], "4h ago")

	// "2h" centered around position 25-26 (the 2h separator)
	copy(line[24:], "2h")

	// "now" right-aligned, ending at position 50
	copy(line[48:], "now")

	return cli.StyleDim.Render(string(line))
}

// daemonSyncWarningThreshold is the uptime duration after which we expect syncs to have occurred
const daemonSyncWarningThreshold = time.Minute

// daemonBootstrapThreshold is the grace period for initial daemon startup
const daemonBootstrapThreshold = 3 * time.Minute

// isDaemonBootstrapping returns true if the daemon is still in its initial startup phase.
// During bootstrap (running, 0 syncs, uptime < 3min), warnings are softened.
func isDaemonBootstrapping(status *daemon.StatusData) bool {
	if status == nil || !status.Running {
		return false
	}
	return status.TotalSyncs == 0 &&
		status.Uptime < daemonBootstrapThreshold &&
		daemonHasConfiguredRepos(status)
}

// daemonHasConfiguredRepos checks if the daemon has repos that should be syncing.
// Returns true if any workspaces (ledger or team contexts) are configured.
func daemonHasConfiguredRepos(status *daemon.StatusData) bool {
	if status == nil {
		return false
	}
	// prefer new Workspaces map, fall back to legacy fields
	for _, workspaces := range status.Workspaces {
		if len(workspaces) > 0 {
			return true
		}
	}
	return status.LedgerPath != "" || len(status.TeamContexts) > 0
}

// renderDaemonSyncSection renders daemon sync statistics
func renderDaemonSyncSection(status *daemon.StatusData, syncHistory []daemon.SyncEvent, noProject bool, projectInitialized bool) string {
	var b strings.Builder

	b.WriteString("\n")
	b.WriteString(statusHeaderStyle.Render("Daemon Sync"))
	b.WriteString("\n")
	b.WriteString(statusMutedStyle.Render("───────────"))
	b.WriteString("\n")

	// check if not in a project
	if noProject {
		b.WriteString(statusLabelStyle.Render("Status"))
		b.WriteString(formatValue("n/a (not in a git repo)", "muted"))
		b.WriteString("\n")
		return b.String()
	}

	// handle nil status (daemon not connected)
	if status == nil {
		b.WriteString(statusLabelStyle.Render("Status"))
		if projectInitialized {
			b.WriteString(statusMutedStyle.Render("⟳ not started — will auto-start on next session"))
		} else {
			b.WriteString(statusMutedStyle.Render("not running (expected until 'ox init' completed)"))
		}
		b.WriteString("\n")
		return b.String()
	}

	// check for bootstrap vs warning condition
	hasConfiguredRepos := daemonHasConfiguredRepos(status)
	bootstrapping := isDaemonBootstrapping(status)
	isNotSyncing := status.Running &&
		status.Uptime > daemonSyncWarningThreshold &&
		status.TotalSyncs == 0 &&
		hasConfiguredRepos &&
		!bootstrapping // don't warn during bootstrap

	// daemon status
	if status.Running {
		b.WriteString(statusLabelStyle.Render("Status"))
		uptime := formatDurationShort(status.Uptime)
		if bootstrapping {
			b.WriteString(statusMutedStyle.Render(fmt.Sprintf("⟳ running %s — initial sync in progress (pid %d)", uptime, status.Pid)))
		} else if isNotSyncing {
			b.WriteString(formatValue(fmt.Sprintf("running %s, not syncing (pid %d)", uptime, status.Pid), "warning"))
		} else {
			b.WriteString(formatValue(fmt.Sprintf("running %s (pid %d)", uptime, status.Pid), "success"))
		}
		b.WriteString("\n")
	} else {
		b.WriteString(statusLabelStyle.Render("Status"))
		if !projectInitialized {
			b.WriteString(statusMutedStyle.Render("not running (expected until 'ox init' completed)"))
		} else {
			b.WriteString(formatValue("not running", "error"))
		}
		b.WriteString("\n")
		return b.String()
	}

	// sync stats - show warning indicator when zero syncs but repos are configured
	b.WriteString(statusLabelStyle.Render("Total syncs"))
	if bootstrapping {
		b.WriteString(statusMutedStyle.Render(fmt.Sprintf("%d (initial sync pending)", status.TotalSyncs)))
	} else if isNotSyncing {
		b.WriteString(statusWarningStyle.Render(fmt.Sprintf("%d ", status.TotalSyncs)))
		b.WriteString(formatValue("expected syncs with configured repos", "warning"))
	} else {
		b.WriteString(statusValueStyle.Render(fmt.Sprintf("%d", status.TotalSyncs)))
		lastSyncStr := ""
		if !status.LastSync.IsZero() {
			lastSyncStr = fmt.Sprintf("; last @ %s", status.LastSync.Format("2006-01-02 15:04:05"))
		}
		b.WriteString(statusMutedStyle.Render(fmt.Sprintf(" (%d last hour%s)", status.SyncsLastHour, lastSyncStr)))
	}
	b.WriteString("\n")

	if status.AvgSyncTime > 0 {
		b.WriteString(statusLabelStyle.Render("Avg sync time"))
		b.WriteString(statusMutedStyle.Render(formatDurationShort(status.AvgSyncTime)))
		b.WriteString("\n")
	}

	// sparkline for last 4 hours (48 buckets = 5 min each)
	if len(syncHistory) > 0 {
		b.WriteString(statusLabelStyle.Render("Activity (4h)"))
		b.WriteString(renderSparkline(syncHistory, sparklineBuckets, sparklineWindow))
		b.WriteString("\n")
		// time markers below sparkline
		b.WriteString(statusLabelStyle.Render(""))
		b.WriteString(renderSparklineTimeMarkers())
		b.WriteString("\n")
	}

	// error info
	if status.LastError != "" {
		b.WriteString(statusLabelStyle.Render("Last error"))
		b.WriteString(formatValue(status.LastError, "error"))
		b.WriteString("\n")
	}

	// show workspaces being synced (new unified view)
	// count total workspaces across all types
	totalWorkspaces := 0
	for _, wsList := range status.Workspaces {
		totalWorkspaces += len(wsList)
	}

	if totalWorkspaces > 0 {
		// extract common git host from any workspace for the header
		syncHost := ""
		for _, wsList := range status.Workspaces {
			for _, ws := range wsList {
				if h := extractGitHost(ws.CloneURL); h != "" {
					syncHost = h
					break
				}
			}
			if syncHost != "" {
				break
			}
		}

		b.WriteString("\n")
		b.WriteString(statusLabelStyle.Render("Syncing"))
		if syncHost != "" {
			b.WriteString(statusValueStyle.Render("→ " + syncHost))
		}
		b.WriteString("\n")

		// display in consistent order: ledger first, then team-contexts
		// compute label width: longest label + 2 (indent) + 2 (padding), min 20
		syncLabelWidth := 20
		for _, wsList := range status.Workspaces {
			for _, ws := range wsList {
				name := ws.Type
				if ws.TeamName != "" {
					name = ws.TeamName
				} else if ws.TeamID != "" {
					name = ws.TeamID
				}
				if w := len(name) + 4; w > syncLabelWidth { // 2 indent + 2 padding
					syncLabelWidth = w
				}
			}
		}
		syncLabel := statusLabelStyle.Width(syncLabelWidth)

		wsOrder := []string{"ledger", "team-context"}
		for _, wsType := range wsOrder {
			workspaces, ok := status.Workspaces[wsType]
			if !ok || len(workspaces) == 0 {
				continue
			}

			for _, ws := range workspaces {
				label := ws.Type
				if ws.TeamName != "" {
					label = ws.TeamName
				} else if ws.TeamID != "" {
					label = ws.TeamID
				}

				b.WriteString(syncLabel.Render("  " + label))
				if ws.Exists {
					b.WriteString(statusSuccessStyle.Render("✓ "))
				} else {
					b.WriteString(statusWarningStyle.Render("⚠ "))
				}
				// condensed: sync time on same line as label
				if !ws.LastSync.IsZero() {
					b.WriteString(statusMutedStyle.Render(formatTimeAgo(ws.LastSync)))
				} else if !ws.Exists && ws.CloneURL != "" {
					b.WriteString(statusMutedStyle.Render(ws.CloneURL))
				}
				b.WriteString("\n")

				if ws.LastErr != "" {
					b.WriteString(syncLabel.Render("      Error"))
					b.WriteString(formatValue(ws.LastErr, "error"))
					b.WriteString("\n")
				}
			}
		}
	} else {
		// fall back to legacy display if Workspaces not populated
		// ledger path
		if status.LedgerPath != "" {
			b.WriteString("\n")
			b.WriteString(statusLabelStyle.Render("Ledger path"))
			b.WriteString(statusMutedStyle.Render(status.LedgerPath))
			b.WriteString("\n")
		}

		// team contexts from daemon
		if len(status.TeamContexts) > 0 {
			b.WriteString("\n")
			for _, tc := range status.TeamContexts {
				label := tc.TeamName
				if label == "" {
					label = tc.TeamID
				}
				b.WriteString(statusLabelStyle.Render(label))
				b.WriteString(statusMutedStyle.Render(tc.Path))
				b.WriteString("\n")

				// sync status with git host
				if !tc.LastSync.IsZero() {
					b.WriteString(statusLabelStyle.Render("  Last sync"))
					b.WriteString(statusMutedStyle.Render(formatTimeAgo(tc.LastSync)))
					b.WriteString("\n")
				}
				if tc.LastErr != "" {
					b.WriteString(statusLabelStyle.Render("  Error"))
					b.WriteString(formatValue(tc.LastErr, "error"))
					b.WriteString("\n")
				}
			}
		}
	}

	return b.String()
}

// formatDurationShort formats a duration in a short human-readable form
func formatDurationShort(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	if d < time.Hour {
		return fmt.Sprintf("%.1fm", d.Minutes())
	}
	return fmt.Sprintf("%.1fh", d.Hours())
}

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Display SageOx status and directory locations",
	Long: `Display current authentication status, configuration, and data locations.

Shows authentication state, project initialization, ledger/team context sync status,
daemon health, and a tree view of all SageOx directory locations.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		gitRoot := findGitRoot()

		// Get current endpoint - use project config if available
		currentEndpoint := endpoint.GetForProject(gitRoot)
		endpointSlug := endpoint.NormalizeSlug(currentEndpoint)

		authenticated, err := auth.IsAuthenticatedForEndpoint(currentEndpoint)
		if err != nil {
			return fmt.Errorf("failed to check authentication status: %w", err)
		}

		// get auth token if authenticated
		var token *auth.StoredToken
		if authenticated {
			token, err = auth.GetTokenForEndpoint(currentEndpoint)
			if err != nil {
				return fmt.Errorf("failed to get token: %w", err)
			}

			// ensure git credentials are valid (auto-refresh if needed)
			// this is fast (local check) unless credentials need refresh
			_, _ = gitserver.EnsureValidCredentialsForEndpoint(currentEndpoint, func() (*gitserver.GitCredentials, error) {
				client := api.NewRepoClientWithEndpoint(currentEndpoint).WithAuthToken(token.AccessToken)
				return fetchGitCredentials(client)
			})
		}

		// get config paths
		authFile, err := auth.GetAuthFilePath()
		if err != nil {
			return fmt.Errorf("failed to get auth file path: %w", err)
		}
		userConfigDir := config.GetUserConfigDir()
		authFileExists := false
		if _, err := os.Stat(authFile); err == nil {
			authFileExists = true
		}

		// get working directory
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("failed to get working directory: %w", err)
		}

		sageoxDir := filepath.Join(cwd, ".sageox")
		var localCfg *config.LocalConfig
		var projectInitialized bool

		if stat, err := os.Stat(sageoxDir); err == nil && stat.IsDir() {
			projectInitialized = true

			if gitRoot != "" {
				// load local config for git repos section
				localCfg, _ = config.LoadLocalConfig(gitRoot)

				// if no ledger path in config, try to get info from cloud API or default path
				if localCfg.Ledger == nil || localCfg.Ledger.Path == "" {
					ledgerPath, _ := ledger.DefaultPath()

					// first check if ledger exists locally at default path
					if ledgerPath != "" && ledger.Exists(ledgerPath) {
						localCfg.Ledger = &config.LedgerConfig{
							Path: ledgerPath,
						}
					} else if authenticated {
						// ledger not cloned yet - get expected info from cloud API
						// so we can show what SHOULD exist even if not cloned
						if ledgerURL := getLedgerRemoteURL(currentEndpoint); ledgerURL != "" {
							if ledgerPath == "" {
								ledgerPath = "(pending clone)"
							}
							localCfg.Ledger = &config.LedgerConfig{
								Path: ledgerPath,
							}
						}
					}
				}
			}
		}

		// fetch repo detail once for both human-readable and JSON output paths
		var repoDetail *api.RepoDetailResponse
		if authenticated && token != nil {
			var projectCfg *config.ProjectConfig
			if gitRoot != "" {
				projectCfg, _ = config.LoadProjectConfig(gitRoot)
			}
			if projectCfg != nil && projectCfg.RepoID != "" {
				client := api.NewRepoClientWithEndpoint(currentEndpoint).WithAuthToken(token.AccessToken)
				repoDetail, _ = client.GetRepoDetail(projectCfg.RepoID)
			}
		}

		// JSON output mode
		if statusJSONFlag {
			output := buildStatusJSON(authenticated, token, endpointSlug, authFile, authFileExists,
				userConfigDir, cwd, sageoxDir, projectInitialized, localCfg, gitRoot, repoDetail)
			jsonBytes, err := json.MarshalIndent(output, "", "  ")
			if err != nil {
				return fmt.Errorf("failed to marshal JSON: %w", err)
			}
			fmt.Println(string(jsonBytes))
			return nil
		}

		// Human-readable output mode
		// Authentication Status - always show, includes endpoint
		fmt.Print(renderAuthStatus(authFile))
		if len(auth.GetLoggedInEndpoints()) == 0 {
			// use contextual action hint matching help's visual style
			cli.PrintActionHint("ox login", "Authenticate with "+cli.Wordmark(), 1)
		}

		configDirSemantic := "error"
		if pathExistsStatus(userConfigDir) {
			configDirSemantic = "success"
		}

		configRows := [][]string{
			{"User config dir", userConfigDir, configDirSemantic},
		}
		fmt.Print(renderTable("Configuration", configRows))

		fmt.Print(renderProjectStatus(cwd, gitRoot, projectInitialized))
		if gitRoot != "" && !projectInitialized {
			cli.PrintActionHint("ox init", "Initialize project for AI agent context", 2)
		}

		// fetch daemon status once, pass to both git repos and daemon sync sections
		var daemonStatus *daemon.StatusData
		var syncHistory []daemon.SyncEvent
		if gitRoot != "" {
			client := daemon.TryConnectOrDirect()
			if client != nil {
				if ds, err := client.Status(); err == nil {
					daemonStatus = ds
					syncHistory, _ = client.SyncHistory()
				}
			}
		}

		// Ledger and Team Context sections - shows repos from cloud API
		// Only displays repos that are actually provisioned
		fmt.Print(renderGitReposSection(localCfg, gitRoot, daemonStatus))

		// show daemon sync section - always show so user knows daemon status
		if gitRoot == "" {
			fmt.Print(renderDaemonSyncSection(nil, nil, true, projectInitialized))
		} else {
			fmt.Print(renderDaemonSyncSection(daemonStatus, syncHistory, false, projectInitialized))
		}

		// show contextual tip
		userCfg, _ := config.LoadUserConfig("")
		tips.MaybeShow("status", tips.AlwaysShow, cfg.Quiet, !userCfg.AreTipsEnabled(), cfg.JSON)

		return nil
	},
}

// buildStatusJSON constructs the JSON output structure for ox status --json
func buildStatusJSON(authenticated bool, token *auth.StoredToken, endpointSlug, authFile string, authFileExists bool,
	userConfigDir, cwd, sageoxDir string, projectInitialized bool, localCfg *config.LocalConfig, gitRoot string,
	repoDetail *api.RepoDetailResponse) statusJSONOutput {

	output := statusJSONOutput{}

	// auth section
	output.Auth = &statusAuthJSON{
		Authenticated: authenticated,
		Endpoint:      endpointSlug,
	}
	if authenticated && token != nil {
		output.Auth.User = token.UserInfo.Name
		output.Auth.Email = token.UserInfo.Email
		output.Auth.ExpiresAt = &token.ExpiresAt
	}

	// config section
	output.Config = &statusConfigJSON{
		UserConfigDir:  userConfigDir,
		AuthFile:       authFile,
		AuthFileExists: authFileExists,
	}

	// project section
	output.Project = &statusProjectJSON{
		Initialized: projectInitialized,
		Directory:   cwd,
	}
	if projectInitialized {
		output.Project.ConfigPath = sageoxDir
	}

	// ledger section
	if localCfg != nil && localCfg.Ledger != nil && localCfg.Ledger.Path != "" {
		status := getGitRepoStatus(localCfg.Ledger.Path, localCfg.Ledger.LastSync, localCfg.Ledger.HasLastSync())
		output.Ledger = &statusLedgerJSON{
			Configured: true,
			Path:       localCfg.Ledger.Path,
			Exists:     status.Exists,
			Branch:     status.Branch,
		}
		if status.Error != "" {
			output.Ledger.Error = status.Error
		} else if status.UncommittedCount > 0 {
			output.Ledger.Status = fmt.Sprintf("%d uncommitted", status.UncommittedCount)
		} else {
			output.Ledger.Status = "synced"
		}
		// populate visibility/access from repoDetail
		if repoDetail != nil {
			output.Ledger.Visibility = repoDetail.Visibility
			output.Ledger.AccessLevel = repoDetail.AccessLevel
		}
	}

	// team contexts section
	if localCfg != nil && len(localCfg.TeamContexts) > 0 {
		for _, tc := range localCfg.TeamContexts {
			if tc.Path == "" {
				continue
			}
			status := getGitRepoStatus(tc.Path, tc.LastSync, tc.HasLastSync())
			tcJSON := statusTeamContextJSON{
				TeamID:   tc.TeamID,
				TeamName: tc.TeamName,
				Path:     tc.Path,
				Exists:   status.Exists,
				Branch:   status.Branch,
			}
			if status.Error != "" {
				tcJSON.Error = status.Error
			} else if status.UncommittedCount > 0 {
				tcJSON.Status = fmt.Sprintf("%d uncommitted", status.UncommittedCount)
			} else {
				tcJSON.Status = "synced"
			}
			output.TeamContexts = append(output.TeamContexts, tcJSON)
		}
	}

	// daemon section
	if gitRoot != "" {
		output.Daemon = &statusDaemonJSON{}
		client := daemon.TryConnectOrDirect()
		if client != nil {
			if daemonStatus, err := client.Status(); err == nil {
				output.Daemon.Running = daemonStatus.Running
				output.Daemon.Pid = daemonStatus.Pid
				output.Daemon.UptimeSeconds = int64(daemonStatus.Uptime.Seconds())
				output.Daemon.TotalSyncs = daemonStatus.TotalSyncs
				output.Daemon.SyncsLastHour = daemonStatus.SyncsLastHour
				output.Daemon.LastError = daemonStatus.LastError
			}
		}
	}

	return output
}

// renderAuthStatus renders the authentication status section
// Shows all logged-in endpoints, not just the current one
func renderAuthStatus(authFile string) string {
	var b strings.Builder

	b.WriteString("\n")
	b.WriteString(statusHeaderStyle.Render("Authentication Status"))
	b.WriteString("\n")
	b.WriteString(statusMutedStyle.Render("─────────────────────"))
	b.WriteString("\n")

	// get all logged-in endpoints
	loggedInEndpoints := auth.GetLoggedInEndpoints()

	if len(loggedInEndpoints) == 0 {
		b.WriteString(statusLabelStyle.Render("Status"))
		b.WriteString(statusErrorStyle.Render("✗ not logged in"))
		b.WriteString("\n")
		return b.String()
	}

	// show each logged-in endpoint with its details
	for i, ep := range loggedInEndpoints {
		epToken, _ := auth.GetTokenForEndpoint(ep)
		epSlug := endpoint.NormalizeSlug(ep)

		if i > 0 {
			b.WriteString("\n")
		}

		b.WriteString(statusLabelStyle.Render("Endpoint"))
		b.WriteString(statusHighlightStyle.Render(epSlug))
		b.WriteString(statusSuccessStyle.Render(" (✓ logged in)"))
		b.WriteString("\n")

		if epToken != nil {
			b.WriteString(statusLabelStyle.Render("User"))
			b.WriteString(statusHighlightStyle.Render(epToken.UserInfo.Name))
			b.WriteString(statusMutedStyle.Render(" <" + epToken.UserInfo.Email + ">"))
			b.WriteString("\n")

			b.WriteString(statusLabelStyle.Render("Token expires"))
			b.WriteString(statusMutedStyle.Render(epToken.ExpiresAt.Format("2006-01-02 15:04:05 MST")))
			if i == 0 {
				b.WriteString(statusMutedStyle.Render(" (" + authFile + ")"))
			}
			b.WriteString("\n")
		}
	}

	return b.String()
}

// renderProjectStatus renders the project status section with tree-like structure.
// gitRoot is empty when not inside a git repository.
func renderProjectStatus(cwd, gitRoot string, initialized bool) string {
	var b strings.Builder

	b.WriteString("\n")
	b.WriteString(statusHeaderStyle.Render("Project Status"))
	b.WriteString("\n")
	b.WriteString(statusMutedStyle.Render("──────────────"))
	b.WriteString("\n")

	if gitRoot == "" {
		b.WriteString(statusLabelStyle.Render("Directory"))
		b.WriteString(statusMutedStyle.Render(cwd))
		b.WriteString("\n")
		b.WriteString(statusLabelStyle.Render("Status"))
		b.WriteString(statusErrorStyle.Render("✗ not a git repo"))
		b.WriteString("\n")
		return b.String()
	}

	b.WriteString(statusLabelStyle.Render("Repo directory"))
	b.WriteString(statusMutedStyle.Render(gitRoot))
	b.WriteString("\n")

	b.WriteString(statusLabelStyle.Render("  " + cli.Wordmark() + " state"))
	if initialized {
		b.WriteString(statusMutedStyle.Render("└── .sageox/ dir "))
		b.WriteString(statusSuccessStyle.Render("✓"))
	} else {
		b.WriteString(statusMutedStyle.Render("└── .sageox/ dir "))
		b.WriteString(statusWarningStyle.Render("(not initialized)"))
	}
	b.WriteString("\n")

	return b.String()
}

// pathExistsStatus checks if a path exists (for status command)
func pathExistsStatus(path string) bool {
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}
