package daemon

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// cloneBackoffMax is the maximum backoff duration for transient clone errors (1 hour).
const cloneBackoffMax = 1 * time.Hour

// clonePermanentBackoffMax caps backoff for permanent errors (auth, permissions).
// Kept short so that when the user runs 'ox login', the daemon retries quickly.
const clonePermanentBackoffMax = 5 * time.Minute

// isClonePermanentError returns true if the error message indicates a failure
// that won't resolve on its own (bad credentials, missing permissions, bad URL).
// Transient errors (network timeout, server 503) return false.
func isClonePermanentError(msg string) bool {
	permanentPatterns := []string{
		"Authentication failed",
		"Permission denied",
		"could not read Username",
		"invalid credentials",
		"repository not found",
		"does not appear to be a git repository",
		"HTTP 401",
		"HTTP 403",
		"HTTP 404",
		"invalid clone URL",
	}
	for _, p := range permanentPatterns {
		if strings.Contains(msg, p) {
			return true
		}
	}
	return false
}

// cloneInBackground clones a repo in the background without blocking the sync loop.
// Implements exponential backoff on failure: 1min, 2min, 4min, 8min, ..., max 1 hour.
// After successful clone, clears retry state and invalidates the workspace registry cache
// so the next sync will see the newly cloned repo.
//
// Concurrency is bounded by cloneSem inside Checkout().
func (s *SyncScheduler) cloneInBackground(cloneURL, repoPath, repoType, workspaceID string) {
	// cloneWg.Add(1) is called by the caller BEFORE the go statement to
	// avoid a race between Add and Wait.
	defer s.cloneWg.Done()

	// bail out if scheduler is shutting down
	if s.ctx != nil {
		select {
		case <-s.ctx.Done():
			return
		default:
		}
	}

	// deduplicate: skip if clone already in progress for this workspace
	if _, loaded := s.cloneInFlight.LoadOrStore(workspaceID, true); loaded {
		s.logger.Debug("clone already in progress, skipping duplicate", "type", repoType, "id", workspaceID)
		return
	}
	defer s.cloneInFlight.Delete(workspaceID)

	s.logger.Info("background clone starting", "type", repoType, "path", repoPath)

	// get current retry state
	attempts, _ := s.workspaceRegistry.GetCloneRetryInfo(workspaceID)

	// use the Checkout function which handles all clone logic including AGENTS.md creation
	result, err := s.Checkout(CheckoutPayload{
		CloneURL: cloneURL,
		RepoPath: repoPath,
		RepoType: repoType,
	}, nil) // no progress writer for background clones

	if err != nil {
		s.logger.Error("background clone failed", "type", repoType, "path", repoPath, "error", err)

		// semaphore timeout is transient — retry next cycle without escalating backoff
		if errors.Is(err, ErrCloneSemaphoreTimeout) {
			s.logger.Info("clone semaphore busy, will retry next cycle", "type", repoType, "path", repoPath)
			return
		}

		// increment attempt count and calculate backoff
		newAttempts := attempts + 1

		// classify error to choose backoff strategy
		permanent := isClonePermanentError(err.Error())

		// exponential backoff: 1min, 2min, 4min, 8min, ..., max 1 hour
		// permanent errors cap at 5 min — user may fix creds and we should retry soon
		maxBack := cloneBackoffMax
		if permanent {
			maxBack = clonePermanentBackoffMax
		}
		backoff := exponentialBackoff(newAttempts, time.Minute, maxBack)

		nextRetry := time.Now().Add(backoff)
		s.workspaceRegistry.SetCloneRetry(workspaceID, newAttempts, nextRetry)

		hint := "will retry"
		if permanent {
			hint = "likely needs 'ox login' or permission fix"
		}

		// detect 403/forbidden for more specific guidance
		errLower := strings.ToLower(err.Error())
		forbidden := strings.Contains(errLower, "403") || strings.Contains(errLower, "forbidden")
		if forbidden {
			hint = "access denied — you are not a member of this team. Request an invite URL from a team admin."
		}

		errMsg := fmt.Sprintf("clone failed (attempt %d, %s): %v", newAttempts, hint, err)
		s.workspaceRegistry.SetWorkspaceError(workspaceID, errMsg)

		// report to issue tracker so it surfaces in ox doctor
		if s.issues != nil {
			repoID := workspaceID
			if repoType == "ledger" {
				repoID = "ledger"
			}
			issueSummary := fmt.Sprintf("Clone failed for %s: %v. Run 'ox doctor' for details.", repoType, err)
			if forbidden {
				issueSummary = fmt.Sprintf("Access denied for %s. You are not a member of this team — request an invite URL from a team admin.", repoType)
			}
			s.issues.SetIssue(DaemonIssue{
				Type:     IssueTypeCloneFailed,
				Severity: SeverityError,
				Repo:     repoID,
				Summary:  issueSummary,
			})
		}

		s.logger.Warn("clone retry scheduled",
			"type", repoType, "attempts", newAttempts, "backoff", backoff,
			"permanent", permanent, "next_retry", nextRetry)
		return
	}

	// determine repo ID for issue tracking
	repoID := workspaceID
	if repoType == "ledger" {
		repoID = "ledger"
	}

	if result.AlreadyExists {
		s.logger.Debug("background clone: repo already exists", "type", repoType, "path", repoPath)
		// clear any previous retry state since repo now exists
		s.workspaceRegistry.ClearCloneRetry(workspaceID)
		// clear clone failure issue
		if s.issues != nil {
			s.issues.ClearIssue(IssueTypeCloneFailed, repoID)
		}
		// ensure sync timestamp is set (may be zero if cloned before this fix)
		if ws := s.workspaceRegistry.GetWorkspace(workspaceID); ws != nil && ws.ConfigLastSync.IsZero() {
			if err := s.workspaceRegistry.UpdateConfigLastSync(workspaceID); err != nil {
				s.logger.Warn("failed to backfill config last sync", "type", repoType, "error", err)
			}
			s.recordSyncState(context.Background(), repoPath)
		}
	} else if result.Cloned {
		s.logger.Info("background clone complete", "type", repoType, "path", repoPath)
		// clear retry state on success
		s.workspaceRegistry.ClearCloneRetry(workspaceID)
		// clear clone failure issue
		if s.issues != nil {
			s.issues.ClearIssue(IssueTypeCloneFailed, repoID)
		}
		// persist sync timestamp so status shows "synced" after clone
		if err := s.workspaceRegistry.UpdateConfigLastSync(workspaceID); err != nil {
			s.logger.Warn("failed to update config last sync after clone", "type", repoType, "error", err)
		}
		s.recordSyncState(context.Background(), repoPath)
	}

	// update exists flags so status renders correctly before next Reload()
	s.workspaceRegistry.RefreshExists()
	// invalidate cache so next sync sees the cloned repo
	s.workspaceRegistry.InvalidateConfigCache()
}

