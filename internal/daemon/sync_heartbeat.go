package daemon

import (
	"os"
	"time"

	"github.com/sageox/ox/internal/version"
)

// writeHeartbeats writes heartbeat entries to all monitored repos.
// Uses the WorkspaceRegistry to avoid repeated config file reads.
// Writes to global cache at ~/.cache/sageox/<endpoint>/heartbeats/
//
// CRITICAL DESIGN: Uses workspace_id (hash of project root) for workspace/ledger heartbeats.
// This prevents collisions when users have multiple git worktrees of the same repository.
// See internal/daemon/heartbeat_file.go package docs for full explanation.
func (s *SyncScheduler) writeHeartbeats() {
	s.mu.Lock()
	lastSync := s.lastSync
	errorCount := len(s.recentErrors)
	s.mu.Unlock()

	endpoint := s.workspaceRegistry.GetEndpoint()
	if endpoint == "" {
		s.logger.Debug("no endpoint available for heartbeat")
		return
	}

	// Use repo_id + repo-based workspace_id for heartbeat filenames.
	// With 1 daemon per repo, all worktrees share a daemon and should
	// see the same heartbeat files. Repo-based ID is consistent across clones.
	repoID := s.workspaceRegistry.GetRepoID()
	if repoID == "" {
		s.logger.Debug("no repo_id available for heartbeat")
		return
	}

	workspaceID := CurrentWorkspaceID()
	if workspaceID == "" {
		s.logger.Debug("no workspace_id available for heartbeat")
		return
	}

	status := "healthy"
	if errorCount > 0 {
		status = "error"
	}

	// common entry data
	baseEntry := HeartbeatEntry{
		Timestamp:     time.Now(),
		DaemonPID:     os.Getpid(),
		DaemonVersion: version.Version,
		Workspace:     s.config.ProjectRoot,
		LastSync:      lastSync,
		Status:        status,
		ErrorCount:    errorCount,
	}

	// 1. Write workspace heartbeat (uses repo_id + workspace_id)
	workspacePath := UserHeartbeatPath(endpoint, repoID, workspaceID)
	if err := WriteHeartbeatToPath(workspacePath, baseEntry); err != nil {
		s.logger.Debug("failed to write workspace heartbeat", "error", err)
	}

	// 2. Write ledger heartbeat (uses repo_id + workspace_id)
	// Each worktree has its own ledger (sibling pattern), so include both IDs
	if ledger := s.workspaceRegistry.GetLedger(); ledger != nil && ledger.Exists {
		ledgerEntry := baseEntry
		// use ledger-specific last sync if available
		if !ledger.ConfigLastSync.IsZero() {
			ledgerEntry.LastSync = ledger.ConfigLastSync
		}
		ledgerPath := UserLedgerHeartbeatPath(endpoint, repoID, workspaceID)
		if err := WriteHeartbeatToPath(ledgerPath, ledgerEntry); err != nil {
			s.logger.Debug("failed to write ledger heartbeat", "error", err)
		}
	}

	// 3. Write team context heartbeats (shared using team_id)
	// Team contexts are shared across projects, so use team_id (last-write-wins is OK)
	for _, tc := range s.workspaceRegistry.GetTeamContexts() {
		if tc.TeamID == "" || !tc.Exists {
			continue
		}
		teamEntry := baseEntry
		// use team-specific last sync if available
		if !tc.ConfigLastSync.IsZero() {
			teamEntry.LastSync = tc.ConfigLastSync
		}
		teamPath := UserTeamHeartbeatPath(endpoint, tc.TeamID)
		if err := WriteHeartbeatToPath(teamPath, teamEntry); err != nil {
			s.logger.Debug("failed to write team heartbeat",
				"team_id", tc.TeamID, "error", err)
		}
	}
}
