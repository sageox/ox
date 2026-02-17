package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/gitserver"
	"github.com/sageox/ox/internal/paths"
	"github.com/sageox/ox/internal/repotools"
	"github.com/sageox/ox/internal/ui"
)

// sageoxGitignoreContent is the expected content of .sageox/.gitignore
// Note: health.json is now in cache/ which is gitignored via cache/
const sageoxGitignoreContent = `# Ignore logs, cache, and session data
logs/
cache/
session.jsonl
sessions/
.needs-doctor
.needs-doctor-agent

# Ignore agent instance state (ephemeral, per-user)
agent_instances/

# Ignore local-only config (sync state, machine-specific)
config.local.toml

# Keep core files (committed to git)
!README.md
!config.json
!discovered.jsonl
!offline/
`

// requiredGitignoreEntries are the entries that must be present in .sageox/.gitignore
var requiredGitignoreEntries = []string{
	"logs/",
	"cache/",
	"session.jsonl",
	"sessions/",
	".needs-doctor",
	".needs-doctor-agent",
	"agent_instances/",
	"config.local.toml",
	"!README.md",
	"!config.json",
	"!discovered.jsonl",
	"!offline/",
}

// mergeGitignoreEntries ensures required entries are present in content.
// Preserves user's existing entries and adds missing required ones.
// Also removes conflicting entries (e.g., removes "foo" if "!foo" is required).
func mergeGitignoreEntries(existingContent string) (string, bool) {
	lines := strings.Split(existingContent, "\n")
	existing := make(map[string]bool)
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" && !strings.HasPrefix(trimmed, "#") {
			existing[trimmed] = true
		}
	}

	// check for conflicting entries that need to be removed
	// e.g., if "!discovered.jsonl" is required, remove "discovered.jsonl"
	var conflictsToRemove []string
	for _, required := range requiredGitignoreEntries {
		if strings.HasPrefix(required, "!") {
			// this is a "keep" entry, check if the ignore version exists
			ignoreVersion := required[1:] // remove the "!"
			if existing[ignoreVersion] {
				conflictsToRemove = append(conflictsToRemove, ignoreVersion)
			}
		}
	}

	// remove conflicting entries from content
	changed := false
	if len(conflictsToRemove) > 0 {
		conflictSet := make(map[string]bool)
		for _, c := range conflictsToRemove {
			conflictSet[c] = true
		}
		var newLines []string
		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			if !conflictSet[trimmed] {
				newLines = append(newLines, line)
			}
		}
		lines = newLines
		existingContent = strings.Join(lines, "\n")
		changed = true

		// rebuild existing map after removal
		existing = make(map[string]bool)
		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			if trimmed != "" && !strings.HasPrefix(trimmed, "#") {
				existing[trimmed] = true
			}
		}
	}

	var missing []string
	for _, required := range requiredGitignoreEntries {
		if !existing[required] {
			missing = append(missing, required)
		}
	}

	if len(missing) == 0 && !changed {
		return existingContent, false // no changes needed
	}

	if len(missing) == 0 {
		return existingContent, changed // only removed conflicts
	}

	// add missing entries at the end
	result := strings.TrimRight(existingContent, "\n")
	result += "\n\n# SageOx required entries\n"
	for _, entry := range missing {
		result += entry + "\n"
	}
	return result, true
}