// triggerMissingClones immediately triggers clones for workspaces that don't exist
// but have a clone URL. This is called on startup for self-healing behavior.
// Also tries to bootstrap ledger from API if not in credentials.
func (s *SyncScheduler) triggerMissingClones() {
	// check ledger - may need to fetch URL from API first
	ledger := s.workspaceRegistry.GetLedger()
	if ledger == nil || ledger.CloneURL == "" {
		// try to fetch ledger URL from API using repo_id
		s.fetchLedgerURLFromAPI()
		// reload after API fetch
		ledger = s.workspaceRegistry.GetLedger()
	}

	if ledger != nil && !ledger.Exists && ledger.CloneURL != "" {
		if s.workspaceRegistry.ShouldRetryClone(ledger.ID) {
			s.logger.Info("triggering immediate ledger clone (self-healing)", "path", ledger.Path)
			s.cloneWg.Add(1)
			go s.cloneInBackground(ledger.CloneURL, ledger.Path, "ledger", ledger.ID)
		}
	}

	// check team contexts
	for _, ws := range s.workspaceRegistry.GetTeamContexts() {
		if !ws.Exists && ws.CloneURL != "" && ws.Path != "" {
			if s.workspaceRegistry.ShouldRetryClone(ws.ID) {
				s.logger.Info("triggering immediate team context clone (self-healing)",
					"team", ws.TeamName, "path", ws.Path)
				s.cloneWg.Add(1)
				go s.cloneInBackground(ws.CloneURL, ws.Path, "team-context", ws.ID)
			}
		}
	}
}
