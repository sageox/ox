package agentwork

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/sageox/ox/internal/config"
)

const (
	// maxRetries is the maximum number of attempts for a work item before it is dropped.
	maxRetries = 3
	// maxRecentEntries is the number of completed processes to keep for observability.
	maxRecentEntries = 10
	// doctorInterval is the periodic timer for running detection scans
	// independent of sync signals. Catches orphaned sessions, stale
	// recordings, and other time-based conditions. Kept short (5m) because
	// detection is cheap (directory listing + PID checks) and the daemon
	// auto-exits after 1h idle — a long interval risks never running.
	doctorInterval = 5 * time.Minute
	// detectCooldown is the minimum interval between detection scans for a handler.
	// Prevents expensive ledger scans from running on every sync signal.
	detectCooldown = 5 * time.Minute

	// status constants for AgentProcess
	statusRunning   = "running"
	statusCompleted = "completed"
	statusFailed    = "failed"
)

// AgentWorkStats tracks cumulative invocation metrics.
type AgentWorkStats struct {
	TotalInvocations int `json:"total_invocations"`
	Successes        int `json:"successes"`
	Failures         int `json:"failures"`
}

// AgentProcess describes a single agent invocation (active or completed).
type AgentProcess struct {
	PID       int           `json:"pid"`
	WorkType  string        `json:"work_type"`
	Target    string        `json:"target"`
	StartedAt time.Time     `json:"started_at"`
	Status    string        `json:"status"` // "running", "completed", "failed"
	Duration  time.Duration `json:"duration,omitempty"`
	Error     string        `json:"error,omitempty"`
	ExitCode  int           `json:"exit_code,omitempty"`
}

// WorkResult captures the outcome of processing a work item.
type WorkResult struct {
	Item     *WorkItem
	Success  bool
	Output   string
	Duration time.Duration
	Error    string
}

// AgentWorkStatus is the IPC-friendly snapshot of Manager state.
type AgentWorkStatus struct {
	Enabled    bool           `json:"enabled"`
	AgentType  string         `json:"agent_type"`
	AgentAvail bool           `json:"agent_available"`
	QueueDepth int            `json:"queue_depth"`
	Active     []AgentProcess `json:"active,omitempty"`
	Recent     []AgentProcess `json:"recent,omitempty"`
	Stats      AgentWorkStats `json:"stats"`
}

// Manager coordinates agent work items in the daemon.
type Manager struct {
	runner   Runner
	queue    *WorkQueue
	logger   *slog.Logger
	handlers map[string]WorkHandler

	// rate limiting
	rateLimiter *RateLimiter

	// concurrency control
	sem chan struct{}

	// detection cooldown: tracks last detect time per handler type
	lastDetect map[string]time.Time

	// observability
	mu     sync.Mutex
	stats  AgentWorkStats
	active map[string]AgentProcess // keyed by WorkItem.ID
	recent []AgentProcess

	// callbacks
	onComplete func(result WorkResult)

	// configuration (reloaded each cycle)
	configLoader func() *config.AgentWorkerConfig

	// ledger path for handler detection
	ledgerPath string

	// optional session watcher for tail-mode recordings (hookless agents)
	sessionWatcher *SessionWatcherManager

	// signals
	syncSignal    <-chan struct{}
	processSignal chan struct{}
}

// NewManager creates a Manager wired to the given runner and config loader.
// syncSignal is a channel that fires after each ledger sync to trigger immediate
// detection and processing. ledgerPath is passed to handler Detect calls.
func NewManager(
	runner Runner,
	logger *slog.Logger,
	configLoader func() *config.AgentWorkerConfig,
	syncSignal <-chan struct{},
	ledgerPath string,
) *Manager {
	if logger == nil {
		logger = slog.Default()
	}

	cfg := configLoader()
	if cfg == nil {
		cfg = (&config.AgentWorkerConfig{}).WithDefaults()
	}

	maxConcurrent := cfg.GetMaxConcurrent()
	maxPerHour := cfg.GetMaxInvocationsPerHour()

	return &Manager{
		runner:        runner,
		queue:         NewWorkQueue(logger),
		logger:        logger,
		handlers:      make(map[string]WorkHandler),
		lastDetect:    make(map[string]time.Time),
		active:        make(map[string]AgentProcess),
		rateLimiter:   NewRateLimiter(maxPerHour, time.Hour),
		sem:           make(chan struct{}, maxConcurrent),
		configLoader:  configLoader,
		ledgerPath:    ledgerPath,
		syncSignal:    syncSignal,
		processSignal: make(chan struct{}, 1),
	}
}

