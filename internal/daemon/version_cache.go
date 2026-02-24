package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/sageox/ox/internal/logger"
	"github.com/sageox/ox/internal/paths"
	"github.com/sageox/ox/internal/useragent"
)

// VersionCacheData holds the cached latest release version from GitHub.
type VersionCacheData struct {
	LatestVersion string    `json:"latest_version"`
	CheckedAt     time.Time `json:"checked_at"`
	ETag          string    `json:"etag,omitempty"`
}

// VersionCache manages the GitHub release version cache on disk.
// Thread-safe for concurrent access.
type VersionCache struct {
	mu       sync.RWMutex
	data     *VersionCacheData
	filePath string
	logger   *slog.Logger

	// injectable for testing
	httpClient *http.Client
	apiURL     string
}

// NewVersionCache creates a new version cache.
// The cache file is stored at ~/.cache/sageox/version-check.json (or XDG equivalent).
func NewVersionCache(log *slog.Logger) *VersionCache {
	return &VersionCache{
		filePath:   paths.CacheDir() + "/version-check.json",
		logger:     log,
		httpClient: http.DefaultClient,
		apiURL:     "https://api.github.com/repos/sageox/ox/releases/latest",
	}
}

// Load reads the version cache from disk if it exists.
// Returns nil error if file doesn't exist (empty cache is valid).
// Safe to call concurrently.
func (v *VersionCache) Load() error {
	v.mu.Lock()
	defer v.mu.Unlock()

	raw, err := os.ReadFile(v.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			v.data = nil
			return nil
		}
		return fmt.Errorf("read version cache: %w", err)
	}

	if len(raw) == 0 {
		v.data = nil
		return nil
	}

	var data VersionCacheData
	if err := json.Unmarshal(raw, &data); err != nil {
		// corrupted cache, reset it
		v.data = nil
		return nil
	}

	v.data = &data
	return nil
}

// Save writes the version cache to disk atomically.
// Creates the cache directory if it doesn't exist.
// Safe to call concurrently.
func (v *VersionCache) Save(data *VersionCacheData) error {
	if data == nil {
		return nil
	}

	v.mu.Lock()
	defer v.mu.Unlock()

	if _, err := paths.EnsureDirForFile(v.filePath); err != nil {
		return fmt.Errorf("create cache directory: %w", err)
	}

	raw, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal version cache: %w", err)
	}

	tmpFile := v.filePath + ".tmp"
	if err := os.WriteFile(tmpFile, raw, 0600); err != nil {
		return fmt.Errorf("write temp file: %w", err)
	}

	if err := os.Rename(tmpFile, v.filePath); err != nil {
		os.Remove(tmpFile)
		return fmt.Errorf("rename temp file: %w", err)
	}

	v.data = data
	return nil
}

// CheckAndUpdate fetches the latest release from GitHub and updates the cache.
// Uses ETag conditional requests to avoid unnecessary data transfer.
// Safe to call concurrently.
func (v *VersionCache) CheckAndUpdate(ctx context.Context) error {
	checkCtx, cancel := context.WithTimeout(ctx, 1*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(checkCtx, "GET", v.apiURL, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("User-Agent", useragent.DaemonString())

	// send conditional request if we have a cached ETag
	v.mu.RLock()
	cachedETag := ""
	if v.data != nil {
		cachedETag = v.data.ETag
	}
	v.mu.RUnlock()

	if cachedETag != "" {
		req.Header.Set("If-None-Match", cachedETag)
	}

	logger.LogHTTPRequest("GET", v.apiURL)
	start := time.Now()

	resp, err := v.httpClient.Do(req)
	duration := time.Since(start)
	if err != nil {
		logger.LogHTTPError("GET", v.apiURL, err, duration)
		return fmt.Errorf("fetch latest release: %w", err)
	}
	defer resp.Body.Close()

	logger.LogHTTPResponse("GET", v.apiURL, resp.StatusCode, duration)

	switch resp.StatusCode {
	case http.StatusNotModified:
		// version unchanged, just update the check timestamp
		v.logger.Debug("version check: not modified", "etag", cachedETag)
		v.mu.Lock()
		if v.data != nil {
			updated := *v.data
			updated.CheckedAt = time.Now()
			v.data = &updated
		}
		v.mu.Unlock()
		return nil

	case http.StatusOK:
		var release struct {
			TagName string `json:"tag_name"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
			return fmt.Errorf("decode release response: %w", err)
		}

		newData := &VersionCacheData{
			LatestVersion: release.TagName,
			CheckedAt:     time.Now(),
			ETag:          resp.Header.Get("ETag"),
		}

		v.logger.Info("version check: fetched latest", "version", release.TagName)
		return v.Save(newData)

	default:
		v.logger.Warn("version check: unexpected status", "status", resp.StatusCode)
		return fmt.Errorf("github api returned %d", resp.StatusCode)
	}
}

// Data returns a copy of the cached version data.
// Returns nil if no data is cached.
// Safe to call concurrently.
func (v *VersionCache) Data() *VersionCacheData {
	v.mu.RLock()
	defer v.mu.RUnlock()

	if v.data == nil {
		return nil
	}

	dataCopy := *v.data
	return &dataCopy
}
