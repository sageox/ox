package main

import (
	"bufio"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/repotools"
	"github.com/sageox/ox/internal/ui"
	"github.com/sageox/ox/internal/uninstall"
	"github.com/spf13/cobra"
)

var (
	uninstallUserIntegrations bool
	uninstallForce            bool
	uninstallDryRun           bool
	uninstallAll              bool // uninstall from all endpoints
	uninstallLocalOnly        bool // skip cloud notification, don't require auth
)

var uninstallCmd = &cobra.Command{
	Use:   "uninstall",
	Short: "Remove SageOx from this repository",
	Long: `Completely remove SageOx from this repository by removing the .sageox directory,
git hooks, and agent integration files.

WARNING: This is a destructive operation and cannot be undone.

This command will:
1. Remove .sageox/ directory and all contents
2. Remove git hooks (prepare-commit-msg, etc.)
3. Clean up agent integration files (.claude/settings.json, etc.)
4. Remove 'ox agent prime' from AGENTS.md/CLAUDE.md
5. Optionally remove user-level integration hooks

If multiple endpoints are configured, you can select which endpoint to uninstall from,
or use --all to remove from all endpoints.

Files tracked in git will be unstaged but not deleted from the working tree
unless explicitly requested.`,
	Example: `  # Preview what will be removed (recommended first step)
  ox uninstall --dry-run

  # Uninstall SageOx from current repository
  ox uninstall

  # Uninstall from all configured endpoints
  ox uninstall --all

  # Uninstall including user-level hooks
  ox uninstall --user-integrations

  # Local-only uninstall (skips cloud notification, doesn't require login)
  ox uninstall --local-only

  # Non-interactive uninstall for CI/automation
  ox uninstall --force`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runUninstall()
	},
}

func init() {
	uninstallCmd.Flags().BoolVar(&uninstallUserIntegrations, "user-integrations", false, "also remove user-level integration hooks (default: false)")
	uninstallCmd.Flags().BoolVar(&uninstallForce, "force", false, "skip confirmation prompt for non-interactive use (default: false)")
	uninstallCmd.Flags().BoolVar(&uninstallDryRun, "dry-run", false, "preview what would be removed without making changes (default: false)")
	uninstallCmd.Flags().BoolVar(&uninstallAll, "all", false, "uninstall from all configured endpoints (default: false)")
	uninstallCmd.Flags().BoolVar(&uninstallLocalOnly, "local-only", false, "skip cloud notification (doesn't require login, but cloud resources won't be cleaned up)")
	rootCmd.AddCommand(uninstallCmd)
}

