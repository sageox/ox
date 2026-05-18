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
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/daemon/hooks"
	"github.com/sageox/ox/internal/flags"
	"github.com/sageox/ox/internal/gitserver"
	"github.com/sageox/ox/internal/gitutil"
	"github.com/sageox/ox/internal/ledger"
	"github.com/sageox/ox/internal/observability"
	"github.com/sageox/ox/internal/version"
	whisperstore "github.com/sageox/ox/internal/whisper/store"
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

// SyncOption configures optional SyncScheduler dependencies.
type SyncOption func(*SyncScheduler)

// WithGitRunner overrides the default git command runner.
func WithGitRunner(g gitutil.GitRunner) SyncOption {
	return func(s *SyncScheduler) { s.git = g }
}

// WithCredentialProvider overrides the default credential loader.
func WithCredentialProvider(c CredentialProvider) SyncOption {
	return func(s *SyncScheduler) { s.creds = c }
}

// SyncScheduler manages periodic sync operations.
type SyncScheduler struct {
	config *Config
	logger *slog.Logger

	// injectable dependencies (nil = production defaults, set in constructor)
	git   gitutil.GitRunner
	creds CredentialProvider

	// llmResolver is the optional tier-3 LLM merge escalator for the
	// pull cycle (ox-21cb). Wired by daemon startup when an LLM merge
	// binary is available; nil otherwise — pullManagedRepo treats nil
	// as "no daemon-side LLM tier configured" and falls through to the
	// existing surface-and-wait error path. CLI-side escalation
	// (cmd/ox/session_upload.go::ledgerLLMResolveHook) handles the
	// case where the user invokes a session-stop while ox is already
	// running under an LLM.
	llmResolver func(ctx context.Context, repoPath string, paths []string) (bool, error)

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
	cloneSem      chan struct{}  // semaphore limiting concurrent clones
	cloneInFlight sync.Map       // tracks workspace IDs with clone in progress (dedup)
	cloneWg       sync.WaitGroup // tracks in-flight background clone goroutines

	// cloneMu guards cloneWg.Add against a concurrent Wait during shutdown.
	// Without this, a pullChanges goroutine still running after ctx cancel
	// can race Add(1) with Wait() and trip WaitGroup's reuse panic.
	cloneMu       sync.Mutex
	cloneShutdown bool

	// lifecycle context — canceled when scheduler stops
	ctx context.Context

	// GC state — only one GC runs at a time across all workspaces
	gcInProgress int32

	// per-workspace locks for sync state file updates (load-mutate-save)
	syncStateLocks sync.Map // map[string]*sync.Mutex

	// workspace IDs where UpdateConfigLastSync is known to fail (e.g., uninitialized projects)
	configSyncSkipped sync.Map

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

	// tracks last ledger HEAD sha to detect changes and trigger ledger index rebuilds
	lastLedgerSha string

	// settings fetcher for CLI feature flag polling
	settingsFetcher *SettingsFetcher

	// kbListerFactory builds the kb API client used by syncBubbles. nil in
	// production (real api.KBClient is constructed lazily); tests inject
	// fakes via SetKBBubbleListerFactory.
	kbListerFactory kbBubbleListerFactory

	// OTel tracer for per-task trace contexts (nil = tracing disabled)
	tracer *observability.DaemonTracer

	// event bus for emitting sync events to hooks/notifications
	eventBus *hooks.EventBus
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
// Optional SyncOption values override default dependencies for testing.
func NewSyncScheduler(cfg *Config, logger *slog.Logger, opts ...SyncOption) *SyncScheduler {
	// get repo name for workspace registry
	repoName := filepath.Base(cfg.ProjectRoot)

	s := &SyncScheduler{
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

	for _, opt := range opts {
		opt(s)
	}

	// production defaults for nil dependencies
	if s.git == nil {
		s.git = gitutil.DefaultRunner()
	}
	if s.creds == nil {
		s.creds = &realCredentialProvider{}
	}

	return s
}

// SetActivityCallback sets the callback for activity tracking.
func (s *SyncScheduler) SetActivityCallback(cb func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onActivity = cb
}

// SetLLMResolver wires the daemon-side tier-3 LLM merge escalator
// (ox-21cb). Daemon startup calls this when OX_DAEMON_LLM_MERGE_BIN (or
// equivalent server-side config) is set. Pass nil to disable; the pull
// cycle then falls through to the existing surface-and-wait path
// without the LLM tier — same as before this issue.
//
// The resolver should follow automerge.Resolve semantics: returns
// (true, nil) on full resolution including rebase --continue, (false,
// nil) for "nothing I could do," ErrLLMUnavailable when no binary is
// configured, or any other error to abort.
func (s *SyncScheduler) SetLLMResolver(cb func(ctx context.Context, repoPath string, paths []string) (bool, error)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.llmResolver = cb
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

func (s *SyncScheduler) SetSettingsFetcher(f *SettingsFetcher) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.settingsFetcher = f
}

// SetTracer sets the OTel daemon tracer for per-task trace contexts.
func (s *SyncScheduler) SetTracer(t *observability.DaemonTracer) {
	s.tracer = t
}

// SetEventBus sets the event bus for emitting sync events.
func (s *SyncScheduler) SetEventBus(bus *hooks.EventBus) {
	s.eventBus = bus
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

// addClone is a race-free replacement for cloneWg.Add(1). Returns false if
// the scheduler is shutting down, in which case the caller must NOT spawn
// the background goroutine. Callers must not touch cloneWg.Add directly.
func (s *SyncScheduler) addClone() bool {
	s.cloneMu.Lock()
	defer s.cloneMu.Unlock()
	if s.cloneShutdown {
		return false
	}
	s.cloneWg.Add(1)
	return true
}

// waitClones signals shutdown (blocking further Adds) and waits up to
// timeout for in-flight clone goroutines to finish.
func (s *SyncScheduler) waitClones(timeout time.Duration) {
	s.cloneMu.Lock()
	s.cloneShutdown = true
	s.cloneMu.Unlock()

	done := make(chan struct{})
	go func() { s.cloneWg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(timeout):
		s.logger.Warn("timed out waiting for background clones")
	}
}

// Start starts the sync scheduler.
func (s *SyncScheduler) Start(ctx context.Context) {
	s.ctx = ctx

	// load initial workspace state from config
	if err := s.workspaceRegistry.LoadFromConfig(); err != nil {
		s.logger.Warn("failed to load workspace registry", "error", err)
	}

	// One-shot credential migration sweep (ox-eeqi). Walk every workspace
	// the daemon knows about; for each ledger with an https:// origin,
	// strip any embedded oauth2:TOKEN userinfo and install the ox
	// credential helper into .git/config. Idempotent — safe on every
	// daemon startup, including restarts in steady state.
	//
	// Backups taken AFTER this migration won't contain the embedded PAT.
	// Backups taken BEFORE it still do, which is what the user has to
	// rotate the leaked PAT to fully recover from. The migration is
	// best-effort: per-ledger failures log warnings and continue.
	s.migrateLedgerCredentialsOnStartup()

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

	// codedb freshness check ticker — detects new commits (branch switch, manual commit, pulled history).
	// Decoupled from git pull: dirty overlay via fsnotify handles file edits with ~5s latency.
	var codedbCheckTicker *time.Ticker
	var codedbCheckChan <-chan time.Time
	if s.config.CodeDBCheckInterval > 0 && s.config.ProjectRoot != "" && s.codedb != nil {
		codedbCheckTicker = time.NewTicker(s.config.CodeDBCheckInterval)
		codedbCheckChan = codedbCheckTicker.C
		defer codedbCheckTicker.Stop()
	}

	// ledger index check ticker — checks if codedb ledger index needs rebuild
	var ledgerIndexTicker *time.Ticker
	var ledgerIndexChan <-chan time.Time
	if s.config.LedgerCheckInterval > 0 && s.config.ProjectRoot != "" && s.codedb != nil {
		ledgerIndexTicker = time.NewTicker(s.config.LedgerCheckInterval)
		ledgerIndexChan = ledgerIndexTicker.C
		defer ledgerIndexTicker.Stop()
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

	// CLI settings polling — fetch feature flags from cloud API on a background interval.
	// Uses CLISettingsMaxAge (1h) as the ticker interval; SettingsFetcher deduplicates internally.
	var settingsTicker *time.Ticker
	var settingsChan <-chan time.Time
	if s.settingsFetcher != nil {
		settingsTicker = time.NewTicker(flags.CLISettingsMaxAge)
		settingsChan = settingsTicker.C
		defer settingsTicker.Stop()

		// initial fetch after short delay (let credentials settle from heartbeat)
		go func() {
			time.Sleep(3 * time.Second)
			s.settingsFetcher.Fetch(ctx)
		}()
	}

	// write initial heartbeat
	s.writeHeartbeats()

	// immediate anti-entropy check on startup (same logic as periodic ticker)
	s.triggerMissingClones()

	// immediate initial pull so last_sync gets populated right away
	// (don't wait 5 minutes for the first readTicker)
	go s.pullChanges(ctx)

	// initial codedb check on startup (don't wait for CodeDBCheckInterval)
	go s.checkCodeDBFreshness(ctx)

	for {
		select {
		case <-ctx.Done():
			// wait briefly for in-flight background clones to finish,
			// blocking any further clone spawns from still-running sync goroutines
			s.waitClones(3 * time.Second)
			s.logger.Info("sync scheduler stopped")
			return

		case <-readTicker.C:
			// not traced: high-frequency sync dominates span volume with little diagnostic value
			s.pullChanges(ctx)
			readTicker.Reset(jitteredDuration(s.config.SyncIntervalRead, 0.10))

		case <-teamContextChan:
			// not traced: high-frequency sync dominates span volume with little diagnostic value
			s.pullTeamContexts(ctx)
			// kb bubbles share the team-context cadence (15s). Per-bubble
			// FETCH_HEAD dedup inside pullManagedRepo (threshold =
			// max(SyncInterval/2, MinFetchHeadAge)) gates repo/custom
			// bubbles down to the slower read cadence automatically — see
			// kbSyncIntervalFor in sync_bubbles.go.
			s.syncBubbles(ctx)
			if teamContextTicker != nil {
				teamContextTicker.Reset(jitteredDuration(s.config.TeamContextSyncInterval, 0.10))
			}

		case <-heartbeatTicker.C:
			// not traced: lightweight liveness ping, not useful in telemetry
			s.writeHeartbeats()

		case <-versionCheckChan:
			taskCtx, span := s.tracer.StartTask(ctx, "daemon:version_check")
			s.checkLatestVersion(taskCtx)
			span.End()

		case <-gcChan:
			taskCtx, span := s.tracer.StartTask(ctx, "daemon:gc_check")
			s.checkAndRunGC(taskCtx)
			span.End()

		case <-distillChan:
			taskCtx, span := s.tracer.StartTask(ctx, "daemon:distill")
			s.triggerDistill(taskCtx)
			span.End()

		case <-githubSyncChan:
			if s.githubSync != nil {
				if l := s.workspaceRegistry.GetLedger(); l != nil && l.Path != "" && l.Exists {
					taskCtx, span := s.tracer.StartTask(ctx, "daemon:github_sync")
					s.githubSync.CheckAndSync(taskCtx, l.Path)
					span.End()
				}
			}

		case <-codedbCheckChan:
			taskCtx, span := s.tracer.StartTask(ctx, "daemon:codedb_freshness")
			s.checkCodeDBFreshness(taskCtx)
			span.End()
			if codedbCheckTicker != nil {
				codedbCheckTicker.Reset(jitteredDuration(s.config.CodeDBCheckInterval, 0.10))
			}

		case <-ledgerIndexChan:
			taskCtx, span := s.tracer.StartTask(ctx, "daemon:ledger_index")
			s.triggerLedgerIndexRebuild(taskCtx)
			span.End()

		case <-settingsChan:
			if s.settingsFetcher != nil {
				go func() {
					taskCtx, span := s.tracer.StartTask(ctx, "daemon:settings_fetch")
					s.settingsFetcher.Fetch(taskCtx)
					span.End()
				}()
			}

		case <-s.triggerChan:
			// watcher-triggered sync: skip sparse-checkout refresh to avoid
			// feedback loop where .sageox/cache/ writes trigger sync which
			// runs ConfigureSparseCheckout which could wipe cache
			taskCtx, span := s.tracer.StartTask(ctx, "daemon:watcher_sync")
			s.syncFromWatcher(taskCtx)
			span.End()
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
	_ = s.doPull(pullCtx, nil, false, true)
}

// checkCodeDBFreshness runs the codedb freshness check on its own schedule.
// Decoupled from pullChanges because dirty overlay (via fsnotify) handles
// uncommitted file search with ~5s latency — this only catches new commits.
func (s *SyncScheduler) checkCodeDBFreshness(ctx context.Context) {
	if s.codedb == nil {
		return
	}
	// update ledger path so CodeDB can index GitHub data from the ledger
	if ledger := s.workspaceRegistry.GetLedger(); ledger != nil && ledger.Path != "" && ledger.Exists {
		s.codedb.SetLedgerPath(ledger.Path)
	}
	s.codedb.CheckFreshness(ctx)
}

// checkLatestVersion fetches the latest GitHub release using ETag conditional requests.
// Called periodically by the sync scheduler to keep the version cache warm.
// If a newer version is detected, injects a broadcast whisper so all active agents are notified.
func (s *SyncScheduler) checkLatestVersion(ctx context.Context) {
	if err := s.versionCache.CheckAndUpdate(ctx); err != nil {
		s.logger.Warn("version check failed", "error", err)
		return
	}

	// check if the cached version is newer than what we're running
	data := s.versionCache.Data()
	if data == nil {
		return
	}

	latest := strings.TrimPrefix(data.LatestVersion, "v")
	current := strings.TrimPrefix(version.Version, "v")
	if latest == "" || latest == current {
		return
	}

	// simple semver comparison: is latest newer than current?
	if !isNewerSemver(latest, current) {
		return
	}

	// inject a broadcast whisper (agent_id="" = all agents receive it)
	s.mu.Lock()
	registry := s.whisperRegistry
	s.mu.Unlock()

	if registry == nil {
		return
	}

	id, err := uuid.NewV7()
	if err != nil {
		return
	}

	_ = registry.Add("ledger", whisperstore.WhisperEntry{
		ID:         id.String(),
		Scope:      "ledger",
		Type:       whisperstore.WhisperStructural,
		Source:     "version-check",
		Topic:      "upgrade",
		Content:    fmt.Sprintf("ox v%s → v%s available. Run `ox upgrade` to update.", current, latest),
		Importance: whisperstore.ImportanceNormal,
		CreatedAt:  time.Now(),
		AgentID:    "", // broadcast to all agents
	})

	s.logger.Info("upgrade whisper injected", "current", current, "latest", latest)
}

// isNewerSemver returns true if a is newer than b (simple dot-separated numeric comparison).
func isNewerSemver(a, b string) bool {
	aParts := strings.Split(a, ".")
	bParts := strings.Split(b, ".")
	for i := 0; i < len(aParts) && i < len(bParts); i++ {
		var aNum, bNum int
		_, _ = fmt.Sscanf(aParts[i], "%d", &aNum)
		_, _ = fmt.Sscanf(bParts[i], "%d", &bNum)
		if aNum > bNum {
			return true
		}
		if aNum < bNum {
			return false
		}
	}
	return len(aParts) > len(bParts)
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
func (s *SyncScheduler) doPull(ctx context.Context, progress *ProgressWriter, forceSync bool, refreshSparse bool) error {
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
					if s.addClone() {
						go s.cloneInBackground(ledger.CloneURL, ledger.Path, "ledger", ledger.ID) //nolint:gosec // G118 - intentionally uses background context; goroutine outlives request scope
					}
				}
			}
		}
		return nil // can't pull from repo that isn't cloned yet
	}

	// corruption check: move aside for re-clone on next cycle
	if !isValidGitRepo(s.config.LedgerPath) {
		backupPath := fmt.Sprintf("%s.bak.%d", s.config.LedgerPath, time.Now().Unix())
		s.logger.Warn("ledger repo corrupt, moving aside for re-clone",
			"path", s.config.LedgerPath, "backup", backupPath)
		if err := os.Rename(s.config.LedgerPath, backupPath); err != nil {
			s.logger.Error("failed to move corrupt ledger aside", "error", err)
			return fmt.Errorf("corrupt ledger at %s but rename failed: %w", s.config.LedgerPath, err)
		}
		return nil
	}

	// sync backoff — skip if recent sync failures triggered backoff
	if !s.shouldSyncOrBypass("ledger", forceSync) {
		return nil
	}

	// pull-in-progress mutex (ledger-specific: only one ledger pull at a time)
	s.mu.Lock()
	if s.pullInProgress {
		s.mu.Unlock()
		if progress != nil {
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

	if progress != nil {
		_ = progress.WriteStage("fetching", "Fetching from remote...")
	}
	s.logger.Debug("pulling changes")

	// acquire ledger mutex to prevent concurrent git operations with GitHub sync push
	s.ledgerMu.Lock()
	result := func() ManagedRepoPullResult {
		defer s.ledgerMu.Unlock()

		// refresh sparse checkout so the rolling murmur window stays current.
		// Without this, hourly directories computed at clone time go stale and
		// new murmur files pulled from remote are not materialized on disk.
		if refreshSparse {
			if err := ledger.ConfigureSparseCheckout(s.config.LedgerPath); err != nil {
				s.logger.Warn("failed to refresh ledger sparse checkout", "error", err)
			}
		}

		// ledger repos don't have a sync.manifest — use the manifest defaults
		// (data/) which cover all idempotent import paths (github, linear, murmurs).
		return s.pullManagedRepo(ctx, ManagedRepoPullOpts{
			RepoPath:           s.config.LedgerPath,
			RepoName:           "ledger",
			ProjectRoot:        s.config.ProjectRoot,
			SyncInterval:       s.config.SyncIntervalRead,
			DetectDivergence:   true,
			ResolveRules:       ledger.DefaultResolveRules,
			EnsureKBMergeAttrs: true,          // shared kb resilience for both ledger + team-context
			LLMResolver:        s.llmResolver, // ox-21cb: tier 3 escalation when configured
			Logger:             s.logger,
		})
	}()

	// handle skip results
	if result.Skipped {
		// lock file skip: report the issue
		if result.Issue != nil && s.issues != nil {
			s.issues.SetIssue(*result.Issue)
		} else if s.issues != nil {
			// clear lock issue if previously set but now resolved
			s.issues.ClearIssue(IssueTypeGitLock, "ledger")
		}

		// remote-unchanged or recently-fetched: update sync timestamps
		if result.SkipReason == "remote unchanged" || result.SkipReason == "recently fetched" {
			s.workspaceRegistry.ClearSyncFailures("ledger")
			s.mu.Lock()
			s.lastSync = time.Now()
			s.mu.Unlock()
			if err := s.workspaceRegistry.UpdateConfigLastSync("ledger"); err != nil {
				s.logger.Warn("failed to update ledger config last sync", "error", err)
			}
			s.recordSyncState(ctx, s.config.LedgerPath)
		}

		if progress != nil {
			_ = progress.WriteStage("skipped", result.SkipReason)
		}
		return nil
	}

	if progress != nil {
		_ = progress.WriteStage("pulling", "Pulling changes...")
	}

	// handle errors
	if result.Err != nil {
		s.recordError(result.Err.Error())
		s.metrics.RecordPullFailure()
		s.workspaceRegistry.RecordSyncFailure("ledger")
		s.recordSyncStateFailure(s.config.LedgerPath)

		if result.Issue != nil {
			if result.Issue.Type == IssueTypeMergeConflict {
				s.metrics.RecordConflict()
			}
			if s.issues != nil {
				s.issues.SetIssue(*result.Issue)
			}
		}
		if s.eventBus != nil {
			s.eventBus.Emit(ctx, hooks.Event{
				Name:    hooks.EventSyncFailed,
				Project: s.config.ProjectRoot,
				RepoID:  config.GetRepoID(s.config.ProjectRoot),
				Payload: hooks.SyncFailedPayload("ledger", "pull", result.Err.Error()),
			})
		}
		return fmt.Errorf("ledger %w", result.Err)
	}

	// handle divergence metric
	if result.Diverged {
		s.metrics.RecordDivergence()
	}

	// clear lock issue now that we've pulled successfully
	if s.issues != nil {
		s.issues.ClearIssue(IssueTypeGitLock, "ledger")
	}

	// sync succeeded - clear all failure-related issues
	s.workspaceRegistry.ClearSyncFailures("ledger")
	if s.issues != nil {
		s.issues.ClearIssue(IssueTypeMergeConflict, "ledger")
		s.issues.ClearIssue(IssueTypeSyncBackoff, "ledger")
		s.issues.ClearIssue(IssueTypeDiverged, "ledger")
	}

	duration := time.Since(startTime)
	s.recordSync("pull", "ledger", duration, 0)
	s.metrics.RecordPullSuccess(duration)
	s.recordActivity()

	if s.eventBus != nil {
		s.eventBus.Emit(ctx, hooks.Event{
			Name:    hooks.EventSyncCompleted,
			Project: s.config.ProjectRoot,
			RepoID:  config.GetRepoID(s.config.ProjectRoot),
			Payload: hooks.SyncPayload("ledger", "pull", duration),
		})
	}

	s.mu.Lock()
	s.lastSync = time.Now()
	s.mu.Unlock()

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

	if progress != nil {
		_ = progress.WriteStage("complete", "Pull complete")
	}
	s.logger.Debug("pull complete", "duration", duration)
	return nil
}

// pushMurmurCommits pushes any local murmur commits to the ledger remote.
// Called during the ledger sync cycle for natural batching (~60s).
// Non-fatal: failures are logged but don't block the sync cycle.
func (s *SyncScheduler) pushMurmurCommits(ctx context.Context, ledgerPath string) {
	// check for unpushed murmur commits
	out, err := s.git.RunGit(ctx, ledgerPath, "log", "--oneline", "origin/main..HEAD", "--", "data/murmurs/")
	if err != nil || strings.TrimSpace(out) == "" {
		return
	}

	s.logger.Debug("pushing unpushed murmur commits", "path", ledgerPath)

	s.ledgerMu.Lock()
	defer s.ledgerMu.Unlock()

	ep := s.workspaceRegistry.GetEndpoint()
	if err := gitutil.PushWithRetry(ctx, ledgerPath, gitutil.PushOpts{
		AutoResolvePrefixes: ledger.AutoResolvePrefixes,
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

// detectDivergedBranches checks if local and remote have both progressed independently.
func (s *SyncScheduler) detectDivergedBranches(ctx context.Context) bool {
	return detectDivergedBranchesAt(ctx, s.config.LedgerPath)
}

// syncAll performs a full sync (pull-only — CLI handles push via LFS pipeline).
func (s *SyncScheduler) syncAll(ctx context.Context) {
	s.pullChanges(ctx)
}

// syncFromWatcher handles watcher-triggered sync without sparse-checkout refresh.
// The file watcher fires on .sageox/cache/ writes (codedb indexing), and running
// ConfigureSparseCheckout on that path creates a feedback loop that can wipe the
// cache mid-index. The watcher only needs to pull remote changes, not reconfigure
// the sparse-checkout cone.
func (s *SyncScheduler) syncFromWatcher(ctx context.Context) {
	s.triggerMissingClones()
	pullCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	_ = s.doPull(pullCtx, nil, false, false)
	// watcher events may indicate branch switch or commit — check for new commits
	s.checkCodeDBFreshness(ctx)
}

// triggerLedgerIndexRebuild checks if content sources (ledger, team contexts) have
// changed since the last ledger index build and fires a background rebuild if so.
// Runs on its own tick (LedgerCheckInterval), independent of ledger pull cadence.
func (s *SyncScheduler) triggerLedgerIndexRebuild(ctx context.Context) {
	if s.codedb == nil {
		return
	}
	ledger := s.workspaceRegistry.GetLedger()
	if ledger == nil || ledger.Path == "" || !ledger.Exists {
		return
	}

	// build composite fingerprint from all content sources
	fingerprint := s.contentSourceFingerprint(ctx, ledger.Path)
	s.mu.Lock()
	if fingerprint == "" || fingerprint == s.lastLedgerSha {
		s.mu.Unlock()
		return
	}
	oldFingerprint := s.lastLedgerSha
	s.mu.Unlock()

	s.logger.Info("codedb ledger index rebuild triggered", "old_fingerprint", oldFingerprint, "new_fingerprint", fingerprint)
	go func(fp, ledgerPath string) {
		s.codedb.BuildLedgerIndex(ctx, ledgerPath)
		// only advance fingerprint after successful build so failed builds retry
		s.mu.Lock()
		s.lastLedgerSha = fp
		s.mu.Unlock()
	}(fingerprint, ledger.Path)
}

// contentSourceFingerprint returns a composite hash of HEAD shas from all content
// sources: ledger + team contexts. Any change in any source triggers a rebuild.
func (s *SyncScheduler) contentSourceFingerprint(ctx context.Context, ledgerPath string) string {
	// start with ledger HEAD
	out, err := exec.CommandContext(ctx, "git", "-C", ledgerPath, "rev-parse", "HEAD").Output()
	if err != nil {
		s.logger.Debug("codedb ledger: failed to get ledger HEAD", "error", err)
		return ""
	}
	parts := []string{"ledger=" + strings.TrimSpace(string(out))}

	// append team context HEADs sorted by path for deterministic fingerprint
	teamContexts := s.workspaceRegistry.GetTeamContexts()
	sort.Slice(teamContexts, func(i, j int) bool {
		return teamContexts[i].Path < teamContexts[j].Path
	})
	for _, tc := range teamContexts {
		if tc.Path == "" || !tc.Exists {
			continue
		}
		tcOut, tcErr := exec.CommandContext(ctx, "git", "-C", tc.Path, "rev-parse", "HEAD").Output()
		if tcErr != nil {
			continue // skip unavailable team contexts
		}
		parts = append(parts, tc.Path+"="+strings.TrimSpace(string(tcOut)))
	}

	return strings.Join(parts, ":")
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

	return s.doPull(ctx, progress, true, true)
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

	// Clone with bare URL + an in-argv credential helper instead of embedding
	// the PAT into the origin URL. Per ox-eeqi, embedded credentials persist
	// into .git/config and leak via backups, `git remote -v`, and GIT_TRACE.
	// After clone succeeds, we install the helper into the cloned repo's
	// .git/config (see post-clone block) so future fetch/push operations
	// resolve credentials the same way.
	//
	// The `-c credential.helper=` (empty) before the real helper resets any
	// inherited helpers so ours is authoritative for this single invocation.
	cloneURL := payload.CloneURL
	endpointURL := s.workspaceRegistry.GetEndpoint()
	s.logger.Info("checkout: clone via credential helper",
		"endpoint", endpointURL, "clone_url", payload.CloneURL)
	// Sanity check: we have credentials for this endpoint (caller already
	// authenticated). Without them, the helper will return nothing and git
	// falls back to its own prompt — fine for interactive use, broken for
	// daemon. So we log clearly when creds are missing.
	if creds, err := s.creds.LoadCredentialsForEndpoint(endpointURL); err != nil {
		s.logger.Error("checkout: failed to load git credentials", "error", err, "endpoint", endpointURL)
	} else if creds == nil || creds.Token == "" {
		s.logger.Warn("checkout: no git credentials found; clone may fail",
			"endpoint", endpointURL)
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
		// Per ox-eeqi: clone with ox-managed credential helper, not embedded
		// URL credentials. `-c credential.helper=` empty clears inherited
		// helpers; the second `-c` installs ours for this single invocation.
		helperCmd := gitserver.DefaultHelperCommand()
		preArgs := []string{
			"-c", "credential.helper=",
			"-c", "credential.helper=" + helperCmd,
		}
		cloneArgs := append(preArgs, gitHTTPTimeoutFlags()...)
		// Belt-and-suspenders hardening on the daemon's auto-clone path:
		//   - protocol.{file,ext}.allow=never disables ext::sh:// transport (CVE-2017-1000117
		//     class) and file:// fetch from submodules / .gitmodules, even though we already
		//     reject those at the URL parse step. A compromised cloud API can't sneak them
		//     past the parser via, say, a redirect into a submodule that uses ext::.
		//   - "--" terminates option parsing so an attacker URL beginning with "-" can never
		//     be reinterpreted as a flag, regardless of future scheme/host policy changes.
		//
		// gitserver.TestAllowFileTransport is a test-only override that lets
		// bubble/sync tests clone from a local bare repo via file://. Production
		// must never set it; see its godoc.
		if !gitserver.TestAllowFileTransport {
			cloneArgs = append(cloneArgs, "-c", "protocol.file.allow=never")
		}
		cloneArgs = append(cloneArgs,
			"-c", "protocol.ext.allow=never",
			"clone", "--quiet", "--", cloneURL, payload.RepoPath,
		)
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

	// Install the ox-managed credential helper into the fresh clone's
	// .git/config so subsequent fetch/push/LFS operations resolve creds
	// via the helper instead of via embedded URL userinfo. Idempotent on
	// re-clone. Per ox-eeqi.
	if changed, err := gitserver.MigrateLedgerCredentials(payload.RepoPath, gitserver.DefaultHelperCommand()); err != nil {
		s.logger.Warn("checkout: failed to install credential helper",
			"error", err, "path", payload.RepoPath)
	} else if changed {
		s.logger.Info("checkout: stripped embedded PAT from origin",
			"path", payload.RepoPath)
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
// offline-safe: returns false on any error, causing fallback to normal fetch+pull path
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

// trustedGitHosts is the allowlist of hosts permitted for the DAEMON's
// auto-clone path. It is intentionally narrower than the CLI's manual-clone
// surface: the daemon only ever clones what the SageOx cloud API directed it
// to, and the cloud API should only ever direct it at SageOx-controlled hosts.
//
// Threat collapsed by narrowing: a compromised cloud API could previously
// return a `repo.URL` pointing at `github.com/attacker/evil.git` and have
// the daemon clone it as a "team context" — landing attacker-controlled bytes
// inside the user's workspace without any further compromise. See SECREVIEW
// threat-model finding on the cloud-API trust boundary (workspace_registry.go).
//
// Includes base domains (sageox.ai, sageox.io) to allow staging subdomains
// like git.test.sageox.ai.
//
// If a future product feature ever requires the daemon to clone from
// non-SageOx hosts, add an explicit, scoped allow-list keyed on the workspace
// type — do NOT widen this list.
var trustedGitHosts = []string{
	"sageox.io",
	"sageox.ai",
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

// migrateLedgerCredentialsOnStartup walks every known workspace and runs
// the ox-eeqi credential-helper migration. Per-workspace failures log and
// continue — one broken ledger can't block daemon startup.
//
// Migration semantics per workspace:
//   - SSH remotes, file:// remotes, missing-origin repos: skipped (no-op).
//   - https:// origin with embedded oauth2:TOKEN: strip + install helper.
//   - https:// origin already bare: install helper only.
//   - Repo with no git config (path doesn't exist yet, not cloned):
//     skipped — checkout flow installs the helper when the clone completes.
//
// The migration sweep is intentionally non-fatal at every level. The user
// can recover from a botched migration by deleting the helper config from
// .git/config manually; that's a strictly weaker failure mode than
// continuing to leak the PAT.
func (s *SyncScheduler) migrateLedgerCredentialsOnStartup() {
	workspaces := s.workspaceRegistry.GetAllWorkspaces()
	if len(workspaces) == 0 {
		return
	}
	helper := gitserver.DefaultHelperCommand()
	migrated := 0
	for _, ws := range workspaces {
		if ws.Path == "" {
			continue
		}
		// Skip workspaces whose path doesn't yet exist — they'll be handled
		// at checkout time.
		if _, err := os.Stat(filepath.Join(ws.Path, ".git")); err != nil {
			continue
		}
		changed, err := gitserver.MigrateLedgerCredentials(ws.Path, helper)
		if err != nil {
			s.logger.Warn("startup migration: helper install failed",
				"path", ws.Path, "error", err)
			continue
		}
		if changed {
			migrated++
			s.logger.Info("startup migration: stripped embedded PAT", "path", ws.Path)
		}
	}
	if migrated > 0 {
		s.logger.Info("startup migration: complete",
			"workspaces_total", len(workspaces),
			"workspaces_migrated", migrated)
	}
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
