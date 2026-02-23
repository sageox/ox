package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/daemon"
	"github.com/sageox/ox/internal/paths"
	"github.com/sageox/ox/internal/repotools"
	"github.com/spf13/cobra"
)

// SyncResult represents the JSON output for sync operations.
type SyncResult struct {
	Success      bool                    `json:"success"`
	Mode         string                  `json:"mode"` // "daemon" or "direct"
	Ledger       *SyncLedgerResult       `json:"ledger,omitempty"`
	TeamContexts []TeamContextSyncResult `json:"team_contexts,omitempty"`
	Error        string                  `json:"error,omitempty"`
}

// SyncLedgerResult represents sync result for the ledger.
type SyncLedgerResult struct {
	Path   string `json:"path"`
	Status string `json:"status"` // "synced", "skipped", "error", "not_found"
	Error  string `json:"error,omitempty"`
}

// TeamContextSyncResult represents sync result for a single team context.
type TeamContextSyncResult struct {
	TeamID   string `json:"team_id"`
	TeamName string `json:"team_name"`
	Path     string `json:"path"`
	Status   string `json:"status"` // "synced", "skipped", "error", "not_found"
	Error    string `json:"error,omitempty"`
}

var syncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Manually sync ledger/team contexts (rarely needed)",
	Long: `Manually synchronize your ledger and team context repositories.

NOTE: You should RARELY need this command. The background daemon automatically
keeps your ledger and team contexts synchronized. This command exists only for:
  - Troubleshooting sync issues
  - Forcing an immediate sync
  - Diagnostic purposes

The daemon syncs automatically on:
  - File changes in your project
  - Periodic intervals (configurable)
  - Session start/end events

REQUIRES: Daemon must be running. Pull operations are handled by the daemon
to ensure consistent sync behavior and proper locking.

Examples:
  ox sync              # sync all workspaces (rarely needed)
  ox sync --team acme  # sync specific team context
  ox sync --all-teams  # sync all team contexts`,
	RunE: runSync,
}

func init() {
	syncCmd.Flags().String("team", "", "sync a specific team context by ID")
	syncCmd.Flags().Bool("all-teams", false, "sync all configured team contexts")
	syncCmd.Flags().String("remove-team", "", "remove a team context (clears config and optionally deletes repo)")

	// add to root command
	rootCmd.AddCommand(syncCmd)
	syncCmd.GroupID = "auth" // group with ledger and other auth-related commands
}

