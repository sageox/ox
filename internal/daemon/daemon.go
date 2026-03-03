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
	"syscall"
	"time"

	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/gitserver"
	"github.com/sageox/ox/internal/version"
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
	server    *Server
	scheduler *SyncScheduler
	watcher   *Watcher
	heartbeat *HeartbeatHandler
	telemetry *TelemetryCollector
	friction  *FrictionCollector
	issues    *IssueTracker

	// state
	mu           sync.Mutex
	running      bool
	startTime    time.Time // daemon start time for uptime tracking
	lastActivity time.Time // tracks last activity for inactivity timeout

	// startup timing (populated during Start())
	startupDuration  time.Duration
	throttleDuration time.Duration
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

	d.logger.Info("daemon starting", "ledger", d.config.LedgerPath, "version", Version)

	startSetup := time.Now()

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

	// start IPC server
	d.server = NewServer(d.logger)

	// load project endpoint early - needed by friction collector and credential loading
	var projectEndpoint string
	if projectCfg, err := config.LoadProjectConfig(d.config.ProjectRoot); err == nil && projectCfg != nil {
		projectEndpoint = projectCfg.GetEndpoint()
	}

	// start telemetry collector
	d.telemetry = NewTelemetryCollector(d.logger)
	d.startTime = time.Now()

	// start friction collector for UX analytics
	// uses project endpoint so events go to the correct API (e.g., test.sageox.ai)
	d.friction = NewFrictionCollector(d.logger, projectEndpoint)

	// initialize issue tracker for health check system
	d.issues = NewIssueTracker()

	// start heartbeat handler
	d.heartbeat = NewHeartbeatHandler(d.logger)
	d.heartbeat.SetActivityCallback(d.recordActivity)
	d.heartbeat.SetTeamNeededCallback(func(teamID string) {
		// TODO: implement lazy team loading via clone queue
		d.logger.Debug("team context needed", "team_id", teamID)
	})
	d.heartbeat.SetVersionMismatchCallback(func(cliVersion, daemonVersion string) {
		// CLI has been upgraded - daemon should restart to match
		d.logger.Info("restarting due to version mismatch",
			"cli_version", cliVersion,
			"daemon_version", daemonVersion,
		)
		// stop gracefully - CLI will restart daemon with new version
		go d.Stop()
	})
	d.server.SetHeartbeatHandler(d.heartbeat.Handle)

	// pre-populate credentials from credential store (if available)
	// heartbeats will refresh these, but this handles cold-start
	if creds, err := gitserver.LoadCredentialsForEndpoint(projectEndpoint); err == nil && creds != nil {
		hbCreds := &HeartbeatCreds{
			Token:     creds.Token,
			ServerURL: creds.ServerURL,
			ExpiresAt: creds.ExpiresAt,
		}
		// also load auth token (JWT) for REST API calls (friction, repos, etc.)
		if token, err := auth.GetTokenForEndpoint(projectEndpoint); err == nil && token != nil {
			hbCreds.AuthToken = token.AccessToken
		}
		d.heartbeat.SetInitialCredentials(hbCreds)
	}

	// start sync scheduler
	d.scheduler = NewSyncScheduler(d.config, d.logger)

	// wire auth token getter so scheduler and friction can authenticate API calls
	d.scheduler.SetAuthTokenGetter(d.heartbeat.GetAuthToken)
	d.friction.SetAuthTokenGetter(d.heartbeat.GetAuthToken)

	// wire issue tracker so scheduler can report issues needing LLM reasoning
	d.scheduler.SetIssueTracker(d.issues)

	// set telemetry callback on scheduler for sync:complete events
	d.scheduler.SetTelemetryCallback(func(syncType, operation, status string, duration time.Duration) {
		if d.telemetry != nil {
			d.telemetry.RecordSyncComplete(syncType, operation, status, duration, 0)
		}
	})

	// set up IPC handlers
	d.server.SetHandlers(
		func() error { return d.scheduler.Sync() },
		func() { d.Stop() },
		func() *StatusData {
			lastErr, lastErrTime := d.scheduler.LastError()
			lastErrTimeStr := ""
			if !lastErrTime.IsZero() {
				lastErrTimeStr = lastErrTime.Format(time.RFC3339)
			}
			stats := d.scheduler.SyncStats()
			// get workspace path
			workspacePath, _ := os.Getwd()

			// get activity summary from heartbeat handler
			var activitySummary *ActivitySummary
			if d.heartbeat != nil {
				summary := d.heartbeat.GetActivitySummary()
				activitySummary = &summary
			}

			// get authenticated user from heartbeat handler
			var authUser *AuthenticatedUser
			if d.heartbeat != nil {
				authUser = d.heartbeat.GetAuthenticatedUser()
			}

			// get issues from issue tracker
			var issues []DaemonIssue
			needsHelp := false
			if d.issues != nil {
				issues = d.issues.GetIssues()
				needsHelp = d.issues.NeedsHelp()
			}

			// get all workspaces being synced (ledger + team contexts)
			// keyed by type for flexibility with future workspace types
			workspaces := make(map[string][]WorkspaceSyncStatus)
			projectTeamID := ""
			if registry := d.scheduler.WorkspaceRegistry(); registry != nil {
				projectTeamID = registry.ProjectTeamID()
				for _, ws := range registry.GetAllWorkspaces() {
					wsType := string(ws.Type)
					// normalize type to match API convention (team_context -> team-context)
					if wsType == "team_context" {
						wsType = "team-context"
					}
					workspaces[wsType] = append(workspaces[wsType], WorkspaceSyncStatus{
						ID:       ws.ID,
						Type:     wsType,
						Path:     ws.Path,
						CloneURL: ws.CloneURL,
						Exists:   ws.Exists,
						TeamID:   ws.TeamID,
						TeamName: ws.TeamName,
						TeamSlug: ws.TeamSlug,
						LastSync:       ws.ConfigLastSync,
						LastErr:        ws.LastErr,
						Syncing:        ws.SyncInProgress,
						LastGCTime:     ws.LastGCTime,
						GCIntervalDays: ws.GCIntervalDays,
					})
				}
			}

			return &StatusData{
				Running:           true,
				Pid:               os.Getpid(),
				Version:           version.Version,
				Uptime:            time.Since(d.server.startTime),
				WorkspacePath:     workspacePath,
				LedgerPath:        d.config.LedgerPath,
				LastSync:          d.scheduler.LastSync(),
				SyncIntervalRead:  d.config.SyncIntervalRead,
				RecentErrorCount:  d.scheduler.RecentErrorCount(),
				LastError:         lastErr,
				LastErrorTime:     lastErrTimeStr,
				TotalSyncs:        stats.TotalSyncs,
				SyncsLastHour:     stats.SyncsLastHour,
				AvgSyncTime:       stats.AvgDuration,
				Workspaces:        workspaces,
				ProjectTeamID:     projectTeamID,
				TeamContexts:      d.scheduler.TeamContextStatus(),
				InactivityTimeout: d.config.InactivityTimeout,
				TimeSinceActivity: d.timeSinceLastActivity(),
				Activity:          activitySummary,
				AuthenticatedUser: authUser,
				NeedsHelp:          needsHelp,
				Issues:             issues,
				StartupDurationMs:  d.startupDuration.Milliseconds(),
				ThrottleDurationMs: d.throttleDuration.Milliseconds(),
			}
		},
	)

	// set sync handler with progress support (supersedes legacy handler in SetHandlers)
	d.server.SetSyncHandler(func(progress *ProgressWriter) error {
		return d.scheduler.SyncWithProgress(progress)
	})

	// set team sync handler for on-demand team context sync
	d.server.SetTeamSyncHandler(func(progress *ProgressWriter) error {
		return d.scheduler.TeamSync(progress)
	})

	// set sync history handler
	d.server.SetSyncHistoryHandler(func() []SyncEvent {
		return d.scheduler.SyncHistory()
	})

	// set doctor handler for health checks (triggered by ox doctor, etc.)
	d.server.SetDoctorHandler(func() *DoctorResponse {
		// trigger anti-entropy (self-healing for missing repos)
		d.scheduler.TriggerAntiEntropy()
		return &DoctorResponse{
			AntiEntropyTriggered: true,
		}
	})

	// set trigger_gc handler for forced GC reclone (triggered by ox doctor --gc)
	d.server.SetTriggerGCHandler(func() *TriggerGCResponse {
		return d.scheduler.TriggerGC(d.ctx)
	})

	// set checkout handler for ledger/team context clones
	d.server.SetCheckoutHandler(func(payload CheckoutPayload, progress *ProgressWriter) (*CheckoutResult, error) {
		return d.scheduler.Checkout(payload, progress)
	})

	// set telemetry handler for CLI events
	d.server.SetTelemetryHandler(func(payload json.RawMessage) {
		var p TelemetryPayload
		if err := json.Unmarshal(payload, &p); err == nil {
			d.telemetry.RecordFromIPC(p.Event, p.Props)
		}
	})

	// set friction handler for UX analytics
	d.server.SetFrictionHandler(func(payload FrictionPayload) {
		d.friction.RecordFromIPC(payload)
	})

	// set sessions handler for agent session tracking (deprecated)
	d.server.SetSessionsHandler(func() []AgentSession {
		return d.getAgentSessions()
	})

	// set instances handler for agent instance tracking
	d.server.SetInstancesHandler(func() []InstanceInfo {
		return d.getAgentInstances()
	})

	setupDuration := time.Since(startSetup)

	// start telemetry background sender
	d.telemetry.Start()

	// start friction background sender
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

	// start file watcher if ledger path is set
	if d.config.LedgerPath != "" {
		d.watcher = NewWatcher(d.config.LedgerPath, d.config.DebounceWindow, d.logger)
		d.wg.Add(1)
		go func() {
			defer d.wg.Done()
			d.watcher.Start(d.ctx, func() {
				d.recordActivity() // file changes = activity
				d.scheduler.TriggerSync()
			})
		}()
	}

	// set activity callback on server (IPC requests = activity)
	d.server.SetActivityCallback(d.recordActivity)

	// record startup timing
	totalDuration := time.Since(startTotal)
	d.startupDuration = totalDuration
	d.throttleDuration = throttleDuration
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
	idleThreshold := IdleThreshold

	for _, agentID := range keys {
		last := tracker.Last(agentID)
		count := tracker.Count(agentID)

		status := StatusActive
		if now.Sub(last) > idleThreshold {
			status = StatusIdle
		}

		instances = append(instances, InstanceInfo{
			AgentID:        agentID,
			WorkspacePath:  workspacePath,
			LastHeartbeat:  last,
			HeartbeatCount: count,
			Status:         status,
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
	client := NewClientWithTimeout(100 * time.Millisecond)
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
