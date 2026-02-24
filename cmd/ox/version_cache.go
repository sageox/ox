package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sageox/ox/internal/paths"
	"github.com/sageox/ox/internal/version"
)

// versionCacheData matches the daemon's cache format for latest version info.
type versionCacheData struct {
	LatestVersion string    `json:"latest_version"`
	CheckedAt     time.Time `json:"checked_at"`
	ETag          string    `json:"etag,omitempty"`
}

// versionCheckResult holds the outcome of comparing cached version against current.
type versionCheckResult struct {
	UpdateAvailable bool
	LatestVersion   string
	CurrentVersion  string
}

// readVersionCache reads the daemon-written version cache file.
// Returns nil on any error (missing file, corrupt JSON, etc.).
func readVersionCache() *versionCacheData {
	data, err := os.ReadFile(filepath.Join(paths.CacheDir(), "version-check.json"))
	if err != nil {
		return nil
	}
	var cache versionCacheData
	if err := json.Unmarshal(data, &cache); err != nil {
		return nil
	}
	return &cache
}

// writeVersionCacheFromDoctor writes the version cache as a side effect of doctor's
// live GitHub check. This warms the cache for prime even when the daemon isn't running.
func writeVersionCacheFromDoctor(latestVersion string) {
	cachePath := filepath.Join(paths.CacheDir(), "version-check.json")

	// read existing cache to preserve ETag if present
	existing := readVersionCache()
	data := &versionCacheData{
		LatestVersion: latestVersion,
		CheckedAt:     time.Now(),
	}
	if existing != nil {
		data.ETag = existing.ETag
	}

	raw, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return
	}

	dir := filepath.Dir(cachePath)
	_ = os.MkdirAll(dir, 0700)
	tmpPath := cachePath + ".tmp"
	if err := os.WriteFile(tmpPath, raw, 0600); err != nil {
		return
	}
	_ = os.Rename(tmpPath, cachePath)
}

// checkVersionFromCache reads the version cache and returns an update result
// if a newer version is available. Returns nil if no cache exists or no
// update is available.
func checkVersionFromCache() *versionCheckResult {
	cached := readVersionCache()
	if cached == nil {
		return nil
	}

	latest := strings.TrimPrefix(cached.LatestVersion, "v")
	current := strings.TrimPrefix(version.Version, "v")

	if latest == "" || !isNewerVersion(latest, current) {
		return nil
	}

	return &versionCheckResult{
		UpdateAvailable: true,
		LatestVersion:   latest,
		CurrentVersion:  current,
	}
}
