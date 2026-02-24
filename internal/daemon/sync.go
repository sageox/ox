// Package daemon implements the background sync daemon for ledger and team contexts.
//
// The daemon only performs git pull (read) operations. The CLI handles
// add/commit/push (write) operations directly via the session upload pipeline.
//
// # NETWORK DISCONNECTION HANDLING
//
// The daemon operates normally when the internet is disconnected. This is NOT a
// failure mode - developers frequently work offline (planes, cafes, etc.).
//
// Design principles:
//   - Network failures are expected and handled gracefully
//   - Logs should NOT fill up during disconnection (use Warn, not Error)
//   - Operations retry on the next sync interval when connectivity returns
//   - The daemon should return to normal operation automatically when reconnected
//
// SageOx is multiplayer, but the underlying git repos work fine offline.
// Only API calls and git fetch require daemon connectivity; push is CLI-side.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/gitserver"
	"github.com/sageox/ox/internal/version"
)

// Sync timing constants - extracted for clarity and testability.
const (
	// minFetchHeadAge is the minimum age of FETCH_HEAD before we'll fetch again.
	// Prevents redundant fetches if another process (e.g., user or another daemon) fetched recently.
	minFetchHeadAge = 2 * time.Minute

	// minTeamContextFetchAge is the minimum age before re-fetching a team context.
	// Team contexts are shared across repos, so we use a longer interval to reduce redundant fetches.
	minTeamContextFetchAge = 5 * time.Minute

	// teamDiscoveryInterval is how often we re-fetch the team list from the API,
	// independent of credential token expiry. This ensures new teams are discovered
	// promptly even when the token is still fresh.
	teamDiscoveryInterval = 5 * time.Minute

	// maxConcurrentClones limits background clone operations to prevent resource exhaustion.
	// 100 team contexts shouldn't spawn 100 concurrent git clones.
	maxConcurrentClones = 3
)

// credentialPattern matches oauth2:TOKEN@ patterns in git output.
// Used to sanitize credentials before logging.
var credentialPattern = regexp.MustCompile(`oauth2:[^@]+@`)

// sanitizeGitOutput removes credentials from git command output.
// Replaces oauth2:TOKEN@ patterns with oauth2:***@ to prevent credential leaks in logs.
func sanitizeGitOutput(output string) string {
	return credentialPattern.ReplaceAllString(output, "oauth2:***@")
}

// ErrInvalidRepoPath indicates the repo path failed security validation.
var ErrInvalidRepoPath = errors.New("invalid repo path: path traversal or unsafe location detected")

// SyncMetrics tracks observability counters and timing for sync operations.
type SyncMetrics struct {
	mu sync.RWMutex

	// counters
	PullSuccessCount   int64 `json:"pull_success_count"`
	PullFailureCount   int64 `json:"pull_failure_count"`
	ConflictCount      int64 `json:"conflict_count"`
	ForcePushCount     int64 `json:"force_push_count"`
	TeamSyncCount      int64 `json:"team_sync_count"`
	TeamSyncErrorCount int64 `json:"team_sync_error_count"`

	// timing (rolling window, last 100 samples)
	pullDurations []time.Duration

	// timestamps
	LastPullSuccess time.Time `json:"last_pull_success,omitempty"`
	LastPullFailure time.Time `json:"last_pull_failure,omitempty"`
	LastConflict    time.Time `json:"last_conflict,omitempty"`

	// max samples for duration tracking
	maxSamples int
}

// NewSyncMetrics creates a new SyncMetrics instance.
func NewSyncMetrics() *SyncMetrics {
	return &SyncMetrics{
		maxSamples:    100,
		pullDurations: make([]time.Duration, 0, 100),
	}
}

// RecordPullSuccess records a successful pull operation.
func (m *SyncMetrics) RecordPullSuccess(duration time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.PullSuccessCount++
	m.LastPullSuccess = time.Now()
	m.pullDurations = appendDuration(m.pullDurations, duration, m.maxSamples)
}

// RecordPullFailure records a failed pull operation.
func (m *SyncMetrics) RecordPullFailure() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.PullFailureCount++
	m.LastPullFailure = time.Now()
}

// RecordConflict records a merge conflict detection.
func (m *SyncMetrics) RecordConflict() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.ConflictCount++
	m.LastConflict = time.Now()
}

// RecordForcePush records a force push detection.
func (m *SyncMetrics) RecordForcePush() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.ForcePushCount++
}

// RecordTeamSync records a successful team context sync.
func (m *SyncMetrics) RecordTeamSync() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.TeamSyncCount++
}

// RecordTeamSyncError records a failed team context sync.
func (m *SyncMetrics) RecordTeamSyncError() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.TeamSyncErrorCount++
}

// SyncMetricsSnapshot is a point-in-time copy of sync metrics for reporting.
type SyncMetricsSnapshot struct {
	PullSuccessCount   int64         `json:"pull_success_count"`
	PullFailureCount   int64         `json:"pull_failure_count"`
	ConflictCount      int64         `json:"conflict_count"`
	ForcePushCount     int64         `json:"force_push_count"`
	TeamSyncCount      int64         `json:"team_sync_count"`
	TeamSyncErrorCount int64         `json:"team_sync_error_count"`
	LastPullSuccess    time.Time     `json:"last_pull_success,omitempty"`
	LastPullFailure    time.Time     `json:"last_pull_failure,omitempty"`
	LastConflict       time.Time     `json:"last_conflict,omitempty"`
	AvgPullDuration    time.Duration `json:"avg_pull_duration"`
	P95PullDuration    time.Duration `json:"p95_pull_duration"`
}

// Snapshot returns a point-in-time copy of metrics for reporting.
func (m *SyncMetrics) Snapshot() SyncMetricsSnapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return SyncMetricsSnapshot{
		PullSuccessCount:   m.PullSuccessCount,
		PullFailureCount:   m.PullFailureCount,
		ConflictCount:      m.ConflictCount,
		ForcePushCount:     m.ForcePushCount,
		TeamSyncCount:      m.TeamSyncCount,
		TeamSyncErrorCount: m.TeamSyncErrorCount,
		LastPullSuccess:    m.LastPullSuccess,
		LastPullFailure:    m.LastPullFailure,
		LastConflict:       m.LastConflict,
		AvgPullDuration:    avgDuration(m.pullDurations),
		P95PullDuration:    p95Duration(m.pullDurations),
	}
}

// appendDuration appends a duration to a slice, maintaining max size.
func appendDuration(durations []time.Duration, d time.Duration, maxSamples int) []time.Duration {
	durations = append(durations, d)
	if len(durations) > maxSamples {
		durations = durations[len(durations)-maxSamples:]
	}
	return durations
}

// avgDuration calculates the average duration.
func avgDuration(durations []time.Duration) time.Duration {
	if len(durations) == 0 {
		return 0
	}
	var total time.Duration
	for _, d := range durations {
		total += d
	}
	return total / time.Duration(len(durations))
}

// p95Duration calculates the 95th percentile duration.
func p95Duration(durations []time.Duration) time.Duration {
	if len(durations) == 0 {
		return 0
	}
	// copy and sort
	sorted := make([]time.Duration, len(durations))
	copy(sorted, durations)
	slices.SortFunc(sorted, func(a, b time.Duration) int {
		if a < b {
			return -1
		}
		if a > b {
			return 1
		}
		return 0
	})
	// p95 index
	idx := int(float64(len(sorted)) * 0.95)
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

// hasLockFiles checks for stale git lock files that indicate crashed processes.
// These files block all git operations and must be manually removed.
// Returns a slice of lock file names found (e.g., "index.lock").
func hasLockFiles(gitDir string) []string {
	lockFiles := []string{
		"index.lock",
		"shallow.lock",
		"config.lock",
		"HEAD.lock",
	}
	var found []string
	for _, lock := range lockFiles {
		path := filepath.Join(gitDir, lock)
		if _, err := os.Stat(path); err == nil {
			found = append(found, lock)
		}
	}
	return found
}

// SyncScheduler manages periodic sync operations.
type SyncScheduler struct {
	config *Config
	logger *slog.Logger

	// state
	mu       sync.Mutex
	lastSync time.Time

	// per-operation flags to reduce lock contention
	// each operation only blocks itself, not unrelated operations
	pullInProgress        bool
	lastCredentialRefresh time.Time // dedup concurrent credential refresh calls
	lastTeamDiscovery     time.Time // dedup concurrent team discovery calls

	// error tracking
	recentErrors  []syncError
	maxRecentErrs int

	// sync history (for insights/sparklines)
	syncHistory    []SyncEvent
	maxSyncHistory int

	// observability metrics
	metrics *SyncMetrics

	// remote change tracking - tracks FETCH_HEAD mtime to distinguish
	// "when we synced" from "when remote had new content"
	remoteChangeTracker *ActivityTracker

	// unified workspace registry - tracks ledger and team contexts
	workspaceRegistry *WorkspaceRegistry

	// channels
	triggerChan chan struct{}

	// worker pool for bounded clone concurrency
	cloneSem      chan struct{} // semaphore limiting concurrent clones
	cloneInFlight sync.Map      // tracks workspace IDs with clone in progress (dedup)

	// test hooks (nil in production)
	onBeforeCloneSem func() // called just before acquiring cloneSem; tests use this to observe blocking

	// callbacks
	onActivity   func()                                                           // called on any sync activity
	onTelemetry  func(syncType, operation, status string, duration time.Duration) // called on sync complete for telemetry
	getAuthToken func() string                                                    // returns cached auth token from heartbeat

	// issues tracker for health check system
	issues *IssueTracker

	// version cache for GitHub release checks
	versionCache *VersionCache
}

// syncError tracks a sync error with timestamp.
type syncError struct {
	Time    time.Time
	Message string
}

// SyncEvent tracks a successful sync with metadata.
type SyncEvent struct {
	Time         time.Time     `json:"time"`
	Type         string        `json:"type"` // "pull", "push", "full", "team_context"
	Duration     time.Duration `json:"duration"`
	FilesChanged int           `json:"files_changed"`
}

// TeamContextSyncStatus tracks sync status for a team context repo.
type TeamContextSyncStatus struct {
	TeamID   string    `json:"team_id"`
	TeamName string    `json:"team_name"`
	Path     string    `json:"path"`
	CloneURL string    `json:"clone_url,omitempty"` // git remote URL
	LastSync time.Time `json:"last_sync"`
	LastErr  string    `json:"last_error,omitempty"`
	Exists   bool      `json:"exists"` // whether the local path exists
}

// NewSyncScheduler creates a new sync scheduler.
func NewSyncScheduler(cfg *Config, logger *slog.Logger) *SyncScheduler {
	// get repo name for workspace registry
	repoName := filepath.Base(cfg.ProjectRoot)

	return &SyncScheduler{
		config:              cfg,
		logger:              logger,
		triggerChan:         make(chan struct{}, 1), // buffered to prevent blocking on trigger
		cloneSem:            make(chan struct{}, maxConcurrentClones),
		maxRecentErrs:       10,  // keep last 10 errors
		maxSyncHistory:      100, // keep last 100 syncs for sparklines
		metrics:             NewSyncMetrics(),
		remoteChangeTracker: NewActivityTracker(100),
		workspaceRegistry:   NewWorkspaceRegistry(cfg.ProjectRoot, repoName),
		versionCache:        NewVersionCache(logger),
	}
}

// SetActivityCallback sets the callback for activity tracking.
func (s *SyncScheduler) SetActivityCallback(cb func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onActivity = cb
}

// SetTelemetryCallback sets the callback for telemetry events.
// Called when sync operations complete with syncType, operation, status, and duration.
func (s *SyncScheduler) SetTelemetryCallback(cb func(syncType, operation, status string, duration time.Duration)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onTelemetry = cb
}

// SetAuthTokenGetter sets the callback to get auth token from heartbeat cache.
// Used for lazy credential refresh via /api/v1/cli/repos.
func (s *SyncScheduler) SetAuthTokenGetter(cb func() string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.getAuthToken = cb
}

// SetIssueTracker sets the issue tracker for reporting sync issues.
// Issues are reported when the daemon encounters problems it cannot resolve
// with deterministic code (e.g., merge conflicts requiring LLM reasoning).
func (s *SyncScheduler) SetIssueTracker(tracker *IssueTracker) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.issues = tracker
}