func runUninstall() error {
	// require git to be installed
	if err := repotools.RequireVCS(repotools.VCSGit); err != nil {
		return fmt.Errorf("ox uninstall requires git: %w", err)
	}

	// find git root
	gitRoot, err := repotools.FindRepoRoot(repotools.VCSGit)
	if err != nil {
		return fmt.Errorf("failed to find git repository: %w", err)
	}

	// check if SageOx is actually installed
	sageoxDir := filepath.Join(gitRoot, ".sageox")
	if _, err := os.Stat(sageoxDir); os.IsNotExist(err) {
		cli.PrintError("SageOx is not installed in this repository")
		fmt.Fprintln(os.Stderr)
		fmt.Fprintf(os.Stderr, "No .sageox directory found at: %s\n", sageoxDir)
		fmt.Fprintln(os.Stderr)
		fmt.Fprintf(os.Stderr, "If you want to initialize SageOx, run: %s\n", cli.StyleCommand.Render("ox init"))
		return fmt.Errorf("sageox not installed")
	}

	// read repo marker data before uninstall (for cloud notification)
	// this must happen before we delete the .sageox directory
	markerData, _ := api.ReadFirstRepoMarker(sageoxDir)
	if markerData != nil {
		slog.Debug("uninstall marker", "repo_id", markerData.RepoID, "endpoint", markerData.GetEndpoint())
	}

	// get configured endpoints
	endpoints := config.GetConfiguredEndpoints(gitRoot)

	// determine which endpoint(s) to uninstall from
	selectedEndpoint, uninstallAllEndpoints, err := selectEndpointForUninstall(gitRoot, endpoints)
	if err != nil {
		if err.Error() == "uninstall canceled" {
			fmt.Fprintln(os.Stderr)
			cli.PrintSuccess("Uninstall canceled")
			return nil
		}
		return err
	}

	slog.Info("uninstall", "git_root", gitRoot, "dry_run", uninstallDryRun, "force", uninstallForce, "endpoint", selectedEndpoint, "all_endpoints", uninstallAllEndpoints)

	// show preview of what will be removed
	if err := showPreview(gitRoot); err != nil {
		return fmt.Errorf("failed to generate preview: %w", err)
	}

	// show endpoint scope
	if len(endpoints) > 1 {
		fmt.Fprintln(os.Stderr)
		if uninstallAllEndpoints {
			fmt.Println(cli.StyleBold.Render("Scope: All endpoints"))
			for _, ep := range endpoints {
				fmt.Printf("  %s %s\n", ui.MutedStyle.Render("•"), endpoint.NormalizeSlug(ep))
			}
		} else {
			fmt.Printf("%s %s\n", cli.StyleBold.Render("Scope:"), endpoint.NormalizeSlug(selectedEndpoint))
		}
	}

	// if dry-run, stop here
	if uninstallDryRun {
		fmt.Fprintln(os.Stderr)
		cli.PrintSuccess("Dry run complete. No changes were made.")
		fmt.Fprintf(os.Stderr, "Run without %s to perform uninstall.\n", cli.StyleFlag.Render("--dry-run"))
		return nil
	}

	// check authentication unless --local-only
	if !uninstallLocalOnly {
		authErr := checkUninstallAuth(endpoints, uninstallAllEndpoints, selectedEndpoint)
		if authErr != nil {
			return authErr
		}
	} else {
		// warn about local-only mode
		fmt.Fprintln(os.Stderr)
		fmt.Println(cli.StyleWarning.Bold(true).Render("⚠ LOCAL-ONLY MODE"))
		fmt.Println(cli.StyleWarning.Render("Cloud resources will NOT be cleaned up."))
		fmt.Println(cli.StyleWarning.Render("Team admins may need to manually remove this repo from SageOx."))
		fmt.Fprintln(os.Stderr)
	}

	// confirm with user unless --force
	if !uninstallForce {
		repoName := filepath.Base(gitRoot)
		if !confirmUninstallWithInput(repoName, selectedEndpoint, uninstallAllEndpoints, gitRoot) {
			fmt.Fprintln(os.Stderr)
			cli.PrintSuccess("Uninstall canceled")
			return nil
		}
	} else {
		slog.Info("uninstall confirmation", "skipped", "force flag enabled")
	}

	// perform uninstall steps
	fmt.Fprintln(os.Stderr)
	fmt.Println(cli.StyleBold.Render("Uninstalling SageOx..."))
	fmt.Fprintln(os.Stderr)

	if err := removeSageoxDir(gitRoot); err != nil {
		return fmt.Errorf("failed to remove .sageox directory: %w", err)
	}

	if err := removeHooks(gitRoot); err != nil {
		// non-fatal: log warning and continue
		slog.Warn("failed to remove hooks", "error", err)
		cli.PrintWarning(fmt.Sprintf("Could not remove all hooks: %v", err))
	}

	if err := cleanupAgentFiles(gitRoot); err != nil {
		// non-fatal: log warning and continue
		slog.Warn("failed to cleanup agent files", "error", err)
		cli.PrintWarning(fmt.Sprintf("Could not cleanup all agent files: %v", err))
	}

	if uninstallUserIntegrations {
		if err := removeUserIntegrationsWithConfirmation(); err != nil {
			// non-fatal: log warning and continue
			slog.Warn("failed to remove user integrations", "error", err)
			cli.PrintWarning(fmt.Sprintf("Could not remove user integrations: %v", err))
		}
	}

	// notify cloud of uninstall (unless --local-only)
	if !uninstallLocalOnly {
		notifyCloudUninstall(markerData)
	}

	// success message
	fmt.Fprintln(os.Stderr)
	fmt.Println(cli.StyleBold.Render("SageOx uninstalled successfully"))
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "The .sageox directory and related files have been removed.")
	if uninstallUserIntegrations {
		fmt.Fprintln(os.Stderr, "User-level integrations have been removed.")
	}

	// display cloud resources notice for local-only mode
	// (normal mode shows details in notifyCloudUninstall)
	if uninstallLocalOnly {
		fmt.Fprintln(os.Stderr)
		fmt.Println(cli.StyleWarning.Render("⚠ Cloud resources were NOT cleaned up (--local-only mode)."))
		fmt.Fprintln(os.Stderr, cli.StyleDim.Render("Team admins may need to manually remove this repo from SageOx."))
	}

	fmt.Fprintln(os.Stderr)
	fmt.Fprintf(os.Stderr, "To reinstall SageOx, run: %s\n", cli.StyleCommand.Render("ox init"))

	return nil
}

