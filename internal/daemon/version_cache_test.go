package daemon

import (
	"encoding/json"
	"log/slog"
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

func TestVersionCache_LoadMissingFile(t *testing.T) {
	t.Parallel()

	vc := testVersionCache(t)

	err := vc.Load()
	require.NoError(t, err)
	assert.Nil(t, vc.Data())
}

func TestVersionCache_SaveAndLoad(t *testing.T) {
	t.Parallel()

	vc := testVersionCache(t)

	now := time.Now().Truncate(time.Second)
	data := &VersionCacheData{
		LatestVersion: "v0.9.0",
		CheckedAt:     now,
		ETag:          `"abc123"`,
	}

	err := vc.Save(data)
	require.NoError(t, err)

	// create a fresh cache pointing at the same file to verify disk round-trip
	vc2 := &VersionCache{
		filePath: vc.filePath,
		logger:   slog.Default(),
	}
	err = vc2.Load()
	require.NoError(t, err)

	loaded := vc2.Data()
	require.NotNil(t, loaded)
	assert.Equal(t, "v0.9.0", loaded.LatestVersion)
	assert.Equal(t, `"abc123"`, loaded.ETag)
	// JSON time marshaling loses sub-second precision; compare truncated
	assert.True(t, loaded.CheckedAt.Equal(now) || loaded.CheckedAt.Sub(now).Abs() < time.Second,
		"CheckedAt mismatch: got %v, want %v", loaded.CheckedAt, now)
}

func TestVersionCache_SaveNil(t *testing.T) {
	t.Parallel()

	vc := testVersionCache(t)

	// saving nil is a no-op
	err := vc.Save(nil)
	require.NoError(t, err)

	// file should not exist
	_, err = os.Stat(vc.filePath)
	assert.True(t, os.IsNotExist(err))
}

func TestVersionCache_SaveAtomicWrite(t *testing.T) {
	t.Parallel()

	vc := testVersionCache(t)

	data := &VersionCacheData{
		LatestVersion: "v0.8.0",
		CheckedAt:     time.Now(),
		ETag:          `"etag1"`,
	}
	err := vc.Save(data)
	require.NoError(t, err)

	// temp file should not linger after successful save
	tmpFile := vc.filePath + ".tmp"
	_, err = os.Stat(tmpFile)
	assert.True(t, os.IsNotExist(err), "temp file should not exist after save")

	// actual file should exist with correct content
	_, err = os.Stat(vc.filePath)
	require.NoError(t, err)
}

func TestVersionCache_SaveCreatesDirectory(t *testing.T) {
	t.Parallel()

	vc := &VersionCache{
		filePath: filepath.Join(t.TempDir(), "nested", "deep", "version-check.json"),
		logger:   slog.Default(),
	}

	data := &VersionCacheData{
		LatestVersion: "v0.7.0",
		CheckedAt:     time.Now(),
	}
	err := vc.Save(data)
	require.NoError(t, err)

	_, err = os.Stat(vc.filePath)
	require.NoError(t, err)
}

func TestVersionCache_LoadCorruptFile(t *testing.T) {
	t.Parallel()

	vc := testVersionCache(t)

	// write corrupt JSON to the cache path
	err := os.MkdirAll(filepath.Dir(vc.filePath), 0700)
	require.NoError(t, err)
	err = os.WriteFile(vc.filePath, []byte("{not valid json!!!"), 0600)
	require.NoError(t, err)

	// load should succeed (corrupt cache is silently reset)
	err = vc.Load()
	require.NoError(t, err)
	assert.Nil(t, vc.Data())
}

func TestVersionCache_LoadEmptyFile(t *testing.T) {
	t.Parallel()

	vc := testVersionCache(t)

	err := os.MkdirAll(filepath.Dir(vc.filePath), 0700)
	require.NoError(t, err)
	err = os.WriteFile(vc.filePath, []byte{}, 0600)
	require.NoError(t, err)

	err = vc.Load()
	require.NoError(t, err)
	assert.Nil(t, vc.Data())
}

func TestVersionCache_DataReturnsCopy(t *testing.T) {
	t.Parallel()

	vc := testVersionCache(t)

	data := &VersionCacheData{
		LatestVersion: "v0.9.0",
		CheckedAt:     time.Now(),
		ETag:          `"original"`,
	}
	err := vc.Save(data)
	require.NoError(t, err)

	// mutating the returned copy should not affect the cache
	copy1 := vc.Data()
	require.NotNil(t, copy1)
	copy1.LatestVersion = "v999.0.0"
	copy1.ETag = "tampered"

	copy2 := vc.Data()
	require.NotNil(t, copy2)
	assert.Equal(t, "v0.9.0", copy2.LatestVersion)
	assert.Equal(t, `"original"`, copy2.ETag)
}

func TestVersionCache_DataNilWhenEmpty(t *testing.T) {
	t.Parallel()

	vc := testVersionCache(t)
	assert.Nil(t, vc.Data(), "Data() should be nil before any load or save")
}

func TestVersionCache_SaveOverwrite(t *testing.T) {
	t.Parallel()

	vc := testVersionCache(t)

	// save first version
	err := vc.Save(&VersionCacheData{
		LatestVersion: "v0.1.0",
		CheckedAt:     time.Now(),
	})
	require.NoError(t, err)

	// overwrite with second version
	err = vc.Save(&VersionCacheData{
		LatestVersion: "v0.2.0",
		CheckedAt:     time.Now(),
		ETag:          `"new"`,
	})
	require.NoError(t, err)

	// reload and verify the latest
	vc2 := &VersionCache{
		filePath: vc.filePath,
		logger:   slog.Default(),
	}
	err = vc2.Load()
	require.NoError(t, err)

	loaded := vc2.Data()
	require.NotNil(t, loaded)
	assert.Equal(t, "v0.2.0", loaded.LatestVersion)
	assert.Equal(t, `"new"`, loaded.ETag)
}

func TestVersionCache_ConcurrentAccess(t *testing.T) {
	t.Parallel()

	vc := testVersionCache(t)

	var wg sync.WaitGroup
	for i := range 10 {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			d := &VersionCacheData{
				LatestVersion: "v0." + string(rune('0'+n)) + ".0",
				CheckedAt:     time.Now(),
			}
			_ = vc.Save(d)
			_ = vc.Load()
			_ = vc.Data()
		}(i)
	}

	wg.Wait()

	// should not panic or deadlock; data should still be loadable
	err := vc.Load()
	require.NoError(t, err)
}

