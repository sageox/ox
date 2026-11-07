package daemon

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/gitutil"
)

// cloneBackoffMax is the maximum backoff duration for transient clone errors (1 hour).
const cloneBackoffMax = 1 * time.Hour

// clonePermanentBackoffMax caps backoff for permanent errors (auth, permissions).
// Kept short so that when the user runs 'ox login', the daemon retries quickly.
const clonePermanentBackoffMax = 5 * time.Minute

// teamContextCloneBackoffMax caps the retry delay for a failing team context.
// It is a small partial clone, so a failure that outlasts half an hour is not
// about its size, and an hour of silence after a fixed network hiccup is too long.
const teamContextCloneBackoffMax = 30 * time.Minute

// cloneBackoffDelay is how long a clone that has failed `failures` times in a
// row waits before its next attempt: 1m, 2m, 4m, ... up to the cap for the
// kind of failure.
func cloneBackoffDelay(repoType string, permanent bool, failures int) time.Duration {
	maxBackoff := cloneBackoffMax
	switch {
	case permanent:
		maxBackoff = clonePermanentBackoffMax
	case repoType == "team-context":
		maxBackoff = teamContextCloneBackoffMax
	}
	return exponentialBackoff(failures, time.Minute, maxBackoff)
}

// cloneNow is the clock for clone retry windows.
func (s *SyncScheduler) cloneNow() time.Time {
	if s.cloneClock != nil {
		return s.cloneClock()
	}
	return time.Now()
}

// cloneRetryWait reports how long a failed clone must still wait before its
// next attempt (zero once it may run, or if it never failed), how many
// consecutive failures put it there, and when the wait ends.
func (s *SyncScheduler) cloneRetryWait(id string) (wait time.Duration, failures int, next time.Time) {
	failures, next = s.workspaceRegistry.GetCloneRetryInfo(id)
	if failures == 0 || next.IsZero() {
		return 0, failures, next
	}
	return max(next.Sub(s.cloneNow()), 0), failures, next
}

// logCloneBackoff says a team context's clone is waiting out its backoff.
// triggerMissingClones runs every sync cycle, so the line is written once per
// failure: the next failure moves the retry time and earns a new line.
func (s *SyncScheduler) logCloneBackoff(ws WorkspaceState, wait time.Duration, failures int, next time.Time) {
	if logged, ok := s.cloneBackoffLogged.Load(ws.ID); ok && logged.(time.Time).Equal(next) {
		return
	}
	s.cloneBackoffLogged.Store(ws.ID, next)
	s.logger.Info("team context clone backoff",
		"team_id", cmp.Or(ws.TeamID, ws.ID), "next_attempt_in", shortDuration(wait), "failures", failures)
}

// shortDuration renders a wait the way a person says it: "4m", not "4m0s".
func shortDuration(d time.Duration) string {
	text := d.Round(time.Second).String()
	if strings.HasSuffix(text, "m0s") {
		text = strings.TrimSuffix(text, "0s")
	}
	if strings.HasSuffix(text, "h0m") {
		text = strings.TrimSuffix(text, "0m")
	}
	return text
}

// backgroundCloneContext keeps clones tied to the daemon rather than the
// request that triggered them. Unstarted schedulers have no lifecycle yet.
func (s *SyncScheduler) backgroundCloneContext() context.Context {
	if s.ctx != nil {
		return s.ctx
	}
	return context.Background()
}