// Metrics returns the sync metrics for observability.
func (s *SyncScheduler) Metrics() *SyncMetrics {
	return s.metrics
}

// WorkspaceRegistry returns the workspace registry for status queries.
func (s *SyncScheduler) WorkspaceRegistry() *WorkspaceRegistry {
	return s.workspaceRegistry
}

// recordActivity calls the activity callback if set.
func (s *SyncScheduler) recordActivity() {
	s.mu.Lock()
	cb := s.onActivity
	s.mu.Unlock()
	if cb != nil {
		cb()
	}
}

// recordSync records a successful sync event and emits telemetry.
func (s *SyncScheduler) recordSync(syncType string, duration time.Duration, filesChanged int) {
	s.mu.Lock()

	s.syncHistory = append(s.syncHistory, SyncEvent{
		Time:         time.Now(),
		Type:         syncType,
		Duration:     duration,
		FilesChanged: filesChanged,
	})

	// keep only recent history
	if len(s.syncHistory) > s.maxSyncHistory {
		s.syncHistory = s.syncHistory[len(s.syncHistory)-s.maxSyncHistory:]
	}

	// capture callback under lock
	cb := s.onTelemetry
	s.mu.Unlock()

	// emit telemetry outside lock
	if cb != nil {
		// map sync type to operation (pull/push/team_context -> pull/push/sync)
		operation := syncType
		if syncType == "team_context" {
			operation = "sync"
		}
		cb("ledger", operation, "success", duration)
	}
}

// recordRemoteChange records when remote changes were observed for a repo.
// Uses FETCH_HEAD mtime to track when the remote had new content,
// distinct from when we actually synced/pulled.
func (s *SyncScheduler) recordRemoteChange(repoPath string, mtime time.Time) {
	s.remoteChangeTracker.RecordAt(repoPath, mtime)
}

// RemoteChangeActivity returns the remote change tracker for status display.
func (s *SyncScheduler) RemoteChangeActivity() *ActivityTracker {
	return s.remoteChangeTracker
}

// LastRemoteChange returns the most recent FETCH_HEAD mtime for a repo.
// Returns zero time if no remote changes have been observed.
func (s *SyncScheduler) LastRemoteChange(repoPath string) time.Time {
	return s.remoteChangeTracker.Last(repoPath)
}

// SyncHistory returns recent sync events for display.
func (s *SyncScheduler) SyncHistory() []SyncEvent {
	s.mu.Lock()
	defer s.mu.Unlock()

	// return a copy
	result := make([]SyncEvent, len(s.syncHistory))
	copy(result, s.syncHistory)
	return result
}

// SyncStats returns aggregate statistics about recent syncs.
func (s *SyncScheduler) SyncStats() SyncStatistics {
	s.mu.Lock()
	defer s.mu.Unlock()

	stats := SyncStatistics{}
	if len(s.syncHistory) == 0 {
		return stats
	}

	stats.TotalSyncs = len(s.syncHistory)

	// calculate stats from last hour
	cutoff := time.Now().Add(-time.Hour)
	var lastHourCount int
	var totalDuration time.Duration

	for _, e := range s.syncHistory {
		totalDuration += e.Duration
		if e.Time.After(cutoff) {
			lastHourCount++
		}
	}

	stats.SyncsLastHour = lastHourCount
	stats.AvgDuration = totalDuration / time.Duration(len(s.syncHistory))

	// oldest and newest
	stats.OldestSync = s.syncHistory[0].Time
	stats.NewestSync = s.syncHistory[len(s.syncHistory)-1].Time

	return stats
}

// SyncStatistics holds aggregate sync metrics.
type SyncStatistics struct {
	TotalSyncs    int
	SyncsLastHour int
	AvgDuration   time.Duration
	OldestSync    time.Time
	NewestSync    time.Time
}

// recordError records a sync error for diagnostics.
func (s *SyncScheduler) recordError(msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.recentErrors = append(s.recentErrors, syncError{
		Time:    time.Now(),
		Message: msg,
	})

	// keep only recent errors
	if len(s.recentErrors) > s.maxRecentErrs {
		s.recentErrors = s.recentErrors[len(s.recentErrors)-s.maxRecentErrs:]
	}
}

// RecentErrorCount returns the count of recent errors (last hour).
func (s *SyncScheduler) RecentErrorCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	cutoff := time.Now().Add(-time.Hour)
	count := 0
	for _, e := range s.recentErrors {
		if e.Time.After(cutoff) {
			count++
		}
	}
	return count
}

// LastError returns the most recent error message and time.
func (s *SyncScheduler) LastError() (string, time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.recentErrors) == 0 {
		return "", time.Time{}
	}
	last := s.recentErrors[len(s.recentErrors)-1]
	return last.Message, last.Time
}

// Start starts the sync scheduler.
func (s *SyncScheduler) Start(ctx context.Context) {
	// load initial workspace state from config
	if err := s.workspaceRegistry.LoadFromConfig(); err != nil {
		s.logger.Warn("failed to load workspace registry", "error", err)
	}

	readTicker := time.NewTicker(s.config.SyncIntervalRead)
	defer readTicker.Stop()

	// Daemon is read-only: CLI handles ledger pushes directly.

	// heartbeat ticker - write heartbeats every 5 minutes
	heartbeatInterval := 5 * time.Minute
	heartbeatTicker := time.NewTicker(heartbeatInterval)
	defer heartbeatTicker.Stop()

	// version check ticker - check GitHub for new releases (ETag conditional requests)
	var versionCheckTicker *time.Ticker
	var versionCheckChan <-chan time.Time
	if s.config.VersionCheckInterval > 0 {
		versionCheckTicker = time.NewTicker(s.config.VersionCheckInterval)
		versionCheckChan = versionCheckTicker.C
		defer versionCheckTicker.Stop()

		// load cached version data and do initial check on startup
		_ = s.versionCache.Load()
		go s.checkLatestVersion(ctx)
	}

	// team context sync (lower priority, less frequent)
	var teamContextTicker *time.Ticker
	var teamContextChan <-chan time.Time
	if s.config.TeamContextSyncInterval > 0 && s.config.ProjectRoot != "" {
		teamContextTicker = time.NewTicker(s.config.TeamContextSyncInterval)
		teamContextChan = teamContextTicker.C
		defer teamContextTicker.Stop()

		s.logger.Info("sync scheduler started",
			"read_interval", s.config.SyncIntervalRead,
			"team_context_interval", s.config.TeamContextSyncInterval,
			"heartbeat_interval", heartbeatInterval,
		)

		// delayed team context sync for regular pulls (not just cloning)
		go func() {
			time.Sleep(5 * time.Second)
			s.pullTeamContexts(ctx)
		}()
	} else {
		s.logger.Info("sync scheduler started",
			"read_interval", s.config.SyncIntervalRead,
			"heartbeat_interval", heartbeatInterval,
		)
	}

	// write initial heartbeat
	s.writeHeartbeats()

	// immediate anti-entropy check on startup (same logic as periodic ticker)
	s.triggerMissingClones()

	for {
		select {
		case <-ctx.Done():
			s.logger.Info("sync scheduler stopped")
			return

		case <-readTicker.C:
			s.pullChanges(ctx)

		case <-teamContextChan:
			s.pullTeamContexts(ctx)

		case <-heartbeatTicker.C:
			s.writeHeartbeats()

		case <-versionCheckChan:
			s.checkLatestVersion(ctx)

		case <-s.triggerChan:
			// triggered by file watcher, do full sync
			s.syncAll(ctx)
		}
	}
}

