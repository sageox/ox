package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/daemon/agentwork"
	"github.com/sageox/ox/internal/daemon/hooks"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/flags"
	"github.com/sageox/ox/internal/observability"
	"github.com/sageox/ox/internal/gitserver"
	"github.com/sageox/ox/internal/gitutil"
	"github.com/sageox/ox/internal/ledger"
	"github.com/sageox/ox/internal/paths"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/version"
	whisperstore "github.com/sageox/ox/internal/whisper/store"
)

// Version returns the daemon version including build timestamp.
// Used for heartbeat version comparison to detect when CLI has been rebuilt.
// Includes BuildDate so dirty rebuilds (same git hash) still trigger restart.
func Version() string {
	return version.Full()
}

// Restart loop detection constants.
// If daemon restarts more than maxRestartsInWindow times within restartWindow,
// it's considered a restart loop and we add throttle delays.
const (
	restartWindow       = 5 * time.Minute // window to detect restart loops
	maxRestartsInWindow = 3               // max restarts before throttling
	maxThrottleDelay    = 2 * time.Minute // max delay between restart attempts
	minThrottleDelay    = 5 * time.Second // starting delay
	restartHistoryFile  = "daemon-restarts.json"
)

// ErrNotRunning indicates the daemon is not running.
var ErrNotRunning = errors.New("daemon not running")

// ErrShutdownTimeout indicates goroutines did not finish within the timeout.
var ErrShutdownTimeout = errors.New("shutdown timeout: goroutines did not finish in time")

// restartHistory tracks recent daemon starts for loop detection.
type restartHistory struct {
	Restarts []time.Time `json:"restarts"`
}

// restartHistoryPath returns the path to the restart history file.
func restartHistoryPath() string {
	return filepath.Join(config.GetUserConfigDir(), restartHistoryFile)
}

// loadRestartHistory loads the restart history from disk.
func loadRestartHistory() (*restartHistory, error) {
	data, err := os.ReadFile(restartHistoryPath())
	if err != nil {
		if os.IsNotExist(err) {
			return &restartHistory{}, nil
		}
		return nil, err
	}
	var h restartHistory
	if err := json.Unmarshal(data, &h); err != nil {
		return &restartHistory{}, nil // corrupt file, start fresh
	}
	return &h, nil
}

// saveRestartHistory saves the restart history to disk.
func saveRestartHistory(h *restartHistory) error {
	// prune old entries (keep only those within window)
	cutoff := time.Now().Add(-restartWindow)
	var recent []time.Time
	for _, t := range h.Restarts {
		if t.After(cutoff) {
			recent = append(recent, t)
		}
	}
	h.Restarts = recent

	data, err := json.Marshal(h)
	if err != nil {
		return err
	}
	return os.WriteFile(restartHistoryPath(), data, 0600)
}

// recordRestart adds the current time to restart history.
func recordRestart() error {
	h, _ := loadRestartHistory() // ignore errors, start fresh if needed
	h.Restarts = append(h.Restarts, time.Now())
	return saveRestartHistory(h)
}

// checkRestartLoop checks if we're in a restart loop and returns throttle delay.
// Returns 0 if no throttling needed.
func checkRestartLoop(logger *slog.Logger) time.Duration {
	h, err := loadRestartHistory()
	if err != nil {
		return 0
	}

	// count restarts within window
	cutoff := time.Now().Add(-restartWindow)
	count := 0
	for _, t := range h.Restarts {
		if t.After(cutoff) {
			count++
		}
	}

	if count < maxRestartsInWindow {
		return 0
	}

	// calculate exponential backoff: 5s, 10s, 20s, 40s, ... up to 2min
	excess := count - maxRestartsInWindow
	delay := minThrottleDelay
	for i := 0; i < excess && delay < maxThrottleDelay; i++ {
		delay *= 2
	}
	if delay > maxThrottleDelay {
		delay = maxThrottleDelay
	}

	logger.Warn("restart loop detected, throttling",
		"restart_count", count,
		"window", restartWindow,
		"delay", delay,
	)
	return delay
}

// Daemon manages background ledger sync operations.
type Daemon struct {
	config *Config
	logger *slog.Logger

	// lifecycle
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	// components
	server                 *Server
	service                *daemonServiceImpl
	scheduler              *SyncScheduler
	watcher                *Watcher
	heartbeat              *HeartbeatHandler
	telemetry              *TelemetryCollector
	friction               *FrictionCollector
	issues                 *IssueTracker
	codedb                 *CodeDBManager
	agentWorker            *agentwork.Manager
	sessionFinalizeHandler *agentwork.SessionFinalizeHandler
	sessionWatcher         *agentwork.SessionWatcherManager
	whisperRegistry        *WhisperRegistry
	murmurNudgeSource      *MurmurNudgeSource
	projectWatcher         *ProjectWatcher
	dbMaintenance          *DBMaintenanceScheduler
	settingsFetcher        *SettingsFetcher
	eventBus               *hooks.EventBus
	tracer                 *observability.DaemonTracer

	// state
	mu               sync.Mutex
	running          bool
	restartRequested bool      // set when version mismatch triggers restart
	wasSuperseded    bool      // set when exiting because another daemon took over
	startTime        time.Time // daemon start time for uptime tracking
	lastActivity     time.Time // tracks last activity for inactivity timeout
	pendingWorkSince time.Time // when pending work was first detected during inactivity (zero = no pending work)

	// cachedWorkspacePath is set BEFORE StabilizeCWD() so that later code
	// never falls back to os.Getwd() (which returns $HOME post-stabilize)
	cachedWorkspacePath string

	// startup timing (written once in Start(), read by IPC status handler)
	startupDurationMs  atomic.Int64
	throttleDurationMs atomic.Int64
}

// New creates a new daemon instance.
func New(config *Config, logger *slog.Logger) *Daemon {
	if config == nil {
		config = DefaultConfig()
	}
	if logger == nil {
		logger = slog.Default()
	}

	return &Daemon{
		config:       config,
		logger:       logger,
		lastActivity: time.Now(), // initialize activity timestamp
	}
}

// recordActivity updates the last activity timestamp.
func (d *Daemon) recordActivity() {
	d.mu.Lock()
	d.lastActivity = time.Now()
	d.mu.Unlock()
}

// timeSinceLastActivity returns duration since last activity.
func (d *Daemon) timeSinceLastActivity() time.Duration {
	d.mu.Lock()
	defer d.mu.Unlock()
	return time.Since(d.lastActivity)
}

// checkDeadAgentsAndFinalize scans tracked agents for dead PIDs and immediately
// enqueues orphaned sessions for finalization. This is the fast path: agent dies
// → heartbeat check detects (~seconds) → finalization queued → daemon stays alive.
func (d *Daemon) checkDeadAgentsAndFinalize() {
	if d.heartbeat == nil || d.agentWorker == nil || d.config.LedgerPath == "" {
		return
	}

	tracker := d.heartbeat.GetAgentActivity()
	keys := tracker.Keys()
	now := time.Now()

	for _, agentID := range keys {
		last := tracker.Last(agentID)
		if now.Sub(last) <= IdleThreshold {
			continue // recently active, skip
		}

		pid := d.heartbeat.GetAgentPID(agentID)
		if pid <= 0 {
			continue // no PID known
		}

		proc, err := os.FindProcess(pid)
		if err != nil || proc.Signal(syscall.Signal(0)) != nil {
			// PID is dead — check for orphaned sessions
			d.logger.Info("agent PID dead, checking for orphaned sessions",
				"agent_id", agentID, "pid", pid,
			)

			if d.sessionFinalizeHandler == nil {
				continue
			}

			items := d.sessionFinalizeHandler.DetectOrphanedForAgent(d.config.LedgerPath, agentID, pid)
			for _, item := range items {
				if d.agentWorker.Enqueue(item) {
					d.logger.Info("enqueued orphaned session for finalization",
						"agent_id", agentID, "dedup_key", item.DedupKey,
					)
				}
			}
		}
	}
}

