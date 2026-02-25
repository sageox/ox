package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/useragent"
	"github.com/sageox/ox/internal/daemon"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/logger"
	"github.com/sageox/ox/internal/version"
)

const (
	// sendTimeout is intentionally very short to never block user operations
	sendTimeout = 2 * time.Second
	// batchInterval is how often to flush queued events
	batchInterval = 30 * time.Second
	// maxQueueSize limits memory usage for queued events
	maxQueueSize = 100
	// telemetryPath is the API endpoint for telemetry
	telemetryPath = "/api/v1/telemetry"
)

// Client handles telemetry collection and transmission.
//
// BEST EFFORT DESIGN: Telemetry is intentionally lossy. We prioritize:
//  1. CLI responsiveness - never block user operations
//  2. Simplicity - no retries, no delivery guarantees
//  3. Privacy - easy opt-out, no PII collected
//
// Events may be lost and that's OK - this is for aggregate analytics.
//
// FUTURE: Consider moving telemetry transmission to a separate daemon process.
// A daemon would provide more reliable delivery without impacting CLI responsiveness,
// and could handle retries, backoff, and batching more intelligently. For now,
// disk-based queueing with lazy flush is simpler and sufficient.
type Client struct {
	httpClient *http.Client
	baseURL    string
	version    string
	enabled    bool

	// context fields added to all events
	repoID      string // repository ID from .sageox/config.json
	apiEndpoint string // API endpoint used

	mu        sync.Mutex
	queue     []Event    // in-memory queue (fallback when no project context)
	fileQueue *FileQueue // disk-based queue for persistence
	stats     *Stats
	shutdown  chan struct{}
	wg        sync.WaitGroup
}

// ClientOption configures a Client
type ClientOption func(*Client)

// WithHTTPClient sets a custom HTTP client
func WithHTTPClient(client *http.Client) ClientOption {
	return func(c *Client) {
		c.httpClient = client
	}
}

// WithBaseURL sets a custom base URL
func WithBaseURL(baseURL string) ClientOption {
	return func(c *Client) {
		c.baseURL = baseURL
	}
}

// WithEnabled explicitly enables/disables telemetry
func WithEnabled(enabled bool) ClientOption {
	return func(c *Client) {
		c.enabled = enabled
	}
}

// WithProjectRoot sets the project root for disk-based event queueing.
// Events are persisted to .sageox/cache/telemetry.jsonl and POSTed lazily.
// Also loads repo_id from project config for event context.
func WithProjectRoot(projectRoot string) ClientOption {
	return func(c *Client) {
		c.fileQueue = NewFileQueue(projectRoot)
		// load repo_id from project config
		if cfg, err := config.LoadProjectConfig(projectRoot); err == nil && cfg != nil {
			c.repoID = cfg.RepoID
		}
	}
}

// NewClient creates a new telemetry client.
// Telemetry is enabled by default unless user has opted out.
func NewClient(sessionID string, opts ...ClientOption) *Client {
	// check user preference
	enabled := true
	if cfg, err := config.LoadUserConfig(""); err == nil {
		enabled = cfg.IsTelemetryEnabled()
	}

	client := &Client{
		httpClient: &http.Client{
			Timeout: sendTimeout,
		},
		baseURL:     endpoint.Get(),
		version:     version.Version,
		enabled:     enabled,
		apiEndpoint: endpoint.Get(), // capture endpoint at client creation time
		queue:       make([]Event, 0, maxQueueSize),
		stats:       NewStats(sessionID),
		shutdown:    make(chan struct{}),
	}

	for _, opt := range opts {
		opt(client)
	}

	return client
}

// Start begins background processing of telemetry events.
// Should be called once at CLI startup.
// If file queue has pending events that need flushing, spawns a background POST.
func (c *Client) Start() {
	if !c.enabled {
		return
	}

	// check if file queue needs flushing (lazy flush from previous invocations)
	if c.fileQueue != nil && c.fileQueue.ShouldFlush() {
		go c.flushFileQueue()
	}

	c.wg.Add(1)
	go c.backgroundWorker()
}

