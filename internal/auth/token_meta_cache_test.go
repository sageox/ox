package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// withTempCacheDir redirects paths.CacheDir() to a t.TempDir by setting
// the relevant XDG env so isolation is hermetic between tests.
func withTempCacheDir(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", tmp)
	// Defensively unset the legacy disable flag — both paths must point at tmp.
	t.Setenv("OX_XDG_DISABLE", "")
	t.Setenv("HOME", tmp) // legacy fallback respects HOME
	return tmp
}

// TestSaveAndLoadTokenMetaCache_RoundTrip — Failure prevented: cache
// serialization or path resolution drifts and we silently re-fetch on
// every CLI invocation.
func TestSaveAndLoadTokenMetaCache_RoundTrip(t *testing.T) {
	withTempCacheDir(t)

	expires := time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC)
	in := &tokenMetaCacheFileFormat{Entries: map[string]TokenMeta{
		"key1": {
			ExpiresAt:   &expires,
			TokenPrefix: "oxp_AAAA",
			Name:        "test",
			FetchedAt:   time.Now().UTC(),
		},
	}}
	require.NoError(t, saveTokenMetaCache(in))

	out, err := loadTokenMetaCache()
	require.NoError(t, err)
	require.Contains(t, out.Entries, "key1")
	assert.Equal(t, expires, *out.Entries["key1"].ExpiresAt)
	assert.Equal(t, "oxp_AAAA", out.Entries["key1"].TokenPrefix)
}

// TestSaveTokenMetaCache_FilePerms — Failure prevented: cache file is
// world-readable and accidentally leaks metadata on shared hosts. The
// rename + chmod path must produce 0600.
func TestSaveTokenMetaCache_FilePerms(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix perm bits not meaningful on windows")
	}
	withTempCacheDir(t)

	require.NoError(t, saveTokenMetaCache(&tokenMetaCacheFileFormat{
		Entries: map[string]TokenMeta{},
	}))

	info, err := os.Stat(tokenMetaCachePath())
	require.NoError(t, err)
	perm := info.Mode().Perm()
	assert.Equal(t, os.FileMode(0o600), perm, "cache file must be 0600")
}

// TestFetchTokenMetaCached_HitsCacheWhenFresh — Failure prevented: every
// CLI command hits the network for /auth/me, blowing the latency budget
// and rate-limiting the user.
func TestFetchTokenMetaCached_HitsCacheWhenFresh(t *testing.T) {
	withTempCacheDir(t)

	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		expires := time.Now().Add(48 * time.Hour)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"expires_at":   expires.Format(time.RFC3339),
			"created_at":   time.Now().Add(-1 * time.Hour).Format(time.RFC3339),
			"token_prefix": "oxp_AAAA",
			"name":         "test",
		})
	}))
	defer srv.Close()

	ctx := context.Background()
	tok := "oxp_test_token"

	// First call: cache miss → fetch.
	meta1, err := FetchTokenMetaCached(ctx, srv.URL, tok)
	require.NoError(t, err)
	require.NotNil(t, meta1)
	assert.Equal(t, int32(1), atomic.LoadInt32(&calls))

	// Second call within TTL: cache hit, no network.
	meta2, err := FetchTokenMetaCached(ctx, srv.URL, tok)
	require.NoError(t, err)
	require.NotNil(t, meta2)
	assert.Equal(t, int32(1), atomic.LoadInt32(&calls), "second call must hit cache, not network")
}

// TestFetchTokenMetaCached_StaleEntryRefetches — Failure prevented: stale
// cache entries persist forever and the user never sees an updated
// expiry after rotating their token.
func TestFetchTokenMetaCached_StaleEntryRefetches(t *testing.T) {
	withTempCacheDir(t)

	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		expires := time.Now().Add(48 * time.Hour)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"expires_at":   expires.Format(time.RFC3339),
			"token_prefix": "oxp_AAAA",
			"name":         "test",
		})
	}))
	defer srv.Close()

	tok := "oxp_test_token"
	key := tokenHashKey(tok)

	// Plant a stale entry directly so we don't have to wait an hour.
	staleExpires := time.Now().Add(1 * time.Hour)
	stale := &tokenMetaCacheFileFormat{Entries: map[string]TokenMeta{
		key: {
			ExpiresAt:   &staleExpires,
			TokenPrefix: "oxp_OLD",
			FetchedAt:   time.Now().Add(-2 * time.Hour),
		},
	}}
	require.NoError(t, saveTokenMetaCache(stale))

	meta, err := FetchTokenMetaCached(context.Background(), srv.URL, tok)
	require.NoError(t, err)
	require.NotNil(t, meta)
	assert.Equal(t, int32(1), atomic.LoadInt32(&calls), "stale entry must trigger refetch")
	assert.Equal(t, "oxp_AAAA", meta.TokenPrefix, "must return the fresh value, not the stale prefix")
}