func TestVersionCache_FilePermissions(t *testing.T) {
	t.Parallel()

	vc := testVersionCache(t)

	err := vc.Save(&VersionCacheData{
		LatestVersion: "v0.5.0",
		CheckedAt:     time.Now(),
	})
	require.NoError(t, err)

	info, err := os.Stat(vc.filePath)
	require.NoError(t, err)
	// file should be owner-readable/writable only (0600)
	assert.Equal(t, os.FileMode(0600), info.Mode().Perm())
}

func TestVersionCache_JSONFormat(t *testing.T) {
	t.Parallel()

	vc := testVersionCache(t)

	now := time.Now().Truncate(time.Second)
	err := vc.Save(&VersionCacheData{
		LatestVersion: "v0.10.0",
		CheckedAt:     now,
		ETag:          `W/"abc"`,
	})
	require.NoError(t, err)

	// verify the on-disk format is valid JSON with expected keys
	raw, err := os.ReadFile(vc.filePath)
	require.NoError(t, err)

	var parsed map[string]interface{}
	err = json.Unmarshal(raw, &parsed)
	require.NoError(t, err)

	assert.Equal(t, "v0.10.0", parsed["latest_version"])
	assert.Equal(t, `W/"abc"`, parsed["etag"])
	assert.Contains(t, parsed, "checked_at")
}
