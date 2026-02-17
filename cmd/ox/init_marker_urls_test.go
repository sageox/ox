//go:build !short

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/endpoint"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRepoMarkerURLs_IsExpired(t *testing.T) {
	tests := []struct {
		name         string
		urlsCachedAt time.Time
		wantExpired  bool
	}{
		{
			name:         "zero time is expired",
			urlsCachedAt: time.Time{},
			wantExpired:  true,
		},
		{
			name:         "recent cache is not expired",
			urlsCachedAt: time.Now().Add(-1 * time.Hour),
			wantExpired:  false,
		},
		{
			name:         "6 day old cache is not expired",
			urlsCachedAt: time.Now().Add(-6 * 24 * time.Hour),
			wantExpired:  false,
		},
		{
			name:         "8 day old cache is expired",
			urlsCachedAt: time.Now().Add(-8 * 24 * time.Hour),
			wantExpired:  true,
		},
		{
			name:         "exactly 7 days is expired",
			urlsCachedAt: time.Now().Add(-URLCacheExpiry - time.Second),
			wantExpired:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			urls := &repoMarkerURLs{
				URLsCachedAt: tt.urlsCachedAt,
			}
			assert.Equal(t, tt.wantExpired, urls.IsExpired())
		})
	}
}

func TestReadMarkerURLsWithExpiry(t *testing.T) {
	tmpDir := t.TempDir()
	sageoxDir := filepath.Join(tmpDir, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755))

	currentEndpoint := endpoint.Get()

	t.Run("returns nil for expired cache", func(t *testing.T) {
		// create marker with old timestamp
		oldTime := time.Now().Add(-8 * 24 * time.Hour).UTC().Format(time.RFC3339)
		marker := repoMarkerWithURLs{
			RepoID:       "repo_test123",
			Endpoint:     currentEndpoint,
			LedgerURL:    "https://git.example.com/ledger.git",
			URLsCachedAt: oldTime,
		}
		data, _ := json.Marshal(marker)
		markerPath := filepath.Join(sageoxDir, ".repo_test123")
		require.NoError(t, os.WriteFile(markerPath, data, 0644))

		urls, err := ReadMarkerURLsWithExpiry(sageoxDir, currentEndpoint)
		require.NoError(t, err)
		assert.Nil(t, urls, "expired cache should return nil")
	})

	t.Run("returns URLs for fresh cache", func(t *testing.T) {
		// create marker with recent timestamp
		recentTime := time.Now().Add(-1 * time.Hour).UTC().Format(time.RFC3339)
		marker := repoMarkerWithURLs{
			RepoID:       "repo_fresh456",
			Endpoint:     currentEndpoint,
			LedgerURL:    "https://git.example.com/ledger-fresh.git",
			TeamURLs:     map[string]string{"team1": "https://git.example.com/team1.git"},
			URLsCachedAt: recentTime,
		}
		data, _ := json.Marshal(marker)
		markerPath := filepath.Join(sageoxDir, ".repo_fresh456")
		require.NoError(t, os.WriteFile(markerPath, data, 0644))

		urls, err := ReadMarkerURLsWithExpiry(sageoxDir, currentEndpoint)
		require.NoError(t, err)
		require.NotNil(t, urls)
		assert.Equal(t, "https://git.example.com/ledger-fresh.git", urls.LedgerURL)
		assert.Equal(t, "https://git.example.com/team1.git", urls.TeamURLs["team1"])
	})
}

func TestReadMarkerURLs_NoExpiry(t *testing.T) {
	tmpDir := t.TempDir()
	sageoxDir := filepath.Join(tmpDir, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755))

	currentEndpoint := endpoint.Get()

	// create marker with old timestamp
	oldTime := time.Now().Add(-30 * 24 * time.Hour).UTC().Format(time.RFC3339)
	marker := repoMarkerWithURLs{
		RepoID:       "repo_old789",
		Endpoint:     currentEndpoint,
		LedgerURL:    "https://git.example.com/ledger-old.git",
		URLsCachedAt: oldTime,
	}
	data, _ := json.Marshal(marker)
	markerPath := filepath.Join(sageoxDir, ".repo_old789")
	require.NoError(t, os.WriteFile(markerPath, data, 0644))

	// ReadMarkerURLs should return URLs regardless of age
	urls, err := ReadMarkerURLs(sageoxDir, currentEndpoint)
	require.NoError(t, err)
	require.NotNil(t, urls, "ReadMarkerURLs should return URLs regardless of expiry")
	assert.Equal(t, "https://git.example.com/ledger-old.git", urls.LedgerURL)
}