// hasPendingWork returns true if there are queued/in-flight finalization items
// or active session watchers.
func (d *Daemon) hasPendingWork() bool {
	if d.agentWorker != nil && d.agentWorker.HasPendingWork() {
		return true
	}
	if d.sessionWatcher != nil && len(d.sessionWatcher.ActiveSessions()) > 0 {
		return true
	}
	return false
}

// RestartRequested returns true if the daemon stopped due to a version mismatch
// and should be re-executed with the updated binary.
func (d *Daemon) RestartRequested() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.restartRequested
}

// Start starts the daemon in the foreground.
// This blocks until Stop is called or a termination signal is received.
func (d *Daemon) Start() error {
	startTotal := time.Now()

	// check for restart loop before proceeding
	var throttleDuration time.Duration
	if delay := checkRestartLoop(d.logger); delay > 0 {
		d.logger.Info("throttling startup due to restart loop", "delay", delay)
		throttleStart := time.Now()
		time.Sleep(delay)
		throttleDuration = time.Since(throttleStart)
	}

	// record this startup attempt for loop detection
	if err := recordRestart(); err != nil {
		d.logger.Debug("failed to record restart", "error", err)
	}

	d.mu.Lock()
	if d.running {
		d.mu.Unlock()
		return errors.New("daemon already running")
	}

	d.ctx, d.cancel = context.WithCancel(context.Background())
	d.running = true
	d.mu.Unlock()

	d.logger.Info("daemon starting", "ledger", d.config.LedgerPath, "version", Version())

	// write PID file (informational only)
	if err := d.writePidFile(); err != nil {
		d.logger.Warn("failed to write pid file", "error", err)
	}

	// register in daemon registry for multi-daemon support
	// use ProjectRoot (the actual workspace), not LedgerPath
	// NOTE: must happen BEFORE StabilizeCWD() which changes cwd to $HOME
	workspacePath := d.config.ProjectRoot
	if workspacePath == "" {
		d.logger.Warn("daemon config missing ProjectRoot, falling back to cwd")
		workspacePath, _ = os.Getwd()
	}
	// cache for later use (after StabilizeCWD, os.Getwd returns $HOME)
	d.cachedWorkspacePath = workspacePath

	// Self-heal "init was reverted from git" before we cache the workspace ID.
	// If .sageox/config.json is missing but the surviving local state still
	// encodes a repo_id (config.local.toml ledger path or .repo_<uuid> marker),
	// write it back so RegisterDaemon → CurrentWorkspaceID picks the
	// repo-id-derived workspace_id the CLI will also compute. Without this,
	// the daemon registers under a path-hash workspace_id the CLI provably
	// cannot recompute, and every IPC call sees "daemon unavailable".
	if backfilled, err := config.BackfillProjectConfigFromLocalState(workspacePath); err != nil {
		d.logger.Warn("config.json backfill failed", "error", err, "workspace", workspacePath)
	} else if backfilled {
		d.logger.Info("recovered .sageox/config.json from local state",
			"workspace", workspacePath,
			"reason", "init artifacts likely reverted from git")
	}

	if err := RegisterDaemon(workspacePath, Version()); err != nil {
		d.logger.Warn("failed to register daemon", "error", err)
	}

	// move CWD to $HOME so git commands don't fail if the original CWD
	// is deleted (e.g. tmpdir cleanup on macOS). Must happen after
	// workspace ID is cached and PID file is written.
	StabilizeCWD()

	// start IPC server — daemonServiceImpl is a thin shim over *Daemon that
	// implements DaemonService; components (scheduler, heartbeat, etc.) are
	// initialized below, so all methods guard against nil receivers.
	d.service = &daemonServiceImpl{d}
	d.server = NewServerWithService(d.logger, d.service)

	setupDuration := d.initComponents()

	d.startWorkers()

	// record startup timing
	totalDuration := time.Since(startTotal)
	d.startupDurationMs.Store(totalDuration.Milliseconds())
	d.throttleDurationMs.Store(throttleDuration.Milliseconds())
	d.logger.Info("daemon startup complete",
		"total", totalDuration,
		"throttle", throttleDuration,
		"setup", setupDuration,
	)

	// NOTE: no activity callback on scheduler — the daemon's own background
	// syncs must NOT reset the inactivity timer, or it will never self-exit.

	// handle shutdown signals (SIGINT, SIGTERM, SIGHUP on Unix)
	// these handle explicit kills (e.g., `ox daemon stop` sends SIGTERM via IPC)
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, shutdownSignals()...)

	// inactivity check ticker (only if timeout is configured)
	var inactivityTicker *time.Ticker
	var inactivityChan <-chan time.Time
	if d.config.InactivityTimeout > 0 {
		// check every 5 minutes or 1/10th of timeout, whichever is smaller
		checkInterval := d.config.InactivityTimeout / 10
		if checkInterval > 5*time.Minute {
			checkInterval = 5 * time.Minute
		}
		if checkInterval < time.Minute {
			checkInterval = time.Minute
		}
		inactivityTicker = time.NewTicker(checkInterval)
		inactivityChan = inactivityTicker.C
		defer inactivityTicker.Stop()
		d.logger.Info("inactivity timeout enabled", "timeout", d.config.InactivityTimeout, "check_interval", checkInterval)
	}

	// socket self-check ticker: detect when another daemon has taken over our socket
	var socketCheckTicker *time.Ticker
	var socketCheckChan <-chan time.Time
	if d.config.SocketCheckInterval > 0 {
		socketCheckTicker = time.NewTicker(d.config.SocketCheckInterval)
		socketCheckChan = socketCheckTicker.C
		defer socketCheckTicker.Stop()
		d.logger.Info("socket self-check enabled", "interval", d.config.SocketCheckInterval)
	}

	for {
		select {
		case sig := <-sigChan:
			d.logger.Info("received signal", "signal", sig)
			return d.shutdown()
		case <-d.ctx.Done():
			d.logger.Info("context canceled")
			return d.shutdown()
		case <-socketCheckChan:
			if superseded() {
				d.logger.Info("registry PID changed, shutting down (superseded by new daemon)")
				d.wasSuperseded = true
				return d.shutdown()
			}
		case <-inactivityChan:
			// fast-path: detect dead agent PIDs and immediately enqueue orphaned sessions
			d.checkDeadAgentsAndFinalize()

			// check if ledger path still exists (handles directory renames/moves)
			if d.config.LedgerPath != "" {
				if _, err := os.Stat(d.config.LedgerPath); os.IsNotExist(err) {
					d.logger.Info("ledger path no longer exists, exiting", "path", d.config.LedgerPath)
					return d.shutdown()
				}
			}

			inactiveDuration := d.timeSinceLastActivity()
			uptime := time.Since(d.startTime)
			minUptime := time.Minute // don't exit before 1 minute of runtime
			if inactiveDuration >= d.config.InactivityTimeout && uptime >= minUptime {
				// check for pending work before exiting
				if d.hasPendingWork() {
					d.mu.Lock()
					if d.pendingWorkSince.IsZero() {
						d.pendingWorkSince = time.Now()
						d.mu.Unlock()
						d.logger.Info("inactivity timeout reached but pending work exists, delaying exit",
							"inactive_duration", inactiveDuration,
							"grace_period", d.config.PendingWorkGracePeriod,
						)
						continue
					}
					graceElapsed := time.Since(d.pendingWorkSince)
					d.mu.Unlock()
					if graceElapsed < d.config.PendingWorkGracePeriod {
						d.logger.Debug("pending work grace period active",
							"elapsed", graceElapsed,
							"grace_period", d.config.PendingWorkGracePeriod,
						)
						continue
					}
					d.logger.Warn("pending work grace period exceeded, shutting down anyway",
						"grace_elapsed", graceElapsed,
						"grace_period", d.config.PendingWorkGracePeriod,
					)
				}
				d.logger.Info("shutting down due to inactivity", "inactive_duration", inactiveDuration, "timeout", d.config.InactivityTimeout, "uptime", uptime)
				return d.shutdown()
			}
			// reset pending work tracker when not past inactivity timeout
			d.mu.Lock()
			d.pendingWorkSince = time.Time{}
			d.mu.Unlock()
			d.logger.Debug("inactivity check", "inactive_duration", inactiveDuration, "timeout", d.config.InactivityTimeout)
		}
	}
}

