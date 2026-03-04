package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/useragent"
	"github.com/sageox/ox/internal/version"
)

const (
	// telemetryBufferSize is the max number of events in the ring buffer
	telemetryBufferSize = 500

	// telemetryBatchThreshold triggers early flush when buffer reaches this count
	telemetryBatchThreshold = 50

	// telemetryDefaultInterval is the default send interval (can be overridden by server)
	telemetryDefaultInterval = 60 * time.Second

	// telemetrySendTimeout for HTTP requests
	telemetrySendTimeout = 5 * time.Second

	// telemetryEndpoint is the default telemetry API endpoint
	telemetryEndpoint = "https://telemetry.sageox.ai/tevents"
)

// TelemetryEvent matches the spec format for telemetry events.
type TelemetryEvent struct {
	UUID    string         `json:"uuid"`
	TS      int64          `json:"ts"`
	TSLocal string         `json:"tslocal"`
	Event   string         `json:"event"`
	Props   map[string]any `json:"props"`
}

// TelemetryCollector manages event buffering and transmission to the cloud.
// It uses a ring buffer for bounded memory usage and supports server-controlled
// throttling via X-SageOx-Interval header.
type TelemetryCollector struct {
	mu         sync.Mutex
	buffer     []TelemetryEvent // ring buffer (pre-allocated)
	head       int              // next write position
	count      int              // current number of events (0 to bufferSize)
	bufferSize int              // max capacity

	sendInterval time.Duration // from server header (default 60s)
	lastSend     time.Time

	clientID   string // persistent UUIDv4
	appType    string // "ox-daemon"
	appVersion string

	httpClient *http.Client
	endpoint   string
	enabled    bool

	shutdown chan struct{}
	stopped  sync.Once // prevents double close panic
	wg       sync.WaitGroup
	logger   *slog.Logger
}

// NewTelemetryCollector creates a new telemetry collector.
// It loads or generates a persistent client ID and checks opt-out settings.
func NewTelemetryCollector(logger *slog.Logger) *TelemetryCollector {
	enabled := isTelemetryEnabled()

	endpoint := os.Getenv("SAGEOX_TELEMETRY_ENDPOINT")
	if endpoint == "" {
		endpoint = telemetryEndpoint
	}

	return &TelemetryCollector{
		buffer:       make([]TelemetryEvent, telemetryBufferSize),
		bufferSize:   telemetryBufferSize,
		sendInterval: telemetryDefaultInterval,
		clientID:     getOrCreateClientID(),
		appType:      "ox-daemon",
		appVersion:   version.Version,
		httpClient: &http.Client{
			Timeout: telemetrySendTimeout,
		},
		endpoint: endpoint,
		enabled:  enabled,
		shutdown: make(chan struct{}),
		logger:   logger,
	}
}

// isTelemetryEnabled checks opt-out settings.
func isTelemetryEnabled() bool {
	// standard opt-out
	if os.Getenv("DO_NOT_TRACK") == "1" {
		return false
	}
	// sageox-specific opt-out
	if strings.ToLower(os.Getenv("SAGEOX_TELEMETRY")) == "false" {
		return false
	}
	// check user config
	if cfg, err := config.LoadUserConfig(); err == nil {
		return cfg.IsTelemetryEnabled()
	}
	return true
}

// getOrCreateClientID returns a persistent client ID.
// Checks SAGEOX_CLIENT_ID env, then ~/.sageox/config/client_id file.
func getOrCreateClientID() string {
	// check env first
	if id := os.Getenv("SAGEOX_CLIENT_ID"); id != "" {
		return id
	}

	// check file
	configDir := config.GetUserConfigDir()
	path := filepath.Join(configDir, "client_id")

	if data, err := os.ReadFile(path); err == nil {
		id := strings.TrimSpace(string(data))
		if id != "" {
			return id
		}
	}

	// generate new UUIDv4
	id := uuid.NewString()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		slog.Debug("failed to create config directory for telemetry client ID", "error", err)
	} else if err := os.WriteFile(path, []byte(id+"\n"), 0600); err != nil {
		slog.Debug("failed to persist telemetry client ID", "error", err)
	}

	return id
}

