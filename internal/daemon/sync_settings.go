package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/flags"
)

// SettingsFetcher periodically fetches CLI settings from the cloud API
// and caches them to disk for zero-latency CLI startup.
type SettingsFetcher struct {
	logger       *slog.Logger
	endpoint     string // normalized project endpoint
	getAuthToken func() string

	mu        sync.Mutex
	last      *flags.CLISettingsResponse
	lastFetch time.Time
	lastErr   error
}

// NewSettingsFetcher creates a fetcher for the given project endpoint.
func NewSettingsFetcher(logger *slog.Logger, projectEndpoint string) *SettingsFetcher {
	return &SettingsFetcher{
		logger:   logger,
		endpoint: endpoint.NormalizeEndpoint(projectEndpoint),
	}
}

// SetAuthTokenGetter wires the auth token source (typically from heartbeat).
func (f *SettingsFetcher) SetAuthTokenGetter(fn func() string) {
	f.getAuthToken = fn
}

// Fetch calls GET /api/v1/cli/settings and caches the result to disk.
// Safe to call concurrently; deduplicates within CLISettingsMaxAge.
func (f *SettingsFetcher) Fetch(ctx context.Context) {
	f.mu.Lock()
	if !f.lastFetch.IsZero() && time.Since(f.lastFetch) < flags.CLISettingsMaxAge {
		f.mu.Unlock()
		return // dedup: too soon since last fetch
	}
	f.lastFetch = time.Now()
	f.mu.Unlock()

	token := f.resolveAuthToken()
	if token == "" {
		f.logger.Debug("settings fetch skipped: no auth token")
		return
	}

	client := api.NewRepoClientWithEndpoint(f.endpoint).WithAuthToken(token)

	fetchCtx, cancel := context.WithTimeout(ctx, flags.CLISettingsFetchTimeout)
	defer cancel()
	_ = fetchCtx // timeout is on the HTTP request inside GetCLISettings

	rawJSON, err := client.GetCLISettings()
	if err != nil {
		if errors.Is(err, api.ErrCLISettingsUnsupported) {
			// Version-skew is expected during rolling server deployments. Keep
			// defaults/cached settings without emitting an operator-facing warning.
			f.logger.Debug("cli settings endpoint unsupported; using cached/default settings", "endpoint", f.endpoint)
			return
		}
		f.mu.Lock()
		f.lastErr = err
		f.mu.Unlock()
		f.logger.Warn("cli settings fetch failed", "endpoint", f.endpoint, "err", err)
		return
	}

	var resp flags.CLISettingsResponse
	if err := json.Unmarshal(rawJSON, &resp); err != nil {
		f.mu.Lock()
		f.lastErr = err
		f.mu.Unlock()
		f.logger.Warn("cli settings unmarshal failed", "err", err)
		return
	}
	resp.FetchedAt = time.Now()

	// cache to disk for CLI startup
	if err := flags.SaveCachedSettings(f.endpoint, &resp); err != nil {
		f.logger.Warn("cli settings cache write failed", "err", err)
	}

	f.mu.Lock()
	f.last = &resp
	f.lastErr = nil
	f.mu.Unlock()

	f.logger.Debug("cli settings fetched and cached", "endpoint", f.endpoint)
}

// CachedSettings returns the in-memory cached settings (may be nil if never fetched).
func (f *SettingsFetcher) CachedSettings() *flags.CLISettingsResponse {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.last
}

// resolveAuthToken gets the auth token from heartbeat or falls back to disk.
func (f *SettingsFetcher) resolveAuthToken() string {
	if f.getAuthToken != nil {
		if t := f.getAuthToken(); t != "" {
			return t
		}
	}
	// fall back to on-disk auth token
	tok, err := auth.GetTokenForEndpoint(f.endpoint)
	if err != nil || tok == nil {
		return ""
	}
	return tok.AccessToken
}