// getAgentSessions returns active agent sessions from the heartbeat handler.
// Converts the activity tracker data into AgentSession structs.
// Deprecated: Use getAgentInstances instead.
func (d *Daemon) getAgentSessions() []AgentSession {
	if d.heartbeat == nil {
		return nil
	}

	// use cached workspace path (set before StabilizeCWD changed cwd to $HOME)
	workspacePath := d.cachedWorkspacePath
	if workspacePath == "" {
		workspacePath = d.config.ProjectRoot
	}

	tracker := d.heartbeat.GetAgentActivity()
	keys := tracker.Keys()
	sessions := make([]AgentSession, 0, len(keys))

	now := time.Now()
	idleThreshold := IdleThreshold

	for _, agentID := range keys {
		last := tracker.Last(agentID)
		count := tracker.Count(agentID)

		status := StatusActive
		if now.Sub(last) > idleThreshold {
			status = StatusIdle
		}

		sessions = append(sessions, AgentSession{
			AgentID:        agentID,
			WorkspacePath:  workspacePath,
			LastHeartbeat:  last,
			HeartbeatCount: count,
			Status:         status,
		})
	}

	return sessions
}

// heartbeatAgentResolver adapts HeartbeatHandler to ActiveAgentResolver
// for use by the file-change murmur publisher.
type heartbeatAgentResolver struct {
	heartbeat *HeartbeatHandler
}

func (r *heartbeatAgentResolver) ActiveAgentIDs() []string {
	if r.heartbeat == nil {
		return nil
	}
	tracker := r.heartbeat.GetAgentActivity()
	keys := tracker.Keys()
	now := time.Now()
	var active []string
	for _, id := range keys {
		last := tracker.Last(id)
		elapsed := now.Sub(last)
		if elapsed > StaleThreshold {
			continue
		}
		// Check PID liveness — a dead PID with a stale-ish heartbeat means exited.
		pid := r.heartbeat.GetAgentPID(id)
		if pid > 0 {
			proc, err := os.FindProcess(pid)
			if err != nil || proc.Signal(syscall.Signal(0)) != nil {
				if elapsed > IdleThreshold {
					continue
				}
			}
		}
		active = append(active, id)
	}
	return active
}

// getAgentInstances returns active agent instances from the heartbeat handler.
// Converts the activity tracker data into InstanceInfo structs.
func (d *Daemon) getAgentInstances() []InstanceInfo {
	if d.heartbeat == nil {
		return nil
	}

	// use cached workspace path (set before StabilizeCWD changed cwd to $HOME)
	workspacePath := d.cachedWorkspacePath
	if workspacePath == "" {
		workspacePath = d.config.ProjectRoot
	}

	tracker := d.heartbeat.GetAgentActivity()
	keys := tracker.Keys()
	instances := make([]InstanceInfo, 0, len(keys))

	now := time.Now()

	for _, agentID := range keys {
		last := tracker.Last(agentID)
		count := tracker.Count(agentID)

		elapsed := now.Sub(last)

		// Instant liveness check: if we know the PID, trust it over heartbeat timing.
		// A living process is always shown (no stale cutoff). A dead PID is always exited.
		agentPID := d.heartbeat.GetAgentPID(agentID)
		pidAlive := false
		if agentPID > 0 {
			proc, procErr := os.FindProcess(agentPID)
			pidAlive = procErr == nil && proc.Signal(syscall.Signal(0)) == nil
		}

		// skip stale instances with no known-live PID — likely ended session
		if elapsed > StaleThreshold && !pidAlive {
			continue
		}

		status := StatusActive
		if elapsed > IdleThreshold {
			status = StatusIdle
		}
		// Only mark exited if PID is dead AND the heartbeat is stale.
		// Hook heartbeats set ParentPID to the short-lived shell subprocess PID, which
		// dies immediately after the hook. A fresh heartbeat means the agent is still
		// active even if the most-recently-recorded PID is gone.
		if agentPID > 0 && !pidAlive && elapsed > IdleThreshold {
			status = StatusExited
		}

		ctxStats := d.heartbeat.GetAgentContextStats(agentID)
		instances = append(instances, InstanceInfo{
			AgentID:                 agentID,
			WorkspacePath:           workspacePath,
			LastHeartbeat:           last,
			HeartbeatCount:          count,
			Status:                  status,
			CumulativeContextTokens: ctxStats.ContextTokens,
			CommandCount:            ctxStats.CommandCount,
			ParentAgentID:           d.heartbeat.GetAgentParentID(agentID),
			AgentType:               d.heartbeat.GetAgentType(agentID),
			ParentPID:               d.heartbeat.GetAgentPID(agentID),
			LastWhisper:             d.heartbeat.GetAgentLastWhisper(agentID),
			PrincipalID:             d.heartbeat.GetAgentPrincipalID(agentID),
		})
	}

	return instances
}

// Stop stops the daemon gracefully.
func (d *Daemon) Stop() error {
	d.mu.Lock()
	if !d.running {
		d.mu.Unlock()
		return ErrNotRunning
	}
	d.running = false // set before cancel to prevent Start() race
	cancel := d.cancel
	d.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	return nil
}

// shutdown performs graceful shutdown.
func (d *Daemon) shutdown() error {
	d.logger.Info("shutting down")

	// emit daemon.stopped event before tearing down
	if d.eventBus != nil {
		d.eventBus.Emit(context.Background(), hooks.Event{
			Name:    hooks.EventDaemonStopped,
			Project: d.config.ProjectRoot,
			RepoID:  config.GetRepoID(d.config.ProjectRoot),
		})
	}

	// record shutdown telemetry and flush before stopping
	if d.telemetry != nil {
		uptime := time.Since(d.startTime)
		d.telemetry.RecordDaemonShutdown(uptime, "graceful")
		d.telemetry.Stop() // flush and stop background sender
	}

	// stop friction collector and flush pending events
	if d.friction != nil {
		d.friction.Stop()
	}

	// flush pending trace spans before canceling context (exporter needs live HTTP)
	observability.Shutdown(context.Background())

	// close whisper registry (flush SQLite WAL before exit)
	if d.whisperRegistry != nil {
		if err := d.whisperRegistry.Close(); err != nil {
			d.logger.Warn("failed to close whisper registry", "error", err)
		}
	}

	// stop session watchers before canceling context
	if d.sessionWatcher != nil {
		d.sessionWatcher.StopAll()
	}

	// cancel context to stop all goroutines
	if d.cancel != nil {
		d.cancel()
	}

	// wait for goroutines with timeout
	done := make(chan struct{})
	go func() {
		d.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		d.logger.Info("graceful shutdown complete")
		d.cleanup() // only cleanup after successful wait
	case <-time.After(5 * time.Second):
		d.logger.Warn("shutdown timeout, forcing exit")
		// don't cleanup - let OS clean up to avoid corrupting running goroutines
		d.mu.Lock()
		d.running = false
		d.mu.Unlock()
		return ErrShutdownTimeout
	}

	d.mu.Lock()
	d.running = false
	d.mu.Unlock()

	return nil
}