// notifyCloudUninstall sends notification to the cloud API about the local uninstall.
// This requires authentication to ensure only authorized team members can trigger
// cloud-side uninstallation workflows. If the user is not logged in, a warning is
// displayed but local uninstall proceeds.
func notifyCloudUninstall(marker *api.RepoMarkerData) {
	if marker == nil || marker.RepoID == "" {
		slog.Debug("uninstall cloud notification", "skipped", "no marker data")
		return
	}

	ep := marker.GetEndpoint()
	if ep == "" {
		slog.Debug("uninstall cloud notification", "skipped", "no endpoint in marker")
		return
	}

	// get auth token for this endpoint
	token, err := auth.GetTokenForEndpoint(ep)
	if err != nil {
		slog.Warn("uninstall cloud notification", "skipped", "failed to get auth token", "error", err)
		fmt.Println(cli.StyleWarning.Render("⚠ Could not notify cloud (auth error). Cloud resources may need manual cleanup."))
		return
	}

	if token == nil || token.AccessToken == "" {
		slog.Warn("uninstall cloud notification", "skipped", "not authenticated")
		fmt.Println(cli.StyleWarning.Render("⚠ Not logged in. Cloud resources will not be cleaned up."))
		fmt.Printf("  %s %s %s\n", cli.StyleDim.Render("Run"), cli.StyleCommand.Render("ox login"), cli.StyleDim.Render("to authenticate before uninstalling."))
		return
	}

	slog.Info("uninstall cloud notification", "repo_id", marker.RepoID, "endpoint", ep)

	// create authenticated client
	client := api.NewRepoClientWithEndpoint(ep).WithAuthToken(token.AccessToken)

	// notify cloud - errors are logged but don't block local uninstall
	if err := client.NotifyUninstall(marker.RepoID, marker.RepoSalt); err != nil {
		slog.Warn("uninstall cloud notification failed", "error", err)
		fmt.Println(cli.StyleWarning.Render("⚠ Cloud notification failed. Cloud resources may need manual cleanup."))
	} else {
		fmt.Println(cli.StyleSuccess.Render("✓ Uninstall request submitted"))
		fmt.Fprintln(os.Stderr)
		fmt.Println(cli.StyleDim.Render("Team admins will receive an email to confirm deletion of cloud resources."))
		statusURL := fmt.Sprintf("https://%s/repos/%s/uninstall", endpoint.NormalizeSlug(ep), marker.RepoID)
		fmt.Printf("%s %s %s\n", cli.StyleDim.Render("Visit"), cli.StyleCommand.Render(statusURL), cli.StyleDim.Render("to check status."))

		// offer to open browser for immediate confirmation
		fmt.Fprintln(os.Stderr)
		if promptConfirmCloudDeletion() {
			confirmURL := fmt.Sprintf("https://%s/repos/%s/uninstall/confirm", endpoint.NormalizeSlug(ep), marker.RepoID)
			if err := openBrowserForUninstall(confirmURL); err != nil {
				slog.Warn("failed to open browser", "error", err)
				fmt.Println(cli.StyleWarning.Render("⚠ Could not open browser."))
				fmt.Printf("%s %s\n", cli.StyleDim.Render("Open manually:"), cli.StyleCommand.Render(confirmURL))
			} else {
				fmt.Println(cli.StyleSuccess.Render("✓ Opened browser for confirmation"))
			}
		}
	}
}