// Stop gracefully shuts down the telemetry client.
// Flushes any remaining queued events with a short timeout.
func (c *Client) Stop() {
	if !c.enabled {
		return
	}

	close(c.shutdown)
	c.wg.Wait()

	// final flush with very short timeout - don't delay CLI exit
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	c.flushWithContext(ctx)
}

// backgroundWorker periodically flushes queued events
func (c *Client) backgroundWorker() {
	defer c.wg.Done()

	ticker := time.NewTicker(batchInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), sendTimeout)
			c.flushWithContext(ctx)
			cancel()
		case <-c.shutdown:
			return
		}
	}
}

// Track queues an event for transmission.
// This is non-blocking and will never fail from the caller's perspective.
// If a file queue is configured, events are persisted to disk for reliability.
func (c *Client) Track(event Event) {
	if !c.enabled {
		return
	}

	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now()
	}

	// add context fields if not already set
	if event.RepoID == "" && c.repoID != "" {
		event.RepoID = c.repoID
	}
	if event.APIEndpoint == "" && c.apiEndpoint != "" {
		event.APIEndpoint = c.apiEndpoint
	}

	// prefer disk-based queue for persistence
	if c.fileQueue != nil {
		_ = c.fileQueue.Append(event) // errors are non-fatal
		return
	}

	// fallback to in-memory queue
	c.mu.Lock()
	defer c.mu.Unlock()

	// drop oldest events if queue is full (graceful degradation)
	if len(c.queue) >= maxQueueSize {
		c.queue = c.queue[1:]
	}

	c.queue = append(c.queue, event)
}

// TrackCommand is a convenience method for tracking command execution.
// Non-blocking - safe to call in hot paths.
func (c *Client) TrackCommand(command string, duration time.Duration, success bool, errorCode string) {
	if !c.enabled {
		return
	}

	// update local stats
	c.mu.Lock()
	c.stats.RecordCommand(command, duration, success)
	c.mu.Unlock()

	event := Event{
		Type:      EventCommandComplete,
		Command:   command,
		Duration:  duration.Milliseconds(),
		Success:   success,
		ErrorCode: errorCode,
		SessionID: c.stats.SessionID,
	}

	c.Track(event)
}

// TrackAsync sends a single event asynchronously without queuing.
// Use for one-off events that should be sent immediately.
// Fire-and-forget: errors are silently discarded.
func (c *Client) TrackAsync(event Event) {
	if !c.enabled {
		return
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), sendTimeout)
		defer cancel()
		c.sendEvents(ctx, []Event{event})
	}()
}

// Flush sends all queued events immediately.
// Non-blocking: spawns goroutine for transmission.
func (c *Client) Flush() {
	if !c.enabled {
		return
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), sendTimeout)
		defer cancel()
		c.flushWithContext(ctx)
	}()
}

// flushWithContext sends all queued events with the given context
func (c *Client) flushWithContext(ctx context.Context) {
	c.mu.Lock()
	if len(c.queue) == 0 {
		c.mu.Unlock()
		return
	}

	// take ownership of queue
	events := c.queue
	c.queue = make([]Event, 0, maxQueueSize)
	c.mu.Unlock()

	c.sendEvents(ctx, events)
}

// flushFileQueue reads events from disk and POSTs them.
// On success, truncates the queue file. Fire-and-forget.
func (c *Client) flushFileQueue() {
	if c.fileQueue == nil {
		return
	}

	events, err := c.fileQueue.ReadAll()
	if err != nil || len(events) == 0 {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), sendTimeout)
	defer cancel()

	// send events - if successful, truncate the file
	if c.sendEventsWithResult(ctx, events) {
		_ = c.fileQueue.Truncate()
	}
	// on failure, events remain on disk for next invocation
}

