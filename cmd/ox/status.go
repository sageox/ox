package main

import (
	"context"
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
	"github.com/sageox/ox/internal/daemon/agentwork"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/gitserver"
	"github.com/sageox/ox/internal/gitutil"
	"github.com/sageox/ox/internal/ledger"
	"github.com/sageox/ox/internal/paths"
	"github.com/sageox/ox/internal/status"
	"github.com/sageox/ox/internal/tips"
	"github.com/sageox/ox/internal/tui"
	"github.com/sageox/ox/internal/version"
	"github.com/spf13/cobra"
)

// type aliases for status JSON types (defined in internal/status)
type statusJSONOutput = status.JSONOutput
type statusAICoworkerJSON = status.AICoworkerJSON
type statusVersionJSON = status.VersionJSON
type statusAuthJSON = status.AuthJSON
type statusConfigJSON = status.ConfigJSON
type statusProjectJSON = status.ProjectJSON
type statusCodeIndexJSON = status.CodeIndexJSON
type statusLedgerJSON = status.LedgerJSON
type statusTeamContextJSON = status.TeamContextJSON
type statusDaemonJSON = status.DaemonJSON

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
			semantic = status.InferSemantic(label, value)
		}

		b.WriteString(statusLabelStyle.Render(label))
		b.WriteString(formatValue(value, semantic))
		b.WriteString("\n")
	}

	return b.String()
}

