package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// --- GetTeamURLWithFallback ---

func TestGetTeamURLWithFallback_EmptyTeamID(t *testing.T) {
	t.Parallel()
	// empty team ID should return empty immediately
	got := GetTeamURLWithFallback("/some/dir", "", "https://sageox.ai")
	assert.Equal(t, "", got)
}

func TestGetTeamURLWithFallback_EmptySageoxDir(t *testing.T) {
	t.Parallel()
	// empty sageox dir means no fallback; with no API access, returns empty
	got := GetTeamURLWithFallback("", "team-123", "https://nonexistent.example.com")
	assert.Equal(t, "", got)
}

// --- GetLedgerURLWithFallback ---

func TestGetLedgerURLWithFallback_EmptySageoxDir(t *testing.T) {
	t.Parallel()
	// no sageox dir means no fallback possible
	got := GetLedgerURLWithFallback("", "https://nonexistent.example.com")
	assert.Equal(t, "", got)
}

func TestGetLedgerURLWithFallback_NonexistentDir(t *testing.T) {
	t.Parallel()
	// dir doesn't exist, API won't work either
	got := GetLedgerURLWithFallback("/nonexistent/dir/.sageox", "https://nonexistent.example.com")
	assert.Equal(t, "", got)
}

// --- ShouldRefreshCachedURLs ---

func TestShouldRefreshCachedURLs_EmptySageoxDir(t *testing.T) {
	t.Parallel()
	// empty sageox dir returns false (can't refresh what doesn't exist)
	got := ShouldRefreshCachedURLs("", "https://sageox.ai")
	assert.False(t, got)
}

func TestShouldRefreshCachedURLs_NonexistentDir(t *testing.T) {
	t.Parallel()
	// dir doesn't exist => no cache => should refresh
	got := ShouldRefreshCachedURLs("/nonexistent/.sageox", "https://sageox.ai")
	assert.True(t, got)
}

func TestShouldRefreshCachedURLs_EmptyDir(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// empty dir has no marker files => should refresh
	got := ShouldRefreshCachedURLs(dir, "https://sageox.ai")
	assert.True(t, got)
}

// --- FetchAndCacheURLs ---

func TestFetchAndCacheURLs_EmptyInputs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		sageox   string
		repoID   string
		want     bool
	}{
		{"both empty", "", "", false},
		{"sageox empty", "", "repo-1", false},
		{"repoID empty", "/tmp/sageox", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := FetchAndCacheURLs(tt.sageox, tt.repoID, "https://nonexistent.example.com")
			assert.Equal(t, tt.want, got)
		})
	}
}

// --- FetchLedgerURLWithFallback ---

func TestFetchLedgerURLWithFallback_NoAuthNoCache(t *testing.T) {
	t.Parallel()
	// no auth token, no cache dir
	url, fromCache, err := FetchLedgerURLWithFallback("", "https://nonexistent.example.com")
	assert.Equal(t, "", url)
	assert.False(t, fromCache)
	assert.Error(t, err)
}

// --- FetchTeamURLWithFallback ---

func TestFetchTeamURLWithFallback_EmptyTeamID(t *testing.T) {
	t.Parallel()
	url, fromCache, err := FetchTeamURLWithFallback("/tmp/sageox", "", "https://sageox.ai")
	assert.Equal(t, "", url)
	assert.False(t, fromCache)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "team ID is empty")
}

func TestFetchTeamURLWithFallback_NoAuthNoCache(t *testing.T) {
	t.Parallel()
	url, fromCache, err := FetchTeamURLWithFallback("", "team-1", "https://nonexistent.example.com")
	assert.Equal(t, "", url)
	assert.False(t, fromCache)
	assert.Error(t, err)
}

// --- GetLedgerURLWithFallbackFromRepoRoot ---

func TestGetLedgerURLWithFallbackFromRepoRoot_EmptyRoot(t *testing.T) {
	t.Parallel()
	// empty root means no fallback; just API attempt which will fail
	got := GetLedgerURLWithFallbackFromRepoRoot("")
	assert.Equal(t, "", got)
}

// --- GetTeamURLWithFallbackFromRepoRoot ---

func TestGetTeamURLWithFallbackFromRepoRoot_EmptyInputs(t *testing.T) {
	t.Parallel()

	// empty root or team ID
	got := GetTeamURLWithFallbackFromRepoRoot("", "team-1")
	assert.Equal(t, "", got)

	got = GetTeamURLWithFallbackFromRepoRoot("/tmp/repo", "")
	assert.Equal(t, "", got)

	got = GetTeamURLWithFallbackFromRepoRoot("", "")
	assert.Equal(t, "", got)
}
