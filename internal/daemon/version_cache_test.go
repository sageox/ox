package daemon

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testVersionCache(t *testing.T) *VersionCache {
	t.Helper()
	vc := NewVersionCache(slog.Default())
	vc.filePath = filepath.Join(t.TempDir(), "version-check.json")
	return vc
}

// testVersionCacheWithServer creates a VersionCache wired to a test HTTP server.
func testVersionCacheWithServer(t *testing.T, handler http.HandlerFunc) *VersionCache {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	vc := testVersionCache(t)
	vc.httpClient = srv.Client()
	vc.apiURL = srv.URL
	return vc
}

func TestVersionCache_SaveAndLoadRoundTrip(t *testing.T) {
	t.Parallel()
	vc := testVersionCache(t)

	now := time.Now().Truncate(time.Second)
	err := vc.Save(&VersionCacheData{
		LatestVersion: "v0.9.0",
		CheckedAt:     now,
		ETag:          `"abc123"`,
	})
	require.NoError(t, err)

	// fresh instance reading from same file — proves disk format is correct
	vc2 := &VersionCache{filePath: vc.filePath, logger: slog.Default()}
	require.NoError(t, vc2.Load())

	loaded := vc2.Data()
	require.NotNil(t, loaded)
	assert.Equal(t, "v0.9.0", loaded.LatestVersion)
	assert.Equal(t, `"abc123"`, loaded.ETag)
}

// corrupt cache must not crash — real scenario: partial write, disk full
func TestVersionCache_LoadCorruptFile(t *testing.T) {
	t.Parallel()
	vc := testVersionCache(t)

	require.NoError(t, os.MkdirAll(filepath.Dir(vc.filePath), 0700))
	require.NoError(t, os.WriteFile(vc.filePath, []byte("{not valid json!!!"), 0600))

	err := vc.Load()
	require.NoError(t, err, "corrupt cache should not return error")
	assert.Nil(t, vc.Data(), "corrupt cache should reset to nil")
}

// Data() must return a copy, not a pointer to internal state.
// Without this, callers can silently corrupt the cache.
func TestVersionCache_DataReturnsCopy(t *testing.T) {
	t.Parallel()
	vc := testVersionCache(t)

	require.NoError(t, vc.Save(&VersionCacheData{
		LatestVersion: "v0.9.0",
		ETag:          `"original"`,
	}))

	copy1 := vc.Data()
	copy1.LatestVersion = "v999.0.0"
	copy1.ETag = "tampered"

	copy2 := vc.Data()
	assert.Equal(t, "v0.9.0", copy2.LatestVersion)
	assert.Equal(t, `"original"`, copy2.ETag)
}

// concurrent Save/Load/Data must not panic or deadlock under -race
func TestVersionCache_ConcurrentAccess(t *testing.T) {
	t.Parallel()
	vc := testVersionCache(t)

	var wg sync.WaitGroup
	for i := range 10 {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			_ = vc.Save(&VersionCacheData{LatestVersion: "v0." + string(rune('0'+n)) + ".0"})
			_ = vc.Load()
			_ = vc.Data()
		}(i)
	}
	wg.Wait()

	require.NoError(t, vc.Load())
}

// 200 OK: should parse tag_name, store ETag, and persist to disk
func TestCheckAndUpdate_200_StoresVersionAndETag(t *testing.T) {
	t.Parallel()

	vc := testVersionCacheWithServer(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "application/vnd.github.v3+json", r.Header.Get("Accept"))
		assert.Empty(t, r.Header.Get("If-None-Match"), "first request should have no ETag")

		w.Header().Set("ETag", `"etag-v1"`)
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"tag_name": "v0.10.0"})
	})

	err := vc.CheckAndUpdate(context.Background())
	require.NoError(t, err)

	data := vc.Data()
	require.NotNil(t, data)
	assert.Equal(t, "v0.10.0", data.LatestVersion)
	assert.Equal(t, `"etag-v1"`, data.ETag)
	assert.WithinDuration(t, time.Now(), data.CheckedAt, 5*time.Second)

	// verify it actually hit disk (not just in-memory)
	vc2 := &VersionCache{filePath: vc.filePath, logger: slog.Default()}
	require.NoError(t, vc2.Load())
	assert.Equal(t, "v0.10.0", vc2.Data().LatestVersion)
}

// 304 Not Modified: ETag should be sent, cached version preserved, timestamp updated
func TestCheckAndUpdate_304_SendsETagPreservesVersion(t *testing.T) {
	t.Parallel()

	var receivedETag string
	vc := testVersionCacheWithServer(t, func(w http.ResponseWriter, r *http.Request) {
		receivedETag = r.Header.Get("If-None-Match")
		w.WriteHeader(http.StatusNotModified)
	})

	// seed cache with existing data
	oldTime := time.Now().Add(-1 * time.Hour)
	vc.data = &VersionCacheData{
		LatestVersion: "v0.8.0",
		CheckedAt:     oldTime,
		ETag:          `"cached-etag"`,
	}

	err := vc.CheckAndUpdate(context.Background())
	require.NoError(t, err)

	assert.Equal(t, `"cached-etag"`, receivedETag, "should send cached ETag in If-None-Match")

	data := vc.Data()
	require.NotNil(t, data)
	assert.Equal(t, "v0.8.0", data.LatestVersion, "version should be unchanged on 304")
	assert.True(t, data.CheckedAt.After(oldTime), "timestamp should be updated on 304")
}

// server error: should return error, not corrupt the cache
func TestCheckAndUpdate_ServerError_PreservesCache(t *testing.T) {
	t.Parallel()

	vc := testVersionCacheWithServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	// seed cache
	vc.data = &VersionCacheData{
		LatestVersion: "v0.7.0",
		ETag:          `"existing"`,
	}

	err := vc.CheckAndUpdate(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "500")

	// cache should be untouched
	data := vc.Data()
	require.NotNil(t, data)
	assert.Equal(t, "v0.7.0", data.LatestVersion)
}

// malformed JSON body on 200: should return error, not panic
func TestCheckAndUpdate_MalformedBody(t *testing.T) {
	t.Parallel()

	vc := testVersionCacheWithServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("not json"))
	})

	err := vc.CheckAndUpdate(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode")
}