// promptConfirmCloudDeletion asks the user if they want to confirm cloud resource deletion now.
// Returns true if user wants to proceed, false otherwise. Default is No.
func promptConfirmCloudDeletion() bool {
	fmt.Printf("%s [yN]: ", cli.StyleDim.Render("Confirm cloud resource deletion now?"))

	reader := bufio.NewReader(os.Stdin)
	input, err := reader.ReadString('\n')
	if err != nil {
		return false
	}

	input = strings.TrimSpace(strings.ToLower(input))
	return input == "y" || input == "yes"
}

// openBrowserForUninstall opens the given URL in the default browser.
// SKIP_BROWSER and headless detection are handled inside cli.OpenInBrowser.
func openBrowserForUninstall(url string) error {
	return cli.OpenInBrowser(url)
}

// checkUninstallAuth verifies the user is authenticated for the endpoint(s) being uninstalled.
// Returns an error with instructions if not authenticated.
func checkUninstallAuth(endpoints []string, allEndpoints bool, selectedEndpoint string) error {
	var endpointsToCheck []string

	if allEndpoints {
		endpointsToCheck = endpoints
	} else if selectedEndpoint != "" {
		endpointsToCheck = []string{selectedEndpoint}
	} else {
		// fall back to default endpoint
		endpointsToCheck = []string{endpoint.Get()}
	}

	// check auth for each endpoint
	var unauthEndpoints []string
	for _, ep := range endpointsToCheck {
		token, err := auth.GetTokenForEndpoint(ep)
		if err != nil || token == nil || token.AccessToken == "" {
			unauthEndpoints = append(unauthEndpoints, endpoint.NormalizeSlug(ep))
		}
	}

	if len(unauthEndpoints) == 0 {
		return nil // all endpoints authenticated
	}

	// show error with instructions
	fmt.Fprintln(os.Stderr)
	cli.PrintError("Authentication required to uninstall")
	fmt.Fprintln(os.Stderr)
	fmt.Println(cli.StyleDim.Render("Uninstalling notifies the SageOx cloud to clean up resources and"))
	fmt.Println(cli.StyleDim.Render("coordinate with team admins. This requires you to be logged in."))
	fmt.Fprintln(os.Stderr)

	if len(unauthEndpoints) == 1 {
		fmt.Printf("%s %s\n", cli.StyleWarning.Render("Not authenticated for:"), cli.StyleCommand.Render(unauthEndpoints[0]))
	} else {
		fmt.Println(cli.StyleWarning.Render("Not authenticated for:"))
		for _, ep := range unauthEndpoints {
			fmt.Printf("  %s %s\n", cli.StyleDim.Render("•"), cli.StyleCommand.Render(ep))
		}
	}

	fmt.Fprintln(os.Stderr)
	fmt.Printf("To authenticate, run: %s\n", cli.StyleCommand.Render("ox login"))
	fmt.Fprintln(os.Stderr)
	fmt.Println(cli.StyleDim.Render("To force local-only uninstall (cloud resources will NOT be cleaned up):"))
	fmt.Printf("  %s\n", cli.StyleCommand.Render("ox uninstall --local-only"))
	fmt.Fprintln(os.Stderr)

	return fmt.Errorf("authentication required")
}

// selectEndpointForUninstall handles endpoint selection when multiple endpoints are configured.
// Returns: selected endpoint, whether to uninstall from all endpoints, error.
// If only one endpoint exists or --all flag is set, returns immediately.
func selectEndpointForUninstall(gitRoot string, endpoints []string) (string, bool, error) {
	// handle --all flag
	if uninstallAll {
		if len(endpoints) == 0 {
			return endpoint.Get(), true, nil
		}
		return "", true, nil
	}

	// if no endpoints configured, use current/default
	if len(endpoints) == 0 {
		return endpoint.GetForProject(gitRoot), false, nil
	}

	// if only one endpoint, use it without prompting
	if len(endpoints) == 1 {
		return endpoints[0], false, nil
	}

	// multiple endpoints - prompt for selection (unless --force)
	if uninstallForce {
		// with --force, uninstall from all endpoints
		return "", true, nil
	}

	// build options for selection
	options := make([]string, 0, len(endpoints)+1)
	for _, ep := range endpoints {
		slug := endpoint.NormalizeSlug(ep)
		if endpoint.IsProduction(ep) {
			options = append(options, fmt.Sprintf("%s (production)", slug))
		} else {
			options = append(options, slug)
		}
	}
	options = append(options, "All endpoints")

	fmt.Fprintln(os.Stderr)
	fmt.Println(cli.StyleBold.Render("Multiple endpoints configured"))
	fmt.Println(ui.MutedStyle.Render("Select which endpoint to uninstall from:"))
	fmt.Fprintln(os.Stderr)

	selectedIdx, err := cli.SelectOne("Select endpoint", options, 0)
	if err != nil {
		return "", false, fmt.Errorf("uninstall canceled")
	}

	// check if "All endpoints" was selected
	if selectedIdx == len(endpoints) {
		return "", true, nil
	}

	return endpoints[selectedIdx], false, nil
}

