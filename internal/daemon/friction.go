package daemon

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/uxfriction"
	"github.com/sageox/ox/internal/version"
)

const (
	// frictionBufferSize is the max number of events in the ring buffer.
	// Matches MaxEventsPerRequest (100) so a full buffer can be sent in one request.
	frictionBufferSize = 100

	// frictionFlushInterval is the default interval between flush attempts.
	// 15 minutes is appropriate because friction events are rare (typos/unknown
	// commands), and we don't want to generate unnecessary traffic. The daemon
	// also flushes on shutdown, and the batch threshold handles burst scenarios.
	frictionFlushInterval = 15 * time.Minute

	// frictionBatchThreshold triggers early flush when buffer reaches this count.
	frictionBatchThreshold = 20

	// frictionDefaultEndpoint is the fallback friction API endpoint when no project
	// endpoint is configured. Matches endpoint.Default.
	frictionDefaultEndpoint = "https://sageox.ai"
)

// FrictionCollector manages friction event buffering and transmission to the cloud.
// It uses a ring buffer for bounded memory usage (no unbounded growth even if backend
// is unavailable) and respects server-controlled rate limiting via X-SageOx-Sample-Rate
// and Retry-After headers.
type FrictionCollector struct {
	mu           sync.Mutex
	buffer       *uxfriction.RingBuffer
	client       *uxfriction.Client
	catalogCache *CatalogCache

	enabled      bool
	shutdown     chan struct{}
	stopped      sync.Once
	wg           sync.WaitGroup
	logger       *slog.Logger
	getAuthToken func() string
}

// NewFrictionCollector creates a new friction event collector.
// If friction is disabled via settings, the collector operates as a no-op.
//
// projectEndpoint is the project's configured endpoint (e.g., "https://test.sageox.ai").
// If empty, falls back to the default production endpoint.
// SAGEOX_FRICTION_ENDPOINT env var always takes precedence when set.
func NewFrictionCollector(logger *slog.Logger, projectEndpoint string) *FrictionCollector {
	enabled := isFrictionEnabled()

	// precedence: env var > project endpoint > default
	ep := os.Getenv("SAGEOX_FRICTION_ENDPOINT")
	if ep == "" {
		ep = projectEndpoint
	}
	if ep == "" {
		ep = frictionDefaultEndpoint
	}

	fc := &FrictionCollector{
		buffer:       uxfriction.NewRingBuffer(frictionBufferSize),
		catalogCache: NewCatalogCache(),
		enabled:      enabled,
		shutdown:     make(chan struct{}),
		logger:       logger,
	}

	// AuthFunc calls through to fc.getAuthToken, which is wired later via
	// SetAuthTokenGetter. This lazy indirection lets us create the collector
	// before the heartbeat handler is ready.
	fc.client = uxfriction.NewClient(uxfriction.ClientConfig{
		Endpoint: ep,
		Version:  version.Version,
		AuthFunc: func() string {
			if fc.getAuthToken != nil {
				return fc.getAuthToken()
			}
			return ""
		},
	})

	return fc
}

// SetAuthTokenGetter sets the callback to get auth token from heartbeat cache.
// Friction events are only accepted by the server from authenticated users.
func (f *FrictionCollector) SetAuthTokenGetter(cb func() string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.getAuthToken = cb
}

// isFrictionEnabled checks if friction telemetry should be collected.
func isFrictionEnabled() bool {
	// standard opt-out
	if os.Getenv("DO_NOT_TRACK") == "1" {
		return false
	}
	// sageox-specific opt-out for friction
	if strings.ToLower(os.Getenv("SAGEOX_FRICTION")) == "false" {
		return false
	}
	// check user config for telemetry setting (friction piggybacks on this)
	if cfg, err := config.LoadUserConfig(); err == nil {
		return cfg.IsTelemetryEnabled()
	}
	return true
}

// Start begins background processing of friction events.
// Also loads any cached catalog from disk.
func (f *FrictionCollector) Start() {
	if !f.enabled {
		return
	}

	// load cached catalog
	if err := f.catalogCache.Load(); err != nil {
		f.logger.Debug("failed to load catalog cache", "error", err)
	} else if v := f.catalogCache.Version(); v != "" {
		f.logger.Debug("loaded catalog cache", "version", v)
	}

	f.wg.Add(1)
	go f.backgroundSender()
}

// Stop gracefully shuts down the friction collector.
// Performs a final flush before returning. Safe to call multiple times.
func (f *FrictionCollector) Stop() {
	if !f.enabled {
		return
	}

	f.stopped.Do(func() {
		close(f.shutdown)
	})
	f.wg.Wait()
}

