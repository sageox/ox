package paths

import (
	"os"
	"path/filepath"
)

// useXDGMode returns true unless OX_XDG_DISABLE is set.
// XDG Base Directory Specification compliance is the default.
// https://specifications.freedesktop.org/basedir-spec/latest/
//
// Default XDG paths:
//   - Config: ~/.config/sageox (or $XDG_CONFIG_HOME/sageox)
//   - Data:   ~/.local/share/sageox (or $XDG_DATA_HOME/sageox)
//   - Cache:  ~/.cache/sageox (or $XDG_CACHE_HOME/sageox)
//   - State:  ~/.local/state/sageox (or $XDG_STATE_HOME/sageox)
//
// Legacy mode (OX_XDG_DISABLE=1):
//   - All paths under ~/.sageox/
//
// Note: OX_XDG_ENABLE is still supported for backwards compatibility.
func useXDGMode() bool {
	// explicit disable takes precedence
	if os.Getenv("OX_XDG_DISABLE") != "" {
		return false
	}
	// legacy enable flag still works
	if os.Getenv("OX_XDG_ENABLE") != "" {
		return true
	}
	// XDG is now the default
	return true
}

// xdgConfigHome returns the XDG config directory.
// Respects XDG_CONFIG_HOME, defaults to ~/.config
func xdgConfigHome() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return xdg
	}
	home := getHomeDir()
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".config")
}

// xdgDataHome returns the XDG data directory.
// Respects XDG_DATA_HOME, defaults to ~/.local/share
func xdgDataHome() string {
	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
		return xdg
	}
	home := getHomeDir()
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".local", "share")
}

// xdgCacheHome returns the XDG cache directory.
// Respects XDG_CACHE_HOME, defaults to ~/.cache
func xdgCacheHome() string {
	if xdg := os.Getenv("XDG_CACHE_HOME"); xdg != "" {
		return xdg
	}
	home := getHomeDir()
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".cache")
}

// xdgStateHome returns the XDG state directory.
// Respects XDG_STATE_HOME, defaults to ~/.local/state. A relative value is
// ignored, as the XDG spec requires (an unexpanded "~/..." from pam_env, say).
func xdgStateHome() string {
	if xdg := os.Getenv("XDG_STATE_HOME"); filepath.IsAbs(xdg) {
		return xdg
	}
	home := getHomeDir()
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".local", "state")
}

// xdgRuntimeDir returns the XDG runtime directory for ephemeral files.
// Respects XDG_RUNTIME_DIR, falls back to /tmp
// This is used for daemon sockets and locks that should not persist across reboots.
func xdgRuntimeDir() string {
	if xdg := os.Getenv("XDG_RUNTIME_DIR"); xdg != "" {
		return xdg
	}
	return os.TempDir()
}