// sendEventsWithResult transmits events and returns success status.
func (c *Client) sendEventsWithResult(ctx context.Context, events []Event) bool {
	if len(events) == 0 {
		return true
	}

	batch := Batch{
		Events:    events,
		CLIVer:    c.version,
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
		Timestamp: time.Now(),
	}

	body, err := json.Marshal(batch)
	if err != nil {
		return false
	}

	url := c.baseURL + telemetryPath
	req, err := useragent.NewRequest(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return false
	}

	req.Header.Set("Content-Type", "application/json")

	logger.LogHTTPRequest("POST", url)
	start := time.Now()

	resp, err := c.httpClient.Do(req)
	duration := time.Since(start)
	if err != nil {
		logger.LogHTTPError("POST", url, err, duration)
		return false
	}
	resp.Body.Close()

	logger.LogHTTPResponse("POST", url, resp.StatusCode, duration)

	// consider 2xx as success
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

// sendEvents transmits events to the API.
// Errors are silently discarded - telemetry should never affect user experience.
func (c *Client) sendEvents(ctx context.Context, events []Event) {
	if len(events) == 0 {
		return
	}

	batch := Batch{
		Events:    events,
		CLIVer:    c.version,
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
		Timestamp: time.Now(),
	}

	body, err := json.Marshal(batch)
	if err != nil {
		// silently discard - don't log to avoid noise
		return
	}

	url := c.baseURL + telemetryPath
	req, err := useragent.NewRequest(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return
	}

	req.Header.Set("Content-Type", "application/json")

	logger.LogHTTPRequest("POST", url)
	start := time.Now()

	resp, err := c.httpClient.Do(req)
	duration := time.Since(start)
	if err != nil {
		logger.LogHTTPError("POST", url, err, duration)
		// network error - silently discard
		return
	}
	resp.Body.Close()

	logger.LogHTTPResponse("POST", url, resp.StatusCode, duration)

	// we don't care about the response - fire and forget
}

// GetStats returns a copy of current session statistics
func (c *Client) GetStats() Stats {
	c.mu.Lock()
	defer c.mu.Unlock()

	// return a copy to avoid data races
	statsCopy := *c.stats
	statsCopy.CommandCounts = make(map[string]int)
	for k, v := range c.stats.CommandCounts {
		statsCopy.CommandCounts[k] = v
	}
	return statsCopy
}

// IsEnabled returns whether telemetry is enabled
func (c *Client) IsEnabled() bool {
	return c.enabled
}

// SendToDaemon sends a telemetry event to the daemon via IPC (fire-and-forget).
// This is the preferred method when daemon is running, as it's non-blocking
// and allows the daemon to handle batching and transmission.
// Silently fails if daemon is not running or IPC fails.
func (c *Client) SendToDaemon(eventType string, props map[string]any) {
	if !c.enabled {
		return
	}

	// ensure required fields
	if props == nil {
		props = make(map[string]any)
	}
	if _, ok := props["app_type"]; !ok {
		props["app_type"] = "ox"
	}
	if _, ok := props["app_version"]; !ok {
		props["app_version"] = c.version
	}

	// fire-and-forget in goroutine to never block
	go c.sendToDaemonAsync(eventType, props)
}

// sendToDaemonAsync sends the event to daemon via IPC.
// Uses very short timeout since this is best-effort.
func (c *Client) sendToDaemonAsync(eventType string, props map[string]any) {
	// very short timeout - we don't want to block
	client := daemon.NewClientWithTimeout(50 * time.Millisecond)

	payload := daemon.TelemetryPayload{
		Event: eventType,
		Props: props,
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return // silently fail
	}

	// fire-and-forget: don't wait for response
	_ = client.SendOneWay(daemon.Message{
		Type:    daemon.MsgTypeTelemetry,
		Payload: data,
	})
}

// TrackDaemonStartFailure reports daemon start failures directly to the telemetry API.
//
// IMPORTANT: This is the ONLY case where the ox CLI sends telemetry directly to the API.
// All other telemetry goes through the daemon via IPC. This exception exists because
// daemon start failures occur before the daemon is running, so we cannot use the daemon
// to report them.
//
// This method:
//   - Respects user opt-out (DO_NOT_TRACK=1, SAGEOX_TELEMETRY=false, or config setting)
//   - Uses rate limiting with exponential backoff (1min, 2min, 4min... up to 32min)
//   - Prevents swarms of failure metrics during persistent daemon issues
//   - Sends asynchronously to never block CLI operations
func (c *Client) TrackDaemonStartFailure(errorType, errorMsg string) {
	if !c.enabled {
		return
	}

	// check rate limit before sending
	if !c.checkDaemonFailureRateLimit() {
		return // rate limited, skip this report
	}

	event := Event{
		Type:      "daemon:start_failure",
		Timestamp: time.Now(),
		SessionID: c.stats.SessionID,
		Metadata: map[string]string{
			"error_type": errorType,
			"error_msg":  truncateString(errorMsg, 500),
		},
	}

	// send directly (not via daemon) in background
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), sendTimeout)
		defer cancel()
		c.sendEvents(ctx, []Event{event})
	}()
}