// Start begins background processing of telemetry events.
func (c *TelemetryCollector) Start() {
	if !c.enabled {
		return
	}

	c.wg.Add(1)
	go c.backgroundSender()
}

// Stop gracefully shuts down the telemetry collector.
// Performs a final flush before returning. Safe to call multiple times.
func (c *TelemetryCollector) Stop() {
	if !c.enabled {
		return
	}

	c.stopped.Do(func() {
		close(c.shutdown)
	})
	c.wg.Wait()
}

// Record adds an event to the ring buffer.
// This is non-blocking and safe for concurrent use.
func (c *TelemetryCollector) Record(event string, props map[string]any) {
	if !c.enabled {
		return
	}

	// ensure props has required fields
	if props == nil {
		props = make(map[string]any)
	}
	if _, ok := props["app_type"]; !ok {
		props["app_type"] = c.appType
	}
	if _, ok := props["app_version"]; !ok {
		props["app_version"] = c.appVersion
	}

	now := time.Now()
	e := TelemetryEvent{
		UUID:    uuid.NewString(), // UUIDv4 for simplicity
		TS:      now.UnixMilli(),
		TSLocal: now.Format(time.RFC3339),
		Event:   event,
		Props:   props,
	}

	c.mu.Lock()
	// ring buffer write - oldest events overwritten when full
	c.buffer[c.head] = e
	c.head = (c.head + 1) % c.bufferSize
	if c.count < c.bufferSize {
		c.count++
	}
	shouldFlush := c.count >= telemetryBatchThreshold
	c.mu.Unlock()

	// trigger early flush if threshold reached
	if shouldFlush {
		select {
		case <-c.shutdown:
			// don't trigger if shutting down
		default:
			// non-blocking signal to flush
			go c.flush()
		}
	}
}

// RecordFromIPC records an event received via IPC from CLI.
// The props should already contain app_type from the CLI.
func (c *TelemetryCollector) RecordFromIPC(event string, props map[string]any) {
	if !c.enabled {
		return
	}

	now := time.Now()
	e := TelemetryEvent{
		UUID:    uuid.NewString(),
		TS:      now.UnixMilli(),
		TSLocal: now.Format(time.RFC3339),
		Event:   event,
		Props:   props,
	}

	c.mu.Lock()
	c.buffer[c.head] = e
	c.head = (c.head + 1) % c.bufferSize
	if c.count < c.bufferSize {
		c.count++
	}
	c.mu.Unlock()
}

// backgroundSender periodically flushes events to the server.
func (c *TelemetryCollector) backgroundSender() {
	defer c.wg.Done()

	ticker := time.NewTicker(c.sendInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			c.flush()
			// update ticker interval if server changed it
			c.mu.Lock()
			newInterval := c.sendInterval
			c.mu.Unlock()
			ticker.Reset(newInterval)

		case <-c.shutdown:
			c.flush() // final flush
			return
		}
	}
}

// flush sends buffered events to the server.
func (c *TelemetryCollector) flush() {
	events := c.drainBuffer()
	if len(events) == 0 {
		return
	}

	c.sendEvents(events)
}

// drainBuffer extracts all events from the ring buffer in chronological order.
// Returns a slice of events and resets the buffer.
func (c *TelemetryCollector) drainBuffer() []TelemetryEvent {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.count == 0 {
		return nil
	}

	// extract events in chronological order
	events := make([]TelemetryEvent, c.count)
	if c.count < c.bufferSize {
		// buffer not full - events start at 0
		copy(events, c.buffer[:c.count])
	} else {
		// buffer full - events wrap around
		// oldest event is at c.head, newest at (c.head-1+bufferSize)%bufferSize
		start := c.head
		for i := 0; i < c.count; i++ {
			events[i] = c.buffer[(start+i)%c.bufferSize]
		}
	}

	// reset buffer
	c.head = 0
	c.count = 0
	c.lastSend = time.Now()

	return events
}

