// Package daemon implements the background sync daemon for ledger and team contexts.
//
// The daemon performs git pull (read) operations for ledger and team context sync.
// The CLI handles add/commit/push (write) operations via the session upload pipeline.
// Exception: GitHubSyncManager also performs add/commit/push for data/github/ files,
// since these are idempotent and last-write-wins safe (accept-theirs conflict resolution).
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
	"strings"
	"sync"
	"time"

	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/gitserver"
	"github.com/sageox/ox/internal/gitutil"
)

// Sync timing constants - extracted for clarity and testability.
const (
	// minTeamContextFetchAge is the minimum age before re-fetching a team context.
	// Team contexts are shared across repos, so we use a shorter interval for fast sync.
	minTeamContextFetchAge = 15 * time.Second

	// teamDiscoveryInterval is how often we re-fetch the team list from the API,
	// independent of credential token expiry. This ensures new teams are discovered
	// promptly even when the token is still fresh.
	teamDiscoveryInterval = 5 * time.Minute

	// maxConcurrentClones limits background clone operations to prevent resource exhaustion.
	// 100 team contexts shouldn't spawn 100 concurrent git clones.
	maxConcurrentClones = 3

	// cloneSemTimeout is the maximum time to wait for a clone semaphore slot.
	// Prevents indefinite blocking when all clone slots are hung.
	cloneSemTimeout = 2 * time.Minute
)

// gitHTTPTimeoutFlags returns flags for daemon git commands. See gitutil.GitHTTPTimeoutFlags.
var gitHTTPTimeoutFlags = gitutil.GitHTTPTimeoutFlags

// ErrInvalidRepoPath indicates the repo path failed security validation.
var ErrInvalidRepoPath = errors.New("invalid repo path: path traversal or unsafe location detected")

// ErrCloneSemaphoreTimeout indicates all clone slots were busy and the wait timed out.
// This is a transient error that should be retried on the next sync cycle without
// exponential backoff — the slots will free up when in-progress clones finish.
var ErrCloneSemaphoreTimeout = errors.New("clone semaphore timeout")

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
	cloneSem      chan struct{}   // semaphore limiting concurrent clones
	cloneInFlight sync.Map       // tracks workspace IDs with clone in progress (dedup)
	cloneWg       sync.WaitGroup // tracks in-flight background clone goroutines

	// lifecycle context — canceled when scheduler stops
	ctx context.Context

	// GC state — only one GC runs at a time across all workspaces
	gcInProgress int32

	// per-workspace locks for sync state file updates (load-mutate-save)
	syncStateLocks sync.Map // map[string]*sync.Mutex

	// test hooks (nil in production)
	onBeforeCloneSem        func()        // called just before acquiring cloneSem; tests use this to observe blocking
	cloneSemTimeoutOverride time.Duration // override cloneSemTimeout for tests (0 = use default)

	// callbacks
	onActivity   func()                                                           // called on any sync activity
	onTelemetry  func(syncType, operation, status string, duration time.Duration) // called on sync complete for telemetry
	getAuthToken func() string                                                    // returns cached auth token from heartbeat

	// issues tracker for health check system
	issues *IssueTracker

	// version cache for GitHub release checks
	versionCache *VersionCache

	// code index manager for periodic freshness checks
	codedb *CodeDBManager

	// github sync manager for automatic PR/issue sync
	githubSync *GitHubSyncManager

	// shared mutex for all ledger git operations (pull, push, etc.)
	ledgerMu sync.Mutex

	// agent work signal channel — notified after successful ledger pull
	agentWorkSignal chan<- struct{}


	// whisper registry for trigger whispers on sync events
	whisperRegistry *WhisperRegistry

	// murmur relay for converting murmur files to whisper entries
	murmurRelay *MurmurRelay
}

// syncError tracks a sync error with timestamp.
type syncError struct {
	Time    time.Time
	Message string
}