// Liveness Detection: Socket Ping
//
// Claude manages the daemon process lifecycle (launching and killing), so flock-based
// locking is unnecessary. Having two daemons briefly run is harmless — one will shut
// down via inactivity timeout within 1 hour.
//
// We detect liveness by pinging the daemon over its Unix socket. PID file is kept
// as a secondary safety net for recovery scenarios (kill -9 to force-stop a hung daemon).
//
// See: docs/ai/analysis/february-2026-ipc-analysis.md

// writePidFile writes the daemon PID to a file.
func (d *Daemon) writePidFile() error {
	pidPath := PidPath()

	// 0700 = owner-only directory access
	if err := os.MkdirAll(filepath.Dir(pidPath), 0700); err != nil {
		return err
	}

	// 0600 = owner read/write only (security: prevent other users from reading)
	return os.WriteFile(pidPath, []byte(fmt.Sprintf("%d", os.Getpid())), 0600)
}

// cleanup removes PID and socket files.
// When the daemon was superseded by a new instance, skip removing the socket
// and registry entry — those now belong to the replacement daemon.
func (d *Daemon) cleanup() {
	if !d.wasSuperseded {
		if err := UnregisterDaemon(); err != nil {
			d.logger.Warn("failed to unregister daemon", "error", err)
		}
		os.Remove(SocketPath())
		os.Remove(PidPath())
	}
}

// superseded returns true if another daemon has taken over this workspace.
// Checks the registry to see if a different PID is now registered for our workspace ID.
// This handles the case where a new daemon replaces the socket file (same path, different process).
func superseded() bool {
	reg, err := LoadRegistry()
	if err != nil {
		return false // can't tell, assume not superseded
	}
	info := reg.FindByWorkspaceID(CurrentWorkspaceID())
	if info == nil {
		// Entry missing — could be a race in registry writes or a cleared file.
		// Only evict when the registry positively names a different PID.
		return false
	}
	return info.PID != os.Getpid()
}

// DaemonState describes the lifecycle state of the daemon process.
type DaemonState int

const (
	// DaemonStateStopped: no PID file, dead process, or unreadable PID.
	DaemonStateStopped DaemonState = iota
	// DaemonStateStarting: process is alive but IPC not yet ready.
	// Normal during throttled restarts (up to 2 min) or fast initial startup.
	DaemonStateStarting
	// DaemonStateStuck: process alive, IPC unreachable, past the startup window.
	// PID file is older than startupStuckThreshold — the process likely hung in init.
	DaemonStateStuck
	// DaemonStateRunning: IPC socket is up and responding to pings.
	DaemonStateRunning
)

// startupStuckThreshold is how long a process can be alive without IPC before
// it's considered stuck rather than starting. Must exceed the maximum restart
// throttle delay (2 min) plus a generous startup window.
const startupStuckThreshold = 3 * time.Minute

// resolveSocketPath returns the socket path for the current repo's running daemon.
// Tries the current workspace socket first; falls back to a registry lookup by
// repo_id to handle workspace ID drift between binary versions (e.g., when the
// workspace ID computation changes but an older daemon is still running).
func resolveSocketPath() string {
	sock := SocketPath()
	if _, err := os.Stat(sock); err == nil {
		return sock // fast path: current workspace socket exists
	}

	// Socket missing for current workspace ID — check registry for a daemon
	// registered under the same repo_id with a different workspace ID.
	// Use config.FindProjectRoot() so subdirectory invocations find the config.
	dir := config.FindProjectRoot()
	if dir == "" {
		return sock
	}
	repoID := config.GetRepoID(dir)
	if info := FindDaemonForRepo(repoID); info != nil {
		if _, err := os.Stat(info.SocketPath); err == nil {
			slog.Debug("resolved daemon via registry fallback",
				"computed_workspace", CurrentWorkspaceID(),
				"registry_workspace", info.WorkspaceID,
				"repo_id", repoID)
			return info.SocketPath
		}
	}
	return sock
}

// pidForSocket looks up the PID registered for a given socket path in the daemon registry.
// Returns 0 if the socket path is not found in the registry.
func pidForSocket(socketPath string) int {
	reg, err := LoadRegistry()
	if err != nil {
		return 0
	}
	for _, info := range reg.Daemons {
		if info.SocketPath == socketPath {
			return info.PID
		}
	}
	return 0
}

// GetState returns the current lifecycle state of the daemon.
// This is the canonical way to check daemon status — prefer it over
// the boolean helpers IsRunning/IsStarting, which are thin wrappers.
func GetState() DaemonState {
	// IPC ping is the authoritative check for a fully-running daemon.
	// Uses resolveSocketPath to handle workspace ID drift across binary versions.
	socketPath := resolveSocketPath()
	client := &Client{socketPath: socketPath, timeout: 2 * time.Second}
	if client.Ping() == nil {
		return DaemonStateRunning
	}

	// Ping failed. If the socket file exists AND the owning process is still alive,
	// the daemon is running but temporarily unable to respond (busy with GC, large sync).
	// Don't classify as stuck based on a timeout alone.
	// Cross-check with the registry: a stale socket from an ungraceful exit is NOT running.
	if _, err := os.Stat(socketPath); err == nil {
		if pid := pidForSocket(socketPath); pid > 0 {
			if proc, pErr := os.FindProcess(pid); pErr == nil {
				if proc.Signal(syscall.Signal(0)) == nil {
					return DaemonStateRunning
				}
				// Registry positively identified a dead owner — safe to remove stale socket.
				_ = os.Remove(socketPath)
			}
		}
		// pid == 0 means registry entry missing or unreadable — don't remove the socket
		// since we can't confirm the owner is gone; treat as unknown state.
	}

	// No socket — check whether a process is alive via PID file.
	data, err := os.ReadFile(PidPath())
	if err != nil {
		return DaemonStateStopped
	}
	var pid int
	if _, err := fmt.Sscanf(string(data), "%d", &pid); err != nil {
		return DaemonStateStopped
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return DaemonStateStopped
	}
	if proc.Signal(syscall.Signal(0)) != nil {
		return DaemonStateStopped // process is dead
	}

	// Process is alive but no socket yet (still initializing).
	// Use PID file mtime to distinguish legitimate startup from stuck.
	if info, err := os.Stat(PidPath()); err == nil {
		if time.Since(info.ModTime()) > startupStuckThreshold {
			return DaemonStateStuck
		}
	}
	return DaemonStateStarting
}

// IsRunning checks if the daemon is fully running and responsive to IPC.
func IsRunning() bool { return GetState() == DaemonStateRunning }

// IsStarting checks if a daemon process exists but is not yet responding to IPC.
// Returns true for both Starting and Stuck states — callers that need to distinguish
// them should call GetState() directly.
func IsStarting() bool {
	s := GetState()
	return s == DaemonStateStarting || s == DaemonStateStuck
}

