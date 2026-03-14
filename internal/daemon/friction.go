package daemon

import (
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/useragent"
	"github.com/sageox/ox/internal/version"

	friction "github.com/sageox/frictionax"
)

const (
	// frictionDefaultEndpoint is the fallback friction API endpoint when no project
	// endpoint is configured. Matches endpoint.Default.
	frictionDefaultEndpoint = "https://sageox.ai"
)

// FrictionCollector manages friction event buffering and transmission to the cloud.
// It delegates to a frictionax.Friction instance which handles the ring buffer,
// background flush loop, rate limiting, and catalog caching internally.
type FrictionCollector struct {
	mu           sync.Mutex
	engine       *friction.Friction
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
	// precedence: env var > project endpoint > default
	ep := os.Getenv("SAGEOX_FRICTION_ENDPOINT")
	if ep == "" {
		ep = projectEndpoint
	}
	if ep == "" {
		ep = frictionDefaultEndpoint
	}

	fc := &FrictionCollector{
		logger: logger,
	}

	fc.engine = friction.New(nil, // nil adapter: daemon only records events, never parses CLI errors
		friction.WithCatalog("ox"),
		friction.WithTelemetry(ep, version.Version),
		friction.WithAuth(func() string {
			if fc.getAuthToken != nil {
				return fc.getAuthToken()
			}
			return ""
		}),
		friction.WithCachePath(catalogCacheFile()),
		friction.WithRequestDecorator(func(r *http.Request) {
			r.Header.Set("User-Agent", useragent.DaemonString())
		}),
		friction.WithIsEnabled(func() bool { return isFrictionEnabled() }),
		friction.WithLogger(logger),
	)

	return fc
}

// catalogCacheFile returns the path to the catalog cache file.
func catalogCacheFile() string {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return cacheDir + "/sageox/friction-catalog.json"
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
	if os.Getenv("DO_NOT_TRACK") == "1" {
		return false
	}
	if strings.ToLower(os.Getenv("SAGEOX_FRICTION")) == "false" {
		return false
	}
	if cfg, err := config.LoadUserConfig(); err == nil {
		return cfg.IsTelemetryEnabled()
	}
	return true
}

// Start begins background processing of friction events.
// frictionax starts its background sender automatically in New(), so this
// is retained for API compatibility but is effectively a no-op.
func (f *FrictionCollector) Start() {
	// frictionax collector is started automatically during New()
}

// Stop gracefully shuts down the friction collector.
// Performs a final flush before returning. Safe to call multiple times.
func (f *FrictionCollector) Stop() {
	if f.engine != nil {
		f.engine.Close()
	}
}

// RecordFromIPC adds a friction event from an IPC payload.
func (f *FrictionCollector) RecordFromIPC(payload FrictionPayload) {
	event := friction.FrictionEvent{
		Timestamp:  payload.Timestamp,
		Kind:       friction.FailureKind(payload.Kind),
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
func (f *FrictionCollector) Record(event friction.FrictionEvent) {
	if !isFrictionEnabled() || f.engine == nil {
		return
	}

	if event.Timestamp == "" {
		event.Timestamp = time.Now().UTC().Format(time.RFC3339)
	}

	f.engine.Record(event)
}

// IsEnabled returns whether friction collection is enabled.
func (f *FrictionCollector) IsEnabled() bool {
	return isFrictionEnabled()
}

// Stats returns current friction stats for status display.
func (f *FrictionCollector) Stats() FrictionStats {
	enabled := isFrictionEnabled()
	if f.engine == nil {
		return FrictionStats{Enabled: enabled}
	}
	s := f.engine.Stats()
	return FrictionStats{
		Enabled:        enabled,
		BufferCount:    s.BufferCount,
		BufferSize:     s.BufferSize,
		SampleRate:     s.SampleRate,
		CatalogVersion: s.CatalogVersion,
	}
}

// FrictionStats holds friction statistics for status display.
type FrictionStats struct {
	Enabled        bool    `json:"enabled"`
	BufferCount    int     `json:"buffer_count"`
	BufferSize     int     `json:"buffer_size"`
	SampleRate     float64 `json:"sample_rate"`
	CatalogVersion string  `json:"catalog_version,omitempty"`
}
