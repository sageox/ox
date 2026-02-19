package daemon

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/sageox/ox/internal/uxfriction"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCatalogCache_NewCatalogCache(t *testing.T) {
	t.Parallel()

	cache := NewCatalogCache()
	require.NotNil(t, cache)
	assert.NotEmpty(t, cache.FilePath())
}

func TestCatalogCache_LoadNonExistent(t *testing.T) {
	t.Parallel()

	// create cache with temp dir
	tmpDir := t.TempDir()
	cache := &CatalogCache{
		filePath: filepath.Join(tmpDir, "nonexistent.json"),
	}

	// load should succeed with empty catalog
	err := cache.Load()
	require.NoError(t, err)
	assert.Empty(t, cache.Version())
	assert.Nil(t, cache.Data())
}

func TestCatalogCache_SaveAndLoad(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	cache := &CatalogCache{
		filePath: filepath.Join(tmpDir, "catalog.json"),
	}

	catalog := &uxfriction.CatalogData{
		Version: "v2026-01-17-001",
		Commands: []uxfriction.CommandMapping{
			{
				Pattern:    "daemons list --every",
				Target:     "daemons show --all",
				Count:      127,
				Confidence: 0.95,
			},
		},
		Tokens: []uxfriction.TokenMapping{
			{
				Pattern:    "prine",
				Target:     "prime",
				Kind:       "unknown-command",
				Count:      89,
				Confidence: 0.92,
			},
		},
	}

	// save catalog
	err := cache.Save(catalog)
	require.NoError(t, err)

	// verify file exists
	_, err = os.Stat(cache.FilePath())
	require.NoError(t, err)

	// create new cache and load
	cache2 := &CatalogCache{
		filePath: cache.FilePath(),
	}
	err = cache2.Load()
	require.NoError(t, err)

	// verify loaded data
	assert.Equal(t, "v2026-01-17-001", cache2.Version())

	data := cache2.Data()
	require.NotNil(t, data)
	assert.Len(t, data.Commands, 1)
	assert.Len(t, data.Tokens, 1)
	assert.Equal(t, "prine", data.Tokens[0].Pattern)
	assert.Equal(t, "prime", data.Tokens[0].Target)
}

func TestCatalogCache_SaveNil(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	cache := &CatalogCache{
		filePath: filepath.Join(tmpDir, "catalog.json"),
	}

	// save nil should be no-op
	err := cache.Save(nil)
	require.NoError(t, err)

	// file should not exist
	_, err = os.Stat(cache.FilePath())
	assert.True(t, os.IsNotExist(err))
}

func TestCatalogCache_Update(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	cache := &CatalogCache{
		filePath: filepath.Join(tmpDir, "catalog.json"),
	}

	catalog1 := &uxfriction.CatalogData{
		Version: "v1",
		Tokens: []uxfriction.TokenMapping{
			{Pattern: "typo1", Target: "correct1"},
		},
	}

	// first update should succeed
	updated, err := cache.Update(catalog1)
	require.NoError(t, err)
	assert.True(t, updated)
	assert.Equal(t, "v1", cache.Version())

	// same version should not update
	updated, err = cache.Update(catalog1)
	require.NoError(t, err)
	assert.False(t, updated)

	// new version should update
	catalog2 := &uxfriction.CatalogData{
		Version: "v2",
		Tokens: []uxfriction.TokenMapping{
			{Pattern: "typo2", Target: "correct2"},
		},
	}
	updated, err = cache.Update(catalog2)
	require.NoError(t, err)
	assert.True(t, updated)
	assert.Equal(t, "v2", cache.Version())

	// verify new data
	data := cache.Data()
	require.NotNil(t, data)
	assert.Equal(t, "typo2", data.Tokens[0].Pattern)
}

func TestCatalogCache_UpdateNil(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	cache := &CatalogCache{
		filePath: filepath.Join(tmpDir, "catalog.json"),
	}

	// update with nil should be no-op
	updated, err := cache.Update(nil)
	require.NoError(t, err)
	assert.False(t, updated)
}

func TestCatalogCache_Clear(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	cache := &CatalogCache{
		filePath: filepath.Join(tmpDir, "catalog.json"),
	}

	// save first
	catalog := &uxfriction.CatalogData{Version: "v1"}
	err := cache.Save(catalog)
	require.NoError(t, err)

	// verify file exists
	_, err = os.Stat(cache.FilePath())
	require.NoError(t, err)

	// clear
	err = cache.Clear()
	require.NoError(t, err)

	// verify file deleted
	_, err = os.Stat(cache.FilePath())
	assert.True(t, os.IsNotExist(err))

	// verify memory cleared
	assert.Empty(t, cache.Version())
	assert.Nil(t, cache.Data())
}

