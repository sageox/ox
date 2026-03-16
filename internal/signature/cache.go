package signature

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"github.com/sageox/ox/internal/paths"
)

// CacheEntry represents a single cached verification result
type CacheEntry struct {
	FilePath    string    `json:"file_path"`
	ContentHash string    `json:"content_hash"`
	Verified    bool      `json:"verified"`
	Timestamp   time.Time `json:"timestamp"`
}

// VerificationCache stores cached verification results with tamper protection
type VerificationCache struct {
	Entries []CacheEntry `json:"entries"`
	HMAC    string       `json:"hmac"`
}

// GetCachePath returns the path to the verification cache file.
// Uses centralized paths package for consistent path resolution.
func GetCachePath() (string, error) {
	path := paths.VerificationCacheFile()
	if path == "" {
		return "", fmt.Errorf("failed to get verification cache path")
	}
	return path, nil
}

// getMachineIDPath returns the path to the machine ID file.
// Uses centralized paths package for consistent path resolution.
func getMachineIDPath() (string, error) {
	path := paths.MachineIDFile()
	if path == "" {
		return "", fmt.Errorf("failed to get machine ID path")
	}
	return path, nil
}

// GetMachineID returns the machine ID, creating it if it doesn't exist
func GetMachineID() (string, error) {
	idPath, err := getMachineIDPath()
	if err != nil {
		return "", err
	}

	// try to read existing machine ID
	data, err := os.ReadFile(idPath)
	if err == nil {
		return string(data), nil
	}

	if !os.IsNotExist(err) {
		return "", fmt.Errorf("failed to read machine ID file: %w", err)
	}

	// generate new machine ID
	hostname, err := os.Hostname()
	if err != nil {
		hostname = "unknown-host"
	}

	username := os.Getenv("USER")
	if username == "" {
		username = os.Getenv("USERNAME")
	}
	if username == "" {
		username = "unknown-user"
	}

	// create unique machine ID from hostname + username + random UUID
	randomUUID := uuid.New().String()
	machineID := fmt.Sprintf("%s-%s-%s", hostname, username, randomUUID)

	// ensure config directory exists
	configDir := filepath.Dir(idPath)
	if err := os.MkdirAll(configDir, 0700); err != nil {
		return "", fmt.Errorf("failed to create config directory: %w", err)
	}

	// save machine ID to disk
	if err := os.WriteFile(idPath, []byte(machineID), 0600); err != nil {
		return "", fmt.Errorf("failed to write machine ID file: %w", err)
	}

	return machineID, nil
}

// calculateHMAC computes HMAC-SHA256 of the cache entries
func calculateHMAC(entries []CacheEntry, key string) (string, error) {
	// encode entries as JSON for consistent hashing
	data, err := json.Marshal(entries)
	if err != nil {
		return "", fmt.Errorf("failed to marshal entries: %w", err)
	}

	// compute HMAC-SHA256
	h := hmac.New(sha256.New, []byte(key))
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil)), nil
}

// verifyHMAC checks if the HMAC matches the entries
func verifyHMAC(entries []CacheEntry, expectedHMAC string, key string) bool {
	actualHMAC, err := calculateHMAC(entries, key)
	if err != nil {
		return false
	}
	return hmac.Equal([]byte(actualHMAC), []byte(expectedHMAC))
}

// LoadCache loads and validates the verification cache
// Returns empty cache if file doesn't exist or HMAC verification fails
func LoadCache() (*VerificationCache, error) {
	cachePath, err := GetCachePath()
	if err != nil {
		return nil, err
	}

	// read cache file
	data, err := os.ReadFile(cachePath)
	if err != nil {
		if os.IsNotExist(err) {
			// return empty cache if file doesn't exist
			return &VerificationCache{Entries: []CacheEntry{}}, nil
		}
		return nil, fmt.Errorf("failed to read cache file: %w", err)
	}

	// parse cache
	var cache VerificationCache
	if err := json.Unmarshal(data, &cache); err != nil {
		// invalid JSON, return empty cache
		return &VerificationCache{Entries: []CacheEntry{}}, nil
	}

	// get machine ID for HMAC verification
	machineID, err := GetMachineID()
	if err != nil {
		return nil, err
	}

	// verify HMAC
	if !verifyHMAC(cache.Entries, cache.HMAC, machineID) {
		// HMAC mismatch, discard cache and return empty
		return &VerificationCache{Entries: []CacheEntry{}}, nil
	}

	return &cache, nil
}

// SaveCache saves the verification cache with updated HMAC
func SaveCache(cache *VerificationCache) error {
	cachePath, err := GetCachePath()
	if err != nil {
		return err
	}

	// get machine ID for HMAC
	machineID, err := GetMachineID()
	if err != nil {
		return err
	}

	// calculate HMAC
	hmacValue, err := calculateHMAC(cache.Entries, machineID)
	if err != nil {
		return err
	}
	cache.HMAC = hmacValue

	// ensure config directory exists
	configDir := filepath.Dir(cachePath)
	if err := os.MkdirAll(configDir, 0700); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	// marshal cache to JSON
	data, err := json.MarshalIndent(cache, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal cache: %w", err)
	}

	// atomic write: write to temp file then rename
	tempPath := cachePath + ".tmp"
	if err := os.WriteFile(tempPath, data, 0600); err != nil {
		return fmt.Errorf("failed to write temp cache file: %w", err)
	}

	if err := os.Rename(tempPath, cachePath); err != nil {
		os.Remove(tempPath) // cleanup temp file on error
		return fmt.Errorf("failed to rename temp cache file: %w", err)
	}

	return nil
}

// GetCachedResult looks up a cached verification result by file path and content hash
func GetCachedResult(filePath, contentHash string) (*CacheEntry, error) {
	cache, err := LoadCache()
	if err != nil {
		return nil, err
	}

	for _, entry := range cache.Entries {
		if entry.FilePath == filePath && entry.ContentHash == contentHash {
			return &entry, nil
		}
	}

	return nil, nil // not found
}

// SetCachedResult updates the cache with a verification result
func SetCachedResult(filePath, contentHash string, verified bool) error {
	cache, err := LoadCache()
	if err != nil {
		return err
	}

	for i, entry := range cache.Entries {
		if entry.FilePath == filePath && entry.ContentHash == contentHash {
			cache.Entries[i].Verified = verified
			cache.Entries[i].Timestamp = time.Now()
			return SaveCache(cache)
		}
	}

	// not found — add new entry
	cache.Entries = append(cache.Entries, CacheEntry{
		FilePath:    filePath,
		ContentHash: contentHash,
		Verified:    verified,
		Timestamp:   time.Now(),
	})

	return SaveCache(cache)
}