// checkReadmeFile checks if .sageox/README.md exists and is up to date.
// With fix=true, updates the README.md to the latest content.
// Checks for: missing, empty, stale (>7 days), or outdated content.
func checkReadmeFile(fix bool) checkResult {
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return SkippedCheck(".sageox/README.md", "not in git repo", "")
	}

	// if .sageox/ doesn't exist, skip this check - user needs to run ox init
	if !isSageoxInitialized(gitRoot) {
		return SkippedCheck(".sageox/README.md", ".sageox/ not initialized", "Run `ox init` first")
	}

	sageoxDir := filepath.Join(gitRoot, ".sageox")
	readmePath := filepath.Join(sageoxDir, "README.md")

	// load project config to include links in README content
	cfg, _ := config.LoadProjectConfig(gitRoot)
	expectedContent := GetSageoxReadmeContent(cfg)

	info, err := os.Stat(readmePath)
	if err != nil {
		if fix {
			// create the README.md
			if err := os.MkdirAll(sageoxDir, 0755); err != nil {
				return checkResult{
					name:    ".sageox/README.md",
					passed:  false,
					message: "create failed",
					detail:  err.Error(),
				}
			}
			if err := os.WriteFile(readmePath, []byte(expectedContent), 0644); err != nil {
				return checkResult{
					name:    ".sageox/README.md",
					passed:  false,
					message: "write failed",
					detail:  err.Error(),
				}
			}
			return checkResult{
				name:    ".sageox/README.md",
				passed:  true,
				message: "created",
			}
		}
		// .sageox/ exists but README.md is missing - incomplete init
		return FailedCheck(".sageox/README.md", "not found", "Run `ox doctor --fix` to create it")
	}

	if info.Size() == 0 {
		if fix {
			if err := os.WriteFile(readmePath, []byte(expectedContent), 0644); err != nil {
				return checkResult{
					name:    ".sageox/README.md",
					passed:  false,
					message: "fix failed",
					detail:  err.Error(),
				}
			}
			return checkResult{
				name:    ".sageox/README.md",
				passed:  true,
				message: "fixed (was empty)",
			}
		}
		return checkResult{
			name:    ".sageox/README.md",
			passed:  false,
			message: "empty",
			detail:  "Run `ox doctor --fix` to update",
		}
	}

	// read current content to check if it matches expected
	currentContent, err := os.ReadFile(readmePath)
	if err != nil {
		return WarningCheck(".sageox/README.md", "read error", err.Error())
	}

	// check if content is outdated (different from expected template)
	// note: we compare against expected which includes links if config has IDs
	if string(currentContent) != expectedContent {
		if fix {
			if err := os.WriteFile(readmePath, []byte(expectedContent), 0644); err != nil {
				return checkResult{
					name:    ".sageox/README.md",
					passed:  true,
					warning: true,
					message: "update failed",
					detail:  err.Error(),
				}
			}
			return checkResult{
				name:    ".sageox/README.md",
				passed:  true,
				message: "updated to latest version",
			}
		}
		return checkResult{
			name:    ".sageox/README.md",
			passed:  true,
			warning: true,
			message: "outdated",
			detail:  "Run `ox doctor --fix` to update to latest version",
		}
	}

	// check if file is stale (older than 7 days) - even if content matches
	// this ensures the modtime is fresh for cache purposes
	modTime := info.ModTime()
	age := time.Since(modTime)
	sevenDays := 7 * 24 * time.Hour

	if age > sevenDays {
		daysOld := int(age.Hours() / 24)
		if fix {
			// touch the file to update modtime (content already matches)
			if err := os.WriteFile(readmePath, []byte(expectedContent), 0644); err != nil {
				return checkResult{
					name:    ".sageox/README.md",
					passed:  true,
					warning: true,
					message: "touch failed",
					detail:  err.Error(),
				}
			}
			return checkResult{
				name:    ".sageox/README.md",
				passed:  true,
				message: "refreshed",
			}
		}
		return checkResult{
			name:    ".sageox/README.md",
			passed:  true,
			warning: true,
			message: "stale",
			detail:  fmt.Sprintf("Run `ox doctor --fix` to refresh (%d days old)", daysOld),
		}
	}

	return checkResult{
		name:    ".sageox/README.md",
		passed:  true,
		message: "ok",
	}
}

// checkRepoMarker checks if a .sageox/.repo_* marker file exists.
// The marker file is created by ox init and indicates the repo has been initialized.
func checkRepoMarker() checkResult {
	repoRoot := findRepoRoot()
	if repoRoot == "" {
		return SkippedCheck(".repo_* marker", "not in a repository", "")
	}

	sageoxDir := filepath.Join(repoRoot, ".sageox")
	if _, err := os.Stat(sageoxDir); os.IsNotExist(err) {
		return SkippedCheck(".repo_* marker", ".sageox/ not initialized", "Run `ox init` first")
	}

	// look for any .repo_* file
	entries, err := os.ReadDir(sageoxDir)
	if err != nil {
		return WarningCheck(".repo_* marker", "read error", err.Error())
	}

	for _, entry := range entries {
		if !entry.IsDir() && strings.HasPrefix(entry.Name(), ".repo_") {
			return PassedCheck(".repo_* marker", "found")
		}
	}

	// .sageox/ exists but no marker - incomplete init
	return FailedCheck(".repo_* marker", "not found", "Run `ox init` to create it")
}

// repoMarkerData represents the JSON structure in .repo_* files (partial)
type repoMarkerData struct {
	RepoID   string `json:"repo_id"`
	Endpoint string `json:"endpoint"`
	// TODO: Remove after 2026-01-31 - legacy field support
	APIEndpoint string `json:"api_endpoint"` // deprecated: use Endpoint
}

// GetEndpoint returns the endpoint from the marker, preferring new field over legacy
func (m *repoMarkerData) GetEndpoint() string {
	if m.Endpoint != "" {
		return m.Endpoint
	}
	return m.APIEndpoint // fallback to legacy field
}

// checkMultipleEndpoints detects if there are multiple .repo_* files from different endpoints.
// This can happen when a repo is initialized with different SAGEOX_ENDPOINT values.
// This is informational only - no fix is needed.
func checkMultipleEndpoints() checkResult {
	repoRoot := findRepoRoot()
	if repoRoot == "" {
		return SkippedCheck("Multiple endpoints", "not in a repository", "")
	}

	sageoxDir := filepath.Join(repoRoot, ".sageox")
	if _, err := os.Stat(sageoxDir); os.IsNotExist(err) {
		return SkippedCheck("Multiple endpoints", ".sageox/ missing", "")
	}

	// find all .repo_* files
	entries, err := os.ReadDir(sageoxDir)
	if err != nil {
		return WarningCheck("Multiple endpoints", "read error", err.Error())
	}

	// collect endpoint info from each .repo_* file
	type markerInfo struct {
		filename string
		endpoint string
	}
	var markers []markerInfo

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), ".repo_") {
			continue
		}

		// read and parse the marker file
		markerPath := filepath.Join(sageoxDir, entry.Name())
		data, err := os.ReadFile(markerPath)
		if err != nil {
			continue // skip files we can't read
		}

		var marker repoMarkerData
		if err := json.Unmarshal(data, &marker); err != nil {
			continue // skip files we can't parse
		}

		ep := marker.GetEndpoint()
		if ep == "" {
			ep = "(unknown)"
		}
		markers = append(markers, markerInfo{
			filename: entry.Name(),
			endpoint: ep,
		})
	}

	// check if we have multiple markers
	if len(markers) <= 1 {
		return SkippedCheck("Multiple endpoints", "single endpoint", "")
	}

	// check if they reference different endpoints
	endpointSet := make(map[string][]string) // endpoint -> list of filenames
	for _, m := range markers {
		endpointSet[m.endpoint] = append(endpointSet[m.endpoint], m.filename)
	}

	if len(endpointSet) == 1 {
		// all markers point to same endpoint - this is fine (possibly re-init)
		return SkippedCheck("Multiple endpoints", "same endpoint", "")
	}

	// multiple different endpoints detected - build informative message
	var details []string
	for ep, files := range endpointSet {
		details = append(details, fmt.Sprintf("%s: %s", ep, strings.Join(files, ", ")))
	}

	return WarningCheck("Multiple endpoints",
		fmt.Sprintf("%d endpoints detected", len(endpointSet)),
		"This repo has been initialized with multiple API endpoints:\n       "+
			strings.Join(details, "\n       ")+"\n       "+
			"This may indicate team members using different environments (prod vs staging)")
}

