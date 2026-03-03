package main

import (
	"context"
	"fmt"
	"os"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/daemon"
	"github.com/sageox/ox/internal/doctor"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/ledger"
	"github.com/sageox/ox/internal/version"
)

// checkDaemonHealth returns health checks for the daemon.
func checkDaemonHealth(opts doctorOptions) []checkResult {
	ctx := context.Background()

	var results []checkResult

	// first check: validate repo paths exist and are git repos
	gitRoot := findGitRoot()
	if gitRoot != "" {
		localCfg, _ := config.LoadLocalConfig(gitRoot)

		// check ledger path
		ledgerPath, _ := ledger.DefaultPath()
		if ledgerPath != "" {
			results = append(results, checkRepoIsGit(ledgerPath, "ledger"))
		}

		// check team context paths
		if localCfg != nil {
			for _, tc := range localCfg.TeamContexts {
				if tc.Path != "" && tc.TeamID != "" {
					name := tc.TeamID
					if len(name) > 12 {
						name = name[:12] + "..."
					}
					results = append(results, checkRepoIsGit(tc.Path, name))
				}
			}
		}
	}

	// create daemon checks
	checks := []doctor.Check{
		doctor.NewDaemonRunningCheck(),
		doctor.NewDaemonResponsiveCheck(),
		doctor.NewDaemonUptimeCheck(),
		doctor.NewDaemonSyncStatusCheck(),
		doctor.NewDaemonSyncErrorsCheck(),
		doctor.NewDaemonDirtyTeamContextCheck(),
	}

	// add heartbeat checks for monitored repos
	// CRITICAL: Uses BOTH repo_id AND workspace_id for workspace/ledger checks.
	// This supports multiple git worktrees while keeping files debuggable.
	// See internal/daemon/heartbeat_file.go for full explanation.
	if gitRoot != "" {
		ep := endpoint.GetForProject(gitRoot)
		if ep == "" {
			goto skipHeartbeats
		}

		// Get both repo_id (for debugging) and workspace_id (for uniqueness)
		repoID := config.GetRepoID(gitRoot)
		workspaceID := daemon.WorkspaceID(gitRoot)
		if repoID == "" || workspaceID == "" {
			goto skipHeartbeats
		}

		// workspace heartbeat (uses repo_id + workspace_id)
		// Identifier format: repo_abc123_a1b2c3d4
		checks = append(checks, doctor.NewDaemonHeartbeatCheck(
			"workspace",
			repoID+"_"+workspaceID,
			"workspace",
			ep,
		))

		// ledger heartbeat (uses repo_id + workspace_id)
		// Each worktree has its own ledger, so needs unique identifier
		ledgerPath, _ := ledger.DefaultPath()
		if ledgerPath != "" && isGitRepo(ledgerPath) {
			checks = append(checks, doctor.NewDaemonHeartbeatCheck(
				"ledger",
				repoID+"_"+workspaceID,
				"ledger",
				ep,
			))
		}

		// team context heartbeats (uses team_id only - shared across workspaces)
		localCfg, _ := config.LoadLocalConfig(gitRoot)
		if localCfg != nil {
			for _, tc := range localCfg.TeamContexts {
				if tc.Path != "" && tc.TeamID != "" && isGitRepo(tc.Path) {
					name := tc.TeamID
					if len(name) > 12 {
						name = name[:12] + "..."
					}
					checks = append(checks, doctor.NewDaemonHeartbeatCheck(
						"team",
						tc.TeamID,
						name,
						ep,
					))
				}
			}
		}
	}
skipHeartbeats:

	// run checks and convert to checkResult format
	for _, check := range checks {
		result := check.Run(ctx)

		// skip empty results (StatusSkip with no message)
		if result.Status == doctor.StatusSkip && result.Message == "" {
			continue
		}

		results = append(results, convertDoctorResult(result))
	}

	// add daemon version check (this one has fix capability)
	versionCheck := checkDaemonVersion(opts.shouldFix(CheckSlugDaemonVersion))
	results = append(results, versionCheck)

	return results
}

// checkRepoIsGit verifies a repo path exists and is a valid git repository.
// Returns passed if path doesn't exist (not cloned yet) or is a valid git repo.
// Returns warning if path exists but is not a git repo (orphaned directory).
func checkRepoIsGit(path, name string) checkResult {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		// path doesn't exist - that's fine, repo not cloned yet
		return checkResult{
			name:    fmt.Sprintf("%s repo", name),
			skipped: true,
			message: "not cloned",
		}
	}
	if err != nil {
		return checkResult{
			name:    fmt.Sprintf("%s repo", name),
			passed:  false,
			message: "stat failed",
			detail:  err.Error(),
		}
	}
	if !info.IsDir() {
		return checkResult{
			name:    fmt.Sprintf("%s repo", name),
			passed:  false,
			message: "not a directory",
			detail:  path,
		}
	}

	// path exists and is a directory - check if it's a git repo
	if !isGitRepo(path) {
		return checkResult{
			name:    fmt.Sprintf("%s repo", name),
			passed:  false,
			message: "not a git repo",
			detail:  fmt.Sprintf("%s exists but is not a git repository. Delete it and run `ox doctor --fix` to clone.", path),
		}
	}

	return checkResult{
		name:    fmt.Sprintf("%s repo", name),
		passed:  true,
		message: "valid",
	}
}

// checkDaemonVersion verifies the running daemon version matches the CLI version.
// When fix=true and versions mismatch, stops the outdated daemon so it can be restarted.
func checkDaemonVersion(fix bool) checkResult {
	if !daemon.IsRunning() {
		return checkResult{
			name:    "daemon version",
			skipped: true,
			message: "daemon not running",
		}
	}

	client := daemon.NewClient()
	status, err := client.Status()
	if err != nil {
		return checkResult{
			name:    "daemon version",
			passed:  false,
			message: "status check failed",
			detail:  err.Error(),
		}
	}

	cliVersion := version.Version
	daemonVersion := status.Version

	if daemonVersion == cliVersion {
		return checkResult{
			name:    "daemon version",
			passed:  true,
			message: daemonVersion,
		}
	}

	// version mismatch
	if fix {
		// stop the outdated daemon via IPC
		if err := client.Stop(); err != nil {
			return checkResult{
				name:    "daemon version",
				passed:  false,
				message: fmt.Sprintf("mismatch (daemon=%s, cli=%s)", daemonVersion, cliVersion),
				detail:  fmt.Sprintf("failed to stop outdated daemon: %v", err),
			}
		}
		return checkResult{
			name:    "daemon version",
			passed:  true,
			message: fmt.Sprintf("stopped outdated daemon (was %s, cli is %s)", daemonVersion, cliVersion),
			detail:  "daemon will auto-start on next command",
		}
	}

	return checkResult{
		name:    "daemon version",
		passed:  false,
		message: fmt.Sprintf("mismatch (daemon=%s, cli=%s)", daemonVersion, cliVersion),
		detail:  "Run `ox doctor` to restart daemon with correct version",
		slug:    CheckSlugDaemonVersion,
	}.WithFixInfo(CheckSlugDaemonVersion, FixLevelAuto)
}