func runSync(cmd *cobra.Command, args []string) error {
	teamID, _ := cmd.Flags().GetString("team")
	allTeams, _ := cmd.Flags().GetBool("all-teams")
	jsonOutput, _ := cmd.Flags().GetBool("json")
	removeTeamID, _ := cmd.Flags().GetString("remove-team")

	// handle team removal (separate flow)
	if removeTeamID != "" {
		return removeTeamContext(removeTeamID, jsonOutput)
	}

	// CLI delegates pull operations to daemon.
	// This ensures consistent sync behavior and proper locking.
	//
	// Per IPC architecture philosophy (docs/ai/specs/ipc-architecture.md):
	// sync requires daemon for pull operations, but we auto-start the daemon
	// rather than erroring - this improves UX while maintaining the architecture.
	//
	// Use IsHealthyQuick() to detect both "not running" AND "running but hung".
	if err := daemon.IsHealthy(); err != nil {
		if !jsonOutput {
			fmt.Println("Starting daemon...")
		}

		// auto-start daemon in background
		if err := autoStartDaemon(); err != nil {
			if jsonOutput {
				cli.PrintJSON(map[string]any{
					"success": false,
					"error":   fmt.Sprintf("failed to start daemon: %v", err),
					"hint":    "Try starting manually with 'ox daemon start'",
				})
			} else {
				cli.PrintError(fmt.Sprintf("Failed to start daemon: %v", err))
				cli.PrintHint("Try starting manually with 'ox daemon start'")
			}
			return fmt.Errorf("failed to start daemon: %w", err)
		}

		// wait for daemon to be healthy (max 5 seconds)
		ready := false
		for i := 0; i < 50; i++ {
			if daemon.IsHealthy() == nil {
				ready = true
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if !ready {
			if jsonOutput {
				cli.PrintJSON(map[string]any{
					"success": false,
					"error":   "daemon did not start in time",
					"hint":    "Try starting manually with 'ox daemon start'",
				})
			} else {
				cli.PrintError("Daemon did not start in time")
				cli.PrintHint("Try starting manually with 'ox daemon start'")
			}
			return fmt.Errorf("daemon did not start in time")
		}
	}

	result := SyncResult{
		Mode: "daemon",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var syncErr error

	if teamID != "" {
		// sync specific team context
		syncErr = syncTeamContext(ctx, teamID, jsonOutput, &result)
	} else if allTeams {
		// sync all team contexts
		syncErr = syncAllTeamContexts(ctx, jsonOutput, &result)
	} else {
		// default: sync all workspaces via daemon
		syncErr = syncViaDaemon(ctx, jsonOutput, &result)
	}

	result.Success = syncErr == nil
	if syncErr != nil {
		result.Error = syncErr.Error()
	}

	// output result
	if jsonOutput {
		cli.PrintJSON(result)
		if syncErr != nil {
			return cli.ErrSilent
		}
		return nil
	}

	return syncErr
}

// syncViaDaemon triggers a sync via the daemon.
// The daemon handles pull operations.
func syncViaDaemon(_ context.Context, jsonOutput bool, result *SyncResult) error {
	var err error
	if !jsonOutput {
		err = cli.WithSpinnerNoResult("Syncing via daemon...", func() error {
			client := daemon.NewClientWithTimeout(30 * time.Second)
			return client.SyncWithProgress(func(stage string, percent *int, message string) {
				// progress updates are shown by spinner
			})
		})
	} else {
		client := daemon.NewClientWithTimeout(30 * time.Second)
		err = client.SyncWithProgress(nil)
	}

	if err != nil {
		if !jsonOutput {
			cli.PrintError(fmt.Sprintf("Sync failed: %v", err))
		}
		return fmt.Errorf("daemon sync: %w", err)
	}

	result.Ledger = &SyncLedgerResult{Status: "synced"}
	if !jsonOutput {
		cli.PrintSuccess("Synced via daemon")
	}
	return nil
}

// syncTeamContext syncs a specific team context by ID via daemon.
// CLI delegates pull operations to daemon.
func syncTeamContext(_ context.Context, teamID string, jsonOutput bool, result *SyncResult) error {
	tcResult := TeamContextSyncResult{
		TeamID: teamID,
	}

	// daemon syncs all teams at once
	var syncErr error
	if !jsonOutput {
		syncErr = cli.WithSpinnerNoResult(fmt.Sprintf("Syncing team %s via daemon...", teamID), func() error {
			client := daemon.NewClientWithTimeout(60 * time.Second)
			return client.TeamSyncWithProgress(nil)
		})
	} else {
		client := daemon.NewClientWithTimeout(60 * time.Second)
		syncErr = client.TeamSyncWithProgress(nil)
	}

	if syncErr != nil {
		tcResult.Status = "error"
		tcResult.Error = syncErr.Error()
		if !jsonOutput {
			cli.PrintError(fmt.Sprintf("Team sync failed: %v", syncErr))
		}
	} else {
		tcResult.Status = "synced"
		if !jsonOutput {
			cli.PrintSuccess(fmt.Sprintf("Team %s synced via daemon", teamID))
		}
	}

	result.TeamContexts = append(result.TeamContexts, tcResult)
	return syncErr
}

// syncAllTeamContexts syncs all team contexts via daemon.
// CLI delegates pull operations to daemon.
func syncAllTeamContexts(_ context.Context, jsonOutput bool, _ *SyncResult) error {
	var err error
	if !jsonOutput {
		err = cli.WithSpinnerNoResult("Syncing team contexts via daemon...", func() error {
			client := daemon.NewClientWithTimeout(60 * time.Second)
			return client.TeamSyncWithProgress(func(stage string, percent *int, message string) {
				// progress updates handled by daemon
			})
		})
	} else {
		client := daemon.NewClientWithTimeout(60 * time.Second)
		err = client.TeamSyncWithProgress(nil)
	}

	if err != nil {
		if !jsonOutput {
			cli.PrintError(fmt.Sprintf("Team sync failed: %v", err))
		}
		return err
	}

	if !jsonOutput {
		cli.PrintSuccess("Team contexts synced via daemon")
	}

	return nil
}

// syncPathExists checks if a path exists.
// named differently to avoid conflict with other pathExists in package
func syncPathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// autoStartDaemon starts the daemon in background for sync operations.
// This mirrors the logic in daemon.go:startDaemonBackground but is self-contained
// to avoid circular dependencies.
func autoStartDaemon() error {
	// OX_NO_DAEMON=1 prevents daemon start (integration tests)
	if os.Getenv("OX_NO_DAEMON") == "1" {
		return fmt.Errorf("daemon start disabled: OX_NO_DAEMON=1")
	}

	// get the path to the current executable
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("get executable path: %w", err)
	}

	// start daemon process in background
	cmd := exec.Command(exe, "daemon", "start")
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start daemon: %w", err)
	}

	// detach - don't wait for the process
	return nil
}

// removeTeamContext removes a team context from all locations:
// 1. Project config (config.local.toml)
// 2. XDG data directory (~/.local/share/sageox/<endpoint>/teams/<team_id>)
func removeTeamContext(teamID string, jsonOutput bool) error {
	// find project root (optional - we can still remove XDG path without it)
	projectRoot, projectErr := repotools.FindRepoRoot(repotools.VCSGit)

	var localCfg *config.LocalConfig
	var projectCfg *config.ProjectConfig
	var tc *config.TeamContext
	var endpoint string

	// try to load project config for endpoint
	if projectErr == nil {
		projectCfg, _ = config.LoadProjectConfig(projectRoot)
		if projectCfg != nil {
			endpoint = projectCfg.GetEndpoint()
		}

		// try to load local config for team context
		localCfg, _ = config.LoadLocalConfig(projectRoot)
		if localCfg != nil {
			tc = localCfg.GetTeamContext(teamID)
		}
	}

	teamName := teamID
	var configPath string
	if tc != nil {
		if tc.TeamName != "" {
			teamName = tc.TeamName
		}
		configPath = tc.Path
	}

	// determine XDG path (centralized team storage)
	var xdgPath string
	if endpoint != "" {
		xdgPath = paths.TeamContextDir(teamID, endpoint)
	}

	// collect all paths to potentially delete
	var pathsToDelete []string
	if configPath != "" && syncPathExists(configPath) {
		pathsToDelete = append(pathsToDelete, configPath)
	}
	if xdgPath != "" && syncPathExists(xdgPath) && xdgPath != configPath {
		pathsToDelete = append(pathsToDelete, xdgPath)
	}

	// check if we found anything to remove
	if tc == nil && len(pathsToDelete) == 0 {
		if !jsonOutput {
			cli.PrintError(fmt.Sprintf("Team context not found: %s", teamID))
			if endpoint != "" {
				cli.PrintHint(fmt.Sprintf("Checked: project config and %s", xdgPath))
			}
		}
		return fmt.Errorf("team context not found: %s", teamID)
	}

	repoDeleted := false

	// delete repos
	for _, path := range pathsToDelete {
		if !jsonOutput {
			hasChanges, statusErr := checkGitHasUncommittedChanges(path)
			if statusErr != nil {
				cli.PrintWarning(fmt.Sprintf("Could not check git status: %v", statusErr))
			}

			var prompt string
			if hasChanges {
				prompt = fmt.Sprintf("Delete repo at %s? (has uncommitted changes)", path)
			} else {
				prompt = fmt.Sprintf("Delete repo at %s? (no uncommitted changes, safe to delete)", path)
			}

			if cli.ConfirmYesNo(prompt, !hasChanges) {
				if err := os.RemoveAll(path); err != nil {
					cli.PrintWarning(fmt.Sprintf("Failed to delete repo: %v", err))
				} else {
					cli.PrintSuccess(fmt.Sprintf("Deleted %s", path))
					repoDeleted = true
				}
			} else {
				cli.PrintInfo(fmt.Sprintf("Repo left at %s", path))
			}
		} else {
			// json mode: delete without prompting
			if err := os.RemoveAll(path); err == nil {
				repoDeleted = true
			}
		}
	}

	// remove from project config if present
	configRemoved := false
	if localCfg != nil && tc != nil {
		localCfg.RemoveTeamContext(teamID)
		if err := config.SaveLocalConfig(projectRoot, localCfg); err != nil {
			if !jsonOutput {
				cli.PrintWarning(fmt.Sprintf("Failed to save config: %v", err))
			}
		} else {
			configRemoved = true
		}
	}

	if !jsonOutput {
		if configRemoved {
			cli.PrintSuccess(fmt.Sprintf("Removed team %s from configuration", teamName))
		} else if repoDeleted {
			cli.PrintSuccess(fmt.Sprintf("Removed team %s data", teamName))
		}
	}

	if jsonOutput {
		result := map[string]any{
			"success":        true,
			"team_id":        teamID,
			"config_removed": configRemoved,
			"repo_deleted":   repoDeleted,
			"paths_checked":  pathsToDelete,
		}
		cli.PrintJSON(result)
	}

	return nil
}

// checkGitHasUncommittedChanges checks if a git repo has uncommitted changes.
func checkGitHasUncommittedChanges(repoPath string) (bool, error) {
	cmd := exec.Command("git", "-C", repoPath, "status", "--porcelain")
	output, err := cmd.Output()
	if err != nil {
		return false, err
	}
	return len(output) > 0, nil
}