// checkSageoxGitignore checks if .sageox/.gitignore exists with required entries.
// With fix=true, creates or merges missing entries (preserves user customizations).
func checkSageoxGitignore(fix bool) checkResult {
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return SkippedCheck(".sageox/.gitignore", "not in git repo", "")
	}

	// if .sageox/ doesn't exist, skip this check - user needs to run ox init
	if !isSageoxInitialized(gitRoot) {
		return SkippedCheck(".sageox/.gitignore", ".sageox/ not initialized", "Run `ox init` first")
	}

	sageoxDir := filepath.Join(gitRoot, ".sageox")
	gitignorePath := filepath.Join(sageoxDir, ".gitignore")

	content, err := os.ReadFile(gitignorePath)
	if err != nil {
		if os.IsNotExist(err) {
			if fix {
				if err := os.MkdirAll(sageoxDir, 0755); err != nil {
					return FailedCheck(".sageox/.gitignore", "create failed", err.Error())
				}
				if err := os.WriteFile(gitignorePath, []byte(sageoxGitignoreContent), 0644); err != nil {
					return FailedCheck(".sageox/.gitignore", "write failed", err.Error())
				}
				return PassedCheck(".sageox/.gitignore", "created")
			}
			// .sageox/ exists but .gitignore is missing - incomplete init
			return FailedCheck(".sageox/.gitignore", "not found", "Run `ox doctor --fix` to create it")
		}
		return FailedCheck(".sageox/.gitignore", "read error", err.Error())
	}

	// check if required entries are present (merge approach preserves user customizations)
	merged, changed := mergeGitignoreEntries(string(content))
	if changed {
		if fix {
			if err := os.WriteFile(gitignorePath, []byte(merged), 0644); err != nil {
				return FailedCheck(".sageox/.gitignore", "update failed", err.Error())
			}
			return PassedCheck(".sageox/.gitignore", "merged missing entries")
		}
		return WarningCheck(".sageox/.gitignore", "missing entries", "Run `ox doctor --fix` to add required entries")
	}

	return PassedCheck(".sageox/.gitignore", "ok")
}

// checkAPIConnectivity is skipped - connectivity is verified on-demand during API calls.
// The SageOx API's RegisterRepo call will report helpful errors if the network is unavailable.
func checkAPIConnectivity() checkResult {
	return SkippedCheck("SageOx API", "checked on demand", "")
}

// checkAPIEndpoint verifies the stored API endpoint is recorded.
// Shows the endpoint the project is registered with (informational).
// A mismatch between stored and current endpoint is NOT an error - the project
// was intentionally initialized with a specific endpoint.
func checkAPIEndpoint(_ bool) checkResult {
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return SkippedCheck("API endpoint", "not in git repo", "")
	}

	cfg, err := config.LoadProjectConfig(gitRoot)
	if err != nil {
		return SkippedCheck("API endpoint", "no config", "")
	}

	// no stored endpoint - skip check (first init or old config)
	if cfg.Endpoint == "" {
		return SkippedCheck("API endpoint", "not stored", "Run `ox init` to register endpoint")
	}

	currentEndpoint := endpoint.Get()
	if endpoint.NormalizeEndpoint(cfg.Endpoint) == currentEndpoint {
		return PassedCheck("API endpoint", cfg.Endpoint)
	}

	// different endpoint - this is informational, not an error
	// the project was initialized with a specific endpoint and that's valid
	return InfoCheck("API endpoint", cfg.Endpoint,
		fmt.Sprintf("(current default is %s)", currentEndpoint))
}

