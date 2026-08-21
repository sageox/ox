package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/version"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// useTestCacheDir redirects versionCacheFile to a temp directory for the
// duration of the test, preventing writes to the real ~/.cache/sageox/.
func useTestCacheDir(t *testing.T) {
	t.Helper()
	old := versionCacheFile
	versionCacheFile = filepath.Join(t.TempDir(), "version-check.json")
	t.Cleanup(func() { versionCacheFile = old })
}

func writeTestVersionCache(t *testing.T, data *versionCacheData) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(versionCacheFile), 0700))
	raw, err := json.MarshalIndent(data, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(versionCacheFile, raw, 0600))
}

// core semver comparison logic — the double-digit case (0.12 vs 0.9) catches
// lexicographic-vs-numeric bugs
func TestIsNewerVersion(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		a, b string
		want bool
	}{
		{name: "newer minor", a: "0.10.0", b: "0.9.0", want: true},
		{name: "same version", a: "0.9.0", b: "0.9.0", want: false},
		{name: "older minor", a: "0.8.0", b: "0.9.0", want: false},
		{name: "double digit minor", a: "0.12.0", b: "0.9.0", want: true},
		{name: "newer patch", a: "0.9.1", b: "0.9.0", want: true},
		{name: "newer major", a: "1.0.0", b: "0.9.0", want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, isNewerVersion(tt.a, tt.b))
		})
	}
}

// newer version in cache should produce an update result with v-prefix stripped
func TestCheckVersionFromCache_NewerVersion(t *testing.T) {
	useTestCacheDir(t)
	writeTestVersionCache(t, &versionCacheData{
		LatestVersion: "v99.0.0",
		CheckedAt:     time.Now(),
	})

	result := checkVersionFromCache()
	require.NotNil(t, result)
	assert.True(t, result.UpdateAvailable)
	assert.Equal(t, "99.0.0", result.LatestVersion, "v prefix should be stripped")
	assert.Equal(t, version.Version, result.CurrentVersion)
}

// same or older version must not report an update — false positives would nag users
func TestCheckVersionFromCache_NoUpdateWhenCurrentOrNewer(t *testing.T) {
	useTestCacheDir(t)
	for _, latestVersion := range []string{"v" + version.Version, "v0.0.1"} {
		writeTestVersionCache(t, &versionCacheData{
			LatestVersion: latestVersion,
			CheckedAt:     time.Now(),
		})
		result := checkVersionFromCache()
		assert.Nil(t, result, "should not report update for cached version %s", latestVersion)
	}
}

// doctor writes cache as side effect — must preserve daemon's ETag or
// the next conditional request loses its advantage
func TestWriteVersionCacheFromDoctor_PreservesETag(t *testing.T) {
	useTestCacheDir(t)
	writeTestVersionCache(t, &versionCacheData{
		LatestVersion: "v0.8.0",
		CheckedAt:     time.Now().Add(-1 * time.Hour),
		ETag:          `"preserve-me"`,
	})

	writeVersionCacheFromDoctor("v0.9.0")

	cached := readVersionCache()
	require.NotNil(t, cached)
	assert.Equal(t, "v0.9.0", cached.LatestVersion)
	assert.Equal(t, `"preserve-me"`, cached.ETag, "doctor must not clobber daemon's ETag")
}

// A fresh cache must NOT trigger a live GitHub refetch. Proven with a sentinel
// version (v99.0.0) that could never come from the real API: if a refetch
// happened it would overwrite the cache with the real latest and the sentinel
// update would vanish. Also keeps the test hermetic (no network).
func TestRefreshVersionCacheIfStale_FreshCacheSkipsNetwork(t *testing.T) {
	useTestCacheDir(t)
	writeTestVersionCache(t, &versionCacheData{
		LatestVersion: "v99.0.0",
		CheckedAt:     time.Now(),
	})

	result := refreshVersionCacheIfStale(6 * time.Hour)
	require.NotNil(t, result)
	assert.Equal(t, "99.0.0", result.LatestVersion, "fresh cache must be used verbatim, not refetched")

	// the on-disk sentinel must be untouched (no write occurred)
	cached := readVersionCache()
	require.NotNil(t, cached)
	assert.Equal(t, "v99.0.0", cached.LatestVersion)
}

// A stale cache must trigger a refetch and adopt the newly fetched version,
// without touching the network in the test (fetcher is injected).
func TestRefreshVersionCacheIfStale_StaleCacheRefetches(t *testing.T) {
	useTestCacheDir(t)
	writeTestVersionCache(t, &versionCacheData{
		LatestVersion: "v0.0.1", // older than current; would report no update
		CheckedAt:     time.Now().Add(-24 * time.Hour),
	})

	oldFetcher := latestReleaseFetcher
	fetched := false
	latestReleaseFetcher = func() (string, error) {
		fetched = true
		return "v99.0.0", nil
	}
	t.Cleanup(func() { latestReleaseFetcher = oldFetcher })

	result := refreshVersionCacheIfStale(6 * time.Hour)
	assert.True(t, fetched, "stale cache must trigger a refetch")
	require.NotNil(t, result, "refetched newer version should report an update")
	assert.Equal(t, "99.0.0", result.LatestVersion)

	cached := readVersionCache()
	require.NotNil(t, cached)
	assert.Equal(t, "v99.0.0", cached.LatestVersion, "cache must be rewritten with the fetched version")
}

// corrupt cache must not crash prime — graceful degradation
func TestCheckVersionFromCache_CorruptFile(t *testing.T) {
	useTestCacheDir(t)
	require.NoError(t, os.MkdirAll(filepath.Dir(versionCacheFile), 0700))
	require.NoError(t, os.WriteFile(versionCacheFile, []byte("{{bad json"), 0600))

	assert.Nil(t, checkVersionFromCache())
}