// initComponents creates and wires all daemon subsystems. Returns setup duration.
func (d *Daemon) initComponents() time.Duration {
	startSetup := time.Now()

	// load project endpoint early - needed by friction collector and credential loading
	var projectEndpoint string
	if projectCfg, err := config.LoadProjectConfig(d.config.ProjectRoot); err == nil && projectCfg != nil {
		projectEndpoint = projectCfg.GetEndpoint()
	}

	// clean up stale git lock files left by crashed processes before starting sync
	if d.config.LedgerPath != "" {
		gitDir := filepath.Join(d.config.LedgerPath, ".git")
		if removed, _ := gitutil.RemoveStaleLockFiles(gitDir); len(removed) > 0 {
			d.logger.Info("removed stale git lock files at startup", "path", d.config.LedgerPath, "locks", removed)
		}
	}

	// ensure ledger sparse-checkout cone is correct before any sync or indexing.
	// without this, a corrupt cone from a previous session causes every sync cycle
	// to wipe .sageox/cache/ via ConfigureSparseCheckout's rolling-window refresh.
	if d.config.LedgerPath != "" && pathIsGitRepo(d.config.LedgerPath) {
		if err := ledger.ConfigureSparseCheckout(d.config.LedgerPath); err != nil {
			d.logger.Warn("failed to set ledger sparse-checkout at startup", "error", err)
		} else {
			d.logger.Info("ledger sparse-checkout verified at startup")
		}
	}

	// telemetry + friction collectors
	d.telemetry = NewTelemetryCollector(d.logger)
	d.startTime = time.Now()
	d.friction = NewFrictionCollector(d.logger, projectEndpoint)

	// OTel tracing for per-task operational visibility (traces → Honeycomb).
	// Separate from the TelemetryCollector which handles product events.
	if isTelemetryEnabled() && projectEndpoint != "" {
		apiEndpoint := endpoint.GetForProject(d.config.ProjectRoot)
		daemonAttrs := []attribute.KeyValue{
			attribute.String("client.id", d.telemetry.clientID),
			attribute.String("client.class", "daemon"),
			attribute.String("service.version", version.Version),
			attribute.String("os.type", runtime.GOOS),
			attribute.String("host.arch", runtime.GOARCH),
			// Mirror the CLI attribute keys so dashboards can query
			// "ox.version" and "host.os" uniformly across ox-cli and
			// ox-daemon traces. The legacy service.version / os.type
			// keys above are kept to avoid breaking existing queries.
			attribute.String(observability.AttrOXVersion, version.Version),
			attribute.String(observability.AttrHostOS, runtime.GOOS),
		}
		// AGENT_ENV is inherited from the parent process — typically the
		// AI coworker that spawned the daemon via `ox agent prime`. Tag
		// daemon traces with it so background work is attributable to
		// the adapter that triggered it.
		if env := os.Getenv("AGENT_ENV"); env != "" {
			daemonAttrs = append(daemonAttrs, attribute.String(observability.AttrAgentEnv, env))
		}
		if err := observability.Init(d.ctx, "ox-daemon", apiEndpoint, daemonAttrs...); err != nil {
			d.logger.Warn("otel tracing init failed", "error", err)
		}
		d.tracer = observability.NewDaemonTracer()
	}

	// health check + notification infrastructure
	d.issues = NewIssueTracker()

	// whisper store for persistent agent signal delivery
	repoID := config.GetRepoID(d.config.ProjectRoot)
	if repoID != "" && projectEndpoint != "" {
		whisperDBPath := filepath.Join(paths.WhisperDBDir(repoID, projectEndpoint), "whisper.db")
		ledgerWhisperStore, err := whisperstore.Open(whisperDBPath)
		if err != nil {
			d.logger.Warn("failed to open whisper store", "error", err)
		} else {
			d.whisperRegistry = NewWhisperRegistry(ledgerWhisperStore, d.logger)
			d.whisperRegistry.Prune(24 * time.Hour)
			d.whisperRegistry.EnforceMaxSize(10 * 1024 * 1024) // 10MB
			d.logger.Info("whisper registry initialized", "path", whisperDBPath)
		}
	}

	// heartbeat handler
	d.heartbeat = NewHeartbeatHandler(d.logger)
	d.heartbeat.SetActivityCallback(d.recordActivity)
	d.heartbeat.SetTeamNeededCallback(func(teamID string) {
		d.logger.Debug("team context needed", "team_id", teamID)
	})
	d.heartbeat.SetVersionMismatchCallback(func(cliVersion, daemonVersion string) {
		d.logger.Info("restarting due to version mismatch",
			"cli_version", cliVersion,
			"daemon_version", daemonVersion,
		)
		d.mu.Lock()
		d.restartRequested = true
		d.mu.Unlock()
		go d.Stop()
	})
	// pre-populate credentials from credential store (cold-start)
	if creds, err := gitserver.LoadCredentialsForEndpoint(projectEndpoint); err == nil && creds != nil {
		hbCreds := &HeartbeatCreds{
			Token:     creds.Token,
			ServerURL: creds.ServerURL,
			ExpiresAt: creds.ExpiresAt,
		}
		if token, err := auth.GetTokenForEndpoint(projectEndpoint); err == nil && token != nil {
			hbCreds.AuthToken = token.AccessToken
		}
		d.heartbeat.SetInitialCredentials(hbCreds)
	}

	// event bus for hooks (must init before wiring to scheduler/relay)
	d.eventBus = hooks.New(d.logger)
	hookCfgs, err := hooks.LoadHooks()
	if err != nil {
		d.logger.Warn("failed to load hooks config", "error", err)
	}
	if len(hookCfgs) > 0 {
		runner := hooks.NewHookRunner(hookCfgs, d.logger)
		d.eventBus.SetHookDispatch(runner.Dispatch)
	}

	// sync scheduler
	d.scheduler = NewSyncScheduler(d.config, d.logger)
	d.scheduler.SetTracer(d.tracer)
	d.scheduler.SetEventBus(d.eventBus)

	// code index manager
	if d.config.ProjectRoot != "" {
		d.codedb = NewCodeDBManager(d.config.ProjectRoot, d.logger, d.telemetry)
		d.heartbeat.SetCallerPathCallback(func(path string) {
			d.codedb.UpdateProjectRoot(path)
		})
	}

	// agent work manager
	if d.config.LedgerPath != "" {
		agentWorkSignal := make(chan struct{}, 1)
		configLoader := func() *config.AgentWorkerConfig {
			cfg, err := config.LoadUserConfig()
			if err != nil {
				d.logger.Debug("failed to load user config for agent worker", "error", err)
				return (&config.AgentWorkerConfig{}).WithDefaults()
			}
			awCfg := cfg.GetAgentWorkerConfig()
			if awCfg == nil {
				return (&config.AgentWorkerConfig{}).WithDefaults()
			}
			return awCfg
		}
		initialCfg := configLoader()
		resolved := agentwork.ResolveAgent(initialCfg.GetAgent())
		runner := agentwork.NewRunner(resolved, d.logger)
		d.agentWorker = agentwork.NewManager(runner, d.logger, configLoader, agentWorkSignal, d.config.LedgerPath)
		sfh := agentwork.NewSessionFinalizeHandler(d.logger)
		sfh.SetPIDLookup(d.heartbeat.GetAgentPID)
		sfh.SetLedgerMu(d.scheduler.LedgerMu())
		sfh.SetProjectRoot(d.config.ProjectRoot)
		// Wire the LLM-as-judge completer to re-use the same Runner the
		// summarizer path uses. Activation is still gated at call time
		// by OX_SUMMARY_JUDGE=on — configuring the completer unconditionally
		// here keeps the daemon ready for per-run judging without paying
		// any LLM cost until operators flip the env switch.
		sfh.SetJudgeCompleter(agentwork.NewRunnerCompleter(runner))
		// Supply the daemon's root context so judge work cancels promptly
		// on daemon shutdown instead of blocking up to its 3-minute
		// deadline and triggering ErrShutdownTimeout.
		sfh.SetDaemonContext(d.ctx)
		awCfg := configLoader()
		sfh.SetQualityThresholds(awCfg.GetQualityUploadThreshold(), awCfg.GetQualityDiscardThreshold())
		d.sessionFinalizeHandler = sfh
		d.agentWorker.RegisterHandler(sfh)
		d.agentWorker.SetOnComplete(func(result agentwork.WorkResult) {
			status := "success"
			if !result.Success {
				status = "failed"
			}
			d.logger.Info("agent work complete",
				"type", result.Item.Type,
				"status", status,
				"duration", result.Duration,
			)
			if d.telemetry != nil && result.Item.Type == "session-finalize" {
				d.telemetry.Record("session:finalize", map[string]any{
					"status":      status,
					"duration_ms": result.Duration.Milliseconds(),
				})
			}
		})
		d.scheduler.SetAgentWorkSignal(agentWorkSignal)
	}

	// session watcher for tail-mode recordings (hookless agents like Codex)
	d.sessionWatcher = agentwork.NewSessionWatcherManager(d.logger)
	if d.agentWorker != nil {
		d.agentWorker.SetSessionWatcher(d.sessionWatcher)
	}

	// wire cross-component dependencies
	d.scheduler.SetAuthTokenGetter(d.heartbeat.GetAuthToken)
	d.friction.SetAuthTokenGetter(d.heartbeat.GetAuthToken)
	d.scheduler.SetIssueTracker(d.issues)
	if d.whisperRegistry != nil {
		d.scheduler.SetWhisperRegistry(d.whisperRegistry)
		// always wire relay + nudge tracker; they re-check MurmuringEnabled()
		// on every tick so config changes take effect without daemon restart
		murmurRelay := NewMurmurRelay(d.whisperRegistry, d.config.ProjectRoot, d.logger)
		d.scheduler.SetMurmurRelay(murmurRelay)
		murmurRelay.SetEventBus(d.eventBus)
	}
	if d.codedb != nil {
		d.codedb.SetIssueTracker(d.issues)
		d.scheduler.SetCodeDBManager(d.codedb)
	}
	if d.config.ProjectRoot != "" {
		githubSync := NewGitHubSyncManager(d.config.ProjectRoot, d.scheduler.LedgerMu(), d.logger)
		githubSync.SetIssueTracker(d.issues)
		if d.codedb != nil {
			githubSync.SetCodeDBManager(d.codedb)
		}
		d.scheduler.SetGitHubSyncManager(githubSync)
	}
	d.scheduler.SetTelemetryCallback(func(syncType, operation, status string, duration time.Duration) {
		if d.telemetry != nil {
			d.telemetry.RecordSyncComplete(syncType, operation, status, duration, 0)
		}
	})

	// settings fetcher — background polling for CLI feature flags from cloud API
	if projectEndpoint != "" {
		d.settingsFetcher = NewSettingsFetcher(d.logger, projectEndpoint)
		d.settingsFetcher.SetAuthTokenGetter(d.heartbeat.GetAuthToken)
		d.scheduler.SetSettingsFetcher(d.settingsFetcher)
	}

	return time.Since(startSetup)
}