// checkTeamRegistrationWithOpts verifies the repo is registered with SageOx (has team_id).
// Returns a warning if no team_id, indicating offline initialization.
// With opts.fix=true and user consent (or opts.forceYes=true), registers the repo with SageOx.
func checkTeamRegistrationWithOpts(opts doctorOptions) checkResult {
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return SkippedCheck("Team registration", "not in git repo", "")
	}

	cfg, err := config.LoadProjectConfig(gitRoot)
	if err != nil {
		return SkippedCheck("Team registration", "no config", "")
	}

	if cfg.TeamID == "" {
		if opts.fix {
			// prompt user for consent (default No) unless forceYes is set
			shouldRegister := opts.forceYes || promptOfflineMigration(opts.forceYes)
			if shouldRegister {
				// user said yes (or forceYes was set) - register with SageOx
				if teamID, err := registerRepoWithSageOx(gitRoot, cfg); err != nil {
					return WarningCheck("Team registration", "registration failed",
						err.Error())
				} else {
					return PassedCheck("Team registration", fmt.Sprintf("registered (%s)", teamID))
				}
			}
			// user declined
			return WarningCheck("Team registration", "not registered (declined)",
				"Run `ox init` anytime to register")
		}

		return WarningCheck("Team registration", "not registered",
			"Run `ox init` to register and enable:\n"+
				"       • Session ledger (recorded AI coding sessions for the team)\n"+
				"       • Team context (shared conventions, decisions, and learnings)\n"+
				"       • Cross-repo knowledge sharing across teammates")
	}

	return PassedCheck("Team registration", cfg.TeamID)
}

// checkTeamRegistration verifies the repo is registered with SageOx (has team_id).
// Returns a warning if no team_id, indicating offline initialization.
// With fix=true and user consent, registers the repo with SageOx.
// Maintained for backwards compatibility - calls checkTeamRegistrationWithOpts internally.
func checkTeamRegistration(fix bool) checkResult {
	opts := doctorOptions{
		fix:      fix,
		forceYes: false,
	}
	return checkTeamRegistrationWithOpts(opts)
}

// promptOfflineMigration asks user if they want to register with SageOx.
// Returns true if user confirms, false otherwise. Default is No.
// If forceYes is true, returns true immediately without prompting.
func promptOfflineMigration(forceYes bool) bool {
	if forceYes {
		return true
	}

	fmt.Println()
	cli.PrintWarning("This repository is not connected to SageOx.")
	fmt.Println()
	fmt.Println("Benefits of connecting:")
	fmt.Println("  • Best practices and guidance automatically updated as standards evolve")
	fmt.Println("  • Team-specific conventions applied across all repos")
	fmt.Println("  • Personalized recommendations based on your team patterns")
	fmt.Println()
	fmt.Println("Note: Your git user name and email will be shared with SageOx to facilitate team setup.")
	fmt.Println()
	return cli.ConfirmYesNo("Register this repository with SageOx?", false)
}

// registerRepoWithSageOx registers the repo with the SageOx API.
// Returns the team_id on success, or error on failure.
// This reuses the same logic as ox init for consistency.
func registerRepoWithSageOx(gitRoot string, cfg *config.ProjectConfig) (string, error) {
	// get repo_salt from initial commit hash
	repoSalt, _ := repotools.GetInitialCommitHash()

	// get and hash remote URLs
	remoteURLs, _ := repotools.GetRemoteURLs()
	var repoRemoteHashes []string
	if repoSalt != "" && len(remoteURLs) > 0 {
		repoRemoteHashes = repotools.HashRemoteURLs(repoSalt, remoteURLs)
	}

	// get git identity
	gitIdentity, _ := repotools.DetectGitIdentity()

	// build request
	req := &api.RepoInitRequest{
		RepoID: cfg.RepoID,
		Type:   "git",
		InitAt: time.Now().UTC().Format(time.RFC3339),
	}

	if repoSalt != "" {
		req.RepoSalt = repoSalt
	}
	if len(repoRemoteHashes) > 0 {
		req.RepoRemoteHashes = repoRemoteHashes
	}
	if gitIdentity != nil {
		if gitIdentity.Email != "" {
			req.CreatedByEmail = gitIdentity.Email
		}
		if gitIdentity.Name != "" {
			req.CreatedByName = gitIdentity.Name
		}
	}

	// detect if repo is public
	isPublic, _ := repotools.IsPublicRepo()
	req.IsPublic = isPublic

	// create client and add auth token if available
	projectEndpoint := cfg.GetEndpoint()
	client := api.NewRepoClientWithEndpoint(projectEndpoint)
	if token, err := auth.GetTokenForEndpoint(projectEndpoint); err == nil && token != nil && token.AccessToken != "" {
		client.WithAuthToken(token.AccessToken)
	}

	// call API
	resp, err := client.RegisterRepo(req)
	if err != nil {
		return "", err
	}

	if resp == nil {
		return "", fmt.Errorf("API endpoint not available")
	}

	// update config with team_id
	cfg.TeamID = resp.TeamID
	if err := config.SaveProjectConfig(gitRoot, cfg); err != nil {
		return "", fmt.Errorf("failed to save team ID: %w", err)
	}

	return resp.TeamID, nil
}