// sendEvents transmits events to the telemetry server.
func (c *TelemetryCollector) sendEvents(events []TelemetryEvent) {
	if len(events) == 0 {
		return
	}

	payload := map[string]any{
		"clientid": c.clientID,
		"events":   events,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		c.logger.Debug("failed to marshal telemetry", "error", err)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), telemetrySendTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "POST", c.endpoint, bytes.NewReader(body))
	if err != nil {
		c.logger.Debug("failed to create telemetry request", "error", err)
		return
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", useragent.DaemonString())

	resp, err := c.httpClient.Do(req)
	if err != nil {
		c.logger.Debug("failed to send telemetry", "error", err, "events", len(events))
		return // best effort - silently discard on error
	}
	defer resp.Body.Close()

	// check for throttle header from server
	if interval := resp.Header.Get("X-SageOx-Interval"); interval != "" {
		if d, err := time.ParseDuration(interval); err == nil && d > 0 {
			c.mu.Lock()
			c.sendInterval = d
			c.mu.Unlock()
			c.logger.Debug("telemetry interval updated", "interval", d)
		}
	}

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		c.logger.Debug("telemetry sent", "events", len(events), "status", resp.StatusCode)
	} else {
		c.logger.Debug("telemetry send failed", "events", len(events), "status", resp.StatusCode)
	}
}

// IsEnabled returns whether telemetry collection is enabled.
func (c *TelemetryCollector) IsEnabled() bool {
	return c.enabled
}

// Stats returns current telemetry stats for status display.
func (c *TelemetryCollector) Stats() TelemetryStats {
	c.mu.Lock()
	defer c.mu.Unlock()

	return TelemetryStats{
		Enabled:      c.enabled,
		BufferCount:  c.count,
		BufferSize:   c.bufferSize,
		SendInterval: c.sendInterval,
		LastSend:     c.lastSend,
		ClientID:     c.clientID,
	}
}

// TelemetryStats holds telemetry statistics for status display.
type TelemetryStats struct {
	Enabled      bool          `json:"enabled"`
	BufferCount  int           `json:"buffer_count"`
	BufferSize   int           `json:"buffer_size"`
	SendInterval time.Duration `json:"send_interval"`
	LastSend     time.Time     `json:"last_send"`
	ClientID     string        `json:"client_id"`
}

// RecordDaemonStartup records the daemon startup event.
func (c *TelemetryCollector) RecordDaemonStartup() {
	c.Record("app:startup", map[string]any{
		"app_type":    "ox-daemon",
		"app_version": version.Version,
		"os":          runtime.GOOS,
		"arch":        runtime.GOARCH,
		"runtime":     runtime.Version(),
	})
}

// RecordDaemonShutdown records the daemon shutdown event.
func (c *TelemetryCollector) RecordDaemonShutdown(uptime time.Duration, reason string) {
	props := map[string]any{
		"app_type":       "ox-daemon",
		"uptime_seconds": int64(uptime.Seconds()),
	}
	if reason != "" {
		props["reason"] = reason
	}
	c.Record("app:shutdown", props)
}

// RecordDaemonCrash records a daemon crash/panic event.
func (c *TelemetryCollector) RecordDaemonCrash(uptime time.Duration, errType, errMsg string) {
	c.Record("app:crash", map[string]any{
		"app_type":       "ox-daemon",
		"uptime_seconds": int64(uptime.Seconds()),
		"error_type":     errType,
		"error_message":  truncateMessage(errMsg, 500),
	})
}

// RecordSyncComplete records a sync completion event.
func (c *TelemetryCollector) RecordSyncComplete(syncType, operation, status string, duration time.Duration, recordsCount int) {
	c.Record("sync:complete", map[string]any{
		"app_type":      "ox-daemon",
		"sync_type":     syncType,
		"operation":     operation,
		"status":        status,
		"duration_ms":   duration.Milliseconds(),
		"records_count": recordsCount,
	})
}

// truncateMessage truncates a message to maxLen runes (not bytes) to preserve UTF-8.
func truncateMessage(msg string, maxLen int) string {
	runes := []rune(msg)
	if len(runes) <= maxLen {
		return msg
	}
	if maxLen <= 3 {
		return "..."
	}
	return string(runes[:maxLen-3]) + "..."
}