// startWorkers launches all background goroutines (IPC server, scheduler,
// whisper, watcher, agent work, code index freshness).
func (d *Daemon) startWorkers() {
	d.telemetry.Start()
	d.friction.Start()
	d.telemetry.RecordDaemonStartup()

	// emit daemon.started event
	if d.eventBus != nil {
		d.eventBus.Emit(d.ctx, hooks.Event{
			Name:    hooks.EventDaemonStarted,
			Project: d.config.ProjectRoot,
			RepoID:  config.GetRepoID(d.config.ProjectRoot),
		})
	}

	// initialize whisper sources before IPC server starts, so pause/resume
	// handlers can safely read d.murmurNudgeSource without a data race
	if d.whisperRegistry != nil {
		ws := NewWhisperScheduler(d.whisperRegistry, d.logger)
		ws.RegisterSource(NewActivitySummarySource(d.heartbeat, d.scheduler))
		if d.config.MurmurNudgeInterval > 0 {
			d.murmurNudgeSource = NewMurmurNudgeSource(d.whisperRegistry.LedgerStore(), d.heartbeat, d.config.MurmurNudgeInterval, d.config.ProjectRoot)
			ws.RegisterSource(d.murmurNudgeSource)
		}
		if d.config.RecordingReminderInterval > 0 {
			src := NewRecordingReminderSource(
				d.whisperRegistry.LedgerStore(), d.heartbeat,
				d.config.RecordingReminderInterval, d.config.ProjectRoot,
			)
			if d.config.RecordingReminderTick > 0 {
				src.SetTick(d.config.RecordingReminderTick)
			}
			ws.RegisterSource(src)
		}
		if d.config.ProjectRoot != "" && d.config.LedgerPath != "" {
			accumulator := NewChangeAccumulator(3 * time.Second)
			tracker := NewGitTrackedMatcher(d.config.ProjectRoot, d.logger)
			d.projectWatcher = NewProjectWatcher(
				d.config.ProjectRoot, d.logger,
				DefaultWatcherFactory, &RealFileSystem{},
				accumulator, tracker,
			)
			murmurPub := NewFileChangeMurmurPublisher(
				accumulator, d.service,
				d.config.LedgerPath, d.config.ProjectRoot,
				d.logger,
			)
			murmurPub.SetAgentResolver(&heartbeatAgentResolver{heartbeat: d.heartbeat})
			// wire fsnotify-triggered dirty overlay rebuilds to codedb
			if d.codedb != nil {
				debouncer := NewDirtyOverlayDebouncer(d.codedb, d.logger)
				debouncer.Start(d.ctx)
				accumulator.SetOnSettled(debouncer.OnSettled)
			}
			d.wg.Add(2)
			go func() {
				defer d.wg.Done()
				d.projectWatcher.Start(d.ctx)
			}()
			go func() {
				defer d.wg.Done()
				murmurPub.Start(d.ctx)
			}()
		}
		ws.Start(d.ctx, &d.wg)
	}

	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		if err := d.server.Start(d.ctx); err != nil && !errors.Is(err, context.Canceled) {
			d.logger.Error("server error", "error", err)
		}
	}()
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		d.scheduler.Start(d.ctx)
	}()

	// unified DB maintenance: prune, vacuum, integrity check for all SQLite databases
	d.dbMaintenance = NewDBMaintenanceScheduler(d.logger)
	if d.whisperRegistry != nil {
		d.dbMaintenance.Register(NewWhisperDBMaintainer(
			"whisper", d.whisperRegistry, 24*time.Hour, 10*1024*1024))
	}
	if d.codedb != nil {
		d.dbMaintenance.Register(NewCodeDBMaintainer("codedb", d.codedb))
	}
	d.dbMaintenance.Start(d.ctx, &d.wg, 1*time.Hour)

	if d.config.LedgerPath != "" {
		d.watcher = NewWatcher(d.config.LedgerPath, d.config.DebounceWindow, d.logger)
		d.wg.Add(1)
		go func() {
			defer d.wg.Done()
			d.watcher.Start(d.ctx, func() {
				d.recordActivity()
				d.scheduler.TriggerSync()
			})
		}()
	}

	if d.agentWorker != nil {
		d.wg.Add(1)
		go func() {
			defer d.wg.Done()
			d.agentWorker.Start(d.ctx)
		}()
	}

	if d.codedb != nil {
		go d.codedb.CheckFreshness(d.ctx)
	}
}

// daemonServiceImpl implements DaemonService on top of *Daemon.
// All methods guard against nil component fields because the server is created
// before components finish initializing — connections only arrive after Start()
// completes, so nil guards are defensive rather than reachable in practice.
type daemonServiceImpl struct{ d *Daemon }

func (s *daemonServiceImpl) Sync() error {
	return s.d.scheduler.Sync()
}

func (s *daemonServiceImpl) SyncWithProgress(progress *ProgressWriter) error {
	return s.d.scheduler.SyncWithProgress(progress)
}