// checkCloudDoctor fetches cloud-side diagnostics if available.
// Returns check results for issues the cloud can detect that the CLI cannot:
// - Pending merge conflicts (same repo registered twice) - requires cross-repo knowledge
// - Team invites pending acceptance - lives in cloud DB
// - Guidance updates available - version comparison server-side
// - Billing/quota warnings - enterprise only
// - Team-wide health (X repos need updates) - aggregate view
func checkCloudDoctor() []checkResult {
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return nil // not in repo, skip silently
	}

	cfg, err := config.LoadProjectConfig(gitRoot)
	if err != nil || cfg.RepoID == "" {
		return nil // no config or repo_id, skip silently
	}

	// create client and add auth token if available
	projectEndpoint := cfg.GetEndpoint()
	client := api.NewRepoClientWithEndpoint(projectEndpoint)
	if token, err := auth.GetTokenForEndpoint(projectEndpoint); err == nil && token != nil && token.AccessToken != "" {
		client.WithAuthToken(token.AccessToken)
	}

	// call cloud doctor API (graceful degradation built into client)
	resp, err := client.GetDoctorIssues(cfg.RepoID)
	if err != nil || resp == nil {
		// show warning that extended checks are unavailable
		return []checkResult{
			WarningCheck("Cloud doctor", "unavailable",
				"Extended cloud-side health checks could not be performed"),
		}
	}

	if len(resp.Issues) == 0 {
		return nil // no issues, skip silently
	}

	// convert cloud issues to checkResults
	var results []checkResult
	for _, issue := range resp.Issues {
		var result checkResult
		result.name = issue.Title

		// build detail with description (markdown rendered) and action URL
		detail := issue.Description
		if detail != "" {
			// render markdown description with SageOx branding
			detail = ui.RenderMarkdown(detail)
		}
		if issue.ActionURL != "" {
			if detail != "" {
				detail += "\n"
			}
			label := issue.ActionLabel
			if label == "" {
				label = "Take action"
			}
			detail += fmt.Sprintf("%s: %s", label, issue.ActionURL)
		}
		result.detail = detail

		// map severity to check status
		switch issue.Severity {
		case "error":
			result.passed = false
			result.message = issue.Type
		case "warning":
			result.passed = true
			result.warning = true
			result.message = issue.Type
		default: // "info" or unknown
			result.passed = true
			result.message = issue.Type
		}

		results = append(results, result)
	}

	return results
}

// sageoxGitattributesEntries are the entries to add to .gitattributes
var sageoxGitattributesEntries = []string{
	".sageox/** linguist-language=SageOx",
	"*.ox linguist-language=SageOx",
}

// sageoxGitattributesComment is the comment added before SageOx entries
const sageoxGitattributesComment = "# SageOx team context"

// checkGitattributes checks if .gitattributes has SageOx linguist entries.
// With fix=true, adds missing entries (preserves existing content).
func checkGitattributes(fix bool) checkResult {
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return SkippedCheck(".gitattributes", "not in git repo", "")
	}

	gitattrsPath := filepath.Join(gitRoot, ".gitattributes")

	content, err := os.ReadFile(gitattrsPath)
	if err != nil {
		if os.IsNotExist(err) {
			// file doesn't exist - this is fine, we don't create it
			// only add entries if file already exists
			return SkippedCheck(".gitattributes", "no file", "")
		}
		return FailedCheck(".gitattributes", "read error", err.Error())
	}

	// check if SageOx entries are present
	contentStr := string(content)
	missing := getMissingSageoxGitattributes(contentStr)

	if len(missing) == 0 {
		return PassedCheck(".gitattributes", "SageOx entries present")
	}

	if fix {
		// add missing entries
		newContent := addSageoxGitattributesEntries(contentStr, missing)
		if err := os.WriteFile(gitattrsPath, []byte(newContent), 0644); err != nil {
			return FailedCheck(".gitattributes", "write failed", err.Error())
		}
		return PassedCheck(".gitattributes", "added SageOx entries")
	}

	return WarningCheck(".gitattributes", "missing SageOx entries",
		"Run `ox doctor --fix` to add them")
}

// getMissingSageoxGitattributes returns which SageOx entries are missing from content
func getMissingSageoxGitattributes(content string) []string {
	var missing []string
	for _, entry := range sageoxGitattributesEntries {
		if !strings.Contains(content, entry) {
			missing = append(missing, entry)
		}
	}
	return missing
}

// addSageoxGitattributesEntries appends missing SageOx entries to gitattributes content
func addSageoxGitattributesEntries(content string, missing []string) string {
	if len(missing) == 0 {
		return content
	}

	result := strings.TrimRight(content, "\n")
	if result != "" {
		result += "\n\n"
	}
	result += sageoxGitattributesComment + "\n"
	for _, entry := range missing {
		result += entry + "\n"
	}
	return result
}

// EnsureGitattributes adds SageOx entries to .gitattributes, creating the file if needed.
// Called during ox init. Returns true if entries were added or file was created.
func EnsureGitattributes(gitRoot string) (added bool, err error) {
	gitattrsPath := filepath.Join(gitRoot, ".gitattributes")

	content, err := os.ReadFile(gitattrsPath)
	if err != nil {
		if os.IsNotExist(err) {
			// file doesn't exist - create it with SageOx entries
			newContent := addSageoxGitattributesEntries("", sageoxGitattributesEntries)
			if err := os.WriteFile(gitattrsPath, []byte(newContent), 0644); err != nil {
				return false, err
			}
			return true, nil
		}
		return false, err
	}

	contentStr := string(content)
	missing := getMissingSageoxGitattributes(contentStr)

	if len(missing) == 0 {
		return false, nil // already present
	}

	newContent := addSageoxGitattributesEntries(contentStr, missing)
	if err := os.WriteFile(gitattrsPath, []byte(newContent), 0644); err != nil {
		return false, err
	}

	return true, nil
}