func TestGetCachedLedgerURL(t *testing.T) {
	tmpDir := t.TempDir()
	sageoxDir := filepath.Join(tmpDir, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755))

	currentEndpoint := endpoint.Get()

	t.Run("returns empty for non-existent dir", func(t *testing.T) {
		url := GetCachedLedgerURL("/nonexistent/dir", currentEndpoint)
		assert.Empty(t, url)
	})

	t.Run("returns empty for expired cache", func(t *testing.T) {
		oldTime := time.Now().Add(-8 * 24 * time.Hour).UTC().Format(time.RFC3339)
		marker := repoMarkerWithURLs{
			RepoID:       "repo_expired",
			Endpoint:     currentEndpoint,
			LedgerURL:    "https://git.example.com/expired.git",
			URLsCachedAt: oldTime,
		}
		data, _ := json.Marshal(marker)
		require.NoError(t, os.WriteFile(filepath.Join(sageoxDir, ".repo_expired"), data, 0644))

		url := GetCachedLedgerURL(sageoxDir, currentEndpoint)
		assert.Empty(t, url, "should return empty for expired cache")
	})

	t.Run("returns URL for fresh cache", func(t *testing.T) {
		freshTime := time.Now().UTC().Format(time.RFC3339)
		marker := repoMarkerWithURLs{
			RepoID:       "repo_cached",
			Endpoint:     currentEndpoint,
			LedgerURL:    "https://git.example.com/cached.git",
			URLsCachedAt: freshTime,
		}
		data, _ := json.Marshal(marker)
		require.NoError(t, os.WriteFile(filepath.Join(sageoxDir, ".repo_cached"), data, 0644))

		url := GetCachedLedgerURL(sageoxDir, currentEndpoint)
		assert.Equal(t, "https://git.example.com/cached.git", url)
	})
}

func TestGetCachedTeamURL(t *testing.T) {
	tmpDir := t.TempDir()
	sageoxDir := filepath.Join(tmpDir, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755))

	currentEndpoint := endpoint.Get()

	freshTime := time.Now().UTC().Format(time.RFC3339)
	marker := repoMarkerWithURLs{
		RepoID:   "repo_team",
		Endpoint: currentEndpoint,
		TeamURLs: map[string]string{
			"team-abc": "https://git.example.com/team-abc.git",
			"team-xyz": "https://git.example.com/team-xyz.git",
		},
		URLsCachedAt: freshTime,
	}
	data, _ := json.Marshal(marker)
	require.NoError(t, os.WriteFile(filepath.Join(sageoxDir, ".repo_team"), data, 0644))

	t.Run("returns URL for existing team", func(t *testing.T) {
		url := GetCachedTeamURL(sageoxDir, currentEndpoint, "team-abc")
		assert.Equal(t, "https://git.example.com/team-abc.git", url)
	})

	t.Run("returns empty for non-existent team", func(t *testing.T) {
		url := GetCachedTeamURL(sageoxDir, currentEndpoint, "team-notfound")
		assert.Empty(t, url)
	})

	t.Run("returns empty for empty team ID", func(t *testing.T) {
		url := GetCachedTeamURL(sageoxDir, currentEndpoint, "")
		assert.Empty(t, url)
	})
}

func TestURLsCachedAtField(t *testing.T) {
	tmpDir := t.TempDir()
	sageoxDir := filepath.Join(tmpDir, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755))

	currentEndpoint := endpoint.Get()

	// create a marker and verify urls_cached_at is read correctly
	testTime := time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC)
	marker := repoMarkerWithURLs{
		RepoID:       "repo_timestamp",
		Endpoint:     currentEndpoint,
		LedgerURL:    "https://git.example.com/ledger.git",
		URLsCachedAt: testTime.Format(time.RFC3339),
	}
	data, _ := json.Marshal(marker)
	require.NoError(t, os.WriteFile(filepath.Join(sageoxDir, ".repo_timestamp"), data, 0644))

	urls, err := ReadMarkerURLs(sageoxDir, currentEndpoint)
	require.NoError(t, err)
	require.NotNil(t, urls)

	// verify the timestamp was parsed correctly
	assert.Equal(t, testTime.Year(), urls.URLsCachedAt.Year())
	assert.Equal(t, testTime.Month(), urls.URLsCachedAt.Month())
	assert.Equal(t, testTime.Day(), urls.URLsCachedAt.Day())
}

func TestShouldRefreshCachedURLs(t *testing.T) {
	tmpDir := t.TempDir()
	sageoxDir := filepath.Join(tmpDir, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755))

	currentEndpoint := endpoint.Get()

	t.Run("returns true for empty dir", func(t *testing.T) {
		emptyDir := filepath.Join(tmpDir, "empty")
		require.NoError(t, os.MkdirAll(emptyDir, 0755))
		assert.True(t, ShouldRefreshCachedURLs(emptyDir, currentEndpoint))
	})

	t.Run("returns true for expired cache", func(t *testing.T) {
		oldTime := time.Now().Add(-8 * 24 * time.Hour).UTC().Format(time.RFC3339)
		marker := repoMarkerWithURLs{
			RepoID:       "repo_shouldrefresh",
			Endpoint:     currentEndpoint,
			LedgerURL:    "https://git.example.com/old.git",
			URLsCachedAt: oldTime,
		}
		data, _ := json.Marshal(marker)
		require.NoError(t, os.WriteFile(filepath.Join(sageoxDir, ".repo_shouldrefresh"), data, 0644))

		assert.True(t, ShouldRefreshCachedURLs(sageoxDir, currentEndpoint))
	})

	t.Run("returns false for fresh cache", func(t *testing.T) {
		freshDir := filepath.Join(tmpDir, ".sageox_fresh")
		require.NoError(t, os.MkdirAll(freshDir, 0755))

		freshTime := time.Now().UTC().Format(time.RFC3339)
		marker := repoMarkerWithURLs{
			RepoID:       "repo_norefresh",
			Endpoint:     currentEndpoint,
			LedgerURL:    "https://git.example.com/fresh.git",
			URLsCachedAt: freshTime,
		}
		data, _ := json.Marshal(marker)
		require.NoError(t, os.WriteFile(filepath.Join(freshDir, ".repo_norefresh"), data, 0644))

		assert.False(t, ShouldRefreshCachedURLs(freshDir, currentEndpoint))
	})
}