// TriggerSync triggers an immediate sync (debounced by watcher).
func (s *SyncScheduler) TriggerSync() {
	select {
	case s.triggerChan <- struct{}{}:
	default:
		// already triggered, skip
	}
}

// TriggerAntiEntropy triggers self-healing checks for missing workspaces.
// This is called by IPC when doctor or other commands want to ensure
// ledgers and team contexts are cloned.
func (s *SyncScheduler) TriggerAntiEntropy() {
	s.triggerMissingClones()
}

// LastSync returns the timestamp of the last successful sync.
func (s *SyncScheduler) LastSync() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastSync
}

// pullChanges fetches and pulls from remote (used by scheduler).
// Also performs anti-entropy: checks for missing workspaces and triggers clones.
// Errors from doPull are already logged and recorded; background sync continues.
func (s *SyncScheduler) pullChanges(ctx context.Context) {
	// anti-entropy: ensure missing workspaces get cloned
	s.triggerMissingClones()
	_ = s.doPull(ctx, nil, false)
}

// checkLatestVersion fetches the latest GitHub release using ETag conditional requests.
// Called periodically by the sync scheduler to keep the version cache warm.
func (s *SyncScheduler) checkLatestVersion(ctx context.Context) {
	if err := s.versionCache.CheckAndUpdate(ctx); err != nil {
		s.logger.Warn("version check failed", "error", err)
	}
}

// shouldSyncOrBypass checks if a sync should proceed given backoff state.
// If forceSync is true (user-initiated), clears backoff and proceeds.
// If forceSync is false (background ticker) and backoff is active, logs and returns false.
func (s *SyncScheduler) shouldSyncOrBypass(id string, forceSync bool) bool {
	if s.workspaceRegistry.ShouldSync(id) {
		return true
	}
	if forceSync {
		s.workspaceRegistry.ClearSyncFailures(id)
		return true
	}
	failures, nextRetry := s.workspaceRegistry.GetSyncRetryInfo(id)
	s.logger.Warn("sync in backoff, skipping", "id", id, "failures", failures, "next_retry", nextRetry)
	if s.issues != nil {
		s.issues.SetIssue(DaemonIssue{
			Type:     IssueTypeSyncBackoff,
			Severity: SeverityWarning,
			Repo:     id,
			Summary:  fmt.Sprintf("Sync suspended after %d consecutive failures (retrying at %s)", failures, nextRetry.Format(time.Kitchen)),
		})
	}
	return false
}

// doPull fetches and pulls from remote with optional progress updates.
// If ledger doesn't exist locally but has a clone URL, spawns background clone.
// Returns an error if fetch or pull fails (for on-demand sync error reporting).
// Callers that don't need the error (background scheduler) can ignore it.
// forceSync=true bypasses backoff (user-initiated syncs via IPC).
func (s *SyncScheduler) doPull(ctx context.Context, progress *ProgressWriter, forceSync bool) error {
	if s.config.LedgerPath == "" {
		return nil
	}

	// check if ledger is a valid git repo - if not, try to auto-clone
	// handles both missing directories and directories left behind by failed clones
	if !pathIsGitRepo(s.config.LedgerPath) {
		// reload workspace registry to get clone URL
		if err := s.workspaceRegistry.LoadFromConfig(); err == nil {
			if ledger := s.workspaceRegistry.GetLedger(); ledger != nil {
				// if no clone URL from credentials, try fetching from API
				if ledger.CloneURL == "" {
					s.fetchLedgerURLFromAPI()
					// reload ledger after API fetch
					ledger = s.workspaceRegistry.GetLedger()
				}

				if ledger != nil && ledger.CloneURL != "" {
					// check if we should retry (respects exponential backoff)
					if !s.workspaceRegistry.ShouldRetryClone(ledger.ID) {
						attempts, nextRetry := s.workspaceRegistry.GetCloneRetryInfo(ledger.ID)
						s.logger.Debug("ledger clone in backoff, skipping",
							"attempts", attempts, "next_retry", nextRetry)
						return nil
					}

					s.logger.Info("ledger not cloned, starting background clone", "path", ledger.Path)
					if progress != nil {
						_ = progress.WriteStage("cloning", "Cloning ledger in background...")
					}
					// clone in background goroutine - don't block sync loop
					go s.cloneInBackground(ledger.CloneURL, ledger.Path, "ledger", ledger.ID)
				}
			}
		}
		return nil // can't pull from repo that isn't cloned yet
	}

	// skip if repo stuck in broken rebase state
	rebaseMerge := filepath.Join(s.config.LedgerPath, ".git", "rebase-merge")
	rebaseApply := filepath.Join(s.config.LedgerPath, ".git", "rebase-apply")
	if _, err := os.Stat(rebaseMerge); err == nil {
		s.logger.Debug("repo in rebase state, skipping pull", "path", s.config.LedgerPath)
		return nil
	}
	if _, err := os.Stat(rebaseApply); err == nil {
		s.logger.Debug("repo in rebase-apply state, skipping pull", "path", s.config.LedgerPath)
		return nil
	}

	// check for stale lock files from crashed git processes
	gitDir := filepath.Join(s.config.LedgerPath, ".git")
	if locks := hasLockFiles(gitDir); len(locks) > 0 {
		s.logger.Warn("git lock files detected, skipping pull",
			"path", s.config.LedgerPath,
			"locks", strings.Join(locks, ", "))
		if s.issues != nil {
			s.issues.SetIssue(DaemonIssue{
				Type:     IssueTypeGitLock,
				Severity: SeverityWarning,
				Repo:     "ledger",
				Summary: fmt.Sprintf("Stale lock files blocking sync: %s. If no git commands are running, remove with: rm %s/{%s}",
					strings.Join(locks, ", "),
					gitDir,
					strings.Join(locks, ",")),
			})
		}
		return nil
	}
	// clear lock issue if previously set but now resolved
	if s.issues != nil {
		s.issues.ClearIssue(IssueTypeGitLock, "ledger")
	}

	// sync backoff — skip if recent sync failures triggered backoff
	if !s.shouldSyncOrBypass("ledger", forceSync) {
		return nil
	}

	s.mu.Lock()
	if s.pullInProgress {
		s.mu.Unlock()
		if progress != nil {
			// on-demand sync: tell the user a sync is already running
			_ = progress.WriteStage("skipped", "Pull already in progress")
		}
		return nil
	}
	s.pullInProgress = true
	s.mu.Unlock()

	startTime := time.Now()

	defer func() {
		s.mu.Lock()
		s.pullInProgress = false
		s.mu.Unlock()
	}()

	// ls-remote SHA check — skip if remote HEAD matches local (nothing new to pull).
	// Cheaper than git fetch: only hits /info/refs, no upload-pack negotiation.
	if s.remoteRefCheck(ctx, s.config.LedgerPath) {
		// remote matches local — clear any previous failure state
		s.workspaceRegistry.ClearSyncFailures("ledger")

		// update lastSync: we successfully verified the ledger is current
		s.mu.Lock()
		s.lastSync = time.Now()
		s.mu.Unlock()

		// persist sync timestamp so "ox status" shows when we last checked,
		// not when content last changed
		if err := s.workspaceRegistry.UpdateConfigLastSync("ledger"); err != nil {
			s.logger.Warn("failed to update ledger config last sync", "error", err)
		}

		if progress != nil {
			_ = progress.WriteStage("skipped", "Remote unchanged, skipping pull")
		}
		return nil
	}

	// FETCH_HEAD mtime dedup (secondary: cross-daemon coordination, crash loop protection).
	// Kept as fallback for when ls-remote can't run (credential issues, etc).
	fetchHead := filepath.Join(s.config.LedgerPath, ".git", "FETCH_HEAD")
	if info, err := os.Stat(fetchHead); err == nil {
		threshold := max(s.config.SyncIntervalRead/2, minFetchHeadAge)
		if time.Since(info.ModTime()) < threshold {
			s.logger.Debug("ledger recently fetched, skipping", "age", time.Since(info.ModTime()))
			// persist sync timestamp — another daemon recently fetched, ledger is current
			if err := s.workspaceRegistry.UpdateConfigLastSync("ledger"); err != nil {
				s.logger.Warn("failed to update ledger config last sync", "error", err)
			}
			if progress != nil {
				_ = progress.WriteStage("skipped", "Recently fetched, skipping pull")
			}
			return nil
		}
	}

	if progress != nil {
		_ = progress.WriteStage("fetching", "Fetching from remote...")
	}
	s.logger.Debug("pulling changes")

	// refresh remote URL if credentials changed (e.g., user switch via ox login)
	projectEndpoint := endpoint.GetForProject(s.config.ProjectRoot)
	if err := gitserver.RefreshRemoteCredentials(s.config.LedgerPath, projectEndpoint); err != nil {
		s.logger.Warn("ledger remote credential refresh failed", "error", err)
	}

	// git fetch
	// git fetch (capture stderr for diagnosable error messages)
	fetchCmd := exec.CommandContext(ctx, "git", "-C", s.config.LedgerPath, "fetch", "--quiet")
	if output, err := fetchCmd.CombinedOutput(); err != nil {
		detail := sanitizeGitOutput(strings.TrimSpace(string(output)))
		s.logger.Warn("fetch failed", "error", err, "output", detail)
		if detail != "" {
			s.recordError(fmt.Sprintf("fetch failed: %s (%v)", detail, err))
		} else {
			s.recordError(fmt.Sprintf("fetch failed: %v", err))
		}
		s.metrics.RecordPullFailure()
		s.workspaceRegistry.RecordSyncFailure("ledger")
		if detail != "" {
			return fmt.Errorf("ledger fetch failed: %s (%w)", detail, err)
		}
		return fmt.Errorf("ledger fetch failed: %w", err)
	}

	// track FETCH_HEAD mtime to record when remote had new content
	if info, err := os.Stat(fetchHead); err == nil {
		s.recordRemoteChange(s.config.LedgerPath, info.ModTime())
	}

	// detect force push (diverged branches)
	if s.detectForcePush(ctx) {
		s.logger.Warn("force push detected on ledger, skipping pull")
		s.metrics.RecordForcePush()
		if progress != nil {
			_ = progress.WriteStage("skipped", "Force push detected, skipping pull")
		}
		if s.issues != nil {
			s.issues.SetIssue(DaemonIssue{
				Type:     IssueTypeDiverged,
				Repo:     "ledger",
				Severity: SeverityError,
				Summary:  "Ledger has diverged from remote (force push detected). Run 'ox doctor --fix' to re-clone.",
			})
		}
		return errors.New("ledger diverged from remote (force push detected)")
	}

	if progress != nil {
		_ = progress.WriteStage("pulling", "Pulling changes...")
	}

	// git pull --rebase (capture stderr for diagnosable error messages)
	pullCmd := exec.CommandContext(ctx, "git", "-C", s.config.LedgerPath, "pull", "--rebase", "--quiet")
	if output, err := pullCmd.CombinedOutput(); err != nil {
		detail := sanitizeGitOutput(strings.TrimSpace(string(output)))
		s.logger.Warn("pull failed", "error", err, "output", detail)
		if detail != "" {
			s.recordError(fmt.Sprintf("pull failed: %s (%v)", detail, err))
		} else {
			s.recordError(fmt.Sprintf("pull failed: %v", err))
		}
		s.metrics.RecordPullFailure()
		s.workspaceRegistry.RecordSyncFailure("ledger")

		// check if it's a merge conflict
		statusCmd := exec.CommandContext(ctx, "git", "-C", s.config.LedgerPath, "status", "--porcelain")
		if statusOutput, _ := statusCmd.Output(); strings.Contains(string(statusOutput), "UU") {
			s.metrics.RecordConflict()

			// report issue — daemon does not write; next pull will skip via rebase-state check
			if s.issues != nil {
				s.issues.SetIssue(DaemonIssue{
					Type:            IssueTypeMergeConflict,
					Severity:        SeverityError,
					Repo:            "ledger",
					Summary:         "Ledger has merge conflicts. Run 'ox doctor --fix' to re-clone.",
					RequiresConfirm: true, // merge resolution needs human approval
				})
			}
		}
		if detail != "" {
			return fmt.Errorf("ledger pull failed: %s (%w)", detail, err)
		}
		return fmt.Errorf("ledger pull failed: %w", err)
	}

	// sync succeeded - clear failure backoff, merge conflict, and sync backoff issues
	s.workspaceRegistry.ClearSyncFailures("ledger")
	if s.issues != nil {
		s.issues.ClearIssue(IssueTypeMergeConflict, "ledger")
		s.issues.ClearIssue(IssueTypeSyncBackoff, "ledger")
	}

	duration := time.Since(startTime)
	s.recordSync("pull", duration, 0) // pull doesn't track file count yet
	s.metrics.RecordPullSuccess(duration)
	s.recordActivity() // mark as activity

	s.mu.Lock()
	s.lastSync = time.Now()
	s.mu.Unlock()

	// persist sync timestamp so status shows "synced" after daemon restart
	if err := s.workspaceRegistry.UpdateConfigLastSync("ledger"); err != nil {
		s.logger.Warn("failed to update ledger config last sync", "error", err)
	}

	s.logger.Debug("pull complete", "duration", duration)
	return nil
}

