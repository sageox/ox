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

func versionCachePath() string {
	return filepath.Join(paths.CacheDir(), "version-check.json")
}

func writeTestVersionCache(t *testing.T, data *versionCacheData) {
	t.Helper()
	cachePath := versionCachePath()
	require.NoError(t, os.MkdirAll(filepath.Dir(cachePath), 0700))
	raw, err := json.MarshalIndent(data, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(cachePath, raw, 0600))
	t.Cleanup(func() { os.Remove(cachePath) })
}

func removeVersionCache(t *testing.T) {
	t.Helper()
	os.Remove(versionCachePath())
	t.Cleanup(func() { os.Remove(versionCachePath()) })
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

// corrupt cache must not crash prime — graceful degradation
func TestCheckVersionFromCache_CorruptFile(t *testing.T) {
	cachePath := versionCachePath()
	require.NoError(t, os.MkdirAll(filepath.Dir(cachePath), 0700))
	require.NoError(t, os.WriteFile(cachePath, []byte("{{bad json"), 0600))
	t.Cleanup(func() { os.Remove(cachePath) })

	assert.Nil(t, checkVersionFromCache())
}