// RegisterHandler adds a WorkHandler keyed by its Type().
func (m *Manager) RegisterHandler(handler WorkHandler) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.handlers[handler.Type()] = handler
	m.logger.Info("registered agent work handler", "type", handler.Type())
}

// SetOnComplete sets a callback invoked after each work item completes.
func (m *Manager) SetOnComplete(fn func(result WorkResult)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onComplete = fn
}

// Enqueue adds a work item to the queue. Safe for external callers (e.g., IPC).
// Signals the main loop to process immediately so externally-enqueued items
// (e.g., from IPC) don't wait for syncSignal or doctorTicker.
func (m *Manager) Enqueue(item *WorkItem) bool {
	ok := m.queue.Enqueue(item)
	if ok {
		// notify main loop to process immediately
		select {
		case m.processSignal <- struct{}{}:
		default:
		}
	}
	return ok
}

// Start runs the main loop until ctx is canceled.
//
// Two triggers drive work:
//   - syncSignal: fires after a successful ledger pull. Only drains the
//     existing queue — does NOT trigger detection scans (syncs are too
//     frequent for doctor-style work).
//   - doctorTicker: fires every doctorInterval (5m). Runs detect+process.
//     This is the sole trigger for detection scans, catching both
//     newly-synced incomplete sessions and time-based conditions (e.g.,
//     stale recordings with dead parent PIDs).
func (m *Manager) Start(ctx context.Context) {
	doctorTicker := time.NewTicker(doctorInterval)
	defer doctorTicker.Stop()

	m.logger.Info("agent work manager started")

	for {
		select {
		case <-ctx.Done():
			m.logger.Info("agent work manager stopping")
			return
		case _, ok := <-m.syncSignal:
			if !ok {
				m.logger.Info("sync signal channel closed, stopping agent work manager")
				return
			}
			// drain queue only — detection runs on the doctor timer
			m.processQueue(ctx)
		case <-m.processSignal:
			m.processQueue(ctx)
		case <-doctorTicker.C:
			m.logger.Debug("doctor timer fired, running detection")
			m.detectAndEnqueue()
			m.processQueue(ctx)
		}
	}
}

// isEffectivelyEnabled returns true if the agent worker should run.
// Resolves auto-detection: unconfigured ("") checks PATH for available agents.
func isEffectivelyEnabled(cfg *config.AgentWorkerConfig) bool {
	if cfg == nil {
		return false
	}
	return ResolveAgent(cfg.GetAgent()) != "none"
}

// isHandlerEnabled checks whether a handler's work type is enabled in config.
// Uses resolved agent state (auto-detection) rather than raw config.
func isHandlerEnabled(cfg *config.AgentWorkerConfig, handlerType string) bool {
	if !isEffectivelyEnabled(cfg) {
		return false
	}
	switch handlerType {
	case "session-finalize":
		// SessionFinalize defaults to true when agent worker is enabled.
		// Only disabled if explicitly set to false.
		if cfg != nil && cfg.SessionFinalize != nil {
			return *cfg.SessionFinalize
		}
		return true
	default:
		return true
	}
}

