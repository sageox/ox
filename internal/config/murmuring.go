package config

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Murmuring mode constants.
const (
	MurmuringManual = "manual"
	MurmuringAuto   = "auto"
)

// ValidMurmuringModes lists all valid values for the murmuring config field.
var ValidMurmuringModes = []string{MurmuringManual, MurmuringAuto}

// IsValidMurmuringMode returns true if the mode is a recognized murmuring value.
func IsValidMurmuringMode(mode string) bool {
	switch mode {
	case MurmuringManual, MurmuringAuto, "":
		return true
	}
	return false
}

// NormalizeMurmuring returns the canonical murmuring mode.
// Empty string and unrecognized values default to "auto".
func NormalizeMurmuring(mode string) string {
	switch mode {
	case MurmuringManual:
		return MurmuringManual
	default:
		return MurmuringAuto
	}
}

// murmuringCacheEntry holds the mtime-based cache for ResolveMurmuring.
var murmuringCacheEntry struct {
	mu          sync.Mutex
	projectRoot string
	mode        string
	mtime       time.Time
	checkedAt   time.Time
}

// murmuringCacheMaxAge is how often we re-stat the config file.
// Covers relay + nudge source calling within the same second.
const murmuringCacheMaxAge = 5 * time.Second

// ResolveMurmuring determines the effective murmuring mode for a project.
// Reads from project config; defaults to "auto".
// Uses mtime-based caching to avoid repeated os.Stat + ReadFile + json.Unmarshal.
func ResolveMurmuring(projectRoot string) string {
	if projectRoot == "" {
		return MurmuringAuto
	}

	murmuringCacheEntry.mu.Lock()
	defer murmuringCacheEntry.mu.Unlock()

	now := time.Now()

	// fast path: same project root and checked recently — skip even os.Stat
	if murmuringCacheEntry.projectRoot == projectRoot &&
		now.Sub(murmuringCacheEntry.checkedAt) < murmuringCacheMaxAge {
		return murmuringCacheEntry.mode
	}

	configPath := filepath.Join(projectRoot, sageoxDir, projectConfigFilename)
	info, err := os.Stat(configPath)
	if err != nil {
		// file doesn't exist or is inaccessible — cache "auto" with zero mtime
		if !errors.Is(err, os.ErrNotExist) {
			slog.Debug("failed to stat project config for murmuring", "error", err)
		}
		murmuringCacheEntry.projectRoot = projectRoot
		murmuringCacheEntry.mode = MurmuringAuto
		murmuringCacheEntry.mtime = time.Time{}
		murmuringCacheEntry.checkedAt = now
		return MurmuringAuto
	}

	// mtime unchanged — return cached mode
	if murmuringCacheEntry.projectRoot == projectRoot &&
		info.ModTime().Equal(murmuringCacheEntry.mtime) {
		murmuringCacheEntry.checkedAt = now
		return murmuringCacheEntry.mode
	}

	// mtime changed or different project — re-read config
	cfg, err := LoadProjectConfig(projectRoot)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			slog.Debug("failed to load project config for murmuring", "error", err)
		}
		murmuringCacheEntry.projectRoot = projectRoot
		murmuringCacheEntry.mode = MurmuringAuto
		murmuringCacheEntry.mtime = info.ModTime()
		murmuringCacheEntry.checkedAt = now
		return MurmuringAuto
	}

	mode := MurmuringAuto
	if cfg != nil {
		mode = NormalizeMurmuring(cfg.Murmuring)
	}

	murmuringCacheEntry.projectRoot = projectRoot
	murmuringCacheEntry.mode = mode
	murmuringCacheEntry.mtime = info.ModTime()
	murmuringCacheEntry.checkedAt = now
	return mode
}

// MurmuringEnabled returns true if auto-murmuring is active for the project.
func MurmuringEnabled(projectRoot string) bool {
	return ResolveMurmuring(projectRoot) == MurmuringAuto
}
