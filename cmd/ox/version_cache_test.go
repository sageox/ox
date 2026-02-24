package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/paths"
	"github.com/sageox/ox/internal/version"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// versionCachePath returns the canonical version cache file path.
// Mirrors the path used by readVersionCache / writeVersionCacheFromDoctor.
func versionCachePath() string {
	return filepath.Join(paths.CacheDir(), "version-check.json")
}

// writeTestVersionCache writes a versionCacheData to the canonical cache path.
// Returns a cleanup function that removes the file.
func writeTestVersionCache(t *testing.T, data *versionCacheData) {
	t.Helper()
	cachePath := versionCachePath()
	dir := filepath.Dir(cachePath)
	require.NoError(t, os.MkdirAll(dir, 0700))

	raw, err := json.MarshalIndent(data, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(cachePath, raw, 0600))

	t.Cleanup(func() {
		os.Remove(cachePath)
	})
}

// removeVersionCache ensures the version cache file does not exist.
func removeVersionCache(t *testing.T) {
	t.Helper()
	os.Remove(versionCachePath())
	t.Cleanup(func() {
		os.Remove(versionCachePath())
	})
}

func TestIsNewerVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		a    string
		b    string
		want bool
	}{
		{name: "newer major", a: "1.0.0", b: "0.9.0", want: true},
		{name: "newer minor", a: "0.10.0", b: "0.9.0", want: true},
		{name: "newer patch", a: "0.9.1", b: "0.9.0", want: true},
		{name: "same version", a: "0.9.0", b: "0.9.0", want: false},
		{name: "older major", a: "0.9.0", b: "1.0.0", want: false},
		{name: "older minor", a: "0.8.0", b: "0.9.0", want: false},
		{name: "older patch", a: "0.9.0", b: "0.9.1", want: false},
		{name: "a has more parts", a: "0.9.0.1", b: "0.9.0", want: true},
		{name: "b has more parts", a: "0.9.0", b: "0.9.0.1", want: false},
		{name: "double digit minor", a: "0.12.0", b: "0.9.0", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := isNewerVersion(tt.a, tt.b)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestCheckVersionFromCache_NoFile(t *testing.T) {
	removeVersionCache(t)

	result := checkVersionFromCache()
	assert.Nil(t, result)
}

func TestCheckVersionFromCache_NewerVersion(t *testing.T) {
	// write a cache claiming a version higher than current
	writeTestVersionCache(t, &versionCacheData{
		LatestVersion: "v99.0.0",
		CheckedAt:     time.Now(),
	})

	result := checkVersionFromCache()
	require.NotNil(t, result)
	assert.True(t, result.UpdateAvailable)
	assert.Equal(t, "99.0.0", result.LatestVersion)
	assert.Equal(t, version.Version, result.CurrentVersion)
}

func TestCheckVersionFromCache_SameVersion(t *testing.T) {
	writeTestVersionCache(t, &versionCacheData{
		LatestVersion: "v" + version.Version,
		CheckedAt:     time.Now(),
	})

	result := checkVersionFromCache()
	assert.Nil(t, result, "same version should not report an update")
}

func TestCheckVersionFromCache_OlderVersion(t *testing.T) {
	writeTestVersionCache(t, &versionCacheData{
		LatestVersion: "v0.0.1",
		CheckedAt:     time.Now(),
	})

	result := checkVersionFromCache()
	assert.Nil(t, result, "older cached version should not report an update")
}

func TestCheckVersionFromCache_EmptyLatestVersion(t *testing.T) {
	writeTestVersionCache(t, &versionCacheData{
		LatestVersion: "",
		CheckedAt:     time.Now(),
	})

	result := checkVersionFromCache()
	assert.Nil(t, result, "empty latest version should not report an update")
}

func TestCheckVersionFromCache_CorruptFile(t *testing.T) {
	cachePath := versionCachePath()
	require.NoError(t, os.MkdirAll(filepath.Dir(cachePath), 0700))
	require.NoError(t, os.WriteFile(cachePath, []byte("{{bad json"), 0600))
	t.Cleanup(func() { os.Remove(cachePath) })

	result := checkVersionFromCache()
	assert.Nil(t, result, "corrupt cache should return nil")
}

func TestWriteVersionCacheFromDoctor(t *testing.T) {
	// ensure clean state
	removeVersionCache(t)

	writeVersionCacheFromDoctor("v1.2.3")

	// verify the file was written and is readable
	cached := readVersionCache()
	require.NotNil(t, cached)
	assert.Equal(t, "v1.2.3", cached.LatestVersion)
	assert.WithinDuration(t, time.Now(), cached.CheckedAt, 5*time.Second)
}

func TestWriteVersionCacheFromDoctor_PreservesETag(t *testing.T) {
	// write initial cache with an ETag
	writeTestVersionCache(t, &versionCacheData{
		LatestVersion: "v0.8.0",
		CheckedAt:     time.Now().Add(-1 * time.Hour),
		ETag:          `"preserve-me"`,
	})

	// doctor writes a new version — should preserve the existing ETag
	writeVersionCacheFromDoctor("v0.9.0")

	cached := readVersionCache()
	require.NotNil(t, cached)
	assert.Equal(t, "v0.9.0", cached.LatestVersion)
	assert.Equal(t, `"preserve-me"`, cached.ETag)
}

func TestReadVersionCache_ValidFile(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	writeTestVersionCache(t, &versionCacheData{
		LatestVersion: "v0.7.0",
		CheckedAt:     now,
		ETag:          `W/"weak"`,
	})

	cached := readVersionCache()
	require.NotNil(t, cached)
	assert.Equal(t, "v0.7.0", cached.LatestVersion)
	assert.Equal(t, `W/"weak"`, cached.ETag)
}

func TestReadVersionCache_MissingFile(t *testing.T) {
	removeVersionCache(t)

	cached := readVersionCache()
	assert.Nil(t, cached)
}