// confirmUninstallWithInput prompts user for confirmation by typing repo name or "uninstall"
// returns true if user confirms, false otherwise
func confirmUninstallWithInput(repoName, selectedEndpoint string, allEndpoints bool, gitRoot string) bool {
	fmt.Fprintln(os.Stderr)
	fmt.Println(cli.StyleError.Bold(true).Render("DANGER ZONE"))
	fmt.Fprintln(os.Stderr)
	fmt.Println(cli.StyleWarning.Render("You are about to permanently uninstall SageOx from this repository."))
	fmt.Fprintln(os.Stderr)
	fmt.Println("This will:")
	fmt.Println(cli.StyleError.Render("  • Delete .sageox/ directory and all session history"))
	fmt.Println(cli.StyleError.Render("  • Remove SageOx git hooks"))
	fmt.Println(cli.StyleError.Render("  • Clean up AGENTS.md/CLAUDE.md references"))
	fmt.Println(cli.StyleError.Render("  • Notify cloud to begin uninstallation workflow"))
	fmt.Fprintln(os.Stderr)

	// check for ledger and warn about backup
	showLedgerBackupWarning(gitRoot)

	if allEndpoints {
		fmt.Println(cli.StyleError.Bold(true).Render("This will uninstall from ALL configured endpoints."))
	} else if selectedEndpoint != "" {
		slug := endpoint.NormalizeSlug(selectedEndpoint)
		fmt.Printf("Endpoint: %s\n", cli.StyleCommand.Render(slug))
	}

	fmt.Fprintln(os.Stderr)
	fmt.Println(cli.StyleError.Bold(true).Render("This affects ALL users of this repository, not just you."))
	fmt.Fprintln(os.Stderr)
	fmt.Printf("To confirm, type %s or %s:\n",
		cli.StyleCommand.Render(repoName),
		cli.StyleCommand.Render("uninstall"))
	fmt.Fprintln(os.Stderr)
	fmt.Print("> ")

	reader := bufio.NewReader(os.Stdin)
	input, err := reader.ReadString('\n')
	if err != nil {
		slog.Error("failed to read confirmation input", "error", err)
		return false
	}

	input = strings.TrimSpace(input)
	if input != repoName && strings.ToLower(input) != "uninstall" {
		fmt.Fprintln(os.Stderr)
		fmt.Println(cli.StyleError.Render("Confirmation failed."))
		fmt.Printf("Expected: %s or %s, got: %s\n", repoName, "uninstall", input)
		return false
	}

	slog.Info("uninstall confirmed", "repo", repoName, "input", input)
	return true
}

