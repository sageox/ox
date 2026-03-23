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
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/daemon/agentwork"
	"github.com/sageox/ox/internal/gitserver"
	"github.com/sageox/ox/internal/paths"
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
	server          *Server
	scheduler       *SyncScheduler
	watcher         *Watcher
	heartbeat       *HeartbeatHandler
	telemetry       *TelemetryCollector
	friction        *FrictionCollector
	issues          *IssueTracker
	codedb          *CodeDBManager
	agentWorker     *agentwork.Manager
	whisperRegistry *WhisperRegistry

	// state
	mu               sync.Mutex
	running          bool
	restartRequested bool      // set when version mismatch triggers restart
	startTime        time.Time // daemon start time for uptime tracking
	lastActivity     time.Time // tracks last activity for inactivity timeout

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
	workspacePath := d.config.ProjectRoot
	if workspacePath == "" {
		workspacePath, _ = os.Getwd()
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
	d.server = NewServerWithService(d.logger, &daemonServiceImpl{d})

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

	for {
		select {
		case sig := <-sigChan:
			d.logger.Info("received signal", "signal", sig)
			return d.shutdown()
		case <-d.ctx.Done():
			d.logger.Info("context canceled")
			return d.shutdown()
		case <-inactivityChan:
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
				d.logger.Info("shutting down due to inactivity", "inactive_duration", inactiveDuration, "timeout", d.config.InactivityTimeout, "uptime", uptime)
				return d.shutdown()
			}
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

	// get workspace path for this daemon
	workspacePath := d.config.ProjectRoot
	if workspacePath == "" {
		workspacePath, _ = os.Getwd()
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

// getAgentInstances returns active agent instances from the heartbeat handler.
// Converts the activity tracker data into InstanceInfo structs.
func (d *Daemon) getAgentInstances() []InstanceInfo {
	if d.heartbeat == nil {
		return nil
	}

	// get workspace path for this daemon
	workspacePath := d.config.ProjectRoot
	if workspacePath == "" {
		workspacePath, _ = os.Getwd()
	}

	tracker := d.heartbeat.GetAgentActivity()
	keys := tracker.Keys()
	instances := make([]InstanceInfo, 0, len(keys))

	now := time.Now()

	for _, agentID := range keys {
		last := tracker.Last(agentID)
		count := tracker.Count(agentID)

		elapsed := now.Sub(last)

		// skip stale instances (no heartbeat in >5min) — they're dead sessions
		if elapsed > StaleThreshold {
			continue
		}

		status := StatusActive
		if elapsed > IdleThreshold {
			status = StatusIdle
		}

		// instant liveness check: if PID is known and process is dead, mark as exited
		agentPID := d.heartbeat.GetAgentPID(agentID)
		if agentPID > 0 {
			proc, procErr := os.FindProcess(agentPID)
			if procErr != nil || proc.Signal(syscall.Signal(0)) != nil {
				status = StatusExited
			}
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

	// close whisper registry (flush SQLite WAL before exit)
	if d.whisperRegistry != nil {
		if err := d.whisperRegistry.Close(); err != nil {
			d.logger.Warn("failed to close whisper registry", "error", err)
		}
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
func (d *Daemon) cleanup() {
	// unregister from daemon registry
	if err := UnregisterDaemon(); err != nil {
		d.logger.Warn("failed to unregister daemon", "error", err)
	}

	os.Remove(PidPath())
	os.Remove(SocketPath())
}

// IsRunning checks if a daemon is currently running and responsive.
// Uses socket-based ping detection. Claude manages the daemon process lifecycle,
// so flock-based locking is no longer needed.
func IsRunning() bool {
	client := NewClientWithTimeout(500 * time.Millisecond)
	return client.Ping() == nil
}

// IsStarting checks if a daemon process exists (PID file with live process)
// but is not yet responding to IPC. This happens during startup throttling
// or initial setup before the IPC socket is ready.
func IsStarting() bool {
	data, err := os.ReadFile(PidPath())
	if err != nil {
		return false
	}
	var pid int
	if _, err := fmt.Sscanf(string(data), "%d", &pid); err != nil {
		return false
	}
	// check if process is alive (signal 0 = no signal, just check existence)
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// on Unix, FindProcess always succeeds; use Signal(0) to check liveness
	return proc.Signal(syscall.Signal(0)) == nil
}

// initComponents creates and wires all daemon subsystems. Returns setup duration.
func (d *Daemon) initComponents() time.Duration {
	startSetup := time.Now()

	// load project endpoint early - needed by friction collector and credential loading
	var projectEndpoint string
	if projectCfg, err := config.LoadProjectConfig(d.config.ProjectRoot); err == nil && projectCfg != nil {
		projectEndpoint = projectCfg.GetEndpoint()
	}

	// telemetry + friction collectors
	d.telemetry = NewTelemetryCollector(d.logger)
	d.startTime = time.Now()
	d.friction = NewFrictionCollector(d.logger, projectEndpoint)

	// health check + notification infrastructure
	d.issues = NewIssueTracker()

	// whisper store for persistent agent signal delivery
	if config.FeatureWhisperEnabled() {
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
			}
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

	// sync scheduler
	d.scheduler = NewSyncScheduler(d.config, d.logger)

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
		runner := agentwork.NewClaudeRunner(d.logger)
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
		d.agentWorker = agentwork.NewManager(runner, d.logger, configLoader, agentWorkSignal, d.config.LedgerPath)
		sfh := agentwork.NewSessionFinalizeHandler(d.logger)
		sfh.SetPIDLookup(d.heartbeat.GetAgentPID)
		sfh.SetLedgerMu(d.scheduler.LedgerMu())
		awCfg := configLoader()
		sfh.SetQualityThresholds(awCfg.GetQualityUploadThreshold(), awCfg.GetQualityDiscardThreshold())
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
		})
		d.scheduler.SetAgentWorkSignal(agentWorkSignal)
	}

	// wire cross-component dependencies
	d.scheduler.SetAuthTokenGetter(d.heartbeat.GetAuthToken)
	d.friction.SetAuthTokenGetter(d.heartbeat.GetAuthToken)
	d.scheduler.SetIssueTracker(d.issues)
	if d.whisperRegistry != nil {
		d.scheduler.SetWhisperRegistry(d.whisperRegistry)
		murmurRelay := NewMurmurRelay(d.whisperRegistry, d.logger)
		d.scheduler.SetMurmurRelay(murmurRelay)
	}
	if d.codedb != nil {
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

	return time.Since(startSetup)
}

// startWorkers launches all background goroutines (IPC server, scheduler,
// whisper, watcher, agent work, code index freshness).
func (d *Daemon) startWorkers() {
	d.telemetry.Start()
	d.friction.Start()
	d.telemetry.RecordDaemonStartup()

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

	if d.whisperRegistry != nil {
		ws := NewWhisperScheduler(d.whisperRegistry, d.logger)
		ws.RegisterSource(NewActivitySummarySource(d.heartbeat, d.scheduler))
		ws.Start(d.ctx, &d.wg)
		ws.RunPrune(d.ctx, &d.wg, 1*time.Hour)
	}

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
	return s.d.whisperRegistry.GetWhispers(agentID, attention, topics)
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
	s.d.agentWorker.Enqueue(&agentwork.WorkItem{
		Type:     "session-finalize",
		Priority: 1, // high priority (vs 10 for doctor-detected)
		DedupKey: "session-finalize:" + payload.SessionName,
		Payload: &agentwork.SessionFinalizePayload{
			SessionDir: filepath.Join(payload.LedgerPath, "sessions", payload.SessionName),
			RawPath:    filepath.Join(payload.LedgerPath, "sessions", payload.SessionName, "raw.jsonl"),
			Missing:    []string{"summary.md", "summary.json", "session.html", "session.md"},
			LedgerPath: payload.LedgerPath,
		},
	})
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