// detectForcePush checks if local and remote have diverged (force push scenario).
func (s *SyncScheduler) detectForcePush(ctx context.Context) bool {
	// check if branches have diverged
	cmd := exec.CommandContext(ctx, "git", "-C", s.config.LedgerPath,
		"rev-list", "--left-right", "--count", "origin/main...HEAD")
	output, err := cmd.Output()
	if err != nil {
		return false
	}

	// output format: "ahead\tbehind"
	// if both > 0, branches have diverged
	parts := strings.Fields(string(output))
	if len(parts) != 2 {
		return false
	}

	behind := parts[0] != "0"
	ahead := parts[1] != "0"

	return behind && ahead // diverged = both ahead AND behind
}

// credentialRefreshThreshold is how close to expiry credentials must be
// before we proactively refresh them (1 hour)
const credentialRefreshThreshold = 1 * time.Hour

// refreshCredentialsIfNeeded checks if git credentials are expired, near expiry,
// or for a different endpoint, and refreshes them from the cloud API if needed.
// This is lazy refresh - called before sync operations that need valid credentials.
func (s *SyncScheduler) refreshCredentialsIfNeeded() {
	// dedup: stamp-then-release to prevent TOCTOU race where concurrent callers
	// both observe a stale timestamp and both proceed to hit the API.
	s.mu.Lock()
	if !s.lastCredentialRefresh.IsZero() && time.Since(s.lastCredentialRefresh) < 5*time.Minute {
		s.mu.Unlock()
		return
	}
	s.lastCredentialRefresh = time.Now() // stamp before releasing lock
	s.mu.Unlock()

	// get the endpoint for this project
	projectEndpoint := endpoint.GetForProject(s.config.ProjectRoot)

	// load credentials for this specific endpoint
	creds, err := gitserver.LoadCredentialsForEndpoint(projectEndpoint)
	if err != nil {
		s.logger.Debug("failed to load credentials for refresh check", "error", err)
	}

	// check if credentials exist and are fresh
	if creds != nil && !creds.ExpiresAt.IsZero() && time.Until(creds.ExpiresAt) > credentialRefreshThreshold {
		// credentials are still fresh, no refresh needed
		return
	}

	refreshReason := "no credentials for endpoint"
	if creds != nil {
		refreshReason = "credentials expired or near expiry"
	}

	s.logger.Info("refreshing git credentials from API", "reason", refreshReason, "endpoint", projectEndpoint)

	// get auth token for this endpoint
	token, err := auth.GetTokenForEndpoint(projectEndpoint)
	if err != nil {
		s.logger.Warn("failed to get auth token for credential refresh", "error", err)
		return
	}
	if token == nil || token.AccessToken == "" {
		s.logger.Debug("no auth token available for credential refresh")
		return
	}

	// fetch fresh credentials from API using project endpoint
	client := api.NewRepoClientWithEndpoint(projectEndpoint).WithAuthToken(token.AccessToken)
	reposResp, err := client.GetRepos()
	if err != nil {
		s.logger.Warn("failed to fetch repos for credential refresh", "error", err)
		return
	}
	if reposResp == nil {
		s.logger.Debug("no repos returned from API")
		return
	}

	// build and save new credentials
	newCreds := gitserver.GitCredentials{
		Token:     reposResp.Token,
		ServerURL: reposResp.ServerURL,
		Username:  reposResp.Username,
		ExpiresAt: reposResp.ExpiresAt,
		Repos:     make(map[string]gitserver.RepoEntry),
	}
	for _, repo := range reposResp.Repos {
		newCreds.AddRepo(gitserver.RepoEntry{
			Name:   repo.Name,
			Type:   repo.Type,
			URL:    repo.URL,
			TeamID: repo.StableID(),
		})
	}

	if err := gitserver.SaveCredentialsForEndpoint(projectEndpoint, newCreds); err != nil {
		s.logger.Warn("failed to save refreshed credentials", "error", err)
		return
	}

	s.logger.Info("git credentials refreshed successfully", "expires", newCreds.ExpiresAt)
}

// discoverTeams re-fetches the team list from the API independently of token refresh.
// This ensures new teams are discovered promptly even when the credential token is still
// fresh (far from expiry). Only updates the Repos map in credentials; token/expiry are
// preserved from the existing credentials.
func (s *SyncScheduler) discoverTeams() {
	s.mu.Lock()
	if !s.lastTeamDiscovery.IsZero() && time.Since(s.lastTeamDiscovery) < teamDiscoveryInterval {
		s.mu.Unlock()
		return
	}
	s.lastTeamDiscovery = time.Now()
	s.mu.Unlock()

	projectEndpoint := endpoint.GetForProject(s.config.ProjectRoot)

	// load existing credentials — we need a valid token to call the API
	creds, err := gitserver.LoadCredentialsForEndpoint(projectEndpoint)
	if err != nil {
		s.logger.Debug("failed to load credentials for team discovery", "error", err)
		return
	}
	if creds == nil || creds.Token == "" {
		// no credentials available; refreshCredentialsIfNeeded will handle this
		return
	}

	// use the git PAT from credentials to call the repos API
	token, err := auth.GetTokenForEndpoint(projectEndpoint)
	if err != nil {
		s.logger.Debug("failed to get auth token for team discovery", "error", err)
		return
	}
	if token == nil || token.AccessToken == "" {
		return
	}

	client := api.NewRepoClientWithEndpoint(projectEndpoint).WithAuthToken(token.AccessToken)
	reposResp, err := client.GetRepos()
	if err != nil {
		s.logger.Warn("failed to fetch repos for team discovery", "error", err)
		return
	}
	if reposResp == nil {
		return
	}

	// build new repos map from API response
	newRepos := make(map[string]gitserver.RepoEntry)
	for _, repo := range reposResp.Repos {
		entry := gitserver.RepoEntry{
			Name:   repo.Name,
			Type:   repo.Type,
			URL:    repo.URL,
			TeamID: repo.StableID(),
		}
		newRepos[entry.Name] = entry
	}

	// check if repos changed before writing
	if reposEqual(creds.Repos, newRepos) {
		return
	}

	// update only the repos map; preserve existing token, expiry, server URL
	creds.Repos = newRepos
	if err := gitserver.SaveCredentialsForEndpoint(projectEndpoint, *creds); err != nil {
		s.logger.Warn("failed to save credentials after team discovery", "error", err)
		return
	}

	s.logger.Info("team discovery found updated team list", "repo_count", len(newRepos))
}