// detectAndEnqueue runs Detect on every registered handler and enqueues the results.
// Handlers are skipped if their work type is disabled in config.
//
// Before running full detection, lightweight session cleanup (ghost removal) runs
// unconditionally — it requires no LLM and is safe regardless of agent_worker.enabled.
func (m *Manager) detectAndEnqueue() {
	// ghost cleanup always runs — filesystem-only, no LLM needed
	m.runSessionCleanup()

	cfg := m.configLoader()
	if cfg == nil || !isEffectivelyEnabled(cfg) {
		return
	}

	m.mu.Lock()
	handlers := make([]WorkHandler, 0, len(m.handlers))
	for _, h := range m.handlers {
		handlers = append(handlers, h)
	}
	m.mu.Unlock()

	now := time.Now()
	for _, h := range handlers {
		if !isHandlerEnabled(cfg, h.Type()) {
			m.logger.Debug("handler disabled by config", "type", h.Type())
			continue
		}

		// cooldown: skip detection if we ran recently
		m.mu.Lock()
		last := m.lastDetect[h.Type()]
		m.mu.Unlock()
		if !last.IsZero() && now.Sub(last) < detectCooldown {
			m.logger.Debug("handler detect skipped, cooldown active",
				"type", h.Type(),
				"last_detect", last.Format(time.RFC3339),
				"next_detect_after", last.Add(detectCooldown).Format(time.RFC3339),
			)
			continue
		}

		items, err := h.Detect(m.ledgerPath)
		if err != nil {
			m.logger.Warn("handler detect failed", "type", h.Type(), "error", err)
			continue
		}

		m.mu.Lock()
		m.lastDetect[h.Type()] = now
		m.mu.Unlock()

		for _, item := range items {
			if m.queue.Enqueue(item) {
				m.logger.Info("detected agent work", "type", item.Type, "dedup_key", item.DedupKey)
			}
		}
	}
}

// SetSessionWatcher sets an optional SessionWatcherManager for periodic detection
// and cleanup of tail-mode session watchers. Called during daemon startup.
func (m *Manager) SetSessionWatcher(sw *SessionWatcherManager) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessionWatcher = sw
}

// runSessionCleanup runs lightweight filesystem cleanup for the session-finalize
// handler regardless of agent_worker.enabled. Only removes ghost sessions (dead PID
// + no data). Does NOT clear .recording.json markers on sessions with recoverable
// data — those markers are preserved until full Detect() + LLM processing can run.
//
// Also runs session watcher detect/cleanup for tail-mode recordings.
func (m *Manager) runSessionCleanup() {
	m.mu.Lock()
	h, ok := m.handlers[sessionFinalizeType]
	sw := m.sessionWatcher
	m.mu.Unlock()

	if ok {
		if sfh, ok := h.(*SessionFinalizeHandler); ok {
			sfh.Cleanup(m.ledgerPath)
		}
	}

	// tail-mode watcher anti-entropy: restart orphaned watchers, stop finished ones
	if sw != nil {
		sw.DetectAndRestart(m.ledgerPath)
		sw.Cleanup()
	}
}

// ForceDetect runs detection on all enabled handlers, bypassing the cooldown.
// Returns the total number of work items enqueued. Safe to call from IPC handlers.
// Always runs session cleanup (ghost removal) regardless of agent_worker.enabled.
func (m *Manager) ForceDetect() int {
	// ghost cleanup always runs
	m.runSessionCleanup()

	cfg := m.configLoader()
	if cfg == nil || !isEffectivelyEnabled(cfg) {
		return 0
	}

	m.mu.Lock()
	handlers := make([]WorkHandler, 0, len(m.handlers))
	for _, h := range m.handlers {
		handlers = append(handlers, h)
	}
	m.mu.Unlock()

	total := 0
	now := time.Now()
	for _, h := range handlers {
		if !isHandlerEnabled(cfg, h.Type()) {
			m.logger.Debug("handler disabled by config", "type", h.Type())
			continue
		}

		items, err := h.Detect(m.ledgerPath)
		if err != nil {
			m.logger.Warn("handler detect failed", "type", h.Type(), "error", err)
			continue
		}

		// update cooldown timestamp even on forced detect
		m.mu.Lock()
		m.lastDetect[h.Type()] = now
		m.mu.Unlock()

		for _, item := range items {
			if m.queue.Enqueue(item) {
				m.logger.Info("detected agent work", "type", item.Type, "dedup_key", item.DedupKey)
				total++
			}
		}
	}

	// signal queue processor
	select {
	case m.processSignal <- struct{}{}:
	default:
	}

	m.logger.Info("force detect complete", "items_queued", total)
	return total
}