// checkMarkerCachedURLs verifies that the marker file has cached URLs for validation and reconnection.
// With fix=true, attempts to refresh URLs from git credentials or API.
func checkMarkerCachedURLs(fix bool) checkResult {
	repoRoot := findRepoRoot()
	if repoRoot == "" {
		return SkippedCheck("Cached URLs", "not in a repository", "")
	}

	sageoxDir := filepath.Join(repoRoot, ".sageox")
	if _, err := os.Stat(sageoxDir); os.IsNotExist(err) {
		return SkippedCheck("Cached URLs", ".sageox/ not initialized", "Run `ox init` first")
	}

	// check if marker has cached URLs
	currentEndpoint := endpoint.Get()
	urls, err := ReadMarkerURLs(sageoxDir, currentEndpoint)
	if err != nil {
		return WarningCheck("Cached URLs", "read error", err.Error())
	}

	// if URLs are present, validate them
	if urls != nil && urls.LedgerURL != "" {
		// check if URLs match git credentials (source of truth)
		creds, credErr := gitserver.LoadCredentialsForEndpoint(currentEndpoint)
		if credErr == nil && creds != nil {
			mismatch := false
			var mismatchDetails []string

			// check team URLs
			for _, repo := range creds.Repos {
				if repo.Type == "team-context" {
					teamID := strings.TrimPrefix(repo.Name, "team-")
					if cachedURL, ok := urls.TeamURLs[teamID]; ok {
						if cachedURL != repo.URL {
							mismatch = true
							mismatchDetails = append(mismatchDetails, fmt.Sprintf("team %s URL outdated", teamID))
						}
					} else {
						// team exists in creds but not in marker
						mismatch = true
						mismatchDetails = append(mismatchDetails, fmt.Sprintf("team %s URL missing", teamID))
					}
				}
			}

			if mismatch {
				if fix {
					// update marker with current credentials
					cfg, cfgErr := config.LoadProjectConfig(repoRoot)
					if cfgErr != nil || cfg.RepoID == "" {
						return WarningCheck("Cached URLs", "outdated (no repo_id)", strings.Join(mismatchDetails, ", "))
					}
					if err := updateMarkerWithCachedURLs(sageoxDir, cfg.RepoID, currentEndpoint); err != nil {
						return WarningCheck("Cached URLs", "update failed", err.Error())
					}
					return PassedCheck("Cached URLs", "refreshed")
				}
				return WarningCheck("Cached URLs", "outdated",
					strings.Join(mismatchDetails, ", ")+"\n       Run `ox doctor --fix` to refresh")
			}
		}

		return PassedCheck("Cached URLs", "present")
	}

	// no cached URLs - check if we can populate them
	cfg, cfgErr := config.LoadProjectConfig(repoRoot)
	if cfgErr != nil || cfg.RepoID == "" {
		return SkippedCheck("Cached URLs", "no repo_id", "Run `ox init` first")
	}

	if fix {
		// try to populate from git credentials
		if err := updateMarkerWithCachedURLs(sageoxDir, cfg.RepoID, currentEndpoint); err != nil {
			// no credentials available - not an error, just informational
			return WarningCheck("Cached URLs", "not available",
				"Sign in with `ox login` to enable URL caching for offline reconnection")
		}
		return PassedCheck("Cached URLs", "populated")
	}

	// check if git credentials exist but URLs weren't cached
	creds, credErr := gitserver.LoadCredentialsForEndpoint(currentEndpoint)
	if credErr == nil && creds != nil && len(creds.Repos) > 0 {
		return WarningCheck("Cached URLs", "missing",
			"Run `ox doctor --fix` to cache URLs for offline reconnection")
	}

	// no credentials, no URLs - informational only
	return SkippedCheck("Cached URLs", "no credentials",
		"Sign in with `ox login` to enable URL caching")
}