// reposEqual checks if two repo maps have identical entries.
func reposEqual(a, b map[string]gitserver.RepoEntry) bool {
	if len(a) != len(b) {
		return false
	}
	for k, va := range a {
		vb, ok := b[k]
		if !ok {
			return false
		}
		if va.Name != vb.Name || va.Type != vb.Type || va.URL != vb.URL || va.TeamID != vb.TeamID {
			return false
		}
	}
	return true
}

// fetchLedgerURLFromAPI fetches the ledger URL from the cloud API and caches it.
// Called when the ledger needs to be cloned but no clone URL is available from credentials.
// Prefers GetRepoDetail (returns ledger + team contexts in one call), falling back to
// GetLedgerStatus if the server hasn't implemented the new endpoint yet (404 -> nil).
//
// This function handles many failure modes (no config, no auth, network errors, ledger not ready)
// by logging and returning early. This is intentional - the daemon should continue operating
// even if ledger URL fetch fails (e.g., when offline).
func (s *SyncScheduler) fetchLedgerURLFromAPI() {
	// check if we already have a ledger URL
	if ledger := s.workspaceRegistry.GetLedger(); ledger != nil && ledger.CloneURL != "" {
		return
	}

	// backoff on repeated API failures (separate key from git sync —
	// a cloud API outage should not block git fetch/pull against a healthy repo)
	if !s.workspaceRegistry.ShouldSync("ledger-api") {
		return
	}

	// get repo ID from workspace registry (loaded from project config)
	repoID := s.workspaceRegistry.GetRepoID()
	if repoID == "" {
		s.logger.Debug("no repo_id in project config, cannot fetch ledger URL")
		return
	}

	// get the endpoint for this project
	projectEndpoint := s.workspaceRegistry.GetEndpoint()
	if projectEndpoint == "" {
		projectEndpoint = endpoint.GetForProject(s.config.ProjectRoot)
	}

	// get auth token for this endpoint
	token, err := auth.GetTokenForEndpoint(projectEndpoint)
	if err != nil {
		s.logger.Debug("failed to get auth token for ledger status", "error", err)
		return
	}
	if token == nil || token.AccessToken == "" {
		s.logger.Debug("no auth token available for ledger status")
		return
	}

	// prefer GetRepoDetail (returns ledger + team contexts in one call)
	// fall back to GetLedgerStatus if server hasn't implemented new endpoint (404 -> nil)
	client := api.NewRepoClientWithEndpoint(projectEndpoint).WithAuthToken(token.AccessToken)

	detail, detailErr := client.GetRepoDetail(repoID)
	if detailErr != nil {
		s.logger.Warn("failed to fetch repo detail", "repo_id", repoID, "error", detailErr)
		s.workspaceRegistry.RecordSyncFailure("ledger-api")
	}

	// if GetRepoDetail succeeded, use its data
	if detail != nil {
		// register team contexts from API response (includes public TCs for non-members)
		s.registerTeamContextsFromDetail(detail)

		// use ledger data from detail
		if detail.Ledger != nil && detail.Ledger.Status == "ready" && detail.Ledger.RepoURL != "" {
			s.logger.Info("fetched ledger URL from repo detail", "repo_id", repoID)
			if !s.workspaceRegistry.SetLedgerCloneURL(detail.Ledger.RepoURL) {
				s.workspaceRegistry.InitializeLedger(detail.Ledger.RepoURL, s.config.ProjectRoot)
				s.logger.Info("initialized ledger workspace from repo detail", "clone_url", detail.Ledger.RepoURL)
			}
			s.workspaceRegistry.ClearSyncFailures("ledger-api")
			s.persistLedgerPath()
			return
		} else if detail.Ledger != nil {
			// "not ready" is a transient provisioning state, not a failure —
			// don't apply backoff, just skip this tick and retry next cycle
			s.logger.Debug("ledger not ready from repo detail", "status", detail.Ledger.Status, "message", detail.Ledger.Message)
			return
		}
		return
	}

	// fallback: GetRepoDetail returned nil (404 -- server not updated yet)
	status, err := client.GetLedgerStatus(repoID)
	if err != nil {
		// network errors are expected when offline - use Warn not Error
		s.logger.Warn("failed to fetch ledger status", "repo_id", repoID, "error", err)
		s.workspaceRegistry.RecordSyncFailure("ledger-api")
		return
	}

	// defensive: GetLedgerStatus should never return (nil, nil)
	if status == nil {
		s.logger.Debug("unexpected nil ledger status from API")
		return
	}

	// check if ledger is ready
	if status.Status != "ready" {
		// "not ready" is a transient provisioning state, not a failure —
		// don't apply backoff, just skip this tick and retry next cycle
		s.logger.Debug("ledger not ready", "status", status.Status, "message", status.Message)
		return
	}

	// update workspace registry with the ledger URL
	if status.RepoURL != "" {
		s.logger.Info("fetched ledger URL from API", "repo_id", repoID)
		if !s.workspaceRegistry.SetLedgerCloneURL(status.RepoURL) {
			s.workspaceRegistry.InitializeLedger(status.RepoURL, s.config.ProjectRoot)
			s.logger.Info("initialized ledger workspace from API", "clone_url", status.RepoURL)
		}
		s.workspaceRegistry.ClearSyncFailures("ledger-api")
		s.persistLedgerPath()
	}
}

// persistLedgerPath saves the ledger path to config.local.toml for persistence across daemon restarts.
// Uses the workspace registry's config cache to avoid stale-cache overwrites from UpdateConfigLastSync.
func (s *SyncScheduler) persistLedgerPath() {
	ledger := s.workspaceRegistry.GetLedger()
	if ledger == nil || ledger.Path == "" {
		return
	}
	if err := s.workspaceRegistry.PersistLedgerPath(ledger.Path); err != nil {
		s.logger.Warn("failed to persist ledger to config.local.toml", "error", err)
	}
	// trigger clone if ledger doesn't exist on disk (self-healing)
	if !ledger.Exists && ledger.CloneURL != "" {
		if s.workspaceRegistry.ShouldRetryClone(ledger.ID) {
			s.logger.Info("triggering ledger clone after API fetch", "path", ledger.Path)
			go s.cloneInBackground(ledger.CloneURL, ledger.Path, "ledger", ledger.ID)
		}
	}
}

// registerTeamContextsFromDetail registers team contexts from a GetRepoDetail response.
// This enables the daemon to discover and sync public team contexts that non-members have
// viewer access to, even if those team contexts aren't in the user's credentials.
func (s *SyncScheduler) registerTeamContextsFromDetail(detail *api.RepoDetailResponse) {
	if detail == nil {
		return
	}

	// register new team contexts
	if len(detail.TeamContexts) > 0 {
		s.workspaceRegistry.RegisterTeamContextsFromAPI(detail.TeamContexts)
	}

	// cleanup team contexts no longer in the response
	currentTeamIDs := make(map[string]bool)
	for _, tc := range detail.TeamContexts {
		currentTeamIDs[tc.TeamID] = true
		currentTeamIDs[tc.Name] = true
	}
	s.workspaceRegistry.CleanupRevokedTeamContexts(currentTeamIDs)
}

// syncAll performs a full sync (pull-only — CLI handles push via LFS pipeline).
func (s *SyncScheduler) syncAll(ctx context.Context) {
	s.pullChanges(ctx)
}

// Sync performs an immediate full sync. Used for manual requests via IPC.
func (s *SyncScheduler) Sync() error {
	return s.SyncWithProgress(nil)
}

// SyncWithProgress performs a full sync with progress updates.
// If progress is nil, no progress updates are sent.
// Returns an error if the ledger sync fails (surfaced to CLI via IPC).
func (s *SyncScheduler) SyncWithProgress(progress *ProgressWriter) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	return s.doSyncAll(ctx, progress)
}

// doSyncAll performs pull with optional progress updates.
// Returns an error if the pull fails.
func (s *SyncScheduler) doSyncAll(ctx context.Context, progress *ProgressWriter) error {
	// refresh credentials if expired or near expiry
	s.refreshCredentialsIfNeeded()

	return s.doPull(ctx, progress, true)
}