// processQueue drains the queue, respecting config, rate limits, and concurrency.
func (m *Manager) processQueue(ctx context.Context) {
	cfg := m.configLoader()
	if cfg == nil || !isEffectivelyEnabled(cfg) {
		return
	}

	if !m.runner.Available() {
		m.logger.Debug("agent runner not available, skipping queue processing")
		return
	}

	for {
		if ctx.Err() != nil {
			return
		}

		// try to acquire a semaphore slot before consuming a rate limit token;
		// this prevents wasting tokens when concurrency is saturated
		select {
		case m.sem <- struct{}{}:
			// slot acquired
		default:
			m.logger.Debug("concurrency limit reached, pausing queue processing")
			return
		}

		// check rate limit after acquiring slot
		if !m.rateLimiter.Allow() {
			<-m.sem // release the slot we just acquired
			m.logger.Debug("rate limit reached, pausing queue processing")
			return
		}

		item := m.queue.Dequeue()
		if item == nil {
			<-m.sem // release the slot, nothing to process
			return
		}

		go func(item *WorkItem) {
			defer func() {
				<-m.sem
				// signal queue to pick up next item immediately
				select {
				case m.processSignal <- struct{}{}:
				default:
				}
			}()
			m.executeItem(ctx, item)
		}(item)
	}
}

// executeItem runs a single work item through its handler pipeline.
func (m *Manager) executeItem(ctx context.Context, item *WorkItem) {
	defer func() {
		if r := recover(); r != nil {
			m.logger.Error("panic in agent work execution",
				"type", item.Type,
				"dedup_key", item.DedupKey,
				"panic", fmt.Sprintf("%v", r),
				"stack", string(debug.Stack()),
			)
			m.queue.Complete(item.DedupKey)
			m.recordFailure(item, time.Now(), 0, fmt.Sprintf("panic: %v", r))
		}
	}()

	m.mu.Lock()
	handler, ok := m.handlers[item.Type]
	m.mu.Unlock()
	if !ok {
		m.logger.Error("no handler registered for work type", "type", item.Type)
		m.queue.Complete(item.DedupKey)
		return
	}

	// build prompt
	req, err := handler.BuildPrompt(item)
	if err != nil {
		m.logger.Error("failed to build prompt", "type", item.Type, "error", err)
		m.queue.Complete(item.DedupKey)
		m.recordFailure(item, time.Now(), 0, fmt.Sprintf("build prompt: %v", err))
		return
	}

	target := item.DedupKey
	start := time.Now()
	proc := AgentProcess{
		WorkType:  item.Type,
		Target:    target,
		StartedAt: start,
		Status:    statusRunning,
	}

	m.logger.Info("starting agent work",
		"type", item.Type,
		"target", target,
		"attempt", item.Attempts+1,
		"reason", describeWorkReason(item),
	)

	// add to active map (keyed by item ID for race-safe removal)
	m.mu.Lock()
	m.active[item.ID] = proc
	m.mu.Unlock()

	var result *RunResult
	var runErr error
	var duration time.Duration

	if req.SkipLLM {
		// no LLM invocation needed — call ProcessResult directly with empty output
		result = &RunResult{}
		duration = time.Since(start)
		m.mu.Lock()
		delete(m.active, item.ID)
		m.mu.Unlock()
	} else {
		// run the agent
		result, runErr = m.runner.Run(ctx, req)
		duration = time.Since(start)

		// remove from active map
		m.mu.Lock()
		delete(m.active, item.ID)
		m.mu.Unlock()

		if runErr != nil {
			item.Attempts++
			item.LastErr = runErr.Error()

			exitCode := 0
			if result != nil {
				exitCode = result.ExitCode
			}

			if item.Attempts < maxRetries {
				m.logger.Warn("agent run failed, will retry",
					"type", item.Type,
					"attempt", item.Attempts,
					"max_attempts", maxRetries,
					"error", runErr,
				)
				m.queue.Requeue(item)
			} else {
				m.logger.Error("agent run failed after max retries",
					"type", item.Type,
					"attempts", item.Attempts,
					"error", runErr,
				)
				m.queue.Complete(item.DedupKey)
				m.recordFailure(item, start, exitCode, runErr.Error())
			}
			return
		}
	}

	// process result through handler
	if err := handler.ProcessResult(item, result); err != nil {
		m.logger.Error("handler process result failed", "type", item.Type, "error", err)
		m.queue.Complete(item.DedupKey)
		m.recordFailure(item, start, result.ExitCode, fmt.Sprintf("process result: %v", err))
		return
	}

	// success
	m.queue.Complete(item.DedupKey)

	m.mu.Lock()
	m.stats.TotalInvocations++
	m.stats.Successes++

	completed := AgentProcess{
		WorkType:  item.Type,
		Target:    target,
		StartedAt: start,
		Status:    statusCompleted,
		Duration:  duration,
		ExitCode:  result.ExitCode,
	}
	m.recent = append(m.recent, completed)
	if len(m.recent) > maxRecentEntries {
		m.recent = m.recent[len(m.recent)-maxRecentEntries:]
	}

	cb := m.onComplete
	m.mu.Unlock()

	if cb != nil {
		cb(WorkResult{
			Item:     item,
			Success:  true,
			Output:   result.Output,
			Duration: duration,
		})
	}

	m.logger.Info("agent work completed",
		"type", item.Type,
		"target", target,
		"duration", duration,
	)
}