// SyncEvent tracks a successful sync with metadata.
type SyncEvent struct {
	Time         time.Time     `json:"time"`
	Type         string        `json:"type"`                   // "pull", "push", "full", "team_context"
	WorkspaceID  string        `json:"workspace_id,omitempty"` // workspace that was synced (e.g., "ledger", team_id)
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

// SetCodeDBManager sets the CodeDB manager for periodic freshness checks.
func (s *SyncScheduler) SetCodeDBManager(m *CodeDBManager) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.codedb = m
}

// SetGitHubSyncManager sets the GitHub sync manager for periodic PR/issue sync.
func (s *SyncScheduler) SetGitHubSyncManager(m *GitHubSyncManager) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.githubSync = m
}

// LedgerMu returns the shared ledger mutex for git operations.
func (s *SyncScheduler) LedgerMu() *sync.Mutex {
	return &s.ledgerMu
}

// SetAgentWorkSignal sets the channel used to notify the agent work manager
// after a successful ledger pull.
func (s *SyncScheduler) SetAgentWorkSignal(ch chan<- struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.agentWorkSignal = ch
}

// SetWhisperRegistry sets the whisper registry for trigger whispers on sync events.
func (s *SyncScheduler) SetWhisperRegistry(r *WhisperRegistry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.whisperRegistry = r
}

// SetMurmurRelay sets the murmur relay for converting murmur files to whisper entries.
func (s *SyncScheduler) SetMurmurRelay(r *MurmurRelay) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.murmurRelay = r
}