// RecordFromIPC adds a friction event from an IPC payload.
func (f *FrictionCollector) RecordFromIPC(payload FrictionPayload) {
	event := uxfriction.FrictionEvent{
		Timestamp:  payload.Timestamp,
		Kind:       uxfriction.FailureKind(payload.Kind),
		Command:    payload.Command,
		Subcommand: payload.Subcommand,
		Actor:      payload.Actor,
		AgentType:  payload.AgentType,
		PathBucket: payload.PathBucket,
		Input:      payload.Input,
		ErrorMsg:   payload.ErrorMsg,
	}
	f.Record(event)
}

// Record adds a friction event to the buffer.
// This is non-blocking and safe for concurrent use.
// Events are silently dropped if the buffer is full (ring buffer overwrites oldest).
func (f *FrictionCollector) Record(event uxfriction.FrictionEvent) {
	if !f.enabled {
		return
	}

	// ensure timestamp is set
	if event.Timestamp == "" {
		event.Timestamp = time.Now().UTC().Format(time.RFC3339)
	}

	// truncate fields to max length
	event.Truncate()

	f.buffer.Add(event)

	// trigger early flush if threshold reached
	if f.buffer.Count() >= frictionBatchThreshold {
		select {
		case <-f.shutdown:
			// don't trigger if shutting down
		default:
			// non-blocking signal to flush
			go f.flush()
		}
	}
}

// backgroundSender periodically flushes events to the server.
func (f *FrictionCollector) backgroundSender() {
	defer f.wg.Done()

	ticker := time.NewTicker(frictionFlushInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			f.flush()

		case <-f.shutdown:
			f.flush() // final flush
			return
		}
	}
}

// flush sends buffered events to the server.
func (f *FrictionCollector) flush() {
	// check if we should send based on rate limiting
	if !f.client.ShouldSend() {
		f.logger.Debug("friction flush skipped due to rate limiting",
			"sample_rate", f.client.SampleRate(),
			"retry_after", f.client.RetryAfter())
		return
	}

	events := f.buffer.Drain()
	if len(events) == 0 {
		return
	}

	// submit events
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	opts := &uxfriction.SubmitOptions{
		CatalogVersion: f.catalogCache.Version(),
	}

	resp, err := f.client.Submit(ctx, events, opts)
	if err != nil {
		f.logger.Debug("friction submit failed", "error", err, "events", len(events))
		return // best effort - silently discard on error
	}

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		f.logger.Debug("friction events sent", "events", len(events), "status", resp.StatusCode)

		// update catalog cache if response contains new catalog data
		if resp.Catalog != nil {
			if _, err := f.catalogCache.Update(resp.Catalog); err != nil {
				f.logger.Debug("failed to update catalog cache", "error", err)
			}
		}
	} else {
		f.logger.Debug("friction submit returned non-success", "events", len(events), "status", resp.StatusCode)
	}
}

// IsEnabled returns whether friction collection is enabled.
func (f *FrictionCollector) IsEnabled() bool {
	return f.enabled
}

// CatalogVersion returns the current cached catalog version.
// Returns empty string if no catalog is cached.
func (f *FrictionCollector) CatalogVersion() string {
	return f.catalogCache.Version()
}

// CatalogData returns the current cached catalog data.
// Returns nil if no catalog is cached.
func (f *FrictionCollector) CatalogData() *uxfriction.CatalogData {
	return f.catalogCache.Data()
}

// UpdateCatalog updates the catalog cache with new data.
// Returns true if the catalog was updated (version changed).
func (f *FrictionCollector) UpdateCatalog(catalog *uxfriction.CatalogData) (bool, error) {
	return f.catalogCache.Update(catalog)
}

// Stats returns current friction stats for status display.
func (f *FrictionCollector) Stats() FrictionStats {
	return FrictionStats{
		Enabled:        f.enabled,
		BufferCount:    f.buffer.Count(),
		BufferSize:     frictionBufferSize,
		SampleRate:     f.client.SampleRate(),
		RetryAfter:     f.client.RetryAfter(),
		CatalogVersion: f.catalogCache.Version(),
	}
}

// FrictionStats holds friction statistics for status display.
type FrictionStats struct {
	Enabled        bool      `json:"enabled"`
	BufferCount    int       `json:"buffer_count"`
	BufferSize     int       `json:"buffer_size"`
	SampleRate     float64   `json:"sample_rate"`
	RetryAfter     time.Time `json:"retry_after"`
	CatalogVersion string    `json:"catalog_version,omitempty"`
}