func (s *daemonServiceImpl) TeamSync(progress *ProgressWriter) error {
	return s.d.scheduler.TeamSync(progress)
}

func (s *daemonServiceImpl) SyncHistory() []SyncEvent {
	return s.d.scheduler.SyncHistory()
}

func (s *daemonServiceImpl) Status() *StatusData {
	lastErr, lastErrTime := s.d.scheduler.LastError()
	lastErrTimeStr := ""
	if !lastErrTime.IsZero() {
		lastErrTimeStr = lastErrTime.Format(time.RFC3339)
	}
	stats := s.d.scheduler.SyncStats()

	// prefer most recent caller path from heartbeats (stays fresh across clones)
	// fall back to config.ProjectRoot (the clone that started the daemon)
	workspacePath := s.d.config.ProjectRoot
	if s.d.heartbeat != nil {
		if callerPath := s.d.heartbeat.LastCallerPath(); callerPath != "" {
			workspacePath = callerPath
		}
	}

	var activitySummary *ActivitySummary
	if s.d.heartbeat != nil {
		summary := s.d.heartbeat.GetActivitySummary()
		activitySummary = &summary
	}

	var authUser *AuthenticatedUser
	if s.d.heartbeat != nil {
		authUser = s.d.heartbeat.GetAuthenticatedUser()
	}

	var callers []CallerInfo
	if s.d.heartbeat != nil {
		callers = s.d.heartbeat.GetCallers()
	}

	var issues []DaemonIssue
	needsHelp := false
	if s.d.issues != nil {
		issues = s.d.issues.GetIssues()
		needsHelp = s.d.issues.NeedsHelp()
	}

	var codeDBStats *CodeDBStats
	if s.d.codedb != nil {
		st := s.d.codedb.Stats()
		codeDBStats = &st
	}

	var agentWorkStatus *agentwork.AgentWorkStatus
	if s.d.agentWorker != nil {
		st := s.d.agentWorker.Status()
		agentWorkStatus = &st
	}

	// collect all workspaces being synced, keyed by type
	workspaces := make(map[string][]WorkspaceSyncStatus)
	projectTeamID := ""
	if registry := s.d.scheduler.WorkspaceRegistry(); registry != nil {
		projectTeamID = registry.ProjectTeamID()
		for _, ws := range registry.GetAllWorkspaces() {
			wsType := string(ws.Type)
			// normalize type to match API convention (team_context -> team-context)
			if wsType == "team_context" {
				wsType = "team-context"
			}
			workspaces[wsType] = append(workspaces[wsType], WorkspaceSyncStatus{
				ID:             ws.ID,
				Type:           wsType,
				Path:           ws.Path,
				CloneURL:       ws.CloneURL,
				Exists:         ws.Exists,
				TeamID:         ws.TeamID,
				TeamName:       ws.TeamName,
				TeamSlug:       ws.TeamSlug,
				LastSync:       ws.ConfigLastSync,
				LastErr:        ws.LastErr,
				Syncing:        ws.SyncInProgress,
				LastGCTime:     ws.LastGCTime,
				GCIntervalDays: ws.GCIntervalDays,
			})
		}
	}

	return &StatusData{
		Running:            true,
		Pid:                os.Getpid(),
		Version:            Version(),
		Uptime:             time.Since(s.d.startTime),
		WorkspacePath:      workspacePath,
		LedgerPath:         s.d.config.LedgerPath,
		LastSync:           s.d.scheduler.LastSync(),
		SyncIntervalRead:   s.d.config.SyncIntervalRead,
		RecentErrorCount:   s.d.scheduler.RecentErrorCount(),
		LastError:          lastErr,
		LastErrorTime:      lastErrTimeStr,
		TotalSyncs:         stats.TotalSyncs,
		SyncsLastHour:      stats.SyncsLastHour,
		AvgSyncTime:        stats.AvgDuration,
		Workspaces:         workspaces,
		ProjectTeamID:      projectTeamID,
		TeamContexts:       s.d.scheduler.TeamContextStatus(),
		InactivityTimeout:  s.d.config.InactivityTimeout,
		TimeSinceActivity:  s.d.timeSinceLastActivity(),
		Activity:           activitySummary,
		AuthenticatedUser:  authUser,
		NeedsHelp:          needsHelp,
		Issues:             issues,
		StartupDurationMs:  s.d.startupDurationMs.Load(),
		ThrottleDurationMs: s.d.throttleDurationMs.Load(),
		CodeDB:             codeDBStats,
		AgentWork:          agentWorkStatus,
		Callers:            callers,
	}
}

func (s *daemonServiceImpl) SettingsGet() *flags.CLISettingsResponse {
	if s.d.settingsFetcher == nil {
		return nil
	}
	return s.d.settingsFetcher.CachedSettings()
}

func (s *daemonServiceImpl) GetErrors() []StoredError {
	// error store not yet wired; returns nil (handler sends empty array)
	return nil
}

func (s *daemonServiceImpl) Sessions() []AgentSession {
	return s.d.getAgentSessions()
}

func (s *daemonServiceImpl) Instances() []InstanceInfo {
	return s.d.getAgentInstances()
}

func (s *daemonServiceImpl) Whispers(agentID string, attention whisperstore.Attention, topics []string) ([]whisperstore.WhisperEntry, error) {
	if s.d.whisperRegistry == nil {
		return nil, nil
	}
	entries, err := s.d.whisperRegistry.GetWhispers(agentID, attention, topics)
	if err == nil && len(entries) > 0 && agentID != "" && s.d.heartbeat != nil {
		s.d.heartbeat.RecordWhisperDelivery(agentID)
	}
	return entries, err
}

func (s *daemonServiceImpl) WhisperHistory(agentID string, before time.Time, limit int) (*WhisperHistoryResponse, error) {
	if s.d.whisperRegistry == nil {
		return &WhisperHistoryResponse{Entries: []whisperstore.WhisperEntry{}}, nil
	}
	entries, hasMore, err := s.d.whisperRegistry.GetWhispersPage(agentID, before, limit)
	if err != nil {
		return nil, err
	}
	cursor, err := s.d.whisperRegistry.GetCursor(agentID)
	if err != nil {
		return nil, fmt.Errorf("get cursor: %w", err)
	}
	resp := &WhisperHistoryResponse{
		Entries:   entries,
		Cursor:    cursor,
		HasCursor: !cursor.IsZero(),
		HasMore:   hasMore,
	}
	if hasMore && len(entries) > 0 {
		// oldest entry in this page is the cursor for the next page
		resp.NextCursor = entries[len(entries)-1].CreatedAt
	}
	return resp, nil
}

func (s *daemonServiceImpl) CodeStatus() *CodeDBStats {
	if s.d.codedb == nil {
		return nil
	}
	st := s.d.codedb.Stats()
	return &st
}

func (s *daemonServiceImpl) Stop() {
	s.d.Stop() //nolint:errcheck
}

func (s *daemonServiceImpl) Checkout(payload CheckoutPayload, progress *ProgressWriter) (*CheckoutResult, error) {
	return s.d.scheduler.Checkout(payload, progress)
}

func (s *daemonServiceImpl) MarkErrors(ids []string) {
	// error store not yet wired; no-op
	_ = ids
}

func (s *daemonServiceImpl) TriggerGC() *TriggerGCResponse {
	return s.d.scheduler.TriggerGC(s.d.ctx)
}

func (s *daemonServiceImpl) CodeIndex(payload CodeIndexPayload, progress *ProgressWriter) (*CodeIndexResult, error) {
	if s.d.codedb == nil {
		return nil, nil
	}
	result, err := s.d.codedb.Index(s.d.ctx, payload, progress)
	if s.d.telemetry != nil && result != nil {
		status := "success"
		if err != nil {
			status = "error"
		}
		s.d.telemetry.RecordCodeIndexComplete(result, status)
	}
	return result, err
}