// type alias for git repo status (defined in internal/status)
type gitRepoStatus = status.GitRepoStatus

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
	if !gitutil.IsGitRepo(repoPath) {
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

	// get uncommitted changes count (ignore untracked files like .sageox/, .observations/)
	statusCmd := exec.Command("git", "-C", repoPath, "status", "--porcelain", "-uno")
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
// shortenPathViaSymlink returns a short relative path (e.g. ".sageox/ledger")
// if a .sageox/ symlink in projectRoot resolves to fullPath.
// Checks candidates in order and returns the first match, or fullPath unchanged.
func shortenPathViaSymlink(projectRoot, fullPath string, candidates ...string) string {
	if projectRoot == "" || fullPath == "" {
		return fullPath
	}
	for _, rel := range candidates {
		abs := filepath.Join(projectRoot, rel)
		target, err := os.Readlink(abs)
		if err != nil {
			continue
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(abs), target)
		}
		if filepath.Clean(target) == filepath.Clean(fullPath) {
			return rel
		}
	}
	return fullPath
}

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
		// layered sync time: prefer daemon (freshest), fall back to config file (persistent)
		ledgerLastSync := localCfg.Ledger.LastSync
		ledgerHasSync := localCfg.Ledger.HasLastSync()
		if daemonSync, ok := daemonStatus.LastSyncForPath(localCfg.Ledger.Path); ok {
			ledgerLastSync = daemonSync
			ledgerHasSync = true
		}
		repoStatus := getGitRepoStatus(localCfg.Ledger.Path, ledgerLastSync, ledgerHasSync)

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
		b.WriteString(statusMutedStyle.Render(shortenPathViaSymlink(projectRoot, localCfg.Ledger.Path, ".sageox/ledger")))
		b.WriteString("\n")

		// check if ledger doesn't exist locally and user doesn't have access (ErrLedgerNotFound)
		ledgerNotAccessible := !repoStatus.Exists && errors.Is(ledgerStatusErr, api.ErrLedgerNotFound)

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
			statusText, semantic := status.FormatGitRepoStatus(repoStatus)
			if accessLevel == "viewer" {
				statusText += " (read-only)"
			}
			b.WriteString(statusLabelStyle.Render("  Status"))
			b.WriteString(formatValue(statusText, semantic))
			b.WriteString("\n")

			// hint for missing repo
			if !repoStatus.Exists {
				b.WriteString(statusLabelStyle.Render(""))
				b.WriteString(statusMutedStyle.Render("Run 'ox doctor --fix' to restore"))
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
		if daemonStatus.IsBootstrapping() {
			b.WriteString(statusMutedStyle.Render("⟳ setting up..."))
		} else {
			b.WriteString(formatValue("not cloned", "warning"))
		}
		b.WriteString("\n")
		b.WriteString(statusLabelStyle.Render("  Path"))
		b.WriteString(statusMutedStyle.Render(shortenPathViaSymlink(projectRoot, expectedPath, ".sageox/ledger")))
		b.WriteString("\n")
		b.WriteString(statusLabelStyle.Render("  Remote"))
		b.WriteString(statusMutedStyle.Render(cloudLedgerURL))
		b.WriteString("\n")
		b.WriteString(statusLabelStyle.Render(""))
		if daemonStatus.IsBootstrapping() {
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
		b.WriteString(statusMutedStyle.Render(shortenPathViaSymlink(projectRoot, expectedPath, ".sageox/teams/primary", ".sageox/teams/"+cloudTC.StableID())))
		b.WriteString("\n")

		gitDir := filepath.Join(expectedPath, ".git")
		if _, err := os.Stat(gitDir); err == nil {
			// layered sync time: prefer daemon (freshest), fall back to config file (persistent)
			var tcLastSync time.Time
			tcHasSync := false
			if daemonSync, ok := daemonStatus.LastSyncForPath(expectedPath); ok {
				tcLastSync = daemonSync
				tcHasSync = true
			} else if localCfg != nil {
				if tc := localCfg.GetTeamContext(cloudTC.StableID()); tc != nil && tc.HasLastSync() {
					tcLastSync = tc.LastSync
					tcHasSync = true
				}
			}
			repoStatus := getGitRepoStatus(expectedPath, tcLastSync, tcHasSync)
			if repoStatus.Error != "" {
				b.WriteString(statusLabelStyle.Render("  Status"))
				b.WriteString(formatValue(repoStatus.Error, "error"))
			} else {
				statusText, semantic := status.FormatGitRepoStatus(repoStatus)
				b.WriteString(statusLabelStyle.Render("  Status"))
				b.WriteString(formatValue(statusText, semantic))
			}
			b.WriteString("\n")

			// staleness warning
			syncState := daemon.LoadSyncState(expectedPath)
			if syncState.IsStale(daemon.DefaultStalenessThreshold) && !syncState.LastSync.IsZero() {
				b.WriteString(statusLabelStyle.Render(""))
				b.WriteString(statusWarningStyle.Render(fmt.Sprintf("⚠ stale (last sync %s)", status.FormatTimeAgo(syncState.LastSync))))
				b.WriteString("\n")
			}
		} else {
			b.WriteString(statusLabelStyle.Render("  Status"))
			if daemonStatus.IsBootstrapping() {
				b.WriteString(statusMutedStyle.Render("⟳ setting up..."))
			} else {
				b.WriteString(formatValue("not cloned", "warning"))
			}
			b.WriteString("\n")
			b.WriteString(statusLabelStyle.Render("  Remote"))
			b.WriteString(statusMutedStyle.Render(cloudTC.URL))
			b.WriteString("\n")
			b.WriteString(statusLabelStyle.Render(""))
			if daemonStatus.IsBootstrapping() {
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
		b.WriteString(statusMutedStyle.Render(shortenPathViaSymlink(projectRoot, expectedPath, ".sageox/teams/primary", ".sageox/teams/"+detailTC.StableID())))
		b.WriteString("\n")

		gitDir := filepath.Join(expectedPath, ".git")
		if _, err := os.Stat(gitDir); err == nil {
			repoStatus := getGitRepoStatus(expectedPath, time.Time{}, false)
			if repoStatus.Error != "" {
				b.WriteString(statusLabelStyle.Render("  Status"))
				b.WriteString(formatValue(repoStatus.Error, "error"))
			} else if repoStatus.UncommittedCount > 0 {
				b.WriteString(statusLabelStyle.Render("  Status"))
				b.WriteString(formatValue(fmt.Sprintf("%d uncommitted", repoStatus.UncommittedCount), "warning"))
			} else {
				b.WriteString(statusLabelStyle.Render("  Status"))
				b.WriteString(formatValue("synced", "success"))
			}
			b.WriteString("\n")

			// staleness warning
			syncState := daemon.LoadSyncState(expectedPath)
			if syncState.IsStale(daemon.DefaultStalenessThreshold) && !syncState.LastSync.IsZero() {
				b.WriteString(statusLabelStyle.Render(""))
				b.WriteString(statusWarningStyle.Render(fmt.Sprintf("⚠ stale (last sync %s)", status.FormatTimeAgo(syncState.LastSync))))
				b.WriteString("\n")
			}
		} else {
			b.WriteString(statusLabelStyle.Render("  Status"))
			if daemonStatus.IsBootstrapping() {
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

// daemonSyncWarningThreshold is the uptime duration after which we expect syncs to have occurred
const daemonSyncWarningThreshold = time.Minute


// renderDaemonSyncSection renders daemon sync statistics
func renderDaemonSyncSection(ds *daemon.StatusData, syncHistory []daemon.SyncEvent, localCfg *config.LocalConfig, noProject bool, projectInitialized bool) string {
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
	if ds == nil {
		b.WriteString(statusLabelStyle.Render("Status"))
		switch daemon.GetState() {
		case daemon.DaemonStateStarting:
			b.WriteString(statusMutedStyle.Render("◐ starting — process is running but not yet accepting connections"))
		case daemon.DaemonStateStuck:
			b.WriteString(statusMutedStyle.Render("⚠ stuck — process is running but not accepting connections (restart with 'ox daemon restart')"))
		default:
			if projectInitialized {
				b.WriteString(statusMutedStyle.Render("⟳ not started — run 'ox daemon start' or will auto-start on next agentic coding session"))
			} else {
				b.WriteString(statusMutedStyle.Render("not running (expected until 'ox init' completed)"))
			}
		}
		b.WriteString("\n")

		// show last-known sync times from config file when daemon is not running
		if localCfg != nil {
			hasAny := false
			if localCfg.Ledger != nil && localCfg.Ledger.HasLastSync() {
				b.WriteString(statusLabelStyle.Render("  Last ledger sync"))
				b.WriteString(statusMutedStyle.Render(status.FormatTimeAgo(localCfg.Ledger.LastSync)))
				b.WriteString("\n")
				hasAny = true
			}
			for _, tc := range localCfg.TeamContexts {
				if tc.HasLastSync() {
					name := tc.TeamName
					if name == "" {
						name = tc.TeamID
					}
					b.WriteString(statusLabelStyle.Render(fmt.Sprintf("  Last %s sync", name)))
					b.WriteString(statusMutedStyle.Render(status.FormatTimeAgo(tc.LastSync)))
					b.WriteString("\n")
					hasAny = true
				}
			}
			if !hasAny {
				b.WriteString(statusLabelStyle.Render("  Last sync"))
				b.WriteString(statusMutedStyle.Render("never"))
				b.WriteString("\n")
			}
		}

		return b.String()
	}

	// check for bootstrap vs warning condition
	bootstrapping := ds.IsBootstrapping()
	isNotSyncing := ds.Running &&
		ds.Uptime > daemonSyncWarningThreshold &&
		ds.TotalSyncs == 0 &&
		ds.HasConfiguredRepos() &&
		!bootstrapping // don't warn during bootstrap

	// daemon status
	if ds.Running {
		b.WriteString(statusLabelStyle.Render("Status"))
		uptime := status.FormatDurationShort(ds.Uptime)
		if bootstrapping {
			b.WriteString(statusMutedStyle.Render(fmt.Sprintf("⟳ running %s — initial sync in progress (pid %d)", uptime, ds.Pid)))
		} else if isNotSyncing {
			b.WriteString(formatValue(fmt.Sprintf("running %s, not syncing (pid %d)", uptime, ds.Pid), "warning"))
		} else {
			b.WriteString(formatValue(fmt.Sprintf("running %s (pid %d)", uptime, ds.Pid), "success"))
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

	// agent worker status
	b.WriteString(renderAgentWorkerLine())

	// sync stats - show warning indicator when zero syncs but repos are configured
	b.WriteString(statusLabelStyle.Render("Total syncs"))
	if bootstrapping {
		b.WriteString(statusMutedStyle.Render(fmt.Sprintf("%d (initial sync pending)", ds.TotalSyncs)))
	} else if isNotSyncing {
		b.WriteString(statusWarningStyle.Render(fmt.Sprintf("%d ", ds.TotalSyncs)))
		b.WriteString(formatValue("expected syncs with configured repos", "warning"))
	} else {
		b.WriteString(statusValueStyle.Render(fmt.Sprintf("%d", ds.TotalSyncs)))
		lastSyncStr := ""
		if !ds.LastSync.IsZero() {
			lastSyncStr = fmt.Sprintf("; last @ %s", ds.LastSync.Format("2006-01-02 15:04:05"))
		}
		b.WriteString(statusMutedStyle.Render(fmt.Sprintf(" (%d last hour%s)", ds.SyncsLastHour, lastSyncStr)))
	}
	b.WriteString("\n")

	if ds.AvgSyncTime > 0 {
		b.WriteString(statusLabelStyle.Render("Avg sync time"))
		b.WriteString(statusMutedStyle.Render(status.FormatDurationShort(ds.AvgSyncTime)))
		b.WriteString("\n")
	}

	// sparkline for last 4 hours (48 buckets = 5 min each)
	if len(syncHistory) > 0 {
		timestamps := make([]time.Time, len(syncHistory))
		for i, e := range syncHistory {
			timestamps[i] = e.Time
		}
		b.WriteString(statusLabelStyle.Render("Activity (4h)"))
		b.WriteString(cli.StyleDim.Render(tui.RenderSparkline(timestamps, tui.SparklineBuckets, tui.SparklineWindow)))
		b.WriteString("\n")
		// time markers below sparkline
		b.WriteString(statusLabelStyle.Render(""))
		b.WriteString(cli.StyleDim.Render(tui.RenderSparklineTimeMarkers()))
		b.WriteString("\n")
	}

	// error info
	if ds.LastError != "" {
		b.WriteString(statusLabelStyle.Render("Last error"))
		b.WriteString(formatValue(ds.LastError, "error"))
		b.WriteString("\n")
	}

	// show workspaces being synced (new unified view)
	// count total workspaces across all types
	totalWorkspaces := 0
	for _, wsList := range ds.Workspaces {
		totalWorkspaces += len(wsList)
	}

	if totalWorkspaces > 0 {
		// extract common git host from any workspace for the header
		syncHost := ""
		for _, wsList := range ds.Workspaces {
			for _, ws := range wsList {
				if h := status.ExtractGitHost(ws.CloneURL); h != "" {
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
		for _, wsList := range ds.Workspaces {
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
			workspaces, ok := ds.Workspaces[wsType]
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
					b.WriteString(statusMutedStyle.Render(status.FormatTimeAgo(ws.LastSync)))
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
		if ds.LedgerPath != "" {
			b.WriteString("\n")
			b.WriteString(statusLabelStyle.Render("Ledger path"))
			b.WriteString(statusMutedStyle.Render(ds.LedgerPath))
			b.WriteString("\n")
		}

		// team contexts from daemon
		if len(ds.TeamContexts) > 0 {
			b.WriteString("\n")
			for _, tc := range ds.TeamContexts {
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
					b.WriteString(statusMutedStyle.Render(status.FormatTimeAgo(tc.LastSync)))
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

// renderAgentWorkerLine renders the agent worker status as a single labeled line.
func renderAgentWorkerLine() string {
	var b strings.Builder

	userCfg, _ := config.LoadUserConfig()
	raw := ""
	if userCfg != nil {
		raw = userCfg.GetAgentWorkerAgent()
	}

	if raw == "none" {
		b.WriteString(statusLabelStyle.Render("Summarizer"))
		b.WriteString(statusMutedStyle.Render("disabled"))
		b.WriteString("\n")
		return b.String()
	}

	resolved := agentwork.ResolveAgent(raw)
	if resolved == "none" {
		b.WriteString(statusLabelStyle.Render("Summarizer"))
		b.WriteString(statusMutedStyle.Render("none (no agent CLI found)"))
		b.WriteString("\n")
		return b.String()
	}

	u := agentwork.CheckAgentUsability(resolved)

	b.WriteString(statusLabelStyle.Render("Summarizer"))
	if !u.Authenticated {
		b.WriteString(formatValue(fmt.Sprintf("%s (not authenticated)", resolved), "warning"))
	} else {
		source := "auto"
		if raw != "" {
			source = "configured"
		}
		b.WriteString(statusValueStyle.Render(resolved))
		b.WriteString(statusMutedStyle.Render(fmt.Sprintf(" (%s)", source)))
	}
	b.WriteString("\n")
	return b.String()
}

// buildAgentWorkerJSON creates the agent worker JSON for ox status --json.
func buildAgentWorkerJSON() *status.AgentWorkerJSON {
	userCfg, _ := config.LoadUserConfig()
	raw := ""
	if userCfg != nil {
		raw = userCfg.GetAgentWorkerAgent()
	}

	if raw == "none" {
		return &status.AgentWorkerJSON{Agent: "none", Source: "disabled"}
	}

	resolved := agentwork.ResolveAgent(raw)
	source := "auto"
	if raw != "" {
		source = "configured"
	}

	if resolved == "none" {
		return &status.AgentWorkerJSON{Agent: "none", Source: source}
	}

	u := agentwork.CheckAgentUsability(resolved)
	return &status.AgentWorkerJSON{
		Agent:         resolved,
		Source:        source,
		Authenticated: u.Authenticated,
		AuthDetail:    u.AuthDetail,
	}
}

// renderAICoworkersSection renders a one-line summary of active AI coworkers.
// Detail belongs in `ox agent list`.
func renderAICoworkersSection(client *daemon.Client) string {
	if client == nil {
		return ""
	}
	instances, err := client.Instances()
	if err != nil || len(instances) == 0 {
		return ""
	}

	active := 0
	for _, inst := range instances {
		if inst.Status == daemon.StatusActive {
			active++
		}
	}

	var b strings.Builder
	b.WriteString("\n")
	b.WriteString(statusLabelStyle.Render("AI Coworkers"))

	total := len(instances)
	if active == total {
		b.WriteString(formatValue(fmt.Sprintf("%d active", total), "success"))
	} else if active > 0 {
		b.WriteString(formatValue(fmt.Sprintf("%d active, %d idle", active, total-active), "success"))
	} else {
		b.WriteString(formatValue(fmt.Sprintf("%d idle", total), "muted"))
	}
	b.WriteString(statusMutedStyle.Render("  (ox agent list)"))
	b.WriteString("\n")

	return b.String()
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

		var authErr error
		authenticated, err := auth.IsAuthenticatedForEndpoint(currentEndpoint)
		if err != nil {
			// don't bail — show what we can without auth
			authErr = err
			authenticated = false
		}

		// get auth token if authenticated
		var token *auth.StoredToken
		if authenticated {
			token, err = auth.GetTokenForEndpoint(currentEndpoint)
			if err != nil {
				authErr = err
				authenticated = false
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

		// connect to daemon early — needed for project status (code index) and later sections
		var daemonStatus *daemon.StatusData
		var syncHistory []daemon.SyncEvent
		var codeStats *daemon.CodeDBStats
		var client *daemon.Client
		if gitRoot != "" {
			// use longer timeout for status queries — the status handler collects data
			// from multiple subsystems and can exceed 50ms under sync load
			client = daemon.NewClientWithTimeout(500 * time.Millisecond)
			if client.Ping() == nil {
				if ds, err := client.Status(); err == nil {
					daemonStatus = ds
					syncHistory, _ = client.SyncHistory()
				}
				if cs, err := client.CodeStatus(); err == nil {
					codeStats = cs
				}
			} else {
				client = nil
			}
		}

		// JSON output mode
		if statusJSONFlag {
			output := buildStatusJSON(authenticated, authErr, token, endpointSlug, authFile, authFileExists,
				userConfigDir, cwd, sageoxDir, projectInitialized, localCfg, gitRoot, repoDetail, codeStats,
				daemonStatus, client)
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
		if authErr != nil {
			fmt.Printf("  %s %s\n", statusWarningStyle.Render("⚠ token refresh failed:"), statusMutedStyle.Render("run `ox login` to re-authenticate"))
		} else if len(auth.GetLoggedInEndpoints()) == 0 {
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

		fmt.Print(renderProjectStatus(cwd, gitRoot, projectInitialized, codeStats))
		if gitRoot != "" && !projectInitialized {
			cli.PrintActionHint("ox init", "Initialize project for AI agent context", 2)
		}
		if gitRoot != "" && projectInitialized && codeStats == nil {
			// no daemon connected — suggest manual indexing
			cli.PrintActionHint("ox code index", "Index repo for local code search", 0)
		}

		// skip ledger/daemon sections when not in a git repo — nothing to show
		if gitRoot != "" {

			// Ledger and Team Context sections - shows repos from cloud API
			// Only displays repos that are actually provisioned
			fmt.Print(renderGitReposSection(localCfg, gitRoot, daemonStatus))

			// show daemon sync section
			fmt.Print(renderDaemonSyncSection(daemonStatus, syncHistory, localCfg, false, projectInitialized))

			// show active AI coworkers with context stats
			fmt.Print(renderAICoworkersSection(client))
		}

		// show version update notice if available
		if vResult := checkVersionFromCache(); vResult != nil {
			fmt.Printf("\n%s  %s\n",
				statusWarningStyle.Render("Update available"),
				fmt.Sprintf("v%s → v%s — run 'ox upgrade' to update", vResult.CurrentVersion, vResult.LatestVersion),
			)
		}

		// show contextual tip
		userCfg, _ := config.LoadUserConfig()
		tips.MaybeShow("status", tips.AlwaysShow, cfg.Quiet, !userCfg.AreTipsEnabled(), cfg.JSON)

		return nil
	},
}

// buildStatusJSON constructs the JSON output structure for ox status --json.
// daemonStatus and daemonClient are pre-fetched from the daemon to avoid a second ping
// that could race with the first (one succeeds, the other times out = contradictory output).
func buildStatusJSON(authenticated bool, authErr error, token *auth.StoredToken, endpointSlug, authFile string, authFileExists bool,
	userConfigDir, cwd, sageoxDir string, projectInitialized bool, localCfg *config.LocalConfig, gitRoot string,
	repoDetail *api.RepoDetailResponse, codeStats *daemon.CodeDBStats,
	daemonStatus *daemon.StatusData, daemonClient *daemon.Client) statusJSONOutput {

	output := statusJSONOutput{}

	// auth section
	output.Auth = &statusAuthJSON{
		Authenticated: authenticated,
		Endpoint:      endpointSlug,
	}
	if authErr != nil {
		output.Auth.Error = authErr.Error()
	}
	if authenticated && token != nil {
		output.Auth.User = token.UserInfo.Name
		output.Auth.Email = token.UserInfo.Email
		output.Auth.ExpiresAt = &token.ExpiresAt

		// PAT liveness for JSON output
		creds, credErr := gitserver.LoadCredentialsForEndpoint(endpointSlug)
		if credErr == nil && creds != nil && creds.Token != "" && !creds.IsExpired() {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			liveness := gitserver.ValidatePATLiveness(ctx, creds)
			cancel()
			if !liveness.Skipped {
				output.Auth.GitPATValid = &liveness.Valid
				if !liveness.Valid {
					output.Auth.GitPATReason = liveness.Reason
				}
			}
		}
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
		if codeStats != nil {
			ci := &statusCodeIndexJSON{
				Indexed:     codeStats.IndexExists,
				IndexingNow: codeStats.IndexingNow,
				Commits:     codeStats.Commits,
				Blobs:       codeStats.Blobs,
				Symbols:     codeStats.Symbols,
				Error:       codeStats.LastError,
			}
			if !codeStats.LastIndexed.IsZero() {
				ci.LastIndexed = &codeStats.LastIndexed
			}
			output.Project.CodeIndex = ci
		}
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
	} else {
		// ledger not configured locally — report provisioning status from API
		output.Ledger = &statusLedgerJSON{Configured: false}
		if repoDetail != nil && repoDetail.Ledger != nil {
			output.Ledger.Status = repoDetail.Ledger.Status
			if repoDetail.Ledger.Message != "" {
				output.Ledger.Error = repoDetail.Ledger.Message
			}
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
			// sync state staleness
			syncState := daemon.LoadSyncState(tc.Path)
			if !syncState.LastSync.IsZero() {
				ls := syncState.LastSync
				tcJSON.LastSync = &ls
				tcJSON.Stale = syncState.IsStale(daemon.DefaultStalenessThreshold)
			}
			output.TeamContexts = append(output.TeamContexts, tcJSON)
		}
	}

	// daemon section + AI coworkers — use pre-fetched data to avoid a second
	// ping that could race with the first and produce contradictory output
	if gitRoot != "" {
		output.Daemon = &statusDaemonJSON{}
		if daemonStatus != nil {
			output.Daemon.Running = daemonStatus.Running
			output.Daemon.Pid = daemonStatus.Pid
			output.Daemon.UptimeSeconds = int64(daemonStatus.Uptime.Seconds())
			output.Daemon.TotalSyncs = daemonStatus.TotalSyncs
			output.Daemon.SyncsLastHour = daemonStatus.SyncsLastHour
			output.Daemon.LastError = daemonStatus.LastError
		}
		// agent worker status
		output.Daemon.AgentWorker = buildAgentWorkerJSON()

		if daemonClient != nil {
			// AI coworkers with context stats
			if instances, err := daemonClient.Instances(); err == nil && len(instances) > 0 {
				for _, inst := range instances {
					output.AICoworkers = append(output.AICoworkers, statusAICoworkerJSON{
						AgentID:       inst.AgentID,
						ContextTokens: inst.CumulativeContextTokens,
						CommandCount:  inst.CommandCount,
						Status:        inst.Status,
						Age:           status.FormatTimeAgo(inst.LastHeartbeat),
					})
				}
			}
		}
	}

	// version section
	currentVersion := strings.TrimPrefix(version.Version, "v")
	vJSON := &statusVersionJSON{Current: currentVersion}
	if vResult := checkVersionFromCache(); vResult != nil {
		vJSON.Latest = vResult.LatestVersion
		vJSON.UpdateAvailable = true
	}
	output.Version = vJSON

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

			// PAT liveness — probe git server to verify token is accepted
			b.WriteString(statusLabelStyle.Render("Git PAT"))
			creds, credErr := gitserver.LoadCredentialsForEndpoint(ep)
			if credErr != nil || creds == nil || creds.Token == "" {
				b.WriteString(statusMutedStyle.Render("no git credentials"))
			} else if creds.IsExpired() {
				b.WriteString(statusErrorStyle.Render("✗ expired"))
			} else {
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				liveness := gitserver.ValidatePATLiveness(ctx, creds)
				cancel()
				switch {
				case liveness.Valid:
					b.WriteString(statusSuccessStyle.Render("✓ valid"))
				case liveness.Skipped:
					b.WriteString(statusMutedStyle.Render("? " + liveness.Reason))
				default:
					b.WriteString(statusErrorStyle.Render("✗ " + liveness.Reason))
					b.WriteString(statusMutedStyle.Render(" — run `ox login`"))
				}
			}
			b.WriteString("\n")
		}
	}

	return b.String()
}

// renderProjectStatus renders the project status section with tree-like structure.
// gitRoot is empty when not inside a git repository.
func renderProjectStatus(cwd, gitRoot string, initialized bool, codeStats *daemon.CodeDBStats) string {
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
		b.WriteString(statusMutedStyle.Render("├── .sageox/ dir "))
		b.WriteString(statusSuccessStyle.Render("✓"))
	} else {
		b.WriteString(statusMutedStyle.Render("└── .sageox/ dir "))
		b.WriteString(statusWarningStyle.Render("(not initialized)"))
	}
	b.WriteString("\n")

	// code index status — only show when project is initialized
	if initialized {
		b.WriteString(statusLabelStyle.Render("  Code indexed"))
		switch {
		case codeStats == nil:
			b.WriteString(statusMutedStyle.Render("└── unknown (no daemon)"))
		case codeStats.IndexingNow:
			b.WriteString(statusMutedStyle.Render("└── "))
			b.WriteString(statusWarningStyle.Render("indexing…"))
		case !codeStats.IndexExists:
			// no index yet — daemon auto-indexes on start
			b.WriteString(statusMutedStyle.Render("└── "))
			b.WriteString(statusWarningStyle.Render("pending"))
		case codeStats.LastError != "" && codeStats.Commits == 0:
			// index dir exists but empty — initial index failed, will retry
			b.WriteString(statusMutedStyle.Render("└── "))
			b.WriteString(statusWarningStyle.Render("pending"))
		case codeStats.LastError != "":
			b.WriteString(statusMutedStyle.Render("└── "))
			b.WriteString(statusErrorStyle.Render("error: " + codeStats.LastError))
		default:
			b.WriteString(statusMutedStyle.Render("└── "))
			if !codeStats.LastIndexed.IsZero() {
				b.WriteString(statusSuccessStyle.Render(status.FormatTimeAgo(codeStats.LastIndexed)))
			} else {
				b.WriteString(statusSuccessStyle.Render("✓"))
			}
			// show what sources have been indexed
			var sources []string
			if codeStats.Commits > 0 {
				sources = append(sources, "git history")
			}
			if codeStats.Symbols > 0 {
				sources = append(sources, "symbols")
			}
			if codeStats.PRs > 0 || codeStats.Issues > 0 {
				sources = append(sources, "github")
			}
			if len(sources) > 0 {
				b.WriteString(statusMutedStyle.Render("  (" + strings.Join(sources, ", ") + ")"))
			}
		}
		b.WriteString("\n")
	}

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
