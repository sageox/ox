package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"

	"github.com/sageox/ox/internal/uxfriction"
	"github.com/sageox/ox/internal/paths"
)

// CatalogCache manages the friction catalog cache on disk.
// The catalog contains learned corrections for common typos and command mistakes.
// Thread-safe for concurrent access.
type CatalogCache struct {
	mu       sync.RWMutex
	catalog  *uxfriction.CatalogData
	filePath string
}

// NewCatalogCache creates a new catalog cache.
// The cache file is stored at ~/.cache/sageox/friction-catalog.json (or XDG equivalent).
func NewCatalogCache() *CatalogCache {
	return &CatalogCache{
		filePath: catalogCacheFile(),
	}
}

// catalogCacheFile returns the path to the catalog cache file.
func catalogCacheFile() string {
	return paths.CacheDir() + "/friction-catalog.json"
}

// Load reads the catalog from disk if it exists.
// Returns nil error if file doesn't exist (empty catalog is valid).
// Safe to call concurrently.
func (c *CatalogCache) Load() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	data, err := os.ReadFile(c.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			// no cache file is fine, start with empty catalog
			c.catalog = nil
			return nil
		}
		return fmt.Errorf("read catalog cache: %w", err)
	}

	if len(data) == 0 {
		c.catalog = nil
		return nil
	}

	var catalog uxfriction.CatalogData
	if err := json.Unmarshal(data, &catalog); err != nil {
		// corrupted cache, reset it
		c.catalog = nil
		return nil
	}

	c.catalog = &catalog
	return nil
}

// Save writes the catalog to disk.
// Creates the cache directory if it doesn't exist.
// Safe to call concurrently.
func (c *CatalogCache) Save(catalog *uxfriction.CatalogData) error {
	if catalog == nil {
		return nil
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// ensure cache directory exists
	if _, err := paths.EnsureDirForFile(c.filePath); err != nil {
		return fmt.Errorf("create cache directory: %w", err)
	}

	data, err := json.MarshalIndent(catalog, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal catalog: %w", err)
	}

	// write atomically by writing to temp file first
	tmpFile := c.filePath + ".tmp"
	if err := os.WriteFile(tmpFile, data, 0600); err != nil {
		return fmt.Errorf("write temp file: %w", err)
	}

	if err := os.Rename(tmpFile, c.filePath); err != nil {
		os.Remove(tmpFile) // clean up on failure
		return fmt.Errorf("rename temp file: %w", err)
	}

	c.catalog = catalog
	return nil
}

// Version returns the current cached catalog version.
// Returns empty string if no catalog is cached.
// Safe to call concurrently.
func (c *CatalogCache) Version() string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.catalog == nil {
		return ""
	}
	return c.catalog.Version
}

// Data returns the current catalog data.
// Returns nil if no catalog is cached.
// Safe to call concurrently.
func (c *CatalogCache) Data() *uxfriction.CatalogData {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.catalog == nil {
		return nil
	}

	// return a copy to prevent concurrent modification
	catalogCopy := *c.catalog
	if c.catalog.Commands != nil {
		catalogCopy.Commands = make([]uxfriction.CommandMapping, len(c.catalog.Commands))
		copy(catalogCopy.Commands, c.catalog.Commands)
	}
	if c.catalog.Tokens != nil {
		catalogCopy.Tokens = make([]uxfriction.TokenMapping, len(c.catalog.Tokens))
		copy(catalogCopy.Tokens, c.catalog.Tokens)
	}

	return &catalogCopy
}

// Update updates the catalog cache if the new version differs from current.
// Returns true if the catalog was updated.
// Safe to call concurrently.
func (c *CatalogCache) Update(catalog *uxfriction.CatalogData) (bool, error) {
	if catalog == nil {
		return false, nil
	}

	// check if version differs (read lock first for performance)
	c.mu.RLock()
	currentVersion := ""
	if c.catalog != nil {
		currentVersion = c.catalog.Version
	}
	c.mu.RUnlock()

	if currentVersion == catalog.Version {
		return false, nil
	}

	if err := c.Save(catalog); err != nil {
		return false, err
	}

	return true, nil
}

// Clear removes the catalog cache from disk and memory.
// Safe to call concurrently.
func (c *CatalogCache) Clear() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.catalog = nil

	if err := os.Remove(c.filePath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove catalog cache: %w", err)
	}

	return nil
}

// FilePath returns the path to the catalog cache file.
func (c *CatalogCache) FilePath() string {
	return c.filePath
}