// isValidRepoPath validates that a repo path is safe to use.
// Rejects paths with traversal attempts or outside expected directories.
// Resolves symlinks to prevent symlink-based path traversal attacks.
// Returns true if the path is safe, false otherwise.
func isValidRepoPath(path string) bool {
	// reject empty paths
	if path == "" {
		return false
	}

	// reject paths containing traversal sequences before any resolution
	if strings.Contains(path, "..") {
		return false
	}

	// clean the path to resolve any . components
	cleaned := filepath.Clean(path)

	// must be absolute path
	if !filepath.IsAbs(cleaned) {
		return false
	}

	// resolve symlinks in the path to get the real path
	// this prevents symlink-based path traversal attacks
	// (e.g., /allowed/dir/symlink -> /etc/passwd)
	//
	// we use filepath.EvalSymlinks on the parent directory if the path doesn't exist yet,
	// since the target may not exist during clone operations
	realPath := cleaned
	if info, err := os.Lstat(cleaned); err == nil {
		// path exists, resolve it fully
		if resolved, err := filepath.EvalSymlinks(cleaned); err == nil {
			realPath = resolved
		}
		// if path is a symlink and we couldn't resolve it, reject
		if info.Mode()&os.ModeSymlink != 0 && realPath == cleaned {
			return false
		}
	} else if os.IsNotExist(err) {
		// path doesn't exist yet (clone target) - resolve the parent directory
		parentDir := filepath.Dir(cleaned)
		if resolved, err := filepath.EvalSymlinks(parentDir); err == nil {
			realPath = filepath.Join(resolved, filepath.Base(cleaned))
		}
	}

	// get expected base directories
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return false
	}

	// resolve home directory symlinks for consistent comparison
	if resolvedHome, err := filepath.EvalSymlinks(homeDir); err == nil {
		homeDir = resolvedHome
	}

	tmpDir := os.TempDir()
	// resolve and clean tmpDir to normalize (e.g., /var/folders/... on macOS)
	if resolvedTmp, err := filepath.EvalSymlinks(tmpDir); err == nil {
		tmpDir = resolvedTmp
	}
	cleanedTmpDir := filepath.Clean(tmpDir)

	// allow paths under home directory or temp directory (for tests)
	if strings.HasPrefix(realPath, homeDir+string(filepath.Separator)) || realPath == homeDir {
		return true
	}
	if strings.HasPrefix(realPath, cleanedTmpDir+string(filepath.Separator)) || realPath == cleanedTmpDir {
		return true
	}

	// on macOS, /var is symlinked to /private/var, so check both variants
	// this handles cases where resolution might give us either form
	if strings.HasPrefix(realPath, "/private"+cleanedTmpDir+string(filepath.Separator)) {
		return true
	}
	if after, found := strings.CutPrefix(cleanedTmpDir, "/private"); found {
		if strings.HasPrefix(realPath, after+string(filepath.Separator)) {
			return true
		}
	}

	// allow /tmp and /private/tmp (system-wide temp, distinct from os.TempDir() per-user temp)
	// useful for testing and development workflows
	if strings.HasPrefix(realPath, "/tmp"+string(filepath.Separator)) {
		return true
	}
	if strings.HasPrefix(realPath, "/private/tmp"+string(filepath.Separator)) {
		return true
	}

	return false
}

// Checkout clones a repository if it doesn't exist.
// Sends progress updates via ProgressWriter during long operations.
// Uses cloneSem to bound concurrent clone operations (blocks until a slot is available).
// After successful clone of ledger/team-context repos, creates AGENTS.md.
// Checkout clones a repository to the specified path.
//
// ┌─────────────────────────────────────────────────────────────────────────────┐
// │ DAEMON IPC HANDLER: checkout                                                │
// │ Classification: 🔶 CRITICAL PATH WITH FALLBACK                              │
// │ (see docs/ai/specs/ipc-architecture.md)                                     │
// │                                                                             │
// │ Clone is CRITICAL for product functionality - without it, SageOx cannot     │
// │ be initialized at all. However, IPC to this handler is NOT strictly         │
// │ required because the CLI has a FALLBACK:                                    │
// │                                                                             │
// │   cmd/ox/doctor_git_repos.go:cloneViaDaemon()                              │
// │   → Falls back to gitserver.CloneFromURLWithEndpoint() when daemon unavailable │
// │                                                                             │
// │ This handler is PREFERRED over direct clone because it provides:            │
// │ - Centralized credential handling                                           │
// │ - Progress streaming to CLI                                                 │
// │ - Consistent locking for concurrent operations                              │
// │ - AGENTS.md creation after clone                                            │
// │ - Workspace registry cache invalidation                                     │
// └─────────────────────────────────────────────────────────────────────────────┘
func (s *SyncScheduler) Checkout(payload CheckoutPayload, progress *ProgressWriter) (*CheckoutResult, error) {
	// validate path before any operations to prevent path traversal attacks
	if !isValidRepoPath(payload.RepoPath) {
		return nil, ErrInvalidRepoPath
	}

	result := &CheckoutResult{Path: payload.RepoPath}

	// ensure parent directory exists first (needed for both clone and backup rename)
	parentDir := filepath.Dir(payload.RepoPath)
	if err := os.MkdirAll(parentDir, 0755); err != nil {
		return nil, fmt.Errorf("create parent directory: %w", err)
	}

	// check if already exists
	info, statErr := os.Stat(payload.RepoPath)
	if statErr == nil && info.IsDir() {
		// directory exists - check if it's a git repo
		gitDir := filepath.Join(payload.RepoPath, ".git")
		if _, err := os.Stat(gitDir); err == nil {
			s.logger.Debug("checkout: repo already exists", "path", payload.RepoPath)
			result.AlreadyExists = true
			return result, nil
		}
		// directory exists but not a git repo - self-healing: move aside and clone fresh
		// this handles corrupt/incomplete clones that need recovery
		backupPath := fmt.Sprintf("%s.bak.%d", payload.RepoPath, time.Now().Unix())
		s.logger.Warn("checkout: directory exists but not a git repo, moving aside for self-healing",
			"path", payload.RepoPath, "backup", backupPath)
		if err := os.Rename(payload.RepoPath, backupPath); err != nil {
			// if rename fails, log and continue - git clone will fail if there's a real problem
			s.logger.Error("checkout: failed to move directory aside, will attempt clone anyway",
				"path", payload.RepoPath, "error", err)
		}
		// continue with clone below
	}

	// acquire clone slot — blocks until a slot is available (up to maxConcurrentClones)
	// replaces the old single-boolean lock that rejected concurrent clones with an error,
	// which triggered unnecessary exponential backoff on internal contention
	if s.onBeforeCloneSem != nil {
		s.onBeforeCloneSem()
	}
	s.cloneSem <- struct{}{}
	defer func() { <-s.cloneSem }()

	// validate clone URL to prevent SSRF attacks
	// must be done before any network operations
	if err := isValidCloneURL(payload.CloneURL); err != nil {
		s.logger.Warn("checkout: rejected unsafe clone URL", "url", payload.CloneURL, "error", err)
		return nil, fmt.Errorf("invalid clone URL: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// send progress: connecting
	if progress != nil {
		_ = progress.WriteStage("connecting", "Connecting to remote...")
	}
	s.logger.Info("checkout: starting clone", "url", payload.CloneURL, "path", payload.RepoPath, "type", payload.RepoType)

	// send progress: cloning
	if progress != nil {
		_ = progress.WriteStage("cloning", "Cloning repository...")
	}

	// inject credentials into clone URL if available
	// this allows git clone to authenticate without requiring credential helper setup
	// use oauth2:TOKEN format for GitLab compatibility (same as checkout.go)
	// use endpoint-aware credential loading for multi-endpoint support
	cloneURL := payload.CloneURL
	endpoint := s.workspaceRegistry.GetEndpoint()
	s.logger.Info("checkout: loading credentials", "endpoint", endpoint, "clone_url", payload.CloneURL)
	if creds, err := gitserver.LoadCredentialsForEndpoint(endpoint); err == nil && creds != nil && creds.Token != "" {
		s.logger.Info("checkout: injecting git credentials", "token_len", len(creds.Token), "endpoint", endpoint)
		cloneURL = injectGitCredentials(payload.CloneURL, "oauth2", creds.Token)
	} else if err != nil {
		s.logger.Error("checkout: failed to load git credentials", "error", err, "endpoint", endpoint)
	} else if creds == nil {
		s.logger.Warn("checkout: no git credentials found", "endpoint", endpoint)
	} else {
		s.logger.Warn("checkout: git credentials have empty token", "endpoint", endpoint)
	}

	// git clone with progress
	// note: git clone --progress sends progress to stderr, could parse it for more detailed updates
	cloneCmd := exec.CommandContext(ctx, "git", "clone", "--quiet", cloneURL, payload.RepoPath)
	if output, err := cloneCmd.CombinedOutput(); err != nil {
		// sanitize output to prevent credential leaks (clone URL may contain PAT token)
		sanitizedOutput := sanitizeGitOutput(string(output))
		s.logger.Error("checkout: clone failed", "error", err, "output", sanitizedOutput)
		s.recordError(fmt.Sprintf("clone %s failed: %v", payload.RepoType, err))
		// include sanitized output in error for better debugging
		if sanitizedOutput != "" {
			return nil, fmt.Errorf("git clone failed: %s", sanitizedOutput)
		}
		return nil, fmt.Errorf("git clone failed: %w", err)
	}

	// send progress: verifying
	if progress != nil {
		_ = progress.WriteStage("verifying", "Verifying clone...")
	}

	// verify clone succeeded
	gitDir := filepath.Join(payload.RepoPath, ".git")
	if _, err := os.Stat(gitDir); err != nil {
		return nil, fmt.Errorf("clone verification failed: .git directory not found")
	}

	// configure pull strategy to use rebase (avoids merge commits, cleaner history)
	// This prevents git warnings about divergent branches on manual pulls
	configCmd := exec.CommandContext(ctx, "git", "-C", payload.RepoPath, "config", "pull.rebase", "true")
	if output, err := configCmd.CombinedOutput(); err != nil {
		// log but don't fail - this is a nice-to-have config
		s.logger.Warn("checkout: failed to set pull.rebase config", "error", err, "output", string(output))
	}

	result.Cloned = true
	s.logger.Info("checkout: clone complete", "path", payload.RepoPath, "type", payload.RepoType)
	s.recordActivity()

	// invalidate workspace registry cache after cloning new repo
	s.workspaceRegistry.InvalidateConfigCache()

	// create AGENTS.md for newly cloned ledger repos only.
	// team-context repos have AGENTS.md pre-populated by the server.
	if payload.RepoType == "ledger" {
		if progress != nil {
			_ = progress.WriteStage("initializing", "Creating AGENTS.md...")
		}
		agentsOpts := &gitserver.AgentsMDOptions{
			RepoType: payload.RepoType,
		}
		if err := gitserver.CreateAgentsMD(ctx, payload.RepoPath, agentsOpts); err != nil {
			// non-fatal: clone succeeded even if AGENTS.md creation fails
			s.logger.Warn("checkout: failed to create AGENTS.MD", "error", err)
		}
	}

	return result, nil
}

// pullTeamContexts syncs all team context repos from workspace registry (used by scheduler).
// For repos that exist locally: pulls latest changes.
// For repos that don't exist: spawns background clone (non-blocking).
//
// Auto-clone rationale: Team contexts are designed to be shared across repos.
// When the API returns a team context, the user has already consented (by installing
// ox and initializing a repo). Cloning happens in background goroutines to avoid
// blocking the sync scheduler event loop.
// Also performs anti-entropy: checks for missing workspaces and triggers clones.
func (s *SyncScheduler) pullTeamContexts(ctx context.Context) {
	// anti-entropy: ensure missing workspaces get cloned
	s.triggerMissingClones()
	s.doTeamSync(ctx, nil, false)
}

// TeamSync performs an on-demand sync of all team contexts with progress updates.
func (s *SyncScheduler) TeamSync(progress *ProgressWriter) error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	s.doTeamSync(ctx, progress, true)
	return nil
}

// doTeamSync syncs all team context repos with optional progress updates.
// Uses the WorkspaceRegistry to avoid repeated config file reads.
//
// Auto-clone behavior: If a team context doesn't exist locally but has a clone URL,
// spawns a background goroutine to clone it. This doesn't block the sync loop.
// Note: Ledger auto-clone is handled separately in doPull() on the ledger sync ticker.
func (s *SyncScheduler) doTeamSync(ctx context.Context, progress *ProgressWriter, forceSync bool) {
	// refresh credentials if expired or near expiry
	s.refreshCredentialsIfNeeded()

	// discover new teams independently of token refresh — ensures new teams
	// are found even when the credential token is still fresh
	s.discoverTeams()

	if s.config.ProjectRoot == "" {
		if progress != nil {
			_ = progress.WriteStage("skipped", "No project root configured")
		}
		return
	}

	// reload workspace state from config (uses cache if fresh)
	if err := s.workspaceRegistry.LoadFromConfig(); err != nil {
		s.logger.Warn("failed to load workspace registry for team context sync", "error", err)
		if progress != nil {
			_ = progress.WriteMessage(fmt.Sprintf("Failed to load config: %v", err))
		}
		return
	}

	// get team contexts from registry
	teamContexts := s.workspaceRegistry.GetTeamContexts()
	if len(teamContexts) == 0 {
		s.logger.Debug("no team contexts configured")
		if progress != nil {
			_ = progress.WriteStage("skipped", "No team contexts configured")
		}
		return
	}

	s.logger.Debug("syncing team contexts", "count", len(teamContexts))
	if progress != nil {
		_ = progress.WriteStage("starting", fmt.Sprintf("Syncing %d team context(s)...", len(teamContexts)))
	}

	var syncedCount, skippedCount, cloningCount int

	for _, ws := range teamContexts {
		// check if path exists
		if ws.Path == "" {
			s.workspaceRegistry.SetWorkspaceError(ws.ID, "no path configured")
			skippedCount++
			continue
		}

		if !ws.Exists {
			// auto-clone if we have a clone URL
			if ws.CloneURL != "" {
				// check if we should retry (respects exponential backoff)
				if !s.workspaceRegistry.ShouldRetryClone(ws.ID) {
					attempts, nextRetry := s.workspaceRegistry.GetCloneRetryInfo(ws.ID)
					s.logger.Debug("team context clone in backoff, skipping",
						"team", ws.TeamName, "attempts", attempts, "next_retry", nextRetry)
					skippedCount++
					continue
				}

				s.logger.Info("team context not cloned, starting background clone",
					"team", ws.TeamName, "path", ws.Path)
				if progress != nil {
					_ = progress.WriteStage("cloning", fmt.Sprintf("Cloning team %s in background...", ws.TeamName))
				}
				// clone in background goroutine - don't block sync loop
				go s.cloneInBackground(ws.CloneURL, ws.Path, "team-context", ws.ID)
				cloningCount++
			} else {
				s.workspaceRegistry.SetWorkspaceError(ws.ID, "path does not exist and no clone URL available")
				s.logger.Debug("team context path not found and no clone URL", "team", ws.TeamName, "path", ws.Path)
				if progress != nil {
					_ = progress.WriteStage("skipped", fmt.Sprintf("Team %s: not cloned, no URL", ws.TeamName))
				}
				skippedCount++
			}
			continue
		}

		// sync backoff — skip if recent sync failures triggered backoff
		if !s.shouldSyncOrBypass(ws.ID, forceSync) {
			skippedCount++
			continue
		}

		if progress != nil {
			_ = progress.WriteStage("syncing", fmt.Sprintf("Syncing team: %s", ws.TeamName))
		}

		// mark sync in progress
		s.workspaceRegistry.SetSyncInProgress(ws.ID, true)

		// pull changes (read-only, no push)
		startTime := time.Now()
		pullErr := s.pullTeamContext(ctx, ws.Path)

		// mark sync complete
		s.workspaceRegistry.SetSyncInProgress(ws.ID, false)

		if pullErr != nil {
			s.workspaceRegistry.SetWorkspaceError(ws.ID, pullErr.Error())
			s.workspaceRegistry.RecordSyncFailure(ws.ID)
			s.logger.Debug("team context pull failed", "team", ws.TeamName, "error", pullErr)
			s.metrics.RecordTeamSyncError()
			if progress != nil {
				_ = progress.WriteStage("error", fmt.Sprintf("Team %s: %v", ws.TeamName, pullErr))
			}
		} else {
			s.workspaceRegistry.ClearWorkspaceError(ws.ID)
			s.workspaceRegistry.ClearSyncFailures(ws.ID)
			if s.issues != nil {
				s.issues.ClearIssue(IssueTypeSyncBackoff, ws.ID)
			}
			// update last sync in registry and config file
			if err := s.workspaceRegistry.UpdateConfigLastSync(ws.ID); err != nil {
				s.logger.Warn("failed to update config last sync", "team", ws.TeamName, "error", err)
			}
			syncedCount++

			duration := time.Since(startTime)
			s.recordSync("team_context", duration, 0)
			s.metrics.RecordTeamSync()
			s.recordActivity()
			s.logger.Debug("team context synced", "team", ws.TeamName, "duration", duration)
			if progress != nil {
				_ = progress.WriteStage("synced", fmt.Sprintf("Team %s synced", ws.TeamName))
			}
		}
	}

	if progress != nil {
		msg := fmt.Sprintf("Synced %d, skipped %d team context(s)", syncedCount, skippedCount)
		if cloningCount > 0 {
			msg += fmt.Sprintf(", cloning %d in background", cloningCount)
		}
		_ = progress.WriteStage("complete", msg)
	}
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
			go s.cloneInBackground(ledger.CloneURL, ledger.Path, "ledger", ledger.ID)
		}
	}

	// check team contexts
	for _, ws := range s.workspaceRegistry.GetTeamContexts() {
		if !ws.Exists && ws.CloneURL != "" && ws.Path != "" {
			if s.workspaceRegistry.ShouldRetryClone(ws.ID) {
				s.logger.Info("triggering immediate team context clone (self-healing)",
					"team", ws.TeamName, "path", ws.Path)
				go s.cloneInBackground(ws.CloneURL, ws.Path, "team-context", ws.ID)
			}
		}
	}
}

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
	}

	// update exists flags so status renders correctly before next Reload()
	s.workspaceRegistry.RefreshExists()
	// invalidate cache so next sync sees the cloned repo
	s.workspaceRegistry.InvalidateConfigCache()
}