// daemonFailureRateLimit tracks rate limiting state for daemon failure reports.
type daemonFailureRateLimit struct {
	LastSent     time.Time `json:"last_sent"`
	BackoffLevel int       `json:"backoff_level"` // 0=1min, 1=2min, 2=4min, etc.
	MaxBackoff   int       `json:"max_backoff"`   // max backoff level (caps at 32min)
}

const (
	daemonFailureBaseBackoff = 1 * time.Minute
	daemonFailureMaxBackoff  = 5 // 2^5 = 32 minutes max
)

// checkDaemonFailureRateLimit checks if we can send a daemon failure event.
// Returns true if allowed, false if rate limited.
// Updates the rate limit state file on success.
func (c *Client) checkDaemonFailureRateLimit() bool {
	rateLimitPath := c.daemonFailureRateLimitPath()

	// load current state
	state := c.loadDaemonFailureRateLimit(rateLimitPath)

	// calculate current backoff duration
	backoffDuration := daemonFailureBaseBackoff * time.Duration(1<<state.BackoffLevel)

	// check if enough time has passed
	if time.Since(state.LastSent) < backoffDuration {
		return false // rate limited
	}

	// allowed - update state with increased backoff (exponential)
	state.LastSent = time.Now()
	if state.BackoffLevel < daemonFailureMaxBackoff {
		state.BackoffLevel++
	}

	// save state (best effort)
	c.saveDaemonFailureRateLimit(rateLimitPath, state)

	return true
}

// ResetDaemonFailureRateLimit resets the backoff after a successful daemon start.
// Call this when daemon starts successfully to reset exponential backoff.
func (c *Client) ResetDaemonFailureRateLimit() {
	rateLimitPath := c.daemonFailureRateLimitPath()
	state := daemonFailureRateLimit{
		LastSent:     time.Time{}, // zero time
		BackoffLevel: 0,
	}
	c.saveDaemonFailureRateLimit(rateLimitPath, state)
}

func (c *Client) daemonFailureRateLimitPath() string {
	return filepath.Join(config.GetUserConfigDir(), "daemon_failure_ratelimit.json")
}

func (c *Client) loadDaemonFailureRateLimit(path string) daemonFailureRateLimit {
	var state daemonFailureRateLimit
	data, err := os.ReadFile(path)
	if err != nil {
		return state // return zero state on error
	}
	_ = json.Unmarshal(data, &state)
	return state
}

func (c *Client) saveDaemonFailureRateLimit(path string, state daemonFailureRateLimit) {
	data, err := json.Marshal(state)
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(path), 0700)
	_ = os.WriteFile(path, data, 0600)
}

func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}
