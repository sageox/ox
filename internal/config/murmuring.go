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

// MurmurReceive mode constants.
const (
	MurmurReceiveOff = "off"
	MurmurReceiveOn  = "on"
)

// ValidMurmurReceiveModes lists all valid values for the murmur_receive config field.
var ValidMurmurReceiveModes = []string{MurmurReceiveOff, MurmurReceiveOn}

// IsValidMurmurReceiveMode returns true if the mode is a recognized murmur_receive value.
func IsValidMurmurReceiveMode(mode string) bool {
	switch mode {
	case MurmurReceiveOff, MurmurReceiveOn, "":
		return true
	}
	return false
}

// NormalizeMurmurReceive returns the canonical murmur receive mode.
// Empty string and unrecognized values default to "auto".
func NormalizeMurmurReceive(mode string) string {
	switch mode {
	case MurmurReceiveOff:
		return MurmurReceiveOff
	default:
		return MurmurReceiveOn
	}
}

// murmuringCacheEntry holds the mtime-based cache for ResolveMurmuring.
var murmuringCacheEntry struct {
	mu             sync.Mutex
	projectRoot    string
	mode           string
	mtime          time.Time // project config mtime
	userConfigPath string    // resolved user config file path
	userMtime      time.Time // user config mtime
	checkedAt      time.Time
}

// murmuringCacheMaxAge is how often we re-stat the config file.
// Covers relay + nudge source calling within the same second.
const murmuringCacheMaxAge = 5 * time.Second

// resolveUserConfigPath returns the user config file path, respecting OX_USER_CONFIG.
func resolveUserConfigPath() string {
	if envPath := os.Getenv(EnvUserConfig); envPath != "" {
		return envPath
	}
	configDir := GetUserConfigDir()
	if configDir == "" {
		return ""
	}
	return filepath.Join(configDir, "config.yaml")
}

// ResolveMurmuring determines the effective murmuring mode for a project.
// Priority: User config > Project config > Default ("auto").
// Uses mtime-based caching to avoid repeated os.Stat + ReadFile + json.Unmarshal.
func ResolveMurmuring(projectRoot string) string {
	if projectRoot == "" {
		return resolveUserMurmuring(MurmuringAuto)
	}

	murmuringCacheEntry.mu.Lock()
	defer murmuringCacheEntry.mu.Unlock()

	now := time.Now()
	userPath := resolveUserConfigPath()

	// fast path: same project root and checked recently — skip even os.Stat
	if murmuringCacheEntry.projectRoot == projectRoot &&
		murmuringCacheEntry.userConfigPath == userPath &&
		now.Sub(murmuringCacheEntry.checkedAt) < murmuringCacheMaxAge {
		return murmuringCacheEntry.mode
	}

	// stat user config file
	var userMtime time.Time
	if userPath != "" {
		if uInfo, err := os.Stat(userPath); err == nil {
			userMtime = uInfo.ModTime()
		}
	}

	configPath := filepath.Join(projectRoot, sageoxDir, projectConfigFilename)
	info, err := os.Stat(configPath)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			slog.Debug("failed to stat project config for murmuring", "error", err)
		}
		// no project config — check user config
		mode := resolveUserMurmuring(MurmuringAuto)
		murmuringCacheEntry.projectRoot = projectRoot
		murmuringCacheEntry.mode = mode
		murmuringCacheEntry.mtime = time.Time{}
		murmuringCacheEntry.userConfigPath = userPath
		murmuringCacheEntry.userMtime = userMtime
		murmuringCacheEntry.checkedAt = now
		return mode
	}

	// both mtimes unchanged — return cached mode
	if murmuringCacheEntry.projectRoot == projectRoot &&
		murmuringCacheEntry.userConfigPath == userPath &&
		info.ModTime().Equal(murmuringCacheEntry.mtime) &&
		userMtime.Equal(murmuringCacheEntry.userMtime) {
		murmuringCacheEntry.checkedAt = now
		return murmuringCacheEntry.mode
	}

	// mtime changed or different project — re-read configs
	// resolve: user config > project config > default
	userMode := resolveUserMurmuring("")
	var mode string
	if userMode != "" {
		mode = userMode
	} else {
		mode = MurmuringAuto
		cfg, loadErr := LoadProjectConfig(projectRoot)
		if loadErr != nil {
			if !errors.Is(loadErr, os.ErrNotExist) {
				slog.Debug("failed to load project config for murmuring", "error", loadErr)
			}
		} else if cfg != nil {
			mode = NormalizeMurmuring(cfg.GetMurmuring())
		}
	}

	murmuringCacheEntry.projectRoot = projectRoot
	murmuringCacheEntry.mode = mode
	murmuringCacheEntry.mtime = info.ModTime()
	murmuringCacheEntry.userConfigPath = userPath
	murmuringCacheEntry.userMtime = userMtime
	murmuringCacheEntry.checkedAt = now
	return mode
}

// resolveUserMurmuring checks user config for murmuring override.
// Returns the normalized mode if set, or fallback otherwise.
func resolveUserMurmuring(fallback string) string {
	userCfg, _ := LoadUserConfig()
	if userCfg != nil && userCfg.GetMurmuring() != "" {
		return NormalizeMurmuring(userCfg.GetMurmuring())
	}
	return fallback
}


// MurmuringEnabled returns true if auto-murmuring is active for the project.
func MurmuringEnabled(projectRoot string) bool {
	return ResolveMurmuring(projectRoot) == MurmuringAuto
}

// ResolveMurmurReceive determines the effective murmur receive mode.
// Priority: User config > Project config > Default ("on").
// No caching — called infrequently at whisper delivery time.
func ResolveMurmurReceive(projectRoot string) string {
	userCfg, _ := LoadUserConfig()
	if userCfg != nil && userCfg.MurmurReceive != "" {
		return NormalizeMurmurReceive(userCfg.MurmurReceive)
	}
	if projectRoot != "" {
		cfg, _ := LoadProjectConfig(projectRoot)
		if cfg != nil && cfg.MurmurReceive != "" {
			return NormalizeMurmurReceive(cfg.MurmurReceive)
		}
	}
	return MurmurReceiveOn
}

// MurmurReceiveEnabled returns true if murmur reception is active.
func MurmurReceiveEnabled(projectRoot string) bool {
	return ResolveMurmurReceive(projectRoot) == MurmurReceiveOn
}
