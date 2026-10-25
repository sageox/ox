package main

import (
	"strings"
	"time"

	"github.com/sageox/ox/internal/updatenotice"
	"github.com/sageox/ox/internal/version"
)

// versionCacheData is the on-disk version cache plus the notice ledger.
// Aliased rather than redeclared so exactly one struct describes this file — a
// second copy is how a writer silently drops the ledger fields and restores the
// per-command nag.
type versionCacheData = updatenotice.Data

// versionCheckResult holds the outcome of comparing cached version against current.
type versionCheckResult struct {
	UpdateAvailable bool
	LatestVersion   string
	CurrentVersion  string
}

// readVersionCache reads the version cache file.
// Returns nil on any error (missing file, corrupt JSON, etc.).
func readVersionCache() *versionCacheData {
	return updatenotice.Read()
}

// writeVersionCacheFromDoctor writes the version cache as a side effect of doctor's
// live GitHub check. This warms the cache for prime even when the daemon isn't running.
func writeVersionCacheFromDoctor(latestVersion string) {
	data := &versionCacheData{
		LatestVersion: latestVersion,
		CheckedAt:     time.Now(),
	}
	// Preserve the ETag (so the next conditional request keeps its advantage)
	// AND the notice ledger: a routine version refresh must not erase the
	// coworker's memory of having already been told.
	if existing := updatenotice.Read(); existing != nil {
		data.ETag = existing.ETag
		updatenotice.CarryLedger(data, existing)
	}
	_ = updatenotice.Write(data)
}

// clearVersionCacheAfterUpgrade drops the cache — ledger included — after a
// successful `ox upgrade`. Both must go: the running process still reports its
// OLD compiled-in version, so a surviving cache would keep claiming an update
// is available, and a surviving ledger would suppress the FIRST notice about
// the next release line.
func clearVersionCacheAfterUpgrade() {
	updatenotice.Reset()
}

// refreshVersionCacheIfStale ensures the version cache is reasonably fresh
// WITHOUT depending on the daemon. If the cache is missing or older than
// maxAge, it performs one bounded live GitHub check (getLatestGitHubRelease
// caps itself at 1s) and rewrites the cache. Any network failure is silent —
// callers fall back to whatever the cache already holds. This closes the gap
// where a coworker who never runs the daemon never learns an update exists.
func refreshVersionCacheIfStale(maxAge time.Duration) *versionCheckResult {
	cached := readVersionCache()
	if cached == nil || time.Since(cached.CheckedAt) > maxAge {
		if tag, err := latestReleaseFetcher(); err == nil && tag != "" {
			writeVersionCacheFromDoctor(tag)
		}
	}
	return checkVersionFromCache()
}

// latestReleaseFetcher fetches the latest release tag; indirected so tests can
// exercise the stale-cache refetch path without reaching GitHub.
var latestReleaseFetcher = getLatestGitHubRelease

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