func TestCatalogCache_ClearNonExistent(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	cache := &CatalogCache{
		filePath: filepath.Join(tmpDir, "nonexistent.json"),
	}

	// clear should not error for non-existent file
	err := cache.Clear()
	require.NoError(t, err)
}

func TestCatalogCache_LoadCorruptedFile(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "catalog.json")

	// write corrupted JSON
	err := os.WriteFile(filePath, []byte("{invalid json"), 0600)
	require.NoError(t, err)

	cache := &CatalogCache{filePath: filePath}

	// load should succeed with empty catalog (corrupted is reset)
	err = cache.Load()
	require.NoError(t, err)
	assert.Empty(t, cache.Version())
	assert.Nil(t, cache.Data())
}

func TestCatalogCache_LoadEmptyFile(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "catalog.json")

	// write empty file
	err := os.WriteFile(filePath, []byte{}, 0600)
	require.NoError(t, err)

	cache := &CatalogCache{filePath: filePath}

	// load should succeed with empty catalog
	err = cache.Load()
	require.NoError(t, err)
	assert.Empty(t, cache.Version())
	assert.Nil(t, cache.Data())
}

func TestCatalogCache_DataReturnsCopy(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	cache := &CatalogCache{
		filePath: filepath.Join(tmpDir, "catalog.json"),
	}

	catalog := &uxfriction.CatalogData{
		Version: "v1",
		Tokens: []uxfriction.TokenMapping{
			{Pattern: "original", Target: "correct"},
		},
	}
	err := cache.Save(catalog)
	require.NoError(t, err)

	// get data and modify it
	data := cache.Data()
	require.NotNil(t, data)
	data.Tokens[0].Pattern = "modified"

	// original should be unchanged
	data2 := cache.Data()
	assert.Equal(t, "original", data2.Tokens[0].Pattern)
}

func TestCatalogCache_ConcurrentAccess(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	cache := &CatalogCache{
		filePath: filepath.Join(tmpDir, "catalog.json"),
	}

	// concurrent reads and writes
	var wg sync.WaitGroup
	for i := range 10 {
		wg.Add(1)
		go func(version int) {
			defer wg.Done()
			catalog := &uxfriction.CatalogData{
				Version: "v" + string(rune('0'+version)),
			}
			_, _ = cache.Update(catalog)
			_ = cache.Version()
			_ = cache.Data()
		}(i)
	}

	// wait for all goroutines to complete
	wg.Wait()

	// should not panic or deadlock
	_ = cache.Version()
}

func TestCatalogCache_SaveCreatesDirectory(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	nestedPath := filepath.Join(tmpDir, "nested", "deep", "catalog.json")
	cache := &CatalogCache{filePath: nestedPath}

	catalog := &uxfriction.CatalogData{Version: "v1"}
	err := cache.Save(catalog)
	require.NoError(t, err)

	// verify directory was created
	_, err = os.Stat(filepath.Dir(nestedPath))
	require.NoError(t, err)

	// verify file exists
	_, err = os.Stat(nestedPath)
	require.NoError(t, err)
}

func TestCatalogCache_AtomicWrite(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	cache := &CatalogCache{
		filePath: filepath.Join(tmpDir, "catalog.json"),
	}

	// save first version
	catalog1 := &uxfriction.CatalogData{
		Version: "v1",
		Tokens:  []uxfriction.TokenMapping{{Pattern: "a", Target: "b"}},
	}
	err := cache.Save(catalog1)
	require.NoError(t, err)

	// save second version
	catalog2 := &uxfriction.CatalogData{
		Version: "v2",
		Tokens:  []uxfriction.TokenMapping{{Pattern: "c", Target: "d"}},
	}
	err = cache.Save(catalog2)
	require.NoError(t, err)

	// no temp file should remain
	tmpFile := cache.filePath + ".tmp"
	_, err = os.Stat(tmpFile)
	assert.True(t, os.IsNotExist(err))

	// file should have v2 content
	cache2 := &CatalogCache{filePath: cache.filePath}
	err = cache2.Load()
	require.NoError(t, err)
	assert.Equal(t, "v2", cache2.Version())
}