// recordFailure updates stats and recent list for a failed item, then fires
// the onComplete callback.
func (m *Manager) recordFailure(item *WorkItem, startedAt time.Time, exitCode int, errMsg string) {
	target := item.DedupKey

	m.mu.Lock()
	m.stats.TotalInvocations++
	m.stats.Failures++

	failed := AgentProcess{
		WorkType:  item.Type,
		Target:    target,
		StartedAt: startedAt,
		Status:    statusFailed,
		Error:     errMsg,
		ExitCode:  exitCode,
	}
	m.recent = append(m.recent, failed)
	if len(m.recent) > maxRecentEntries {
		m.recent = m.recent[len(m.recent)-maxRecentEntries:]
	}

	cb := m.onComplete
	m.mu.Unlock()

	if cb != nil {
		cb(WorkResult{
			Item:    item,
			Success: false,
			Error:   errMsg,
		})
	}
}

// Status returns a point-in-time snapshot of the manager for IPC status responses.
func (m *Manager) Status() AgentWorkStatus {
	cfg := m.configLoader()
	enabled := cfg != nil && isEffectivelyEnabled(cfg)
	agentType := "none"
	if cfg != nil {
		agentType = ResolveAgent(cfg.GetAgent())
	}

	// read these outside the lock to avoid holding mu during syscalls
	agentAvail := m.runner.Available()
	queueDepth := m.queue.Len()

	m.mu.Lock()
	defer m.mu.Unlock()

	activeCopy := make([]AgentProcess, 0, len(m.active))
	for _, proc := range m.active {
		activeCopy = append(activeCopy, proc)
	}

	recentCopy := make([]AgentProcess, len(m.recent))
	copy(recentCopy, m.recent)

	return AgentWorkStatus{
		Enabled:    enabled,
		AgentType:  agentType,
		AgentAvail: agentAvail,
		QueueDepth: queueDepth,
		Active:     activeCopy,
		Recent:     recentCopy,
		Stats:      m.stats,
	}
}

// HasPendingWork returns true if there are queued or in-flight finalization items.
func (m *Manager) HasPendingWork() bool {
	return m.queue.Len() > 0 || m.queue.InProgressCount() > 0
}

// describeWorkReason returns a human-readable reason for why a work item exists.
func describeWorkReason(item *WorkItem) string {
	if p, ok := item.Payload.(*SessionFinalizePayload); ok && len(p.Missing) > 0 {
		return fmt.Sprintf("missing artifacts: %s", strings.Join(p.Missing, ", "))
	}
	return item.DedupKey
}
