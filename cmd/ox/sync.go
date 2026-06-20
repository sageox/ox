package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/daemon"
	"github.com/sageox/ox/internal/paths"
	"github.com/sageox/ox/internal/repotools"
	"github.com/sageox/ox/internal/version"
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
	TeamSlug string `json:"team_slug,omitempty"`
	Path     string `json:"path"`
	// "synced", "skipped", "cloning", "error", "not_found", "ambiguous", or
	// "unknown" ("unknown" = a legacy daemon that didn't report per-team results;
	// "ambiguous" = a slug/name selector that matched more than one team).
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

var syncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Manually sync ledger/team contexts (rarely needed)",
	Long: `Manually synchronize your ledger and team context repositories.

NOTE: You should RARELY need this command. The background daemon automatically
keeps your ledger and team contexts synchronized. This command exists only for:
  - Troubleshooting sync issues
  - Triggering an immediate sync (this command itself forces sync)
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
	// Per IPC architecture philosophy (docs/specs/ipc-architecture.md):
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
			client := daemon.NewClientForCurrentRepoWithTimeout(30 * time.Second)
			return client.SyncWithProgress(func(stage string, percent *int, message string) {
				// progress updates are shown by spinner
			})
		})
	} else {
		client := daemon.NewClientForCurrentRepoWithTimeout(30 * time.Second)
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
// CLI delegates pull operations to daemon. The daemon syncs all configured
// teams at once and returns per-team results; we report the status of the
// requested team specifically (not the bare success of the IPC round-trip).
func syncTeamContext(_ context.Context, teamID string, jsonOutput bool, result *SyncResult) error {
	var results []daemon.TeamSyncResult
	run := func() error {
		client := daemon.NewClientForCurrentRepoWithTimeout(60 * time.Second)
		var e error
		results, e = client.TeamSyncWithProgress(nil)
		return e
	}

	var syncErr error
	if !jsonOutput {
		syncErr = cli.WithSpinnerNoResult(fmt.Sprintf("Syncing team %s via daemon...", teamID), run)
	} else {
		syncErr = run()
	}

	tcResult := resolveTeamSyncResult(teamID, results, syncErr)
	// for the legacy-daemon ("unknown") case, replace the placeholder error with
	// a version-aware message (queries the daemon's version) before it's reported.
	if tcResult.Status == "unknown" {
		tcResult.Error = legacyDaemonError().Error()
	}
	result.TeamContexts = append(result.TeamContexts, tcResult)

	// The command succeeds only when the requested team context is locally
	// usable: "synced" (pulled) or "skipped" (already up to date and present).
	// Everything else is a non-success that exits non-zero:
	//   - "cloning": clone in progress, not yet usable — fail so scripts don't
	//     proceed against missing context (re-run once the clone finishes)
	//   - "error" / "not_found" / "ambiguous": a real failure, typo/stale selector,
	//     or a selector matching multiple teams
	//   - "unknown": a legacy daemon that returned no per-team data. We fail CLOSED:
	//     its "success" is untrustworthy (old daemons swallowed failures), so we
	//     surface it with a restart hint instead of implying the team synced.
	// A whole-op error caused by some *other* team never reaches here as the
	// requested team's status, so it can't fail this targeted request.
	switch tcResult.Status {
	case "synced":
		if !jsonOutput {
			cli.PrintSuccess(fmt.Sprintf("Team %s synced via daemon", teamID))
		}
		return nil
	case "skipped":
		if !jsonOutput {
			cli.PrintSuccess(fmt.Sprintf("Team %s already up to date", teamID))
		}
		return nil
	case "cloning":
		if !jsonOutput {
			cli.PrintError(fmt.Sprintf("Team %s clone in progress (not yet available); re-run shortly", teamID))
		}
		return fmt.Errorf("team %s clone in progress (not yet available)", teamID)
	default: // error, not_found, ambiguous, unknown
		if !jsonOutput {
			cli.PrintError(fmt.Sprintf("Team sync failed: %v", tcResult.Error))
		}
		return errors.New(tcResult.Error)
	}
}

// resolveTeamSyncResult derives the reported result for a targeted --team sync
// from the daemon's per-team results and the IPC error. It is pure (no I/O) so
// the status-derivation logic can be unit-tested directly.
//
// The selector may be the team ID, its kebab-case slug, or its name (the --help
// example uses a slug-like value), so it is matched against all three.
//
// Selector resolution: an exact team-ID match wins immediately (IDs are unique).
// Otherwise the selector is matched against slug and name, which are NOT
// guaranteed unique — if more than one team matches, the result is "ambiguous"
// rather than silently picking the first (which could report the wrong team).
//
// Status outcomes:
//   - match found      → that team's reported status (synced/skipped/cloning/error)
//   - slug/name matches >1 team → "ambiguous": caller must disambiguate by ID
//   - results == nil, no err → "unknown": a legacy pre-change daemon that returned
//     success without per-team data (current daemons always send an array). The
//     caller fails CLOSED on this — a legacy daemon also swallowed sync failures,
//     so its "success" can't be trusted; the user should restart the daemon and
//     retry rather than proceed on possibly stale/missing context.
//   - empty results + err → "error": transport/setup failure with nothing to attribute
//   - results present, no match → "not_found": a genuine miss (typo/stale config)
func resolveTeamSyncResult(teamID string, results []daemon.TeamSyncResult, syncErr error) TeamContextSyncResult {
	tcResult := TeamContextSyncResult{TeamID: teamID}

	// exact ID match wins (IDs are unique)
	var match *daemon.TeamSyncResult
	for i := range results {
		if teamID == results[i].TeamID {
			match = &results[i]
			break
		}
	}
	// otherwise resolve via slug/name, which may collide across teams
	if match == nil {
		var candidates []*daemon.TeamSyncResult
		for i := range results {
			r := &results[i]
			if (r.TeamSlug != "" && teamID == r.TeamSlug) || (r.TeamName != "" && teamID == r.TeamName) {
				candidates = append(candidates, r)
			}
		}
		switch len(candidates) {
		case 1:
			match = candidates[0]
		case 0:
			// fall through to not_found / legacy / transport handling below
		default:
			ids := make([]string, len(candidates))
			for i, c := range candidates {
				ids[i] = c.TeamID
			}
			tcResult.Status = "ambiguous"
			tcResult.Error = fmt.Sprintf("selector %q matches multiple teams (%s); use the team ID", teamID, strings.Join(ids, ", "))
			return tcResult
		}
	}

	switch {
	case match != nil:
		tcResult.TeamName = match.TeamName
		tcResult.TeamSlug = match.TeamSlug
		tcResult.Path = match.Path
		tcResult.Status = match.Status
		tcResult.Error = match.Error
	case results == nil && syncErr == nil:
		tcResult.Status = "unknown"
		tcResult.Error = "daemon did not report per-team results (restart the daemon: ox daemon restart)"
	case len(results) == 0 && syncErr != nil:
		tcResult.Status = "error"
		tcResult.Error = syncErr.Error()
	default:
		tcResult.Status = "not_found"
		tcResult.Error = fmt.Sprintf("team %q not found among synced team contexts", teamID)
	}

	return tcResult
}

// legacyDaemonError builds the error surfaced when a TeamSync returned success
// with no per-team data — i.e. the daemon predates the per-team-results protocol
// (a new CLI talking to an old, still-running daemon after an upgrade). It reads
// the daemon's reported version to tell the user it's running an older build. It
// does NOT restart the daemon: the daemon restarts itself on its next heartbeat
// (version-mismatch detection), so a re-run resolves it.
func legacyDaemonError() error {
	daemonVer := ""
	client := daemon.NewClientForCurrentRepoWithTimeout(5 * time.Second)
	if st, err := client.Status(); err == nil && st != nil {
		daemonVer = st.Version
	}
	return legacyDaemonErrorMsg(daemonVer)
}

// legacyDaemonErrorMsg formats the legacy-daemon error from the daemon's reported
// version. Split out (pure) so the message logic is unit-testable. daemonVersion
// may carry a "+builddate" suffix, which is stripped before comparison.
func legacyDaemonErrorMsg(daemonVersion string) error {
	const base = "daemon did not report per-team results"
	if daemonVersion == "" {
		return fmt.Errorf("%s; restart the daemon and retry (ox daemon restart)", base)
	}
	daemonSemver, _, _ := strings.Cut(daemonVersion, "+")
	if daemonSemver != version.Version {
		return fmt.Errorf("%s: the daemon is running an older version (%s) than this CLI (%s). "+
			"The daemon restarts itself on its next heartbeat — re-run shortly, or run 'ox daemon restart'",
			base, daemonSemver, version.Version)
	}
	// versions match but still no per-team data — unexpected; surface for diagnosis.
	return fmt.Errorf("%s (daemon version %s); restart the daemon and retry (ox daemon restart)", base, daemonSemver)
}

// syncAllTeamContexts syncs all team contexts via daemon.
// CLI delegates pull operations to daemon and reports the per-team results
// returned by the daemon in the JSON output.
func syncAllTeamContexts(_ context.Context, jsonOutput bool, result *SyncResult) error {
	var results []daemon.TeamSyncResult
	run := func() error {
		client := daemon.NewClientForCurrentRepoWithTimeout(60 * time.Second)
		var e error
		results, e = client.TeamSyncWithProgress(nil)
		return e
	}

	var err error
	if !jsonOutput {
		err = cli.WithSpinnerNoResult("Syncing team contexts via daemon...", run)
	} else {
		err = run()
	}

	for _, r := range results {
		result.TeamContexts = append(result.TeamContexts, TeamContextSyncResult{
			TeamID:   r.TeamID,
			TeamName: r.TeamName,
			TeamSlug: r.TeamSlug,
			Path:     r.Path,
			Status:   r.Status,
			Error:    r.Error,
		})
	}

	// Any error from the daemon must fail the command — never print blanket
	// success on a non-nil error. This covers setup failures (e.g. unreadable
	// config) and aggregated per-team failures, regardless of whether the daemon
	// also sent (possibly empty) per-team data.
	if err != nil {
		if !jsonOutput {
			cli.PrintError(fmt.Sprintf("Team sync failed: %v", err))
		}
		return err
	}

	// Success with no per-team data at all (results is nil, not an empty array)
	// means a legacy pre-change daemon — we can't confirm anything synced, so
	// surface the version skew rather than reporting success. (A current daemon
	// with zero teams sends `[]`, which is non-nil and falls through to success.)
	if results == nil {
		retErr := legacyDaemonError()
		if !jsonOutput {
			cli.PrintError(fmt.Sprintf("Team sync failed: %v", retErr))
		}
		return retErr
	}

	// The sync succeeds only if every team context ended up locally usable
	// (synced or already up to date). Teams that errored or are still cloning are
	// not usable, so report them as a failure rather than printing blanket success
	// — otherwise scripts/distill could proceed against missing/stale context.
	if notReady := notReadyTeams(results); len(notReady) > 0 {
		msg := fmt.Sprintf("team context(s) not ready: %s", strings.Join(notReady, ", "))
		if !jsonOutput {
			cli.PrintError(msg)
		}
		return errors.New(msg)
	}

	if !jsonOutput {
		cli.PrintSuccess("Team contexts synced via daemon")
	}

	return nil
}

// notReadyTeams returns "<name> (<status>)" labels for every team context that
// did not end up locally usable — i.e. anything other than "synced" or
// "skipped" (already up to date). Used to fail an all-teams sync when some team
// is still cloning or errored, instead of reporting blanket success.
func notReadyTeams(results []daemon.TeamSyncResult) []string {
	var notReady []string
	for _, r := range results {
		if r.Status == "synced" || r.Status == "skipped" {
			continue
		}
		name := r.TeamName
		if name == "" {
			name = r.TeamID
		}
		notReady = append(notReady, fmt.Sprintf("%s (%s)", name, r.Status))
	}
	return notReady
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

	// remove from project config if present, under MutateLocalConfig
	// so concurrent daemon writes to config.local.toml don't lose the
	// removal (ox-dfy4).
	configRemoved := false
	if localCfg != nil && tc != nil {
		err := config.MutateLocalConfig(context.Background(), projectRoot, func(cfg *config.LocalConfig) error {
			cfg.RemoveTeamContext(teamID)
			return nil
		})
		if err != nil {
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