func (s *daemonServiceImpl) Doctor() *DoctorResponse {
	// trigger anti-entropy (self-healing for missing repos)
	s.d.scheduler.TriggerAntiEntropy()
	resp := &DoctorResponse{AntiEntropyTriggered: true}
	// trigger session finalization detection (bypasses hourly cooldown)
	if s.d.agentWorker != nil {
		queued := s.d.agentWorker.ForceDetect()
		resp.SessionFinalizeTriggered = true
		resp.SessionFinalizeQueued = queued
	}
	// restart tail-mode watchers for recordings that lost their watcher (daemon restart)
	// and clean up watchers for sessions that have been stopped or orphaned
	if s.d.sessionWatcher != nil {
		s.d.sessionWatcher.DetectAndRestart(s.d.config.LedgerPath)
		s.d.sessionWatcher.Cleanup()
	}
	return resp
}

func (s *daemonServiceImpl) SessionFinalize(payload SessionFinalizeIPCPayload) {
	if s.d.agentWorker == nil {
		s.d.logger.Warn("session_finalize received but agent worker not initialized")
		return
	}
	s.d.logger.Info("session_finalize received, enqueueing",
		"session", payload.SessionName,
		"ledger", payload.LedgerPath,
	)
	// prefer CachePath from the stop hook (actual session location under .sageox/cache/sessions/)
	// fall back to ledger-derived path for backwards compatibility
	sessionDir := filepath.Join(payload.LedgerPath, "sessions", payload.SessionName)
	if payload.CachePath != "" {
		// validate CachePath is under the ledger to prevent path traversal from IPC
		absCache, cacheErr := filepath.Abs(payload.CachePath)
		absLedger, ledgerErr := filepath.Abs(payload.LedgerPath)
		if cacheErr == nil && ledgerErr == nil && strings.HasPrefix(absCache, absLedger+string(filepath.Separator)) {
			sessionDir = payload.CachePath
		} else {
			s.d.logger.Warn("ignoring untrusted CachePath from IPC",
				"cache_path", payload.CachePath, "ledger", payload.LedgerPath)
		}
	}

	// defense in depth: reject header-only sessions before enqueueing.
	// the primary guard is in processAgentSession (CLI), but this prevents any
	// empty session from reaching the LLM and being committed to the ledger.
	rawPath := filepath.Join(sessionDir, "raw.jsonl")
	if !session.HasSubstantiveEntries(rawPath) {
		s.d.logger.Info("session_finalize skipped: header-only raw.jsonl, nothing to upload",
			"session", payload.SessionName)
		return
	}

	s.d.agentWorker.Enqueue(&agentwork.WorkItem{
		Type:     "session-finalize",
		Priority: 1, // high priority (vs 10 for doctor-detected)
		DedupKey: "session-finalize:" + payload.SessionName,
		Payload: &agentwork.SessionFinalizePayload{
			SessionDir: sessionDir,
			RawPath:    filepath.Join(sessionDir, "raw.jsonl"),
			Missing:    []string{"summary.md", "summary.json", "session.html", "session.md"},
			LedgerPath: payload.LedgerPath,
		},
	})
}

func (s *daemonServiceImpl) SessionWatchStart(payload SessionWatchStartPayload) {
	if s.d.sessionWatcher == nil {
		s.d.logger.Warn("session_watch_start received but session watcher not initialized")
		return
	}
	// derive paths server-side; never trust client-supplied destinations
	ledgerPath := s.d.config.LedgerPath
	cachePath := filepath.Join(ledgerPath, "sessions", payload.SessionName)
	if err := s.d.sessionWatcher.StartWatch(
		payload.SessionName, payload.SessionFile,
		payload.AdapterName, ledgerPath, cachePath,
	); err != nil {
		s.d.logger.Error("failed to start session watcher",
			"session", payload.SessionName, "error", err)
	}
}

func (s *daemonServiceImpl) SessionWatchStop(payload SessionWatchStopPayload) {
	if s.d.sessionWatcher == nil {
		s.d.logger.Warn("session_watch_stop received but session watcher not initialized")
		return
	}
	s.d.sessionWatcher.StopWatch(payload.SessionName)
}

func (s *daemonServiceImpl) Activity() {
	s.d.recordActivity()
}

func (s *daemonServiceImpl) Heartbeat(callerID string, payload json.RawMessage) {
	if s.d.heartbeat != nil {
		s.d.heartbeat.Handle(callerID, payload)
	}
}

func (s *daemonServiceImpl) Telemetry(payload json.RawMessage) {
	if s.d.telemetry == nil {
		return
	}
	var p TelemetryPayload
	if err := json.Unmarshal(payload, &p); err == nil {
		s.d.telemetry.RecordFromIPC(p.Event, p.Props)
	}
}

func (s *daemonServiceImpl) Friction(payload FrictionPayload) {
	if s.d.friction != nil {
		s.d.friction.RecordFromIPC(payload)
	}
}

// PublishMurmur writes the murmur file to disk and commits it asynchronously.
// The CLI passes the full MurmurFile JSON so no temp file is needed on the CLI side.
// Runs in a goroutine so the IPC handler returns immediately.
func (s *daemonServiceImpl) PublishMurmur(payload MurmurPayload) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		// write the murmur file to disk (daemon owns all I/O)
		if err := ledger.WriteMurmurRaw(payload.TargetDir, payload.RelPath, payload.MurmurJSON); err != nil {
			s.d.logger.Warn("murmur write failed", "error", err, "rel_path", payload.RelPath)
			return
		}

		if _, err := gitutil.RunGit(ctx, payload.TargetDir, "add", "--sparse", "data/murmurs/"); err != nil {
			s.d.logger.Warn("murmur git add failed", "error", err, "target_dir", payload.TargetDir)
			return
		}

		summary := payload.Content
		if len(summary) > 50 {
			summary = summary[:50] + "..."
		}
		// scope commit to data/murmurs/ — a bare `git commit` would sweep in any
		// other dirty index entries (e.g., session pointer stubs written by a
		// previous finalize), poisoning the push queue with LFS pointer blobs
		// whose backing objects may not be in the remote LFS store.
		if _, err := gitutil.RunGit(ctx, payload.TargetDir, "commit", "-m", fmt.Sprintf("murmur: %s", summary), "--", "data/murmurs/"); err != nil {
			s.d.logger.Warn("murmur git commit failed", "error", err, "target_dir", payload.TargetDir)
			return
		}

		s.d.logger.Debug("murmur written and committed", "rel_path", payload.RelPath)
	}()
}

func (s *daemonServiceImpl) PauseMurmuring(agentID string) {
	if s.d.murmurNudgeSource != nil {
		s.d.murmurNudgeSource.PauseAgent(agentID)
		s.d.logger.Debug("murmur nudging paused", "agent_id", agentID)
	}
}

func (s *daemonServiceImpl) ResumeMurmuring(agentID string) {
	if s.d.murmurNudgeSource != nil {
		s.d.murmurNudgeSource.ResumeAgent(agentID)
		s.d.logger.Debug("murmur nudging resumed", "agent_id", agentID)
	}
}

func (s *daemonServiceImpl) SessionUploaded(name, url, agentID string, dur time.Duration) {
	if s.d.eventBus != nil {
		s.d.eventBus.Emit(context.Background(), hooks.Event{
			Name:    hooks.EventSessionUploaded,
			Project: s.d.config.ProjectRoot,
			RepoID:  config.GetRepoID(s.d.config.ProjectRoot),
			Payload: hooks.SessionUploadedPayload(name, url, agentID, dur),
		})
	}
}