// checkEndpointConsistency verifies that the project endpoint matches the endpoints
// used in local team context and ledger paths.
//
// This detects mismatches like:
//   - Team context path is in ~/.local/share/sageox/sageox.ai/teams/ but project endpoint is localhost
//   - Ledger path is in a different endpoint's directory than the project endpoint
//
// With fix=true, offers to migrate paths to the correct endpoint directory.
func checkEndpointConsistency(fix bool) checkResult {
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return SkippedCheck("Endpoint consistency", "not in git repo", "")
	}

	// get the project endpoint (from config or env)
	projectEndpoint := endpoint.GetForProject(gitRoot)
	projectSlug := endpoint.NormalizeSlug(projectEndpoint)

	// load local config to check paths
	localCfg, err := config.LoadLocalConfig(gitRoot)
	if err != nil {
		return SkippedCheck("Endpoint consistency", "config error", "")
	}

	var mismatches []string

	// check team context paths
	for _, tc := range localCfg.TeamContexts {
		if tc.Path == "" {
			continue
		}
		pathSlug := paths.EndpointSlug(tc.Path)
		if pathSlug != "" && pathSlug != projectSlug {
			teamLabel := tc.TeamID
			if tc.TeamName != "" {
				teamLabel = tc.TeamName
			}
			mismatches = append(mismatches, fmt.Sprintf("Team context %s uses: %s", teamLabel, pathSlug))
		}
	}

	// check ledger path
	if localCfg.Ledger != nil && localCfg.Ledger.Path != "" {
		pathSlug := paths.EndpointSlug(localCfg.Ledger.Path)
		if pathSlug != "" && pathSlug != projectSlug {
			mismatches = append(mismatches, fmt.Sprintf("Ledger path uses: %s", pathSlug))
		}
	}

	// no mismatches found
	if len(mismatches) == 0 {
		return PassedCheck("Endpoint consistency", fmt.Sprintf("all components use %s", projectSlug))
	}

	// mismatches detected
	if fix {
		fmt.Println()
		cli.PrintWarning("Endpoint consistency mismatch detected")
		fmt.Printf("  Project endpoint: %s\n", projectSlug)
		for _, m := range mismatches {
			fmt.Printf("  %s\n", m)
		}
		fmt.Println()
		fmt.Println("  This can happen when switching between environments (dev/staging/prod)")
		fmt.Println("  or when SAGEOX_ENDPOINT was changed after initial setup.")
		fmt.Println()
		fmt.Println("  To fix, you can:")
		fmt.Println("    1. Set SAGEOX_ENDPOINT to match your existing paths")
		fmt.Println("    2. Re-run `ox init` to register with the current endpoint")
		fmt.Println("    3. Manually move paths to the correct endpoint directory")
		fmt.Println()

		// we don't auto-migrate paths as that could cause data loss
		// instead, provide guidance
		return WarningCheck("Endpoint consistency", "mismatch detected",
			fmt.Sprintf("Project endpoint: %s\n       %s\n       Run `ox init` to re-register or set SAGEOX_ENDPOINT to match",
				projectSlug, strings.Join(mismatches, "\n       ")))
	}

	return FailedCheck("Endpoint consistency", "mismatch detected",
		fmt.Sprintf("Project endpoint: %s\n       %s\n       Run `ox doctor --fix` to see migration options",
			projectSlug, strings.Join(mismatches, "\n       ")))
}