// TestFetchTokenMetaCached_EmptyToken — Failure prevented: an empty token
// reaches the server and produces a spurious 401 we silently absorb.
func TestFetchTokenMetaCached_EmptyToken(t *testing.T) {
	withTempCacheDir(t)
	_, err := FetchTokenMetaCached(context.Background(), "https://sageox.ai", "")
	assert.Error(t, err)
}

// TestTokenHashKey_StableAndDistinct — Failure prevented: hash function
// changes break every existing cache file silently.
func TestTokenHashKey_StableAndDistinct(t *testing.T) {
	a := tokenHashKey("oxp_one")
	b := tokenHashKey("oxp_one")
	c := tokenHashKey("oxp_two")
	assert.Equal(t, a, b, "same input must produce same hash")
	assert.NotEqual(t, a, c, "different inputs must produce different hashes")
	assert.Len(t, a, 64, "sha256 hex is 64 chars")
}

// TestTokenMetaCachePath_InsideCacheDir — Failure prevented: cache file
// escapes to an unexpected location and bypasses XDG isolation.
func TestTokenMetaCachePath_InsideCacheDir(t *testing.T) {
	tmp := withTempCacheDir(t)
	path := tokenMetaCachePath()
	// path should sit somewhere under tmp.
	rel, err := filepath.Rel(tmp, path)
	require.NoError(t, err)
	assert.NotContains(t, rel, "..", "path must be inside the configured XDG cache dir")
}

// --- Endpoint scoping (PR #626 follow-up) ---

// TestFetchTokenMetaCached_EndpointIsolation — Failure prevented: the cache
// key was originally tokenHashKey(token) only, so the same token value used
// against two different endpoints (e.g. prod + staging during migration)
// returned the first endpoint's metadata for both. The user would see a
// staging name/expiry surfaced for a prod token (or vice versa).
func TestFetchTokenMetaCached_EndpointIsolation(t *testing.T) {
	withTempCacheDir(t)

	type epResp struct {
		prefix string
		name   string
	}
	serve := func(r epResp) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			expires := time.Now().Add(72 * time.Hour)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"expires_at":   expires.Format(time.RFC3339),
				"token_prefix": r.prefix,
				"name":         r.name,
			})
		}))
	}

	srvProd := serve(epResp{prefix: "oxp_PROD", name: "prod"})
	defer srvProd.Close()
	srvStage := serve(epResp{prefix: "oxp_STAGE", name: "stage"})
	defer srvStage.Close()

	// Same token value, two endpoints — must NOT collide in the cache.
	const tok = "oxp_shared_value"

	prodMeta, err := FetchTokenMetaCached(context.Background(), srvProd.URL, tok)
	require.NoError(t, err)
	require.NotNil(t, prodMeta)
	assert.Equal(t, "oxp_PROD", prodMeta.TokenPrefix)
	assert.Equal(t, "prod", prodMeta.Name)

	stageMeta, err := FetchTokenMetaCached(context.Background(), srvStage.URL, tok)
	require.NoError(t, err)
	require.NotNil(t, stageMeta)
	assert.Equal(t, "oxp_STAGE", stageMeta.TokenPrefix, "second endpoint must NOT return cached prod metadata")
	assert.Equal(t, "stage", stageMeta.Name)

	// Re-fetch prod — should still be the prod entry, untouched by stage.
	prodAgain, err := FetchTokenMetaCached(context.Background(), srvProd.URL, tok)
	require.NoError(t, err)
	require.NotNil(t, prodAgain)
	assert.Equal(t, "oxp_PROD", prodAgain.TokenPrefix)
}

// TestTokenMetaCacheKey_DistinctByEndpoint — Failure prevented: cache-key
// helper conflates (ep, token) and the on-disk file ends up with one entry
// per token instead of one per (endpoint, token) pair.
func TestTokenMetaCacheKey_DistinctByEndpoint(t *testing.T) {
	a := tokenMetaCacheKey("https://api.sageox.ai", "oxp_x")
	b := tokenMetaCacheKey("https://staging.sageox.ai", "oxp_x")
	c := tokenMetaCacheKey("https://api.sageox.ai", "oxp_x")
	assert.NotEqual(t, a, b, "different endpoints must produce different keys for the same token")
	assert.Equal(t, a, c, "same (endpoint, token) must produce the same key")
	// Defend against trivial concatenation collisions:
	// (ep="ab", token="") vs (ep="a", token="b") must NOT collide.
	x := tokenMetaCacheKey("ab", "")
	y := tokenMetaCacheKey("a", "b")
	assert.NotEqual(t, x, y, "separator must prevent prefix-collision")
}