// captureHEAD returns the current HEAD SHA for a git repo.
// Used before a pull to establish a baseline for change detection.
func (s *SyncScheduler) captureHEAD(repoPath string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", repoPath, "rev-parse", "HEAD")
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

// detectChangedFiles runs git diff to find files changed between baseSHA and HEAD.
// Returns nil if baseSHA is empty or on error — graceful degradation.
func (s *SyncScheduler) detectChangedFiles(repoPath, baseSHA string) []string {
	if baseSHA == "" {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", "-C", repoPath,
		"diff", "--name-only", baseSHA, "HEAD")
	output, err := cmd.Output()
	if err != nil {
		return nil
	}

	lines := strings.TrimSpace(string(output))
	if lines == "" {
		return nil
	}

	var files []string
	for _, line := range strings.Split(lines, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			files = append(files, line)
		}
	}
	return files
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
func (s *SyncScheduler) recordSync(syncType string, workspaceID string, duration time.Duration, filesChanged int) {
	s.mu.Lock()

	s.syncHistory = append(s.syncHistory, SyncEvent{
		Time:         time.Now(),
		Type:         syncType,
		WorkspaceID:  workspaceID,
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
	s.ctx = ctx

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

	// GC reclone ticker — checks hourly if any workspace needs a fresh reclone
	var gcTicker *time.Ticker
	var gcChan <-chan time.Time
	if s.config.GCCheckInterval > 0 && s.config.ProjectRoot != "" {
		gcTicker = time.NewTicker(s.config.GCCheckInterval)
		gcChan = gcTicker.C
		defer gcTicker.Stop()
	}

	// memory distillation ticker — spawns `ox distill` as subprocess
	var distillTicker *time.Ticker
	var distillChan <-chan time.Time
	if s.config.DistillInterval > 0 && s.config.ProjectRoot != "" {
		distillTicker = time.NewTicker(s.config.DistillInterval)
		distillChan = distillTicker.C
		defer distillTicker.Stop()
	}

	// github sync ticker — fetches PRs/issues from GitHub API
	var githubSyncTicker *time.Ticker
	var githubSyncChan <-chan time.Time
	if s.config.GitHubSyncInterval > 0 && s.config.ProjectRoot != "" && s.githubSync != nil {
		githubSyncTicker = time.NewTicker(s.config.GitHubSyncInterval)
		githubSyncChan = githubSyncTicker.C
		defer githubSyncTicker.Stop()

		// initial sync after short delay (let ledger pull complete first)
		go func() {
			time.Sleep(30 * time.Second)
			if l := s.workspaceRegistry.GetLedger(); l != nil && l.Path != "" && l.Exists {
				s.githubSync.CheckAndSync(ctx, l.Path)
			}
		}()
	}

	// write initial heartbeat
	s.writeHeartbeats()

	// immediate anti-entropy check on startup (same logic as periodic ticker)
	s.triggerMissingClones()

	// immediate initial pull so last_sync gets populated right away
	// (don't wait 5 minutes for the first readTicker)
	go s.pullChanges(ctx)

	for {
		select {
		case <-ctx.Done():
			// wait briefly for in-flight background clones to finish
			cloneDone := make(chan struct{})
			go func() { s.cloneWg.Wait(); close(cloneDone) }()
			select {
			case <-cloneDone:
			case <-time.After(3 * time.Second):
				s.logger.Warn("timed out waiting for background clones")
			}
			s.logger.Info("sync scheduler stopped")
			return

		case <-readTicker.C:
			s.pullChanges(ctx)
			readTicker.Reset(jitteredDuration(s.config.SyncIntervalRead, 0.10))

		case <-teamContextChan:
			s.pullTeamContexts(ctx)
			if teamContextTicker != nil {
				teamContextTicker.Reset(jitteredDuration(s.config.TeamContextSyncInterval, 0.10))
			}

		case <-heartbeatTicker.C:
			s.writeHeartbeats()

		case <-versionCheckChan:
			s.checkLatestVersion(ctx)

		case <-gcChan:
			s.checkAndRunGC(ctx)

		case <-distillChan:
			s.triggerDistill(ctx)

		case <-githubSyncChan:
			if s.githubSync != nil {
				if l := s.workspaceRegistry.GetLedger(); l != nil && l.Path != "" && l.Exists {
					s.githubSync.CheckAndSync(ctx, l.Path)
				}
			}

		case <-s.triggerChan:
			// triggered by file watcher, do full sync
			s.syncAll(ctx)
		}
	}
}

// triggerDistill spawns `ox distill` as a subprocess for memory distillation.
// The daemon only triggers the process; all writes happen in the subprocess.
func (s *SyncScheduler) triggerDistill(ctx context.Context) {
	// guard: only distill if FEATURE_MEMORY is enabled
	if !auth.IsMemoryEnabled() {
		return
	}

	// guard: need claude CLI available
	if _, err := exec.LookPath("claude"); err != nil {
		s.logger.Debug("distill skipped: claude CLI not in PATH")
		return
	}

	s.logger.Info("triggering memory distillation")
	start := time.Now()

	oxPath, err := os.Executable()
	if err != nil {
		oxPath = "ox" // fall back to PATH lookup
	}

	cmd := exec.CommandContext(ctx, oxPath, "distill")
	cmd.Dir = s.config.ProjectRoot
	cmd.Env = append(os.Environ(), "FEATURE_MEMORY=true")

	out, err := cmd.CombinedOutput()
	duration := time.Since(start)

	if err != nil {
		s.logger.Warn("distill failed", "error", err, "output", strings.TrimSpace(string(out)), "duration", duration)
		return
	}

	s.logger.Info("distill completed", "output", strings.TrimSpace(string(out)), "duration", duration)
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

	// bound background sync to 60s so a DNS/network hang doesn't block
	// the scheduler for minutes (the caller ctx has no deadline)
	pullCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	_ = s.doPull(pullCtx, nil, false)

	// check code index freshness (non-blocking)
	if s.codedb != nil {
		// update ledger path so CodeDB can index GitHub data from the ledger
		if ledger := s.workspaceRegistry.GetLedger(); ledger != nil && ledger.Path != "" && ledger.Exists {
			s.codedb.SetLedgerPath(ledger.Path)
		}
		s.codedb.CheckFreshness(ctx)
	}
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

// pathIsGitRepo checks whether path has a .git directory or file (shallow check).
// For a deeper validity check (catches corrupt repos), use isValidGitRepo.
func pathIsGitRepo(path string) bool {
	if path == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(path, ".git"))
	return err == nil
}

// isValidGitRepo runs git rev-parse --git-dir to verify the repo is functional,
// not just that .git directory exists. Catches partial/corrupt clones from interrupted operations.
func isValidGitRepo(path string) bool {
	cmd := exec.Command("git", "-C", path, "rev-parse", "--git-dir")
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run() == nil
}

// doPull fetches and pulls from remote with optional progress updates.
// If ledger doesn't exist locally but has a clone URL, spawns background clone.
// Returns an error if fetch or pull fails (for on-demand sync error reporting).
// Callers that don't need the error (background scheduler) can ignore it.
// forceSync=true bypasses backoff (user-initiated syncs via IPC).
//
// Architecture note: uses exec.Command("git") rather than go-git because:
//   - process isolation: a hung or crashed git subprocess can be killed without
//     taking down the daemon; an in-process go-git hang blocks the goroutine
//   - --rebase and --autostash: go-git's PullOptions lacks rebase support, which
//     is required for clean linear history on shared ledger repos
//   - lock file safety: if a git process crashes, its .git/index.lock is released
//     by the OS; an in-process crash may leave stale locks in the same process
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
					s.cloneWg.Add(1)
					go s.cloneInBackground(ledger.CloneURL, ledger.Path, "ledger", ledger.ID)
				}
			}
		}
		return nil // can't pull from repo that isn't cloned yet
	}

	// validate the repo is functional (not just .git dir exists)
	// catches partial/corrupt clones from interrupted git clone operations
	if !isValidGitRepo(s.config.LedgerPath) {
		backupPath := fmt.Sprintf("%s.bak.%d", s.config.LedgerPath, time.Now().Unix())
		s.logger.Warn("ledger repo corrupt, moving aside for re-clone",
			"path", s.config.LedgerPath, "backup", backupPath)
		if err := os.Rename(s.config.LedgerPath, backupPath); err != nil {
			s.logger.Error("failed to move corrupt ledger aside", "error", err)
			return fmt.Errorf("corrupt ledger at %s but rename failed: %w", s.config.LedgerPath, err)
		}
		// trigger re-clone on next cycle
		return nil
	}

	// skip if repo stuck in broken rebase state
	if gitutil.IsRebaseInProgress(s.config.LedgerPath) {
		s.logger.Debug("repo in rebase state, skipping pull", "path", s.config.LedgerPath)
		return nil
	}

	// check for stale lock files from crashed git processes
	gitDir := filepath.Join(s.config.LedgerPath, ".git")
	if locks := gitutil.HasLockFiles(gitDir); len(locks) > 0 {
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
		s.recordSyncState(ctx, s.config.LedgerPath)

		if progress != nil {
			_ = progress.WriteStage("skipped", "Remote unchanged, skipping pull")
		}
		return nil
	}

	// FETCH_HEAD mtime dedup (secondary: cross-daemon coordination, crash loop protection).
	// Kept as fallback for when ls-remote can't run (credential issues, etc).
	if age, ok := gitutil.FetchHeadAge(s.config.LedgerPath); ok {
		threshold := max(s.config.SyncIntervalRead/2, gitutil.MinFetchHeadAge)
		if age < threshold {
			s.logger.Debug("ledger recently fetched, skipping", "age", age)
			// persist sync timestamp — another daemon recently fetched, ledger is current
			if err := s.workspaceRegistry.UpdateConfigLastSync("ledger"); err != nil {
				s.logger.Warn("failed to update ledger config last sync", "error", err)
			}
			s.recordSyncState(ctx, s.config.LedgerPath)
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

	// acquire ledger mutex to prevent concurrent git operations with GitHub sync push.
	s.ledgerMu.Lock()
	fetchPullErr := func() error {
		defer s.ledgerMu.Unlock()
		// git fetch (capture stderr for diagnosable error messages)
		fetchArgs := append([]string{"-C", s.config.LedgerPath}, gitHTTPTimeoutFlags()...)
		fetchArgs = append(fetchArgs, "fetch", "--quiet")
		fetchCmd := exec.CommandContext(ctx, "git", fetchArgs...)
		if output, err := fetchCmd.CombinedOutput(); err != nil {
			detail := gitutil.SanitizeOutput(strings.TrimSpace(string(output)))
			s.logger.Warn("fetch failed", "error", err, "output", detail)
			if detail != "" {
				s.recordError(fmt.Sprintf("fetch failed: %s (%v)", detail, err))
			} else {
				s.recordError(fmt.Sprintf("fetch failed: %v", err))
			}
			s.metrics.RecordPullFailure()
			s.workspaceRegistry.RecordSyncFailure("ledger")
			s.recordSyncStateFailure(s.config.LedgerPath)
			if detail != "" {
				return fmt.Errorf("ledger fetch failed: %s (%w)", detail, err)
			}
			return fmt.Errorf("ledger fetch failed: %w", err)
		}

		// track FETCH_HEAD mtime to record when remote had new content
		if info, err := os.Stat(filepath.Join(s.config.LedgerPath, ".git", "FETCH_HEAD")); err == nil {
			s.recordRemoteChange(s.config.LedgerPath, info.ModTime().UTC())
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

		// git pull --rebase --autostash
		// --autostash: local uncommitted changes (from CLI writes, user edits) must not
		// block background sync — stash before rebase, pop after
		pullArgs := append([]string{"-C", s.config.LedgerPath}, gitHTTPTimeoutFlags()...)
		pullArgs = append(pullArgs, "pull", "--rebase", "--autostash", "--quiet")
		pullCmd := exec.CommandContext(ctx, "git", pullArgs...)
		if output, err := pullCmd.CombinedOutput(); err != nil {
			detail := gitutil.SanitizeOutput(strings.TrimSpace(string(output)))
			s.logger.Warn("pull failed", "error", err, "output", detail)
			if detail != "" {
				s.recordError(fmt.Sprintf("pull failed: %s (%v)", detail, err))
			} else {
				s.recordError(fmt.Sprintf("pull failed: %v", err))
			}
			s.metrics.RecordPullFailure()
			s.workspaceRegistry.RecordSyncFailure("ledger")
			s.recordSyncStateFailure(s.config.LedgerPath)

			// check if it's a merge conflict
			statusCmd := exec.CommandContext(ctx, "git", "-C", s.config.LedgerPath, "status", "--porcelain")
			if statusOutput, _ := statusCmd.Output(); strings.Contains(string(statusOutput), "UU") {
				s.metrics.RecordConflict()
				if s.issues != nil {
					s.issues.SetIssue(DaemonIssue{
						Type:            IssueTypeMergeConflict,
						Severity:        SeverityError,
						Repo:            "ledger",
						Summary:         "Ledger has merge conflicts. Run 'ox doctor --fix' to re-clone.",
						RequiresConfirm: true,
					})
				}
			}
			if detail != "" {
				return fmt.Errorf("ledger pull failed: %s (%w)", detail, err)
			}
			return fmt.Errorf("ledger pull failed: %w", err)
		}
		return nil
	}()

	if fetchPullErr != nil {
		return fetchPullErr
	}

	// sync succeeded - clear failure backoff, merge conflict, and sync backoff issues
	s.workspaceRegistry.ClearSyncFailures("ledger")
	if s.issues != nil {
		s.issues.ClearIssue(IssueTypeMergeConflict, "ledger")
		s.issues.ClearIssue(IssueTypeSyncBackoff, "ledger")
	}

	duration := time.Since(startTime)
	s.recordSync("pull", "ledger", duration, 0)
	s.metrics.RecordPullSuccess(duration)
	s.recordActivity() // mark as activity

	s.mu.Lock()
	s.lastSync = time.Now()
	s.mu.Unlock()

	// persist sync timestamp so status shows "synced" after daemon restart
	if err := s.workspaceRegistry.UpdateConfigLastSync("ledger"); err != nil {
		s.logger.Warn("failed to update ledger config last sync", "error", err)
	}
	s.recordSyncState(ctx, s.config.LedgerPath)

	// notify agent work manager that new ledger content may be available
	if s.agentWorkSignal != nil {
		select {
		case s.agentWorkSignal <- struct{}{}:
		default:
		}
	}

	// relay murmurs from ledger after pull
	if s.murmurRelay != nil {
		if l := s.workspaceRegistry.GetLedger(); l != nil && l.Path != "" {
			s.murmurRelay.RelayFromPath(l.Path, "ledger")
		}
	}

	// push any unpushed murmur commits (batched by sync cycle)
	if s.whisperRegistry != nil {
		if l := s.workspaceRegistry.GetLedger(); l != nil && l.Path != "" && l.Exists {
			s.pushMurmurCommits(ctx, l.Path)
		}
	}

	s.logger.Debug("pull complete", "duration", duration)
	return nil
}

// pushMurmurCommits pushes any local murmur commits to the ledger remote.
// Called during the ledger sync cycle for natural batching (~60s).
// Non-fatal: failures are logged but don't block the sync cycle.
func (s *SyncScheduler) pushMurmurCommits(ctx context.Context, ledgerPath string) {
	// check for unpushed murmur commits
	out, err := gitutil.RunGit(ctx, ledgerPath, "log", "--oneline", "origin/main..HEAD", "--", "data/murmurs/")
	if err != nil || strings.TrimSpace(out) == "" {
		return
	}

	s.logger.Debug("pushing unpushed murmur commits", "path", ledgerPath)

	s.ledgerMu.Lock()
	defer s.ledgerMu.Unlock()

	ep := s.workspaceRegistry.GetEndpoint()
	if err := gitutil.PushWithRetry(ctx, ledgerPath, gitutil.PushOpts{
		AutoResolvePrefixes: []string{"data/murmurs/"},
		Logger:              s.logger,
		PrePush: func(repoPath string) error {
			if ep != "" {
				return gitserver.RefreshRemoteCredentials(repoPath, ep)
			}
			return nil
		},
	}); err != nil {
		s.logger.Warn("murmur push failed (non-fatal)", "error", err)
	}
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
// │ Classification: CRITICAL PATH WITH FALLBACK                                 │
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
			// for team-context repos, detect incomplete two-phase clones
			// (.git exists but .sageox/ never materialized)
			incomplete := false
			if payload.RepoType == "team-context" {
				sageoxDir := filepath.Join(payload.RepoPath, ".sageox")
				if _, sErr := os.Stat(sageoxDir); os.IsNotExist(sErr) {
					incomplete = true
					s.logger.Warn("checkout: .git exists but .sageox missing, treating as incomplete clone",
						"path", payload.RepoPath)
					backupPath := fmt.Sprintf("%s.bak.%d", payload.RepoPath, time.Now().Unix())
					if rErr := os.Rename(payload.RepoPath, backupPath); rErr != nil {
						s.logger.Error("checkout: failed to move incomplete clone aside", "error", rErr)
					}
				}
			}
			if !incomplete {
				s.logger.Debug("checkout: repo already exists", "path", payload.RepoPath)
				result.AlreadyExists = true
				return result, nil
			}
			// fall through to clone below
		} else {
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
		}
		// continue with clone below
	}

	// acquire clone slot — blocks until a slot is available (up to maxConcurrentClones)
	// replaces the old single-boolean lock that rejected concurrent clones with an error,
	// which triggered unnecessary exponential backoff on internal contention
	if s.onBeforeCloneSem != nil {
		s.onBeforeCloneSem()
	}
	// acquire clone slot with timeout to prevent indefinite blocking
	semTimeout := s.cloneSemTimeoutOverride
	if semTimeout == 0 {
		semTimeout = cloneSemTimeout
	}
	select {
	case s.cloneSem <- struct{}{}:
		// acquired
	case <-time.After(semTimeout):
		return nil, fmt.Errorf("%w after %v: all %d slots busy", ErrCloneSemaphoreTimeout, semTimeout, maxConcurrentClones)
	}
	defer func() { <-s.cloneSem }()

	// TOCTOU fix: re-verify directory state after acquiring semaphore.
	// While waiting for a slot, another process may have completed the clone.
	if info, statErr := os.Stat(payload.RepoPath); statErr == nil && info.IsDir() {
		gitDir := filepath.Join(payload.RepoPath, ".git")
		if _, err := os.Stat(gitDir); err == nil {
			if payload.RepoType != "team-context" {
				// non-team-context: .git exists = already cloned
				s.logger.Debug("checkout: repo appeared while waiting for semaphore", "path", payload.RepoPath)
				result.AlreadyExists = true
				return result, nil
			}
			// team-context: check if .sageox/ now exists (clone completed by another process)
			sageoxDir := filepath.Join(payload.RepoPath, ".sageox")
			if _, sErr := os.Stat(sageoxDir); sErr == nil {
				s.logger.Debug("checkout: team-context completed while waiting for semaphore", "path", payload.RepoPath)
				result.AlreadyExists = true
				return result, nil
			}
		}
	}

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
	endpointURL := s.workspaceRegistry.GetEndpoint()
	s.logger.Info("checkout: loading credentials", "endpoint", endpointURL, "clone_url", payload.CloneURL)
	if creds, err := gitserver.LoadCredentialsForEndpoint(endpointURL); err == nil && creds != nil && creds.Token != "" {
		s.logger.Info("checkout: injecting git credentials", "token_len", len(creds.Token), "endpoint", endpointURL)
		cloneURL = injectGitCredentials(payload.CloneURL, "oauth2", creds.Token)
	} else if err != nil {
		s.logger.Error("checkout: failed to load git credentials", "error", err, "endpoint", endpointURL)
	} else if creds == nil {
		s.logger.Warn("checkout: no git credentials found", "endpoint", endpointURL)
	} else {
		s.logger.Warn("checkout: git credentials have empty token", "endpoint", endpointURL)
	}

	// branch on repo type: team contexts use two-phase partial clone,
	// ledgers use full clone (they need complete history for CLI writes)
	if payload.RepoType == "team-context" {
		if progress != nil {
			_ = progress.WriteStage("cloning", "Fetching repository structure...")
		}
		mCfg, err := s.twoPhaseClone(ctx, cloneURL, payload.RepoPath, progress)
		if err != nil {
			s.logger.Error("checkout: two-phase clone failed", "error", err)
			s.recordError(fmt.Sprintf("clone %s failed: %v", payload.RepoType, err))
			return nil, err
		}
		if mCfg != nil {
			if mCfg.SyncIntervalMin > 0 {
				s.workspaceRegistry.SetSyncIntervalMin(payload.RepoPath, mCfg.SyncIntervalMin)
			}
			if mCfg.GCIntervalDays > 0 {
				s.workspaceRegistry.SetGCInterval(payload.RepoPath, mCfg.GCIntervalDays)
			}
		}
	} else {
		// ledger: full clone
		if progress != nil {
			_ = progress.WriteStage("cloning", "Cloning repository...")
		}
		cloneArgs := append(gitHTTPTimeoutFlags(), "clone", "--quiet", cloneURL, payload.RepoPath)
		cloneCmd := exec.CommandContext(ctx, "git", cloneArgs...)
		// set cmd.Dir so git doesn't fail when daemon CWD has been deleted
		if parentDir := filepath.Dir(payload.RepoPath); parentDir != "" {
			_ = os.MkdirAll(parentDir, 0755)
			cloneCmd.Dir = parentDir
		}
		if output, err := cloneCmd.CombinedOutput(); err != nil {
			sanitizedOutput := gitutil.SanitizeOutput(string(output))
			s.logger.Error("checkout: clone failed", "error", err, "output", sanitizedOutput)
			s.recordError(fmt.Sprintf("clone %s failed: %v", payload.RepoType, err))
			if sanitizedOutput != "" {
				return nil, fmt.Errorf("git clone failed: %s", sanitizedOutput)
			}
			return nil, fmt.Errorf("git clone failed: %w", err)
		}

		// create AGENTS.md for newly cloned ledger repos
		if progress != nil {
			_ = progress.WriteStage("initializing", "Creating AGENTS.md...")
		}
		agentsOpts := &gitserver.AgentsMDOptions{
			RepoType: payload.RepoType,
		}
		if err := gitserver.CreateAgentsMD(ctx, payload.RepoPath, agentsOpts); err != nil {
			s.logger.Warn("checkout: failed to create AGENTS.MD", "error", err)
		}
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
	configCmd := exec.CommandContext(ctx, "git", "-C", payload.RepoPath, "config", "pull.rebase", "true")
	if output, err := configCmd.CombinedOutput(); err != nil {
		s.logger.Warn("checkout: failed to set pull.rebase config", "error", err, "output", string(output))
	}

	result.Cloned = true
	s.logger.Info("checkout: clone complete", "path", payload.RepoPath, "type", payload.RepoType)
	s.recordActivity()

	// invalidate workspace registry cache after cloning new repo
	s.workspaceRegistry.InvalidateConfigCache()

	return result, nil
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

	// resolve the upstream tracking branch (e.g. "refs/remotes/origin/main" -> "refs/heads/main")
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

// syncStateLock returns a per-workspace mutex for serializing sync state updates.
func (s *SyncScheduler) syncStateLock(path string) *sync.Mutex {
	actual, _ := s.syncStateLocks.LoadOrStore(path, &sync.Mutex{})
	return actual.(*sync.Mutex)
}

// recordSyncState captures git HEAD SHA and persists sync state for a workspace.
// Called after successful pull/clone operations. Failures are logged but not propagated
// since sync state is best-effort observability.
func (s *SyncScheduler) recordSyncState(ctx context.Context, workspacePath string) {
	lock := s.syncStateLock(workspacePath)
	lock.Lock()
	defer lock.Unlock()

	sha, err := gitHeadSHA(ctx, workspacePath)
	if err != nil {
		s.logger.Debug("failed to get HEAD SHA for sync state", "path", workspacePath, "error", err)
		sha = ""
	}

	state := LoadSyncState(workspacePath)
	state.RecordSuccess(sha)
	if err := SaveSyncState(workspacePath, state); err != nil {
		s.logger.Warn("failed to save sync state", "path", workspacePath, "error", err)
	}
}

// recordSyncStateFailure increments the failure counter in sync state.
func (s *SyncScheduler) recordSyncStateFailure(workspacePath string) {
	lock := s.syncStateLock(workspacePath)
	lock.Lock()
	defer lock.Unlock()

	state := LoadSyncState(workspacePath)
	state.RecordFailure()
	if err := SaveSyncState(workspacePath, state); err != nil {
		s.logger.Debug("failed to save sync state failure", "path", workspacePath, "error", err)
	}
}

// gitHeadSHA returns the current HEAD commit SHA for the repo at the given path.
func gitHeadSHA(ctx context.Context, repoPath string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", repoPath, "rev-parse", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