// pullTeamContext performs a git pull on a single team context repo.
// Returns nil if skipped due to recent fetch (by another daemon).
//
// Multi-daemon deduplication: Users often work on multiple repos that share
// the same team context (e.g., 5-6 project repos all pointing to one team
// context directory). Each repo has its own daemon, so without coordination,
// they'd all try to git pull the same team context simultaneously.
//
// We solve this by checking .git/FETCH_HEAD mtime before fetching. Git updates
// this file on every fetch, so if it was recently modified (by any process),
// we skip the pull. This naturally deduplicates without locks - whichever
// daemon fetches first "wins" and others skip for that interval.
//
// Change Detection: After a successful pull, this function compares file states
// before and after to detect changes in key team context files (distilled discussions,
// agent definitions, etc.). When changes are detected, a notification marker is written
// so that CLI commands can "whisper" updates to agents.
func (s *SyncScheduler) pullTeamContext(ctx context.Context, path string) error {
	// skip if repo stuck in broken rebase state
	rebaseMerge := filepath.Join(path, ".git", "rebase-merge")
	rebaseApply := filepath.Join(path, ".git", "rebase-apply")
	if _, err := os.Stat(rebaseMerge); err == nil {
		s.logger.Debug("repo in rebase state, skipping pull", "path", path)
		return nil
	}
	if _, err := os.Stat(rebaseApply); err == nil {
		s.logger.Debug("repo in rebase-apply state, skipping pull", "path", path)
		return nil
	}

	// check for stale lock files from crashed git processes
	gitDir := filepath.Join(path, ".git")
	if locks := hasLockFiles(gitDir); len(locks) > 0 {
		repoName := filepath.Base(path)
		s.logger.Warn("git lock files detected, skipping pull",
			"path", path,
			"locks", strings.Join(locks, ", "))
		if s.issues != nil {
			s.issues.SetIssue(DaemonIssue{
				Type:     IssueTypeGitLock,
				Severity: SeverityWarning,
				Repo:     repoName,
				Summary: fmt.Sprintf("Stale lock files blocking sync: %s. If no git commands are running, remove with: rm %s/{%s}",
					strings.Join(locks, ", "),
					gitDir,
					strings.Join(locks, ",")),
			})
		}
		return nil
	}
	// clear lock issue if previously set but now resolved
	if s.issues != nil {
		repoName := filepath.Base(path)
		s.issues.ClearIssue(IssueTypeGitLock, repoName)
	}

	// ls-remote SHA check — skip if remote HEAD matches local (nothing new to pull).
	// Cheaper than git fetch: only hits /info/refs, no upload-pack negotiation.
	if s.remoteRefCheck(ctx, path) {
		return nil
	}

	// FETCH_HEAD mtime dedup (secondary: multi-daemon dedup on shared team context paths).
	// Kept as fallback for when ls-remote can't run (credential issues, etc).
	fetchHead := filepath.Join(path, ".git", "FETCH_HEAD")
	if info, err := os.Stat(fetchHead); err == nil {
		threshold := max(s.config.TeamContextSyncInterval/2, minTeamContextFetchAge)
		if time.Since(info.ModTime()) < threshold {
			s.logger.Debug("team context recently fetched, skipping", "path", path, "age", time.Since(info.ModTime()))
			return nil
		}
	}

	// refresh remote URL if credentials changed (e.g., user switch via ox login)
	teamEndpoint := endpoint.GetForProject(s.config.ProjectRoot)
	if err := gitserver.RefreshRemoteCredentials(path, teamEndpoint); err != nil {
		s.logger.Warn("team context remote credential refresh failed", "path", path, "error", err)
	}

	// git fetch
	// git fetch (capture stderr for diagnosable error messages)
	fetchCmd := exec.CommandContext(ctx, "git", "-C", path, "fetch", "--quiet")
	if output, err := fetchCmd.CombinedOutput(); err != nil {
		detail := sanitizeGitOutput(strings.TrimSpace(string(output)))
		if detail != "" {
			return fmt.Errorf("fetch failed: %s (%w)", detail, err)
		}
		return fmt.Errorf("fetch failed: %w", err)
	}

	// track FETCH_HEAD mtime for team context repos
	if info, err := os.Stat(fetchHead); err == nil {
		s.recordRemoteChange(path, info.ModTime())
	}

	// git pull --rebase (capture stderr for diagnosable error messages)
	pullCmd := exec.CommandContext(ctx, "git", "-C", path, "pull", "--rebase", "--quiet")
	if output, err := pullCmd.CombinedOutput(); err != nil {
		detail := sanitizeGitOutput(strings.TrimSpace(string(output)))

		// check if it's a merge conflict
		statusCmd := exec.CommandContext(ctx, "git", "-C", path, "status", "--porcelain")
		if statusOutput, _ := statusCmd.Output(); strings.Contains(string(statusOutput), "UU") {
			s.metrics.RecordConflict()

			// report merge conflict issue — daemon does not write; next pull will skip via rebase-state check
			if s.issues != nil {
				repoName := filepath.Base(path)
				s.issues.SetIssue(DaemonIssue{
					Type:            IssueTypeMergeConflict,
					Severity:        SeverityError,
					Repo:            repoName,
					Summary:         fmt.Sprintf("Team context %s has merge conflicts. Run 'ox doctor --fix' to re-clone.", repoName),
					RequiresConfirm: true, // merge resolution needs human approval
				})
			}
		}
		if detail != "" {
			return fmt.Errorf("pull failed: %s (%w)", detail, err)
		}
		return fmt.Errorf("pull failed: %w", err)
	}

	// sync succeeded - clear any previous merge conflict issue for this repo
	if s.issues != nil {
		repoName := filepath.Base(path)
		s.issues.ClearIssue(IssueTypeMergeConflict, repoName)
	}

	return nil
}