// showLedgerBackupWarning checks if a ledger exists and warns the user to back it up
func showLedgerBackupWarning(gitRoot string) {
	// check if local config has ledger configured
	localCfg, err := config.LoadLocalConfig(gitRoot)
	if err != nil {
		return // no config, no ledger
	}

	if localCfg.Ledger == nil || localCfg.Ledger.Path == "" {
		return // no ledger configured
	}

	ledgerPath := localCfg.Ledger.Path
	if !filepath.IsAbs(ledgerPath) {
		ledgerPath = filepath.Join(gitRoot, ".sageox", ledgerPath)
	}

	// check if ledger exists
	if _, err := os.Stat(ledgerPath); os.IsNotExist(err) {
		return // ledger doesn't exist
	}

	// warn the user
	fmt.Println(cli.StyleWarning.Bold(true).Render("⚠ LEDGER DATA WARNING"))
	fmt.Fprintln(os.Stderr)
	fmt.Println(cli.StyleWarning.Render("Your SageOx ledger contains historical insights and agent activity."))
	fmt.Println(cli.StyleWarning.Render("This data will be permanently deleted during uninstall."))
	fmt.Fprintln(os.Stderr)
	fmt.Println(cli.StyleDim.Render("To preserve this data, back up the ledger before continuing:"))
	fmt.Fprintln(os.Stderr)
	fmt.Printf("  %s\n", cli.StyleCommand.Render(fmt.Sprintf("cp -r %s ./sageox-ledger-backup", ledgerPath)))
	fmt.Fprintln(os.Stderr)
	fmt.Println(cli.StyleDim.Render("Or copy it into your git repo for version control:"))
	fmt.Fprintln(os.Stderr)
	fmt.Printf("  %s\n", cli.StyleCommand.Render(fmt.Sprintf("cp -r %s ./.sageox-history && git add .sageox-history", ledgerPath)))
	fmt.Fprintln(os.Stderr)
}

// showPreview displays what will be removed during uninstall
func showPreview(gitRoot string) error {
	items, err := uninstall.FindSageoxFiles(gitRoot)
	if err != nil {
		return fmt.Errorf("finding .sageox files: %w", err)
	}

	if len(items) == 0 {
		fmt.Fprintln(os.Stderr, "No .sageox files found to remove")
		return nil
	}

	fmt.Fprintln(os.Stderr)
	fmt.Println(cli.StyleWarning.Render("Files to be removed:"))
	fmt.Fprintln(os.Stderr)

	// summarize files
	var trackedCount, untrackedCount int
	var totalSize int64

	for _, item := range items {
		if !item.IsDir {
			totalSize += item.Size
			if item.IsTracked {
				trackedCount++
			} else {
				untrackedCount++
			}
		}
	}

	fmt.Printf("  %s\n", cli.StyleBold.Render(".sageox/ directory:"))
	fmt.Printf("    %s %d tracked files\n", cli.StyleDim.Render("•"), trackedCount)
	fmt.Printf("    %s %d untracked files\n", cli.StyleDim.Render("•"), untrackedCount)
	fmt.Printf("    %s Total size: %s\n", cli.StyleDim.Render("•"), formatBytes(totalSize))
	fmt.Fprintln(os.Stderr)

	// show sample of key files
	fmt.Printf("  %s\n", cli.StyleDim.Render("Key files:"))
	sampleCount := 0
	maxSamples := 6
	fileItems := []uninstall.SageoxFileItem{}
	for _, item := range items {
		if !item.IsDir {
			fileItems = append(fileItems, item)
		}
	}

	for i, item := range fileItems {
		if sampleCount >= maxSamples {
			break
		}
		prefix := "├─"
		if sampleCount == maxSamples-1 || i == len(fileItems)-1 {
			prefix = "└─"
		}
		tracked := ""
		if item.IsTracked {
			tracked = cli.StyleDim.Render(" (tracked)")
		}
		fmt.Printf("    %s %s%s\n", cli.StyleDim.Render(prefix), cli.StyleFile.Render(item.RelPath), tracked)
		sampleCount++
	}

	remaining := len(fileItems) - sampleCount
	if remaining > 0 {
		fmt.Printf("    %s\n", cli.StyleDim.Render(fmt.Sprintf("... and %d more files", remaining)))
	}

	// TODO: also preview:
	// - git hooks that will be removed
	// - agent integration files (use FindAgentFileEntries)
	// - AGENTS.md/CLAUDE.md modifications

	fmt.Fprintln(os.Stderr)
	slog.Debug("showPreview", "git_root", gitRoot, "items", len(items))
	return nil
}

// formatBytes formats byte size in human-readable format
func formatBytes(bytes int64) string {
	const (
		KB = 1024
		MB = KB * 1024
		GB = MB * 1024
	)

	switch {
	case bytes >= GB:
		return fmt.Sprintf("%.2f GB", float64(bytes)/float64(GB))
	case bytes >= MB:
		return fmt.Sprintf("%.2f MB", float64(bytes)/float64(MB))
	case bytes >= KB:
		return fmt.Sprintf("%.2f KB", float64(bytes)/float64(KB))
	default:
		return fmt.Sprintf("%d bytes", bytes)
	}
}