// TestReadMarkerURLs_PrefixedMarkerEndpoint tests that a marker with a prefixed endpoint
// is found when the target endpoint is normalized.
func TestReadMarkerURLs_PrefixedMarkerEndpoint(t *testing.T) {
	tmpDir := t.TempDir()
	sageoxDir := filepath.Join(tmpDir, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755))

	freshTime := time.Now().UTC().Format(time.RFC3339)
	marker := repoMarkerWithURLs{
		RepoID:       "repo_prefixed",
		Endpoint:     "https://api.sageox.ai",
		LedgerURL:    "https://git.example.com/prefixed.git",
		URLsCachedAt: freshTime,
	}
	data, _ := json.Marshal(marker)
	require.NoError(t, os.WriteFile(filepath.Join(sageoxDir, ".repo_prefixed"), data, 0644))

	// search with normalized endpoint
	urls, err := ReadMarkerURLs(sageoxDir, "https://sageox.ai")
	require.NoError(t, err)
	require.NotNil(t, urls, "should find marker with prefixed endpoint via normalized target")
	assert.Equal(t, "https://git.example.com/prefixed.git", urls.LedgerURL)
}

// TestReadMarkerURLs_PrefixedTargetEndpoint tests that a marker with a normalized endpoint
// is found when the target endpoint has a prefix.
func TestReadMarkerURLs_PrefixedTargetEndpoint(t *testing.T) {
	tmpDir := t.TempDir()
	sageoxDir := filepath.Join(tmpDir, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755))

	freshTime := time.Now().UTC().Format(time.RFC3339)
	marker := repoMarkerWithURLs{
		RepoID:       "repo_normalized",
		Endpoint:     "https://sageox.ai",
		LedgerURL:    "https://git.example.com/normalized.git",
		URLsCachedAt: freshTime,
	}
	data, _ := json.Marshal(marker)
	require.NoError(t, os.WriteFile(filepath.Join(sageoxDir, ".repo_normalized"), data, 0644))

	// search with prefixed endpoint
	urls, err := ReadMarkerURLs(sageoxDir, "https://www.sageox.ai")
	require.NoError(t, err)
	require.NotNil(t, urls, "should find marker with normalized endpoint via prefixed target")
	assert.Equal(t, "https://git.example.com/normalized.git", urls.LedgerURL)
}

func TestLegacyMarkerWithoutTimestamp(t *testing.T) {
	tmpDir := t.TempDir()
	sageoxDir := filepath.Join(tmpDir, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755))

	currentEndpoint := endpoint.Get()

	// create marker without urls_cached_at (legacy format)
	marker := map[string]interface{}{
		"repo_id":    "repo_legacy",
		"endpoint":   currentEndpoint,
		"ledger_url": "https://git.example.com/legacy.git",
		// no urls_cached_at field
	}
	data, _ := json.Marshal(marker)
	require.NoError(t, os.WriteFile(filepath.Join(sageoxDir, ".repo_legacy"), data, 0644))

	t.Run("ReadMarkerURLs returns URLs for legacy marker", func(t *testing.T) {
		urls, err := ReadMarkerURLs(sageoxDir, currentEndpoint)
		require.NoError(t, err)
		require.NotNil(t, urls)
		assert.Equal(t, "https://git.example.com/legacy.git", urls.LedgerURL)
		assert.True(t, urls.URLsCachedAt.IsZero(), "legacy marker should have zero time")
	})

	t.Run("ReadMarkerURLsWithExpiry returns nil for legacy marker", func(t *testing.T) {
		urls, err := ReadMarkerURLsWithExpiry(sageoxDir, currentEndpoint)
		require.NoError(t, err)
		assert.Nil(t, urls, "legacy marker without timestamp should be treated as expired")
	})

	t.Run("GetCachedLedgerURL returns empty for legacy marker", func(t *testing.T) {
		url := GetCachedLedgerURL(sageoxDir, currentEndpoint)
		assert.Empty(t, url, "legacy marker should be treated as expired")
	})
}