// remoteRefCheck compares the remote tracking branch SHA to the local HEAD SHA via ls-remote.
// Returns true if they match (nothing new to pull), false if different or on error.
// On error, returns false to fall through to the existing fetch+pull path.
//
// Uses the local tracking branch (e.g. refs/heads/main) rather than remote HEAD,
// because remote HEAD is a symbolic ref that may point to a different default branch
// than the local checkout tracks. There is an inherent race between ls-remote and
// the subsequent fetch (the remote can advance between the two calls), but this is
// safe — we just pull slightly stale data and catch up on the next cycle.
//
// This is cheaper than git fetch because ls-remote only hits /info/refs (1 HTTP
// round-trip) without git-upload-pack negotiation or packfile transfer.
func (s *SyncScheduler) remoteRefCheck(ctx context.Context, repoPath string) bool {
	lsCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	// resolve the upstream tracking branch (e.g. "refs/remotes/origin/main" → "refs/heads/main")
	upstreamCmd := exec.CommandContext(ctx, "git", "-C", repoPath, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}")
	upstreamOut, err := upstreamCmd.Output()
	if err != nil {
		// no tracking branch configured — fall through to fetch
		return false
	}
	upstream := strings.TrimSpace(string(upstreamOut))
	// convert "origin/main" to "refs/heads/main" for ls-remote
	remoteRef := upstream
	if strings.HasPrefix(upstream, "origin/") {
		remoteRef = "refs/heads/" + strings.TrimPrefix(upstream, "origin/")
	}

	// git ls-remote origin <ref> — single HTTP round-trip, no local locks
	lsCmd := exec.CommandContext(lsCtx, "git", "-C", repoPath, "ls-remote", "origin", remoteRef)
	lsOut, err := lsCmd.Output()
	if err != nil {
		s.logger.Debug("ls-remote failed, falling through to fetch", "path", repoPath, "error", err)
		return false
	}
	fields := strings.Fields(string(lsOut))
	if len(fields) == 0 {
		return false
	}
	remoteSHA := fields[0]

	// git rev-parse HEAD — local-only, instant
	localCmd := exec.CommandContext(ctx, "git", "-C", repoPath, "rev-parse", "HEAD")
	localOut, err := localCmd.Output()
	if err != nil {
		return false
	}
	localSHA := strings.TrimSpace(string(localOut))

	match := remoteSHA == localSHA
	if match {
		s.logger.Debug("remote ref unchanged", "path", repoPath, "ref", remoteRef, "sha", localSHA[:min(8, len(localSHA))])
	}
	return match
}

// TeamContextStatus returns the current team context sync status.
// Uses the WorkspaceRegistry for a unified view of workspace state.
func (s *SyncScheduler) TeamContextStatus() []TeamContextSyncStatus {
	return s.workspaceRegistry.GetTeamContextStatus()
}

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

	// CRITICAL: Use BOTH repo_id AND workspace_id for heartbeat filenames.
	// - workspace_id (hash of path) prevents collisions between worktrees
	// - repo_id makes debugging easier - you can see which repo it belongs to
	// See UserHeartbeatPath() docs for full explanation.
	repoID := s.workspaceRegistry.GetRepoID()
	if repoID == "" {
		s.logger.Debug("no repo_id available for heartbeat")
		return
	}

	workspaceID := WorkspaceID(s.config.ProjectRoot)
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

// trustedGitHosts is the allowlist of hosts permitted for git clone operations.
// This prevents SSRF attacks by blocking file://, local network, and untrusted hosts.
// Includes base domains (sageox.ai, sageox.io) to allow staging subdomains like git.test.sageox.ai.
var trustedGitHosts = []string{
	"sageox.io",
	"sageox.ai",
	"github.com",
	"gitlab.com",
}

// isValidCloneURL validates that a clone URL is safe to use.
// Prevents SSRF by only allowing https:// URLs from trusted git hosts.
//
// Security considerations:
//   - Blocks file:// URLs (local file access)
//   - Blocks git:// URLs (unauthenticated, can be used for SSRF)
//   - Blocks ssh:// URLs (not needed for daemon operations)
//   - Blocks http:// URLs for remote hosts (insecure, credentials would leak)
//   - Only allows specific trusted hosts to prevent connections to arbitrary servers
//
// Exception: http:// is allowed for local development (localhost, 127.0.0.1, *.local)
func isValidCloneURL(cloneURL string) error {
	if cloneURL == "" {
		return fmt.Errorf("clone URL is empty")
	}

	parsed, err := url.Parse(cloneURL)
	if err != nil {
		return fmt.Errorf("invalid URL %q: %w", cloneURL, err)
	}

	// extract host without port
	host := strings.ToLower(parsed.Hostname())
	if host == "" {
		return fmt.Errorf("URL has no host: %s", cloneURL)
	}

	// check if this is a local development URL (http:// allowed for localhost only)
	isLocalHost := host == "localhost" || host == "127.0.0.1"

	// allow http:// only for local development hosts
	if parsed.Scheme == "http" {
		if isLocalHost {
			return nil // allow http for local development
		}
		return fmt.Errorf("only https:// URLs are supported for remote hosts, got: %s", cloneURL)
	}

	// require https for remote hosts
	if parsed.Scheme != "https" {
		return fmt.Errorf("only https:// URLs are supported, got %s:// in: %s", parsed.Scheme, cloneURL)
	}

	// check against trusted hosts (exact match or subdomain)
	for _, trusted := range trustedGitHosts {
		if host == trusted || strings.HasSuffix(host, "."+trusted) {
			return nil
		}
	}

	return fmt.Errorf("untrusted git host: %s (allowed: %v)", parsed.Host, trustedGitHosts)
}

// injectGitCredentials embeds username:password into a git URL for authentication.
// For GitLab, use "oauth2" as the username with the PAT as password.
// Example: https://git.example.com/repo.git -> https://oauth2:TOKEN@git.example.com/repo.git
// Returns the original URL unchanged if it's not a supported URL scheme.
// Supports https:// URLs and http://localhost URLs (for local development).
func injectGitCredentials(gitURL, username, password string) string {
	if username == "" || password == "" {
		return gitURL
	}

	// support https:// URLs
	if strings.HasPrefix(gitURL, "https://") {
		rest := strings.TrimPrefix(gitURL, "https://")
		return fmt.Sprintf("https://%s:%s@%s", username, password, rest)
	}

	// support http://localhost URLs for local development
	// this is safe because traffic never leaves the machine
	if strings.HasPrefix(gitURL, "http://localhost") || strings.HasPrefix(gitURL, "http://127.0.0.1") {
		rest := strings.TrimPrefix(gitURL, "http://")
		return fmt.Sprintf("http://%s:%s@%s", username, password, rest)
	}

	// don't inject credentials into other http:// URLs (security risk)
	return gitURL
}