// confirmUninstall prompts user for confirmation
// returns true if user confirms, false otherwise
// Deprecated: use confirmUninstallWithInput instead
func confirmUninstall() bool {
	// get repo name for confirmation
	gitRoot, err := repotools.FindRepoRoot(repotools.VCSGit)
	if err != nil {
		slog.Error("failed to find repo root for confirmation", "error", err)
		return false
	}
	repoName := filepath.Base(gitRoot)

	fmt.Fprintln(os.Stderr)
	fmt.Println(cli.StyleError.Bold(true).Render("DANGER ZONE"))
	fmt.Fprintln(os.Stderr)
	fmt.Println(cli.StyleWarning.Render("You are about to permanently uninstall SageOx from this repository."))
	fmt.Fprintln(os.Stderr)
	fmt.Println("This will:")
	fmt.Println(cli.StyleError.Render("  • Delete .sageox/ directory and all session history"))
	fmt.Println(cli.StyleError.Render("  • Remove SageOx git hooks"))
	fmt.Println(cli.StyleError.Render("  • Clean up AGENTS.md/CLAUDE.md references"))
	fmt.Fprintln(os.Stderr)
	fmt.Println(cli.StyleError.Bold(true).Render("This affects ALL users of this repository, not just you."))
	fmt.Fprintln(os.Stderr)
	fmt.Printf("To confirm, type the repository name: %s\n", cli.StyleCommand.Render(repoName))
	fmt.Fprintln(os.Stderr)
	fmt.Print("> ")

	reader := bufio.NewReader(os.Stdin)
	input, err := reader.ReadString('\n')
	if err != nil {
		slog.Error("failed to read confirmation input", "error", err)
		return false
	}

	input = strings.TrimSpace(input)
	if input != repoName {
		fmt.Fprintln(os.Stderr)
		fmt.Println(cli.StyleError.Render("Confirmation failed."))
		fmt.Printf("Expected: %s, got: %s\n", repoName, input)
		return false
	}

	slog.Info("uninstall confirmed", "repo", repoName)
	return true
}

// removeSageoxDir removes the .sageox directory
func removeSageoxDir(gitRoot string) error {
	slog.Info("removing .sageox directory", "git_root", gitRoot)

	// use the uninstall package to remove the directory
	if err := uninstall.RemoveSageoxDir(gitRoot, uninstallDryRun); err != nil {
		return fmt.Errorf("removing .sageox directory: %w", err)
	}

	if !uninstallDryRun {
		cli.PrintSuccess("Removed .sageox directory")
	}

	return nil
}

// removeHooks removes SageOx git hooks
func removeHooks(gitRoot string) error {
	slog.Info("removing SageOx hooks", "git_root", gitRoot)

	// find SageOx hooks first
	hooks, err := uninstall.FindSageoxHooks(gitRoot)
	if err != nil {
		return fmt.Errorf("finding hooks: %w", err)
	}

	if len(hooks) == 0 {
		slog.Debug("no SageOx hooks found")
		return nil
	}

	// remove the hooks
	if err := uninstall.RemoveRepoHooks(gitRoot, uninstallDryRun); err != nil {
		return fmt.Errorf("removing hooks: %w", err)
	}

	if !uninstallDryRun {
		cli.PrintSuccess(fmt.Sprintf("Removed %d git hook(s)", len(hooks)))
	}

	return nil
}

// cleanupAgentFiles removes agent integration files
// uses CleanupAgentFiles from uninstall_agent_files.go for AGENTS.md/CLAUDE.md
func cleanupAgentFiles(gitRoot string) error {
	// cleanup AGENTS.md and CLAUDE.md using existing helper
	if err := CleanupAgentFiles(gitRoot, uninstallDryRun); err != nil {
		slog.Warn("failed to cleanup agent markdown files", "error", err)
	}

	// remove Claude Code slash commands (.claude/commands/ox*.md)
	if err := removeClaudeCommands(gitRoot); err != nil {
		slog.Warn("failed to remove Claude commands", "error", err)
	}

	// TODO: implement cleanup for other agent integrations:
	// - .claude/settings.json (project-level shared hooks, use existing hooks_claude.go helpers)
	// - .opencode/plugin/ox-prime.ts (project-level, use existing hooks_opencode.go helpers)
	// - .cursorrules (SageOx sections if any)
	// - .windsurfrules (SageOx sections if any)
	// must preserve user content, only remove SageOx additions
	slog.Debug("cleanupAgentFiles", "git_root", gitRoot, "status", "partial implementation")
	return nil
}