// checkEndpointNormalization detects stored endpoints with subdomain prefixes
// (api., www., app., git.) in the project config, auth store, and marker files.
// These prefixes are stripped by endpoint.NormalizeEndpoint() but may linger in
// config files written by older CLI versions.
//
// With fix=true, rewrites prefixed values to their normalized form.
func checkEndpointNormalization(fix bool) checkResult {
	const checkName = "Endpoint normalization"

	gitRoot := findGitRoot()

	var issues []string
	var fixed []string

	// 1. Check project config endpoint (read raw JSON to detect prefixed values on disk,
	//    since LoadProjectConfig normalizes defensively)
	if gitRoot != "" {
		configPath := filepath.Join(gitRoot, ".sageox", "config.json")
		if rawCfgData, readErr := os.ReadFile(configPath); readErr == nil && len(rawCfgData) > 0 {
			var rawCfg struct {
				Endpoint string `json:"endpoint"`
			}
			if json.Unmarshal(rawCfgData, &rawCfg) == nil && rawCfg.Endpoint != "" {
				normalized := endpoint.NormalizeEndpoint(rawCfg.Endpoint)
				if normalized != rawCfg.Endpoint {
					prefixed := rawCfg.Endpoint
					if fix {
						cfg, loadErr := config.LoadProjectConfig(gitRoot)
						if loadErr == nil && cfg != nil {
							cfg.Endpoint = normalized
							if saveErr := config.SaveProjectConfig(gitRoot, cfg); saveErr != nil {
								issues = append(issues, fmt.Sprintf("config.json: %s (fix failed: %s)", prefixed, saveErr))
							} else {
								fixed = append(fixed, fmt.Sprintf("config.json: %s -> %s", prefixed, normalized))
							}
						} else {
							issues = append(issues, fmt.Sprintf("config.json: %s (load failed: %v)", prefixed, loadErr))
						}
					} else {
						issues = append(issues, fmt.Sprintf("config.json endpoint: %s", prefixed))
					}
				}
			}
		}
	}

	// 2. Check auth store token keys (read raw JSON to detect prefixed keys on disk)
	authPath, authErr := auth.GetAuthFilePath()
	if authErr == nil {
		rawData, readErr := os.ReadFile(authPath)
		if readErr == nil && len(rawData) > 0 {
			var rawStore struct {
				Tokens map[string]json.RawMessage `json:"tokens"`
			}
			if json.Unmarshal(rawData, &rawStore) == nil && rawStore.Tokens != nil {
				var prefixedKeys []string
				for key := range rawStore.Tokens {
					normalized := endpoint.NormalizeEndpoint(key)
					if normalized != key {
						prefixedKeys = append(prefixedKeys, key)
					}
				}
				if len(prefixedKeys) > 0 {
					if fix {
						for _, oldKey := range prefixedKeys {
							newKey := endpoint.NormalizeEndpoint(oldKey)
							rawStore.Tokens[newKey] = rawStore.Tokens[oldKey]
							delete(rawStore.Tokens, oldKey)
						}
						updatedData, marshalErr := json.MarshalIndent(rawStore, "", "  ")
						if marshalErr != nil {
							issues = append(issues, fmt.Sprintf("auth.json: %d prefixed key(s) (fix failed: %s)", len(prefixedKeys), marshalErr))
						} else {
							if writeErr := os.WriteFile(authPath, updatedData, 0600); writeErr != nil {
								issues = append(issues, fmt.Sprintf("auth.json: %d prefixed key(s) (write failed: %s)", len(prefixedKeys), writeErr))
							} else {
								for _, k := range prefixedKeys {
									fixed = append(fixed, fmt.Sprintf("auth.json key: %s -> %s", k, endpoint.NormalizeEndpoint(k)))
								}
							}
						}
					} else {
						for _, k := range prefixedKeys {
							issues = append(issues, fmt.Sprintf("auth.json key: %s", k))
						}
					}
				}
			}
		}
	}

	// 3. Check marker file endpoint fields
	if gitRoot != "" {
		sageoxDir := filepath.Join(gitRoot, ".sageox")
		entries, err := os.ReadDir(sageoxDir)
		if err == nil {
			for _, entry := range entries {
				if entry.IsDir() || !strings.HasPrefix(entry.Name(), ".repo_") {
					continue
				}

				markerPath := filepath.Join(sageoxDir, entry.Name())
				data, readErr := os.ReadFile(markerPath)
				if readErr != nil {
					continue
				}

				var marker repoMarkerData
				if json.Unmarshal(data, &marker) != nil {
					continue
				}

				storedEP := marker.GetEndpoint()
				if storedEP == "" {
					continue
				}

				normalized := endpoint.NormalizeEndpoint(storedEP)
				if normalized == storedEP {
					continue
				}

				if fix {
					// rewrite the marker preserving all fields via generic map
					var fullMarker map[string]json.RawMessage
					if json.Unmarshal(data, &fullMarker) != nil {
						issues = append(issues, fmt.Sprintf("%s: %s (parse failed)", entry.Name(), storedEP))
						continue
					}

					normalizedJSON, _ := json.Marshal(normalized)
					fullMarker["endpoint"] = normalizedJSON

					// also normalize the legacy api_endpoint field if present and prefixed
					if marker.APIEndpoint != "" {
						apiNormalized := endpoint.NormalizeEndpoint(marker.APIEndpoint)
						if apiNormalized != marker.APIEndpoint {
							apiJSON, _ := json.Marshal(apiNormalized)
							fullMarker["api_endpoint"] = apiJSON
						}
					}

					updatedData, marshalErr := json.MarshalIndent(fullMarker, "", "  ")
					if marshalErr != nil {
						issues = append(issues, fmt.Sprintf("%s: %s (marshal failed: %s)", entry.Name(), storedEP, marshalErr))
						continue
					}
					if writeErr := os.WriteFile(markerPath, updatedData, 0644); writeErr != nil {
						issues = append(issues, fmt.Sprintf("%s: %s (write failed: %s)", entry.Name(), storedEP, writeErr))
					} else {
						fixed = append(fixed, fmt.Sprintf("%s: %s -> %s", entry.Name(), storedEP, normalized))
					}
				} else {
					issues = append(issues, fmt.Sprintf("%s endpoint: %s", entry.Name(), storedEP))
				}
			}
		}
	}

	// no prefixed endpoints found anywhere
	if len(issues) == 0 && len(fixed) == 0 {
		return PassedCheck(checkName, "all endpoints normalized")
	}

	// fix mode completed successfully
	if fix && len(issues) == 0 {
		return PassedCheck(checkName, fmt.Sprintf("normalized %d endpoint(s)", len(fixed)))
	}

	// fix mode with partial failures
	if fix && len(issues) > 0 {
		return WarningCheck(checkName,
			fmt.Sprintf("normalized %d, %d failed", len(fixed), len(issues)),
			strings.Join(issues, "\n       "))
	}

	// detection only (no --fix)
	return WarningCheck(checkName,
		fmt.Sprintf("%d prefixed endpoint(s) found", len(issues)),
		strings.Join(issues, "\n       ")+"\n       Run `ox doctor --fix` to normalize")
}

// checkSiblingWithoutInit detects sibling directory existing without project initialization.
// This can happen when artifacts were created before ox init was run, which indicates
// a potential bug or manual creation of the sibling directory.
func checkSiblingWithoutInit() checkResult {
	gitRoot := findGitRoot()
	if gitRoot == "" {
		return SkippedCheck("Sibling directory", "not in git repo", "")
	}

	// get repo name from git root
	repoName := filepath.Base(gitRoot)

	// compute the sibling directory path using canonical function
	siblingDir := config.DefaultSageoxSiblingDir(repoName, gitRoot)
	if siblingDir == "" {
		return SkippedCheck("Sibling directory", "could not determine path", "")
	}

	// check if sibling directory exists
	siblingExists := false
	if info, err := os.Stat(siblingDir); err == nil && info.IsDir() {
		siblingExists = true
	}

	// check if project is initialized
	isInitialized := config.IsInitialized(gitRoot)

	// if sibling exists but NOT initialized, return a warning
	if siblingExists && !isInitialized {
		return WarningCheck("Sibling directory",
			"exists but project not initialized",
			fmt.Sprintf("Found %s but no .sageox/config.json. Run `ox init` or remove sibling directory", siblingDir))
	}

	// if both exist or neither exist, that's expected
	if siblingExists && isInitialized {
		return PassedCheck("Sibling directory", "consistent with init")
	}

	// neither exists - skip (normal state before init)
	return SkippedCheck("Sibling directory", "not present", "")
}