// clonesInFlight reports whether a clone is running: a checkout in progress
// (background or over IPC), or a background clone not yet in Checkout.
func (s *SyncScheduler) clonesInFlight() bool {
	if s.checkouts.Load() > 0 {
		return true
	}
	running := false
	s.cloneInFlight.Range(func(_, _ any) bool {
		running = true
		return false
	})
	return running
}

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
// Implements exponential backoff on failure: 1min, 2min, 4min, 8min, ..., max 1 hour
// (30 min for a team context).
// After successful clone, clears retry state and invalidates the workspace registry cache
// so the next sync will see the newly cloned repo.
//
// Concurrency is bounded by cloneSem inside Checkout().
func (s *SyncScheduler) cloneInBackground(ctx context.Context, cloneURL, repoPath, repoType, workspaceID string) {
	// cloneWg.Add(1) is called by the caller BEFORE the go statement to
	// avoid a race between Add and Wait.
	defer s.cloneWg.Done()

	// bail out if scheduler is shutting down
	if ctx.Err() != nil {
		return
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
		// the daemon stopped mid-download: not a clone failure, so no error
		// and no backoff; the next start clones again
		if errors.Is(err, errCloneInterrupted) {
			s.logger.Info("clone stopped by daemon shutdown, will retry on next start", "type", repoType, "path", repoPath)
			return
		}
		s.refreshAfterAuthFailure(ctx, err)
		s.logger.Error("background clone failed", "type", repoType, "path", repoPath, "error", err)

		// semaphore timeout is transient — retry next cycle without escalating backoff
		if errors.Is(err, ErrCloneSemaphoreTimeout) {
			s.logger.Info("clone semaphore busy, will retry next cycle", "type", repoType, "path", repoPath)
			return
		}

		// A clone of this path already running comes back as
		// result.InProgress (below), not as an error. This still catches an
		// error that wraps a lock timeout or a context error — same "not our
		// fault, just wait" shape as the semaphore case above, not a real
		// clone failure worth escalating backoff over. Checkout must never
		// hand it the clone's own budget kill (ledgerCloneError,
		// teamContextCloneError): that is a failure, and matching it here
		// meant it was retried every cycle with no backoff.
		if gitutil.IsPreCloneLockBusy(err) {
			s.logger.Info("another clone already in progress for this path, will retry next cycle", "type", repoType, "path", repoPath)
			return
		}

		// increment attempt count and calculate backoff
		newAttempts := attempts + 1

		// classify error to choose backoff strategy
		permanent := isClonePermanentError(err.Error())

		// exponential backoff: 1min, 2min, 4min, 8min, ..., max 1 hour (30 min
		// for a team context); permanent errors cap at 5 min — user may fix
		// creds and we should retry soon
		backoff := cloneBackoffDelay(repoType, permanent, newAttempts)

		nextRetry := s.cloneNow().Add(backoff)
		s.workspaceRegistry.SetCloneRetry(workspaceID, newAttempts, nextRetry)

		hint := "will retry"
		if permanent {
			hint = auth.ReauthenticationRemedy(endpoint.GetForProject(s.config.ProjectRoot)) + " Check repository permissions."
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

	// another actor is already cloning this exact path (ox-baz5.6): not a
	// clone failure, so no backoff; the next cycle sees its result
	if result.InProgress {
		s.logger.Info("another clone already in progress for this path, will retry next cycle", "type", repoType, "path", repoPath)
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
				s.logger.Warn("failed to backfill config last sync", "type", repoType, "path", repoPath, "error", err)
			}
			s.recordSyncState(ctx, repoPath)
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
			s.logger.Warn("failed to update config last sync after clone", "type", repoType, "path", repoPath, "error", err)
		}
		s.recordSyncState(ctx, repoPath)
		if repoType == "ledger" && s.config.LedgerPath == "" && s.onLedgerCloned != nil {
			s.onLedgerCloned(repoPath)
		}
	}

	// update exists flags so status renders correctly before next Reload()
	s.workspaceRegistry.RefreshExists()
	// invalidate cache so next sync sees the cloned repo
	s.workspaceRegistry.InvalidateConfigCache()
}

// triggerMissingClones immediately triggers clones for workspaces that don't exist
// but have a clone URL. This is called on startup for self-healing behavior.
// Also tries to bootstrap ledger from API if not in credentials.
func (s *SyncScheduler) triggerMissingClones(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	// check ledger - may need to fetch URL from API first
	ledger := s.workspaceRegistry.GetLedger()
	if ledger == nil || ledger.CloneURL == "" {
		// try to fetch ledger URL from API using repo_id
		s.fetchLedgerURLFromAPI(ctx)
		// reload after API fetch
		ledger = s.workspaceRegistry.GetLedger()
	}

	if ledger != nil && !ledger.Exists && ledger.CloneURL != "" {
		if s.workspaceRegistry.ShouldRetryClone(ledger.ID) {
			s.logger.Info("triggering immediate ledger clone (self-healing)", "path", ledger.Path)
			if s.addClone() {
				go s.cloneInBackground(s.backgroundCloneContext(), ledger.CloneURL, ledger.Path, "ledger", ledger.ID)
			}
		}
	}

	// check team contexts
	for _, ws := range s.workspaceRegistry.GetTeamContexts() {
		if !ws.Exists && ws.CloneURL != "" && ws.Path != "" {
			if wait, failures, next := s.cloneRetryWait(ws.ID); wait > 0 {
				s.logCloneBackoff(ws, wait, failures, next)
				continue
			}
			s.logger.Info("triggering immediate team context clone (self-healing)",
				"team", ws.TeamName, "path", ws.Path)
			if s.addClone() {
				go s.cloneInBackground(s.backgroundCloneContext(), ws.CloneURL, ws.Path, "team-context", ws.ID)
			}
		}
	}
}