// removeUserIntegrationsWithConfirmation removes user-level integration hooks with user confirmation
func removeUserIntegrationsWithConfirmation() error {
	fmt.Println()
	fmt.Println(ui.RenderCategory("User-Level Integration Removal"))
	fmt.Println()

	// create finder
	finder, err := uninstall.NewUserIntegrationsFinder()
	if err != nil {
		return fmt.Errorf("failed to initialize finder: %w", err)
	}

	// find all user-level integrations
	items, err := finder.FindAll()
	if err != nil {
		return fmt.Errorf("failed to find user integrations: %w", err)
	}

	if len(items) == 0 {
		fmt.Println(ui.MutedStyle.Render("No user-level SageOx integrations found."))
		fmt.Println()
		fmt.Println("User integrations include:")
		fmt.Println("  - Claude Code hooks (~/.claude/settings.json)")
		fmt.Println("  - Claude user config (~/.claude/CLAUDE.md)")
		fmt.Println("  - OpenCode plugins (~/.config/opencode/plugin/)")
		fmt.Println("  - Gemini CLI hooks (~/.gemini/settings.json)")
		fmt.Println("  - code_puppy plugins (~/.code_puppy/plugins/)")
		fmt.Println("  - User git hooks (~/.config/git/hooks/)")
		return nil
	}

	// display what will be removed
	fmt.Printf("Found %d user-level integration(s) to remove:\n", len(items))
	fmt.Println()

	for _, item := range items {
		fmt.Printf("  %s %s\n", ui.FailStyle.Render("✗"), item.Description)
		fmt.Printf("    %s\n", ui.MutedStyle.Render(item.Path))
	}

	fmt.Println()

	// show platform and scope info
	platform := uninstall.GetPlatformInfo()
	fmt.Println(ui.MutedStyle.Render(ui.SeparatorLight))
	fmt.Printf("Platform: %s\n", platform)
	fmt.Println("Scope: User-level only (affects only your account, not this repository)")
	fmt.Println(ui.MutedStyle.Render(ui.SeparatorLight))
	fmt.Println()

	// additional confirmation for user integrations (separate from repo confirmation)
	if !uninstallForce {
		fmt.Println(ui.AccentStyle.Render("WARNING"))
		fmt.Println("This will remove SageOx integration from your user-level agent configuration.")
		fmt.Println("This only affects YOUR account, not other users or this repository.")
		fmt.Println()
		if !cli.ConfirmYesNo("Remove user integrations?", false) {
			fmt.Println()
			fmt.Println("User integration removal canceled.")
			fmt.Println("Repository-level uninstall will continue...")
			return nil
		}
	}

	fmt.Println()

	// remove integrations
	if err := uninstall.RemoveUserIntegrations(items, false); err != nil {
		return fmt.Errorf("failed to remove user integrations: %w", err)
	}

	fmt.Println()
	fmt.Println(ui.PassStyle.Render("✓") + " User integrations removed successfully")
	fmt.Println()

	// check if user config should also be removed
	if uninstall.ShouldRemoveUserConfig() {
		configDir := config.GetUserConfigDir()
		fmt.Println(ui.MutedStyle.Render(ui.SeparatorLight))
		fmt.Println("User configuration directory still exists:")
		fmt.Printf("  %s\n", configDir)
		fmt.Println()
		fmt.Println("This directory contains your SageOx preferences (tips, telemetry settings).")
		fmt.Printf("To remove it manually: %s\n", cli.StyleCommand.Render("rm -rf "+configDir))
		fmt.Println(ui.MutedStyle.Render(ui.SeparatorLight))
	}

	return nil
}
